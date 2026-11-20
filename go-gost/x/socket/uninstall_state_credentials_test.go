package socket

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestUninstallStatePersistsPanelCredentials pins the first half
// of the fix: handleUninstallAgent must persist the panel address
// and secret in the state file. It asserts on the raw state JSON
// (no new symbols) so it fails at runtime — not at build time —
// on code without the fix.
func TestUninstallStatePersistsPanelCredentials(t *testing.T) {
	w4Setup()
	dir := stubUninstallStatePath(t)
	statePath := filepath.Join(dir, "agent-uninstall.state.json")

	root := t.TempDir()
	dataDir := filepath.Join(root, "flmx_agent")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(root, "bin", "flmx_agent")
	if err := os.MkdirAll(filepath.Dir(binPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binPath, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	stubs := stubUninstallHooksWithDataDir(t, binPath, dataDir, true)

	origAckWait, origAttempts, origRetryWait := uninstallAckWaitTimeout, uninstallReportAttempts, uninstallReportRetryWait
	uninstallAckWaitTimeout = 30 * time.Second // only the ack signal may release execution
	uninstallReportAttempts = 1
	uninstallReportRetryWait = 20 * time.Millisecond
	t.Cleanup(func() {
		uninstallAckWaitTimeout, uninstallReportAttempts, uninstallReportRetryWait = origAckWait, origAttempts, origRetryWait
	})
	uninstallStarted.Store(false)
	t.Cleanup(func() { uninstallStarted.Store(false) })
	// Same waiter-exit dance as TestContainerUninstallWaitsForFinalReport:
	// marking the stored state reported lets the waiter return while
	// the exit hook is still stubbed.
	t.Cleanup(func() {
		if st, ok := loadUninstallState(); ok && !st.Reported {
			st.Reported = true
			saveUninstallState(st)
		}
		deadline := time.Now().Add(5 * time.Second)
		for stubs.exit == -1 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
	})

	const secret = "d202-gate-secret-0123456789abcdef"
	w := NewWebSocketReporter("ws://127.0.0.1:1", secret)
	w.addr = "http://127.0.0.1:9"
	w.secret = secret
	if _, err := w.handleUninstallAgent(nil, "req-d202"); err != nil {
		t.Fatalf("handleUninstallAgent: %v", err)
	}
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("state file must exist after dispatch: %v", err)
	}
	if !strings.Contains(string(raw), `"panelAddr":"http://127.0.0.1:9"`) {
		t.Fatalf("stored uninstall state must carry the panel address for a post-restart re-report, got %s", raw)
	}
	if !strings.Contains(string(raw), `"panelSecret":`) {
		t.Fatalf("stored uninstall state must carry the panel secret for a post-restart re-report, got %s", raw)
	}
	w.signalUninstallAck("req-d202", true)
	// Wait in the body until the uninstall has run to its final
	// state (same as TestContainerUninstallWaitsForFinalReport):
	// the cleanup below marks the stored state reported so the
	// waiter can exit, but executeUninstall overwrites the state
	// file when it reaches the final state — marking it reported
	// before that point would be overwritten and the waiter would
	// spin past the cleanups that restore the stubbed package
	// vars (data race under -race).
	deadline := time.Now().Add(10 * time.Second)
	for {
		if st, ok := loadUninstallState(); ok && st.State == "succeeded" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("uninstall did not reach a final state")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

