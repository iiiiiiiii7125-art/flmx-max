package udp

import (
	"errors"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-gost/core/common/bufpool"
	"github.com/go-gost/core/logger"
	"github.com/go-gost/x/internal/util/eventlog"
)

// DefaultReadQueueSize is the per-session datagram receive queue
// depth NewListener falls back to when a caller supplies a
// non-positive ReadQueueSize, and DefaultBacklog is the matching
// fallback for the new-session queue.
//
// The convergence has to happen here (same pattern as DefaultTTL,
// D3-01): the Bind paths (chain, relay and socks connectors) hand
// core's BindOptions.Backlog / UDPDataQueueSize through with no
// defaulting of their own — both are plain int fields in
// go-gost/core — so a zero used to reach newConn verbatim and build
// an unbuffered channel, where the first burst overflowed instantly
// and every excess datagram was discarded ("recv queue is full").
//
// 1024 matches the deepest existing metadata default (rudp, ftcp)
// and absorbs the zero-gap burst class seen live (200 packets from
// one source dropped ~4% at depth 128, K2) with 5x headroom, plus
// the 1000-packet burst used as the live acceptance bar. A slot
// holds one pooled read buffer; an idle session holds no buffers
// at all, and the channel itself costs 1024 pointers (~8KiB) per
// session. The packet-count depth alone is NOT the memory bound:
// see DefaultReadQueueBytes for the byte cap that backstops it
// (P1-02).
const DefaultReadQueueSize = 1024

// DefaultReadQueueBytes is the per-session byte cap on the receive
// queue: a datagram is admitted only while the queued byte total
// plus its own size stays within this budget. Before this cap the
// queue was metered by packet count only, so a stalled consumer
// plus a sender flooding maximum-size (64KiB) datagrams could pin
// 1024 x 65535B ≈ 64MiB in a single session — on a 464MB node a
// handful of such sessions could exhaust memory on their own
// (P1-02).
//
// 4MiB is chosen as follows: at the typical MTU-sized datagram
// (<=1500B) the 1024-packet depth caps the queue at ~1.5MiB, so
// the byte cap never binds for real traffic and burst absorption
// is unchanged; the cap only engages against oversized-datagram
// floods, where it cuts the worst case 16x (64MiB -> 4MiB). 4MiB
// still admits 64 maximum-size datagrams back to back, far beyond
// any legitimate in-flight window for a single UDP association,
// while keeping the per-session worst case small against the
// session counts a small node carries. The packet count remains
// as a second, independent cap; whichever is hit first drops.
const DefaultReadQueueBytes = 4 << 20

// DefaultBacklog is the new-session queue depth NewListener falls
// back to when a caller supplies a non-positive Backlog. It matches
// the metadata-layer default (listener/udp, rudp and ftcp all use
// 128).
const DefaultBacklog = 128

// Overload-path logging for this listener is aggregated through
// the shared eventlog package (P1-03): the drop paths (receive
// queue full, session queue full, temporary read errors) used to
// emit one Warn per event inside the hot read loop, so under a UDP
// flood the log rate equalled the drop rate. The three counters
// below are registered on the listener's Aggregator with the same
// summary wordings the private aggregator used; Flush on Close
// emits the trailing counts.
type ListenConfig struct {
	Addr           net.Addr
	Backlog        int
	ReadQueueSize  int
	ReadBufferSize int
	TTL            time.Duration
	Keepalive      bool
	Logger         logger.Logger
}
type listener struct {
	conn        net.PacketConn
	cqueue      chan net.Conn
	connPool    *connPool
	closed      chan struct{}
	errChan     chan error
	config      *ListenConfig
	events      *eventlog.Aggregator
	dropped     *eventlog.Counter // datagrams dropped: session receive queue full
	sessDropped *eventlog.Counter // new sessions dropped: session queue full
	readErrs    *eventlog.Counter // temporary read errors (detail: most recent error)
}

func NewListener(conn net.PacketConn, cfg *ListenConfig) net.Listener {
	if cfg == nil {
		cfg = &ListenConfig{}
	}
	if cfg.TTL <= 0 {
		// Several Bind paths hand UDPConnTTL through un-defaulted;
		// a non-positive TTL must never reach the conn pool's
		// ticker (D3-01).
		cfg.TTL = DefaultTTL
	}
	if cfg.ReadQueueSize <= 0 {
		// The same Bind paths hand UDPDataQueueSize through
		// un-defaulted; a non-positive size must never reach
		// newConn, where it would build an unbuffered channel
		// that discards every burst datagram (FIX-U).
		cfg.ReadQueueSize = DefaultReadQueueSize
	}
	if cfg.Backlog <= 0 {
		cfg.Backlog = DefaultBacklog
	}

	ln := &listener{
		conn:    conn,
		cqueue:  make(chan net.Conn, cfg.Backlog),
		closed:  make(chan struct{}),
		errChan: make(chan error, 1),
		config:  cfg,
	}
	ln.events = eventlog.New(cfg.Logger, eventlog.DefaultInterval)
	ln.dropped = ln.events.Counter("udp listener: %d datagram(s) discarded (session recv queue full), %d total")
	ln.sessDropped = ln.events.Counter("udp listener: %d new session(s) discarded (connection queue full), %d total")
	ln.readErrs = ln.events.DetailCounter("udp listener: %d temporary read error(s), last: %s, %d total")
	ln.connPool = newConnPool(cfg.TTL).WithLogger(cfg.Logger)
	go ln.listenLoop()

	return ln
}

func (ln *listener) Accept() (conn net.Conn, err error) {
	select {
	case conn = <-ln.cqueue:
		return
	case <-ln.closed:
		return nil, net.ErrClosed
	case err = <-ln.errChan:
		if err == nil {
			err = net.ErrClosed
		}
		return
	}
}

func (ln *listener) listenLoop() {
	for {
		select {
		case <-ln.closed:
			return
		default:
		}

		b := bufpool.Get(ln.config.ReadBufferSize)

		n, raddr, err := ln.conn.ReadFrom(b)
		if err != nil {
			bufpool.Put(b)
			if ln.isClosed() {
				return
			}
			// A transient read error (above all an i/o timeout)
			// must not kill the listener for good: the socket is
			// still open and later datagrams are still expected.
			// Only fatal errors are reported to Accept (F-UDP-3).
			if isTemporaryReadError(err) {
				// Aggregated, not per-event: a repeating
				// transient error must not become a log flood
				// (P1-03). Note the logger may be nil; the
				// counters still advance.
				ln.readErrs.NoteErr(err)
				if !isTimeoutError(err) {
					// Non-timeout transient errors can repeat
					// immediately; avoid a hot spin. Timeout
					// errors are rate-limited by the deadline
					// itself.
					time.Sleep(50 * time.Millisecond)
				}
				continue
			}
			ln.errChan <- err
			close(ln.errChan)
			return
		}

		c := ln.getConn(raddr)
		if c == nil {
			bufpool.Put(b)
			continue
		}

		if err := c.WriteQueue(b[:n]); err != nil {
			// The datagram is dropped, so ownership of the pooled
			// buffer stays with listenLoop: return it (D3-08).
			// Without this, the queue-full path — exactly the
			// overload condition — allocates a fresh buffer per
			// dropped packet instead of reusing the pool.
			bufpool.Put(b)
			if errors.Is(err, errQueueFull) {
				// Counted and logged in aggregated summaries,
				// never per packet (P1-03).
				ln.dropped.Note()
			} else if ln.config.Logger != nil {
				// Session closed mid-delivery: rare, not a flood
				// vector; keep the immediate log.
				ln.config.Logger.Warn("data discarded: ", err)
			}
		}
	}
}

func (ln *listener) isClosed() bool {
	select {
	case <-ln.closed:
		return true
	default:
		return false
	}
}

// isTemporaryReadError reports whether err from the listening socket is
// a transient failure after which reading may continue, as opposed to a
// fatal one (e.g. the socket is gone) that Accept must learn about.
func isTemporaryReadError(err error) bool {
	if errors.Is(err, net.ErrClosed) {
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return ne.Timeout() || ne.Temporary()
	}
	return false
}

func isTimeoutError(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) {
		return ne.Timeout()
	}
	return false
}

func (ln *listener) Addr() net.Addr {
	if ln.config.Addr != nil {
		return ln.config.Addr
	}
	return ln.conn.LocalAddr()
}

func (ln *listener) Close() error {
	select {
	case <-ln.closed:
	default:
		close(ln.closed)
		ln.conn.Close()
		ln.connPool.Close()
		ln.events.Flush()
	}

	return nil
}

func (ln *listener) getConn(raddr net.Addr) *conn {
	c, ok := ln.connPool.Get(raddr.String())
	if ok && !c.isClosed() {
		return c
	}

	c = newConn(ln.conn, ln.Addr(), raddr, ln.config.ReadQueueSize, ln.config.Keepalive)
	select {
	case ln.cqueue <- c:
		ln.connPool.Set(raddr.String(), c)
		return c
	default:
		c.Close()
		// Aggregated with the other overload drops (P1-03):
		// under a new-session flood this fired once per client.
		ln.sessDropped.Note()
		return nil
	}
}

// conn is a server side connection for UDP client peer, it implements net.Conn and net.PacketConn.
//
// Two read faces are offered, and each association consistently uses
// one of them:
//
//   - ReadFrom is the packet face: every call returns exactly one
//     queued datagram, truncated to the caller's buffer per ordinary
//     UDP semantics. Packet protocols (SOCKS5 UDP, Shadowsocks UDP,
//     plain forwarding) use this face.
//   - Read is the stream face: bytes are served from an internal
//     buffer that holds the unread remainder of the current datagram,
//     so a stream-style consumer issuing small reads (io.ReadFull of a
//     header, then of a length-prefixed body, as the relay handshake
//     and B-frame layers do) never loses the tail of a datagram that
//     carried more than the requested bytes (F-UDP-2, U1-003). The
//     next datagram is dequeued only once the buffer is drained.
//
// Deadlines are session-local. The embedded PacketConn is the shared
// listening socket of the whole listener: forwarding a session's
// SetReadDeadline to it (the previous behaviour, via method promotion)
// armed one session's handshake timeout on every association and on
// the accept loop itself, and a failed handshake never cleared it
// (F-UDP-3). The deadline methods below therefore never touch the
// shared socket; a session read deadline is enforced by readDatagram
// against this session's own queue only.
// errQueueFull is returned by WriteQueue when a datagram is
// dropped: the session receive queue is at its packet-count depth
// or its byte budget (P1-02).
var errQueueFull = errors.New("recv queue is full")

type conn struct {
	net.PacketConn
	localAddr  net.Addr
	remoteAddr net.Addr
	rc         chan []byte // data receive queue
	idle       int32       // indicate the connection is idle
	closed     chan struct{}
	closeMutex sync.Mutex
	keepalive  bool

	// qmu guards qBytes. A dedicated mutex (not c.mu, which guards
	// the read-face state) keeps the enqueue hot path free of any
	// interaction with stream reads and deadline changes.
	qmu    sync.Mutex
	qBytes int // bytes currently held in rc

	mu        sync.Mutex    // guards rbuf and the deadline state
	rbuf      []byte        // unread remainder of the current datagram (stream face)
	rbufSrc   []byte        // pooled buffer backing rbuf, returned once drained
	rdeadline time.Time     // session-local read deadline
	wdeadline time.Time     // session-local write deadline
	dlChanged chan struct{} // closed and replaced whenever a deadline changes
}

func newConn(c net.PacketConn, laddr, remoteAddr net.Addr, queueSize int, keepalive bool) *conn {
	return &conn{
		PacketConn: c,
		localAddr:  laddr,
		remoteAddr: remoteAddr,
		rc:         make(chan []byte, queueSize),
		closed:     make(chan struct{}),
		keepalive:  keepalive,
		dlChanged:  make(chan struct{}),
	}
}

// readDatagram dequeues the next datagram for this session, honouring
// the session-local read deadline and session close.
func (c *conn) readDatagram() ([]byte, error) {
	for {
		c.mu.Lock()
		dl := c.rdeadline
		changed := c.dlChanged
		c.mu.Unlock()

		var timer *time.Timer
		var timeout <-chan time.Time
		if !dl.IsZero() {
			d := time.Until(dl)
			if d <= 0 {
				return nil, os.ErrDeadlineExceeded
			}
			timer = time.NewTimer(d)
			timeout = timer.C
		}

		select {
		case bb := <-c.rc:
			if timer != nil {
				timer.Stop()
			}
			// Release the datagram's bytes from the queue byte
			// budget at dequeue time (P1-02); the buffer itself
			// stays alive with the reader until returned to the
			// pool, but the admission budget frees up here so a
			// draining consumer immediately makes room.
			c.qmu.Lock()
			c.qBytes -= len(bb)
			c.qmu.Unlock()
			c.SetIdle(false)
			return bb, nil
		case <-c.closed:
			if timer != nil {
				timer.Stop()
			}
			return nil, net.ErrClosed
		case <-timeout:
			return nil, os.ErrDeadlineExceeded
		case <-changed:
			// A deadline was set or cleared while waiting:
			// recompute and wait again.
			if timer != nil {
				timer.Stop()
			}
		}
	}
}

func (c *conn) ReadFrom(b []byte) (n int, addr net.Addr, err error) {
	bb, err := c.readDatagram()
	if err != nil {
		return 0, nil, err
	}

	n = copy(b, bb)
	bufpool.Put(bb)

	addr = c.remoteAddr

	return
}

func (c *conn) Read(b []byte) (n int, err error) {
	if len(b) == 0 {
		return 0, nil
	}

	c.mu.Lock()
	if len(c.rbuf) > 0 {
		n = copy(b, c.rbuf)
		c.rbuf = c.rbuf[n:]
		if len(c.rbuf) == 0 && c.rbufSrc != nil {
			bufpool.Put(c.rbufSrc)
			c.rbufSrc = nil
		}
		c.mu.Unlock()
		return n, nil
	}
	c.mu.Unlock()

	bb, err := c.readDatagram()
	if err != nil {
		return 0, err
	}

	n = copy(b, bb)
	if n < len(bb) {
		// Retain the unread remainder for the next Read instead of
		// discarding it: stream consumers read datagrams in small
		// pieces (F-UDP-2). bb is owned by this session now, so it
		// must not go back to the pool while rbuf references it.
		c.mu.Lock()
		c.rbuf = bb[n:]
		c.rbufSrc = bb
		c.mu.Unlock()
		return n, nil
	}
	bufpool.Put(bb)
	return n, nil
}

func (c *conn) WriteTo(b []byte, addr net.Addr) (n int, err error) {
	if !c.keepalive {
		defer c.Close()
	}
	c.mu.Lock()
	wdl := c.wdeadline
	c.mu.Unlock()
	if !wdl.IsZero() && time.Until(wdl) <= 0 {
		return 0, os.ErrDeadlineExceeded
	}
	return c.PacketConn.WriteTo(b, addr)
}

func (c *conn) Write(b []byte) (n int, err error) {
	return c.WriteTo(b, c.remoteAddr)
}

// SetDeadline sets the session-local read and write deadlines. It
// deliberately does NOT delegate to the embedded PacketConn: that is
// the listening socket shared by every session of this listener, and a
// deadline set on it fires for all of them (F-UDP-3).
func (c *conn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	c.rdeadline = t
	c.wdeadline = t
	c.broadcastDeadlineChangeLocked()
	c.mu.Unlock()
	return nil
}

func (c *conn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.rdeadline = t
	c.broadcastDeadlineChangeLocked()
	c.mu.Unlock()
	return nil
}

func (c *conn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	c.wdeadline = t
	c.broadcastDeadlineChangeLocked()
	c.mu.Unlock()
	return nil
}

// broadcastDeadlineChangeLocked wakes any goroutine blocked in
// readDatagram so it can recompute its wait against the new deadline.
// Callers must hold c.mu.
func (c *conn) broadcastDeadlineChangeLocked() {
	close(c.dlChanged)
	c.dlChanged = make(chan struct{})
}

func (c *conn) Close() error {
	c.closeMutex.Lock()
	defer c.closeMutex.Unlock()

	select {
	case <-c.closed:
	default:
		close(c.closed)
	}
	return nil
}

func (c *conn) isClosed() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

func (c *conn) LocalAddr() net.Addr {
	return c.localAddr
}

func (c *conn) RemoteAddr() net.Addr {
	return c.remoteAddr
}

func (c *conn) IsIdle() bool {
	return atomic.LoadInt32(&c.idle) > 0
}

func (c *conn) SetIdle(idle bool) {
	v := int32(0)
	if idle {
		v = 1
	}
	atomic.StoreInt32(&c.idle, v)
}

// WriteQueue enqueues one datagram for the session's consumer.
// Admission is capped twice (P1-02): by the channel's packet-count
// depth and by the DefaultReadQueueBytes byte budget, metered on
// the datagram's actual size — whichever cap is hit first drops
// the datagram with errQueueFull. The session-closed check rides
// in the same critical section so a closed session can never
// strand accounted bytes nobody will dequeue.
func (c *conn) WriteQueue(b []byte) error {
	c.qmu.Lock()
	if c.isClosed() {
		c.qmu.Unlock()
		return net.ErrClosed
	}
	if c.qBytes+len(b) > DefaultReadQueueBytes {
		c.qmu.Unlock()
		return errQueueFull
	}
	select {
	case c.rc <- b:
		c.qBytes += len(b)
		c.qmu.Unlock()
		c.SetIdle(false)
		return nil
	default:
		c.qmu.Unlock()
		return errQueueFull
	}
}
