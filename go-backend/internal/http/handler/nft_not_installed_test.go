package handler

import (
	"testing"
	"time"
)

// R4C-02 panel leg: a node that never had nftables must not be
// driven failing by the WS status path, and its kernels must not be
// re-pushed every poll. The agent reports the never-installed shape
// as NftNotInstalled (no NftReadError); the panel must treat that
// signal as "healthy but unmetered", while a real table loss
// (NftAvailable=true + NftReadError) keeps the R3A-NEW-1 behavior
// (failing + alert + re-push).
//
// The NftNotInstalled guard is written as a precedence rule
// (NftNotInstalled beats NftReadError), so the poll/ingest tests
// below send both fields set at once: that is the only input that
// distinguishes the fixed panel from the old one — a new agent
// never sets NftReadError for a never-installed node, and an old
// agent never sets NftNotInstalled. The pure new-agent shape is
// asserted as a contract pin alongside.

func TestIngestNodeNftablesStatusNotInstalled(t *testing.T) {
	r, h := openCollectHealthRepo(t)
	createCollectHealthNode(t, r, 1, "nftables", "203.0.113.1")
	createCollectHealthNode(t, r, 2, "gost", "203.0.113.2")

	// Both-set (precedence probe): NftNotInstalled must win over
	// NftReadError — the row is "missing", not "failed".
	h.ingestNodeNftablesStatus(1, agentKernelStatus{NftAvailable: false, NftReadError: "未安装 nftables，无法统计流量", NftNotInstalled: true})
	node := collectHealthOf(t, r, 1)
	if node.NftablesStatus != "missing" || node.NftablesError != "未安装 nftables，订阅／realm 计数不入账" {
		t.Fatalf("not-installed nftables node: status=%q err=%q, want missing/未安装 nftables，订阅／realm 计数不入账", node.NftablesStatus, node.NftablesError)
	}

	// Pure new-agent shape: same outcome.
	h.ingestNodeNftablesStatus(1, agentKernelStatus{NftAvailable: false, NftNotInstalled: true})
	node = collectHealthOf(t, r, 1)
	if node.NftablesStatus != "missing" {
		t.Fatalf("pure not-installed status: status=%q, want missing", node.NftablesStatus)
	}

	// Gost-mode nodes stay silent (no noise row).
	h.ingestNodeNftablesStatus(2, agentKernelStatus{NftAvailable: false, NftNotInstalled: true})
	node = collectHealthOf(t, r, 2)
	if node.NftablesStatus != "" {
		t.Fatalf("not-installed gost node: status=%q, want \"\"", node.NftablesStatus)
	}

	// Old-agent shape (no NftNotInstalled field): the panel cannot
	// tell "never installed" from "had a table, lost the binary",
	// so NftReadError still means failed until the agent upgrades.
	h.ingestNodeNftablesStatus(1, agentKernelStatus{NftAvailable: false, NftReadError: "未安装 nftables，无法统计流量"})
	node = collectHealthOf(t, r, 1)
	if node.NftablesStatus != "failed" || node.NftablesError != "未安装 nftables，无法统计流量" {
		t.Fatalf("old-agent no-nft: status=%q err=%q, want failed/未安装 nftables，无法统计流量", node.NftablesStatus, node.NftablesError)
	}
}

// alignNotInstalledRig pins the kernel row hashes so poll re-push
// decisions are deterministic: the mock reports configHash
// "c2-hash" and the DB row agrees, so no drift re-push can fire
// and only the NftReadError branch can push.
func alignNotInstalledRig(t *testing.T, r interface {
	UpdateNodeKernelFields(int64, map[string]interface{}) error
}, mock *kernelNotifyMock, kernelID int64) {
	t.Helper()
	if err := r.UpdateNodeKernelFields(kernelID, map[string]interface{}{"config_hash": "c2-hash", "applied_hash": "c2-hash"}); err != nil {
		t.Fatal(err)
	}
	mock.setReportHash("c2-hash")
}

func TestPollNodeKernelsNotInstalledSkipsFailureAndReapply(t *testing.T) {
	h, r, mock, _, k, nodeID := setupKernelNotifyRig(t)
	alignNotInstalledRig(t, r, mock, k.ID)

	applyBefore, _ := mock.calls()
	// Both-set status (precedence probe): three consecutive polls
	// must not record a failure, must not alert, must not re-push.
	mock.setNft(false, "未安装 nftables，无法统计流量", true)
	for i := 0; i < 3; i++ {
		h.pollNodeKernels(nodeID, false)
	}
	// Re-pushes are async goroutines: give them a window to land.
	time.Sleep(1500 * time.Millisecond)
	applyAfter, _ := mock.calls()
	if applyAfter != applyBefore {
		t.Fatalf("not-installed polls re-pushed kernels: applyCalls %d -> %d, want unchanged", applyBefore, applyAfter)
	}
	// Merged semantics (R4C-01 + R4C-02): a successful poll stamps
	// the row "ok" on first success (steady-state successes write
	// nothing after that); the requirement here is no failure
	// recorded — count 0, no alert, no re-push.
	node := collectHealthOf(t, r, nodeID)
	if node.NftCollectFailCount != 0 || node.NftCollectState != "ok" {
		t.Fatalf("not-installed polls: count=%d state=%q, want 0/\"ok\"", node.NftCollectFailCount, node.NftCollectState)
	}
	if nftCollectAlerted(h, nodeID) {
		t.Fatal("not-installed polls must not fire the nft collect alert")
	}

	// Pure new-agent shape: same outcome for three more polls.
	mock.setNft(false, "", true)
	for i := 0; i < 3; i++ {
		h.pollNodeKernels(nodeID, false)
	}
	time.Sleep(1500 * time.Millisecond)
	applyAfter, _ = mock.calls()
	if applyAfter != applyBefore {
		t.Fatalf("pure not-installed polls re-pushed kernels: applyCalls %d -> %d, want unchanged", applyBefore, applyAfter)
	}
	node = collectHealthOf(t, r, nodeID)
	if node.NftCollectFailCount != 0 || node.NftCollectState != "ok" {
		t.Fatalf("pure not-installed polls: count=%d state=%q, want 0/\"ok\"", node.NftCollectFailCount, node.NftCollectState)
	}
}

func TestPollNodeKernelsTableLossStillFailsAndReapplies(t *testing.T) {
	h, r, mock, _, k, nodeID := setupKernelNotifyRig(t)
	alignNotInstalledRig(t, r, mock, k.ID)

	applyBefore, _ := mock.calls()
	mock.setNft(true, "Error: No such file or directory", false)
	for i := 0; i < 3; i++ {
		h.pollNodeKernels(nodeID, false)
	}
	node := collectHealthOf(t, r, nodeID)
	if node.NftCollectFailCount != 3 || node.NftCollectState != "failing" {
		t.Fatalf("table-loss polls: count=%d state=%q, want 3/failing", node.NftCollectFailCount, node.NftCollectState)
	}
	if !nftCollectAlerted(h, nodeID) {
		t.Fatal("table-loss polls must fire the nft collect alert once")
	}
	// The re-push is async and races the hash writes, so only the
	// floor is pinned: at least one re-push must land.
	deadline := time.Now().Add(5 * time.Second)
	for {
		applyAfter, _ := mock.calls()
		if applyAfter > applyBefore || time.Now().After(deadline) {
			if applyAfter <= applyBefore {
				t.Fatalf("table-loss polls never re-pushed kernels: applyCalls stayed %d", applyBefore)
			}
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
}
