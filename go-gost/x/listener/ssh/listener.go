package ssh

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/go-gost/core/limiter"
	"github.com/go-gost/core/listener"
	"github.com/go-gost/core/logger"
	md "github.com/go-gost/core/metadata"
	admission "github.com/go-gost/x/admission/wrapper"
	"github.com/go-gost/x/internal/keepalive"
	xnet "github.com/go-gost/x/internal/net"
	"github.com/go-gost/x/internal/net/proxyproto"
	"github.com/go-gost/x/internal/util/eventlog"
	ssh_util "github.com/go-gost/x/internal/util/ssh"
	climiter "github.com/go-gost/x/limiter/conn/wrapper"
	limiter_wrapper "github.com/go-gost/x/limiter/traffic/wrapper"
	metrics "github.com/go-gost/x/metrics/wrapper"
	stats "github.com/go-gost/x/observer/stats/wrapper"
	"github.com/go-gost/x/registry"
	"golang.org/x/crypto/ssh"
)

func init() {
	registry.ListenerRegistry().Register("ssh", NewListener)
}

type sshListener struct {
	net.Listener
	config  *ssh.ServerConfig
	cqueue  chan net.Conn
	errChan chan error
	logger  logger.Logger
	md      metadata
	options listener.Options

	// events aggregates the per-connection handshake-failure,
	// channel-fault and queue-full discard logging (P1-03
	// family): all fire at client-controlled rate.
	events     *eventlog.Aggregator
	queueFull  *eventlog.Counter
	handshakes *eventlog.Counter
	chanFaults *eventlog.Counter
}

func NewListener(opts ...listener.Option) listener.Listener {
	options := listener.Options{}
	for _, opt := range opts {
		opt(&options)
	}
	events := eventlog.New(options.Logger, eventlog.DefaultInterval)
	return &sshListener{
		logger:  options.Logger,
		options: options,
		events:  events,
		queueFull: events.DetailCounter(
			"ssh listener: %d connection(s) discarded (connection queue is full), last client %s, %d total"),
		handshakes: events.DetailCounter(
			"ssh listener: %d handshake failure(s), last: %s, %d total"),
		chanFaults: events.DetailCounter(
			"ssh listener: %d channel fault(s), last: %s, %d total"),
	}
}

// Close flushes pending aggregated log summaries before closing
// the embedded listener (previously promoted from net.Listener).
func (l *sshListener) Close() error {
	l.events.Flush()
	return l.Listener.Close()
}

func (l *sshListener) Init(md md.Metadata) (err error) {
	if err = l.parseMetadata(md); err != nil {
		return
	}

	network := "tcp"
	if xnet.IsIPv4(l.options.Addr) {
		network = "tcp4"
	}

	lc := net.ListenConfig{}
	keepalive.ApplyListenConfig(&lc)
	if l.md.mptcp {
		lc.SetMultipathTCP(true)
		l.logger.Debugf("mptcp enabled: %v", lc.MultipathTCP())
	}
	ln, err := lc.Listen(context.Background(), network, l.options.Addr)
	if err != nil {
		return err
	}

	ln = proxyproto.WrapListener(l.options.ProxyProtocol, ln, 10*time.Second)
	ln = metrics.WrapListener(l.options.Service, ln)
	ln = stats.WrapListener(ln, l.options.Stats)
	ln = admission.WrapListener(l.options.Admission, ln)
	ln = limiter_wrapper.WrapListener(l.options.Service, ln, l.options.TrafficLimiter)
	ln = climiter.WrapListener(l.options.ConnLimiter, ln)
	l.Listener = ln

	config := &ssh.ServerConfig{
		PasswordCallback:  ssh_util.PasswordCallback(l.options.Auther),
		PublicKeyCallback: ssh_util.PublicKeyCallback(l.md.authorizedKeys),
	}
	config.AddHostKey(l.md.signer)
	if l.options.Auther == nil && len(l.md.authorizedKeys) == 0 {
		config.NoClientAuth = true
	}

	l.config = config
	l.cqueue = make(chan net.Conn, l.md.backlog)
	l.errChan = make(chan error, 1)

	go l.listenLoop()

	return
}

func (l *sshListener) Accept() (conn net.Conn, err error) {
	var ok bool
	select {
	case conn = <-l.cqueue:
		conn = limiter_wrapper.WrapConn(
			conn,
			l.options.TrafficLimiter,
			conn.RemoteAddr().String(),
			limiter.ScopeOption(limiter.ScopeConn),
			limiter.ServiceOption(l.options.Service),
			limiter.NetworkOption(conn.LocalAddr().Network()),
			limiter.SrcOption(conn.RemoteAddr().String()),
		)
	case err, ok = <-l.errChan:
		if !ok {
			err = listener.ErrClosed
		}
	}
	return
}

func (l *sshListener) listenLoop() {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			l.logger.Error("accept:", err)
			l.errChan <- err
			close(l.errChan)
			return
		}
		go l.serveConn(conn)
	}
}

func (l *sshListener) serveConn(conn net.Conn) {
	start := time.Now()
	l.logger.Infof("%s <> %s", conn.RemoteAddr(), conn.LocalAddr())
	defer func() {
		l.logger.WithFields(map[string]any{
			"duration": time.Since(start),
		}).Infof("%s >< %s", conn.RemoteAddr(), conn.LocalAddr())
	}()

	sc, chans, reqs, err := ssh.NewServerConn(conn, l.config)
	if err != nil {
		l.handshakes.NoteErr(err)
		conn.Close()
		return
	}
	defer sc.Close()

	go ssh.DiscardRequests(reqs)
	go func() {
		for newChannel := range chans {
			// Check the type of channel
			t := newChannel.ChannelType()
			switch t {
			case ssh_util.GostSSHTunnelRequest:
				channel, requests, err := newChannel.Accept()
				if err != nil {
					l.chanFaults.NoteDetail("could not accept channel: " + err.Error())
					continue
				}

				go ssh.DiscardRequests(requests)
				cc := ssh_util.NewConn(conn, channel)
				select {
				case l.cqueue <- cc:
				default:
					l.queueFull.NoteDetail(conn.RemoteAddr().String())
					newChannel.Reject(ssh.ResourceShortage, "connection queue is full")
					cc.Close()
				}

			default:
				l.chanFaults.NoteDetail("unsupported channel type: " + t)
				newChannel.Reject(ssh.UnknownChannelType, fmt.Sprintf("unsupported channel type: %s", t))
			}
		}
	}()

	sc.Wait()
}
