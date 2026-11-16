package handler

import (
	"errors"
	"testing"
	"time"
)

// Mixed-streak regression (R4C-01): the SSH forward-table path and
// the WS kernel-table path are independent signals sharing one health
// row. A streak built from SSH failures and topped up by one WS
// failure is partly SSH-caused: the WS success that follows must
// leave it failing until the SSH path itself recovers — it must not
// clear the streak and must not send the "采集恢复" notification
// while SSH is still down.
func TestMixedStreakWsSuccessMustNotClearSshFailures(t *testing.T) {
	r, h := openCollectHealthRepo(t)
	createCollectHealthNode(t, r, 1, "nftables", "203.0.113.1")

	sshErr := errors.New("dial tcp 203.0.113.1:22: i/o timeout")
	h.recordNftCollectFailure(1, sshErr)
	h.recordNftCollectFailure(1, sshErr)
	h.recordNftCollectFailure(1, wsReadErr())
	node := collectHealthOf(t, r, 1)
	if node.NftCollectFailCount != 3 || node.NftCollectState != "failing" {
		t.Fatalf("setup: count=%d state=%q, want 3/failing", node.NftCollectFailCount, node.NftCollectState)
	}
	if !nftCollectAlerted(h, 1) {
		t.Fatal("setup: alert mark must be set after 3 failures")
	}

	nowMs := time.Now().UnixMilli()
	h.recordNftWsCollectSuccess(1, nowMs)
	node = collectHealthOf(t, r, 1)
	if node.NftCollectState != "failing" || node.NftCollectFailCount != 3 {
		t.Fatalf("WS success cleared a mixed streak: state=%q count=%d err=%q, want failing/3 (SSH part still failing)", node.NftCollectState, node.NftCollectFailCount, node.NftCollectLastError)
	}
	if !nftCollectAlerted(h, 1) {
		t.Fatal("WS success cleared the alert mark while the SSH path is still failing")
	}
}

// TestMixedStreakSshSuccessMustNotClearWsFailures pins the mirror
// side (same family as R4C-01): a streak built from WS failures and
// topped up by one SSH failure is partly WS-caused, so the SSH
// success that follows must leave it failing until the WS path
// itself recovers.
func TestMixedStreakSshSuccessMustNotClearWsFailures(t *testing.T) {
	r, h := openCollectHealthRepo(t)
	createCollectHealthNode(t, r, 1, "nftables", "203.0.113.1")

	h.recordNftCollectFailure(1, wsReadErr())
	h.recordNftCollectFailure(1, wsReadErr())
	h.recordNftCollectFailure(1, errors.New("dial tcp 203.0.113.1:22: i/o timeout"))
	node := collectHealthOf(t, r, 1)
	if node.NftCollectFailCount != 3 || node.NftCollectState != "failing" {
		t.Fatalf("setup: count=%d state=%q, want 3/failing", node.NftCollectFailCount, node.NftCollectState)
	}
	if !nftCollectAlerted(h, 1) {
		t.Fatal("setup: alert mark must be set after 3 failures")
	}

	nowMs := time.Now().UnixMilli()
	h.recordNftCollectSuccess(1, nowMs)
	node = collectHealthOf(t, r, 1)
	if node.NftCollectState != "failing" || node.NftCollectFailCount != 3 {
		t.Fatalf("SSH success cleared a mixed streak: state=%q count=%d err=%q, want failing/3 (WS part still failing)", node.NftCollectState, node.NftCollectFailCount, node.NftCollectLastError)
	}
	if !nftCollectAlerted(h, 1) {
		t.Fatal("SSH success cleared the alert mark while the WS path is still failing")
	}
	if node.NftCollectSshFailCount != 0 || node.NftCollectWsFailCount != 2 {
		t.Fatalf("SSH success must zero only the SSH contribution: ssh=%d ws=%d, want 0/2", node.NftCollectSshFailCount, node.NftCollectWsFailCount)
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

// TestMixedStreakRecoversWhenBothPathsRecover pins the full
// resolution of a mixed streak: each path's success clears only its
// own contribution, and the row clears (with the recovery
// notification) only once both have recovered — never stuck failing
// after both paths are healthy.
func TestMixedStreakRecoversWhenBothPathsRecover(t *testing.T) {
	r, h := openCollectHealthRepo(t)
	createCollectHealthNode(t, r, 1, "nftables", "203.0.113.1")

	sshErr := errors.New("dial tcp 203.0.113.1:22: i/o timeout")
	h.recordNftCollectFailure(1, sshErr)
	h.recordNftCollectFailure(1, sshErr)
	h.recordNftCollectFailure(1, wsReadErr())
	if !nftCollectAlerted(h, 1) {
		t.Fatal("setup: alert mark must be set after 3 failures")
	}

	nowMs := time.Now().UnixMilli()
	h.recordNftWsCollectSuccess(1, nowMs)
	node := collectHealthOf(t, r, 1)
	if node.NftCollectState != "failing" || node.NftCollectFailCount != 3 {
		t.Fatalf("WS success on mixed streak: state=%q count=%d, want failing/3", node.NftCollectState, node.NftCollectFailCount)
	}
	if node.NftCollectSshFailCount != 2 || node.NftCollectWsFailCount != 0 {
		t.Fatalf("WS success must zero only the WS contribution: ssh=%d ws=%d, want 2/0", node.NftCollectSshFailCount, node.NftCollectWsFailCount)
	}
	if !nftCollectAlerted(h, 1) {
		t.Fatal("WS success must not clear the alert mark while SSH is still failing")
	}

	h.recordNftCollectSuccess(1, nowMs)
	node = collectHealthOf(t, r, 1)
	if node.NftCollectState != "ok" || node.NftCollectFailCount != 0 || node.NftCollectLastError != "" {
		t.Fatalf("SSH recovery: state=%q count=%d err=%q, want ok/0/\"\"", node.NftCollectState, node.NftCollectFailCount, node.NftCollectLastError)
	}
	if node.NftCollectLastOKAt != nowMs {
		t.Fatalf("SSH recovery: lastOKAt=%d, want %d", node.NftCollectLastOKAt, nowMs)
	}
	if node.NftCollectSshFailCount != 0 || node.NftCollectWsFailCount != 0 {
		t.Fatalf("recovery must zero both contributions: ssh=%d ws=%d, want 0/0", node.NftCollectSshFailCount, node.NftCollectWsFailCount)
	}
	if nftCollectAlerted(h, 1) {
		t.Fatal("recovery: alert mark must be cleared")
	}
}
