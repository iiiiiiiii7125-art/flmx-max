package handler

import (
	"errors"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

func openCollectHealthRepo(t *testing.T) (*repo.Repository, *Handler) {
	t.Helper()
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := &Handler{repo: r, notifier: newNotifier()}
	return r, h
}

func createCollectHealthNode(t *testing.T, r *repo.Repository, id int64, forwardMode, serverIP string) {
	t.Helper()
	if err := r.DB().Create(&model.Node{ID: id, Name: "n", Status: 1, ForwardMode: forwardMode, ServerIP: serverIP}).Error; err != nil {
		t.Fatalf("create node %d: %v", id, err)
	}
}

func collectHealthOf(t *testing.T, r *repo.Repository, id int64) *model.Node {
	t.Helper()
	node, err := r.GetNodeByID(id)
	if err != nil || node == nil {
		t.Fatalf("get node %d: %v", id, err)
	}
	return node
}

func nftCollectAlerted(h *Handler, nodeID int64) bool {
	h.notifier.mu.Lock()
	defer h.notifier.mu.Unlock()
	return h.notifier.nftCollectAlerted[nodeID]
}

// TestRecordNftCollectFailureThreshold pins the A-中-2 threshold
// semantics: the streak count grows every failure round, the state
// flips to "failing" exactly at the 3rd consecutive failure, and the
// alert fires exactly once per streak (later rounds of the same
// streak stay silent).
func TestRecordNftCollectFailureThreshold(t *testing.T) {
	r, h := openCollectHealthRepo(t)
	createCollectHealthNode(t, r, 1, "nftables", "203.0.113.1")

	h.recordNftCollectFailure(1, errors.New("dial tcp 203.0.113.1:22: i/o timeout"))
	node := collectHealthOf(t, r, 1)
	if node.NftCollectFailCount != 1 || node.NftCollectState != "" {
		t.Fatalf("after 1st failure: count=%d state=%q, want count=1 state=\"\"", node.NftCollectFailCount, node.NftCollectState)
	}
	if node.NftCollectLastError == "" {
		t.Fatal("after 1st failure: last error must be recorded")
	}
	if nftCollectAlerted(h, 1) {
		t.Fatal("after 1st failure: alert must not fire below threshold")
	}

	h.recordNftCollectFailure(1, errors.New("dial tcp 203.0.113.1:22: i/o timeout"))
	node = collectHealthOf(t, r, 1)
	if node.NftCollectFailCount != 2 || node.NftCollectState != "" {
		t.Fatalf("after 2nd failure: count=%d state=%q, want count=2 state=\"\"", node.NftCollectFailCount, node.NftCollectState)
	}

	h.recordNftCollectFailure(1, errors.New("dial tcp 203.0.113.1:22: i/o timeout"))
	node = collectHealthOf(t, r, 1)
	if node.NftCollectFailCount != 3 || node.NftCollectState != "failing" {
		t.Fatalf("after 3rd failure: count=%d state=%q, want count=3 state=failing", node.NftCollectFailCount, node.NftCollectState)
	}
	if !nftCollectAlerted(h, 1) {
		t.Fatal("at threshold: alert must fire once")
	}

	h.recordNftCollectFailure(1, errors.New("dial tcp 203.0.113.1:22: i/o timeout"))
	node = collectHealthOf(t, r, 1)
	if node.NftCollectFailCount != 4 || node.NftCollectState != "failing" {
		t.Fatalf("after 4th failure: count=%d state=%q, want count=4 state=failing", node.NftCollectFailCount, node.NftCollectState)
	}
	if !nftCollectAlerted(h, 1) {
		t.Fatal("later rounds of the same streak must keep the alert mark")
	}
}

// TestRecordNftCollectSuccessRecovers pins the recovery path: a
// success after a failing streak resets the streak, stamps LastOKAt
// and clears the alert mark so a later streak can alert again.
func TestRecordNftCollectSuccessRecovers(t *testing.T) {
	r, h := openCollectHealthRepo(t)
	createCollectHealthNode(t, r, 1, "nftables", "203.0.113.1")

	for i := 0; i < 3; i++ {
		h.recordNftCollectFailure(1, errors.New("dial tcp: i/o timeout"))
	}
	if !nftCollectAlerted(h, 1) {
		t.Fatal("setup: alert mark must be set after 3 failures")
	}
	nowMs := time.Now().UnixMilli()
	h.recordNftCollectSuccess(1, nowMs)
	node := collectHealthOf(t, r, 1)
	if node.NftCollectState != "ok" || node.NftCollectFailCount != 0 || node.NftCollectLastError != "" {
		t.Fatalf("after recovery: state=%q count=%d err=%q, want ok/0/\"\"", node.NftCollectState, node.NftCollectFailCount, node.NftCollectLastError)
	}
	if node.NftCollectLastOKAt != nowMs {
		t.Fatalf("after recovery: lastOKAt=%d, want %d", node.NftCollectLastOKAt, nowMs)
	}
	if nftCollectAlerted(h, 1) {
		t.Fatal("after recovery: alert mark must be cleared")
	}
}

// TestRecordNftCollectSuccessSteadyStateNoWrite pins that steady-state
// successes write nothing: LastOKAt reads as "healthy since <time>",
// not "last round time".
func TestRecordNftCollectSuccessSteadyStateNoWrite(t *testing.T) {
	r, h := openCollectHealthRepo(t)
	createCollectHealthNode(t, r, 1, "nftables", "203.0.113.1")
	base := time.Now().Add(-time.Hour).UnixMilli()
	if err := r.UpdateNftCollectHealth(1, "ok", 0, "", base); err != nil {
		t.Fatalf("setup: %v", err)
	}
	h.recordNftCollectSuccess(1, base+60000)
	node := collectHealthOf(t, r, 1)
	if node.NftCollectLastOKAt != base {
		t.Fatalf("steady-state success must not rewrite lastOKAt: got %d want %d", node.NftCollectLastOKAt, base)
	}
}

func TestLatestNodeMetricTimes(t *testing.T) {
	r, _ := openCollectHealthRepo(t)
	now := time.Now().UnixMilli()
	rows := []model.NodeMetric{
		{NodeID: 1, Timestamp: now - 600000},
		{NodeID: 1, Timestamp: now - 30000},
		{NodeID: 2, Timestamp: now - 86400000},
	}
	for i := range rows {
		if err := r.DB().Create(&rows[i]).Error; err != nil {
			t.Fatalf("create metric: %v", err)
		}
	}
	latest, err := r.LatestNodeMetricTimes([]int64{1, 2, 3})
	if err != nil {
		t.Fatalf("LatestNodeMetricTimes: %v", err)
	}
	if latest[1] != now-30000 {
		t.Fatalf("node 1 latest=%d, want %d", latest[1], now-30000)
	}
	if latest[2] != now-86400000 {
		t.Fatalf("node 2 latest=%d, want %d", latest[2], now-86400000)
	}
	if _, ok := latest[3]; ok {
		t.Fatal("node 3 has no metric rows and must be absent from the map")
	}
}

// TestAttachNodeMetricStalenessAndNicShared pins the node-list
// markers (A-低-5/A-低-6): fresh vs stale vs never-reported metric
// streams, and the same-IP NIC-sharing group.
func TestAttachNodeMetricStalenessAndNicShared(t *testing.T) {
	r, h := openCollectHealthRepo(t)
	now := time.Now().UnixMilli()
	for _, row := range []model.NodeMetric{
		{NodeID: 1, Timestamp: now},
		{NodeID: 2, Timestamp: now - 10*60*1000},
	} {
		if err := r.DB().Create(&row).Error; err != nil {
			t.Fatalf("create metric: %v", err)
		}
	}
	items := []map[string]interface{}{
		{"id": int64(1), "serverIp": "203.0.113.1", "serverIpV4": "203.0.113.1"},
		{"id": int64(2), "serverIp": "203.0.113.2", "serverIpV4": "203.0.113.2"},
		{"id": int64(3), "serverIp": "203.0.113.2", "serverIpV4": "203.0.113.2"},
	}
	h.attachNodeMetricStaleness(items)
	h.attachNodeNicShared(items)

	if items[0]["metricStale"] != false {
		t.Fatalf("node 1 (fresh): metricStale=%v, want false", items[0]["metricStale"])
	}
	if items[0]["lastMetricAt"] != now {
		t.Fatalf("node 1: lastMetricAt=%v, want %d", items[0]["lastMetricAt"], now)
	}
	if _, ok := items[0]["nicSharedWith"]; ok {
		t.Fatal("node 1 (unique IP) must not carry nicSharedWith")
	}
	if items[1]["metricStale"] != true {
		t.Fatalf("node 2 (10 min old): metricStale=%v, want true", items[1]["metricStale"])
	}
	if items[2]["metricStale"] != true || items[2]["lastMetricAt"] != int64(0) {
		t.Fatalf("node 3 (never reported): metricStale=%v lastMetricAt=%v, want true/0", items[2]["metricStale"], items[2]["lastMetricAt"])
	}
	shared2, ok := items[1]["nicSharedWith"].([]int64)
	if !ok || len(shared2) != 1 || shared2[0] != 3 {
		t.Fatalf("node 2: nicSharedWith=%v, want [3]", items[1]["nicSharedWith"])
	}
	shared3, ok := items[2]["nicSharedWith"].([]int64)
	if !ok || len(shared3) != 1 || shared3[0] != 2 {
		t.Fatalf("node 3: nicSharedWith=%v, want [2]", items[2]["nicSharedWith"])
	}
}

// TestIngestNodeNftablesStatusMissing pins the A-低-6 "missing"
// state: a positively-reported absence of nftables on an nftables-mode
// node is recorded; gost-mode nodes keep no state (no nft is normal
// for them); the installed/failed paths are unchanged.
func TestIngestNodeNftablesStatusMissing(t *testing.T) {
	r, h := openCollectHealthRepo(t)
	createCollectHealthNode(t, r, 1, "nftables", "203.0.113.1")
	createCollectHealthNode(t, r, 2, "agent", "203.0.113.2")
	createCollectHealthNode(t, r, 3, "nftables", "203.0.113.3")

	h.ingestNodeNftablesStatus(1, agentKernelStatus{})
	node := collectHealthOf(t, r, 1)
	if node.NftablesStatus != "missing" {
		t.Fatalf("nftables-mode node without nft: status=%q, want missing", node.NftablesStatus)
	}
	if node.NftablesError == "" {
		t.Fatal("missing state must carry the explanation in nftablesError")
	}

	h.ingestNodeNftablesStatus(2, agentKernelStatus{})
	node = collectHealthOf(t, r, 2)
	if node.NftablesStatus != "" {
		t.Fatalf("gost-mode node without nft: status=%q, want \"\" (no noise)", node.NftablesStatus)
	}

	h.ingestNodeNftablesStatus(3, agentKernelStatus{NftAvailable: true})
	node = collectHealthOf(t, r, 3)
	if node.NftablesStatus != "installed" {
		t.Fatalf("node with nft: status=%q, want installed", node.NftablesStatus)
	}

	h.ingestNodeNftablesStatus(1, agentKernelStatus{NftAvailable: true})
	node = collectHealthOf(t, r, 1)
	if node.NftablesStatus != "installed" {
		t.Fatalf("missing -> installed transition: status=%q, want installed", node.NftablesStatus)
	}

	h.ingestNodeNftablesStatus(1, agentKernelStatus{NftError: "nft: command not found"})
	node = collectHealthOf(t, r, 1)
	if node.NftablesStatus != "failed" || node.NftablesError != "nft: command not found" {
		t.Fatalf("error report: status=%q err=%q, want failed/nft: command not found", node.NftablesStatus, node.NftablesError)
	}
}
