package udp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-gost/x/internal/idle"
)

// 2.1.2 hotfix: Relay.Run without an explicit WithIdleTimeout
// override takes its window from the node-wide knob
// (internal/idle) — disabled by default, so a silent association
// is no longer reaped unless the panel opted in for the node.

func TestRelaySilentAssociationSurvivesWhenKnobOff(t *testing.T) {
	old := idle.Get()
	idle.Set(0)
	defer idle.Set(old)

	pc1, _ := wireIdlePair("pc1", "peer1")
	pc2, _ := wireIdlePair("pc2", "peer2")

	r := NewRelay(pc1, pc2)
	done := make(chan error, 1)
	go func() { done <- r.Run(context.Background()) }()

	select {
	case err := <-done:
		t.Fatalf("Run returned with the knob off: %v", err)
	case <-time.After(600 * time.Millisecond):
	}

	// Closing an endpoint still ends the relay, with the
	// endpoint's own error — not the idle error.
	pc1.Close()
	select {
	case err := <-done:
		if errors.Is(err, errIdleTimeout) {
			t.Fatalf("Run err = %v, want the endpoint error, not errIdleTimeout", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after endpoint close")
	}
}

func TestRelaySilentAssociationReapedWhenKnobOn(t *testing.T) {
	old := idle.Get()
	idle.Set(300 * time.Millisecond)
	defer idle.Set(old)

	pc1, _ := wireIdlePair("pc1", "peer1")
	pc2, _ := wireIdlePair("pc2", "peer2")

	r := NewRelay(pc1, pc2)
	done := make(chan error, 1)
	go func() { done <- r.Run(context.Background()) }()

	select {
	case err := <-done:
		if !errors.Is(err, errIdleTimeout) {
			t.Fatalf("Run err = %v, want errIdleTimeout", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("silent association not reaped within the configured window")
	}
}
