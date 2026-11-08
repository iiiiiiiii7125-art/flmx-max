package pht

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-gost/core/common/bufpool"
	"github.com/go-gost/core/logger"
	xnet "github.com/go-gost/x/internal/net"
	"github.com/go-gost/x/internal/util/eventlog"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/rs/xid"
)

const (
	defaultBacklog        = 128
	defaultReadBufferSize = 32 * 1024
	defaultReadTimeout    = 10 * time.Second

	// defaultConnTTL bounds how long an authorized connection may
	// sit in the server's table without any push or pull activity
	// before it is reaped (F02). The value matches the FIX-1
	// family no-progress window (300s); a live pht client issues a
	// pull at least once per readTimeout (10s), so the margin is
	// wide, while an authorize-then-abandon client is fully
	// reaped within one window.
	defaultConnTTL = 300 * time.Second
)

type serverOptions struct {
	authorizePath  string
	pushPath       string
	pullPath       string
	backlog        int
	tlsEnabled     bool
	tlsConfig      *tls.Config
	readBufferSize int
	readTimeout    time.Duration
	connTTL        time.Duration
	mptcp          bool
	logger         logger.Logger
}

type ServerOption func(opts *serverOptions)

func PathServerOption(authorizePath, pushPath, pullPath string) ServerOption {
	return func(opts *serverOptions) {
		opts.authorizePath = authorizePath
		opts.pullPath = pullPath
		opts.pushPath = pushPath
	}
}

func BacklogServerOption(backlog int) ServerOption {
	return func(opts *serverOptions) {
		opts.backlog = backlog
	}
}

func TLSConfigServerOption(tlsConfig *tls.Config) ServerOption {
	return func(opts *serverOptions) {
		opts.tlsConfig = tlsConfig
	}
}

func EnableTLSServerOption(enable bool) ServerOption {
	return func(opts *serverOptions) {
		opts.tlsEnabled = enable
	}
}

func ReadBufferSizeServerOption(n int) ServerOption {
	return func(opts *serverOptions) {
		opts.readBufferSize = n
	}
}

func ReadTimeoutServerOption(timeout time.Duration) ServerOption {
	return func(opts *serverOptions) {
		opts.readTimeout = timeout
	}
}

// ConnTTLServerOption sets how long an authorized connection may
// be inactive (no push, no pull) before the server reaps it.
func ConnTTLServerOption(ttl time.Duration) ServerOption {
	return func(opts *serverOptions) {
		opts.connTTL = ttl
	}
}

func MPTCPServerOption(mptcp bool) ServerOption {
	return func(opts *serverOptions) {
		opts.mptcp = mptcp
	}
}

func LoggerServerOption(logger logger.Logger) ServerOption {
	return func(opts *serverOptions) {
		opts.logger = logger
	}
}

// connEntry is one authorized client connection held by the
// server. last is the unix-nano instant of its last activity
// (authorization, or a push/pull request); the sweeper reaps
// entries idle for longer than the configured TTL (F02 — this
// replaces the long-standing "remove stale clients" TODO: entries
// were previously only deleted on push/pull error paths, so an
// authorize-then-abandon client, and any entry whose pull ended
// on EOF, stayed in the table forever).
type connEntry struct {
	conn net.Conn
	last atomic.Int64
}

func newConnEntry(conn net.Conn) *connEntry {
	e := &connEntry{conn: conn}
	e.touch()
	return e
}

func (e *connEntry) touch() {
	e.last.Store(time.Now().UnixNano())
}

func (e *connEntry) idleFor() time.Duration {
	return time.Since(time.Unix(0, e.last.Load()))
}

type Server struct {
	addr        net.Addr
	httpServer  *http.Server
	http3Server *http3.Server
	cqueue      chan net.Conn
	conns       sync.Map
	closed      chan struct{}

	options serverOptions

	// events aggregates the per-request queue-full discard and
	// push-error logging (P1-03 family): both fire at client-
	// controlled rate.
	events    *eventlog.Aggregator
	queueFull *eventlog.Counter
	reqErrs   *eventlog.Counter
}

func NewServer(addr string, opts ...ServerOption) *Server {
	var options serverOptions
	for _, opt := range opts {
		opt(&options)
	}
	if options.backlog <= 0 {
		options.backlog = defaultBacklog
	}
	if options.readBufferSize <= 0 {
		options.readBufferSize = defaultReadBufferSize
	}
	if options.readTimeout <= 0 {
		options.readTimeout = defaultReadTimeout
	}

	events := eventlog.New(options.logger, eventlog.DefaultInterval)
	s := &Server{
		httpServer: &http.Server{
			Addr:              addr,
			ReadHeaderTimeout: 30 * time.Second,
		},
		cqueue:  make(chan net.Conn, options.backlog),
		closed:  make(chan struct{}),
		options: options,
		events:  events,
		queueFull: events.DetailCounter(
			"pht server: %d connection(s) discarded (connection queue is full), last client %s, %d total"),
		reqErrs: events.DetailCounter(
			"pht server: %d request error(s), last: %s, %d total"),
	}

	mux := http.NewServeMux()
	mux.HandleFunc(options.authorizePath, s.handleAuthorize)
	mux.HandleFunc(options.pushPath, s.handlePush)
	mux.HandleFunc(options.pullPath, s.handlePull)
	s.httpServer.Handler = mux

	s.startSweeper()

	return s
}

func NewHTTP3Server(addr string, quicConfig *quic.Config, opts ...ServerOption) *Server {
	var options serverOptions
	for _, opt := range opts {
		opt(&options)
	}
	if options.backlog <= 0 {
		options.backlog = defaultBacklog
	}
	if options.readBufferSize <= 0 {
		options.readBufferSize = defaultReadBufferSize
	}
	if options.readTimeout <= 0 {
		options.readTimeout = defaultReadTimeout
	}

	events := eventlog.New(options.logger, eventlog.DefaultInterval)
	s := &Server{
		http3Server: &http3.Server{
			Addr:       addr,
			TLSConfig:  options.tlsConfig,
			QUICConfig: quicConfig,
		},
		cqueue:  make(chan net.Conn, options.backlog),
		closed:  make(chan struct{}),
		options: options,
		events:  events,
		queueFull: events.DetailCounter(
			"pht server: %d connection(s) discarded (connection queue is full), last client %s, %d total"),
		reqErrs: events.DetailCounter(
			"pht server: %d request error(s), last: %s, %d total"),
	}

	mux := http.NewServeMux()
	mux.HandleFunc(options.authorizePath, s.handleAuthorize)
	mux.HandleFunc(options.pushPath, s.handlePush)
	mux.HandleFunc(options.pullPath, s.handlePull)
	s.http3Server.Handler = mux

	s.startSweeper()

	return s
}

func (s *Server) ListenAndServe() error {
	if s.http3Server != nil {
		network := "udp"
		if xnet.IsIPv4(s.http3Server.Addr) {
			network = "udp4"
		}
		addr, err := net.ResolveUDPAddr(network, s.http3Server.Addr)
		if err != nil {
			return err
		}

		s.addr = addr
		return s.http3Server.ListenAndServe()
	}

	network := "tcp"
	if xnet.IsIPv4(s.httpServer.Addr) {
		network = "tcp4"
	}

	lc := net.ListenConfig{}
	if s.options.mptcp {
		lc.SetMultipathTCP(true)
		s.options.logger.Debugf("mptcp enabled: %v", lc.MultipathTCP())
	}
	ln, err := lc.Listen(context.Background(), network, s.httpServer.Addr)
	if err != nil {
		s.options.logger.Error(err)
		return err
	}

	s.addr = ln.Addr()
	if s.options.tlsEnabled {
		s.httpServer.TLSConfig = s.options.tlsConfig
		ln = tls.NewListener(ln, s.options.tlsConfig)
	}

	return s.httpServer.Serve(ln)
}

func (s *Server) Accept() (conn net.Conn, err error) {
	select {
	case conn = <-s.cqueue:
	case <-s.closed:
		err = http.ErrServerClosed
	}
	return
}

func (s *Server) Close() error {
	s.events.Flush()
	select {
	case <-s.closed:
		return http.ErrServerClosed
	default:
		close(s.closed)

		// F02: drain the connection table. Close previously left
		// every authorized entry (and its pipe end) behind.
		s.conns.Range(func(key, value any) bool {
			s.conns.Delete(key)
			value.(*connEntry).conn.Close()
			return true
		})

		if s.http3Server != nil {
			return s.http3Server.Close()
		}
		return s.httpServer.Close()
	}
}

// connTTL resolves the configured entry TTL, applying the
// default when the option was left unset.
func (s *Server) connTTL() time.Duration {
	if s.options.connTTL > 0 {
		return s.options.connTTL
	}
	return defaultConnTTL
}

// startSweeper launches the stale-connection reaper: ONE
// goroutine per Server (never per connection), ticking at a
// third of the TTL and stopping when the server closes. A
// background reaper is what makes the TTL a time bound rather
// than a next-request bound: with lazy reaping alone, entries on
// an otherwise idle server would sit until some unrelated
// request happened by.
func (s *Server) startSweeper() {
	interval := s.connTTL() / 3
	if interval <= 0 {
		return
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				s.reapStaleConns()
			case <-s.closed:
				return
			}
		}
	}()
}

// reapStaleConns deletes and closes every entry that has seen no
// push or pull activity for longer than the TTL. Closing the
// pipe end also unblocks any pull in flight on it and delivers
// EOF to the accepted side, so the whole tunnel is reaped, not
// just the table entry.
func (s *Server) reapStaleConns() {
	ttl := s.connTTL()
	s.conns.Range(func(key, value any) bool {
		e := value.(*connEntry)
		if e.idleFor() > ttl && s.conns.CompareAndDelete(key, value) {
			e.conn.Close()
		}
		return true
	})
}

func (s *Server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	if s.options.logger.IsLevelEnabled(logger.TraceLevel) {
		dump, _ := httputil.DumpRequest(r, false)
		s.options.logger.Trace(string(dump))
	} else if s.options.logger.IsLevelEnabled(logger.DebugLevel) {
		s.options.logger.Debugf("%s %s", r.Method, r.RequestURI)
	}

	raddr, _ := net.ResolveTCPAddr("tcp", r.RemoteAddr)
	if raddr == nil {
		raddr = &net.TCPAddr{}
	}

	// connection id
	cid := xid.New().String()

	c1, c2 := net.Pipe()
	c := &serverConn{
		Conn:       c1,
		localAddr:  s.addr,
		remoteAddr: raddr,
	}

	select {
	case s.cqueue <- c:
	default:
		c.Close()
		s.queueFull.NoteDetail(r.RemoteAddr)
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}

	w.Write([]byte(fmt.Sprintf("token=%s", cid)))
	s.conns.Store(cid, newConnEntry(c2))
}

func (s *Server) handlePush(w http.ResponseWriter, r *http.Request) {
	if s.options.logger.IsLevelEnabled(logger.TraceLevel) {
		dump, _ := httputil.DumpRequest(r, false)
		s.options.logger.Trace(string(dump))
	} else if s.options.logger.IsLevelEnabled(logger.DebugLevel) {
		s.options.logger.Debugf("%s %s", r.Method, r.RequestURI)
	}

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	cid := r.Form.Get("token")
	v, ok := s.conns.Load(cid)
	if !ok {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	entry := v.(*connEntry)
	entry.touch()
	conn := entry.conn

	br := bufio.NewReader(r.Body)
	data, err := br.ReadString('\n')
	if err != nil {
		if err != io.EOF {
			s.reqErrs.NoteErr(err)
			w.WriteHeader(http.StatusPartialContent)
		}
		conn.Close()
		s.conns.Delete(cid)
		return
	}

	data = strings.TrimSuffix(data, "\n")
	if len(data) == 0 {
		return
	}

	b, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		s.reqErrs.NoteErr(err)
		s.conns.Delete(cid)
		conn.Close()
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	defer conn.SetWriteDeadline(time.Time{})

	if _, err := conn.Write(b); err != nil {
		s.reqErrs.NoteErr(err)
		s.conns.Delete(cid)
		conn.Close()
		w.WriteHeader(http.StatusGone)
	}
}

func (s *Server) handlePull(w http.ResponseWriter, r *http.Request) {
	if s.options.logger.IsLevelEnabled(logger.TraceLevel) {
		dump, _ := httputil.DumpRequest(r, false)
		s.options.logger.Trace(string(dump))
	} else if s.options.logger.IsLevelEnabled(logger.DebugLevel) {
		s.options.logger.Debugf("%s %s", r.Method, r.RequestURI)
	}

	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	cid := r.Form.Get("token")
	v, ok := s.conns.Load(cid)
	if !ok {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	entry := v.(*connEntry)
	entry.touch()
	conn := entry.conn

	w.WriteHeader(http.StatusOK)
	if fw, ok := w.(http.Flusher); ok {
		fw.Flush()
	}

	b := bufpool.Get(s.options.readBufferSize)
	defer bufpool.Put(b)

	for {
		conn.SetReadDeadline(time.Now().Add(s.options.readTimeout))
		n, err := conn.Read(b)
		if n > 0 {
			bw := bufio.NewWriter(w)
			bw.WriteString(base64.StdEncoding.EncodeToString(b[:n]))
			bw.WriteString("\n")
			if err := bw.Flush(); err != nil {
				return
			}
			if fw, ok := w.(http.Flusher); ok {
				fw.Flush()
			}
		}
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				b[0] = '\n' // no data
				w.Write(b[:1])
			} else if errors.Is(err, io.EOF) {
				// Server connection closed: the accepted side
				// is gone, so the tunnel is over. This path
				// previously returned without deleting the
				// entry, one of the F02 only-in-never-out
				// shapes; delete it now instead of leaving it
				// for the sweeper.
				s.conns.Delete(cid)
				conn.Close()
			} else {
				if !errors.Is(err, io.ErrClosedPipe) {
					s.options.logger.Error(err)
				}
				s.conns.Delete(cid)
				conn.Close()
			}
			return
		}
	}
}
