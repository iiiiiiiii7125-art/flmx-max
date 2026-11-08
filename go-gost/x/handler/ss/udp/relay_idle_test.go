package ss

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/go-gost/core/handler"
	xlogger "github.com/go-gost/x/logger"
	xrecorder "github.com/go-gost/x/recorder"
)

// Rescan regression (F07 family): relayPacket's hand-rolled copy
// loops had no no-progress bound, so in UDP-over-TCP mode a
// silently dead client pinned both goroutines and the upstream
// association forever. A fully silent association must now be
// reaped within one window; a trickle in either direction keeps
// it alive.

// ssIdlePacketConn is an in-memory PacketConn pair endpoint with
// real deadline semantics (the sandbox blocks unconnected UDP
// sendto, and the fake exercises exactly the surface relayPacket
// relies on).
type ssIdlePacketConn struct {
	rx     chan ssIdlePacket
	peer   *ssIdlePacketConn
	closed chan struct{}
	once   sync.Once

	mu        sync.Mutex
	rdeadline time.Time
	dlChanged chan struct{}
}

type ssIdlePacket struct {
	data []byte
	addr net.Addr
}

func wireSSIdlePair() (a, b *ssIdlePacketConn) {
	a = &ssIdlePacketConn{rx: make(chan ssIdlePacket, 64), closed: make(chan struct{}), dlChanged: make(chan struct{})}
	b = &ssIdlePacketConn{rx: make(chan ssIdlePacket, 64), closed: make(chan struct{}), dlChanged: make(chan struct{})}
	a.peer, b.peer = b, a
	return a, b
}

func (c *ssIdlePacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
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
		case pkt := <-c.rx:
			if timer != nil {
				timer.Stop()
			}
			return copy(b, pkt.data), pkt.addr, nil
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

func (c *ssIdlePacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	select {
	case <-c.closed:
		return 0, net.ErrClosed
	default:
	}
	data := make([]byte, len(b))
	copy(data, b)
	select {
	case c.peer.rx <- ssIdlePacket{data: data, addr: ssFakeAddr("203.0.113.1:53")}:
		return len(b), nil
	case <-c.peer.closed:
		return 0, net.ErrClosed
	}
}

func (c *ssIdlePacketConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func (c *ssIdlePacketConn) isClosed() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

func (c *ssIdlePacketConn) LocalAddr() net.Addr              { return ssFakeAddr("127.0.0.1:0") }
func (c *ssIdlePacketConn) SetDeadline(t time.Time) error    { return c.SetReadDeadline(t) }
func (c *ssIdlePacketConn) SetWriteDeadline(time.Time) error { return nil }

func (c *ssIdlePacketConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.rdeadline = t
	close(c.dlChanged)
	c.dlChanged = make(chan struct{})
	c.mu.Unlock()
	return nil
}

func setSSRelayWindow(t *testing.T, d time.Duration) {
	t.Helper()
	old := ssRelayIdleTimeout
	ssRelayIdleTimeout = d
	t.Cleanup(func() { ssRelayIdleTimeout = old })
}

func TestRelayPacketReapsSilentAssociation(t *testing.T) {
	setSSRelayWindow(t, 300*time.Millisecond)

	h := &ssuHandler{options: handler.Options{}}
	pc1, _ := wireSSIdlePair()
	pc2, _ := wireSSIdlePair()

	done := make(chan error, 1)
	go func() {
		done <- h.relayPacket(context.Background(), pc1, pc2,
			&xrecorder.HandlerRecorderObject{}, xlogger.Nop())
	}()

	select {
	case err := <-done:
		if !errors.Is(err, errSSRelayIdleTimeout) {
			t.Fatalf("relayPacket error = %v, want errSSRelayIdleTimeout", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("relayPacket did not return for a silent association")
	}
	if !pc1.isClosed() || !pc2.isClosed() {
		t.Fatal("endpoints not closed by the idle teardown")
	}
}

func TestRelayPacketTrickleKeepsAssociationAlive(t *testing.T) {
	setSSRelayWindow(t, 400*time.Millisecond)

	h := &ssuHandler{options: handler.Options{}}
	pc1, peer1 := wireSSIdlePair()
	pc2, _ := wireSSIdlePair()
	defer pc1.Close()
	defer pc2.Close()

	done := make(chan error, 1)
	go func() {
		done <- h.relayPacket(context.Background(), pc1, pc2,
			&xrecorder.HandlerRecorderObject{}, xlogger.Nop())
	}()

	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			peer1.WriteTo([]byte("ping"), pc1.LocalAddr())
			time.Sleep(100 * time.Millisecond)
		}
	}()

	select {
	case err := <-done:
		close(stop)
		t.Fatalf("relayPacket returned early under trickle traffic: %v", err)
	case <-time.After(1200 * time.Millisecond):
	}
	close(stop)

	select {
	case err := <-done:
		if !errors.Is(err, errSSRelayIdleTimeout) {
			t.Fatalf("relayPacket error = %v, want errSSRelayIdleTimeout", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("relayPacket did not reap the association after the trickle stopped")
	}
}
