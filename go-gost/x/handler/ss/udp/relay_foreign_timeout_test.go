package ss

import (
	"context"
	"errors"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gost/core/handler"
	xlogger "github.com/go-gost/x/logger"
	xrecorder "github.com/go-gost/x/recorder"
)

// F-212-C same-family residue (LOOKBACK-final-212 S2-2):
// readSSPacket kept the pre-fix predicate "window off ⇒ a
// timeout is always a resumable wake", so a foreign fatal
// timeout on one leg of an SS-UDP association was swallowed and
// the association never unwound. The live shape: a stream leg
// (UDP-over-TCP) whose peer was declared dead by the kernel
// keepalive stamped on agent-owned sockets — the read on that
// leg fails with a timeout-class error (ETIMEDOUT) that the
// relay itself never armed (with the window off it arms no
// deadline at all).
//
// ssForeignTimeoutPacketConn models the dead leg: every
// ReadFrom fails at once with a timeout-class error, forever.
type ssForeignTimeoutPacketConn struct {
	closed atomic.Bool
}

func (c *ssForeignTimeoutPacketConn) ReadFrom([]byte) (int, net.Addr, error) {
	return 0, nil, &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}
}
func (c *ssForeignTimeoutPacketConn) WriteTo(b []byte, _ net.Addr) (int, error) {
	return len(b), nil
}
func (c *ssForeignTimeoutPacketConn) Close() error {
	c.closed.Store(true)
	return nil
}
func (c *ssForeignTimeoutPacketConn) LocalAddr() net.Addr { return ssFakeAddr("127.0.0.1:0/dead") }
func (c *ssForeignTimeoutPacketConn) SetDeadline(time.Time) error {
	return nil
}
func (c *ssForeignTimeoutPacketConn) SetReadDeadline(time.Time) error { return nil }
func (c *ssForeignTimeoutPacketConn) SetWriteDeadline(time.Time) error {
	return nil
}

func TestRelayPacketForeignTimeoutUnwindsWhenWindowOff(t *testing.T) {
	setSSRelayWindow(t, -time.Second) // pin the window off

	h := &ssuHandler{options: handler.Options{}}
	dead := &ssForeignTimeoutPacketConn{}
	// The surviving leg: a silent endpoint that blocks in
	// ReadFrom until it is closed.
	pc2, _ := wireSSIdlePair()

	done := make(chan error, 1)
	go func() {
		done <- h.relayPacket(context.Background(), dead, pc2,
			&xrecorder.HandlerRecorderObject{}, xlogger.Nop())
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("relayPacket returned nil for an association whose leg died")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("relayPacket never returned: foreign timeout swallowed as a resumable wake with the window off (S2-2)")
	}
	if !dead.closed.Load() {
		t.Fatal("dead leg was not closed on unwind")
	}
	if !pc2.isClosed() {
		t.Fatal("surviving leg was not closed on unwind — the association is not reclaimed whole")
	}
}

// Guard: with the window ARMED, a foreign timeout-class error
// still funnels into the stall judge (readSSPacket cannot tell
// it apart from its own deadline wake), and the association ends
// as an idle teardown once the window elapses — the window-off
// fix must not change armed-window semantics.
func TestRelayPacketForeignTimeoutArmedWindowStillStallsOut(t *testing.T) {
	setSSRelayWindow(t, 300*time.Millisecond)

	h := &ssuHandler{options: handler.Options{}}
	dead := &ssForeignTimeoutPacketConn{}
	pc2, _ := wireSSIdlePair()

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		done <- h.relayPacket(context.Background(), dead, pc2,
			&xrecorder.HandlerRecorderObject{}, xlogger.Nop())
	}()

	select {
	case err := <-done:
		if !errors.Is(err, errSSRelayIdleTimeout) {
			t.Fatalf("relayPacket error = %v, want errSSRelayIdleTimeout once the armed window elapses", err)
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
