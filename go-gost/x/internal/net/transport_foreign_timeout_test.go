package net

import (
	"errors"
	"io"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gost/x/internal/idle"
)

// F-212-C: a session must unwind whole when one leg dies, or the
// dead session pins its surviving leg (and fd, and copy goroutine)
// on the agent forever.
//
// The live shape (LIVE-3 KA-1): the peer host dies silently
// (kill -9 behind a packet drop), and the agent-side kernel
// keepalive — stamped on agent-owned sockets by the 2.1.2 wave —
// eventually declares the dial leg dead. A read blocked on that
// socket then returns a TIMEOUT-class error (ETIMEDOUT), not a
// deadline expiry Transport armed: with the idle window off (the
// default), Transport arms no deadline at all. The copy wrapper
// nevertheless converted every timeout-class error into the
// resumable deadline wake (errReadTimeout); stalled() is false by
// definition when the window is off, so the copy "resumed" against
// the dead socket forever — the client leg stayed ESTABLISHED,
// the client never saw EOF, and the agent's fd count never
// returned to baseline.
//
// timeoutDieConn models the dead leg: every Read fails at once
// with a timeout-class error, as a keepalive-killed socket does.
type timeoutDieConn struct {
	closed atomic.Bool
}

func (c *timeoutDieConn) Read([]byte) (int, error) {
	return 0, &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}
}
func (c *timeoutDieConn) Write(p []byte) (int, error) { return len(p), nil }
func (c *timeoutDieConn) Close() error               { c.closed.Store(true); return nil }

// parkConn models the surviving leg whose peer is alive but
// silent: Read blocks until the conn is closed.
type parkConn struct {
	done   chan struct{}
	closed atomic.Bool
}

func newParkConn() *parkConn { return &parkConn{done: make(chan struct{})} }

func (c *parkConn) Read([]byte) (int, error) {
	<-c.done
	return 0, io.ErrClosedPipe
}
func (c *parkConn) Write(p []byte) (int, error) { return len(p), nil }
func (c *parkConn) Close() error {
	if c.closed.CompareAndSwap(false, true) {
		close(c.done)
	}
	return nil
}

func TestTransportDeadLegTimeoutErrorUnwindsSessionWhenWindowOff(t *testing.T) {
	old := idle.Get()
	idle.Set(0)
	defer idle.Set(old)

	client := newParkConn()
	dead := &timeoutDieConn{}
	done := make(chan error, 1)
	go func() { done <- Transport(client, dead) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Transport returned nil for a session whose leg died")
		}
		if errors.Is(err, ErrNoProgress) {
			t.Fatalf("Transport blamed the idle window (%v); the window is off — the leg's own error must surface", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session never unwound: dead leg's timeout error was swallowed as a resumable wake (F-212-C)")
	}
	if !dead.closed.Load() {
		t.Fatal("dead leg was not closed on unwind")
	}
	if !client.closed.Load() {
		t.Fatal("surviving leg was not closed on unwind — the client would hang without EOF (F-212-C)")
	}
}

// Guard: with the window ARMED, a foreign timeout-class error must
// still funnel into the stall judge (the wrapper cannot tell it
// apart from Transport's own deadline), and the session must end
// as a no-progress teardown once the window elapses — the fix for
// the disabled case must not change armed-window semantics.
func TestTransportDeadLegTimeoutErrorArmedWindowStillStallsOut(t *testing.T) {
	client := newParkConn()
	dead := &timeoutDieConn{}
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- transport(client, dead, 300*time.Millisecond) }()

	select {
	case err := <-done:
		if err != ErrNoProgress {
			t.Fatalf("Transport error = %v, want ErrNoProgress once the armed window elapses", err)
		}
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Fatalf("armed-window teardown took %v, want ≈300ms window", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("armed-window session with a dead leg never stalled out")
	}
	if !client.closed.Load() {
		t.Fatal("surviving leg was not closed by the armed-window teardown")
	}
}
