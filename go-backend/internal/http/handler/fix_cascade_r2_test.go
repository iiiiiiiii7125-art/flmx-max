package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
	"go-backend/internal/ws"
)

// FIX-CASCADE（R2-E1 删除级联完整性）handler 层回归：
//   - E1-02 best-exit 决策条目随删隧道清除；
//   - E1-09(a) 删节点级联前逐内核下发移除；
//   - E1-09(b) Force 强删内核（下发未确认）登记孤儿重查标记、
//     非 Force 下发失败不删不登记、周期轮询确认空集后清标记、
//     节点不可达时保留标记；
//   - E1-01 删隧道回执在有墓碑分流时点名终局/保留计数；
//   - E1-04 state_sweep 全局清扫 legacy 去重过期登记。

func TestFixCascadeBestExitForgetTunnel(t *testing.T) {
	m := newBestExitManager()
	m.decisions[bestExitOwnerKey{TunnelID: 1, OwnerNodeID: 10}] = &bestExitDecision{}
	m.decisions[bestExitOwnerKey{TunnelID: 1, OwnerNodeID: 11}] = &bestExitDecision{}
	m.decisions[bestExitOwnerKey{TunnelID: 2, OwnerNodeID: 10}] = &bestExitDecision{}

	m.forgetTunnel(1)
	if len(m.decisions) != 1 {
		t.Fatalf("E1-02: decisions after forgetTunnel(1) = %v, want only tunnel 2", m.decisions)
	}
	if _, ok := m.decisions[bestExitOwnerKey{TunnelID: 2, OwnerNodeID: 10}]; !ok {
		t.Fatal("E1-02: tunnel 2 decision must survive forgetTunnel(1)")
	}
	// 幂等：重复清除与 nil 接收者不 panic。
	m.forgetTunnel(1)
	var nilManager *bestExitManager
	nilManager.forgetTunnel(1)
}

func seedKernelRemoveFixture(t *testing.T, name string) (*Handler, *repo.Repository, int64, *model.NodeKernel) {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/" + name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	nodeID := seedRelayTargetNode(t, r, "front", "198.51.100.1")
	k := seedChainKernel(t, r, nodeID, kernel.TypeXray)
	return h, r, nodeID, k
}

func callKernelControl(t *testing.T, h *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/kernel/control", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.kernelControl(rec, req)
	return rec
}

func TestFixCascadeKernelRemoveForceMarksOrphanRecheck(t *testing.T) {
	h, r, nodeID, k := seedKernelRemoveFixture(t, "fix-cascade-kernel-force.db")
	// 无 agent 在线：移除下发必然未确认。Force 强删应落库删除 +
	// 登记孤儿重查标记。
	rec := callKernelControl(t, h, `{"id":`+strconv.FormatInt(k.ID, 10)+`,"action":"remove","force":true}`)
	var resp struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.Code != 0 {
		t.Fatalf("force remove response = %s (err=%v), want code 0", rec.Body.String(), err)
	}
	if got, err := r.GetNodeKernel(k.ID); err != nil || got != nil {
		t.Fatalf("kernel row must be deleted after force remove, got=%v err=%v", got, err)
	}
	ids, err := r.ListKernelOrphanRecheckNodeIDs()
	if err != nil || len(ids) != 1 || ids[0] != nodeID {
		t.Fatalf("E1-09: orphan recheck nodes = %v (err=%v), want [%d]", ids, err, nodeID)
	}
	types, err := r.KernelOrphanRecheckTypes(nodeID)
	if err != nil || len(types) != 1 || types[0] != kernel.TypeXray {
		t.Fatalf("E1-09: orphan types = %v (err=%v), want [xray]", types, err)
	}
}

func TestFixCascadeKernelRemoveNonForceFailureKeepsRowAndNoMark(t *testing.T) {
	h, r, nodeID, k := seedKernelRemoveFixture(t, "fix-cascade-kernel-nonforce.db")
	rec := callKernelControl(t, h, `{"id":`+strconv.FormatInt(k.ID, 10)+`,"action":"remove","force":false}`)
	var resp struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.Code == 0 {
		t.Fatalf("non-force remove with unreachable node must fail, response = %s", rec.Body.String())
	}
	if got, err := r.GetNodeKernel(k.ID); err != nil || got == nil {
		t.Fatalf("kernel row must survive failed non-force remove, got=%v err=%v", got, err)
	}
	ids, err := r.ListKernelOrphanRecheckNodeIDs()
	if err != nil || len(ids) != 0 {
		t.Fatalf("E1-09: no orphan mark expected for aborted remove, got %v (node %d)", ids, nodeID)
	}
}

// setupMarkedNodeAgent 建立一个带孤儿重查标记、无 DB 内核的在线
// 节点（mock agent 对全部命令回成功、无 data——KernelStatus 回
// 读为空集）。
func setupMarkedNodeAgent(t *testing.T) (*Handler, *repo.Repository, int64, *r1b3CommandRecorder) {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/fix-cascade-orphan-converge.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	server := ws.NewServer(r, "test-secret")
	server.SetNodeOnlineHook(h.onNodeOnline)
	server.SetNodeOfflineHook(h.notifyNodeOffline)
	h.wsServer = server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		server.ServeHTTP(w, req)
	}))
	t.Cleanup(ts.Close)

	nodeID := seedRelayTargetNode(t, r, "front", "198.51.100.1")
	if err := r.DB().Exec(`UPDATE node SET status = 1 WHERE id = ?`, nodeID).Error; err != nil {
		t.Fatalf("activate node: %v", err)
	}
	if err := r.MarkKernelOrphanRecheck(nodeID, kernel.TypeXray, time.Now().UnixMilli()); err != nil {
		t.Fatalf("mark orphan recheck: %v", err)
	}
	rc := &r1b3CommandRecorder{}
	connectR1B3Agent(t, ts, "front-secret", rc)
	waitForCondition(t, 5*time.Second, func() bool { return server.IsNodeOnline(nodeID) }, "mock agent online")
	return h, r, nodeID, rc
}

func TestFixCascadeOrphanRecheckClearsWhenAgentReportsEmpty(t *testing.T) {
	h, r, nodeID, _ := setupMarkedNodeAgent(t)
	h.refreshKernelStatuses()
	ids, err := r.ListKernelOrphanRecheckNodeIDs()
	if err != nil {
		t.Fatalf("list marks: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("E1-09: marker must clear once the agent confirms an empty kernel set, got %v (node %d)", ids, nodeID)
	}
}

func TestFixCascadeOrphanRecheckKeptWhenNodeUnreachable(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/fix-cascade-orphan-offline.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	nodeID := seedRelayTargetNode(t, r, "front", "198.51.100.1")
	if err := r.DB().Exec(`UPDATE node SET status = 1 WHERE id = ?`, nodeID).Error; err != nil {
		t.Fatalf("activate node: %v", err)
	}
	if err := r.MarkKernelOrphanRecheck(nodeID, kernel.TypeXray, time.Now().UnixMilli()); err != nil {
		t.Fatalf("mark orphan recheck: %v", err)
	}
	// 无 wsServer（不可达）：一轮刷新后标记必须保留。
	h.refreshKernelStatuses()
	ids, err := r.ListKernelOrphanRecheckNodeIDs()
	if err != nil || len(ids) != 1 || ids[0] != nodeID {
		t.Fatalf("E1-09: marker must be kept while the node is unreachable, got %v err=%v", ids, err)
	}
}

func TestFixCascadeNodeDeleteDispatchesKernelRemovals(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/fix-cascade-node-delete-kernels.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	server := ws.NewServer(r, "test-secret")
	server.SetNodeOnlineHook(h.onNodeOnline)
	server.SetNodeOfflineHook(h.notifyNodeOffline)
	h.wsServer = server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		server.ServeHTTP(w, req)
	}))
	t.Cleanup(ts.Close)

	nodeID := seedRelayTargetNode(t, r, "front", "198.51.100.1")
	if err := r.DB().Exec(`UPDATE node SET status = 1 WHERE id = ?`, nodeID).Error; err != nil {
		t.Fatalf("activate node: %v", err)
	}
	seedChainKernel(t, r, nodeID, kernel.TypeXray)
	seedChainKernel(t, r, nodeID, kernel.TypeSingbox)
	rc := &r1b3CommandRecorder{}
	connectR1B3Agent(t, ts, "front-secret", rc)
	waitForCondition(t, 5*time.Second, func() bool { return server.IsNodeOnline(nodeID) }, "mock agent online")

	if _, _, err := h.deleteNodeByID(nodeID); err != nil {
		t.Fatalf("deleteNodeByID: %v", err)
	}
	if node, err := r.GetNodeByID(nodeID); err != nil || node != nil {
		t.Fatalf("node row must be deleted, got=%v err=%v", node, err)
	}
	removed := map[string]bool{}
	for _, cmd := range rc.snapshot() {
		if cmd.Type != "KernelControl" {
			continue
		}
		var data struct {
			Type   string `json:"type"`
			Action string `json:"action"`
		}
		if err := json.Unmarshal(cmd.Data, &data); err != nil {
			continue
		}
		if data.Action == "remove" {
			removed[data.Type] = true
		}
	}
	if !removed[kernel.TypeXray] || !removed[kernel.TypeSingbox] {
		t.Fatalf("E1-09: node delete must dispatch KernelControl remove for every kernel, got %v", removed)
	}
}

func TestFixCascadeDeleteTunnelByIDOutcomeAndBestExit(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/fix-cascade-tunnel-outcome.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	h.bestExit = newBestExitManager()
	h.bestExit.decisions[bestExitOwnerKey{TunnelID: 6, OwnerNodeID: 1}] = &bestExitDecision{}
	now := time.Now().UnixMilli()
	nodeID := seedRelayTargetNode(t, r, "front", "198.51.100.1")
	if err := r.DB().Create(&model.Tunnel{ID: 6, Name: "t6", Type: 1, CreatedTime: now, UpdatedTime: now}).Error; err != nil {
		t.Fatal(err)
	}
	// 61 活调度墓碑（保留）；62 已耗尽墓碑（终删）。
	for _, seed := range []struct {
		id     int64
		nextAt int64
	}{
		{61, now + 3600_000},
		{62, 0},
	} {
		if err := r.DB().Create(&model.Forward{ID: seed.id, UserID: 1, Name: "f", TunnelID: 6, Status: model.ForwardStatusPendingDelete, CreatedTime: now, UpdatedTime: now}).Error; err != nil {
			t.Fatal(err)
		}
		if err := r.DB().Create(&model.NftPendingDelete{ForwardID: seed.id, NodeID: nodeID, NextRetryAt: seed.nextAt, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
			t.Fatal(err)
		}
	}

	outcome, err := h.deleteTunnelByIDWithOutcome(6)
	if err != nil {
		t.Fatalf("deleteTunnelByIDWithOutcome: %v", err)
	}
	if len(outcome.FinalizedPendingForwardIDs) != 1 || outcome.FinalizedPendingForwardIDs[0] != 62 {
		t.Fatalf("E1-01: finalized = %v, want [62]", outcome.FinalizedPendingForwardIDs)
	}
	if len(outcome.RetainedPendingForwardIDs) != 1 || outcome.RetainedPendingForwardIDs[0] != 61 {
		t.Fatalf("E1-01: retained = %v, want [61]", outcome.RetainedPendingForwardIDs)
	}
	var count int64
	if err := r.DB().Model(&model.Forward{}).Where("id = ?", 62).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("E1-01: finalized tombstone row must be gone, count=%d err=%v", count, err)
	}
	if err := r.DB().Model(&model.Forward{}).Where("id = ?", 61).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("E1-01: retained tombstone row must survive, count=%d err=%v", count, err)
	}
	if len(h.bestExit.decisions) != 0 {
		t.Fatalf("E1-02: best-exit decisions must be cleared on tunnel delete, left %v", h.bestExit.decisions)
	}
}

// seedExhaustedTombstone 把一条 nft 转发置为墓碑并把台账年龄推过
// 24h 总帽、下次重试已到期。
func seedExhaustedTombstone(t *testing.T, h *Handler, forward *forwardRecord, nodeID int64) {
	t.Helper()
	now := time.Now()
	if err := h.repo.MarkForwardPendingDelete(forward.ID, nodeID, now); err != nil {
		t.Fatalf("mark pending delete: %v", err)
	}
	aged := now.Add(-25 * time.Hour).UnixMilli()
	if err := h.repo.DB().Exec(`UPDATE nft_pending_delete SET created_at = ?, updated_at = ?, next_retry_at = ? WHERE forward_id = ?`,
		aged, aged, now.Add(-time.Minute).UnixMilli(), forward.ID).Error; err != nil {
		t.Fatalf("age ledger: %v", err)
	}
}

func TestFixCascadePendingDeleteExhaustedOrphanFinalized(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	forward := seedNftForwardForDelete(t, h, fixture)
	manager := &fakeNftablesManager{reconcileErr: errFakeSSHDown}
	h.nftablesManager = newSerializedNftManager(manager)
	seedExhaustedTombstone(t, h, forward, fixture.nodeID)

	// 删隧道：活调度墓碑按分流保留（D7 不破），隧道行删除。
	if err := h.repo.DeleteTunnelCascade(forward.TunnelID); err != nil {
		t.Fatalf("DeleteTunnelCascade: %v", err)
	}
	if fr, err := h.repo.GetForwardRecord(forward.ID); err != nil || fr == nil {
		t.Fatalf("tombstone must survive tunnel cascade while scheduled, fr=%v err=%v", fr, err)
	}
	// 重试持续失败、本次记录耗尽（台账已过 24h 总帽）：隧道已不存在
	// → 终删，不再无限悬空。
	h.retryNftPendingDeletes(time.Now())
	if fr, err := h.repo.GetForwardRecord(forward.ID); err != nil || fr != nil {
		t.Fatalf("E1-01: exhausted orphan tombstone must be finalized, fr=%v err=%v", fr, err)
	}
	if pending, err := h.repo.GetNftPendingDelete(forward.ID); err != nil || pending != nil {
		t.Fatalf("E1-01: ledger row must be gone after finalization, pending=%v err=%v", pending, err)
	}
}

func TestFixCascadePendingDeleteExhaustedKeptWhileTunnelExists(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	forward := seedNftForwardForDelete(t, h, fixture)
	manager := &fakeNftablesManager{reconcileErr: errFakeSSHDown}
	h.nftablesManager = newSerializedNftManager(manager)
	seedExhaustedTombstone(t, h, forward, fixture.nodeID)

	h.retryNftPendingDeletes(time.Now())
	// 隧道尚存：维持现行停放口径（next_retry_at=0 等人工处理），不终删。
	if fr, err := h.repo.GetForwardRecord(forward.ID); err != nil || fr == nil {
		t.Fatalf("tombstone must be kept while its tunnel exists, fr=%v err=%v", fr, err)
	}
	pending, err := h.repo.GetNftPendingDelete(forward.ID)
	if err != nil || pending == nil || pending.NextRetryAt != 0 {
		t.Fatalf("exhausted ledger must be parked with next_retry_at=0, pending=%v err=%v", pending, err)
	}
}

func TestFixCascadeSweepStaleFlowLegacyBatchDedupe(t *testing.T) {
	h := &Handler{repo: openMaintenanceLoopRepo(t, "fix-cascade-dedupe-sweep.db")}
	now := time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC)
	stale := now.UnixMilli() - repo.FlowLegacyBatchDedupeRetention - 1000
	for _, row := range []repo.FlowLegacyBatchDedupe{
		{NodeID: 1, BatchHash: "old", FirstSeen: stale},
		{NodeID: 2, BatchHash: "old2", FirstSeen: stale},
		{NodeID: 1, BatchHash: "fresh", FirstSeen: now.UnixMilli()},
	} {
		if err := h.repo.DB().Create(&row).Error; err != nil {
			t.Fatalf("seed dedupe row: %v", err)
		}
	}
	h.sweepStaleFlowLegacyBatchDedupe(now)
	var remaining []repo.FlowLegacyBatchDedupe
	if err := h.repo.DB().Find(&remaining).Error; err != nil {
		t.Fatalf("list dedupe rows: %v", err)
	}
	if len(remaining) != 1 || remaining[0].BatchHash != "fresh" {
		t.Fatalf("E1-04: sweep must delete only out-of-window rows, remaining=%v", remaining)
	}
}
