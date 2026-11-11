package net

import (
	"errors"
	"io"
	"net"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// Defect (window-on mirror of F-212-C): with the idle window armed,
// EVERY timeout-class error was converted into a resumable wake —
// including a kernel timeout verdict (ETIMEDOUT) on a leg whose
// peer is already dead. The stall judge keeps the session alive as
// long as the sibling direction moves any byte within the window,
// so the copy loop resumed against the dead socket, its next Read
// failed again at once with the same error, and the leg was
// re-polled in a tight non-blocking loop: survival stayed bounded
// by the window, the CPU burn did not (a production agent pegged
// at 100% CPU). A kernel verdict is provably not a wake — it
// carries syscall.ETIMEDOUT, while a genuine expiry of an armed
// deadline reports os.ErrDeadlineExceeded — so it must reach
// copyDirection unconverted and unwind the session whole.

// kernelDeadConn models the keepalive-killed leg: every Read fails
// immediately with the kernel's ETIMEDOUT verdict.
type kernelDeadConn struct {
	reads  atomic.Int64
	closed atomic.Bool
}

func (c *kernelDeadConn) Read([]byte) (int, error) {
	c.reads.Add(1)
	return 0, &net.OpError{Op: "read", Net: "tcp", Err: &os.SyscallError{Syscall: "read", Err: syscall.ETIMEDOUT}}
}
func (c *kernelDeadConn) Write(p []byte) (int, error) { return len(p), nil }
func (c *kernelDeadConn) Close() error                { c.closed.Store(true); return nil }

// trickleConn models the surviving leg carrying a slow but real
// byte stream: one byte every few milliseconds — enough to keep
// the stall judge satisfied for the whole test window.
type trickleConn struct {
	done   chan struct{}
	closed atomic.Bool
}

func newTrickleConn() *trickleConn { return &trickleConn{done: make(chan struct{})} }

func (c *trickleConn) Read(p []byte) (int, error) {
	select {
	case <-c.done:
		return 0, io.ErrClosedPipe
	case <-time.After(10 * time.Millisecond):
	}
	if len(p) == 0 {
		return 0, nil
	}
	p[0] = 'x'
	return 1, nil
}
func (c *trickleConn) Write(p []byte) (int, error) { return len(p), nil }
func (c *trickleConn) Close() error {
	if c.closed.CompareAndSwap(false, true) {
		close(c.done)
	}
	return nil
}

func TestTransportKernelDeadLegUnwindsWhileSiblingTrickles(t *testing.T) {
	dead := &kernelDeadConn{}
	live := newTrickleConn()
	done := make(chan error, 1)
	start := time.Now()
	// A wide window: under the old conversion the sibling's trickle
	// keeps the session unstalled for the full 30s, so nothing but
	// the test's own timeout would ever stop the spin.
	go func() { done <- transport(live, dead, 30*time.Second) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Transport returned nil for a session whose leg died")
		}
		if errors.Is(err, ErrNoProgress) {
			t.Fatalf("Transport blamed the idle window (%v); the leg's kernel verdict must surface instead", err)
		}
		if !errors.Is(err, syscall.ETIMEDOUT) {
			t.Fatalf("Transport error = %v, want the dead leg's ETIMEDOUT", err)
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("unwind took %v; the dead leg was re-polled before the session gave up", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("session never unwound: kernel-dead leg kept resuming while the sibling trickled (dead-leg reads so far: %d)", dead.reads.Load())
	}
	if n := dead.reads.Load(); n > 100 {
		t.Fatalf("dead leg Read called %d times — busy spin, want a single failing read then unwind", n)
	}
	if !dead.closed.Load() {
		t.Fatal("dead leg was not closed on unwind")
	}
	if !live.closed.Load() {
		t.Fatal("surviving leg was not closed on unwind")
	}
}

// stubRW is a scripted endpoint for the trackedRW credit gate.
type stubRW struct {
	readN    int
	readErr  error
	writeN   int
	writeErr error
}

func (s *stubRW) Read([]byte) (int, error)  { return s.readN, s.readErr }
func (s *stubRW) Write([]byte) (int, error) { return s.writeN, s.writeErr }

// The tracker credits progress strictly on bytes reported moved;
// an endpoint whose write moved nothing (blocked, errored) must
// leave the stamp untouched — this is the gate the relay tcpConn
// count fix feeds (a forged len(b) used to sail through it).
func TestTrackedRWCreditsProgressOnlyForMovedBytes(t *testing.T) {
	act := newSessionActivity(time.Minute)
	stamp := act.last.Load()

	time.Sleep(5 * time.Millisecond)
	blocked := &trackedRW{rw: &stubRW{writeN: 0, writeErr: errReadTimeout}, act: act}
	if _, err := blocked.Write([]byte("hello")); err == nil {
		t.Fatal("stub write was expected to report its error")
	}
	if got := act.last.Load(); got != stamp {
		t.Fatal("a zero-byte write refreshed the progress stamp")
	}

	blockedRead := &trackedRW{rw: &stubRW{readN: 0, readErr: errReadTimeout}, act: act}
	if _, err := blockedRead.Read(make([]byte, 8)); err == nil {
		t.Fatal("stub read was expected to report its error")
	}
	if got := act.last.Load(); got != stamp {
		t.Fatal("a zero-byte read refreshed the progress stamp")
	}

	time.Sleep(5 * time.Millisecond)
	moving := &trackedRW{rw: &stubRW{writeN: 3}, act: act}
	if _, err := moving.Write([]byte("hello")); err != nil {
		t.Fatalf("moving write error = %v, want nil", err)
	}
	if got := act.last.Load(); got <= stamp {
		t.Fatal("a write that moved bytes did not refresh the progress stamp")
	}
}
