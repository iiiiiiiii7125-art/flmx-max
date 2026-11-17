// Package http provides HTTP helpers shared by the gost handlers.
package http

import (
	"context"
	"net"
	"sync"
)

// SingleConnListener is a net.Listener that serves exactly one connection.
//
// It exists for the per-connection http.Server pattern used by the api and
// metrics handlers: the handler gets one accepted TCP connection and must
// serve the request(s) on it, then return.
//
// The previous per-handler copies closed the connection channel in send(),
// so the server's second Accept returned net.ErrClosed immediately and
// Serve returned before the serve goroutine had run. The handler then
// called Shutdown, which marks the server as shutting down — and
// net/http's conn.serve returns without invoking the handler once the
// server is shutting down. Every request therefore got an empty reply,
// and Shutdown returned nil, so the failure was silent.
//
// The second Accept here blocks until the connection closes (or ctx is
// done), so Serve only returns after the request has been served.
type SingleConnListener struct {
	ctx   context.Context
	conn  net.Conn
	done  chan struct{}
	once  sync.Once
	mu    sync.Mutex
	taken bool
}

// NewSingleConnListener returns a listener serving conn, unblocking when
// conn closes or ctx is done.
func NewSingleConnListener(ctx context.Context, conn net.Conn) *SingleConnListener {
	l := &SingleConnListener{
		ctx:  ctx,
		done: make(chan struct{}),
	}
	l.conn = &closeNotifyConn{Conn: conn, onClose: l.signalDone}
	return l
}

func (l *SingleConnListener) signalDone() {
	l.once.Do(func() { close(l.done) })
}

// Accept returns the connection on the first call, then blocks until the
// connection closes or the context is done.
func (l *SingleConnListener) Accept() (net.Conn, error) {
	select {
	case <-l.ctx.Done():
		return nil, net.ErrClosed
	default:
	}

	l.mu.Lock()
	if l.taken {
		l.mu.Unlock()
		select {
		case <-l.done:
		case <-l.ctx.Done():
		}
		return nil, net.ErrClosed
	}
	l.taken = true
	conn := l.conn
	l.mu.Unlock()
	return conn, nil
}

// Close unblocks a pending Accept. It does not close the connection; the
// http.Server closes it when the serve goroutine finishes.
func (l *SingleConnListener) Close() error {
	l.signalDone()
	return nil
}

// Addr returns the connection's local address.
func (l *SingleConnListener) Addr() net.Addr {
	return l.conn.LocalAddr()
}

// closeNotifyConn reports the first Close to onClose.
type closeNotifyConn struct {
	net.Conn
	onClose func()
	once    sync.Once
}

func (c *closeNotifyConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.onClose)
	return err
}
