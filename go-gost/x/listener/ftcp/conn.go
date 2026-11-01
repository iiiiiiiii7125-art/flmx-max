package ftcp

import (
	"errors"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// serverConn is a server side connection for UDP client peer, it implements net.Conn and net.PacketConn.
type serverConn struct {
	pc         net.PacketConn
	raddr      net.Addr
	rc         chan []byte // data receive queue
	fresh      int32
	closed     chan struct{}
	closeMutex sync.Mutex
	config     *serverConnConfig

	mu        sync.Mutex    // guards the deadline state
	rdeadline time.Time     // session-local read deadline
	wdeadline time.Time     // session-local write deadline
	dlChanged chan struct{} // closed and replaced whenever a deadline changes
}

type serverConnConfig struct {
	ttl     time.Duration
	qsize   int
	onClose func()
}

func newServerConn(conn net.PacketConn, raddr net.Addr, cfg *serverConnConfig) *serverConn {
	if conn == nil || raddr == nil {
		return nil
	}

	if cfg == nil {
		cfg = &serverConnConfig{}
	}
	c := &serverConn{
		pc:        conn,
		raddr:     raddr,
		rc:        make(chan []byte, cfg.qsize),
		closed:    make(chan struct{}),
		config:    cfg,
		dlChanged: make(chan struct{}),
	}
	go c.ttlWait()
	return c
}

func (c *serverConn) send(b []byte) error {
	select {
	case c.rc <- b:
		return nil
	default:
		return errors.New("queue is full")
	}
}

// Read dequeues the next datagram for this session, honouring the
// session-local read deadline and session close.
func (c *serverConn) Read(b []byte) (n int, err error) {
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
				return 0, os.ErrDeadlineExceeded
			}
			timer = time.NewTimer(d)
			timeout = timer.C
		}

		select {
		case bb := <-c.rc:
			if timer != nil {
				timer.Stop()
			}
			n = copy(b, bb)
			atomic.StoreInt32(&c.fresh, 1)
			return n, nil
		case <-c.closed:
			if timer != nil {
				timer.Stop()
			}
			return 0, errors.New("read from closed connection")
		case <-timeout:
			return 0, os.ErrDeadlineExceeded
		case <-changed:
			// A deadline was set or cleared while waiting:
			// recompute and wait again.
			if timer != nil {
				timer.Stop()
			}
		}
	}
}

func (c *serverConn) Write(b []byte) (n int, err error) {
	c.mu.Lock()
	wdl := c.wdeadline
	c.mu.Unlock()
	if !wdl.IsZero() && time.Until(wdl) <= 0 {
		return 0, os.ErrDeadlineExceeded
	}
	return c.pc.WriteTo(b, c.raddr)
}

func (c *serverConn) Close() error {
	c.closeMutex.Lock()
	defer c.closeMutex.Unlock()

	select {
	case <-c.closed:
		return errors.New("connection is closed")
	default:
		if c.config.onClose != nil {
			c.config.onClose()
		}
		close(c.closed)
	}
	return nil
}

func (c *serverConn) LocalAddr() net.Addr {
	return c.pc.LocalAddr()
}

func (c *serverConn) RemoteAddr() net.Addr {
	return c.raddr
}

// Deadlines are session-local, mirroring internal/net/udp (F-UDP-3).
// c.pc is the listening PacketConn shared by every session of this
// listener: delegating a session's SetDeadline to it (the previous
// behaviour) armed one session's handshake timeout on the shared
// socket, where it fired for all sessions and for the accept loop
// itself. The deadline methods below therefore never touch the
// shared socket; a session read deadline is enforced by Read against
// this session's own queue only.
func (c *serverConn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	c.rdeadline = t
	c.wdeadline = t
	c.broadcastDeadlineChangeLocked()
	c.mu.Unlock()
	return nil
}

func (c *serverConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.rdeadline = t
	c.broadcastDeadlineChangeLocked()
	c.mu.Unlock()
	return nil
}

func (c *serverConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	c.wdeadline = t
	c.broadcastDeadlineChangeLocked()
	c.mu.Unlock()
	return nil
}

// broadcastDeadlineChangeLocked wakes any goroutine blocked in Read
// so it can recompute its wait against the new deadline. Callers must
// hold c.mu.
func (c *serverConn) broadcastDeadlineChangeLocked() {
	close(c.dlChanged)
	c.dlChanged = make(chan struct{})
}

func (c *serverConn) ttlWait() {
	ticker := time.NewTicker(c.config.ttl)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if !atomic.CompareAndSwapInt32(&c.fresh, 1, 0) {
				c.Close()
				return
			}
		case <-c.closed:
			return
		}
	}
}
