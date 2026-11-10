package udp

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// F2-19 attribution: with the window off, a foreign timeout must
// unwind the association but be returned verbatim — not relabelled
// errIdleTimeout. With the window armed and fully stalled, the
// teardown is labelled errIdleTimeout.

func TestRelayAttributionWindowOffForeignTimeoutVerbatim(t *testing.T) {
	dead := &foreignTimeoutPacketConn{}
	pc2, _ := wireIdlePair("pc2", "peer2")
	r := NewRelay(dead, pc2).WithIdleTimeout(-time.Second)
	done := make(chan error, 1)
	go func() { done <- r.Run(context.Background()) }()
	select {
	case err := <-done:
		if errors.Is(err, errIdleTimeout) {
			t.Fatalf("Run err = %v, want foreign timeout verbatim, not errIdleTimeout", err)
		}
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("Run err = %v, want the original deadline-exceeded error", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run never returned")
	}
	if !dead.closed.Load() || !pc2.isClosed() {
		t.Fatal("unwind must still close both endpoints")
	}
}

func TestRelayAttributionWindowArmedStalledIsIdle(t *testing.T) {
	pc1, _ := wireIdlePair("pc1", "peer1")
	pc2, _ := wireIdlePair("pc2", "peer2")
	r := NewRelay(pc1, pc2).WithIdleTimeout(300 * time.Millisecond)
	done := make(chan error, 1)
	go func() { done <- r.Run(context.Background()) }()
	select {
	case err := <-done:
		if !errors.Is(err, errIdleTimeout) {
			t.Fatalf("Run err = %v, want errIdleTimeout", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run never returned for stalled association")
	}
}
