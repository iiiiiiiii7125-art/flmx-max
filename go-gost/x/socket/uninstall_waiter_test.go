package socket

import (
	"testing"
	"time"
)

// Package K: the uninstall ack waiter registry must be bounded.
// Before the fix its only delete was the signal itself, so a
// dispatch whose response frame was never written (connection
// died mid-dispatch) left an entry — and its channel — behind
// forever.

func waiterCount(w *WebSocketReporter) int {
	w.uninstallAckMu.Lock()
	defer w.uninstallAckMu.Unlock()
	return len(w.uninstallAckWaiters)
}

func TestUninstallAckWaiterRelease(t *testing.T) {
	w := &WebSocketReporter{}
	w.registerUninstallAck("req-1")
	if n := waiterCount(w); n != 1 {
		t.Fatalf("waiters = %d, want 1", n)
	}
	w.releaseUninstallAck("req-1")
	if n := waiterCount(w); n != 0 {
		t.Fatalf("waiters = %d after release, want 0", n)
	}
	// Release is idempotent, and a late signal is a safe no-op.
	w.releaseUninstallAck("req-1")
	w.signalUninstallAck("req-1", true)
	if n := waiterCount(w); n != 0 {
		t.Fatalf("waiters = %d after late signal, want 0", n)
	}
}

func TestUninstallAckWaiterSignalStillDelivers(t *testing.T) {
	w := &WebSocketReporter{}
	ch := w.registerUninstallAck("req-2")
	w.signalUninstallAck("req-2", true)
	select {
	case got := <-ch:
		if !got {
			t.Fatal("signal delivered false, want true")
		}
	case <-time.After(time.Second):
		t.Fatal("signal not delivered")
	}
	if n := waiterCount(w); n != 0 {
		t.Fatalf("waiters = %d after signal, want 0", n)
	}
}

func TestUninstallAckWaiterStaleSweep(t *testing.T) {
	w := &WebSocketReporter{}
	w.registerUninstallAck("req-stale")
	// Backdate the stale waiter past the staleness bound.
	w.uninstallAckMu.Lock()
	waiter := w.uninstallAckWaiters["req-stale"]
	waiter.created = time.Now().Add(-uninstallAckStaleAfter - time.Second)
	w.uninstallAckWaiters["req-stale"] = waiter
	w.uninstallAckMu.Unlock()

	w.registerUninstallAck("req-fresh")
	w.uninstallAckMu.Lock()
	_, stalePresent := w.uninstallAckWaiters["req-stale"]
	_, freshPresent := w.uninstallAckWaiters["req-fresh"]
	w.uninstallAckMu.Unlock()
	if stalePresent {
		t.Fatal("stale waiter survived the register-time sweep")
	}
	if !freshPresent {
		t.Fatal("fresh waiter missing after sweep")
	}
}
