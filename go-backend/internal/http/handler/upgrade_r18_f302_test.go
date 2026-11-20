package handler

import (
	"bytes"
	"errors"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// R18-F3-02 (M-3): node local-runtime redeploy used to re-queue
// itself every 30s forever on failure, with no failure count, no
// backoff and no escalation. It now mirrors the nft auto-reconcile
// guard: consecutive failures back off (30s → 5m → 15m → 60m), and
// after 10 failures the node stops being re-queued and is escalated
// with one action=needs_manual_redeploy line. A fresh direct redeploy
// attempt can still succeed and clear the state; an offline skip
// (errRedeployPending) never counts as a failure.
func TestR18F302LocalRuntimeRetryBackoffTiers(t *testing.T) {
	cases := map[int]time.Duration{
		0:  30 * time.Second,
		1:  30 * time.Second,
		2:  5 * time.Minute,
		3:  15 * time.Minute,
		4:  time.Hour,
		10: time.Hour,
	}
	for failures, want := range cases {
		if got := nodeLocalRuntimeRetryBackoff(failures); got != want {
			t.Fatalf("nodeLocalRuntimeRetryBackoff(%d) = %v, want %v", failures, got, want)
		}
	}
}

func TestR18F302LocalRuntimeRetryGivesUpAfterMaxFailures(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	const nodeID = 99

	var buf bytes.Buffer
	oldOut := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(oldOut) })
	failState := func() (int, bool) {
		h.upgradeMu.Lock()
		defer h.upgradeMu.Unlock()
		st := h.nodeLocalRuntimeRetryFail[nodeID]
		if st == nil {
			return 0, false
		}
		return st.failures, st.gaveUp
	}
	// Disarm a queued AfterFunc without touching the failure state
	// (clearNodeLocalRuntimeRetry would also reset the streak, which
	// is exactly what the drive below must not do). The timer
	// closure re-checks the queued flag before doing any work.
	disarmQueued := func() {
		h.upgradeMu.Lock()
		delete(h.nodeLocalRuntimeRetryQueued, nodeID)
		h.upgradeMu.Unlock()
	}
	exhaustedLines := func() int { return strings.Count(buf.String(), "action=needs_manual_redeploy") }

	// Pending (offline skip) never counts.
	h.noteNodeLocalRuntimeRetryFailure(nodeID, errRedeployPending)
	if got, _ := failState(); got != 0 {
		t.Fatalf("pending must not count, failures = %d", got)
	}

	// Nine real failures: still queuing, no escalation yet.
	for i := 0; i < 9; i++ {
		h.noteNodeLocalRuntimeRetryFailure(nodeID, errors.New("redeploy boom"))
	}
	if got, gaveUp := failState(); got != 9 || gaveUp {
		t.Fatalf("after 9 failures = %d gaveUp = %v, want 9 false", got, gaveUp)
	}
	if !h.scheduleNodeLocalRuntimeRetry(nodeID) {
		t.Fatalf("schedule must still queue before the cap")
	}
	disarmQueued()

	// Tenth failure: give up, exactly one escalation line.
	h.noteNodeLocalRuntimeRetryFailure(nodeID, errors.New("redeploy boom"))
	if got, gaveUp := failState(); got != 10 || !gaveUp {
		t.Fatalf("after 10 failures = %d gaveUp = %v, want 10 true", got, gaveUp)
	}
	if got := exhaustedLines(); got != 1 {
		t.Fatalf("needs_manual_redeploy lines = %d, want exactly 1", got)
	}

	// Further failures stay silent on the escalation line, and no
	// new automatic retry is queued.
	h.noteNodeLocalRuntimeRetryFailure(nodeID, errors.New("redeploy boom"))
	if got := exhaustedLines(); got != 1 {
		t.Fatalf("needs_manual_redeploy lines = %d after an 11th failure, want 1", got)
	}
	if h.scheduleNodeLocalRuntimeRetry(nodeID) {
		t.Fatalf("schedule must refuse to queue after giving up")
	}
	h.upgradeMu.Lock()
	_, queued := h.nodeLocalRuntimeRetryQueued[nodeID]
	h.upgradeMu.Unlock()
	if queued {
		t.Fatalf("node must not be in the retry queue after giving up")
	}

	// A success clears the streak, and scheduling works again.
	h.clearNodeLocalRuntimeRetry(nodeID)
	if got, gaveUp := failState(); got != 0 || gaveUp {
		t.Fatalf("after clear failures = %d gaveUp = %v, want 0 false", got, gaveUp)
	}
	if !h.scheduleNodeLocalRuntimeRetry(nodeID) {
		t.Fatalf("schedule must queue again after the state is cleared")
	}
	disarmQueued()
}

func TestR18F302SkippedWarningNamesExhaustion(t *testing.T) {
	// R18-F3-02: the receipt must not promise a retry the node will
	// never get, and with backoff it must not promise a fixed 30s.
	node := tunnelRuntimeSkippedNode{NodeID: 7, Name: "n7", Reason: "offline"}
	if got := tunnelRuntimeSkippedWarning(node, true); !strings.Contains(got, "自动重试") || strings.Contains(got, "30 秒") {
		t.Fatalf("queued warning = %q, want an automatic-retry promise without a fixed 30s", got)
	}
	if got := tunnelRuntimeSkippedWarning(node, false); !strings.Contains(got, "需人工处理") {
		t.Fatalf("exhausted warning = %q, want it to name manual handling", got)
	}
}
