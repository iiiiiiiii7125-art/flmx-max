package v5

import (
	"context"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/go-gost/core/chain"
	xlogger "github.com/go-gost/x/logger"
	xrecorder "github.com/go-gost/x/recorder"
)

// F06 regression: handleUDP waited on the control connection with
// a bare io.Copy, so a client whose application died while its
// kernel kept the TCP connection up (no FIN, no RST, no data)
// pinned the handler, the control connection, the UDP socket and
// the relay goroutines forever. The association must now be reaped
// once the UDP leg has been silent for the no-progress window —
// and a normal client close must still end it immediately.

// silentPacketConn is a PacketConn that never delivers a datagram
// and honours read deadlines, like a real upstream UDP association
// whose peer went silent.
type silentPacketConn struct {
	closed    chan struct{}
	closeOnce sync.Once

	mu        sync.Mutex
	rdeadline time.Time
	dlChanged chan struct{}
}

func newSilentPacketConn() *silentPacketConn {
	return &silentPacketConn{
		closed:    make(chan struct{}),
		dlChanged: make(chan struct{}),
	}
}

func (c *silentPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
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
				return 0, nil, os.ErrDeadlineExceeded
			}
			timer = time.NewTimer(d)
			timeout = timer.C
		}
		select {
		case <-c.closed:
			if timer != nil {
				timer.Stop()
			}
			return 0, nil, net.ErrClosed
		case <-timeout:
			return 0, nil, os.ErrDeadlineExceeded
		case <-changed:
			if timer != nil {
				timer.Stop()
			}
		}
	}
}

func (c *silentPacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	select {
	case <-c.closed:
		return 0, net.ErrClosed
	default:
		return len(b), nil
	}
}

// The Router.Dial contract returns a net.Conn that also implements
// net.PacketConn (the shape of a real UDP association conn).
func (c *silentPacketConn) Read(b []byte) (int, error) {
	n, _, err := c.ReadFrom(b)
	return n, err
}

func (c *silentPacketConn) Write(b []byte) (int, error) {
	return c.WriteTo(b, c.LocalAddr())
}

func (c *silentPacketConn) RemoteAddr() net.Addr { return c.LocalAddr() }

func (c *silentPacketConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func (c *silentPacketConn) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}
}

func (c *silentPacketConn) SetDeadline(t time.Time) error { return c.SetReadDeadline(t) }

func (c *silentPacketConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.rdeadline = t
	close(c.dlChanged)
	c.dlChanged = make(chan struct{})
	c.mu.Unlock()
	return nil
}

func (c *silentPacketConn) SetWriteDeadline(time.Time) error { return nil }

func (c *silentPacketConn) isClosed() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

type stubRouter struct {
	pc *silentPacketConn
}

func (r stubRouter) Options() *chain.RouterOptions { return &chain.RouterOptions{} }

func (r stubRouter) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	return r.pc, nil
}

func (r stubRouter) Bind(ctx context.Context, network, address string, opts ...chain.BindOption) (net.Listener, error) {
	return nil, net.ErrClosed
}

func tcpPair(t *testing.T) (client, server net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c
		}
	}()
	client, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	server = <-accepted
	t.Cleanup(func() {
		client.Close()
		server.Close()
	})
	return client, server
}

func setIdleWindow(t *testing.T, d time.Duration) {
	t.Helper()
	old := udpAssociationIdleTimeout
	udpAssociationIdleTimeout = d
	t.Cleanup(func() { udpAssociationIdleTimeout = old })
}

func TestHandleUDPSilentClientIsReaped(t *testing.T) {
	setIdleWindow(t, 300*time.Millisecond)

	upstream := newSilentPacketConn()
	h := &socks5Handler{md: metadata{enableUDP: true}}
	h.options.Router = stubRouter{pc: upstream}

	client, server := tcpPair(t)
	ro := &xrecorder.HandlerRecorderObject{}

	done := make(chan error, 1)
	go func() {
		done <- h.handleUDP(context.Background(), server, ro, xlogger.NewLogger())
	}()

	// The client holds the control connection open and sends
	// nothing, on either leg — the "kernel alive, application
	// dead" shape. handleUDP must still return.
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("handleUDP error = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handleUDP did not return for a silent association")
	}

	// The teardown closed the control connection from the server
	// side: after the SOCKS5 success reply, the client observes
	// EOF instead of an open, silent connection.
	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	var b [16]byte
	for {
		_, err := client.Read(b[:])
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("client Read error = %v, want io.EOF (control conn closed by teardown)", err)
		}
	}
	// And the upstream association conn was closed too.
	if !upstream.isClosed() {
		t.Fatal("upstream packet conn not closed after teardown")
	}
}

func TestHandleUDPClientCloseEndsAssociation(t *testing.T) {
	// A long window proves the return is caused by the client
	// close, not by the idle timeout.
	setIdleWindow(t, 30*time.Second)

	upstream := newSilentPacketConn()
	h := &socks5Handler{md: metadata{enableUDP: true}}
	h.options.Router = stubRouter{pc: upstream}

	client, server := tcpPair(t)
	ro := &xrecorder.HandlerRecorderObject{}

	done := make(chan error, 1)
	go func() {
		done <- h.handleUDP(context.Background(), server, ro, xlogger.NewLogger())
	}()

	// Let the association establish, then close like a normal
	// client going away.
	time.Sleep(200 * time.Millisecond)
	client.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("handleUDP error = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handleUDP did not return after the client closed the control connection")
	}
}
