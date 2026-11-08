package udp

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"testing"
	"time"
)

// F07 regression: Relay.Run previously returned only when an
// endpoint errored or was closed, so an association whose peer went
// silent without closing (kernel alive, application dead) pinned
// both endpoints and both relay goroutines forever whenever the
// endpoint pairing had no TTL pool behind it. Run must now reap a
// fully silent association within one no-progress window, while a
// slow trickle of datagrams — progress, however slow — keeps it
// alive indefinitely (the criterion is silence, not age).
//
// The tests run on in-memory PacketConns with real deadline
// semantics: the sandbox blocks unconnected UDP sendto, and the
// fake exercises exactly the surface Run relies on (ReadFrom,
// WriteTo, SetReadDeadline, Close).

type idlePacket struct {
	data []byte
	addr net.Addr
}

type idlePacketConn struct {
	name  string
	local net.Addr
	rx    chan idlePacket
	peer  *idlePacketConn

	closed    chan struct{}
	closeOnce sync.Once

	mu        sync.Mutex
	rdeadline time.Time
	dlChanged chan struct{}
}

func newIdlePacketConn(name string) *idlePacketConn {
	return &idlePacketConn{
		name:      name,
		local:     fakeAddr("127.0.0.1:0/" + name),
		rx:        make(chan idlePacket, 64),
		closed:    make(chan struct{}),
		dlChanged: make(chan struct{}),
	}
}

// wireIdlePair returns two endpoints connected to each other: what
// one writes, the other reads.
func wireIdlePair(a, b string) (*idlePacketConn, *idlePacketConn) {
	ca, cb := newIdlePacketConn(a), newIdlePacketConn(b)
	ca.peer, cb.peer = cb, ca
	return ca, cb
}

func (c *idlePacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
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

func (c *idlePacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	select {
	case <-c.closed:
		return 0, net.ErrClosed
	default:
	}
	if c.peer == nil {
		return len(b), nil
	}
	data := make([]byte, len(b))
	copy(data, b)
	select {
	case c.peer.rx <- idlePacket{data: data, addr: c.local}:
		return len(b), nil
	case <-c.peer.closed:
		return 0, net.ErrClosed
	}
}

func (c *idlePacketConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func (c *idlePacketConn) LocalAddr() net.Addr { return c.local }

func (c *idlePacketConn) SetDeadline(t time.Time) error {
	return c.SetReadDeadline(t)
}

func (c *idlePacketConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.rdeadline = t
	close(c.dlChanged)
	c.dlChanged = make(chan struct{})
	c.mu.Unlock()
	return nil
}

func (c *idlePacketConn) SetWriteDeadline(time.Time) error { return nil }

func (c *idlePacketConn) isClosed() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

func TestRelayRunReapsSilentAssociation(t *testing.T) {
	pc1, _ := wireIdlePair("pc1", "peer1")
	pc2, _ := wireIdlePair("pc2", "peer2")

	r := NewRelay(pc1, pc2).WithIdleTimeout(300 * time.Millisecond)

	done := make(chan error, 1)
	go func() { done <- r.Run(context.Background()) }()

	select {
	case err := <-done:
		if !errors.Is(err, errIdleTimeout) {
			t.Fatalf("Run error = %v, want errIdleTimeout", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return for a silent association")
	}

	// The stall path closes both endpoints to unblock the sibling
	// direction.
	if !pc1.isClosed() {
		t.Fatal("pc1 not closed after idle teardown")
	}
	if !pc2.isClosed() {
		t.Fatal("pc2 not closed after idle teardown")
	}
}

func TestRelayRunTrickleKeepsAssociationAlive(t *testing.T) {
	for _, direction := range []string{"pc1->pc2", "pc2->pc1"} {
		t.Run(direction, func(t *testing.T) {
			pc1, peer1 := wireIdlePair("pc1", "peer1")
			pc2, peer2 := wireIdlePair("pc2", "peer2")

			var talker *idlePacketConn
			if direction == "pc1->pc2" {
				talker = peer1
			} else {
				talker = peer2
			}

			r := NewRelay(pc1, pc2).WithIdleTimeout(400 * time.Millisecond)
			done := make(chan error, 1)
			go func() { done <- r.Run(context.Background()) }()

			stop := make(chan struct{})
			go func() {
				for {
					select {
					case <-stop:
						return
					default:
					}
					talker.WriteTo([]byte("ping"), talker.peer.local)
					time.Sleep(100 * time.Millisecond)
				}
			}()

			// ~3 windows of trickle: the association must survive.
			select {
			case err := <-done:
				close(stop)
				t.Fatalf("Run returned early under trickle traffic: %v", err)
			case <-time.After(1200 * time.Millisecond):
			}
			close(stop)

			// Once the trickle stops, one silent window reaps it.
			select {
			case err := <-done:
				if !errors.Is(err, errIdleTimeout) {
					t.Fatalf("Run error = %v, want errIdleTimeout", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("Run did not reap the association after the trickle stopped")
			}
		})
	}
}

func TestRelayRunStillForwardsBothWays(t *testing.T) {
	pc1, peer1 := wireIdlePair("pc1", "peer1")
	pc2, peer2 := wireIdlePair("pc2", "peer2")

	r := NewRelay(pc1, pc2).WithIdleTimeout(5 * time.Second)
	done := make(chan error, 1)
	go func() { done <- r.Run(context.Background()) }()
	defer func() {
		pc1.Close()
		pc2.Close()
		<-done
	}()

	var b [64]byte

	peer1.WriteTo([]byte("hello"), pc1.local)
	peer2.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := peer2.ReadFrom(b[:])
	if err != nil {
		t.Fatalf("peer2 ReadFrom: %v", err)
	}
	if string(b[:n]) != "hello" {
		t.Fatalf("peer2 got %q, want %q", b[:n], "hello")
	}

	peer2.WriteTo([]byte("world"), pc2.local)
	peer1.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err = peer1.ReadFrom(b[:])
	if err != nil {
		t.Fatalf("peer1 ReadFrom: %v", err)
	}
	if string(b[:n]) != "world" {
		t.Fatalf("peer1 got %q, want %q", b[:n], "world")
	}
}
