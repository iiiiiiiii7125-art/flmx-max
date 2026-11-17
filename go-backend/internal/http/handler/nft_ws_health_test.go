package handler

import (
	"errors"
	"testing"
	"time"
)

// WS kernel-table health tests (R3A-NEW-1 / C NEW-1): the agent's
// nft table (flmx_kernel_*) is a different table from the SSH
// forward table (flmx), so their failures are independent signals
// sharing one health row. The WS error text carries the
// nftWsReadErrorPrefix provenance: the SSH success path must not
// clear a WS-caused streak, and the WS success path must not clear
// an SSH-caused one — otherwise a healthy SSH round would reset the
// WS streak every 30s and the alert would never reach threshold.

func wsReadErr() error {
	return errors.New(nftWsReadErrorPrefix + "Error: No such file or directory")
}

// TestIngestNodeNftablesStatusReadError pins that an agent counter
// read error marks the node row "failed" with the error verbatim
// (same visibility as NftError), that NftError still wins when both
// are present, and that the row recovers to "installed" once the
// read succeeds again.
func TestIngestNodeNftablesStatusReadError(t *testing.T) {
	r, h := openCollectHealthRepo(t)
	createCollectHealthNode(t, r, 1, "nftables", "203.0.113.1")

	h.ingestNodeNftablesStatus(1, agentKernelStatus{NftAvailable: true, NftReadError: "Error: No such file or directory"})
	node := collectHealthOf(t, r, 1)
	if node.NftablesStatus != "failed" || node.NftablesError != "Error: No such file or directory" {
		t.Fatalf("read error: status=%q err=%q, want failed/Error: No such file or directory", node.NftablesStatus, node.NftablesError)
	}

	h.ingestNodeNftablesStatus(1, agentKernelStatus{NftAvailable: true, NftError: "install failed", NftReadError: "Error: No such file or directory"})
	node = collectHealthOf(t, r, 1)
	if node.NftablesStatus != "failed" || node.NftablesError != "install failed" {
		t.Fatalf("both errors: status=%q err=%q, want failed/install failed", node.NftablesStatus, node.NftablesError)
	}

	h.ingestNodeNftablesStatus(1, agentKernelStatus{NftAvailable: true})
	node = collectHealthOf(t, r, 1)
	if node.NftablesStatus != "installed" || node.NftablesError != "" {
		t.Fatalf("recovered: status=%q err=%q, want installed/\"\"", node.NftablesStatus, node.NftablesError)
	}
}

// TestWsReadErrorReachesFailureThreshold pins that WS-path failures
// drive the shared A-中-2 streak exactly like SSH-path failures:
// failing at the 3rd consecutive round with the alert firing once.
func TestWsReadErrorReachesFailureThreshold(t *testing.T) {
	r, h := openCollectHealthRepo(t)
	createCollectHealthNode(t, r, 1, "nftables", "203.0.113.1")

	h.recordNftCollectFailure(1, wsReadErr())
	h.recordNftCollectFailure(1, wsReadErr())
	node := collectHealthOf(t, r, 1)
	if node.NftCollectFailCount != 2 || node.NftCollectState != "" {
		t.Fatalf("after 2 WS failures: count=%d state=%q, want 2/\"\"", node.NftCollectFailCount, node.NftCollectState)
	}
	if nftCollectAlerted(h, 1) {
		t.Fatal("below threshold: alert must not fire")
	}

	h.recordNftCollectFailure(1, wsReadErr())
	node = collectHealthOf(t, r, 1)
	if node.NftCollectFailCount != 3 || node.NftCollectState != "failing" {
		t.Fatalf("after 3 WS failures: count=%d state=%q, want 3/failing", node.NftCollectFailCount, node.NftCollectState)
	}
	if !nftCollectAlerted(h, 1) {
		t.Fatal("at threshold: alert must fire once")
	}
}

// TestRecordNftCollectSuccessSkipsWsStreak pins the provenance guard:
// an SSH success must not reset a streak the WS table caused (the
// agent table is still missing), and the WS success path clears only
// that streak, sending the recovery notification.
func TestRecordNftCollectSuccessSkipsWsStreak(t *testing.T) {
	r, h := openCollectHealthRepo(t)
	createCollectHealthNode(t, r, 1, "nftables", "203.0.113.1")

	for i := 0; i < 3; i++ {
		h.recordNftCollectFailure(1, wsReadErr())
	}
	if !nftCollectAlerted(h, 1) {
		t.Fatal("setup: alert mark must be set after 3 WS failures")
	}

	nowMs := time.Now().UnixMilli()
	h.recordNftCollectSuccess(1, nowMs)
	node := collectHealthOf(t, r, 1)
	if node.NftCollectState != "failing" || node.NftCollectFailCount != 3 {
		t.Fatalf("SSH success must not clear a WS streak: state=%q count=%d, want failing/3", node.NftCollectState, node.NftCollectFailCount)
	}
	if !nftCollectAlerted(h, 1) {
		t.Fatal("SSH success must not clear the WS alert mark")
	}

	h.recordNftWsCollectSuccess(1, nowMs)
	node = collectHealthOf(t, r, 1)
	if node.NftCollectState != "ok" || node.NftCollectFailCount != 0 || node.NftCollectLastError != "" {
		t.Fatalf("WS recovery: state=%q count=%d err=%q, want ok/0/\"\"", node.NftCollectState, node.NftCollectFailCount, node.NftCollectLastError)
	}
	if nftCollectAlerted(h, 1) {
		t.Fatal("WS recovery: alert mark must be cleared")
	}
}

// TestRecordNftWsCollectSuccessLeavesSshStreak pins the other side
// of the provenance guard: a WS success must not clear a streak the
// SSH table caused.
func TestRecordNftWsCollectSuccessLeavesSshStreak(t *testing.T) {
	r, h := openCollectHealthRepo(t)
	createCollectHealthNode(t, r, 1, "nftables", "203.0.113.1")

	for i := 0; i < 3; i++ {
		h.recordNftCollectFailure(1, errors.New("dial tcp 203.0.113.1:22: i/o timeout"))
	}
	nowMs := time.Now().UnixMilli()
	h.recordNftWsCollectSuccess(1, nowMs)
	node := collectHealthOf(t, r, 1)
	if node.NftCollectState != "failing" || node.NftCollectFailCount != 3 {
		t.Fatalf("WS success must not clear an SSH streak: state=%q count=%d, want failing/3", node.NftCollectState, node.NftCollectFailCount)
	}
	if !nftCollectAlerted(h, 1) {
		t.Fatal("WS success must not clear the SSH alert mark")
	}

	h.recordNftCollectSuccess(1, nowMs)
	node = collectHealthOf(t, r, 1)
	if node.NftCollectState != "ok" || node.NftCollectFailCount != 0 {
		t.Fatalf("SSH recovery: state=%q count=%d, want ok/0", node.NftCollectState, node.NftCollectFailCount)
	}
}
