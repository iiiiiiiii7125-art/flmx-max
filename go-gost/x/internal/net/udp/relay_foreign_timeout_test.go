package udp

import (
	"context"
	"errors"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gost/x/internal/idle"
)

// F-212-C same-family residue (LOOKBACK-final-212 S2-1):
// readDatagram kept the pre-fix predicate "window off ⇒ a timeout
// is always a resumable wake", so a foreign fatal timeout on one
// leg of the relay was swallowed and the association never
// unwound. The live shape: a stream leg (UDP-over-TCP) whose peer
// was declared dead by the kernel keepalive stamped on
// agent-owned sockets — the read on that leg fails with a
// timeout-class error (ETIMEDOUT) that the relay itself never
// armed (with the window off it arms no deadline at all).
//
// foreignTimeoutPacketConn models the dead leg: every ReadFrom
// fails at once with a timeout-class error, forever.
type foreignTimeoutPacketConn struct {
	closed atomic.Bool
}

func (c *foreignTimeoutPacketConn) ReadFrom([]byte) (int, net.Addr, error) {
	return 0, nil, &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}
}
func (c *foreignTimeoutPacketConn) WriteTo(b []byte, _ net.Addr) (int, error) {
	return len(b), nil
}
func (c *foreignTimeoutPacketConn) Close() error {
	c.closed.Store(true)
	return nil
}
func (c *foreignTimeoutPacketConn) LocalAddr() net.Addr             { return fakeAddr("127.0.0.1:0/dead") }
func (c *foreignTimeoutPacketConn) SetDeadline(time.Time) error     { return nil }
func (c *foreignTimeoutPacketConn) SetReadDeadline(time.Time) error { return nil }
func (c *foreignTimeoutPacketConn) SetWriteDeadline(time.Time) error {
	return nil
}

func TestRelayForeignTimeoutUnwindsWhenWindowOff(t *testing.T) {
	old := idle.Get()
	idle.Set(0)
	defer idle.Set(old)

	dead := &foreignTimeoutPacketConn{}
	// The surviving leg: a silent endpoint that blocks in
	// ReadFrom until it is closed.
	pc2, _ := wireIdlePair("pc2", "peer2")

	r := NewRelay(dead, pc2)
	done := make(chan error, 1)
	go func() { done <- r.Run(context.Background()) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run returned nil for an association whose leg died")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run never returned: foreign timeout swallowed as a resumable wake with the window off (S2-1)")
	}
	if !dead.closed.Load() {
		t.Fatal("dead leg was not closed on unwind")
	}
	if !pc2.isClosed() {
		t.Fatal("surviving leg was not closed on unwind — the association is not reclaimed whole")
	}
}

// Guard: with the window ARMED, a foreign timeout-class error
// still funnels into the stall judge (readDatagram cannot tell it
// apart from its own deadline wake), and the association ends as
// an idle teardown once the window elapses — the window-off fix
// must not change armed-window semantics.
func TestRelayForeignTimeoutArmedWindowStillStallsOut(t *testing.T) {
	dead := &foreignTimeoutPacketConn{}
	pc2, _ := wireIdlePair("pc2", "peer2")

	r := NewRelay(dead, pc2).WithIdleTimeout(300 * time.Millisecond)
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- r.Run(context.Background()) }()

	select {
	case err := <-done:
		if !errors.Is(err, errIdleTimeout) {
			t.Fatalf("Run error = %v, want errIdleTimeout once the armed window elapses", err)
		}
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Fatalf("armed-window teardown took %v, want ≈300ms window", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("armed-window association with a dead leg never stalled out")
	}
	if !pc2.isClosed() {
		t.Fatal("surviving leg was not closed by the armed-window teardown")
	}
}
