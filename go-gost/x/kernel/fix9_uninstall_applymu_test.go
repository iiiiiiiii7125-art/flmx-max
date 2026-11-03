package kernel

import (
	"testing"
	"time"
)

// R9-07 (I-F2-02) regression pin, the inversion of round-9 probe K:
// Manager.Uninstall used to bypass applyMu — with an Apply in flight
// (applyMu held, as during a minutes-long kernel download) Control
// blocked as designed but Uninstall ran straight through, stopping a
// snapshot that predated the Apply and racing its persist. Uninstall
// now takes applyMu with the same caliber as Apply/Control: it must
// block while applyMu is held and proceed once it is released.
func TestR9UninstallBlocksOnApplyMu(t *testing.T) {
	m := &Manager{instances: map[string]*instance{}, nft: &nftCounters{}}

	m.applyMu.Lock()
	ctrlDone := make(chan struct{})
	go func() {
		_, _ = m.Control("xray", "stop")
		close(ctrlDone)
	}()
	select {
	case <-ctrlDone:
		t.Fatal("precondition failed: Control did not block on applyMu")
	case <-time.After(300 * time.Millisecond):
	}

	unDone := make(chan error, 1)
	go func() { unDone <- m.Uninstall() }()
	select {
	case err := <-unDone:
		t.Fatalf("R9-07: Uninstall completed while applyMu was held (err=%v) — it must serialize behind Apply/Control", err)
	case <-time.After(1500 * time.Millisecond):
	}

	m.applyMu.Unlock()
	select {
	case <-unDone:
		// The error itself is environment-dependent (nft netlink is
		// unavailable in the sandbox); completion after release is
		// the pinned behavior.
	case <-time.After(5 * time.Second):
		t.Fatal("R9-07: Uninstall did not proceed after applyMu was released")
	}
	select {
	case <-ctrlDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Control did not proceed after applyMu released")
	}
}
