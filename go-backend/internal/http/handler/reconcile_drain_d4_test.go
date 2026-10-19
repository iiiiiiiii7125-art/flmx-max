package handler

import (
	"testing"
	"time"

	runtimenft "go-backend/internal/runtime/nftables"
	"go-backend/internal/store/model"
)

// R5-B3 D1-004/D8: reconcile（delete table + nft -f）清零计数器，
// 重建前必须 best-effort drain。drain 失败不阻塞 apply。

func TestReconcileDrainsCountersBeforeRebuild(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	manager := &fakeNftablesManager{}
	h.nftablesManager = newSerializedNftManager(manager)
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	tunnelID := seedTunnelForNftables(t, h, "nft-tunnel-drain", fixture.nodeID)
	forward := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:7001", 20000)

	// 第一次 reconcile：建立 binding（含 rule hash），使采样能关联。
	if err := h.syncNftablesNode(fixture.nodeID); err != nil {
		t.Fatalf("initial reconcile: %v", err)
	}
	if manager.reconcileHit != 1 {
		t.Fatalf("expected 1 reconcile, got %d", manager.reconcileHit)
	}

	// 模拟节点上有流量：counter 采样。
	ruleHash := ""
	if len(manager.lastPlan.Rules) > 0 {
		ruleHash = runtimenft.PlanHashes(manager.lastPlan)[forward.ID]
	}
	manager.counterSamples = []runtimenft.CounterSample{
		{ForwardID: forward.ID, Direction: "to-target", Protocol: "tcp", Bytes: 5000, Packets: 50},
	}
	_ = ruleHash

	// 第二次 reconcile：应先 drain（CollectCounters 被调用），流量落库后再重建。
	collectBefore := manager.collectHit
	if err := h.syncNftablesNode(fixture.nodeID); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if manager.collectHit <= collectBefore {
		t.Fatalf("drain did not run CollectCounters before rebuild")
	}
	if manager.reconcileHit != 2 {
		t.Fatalf("expected 2 reconciles, got %d", manager.reconcileHit)
	}

	// 流量已上报（drain 落库）：基线推进到 5000，证明不是被删表清零丢掉。
	// （若无 drain，delete table 会把计数器清零，基线停在 0，下次采样 5000
	// 会被当成 reset 重新基线 → 5000 字节永久丢失。）
	states, err := h.repo.GetNftCounterStatesByNode(fixture.nodeID)
	if err != nil {
		t.Fatalf("get states: %v", err)
	}
	found := false
	for _, s := range states {
		if s.ForwardID == forward.ID && s.Bytes == 5000 {
			found = true
		}
	}
	if !found {
		t.Fatalf("drained traffic lost (baseline not at 5000): %+v", states)
	}
}

func TestReconcileDrainFailureDoesNotBlockApply(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	manager := &fakeNftablesManager{collectErr: errFakeSSHDown}
	h.nftablesManager = newSerializedNftManager(manager)
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	tunnelID := seedTunnelForNftables(t, h, "nft-tunnel-draingap", fixture.nodeID)
	seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:7002", 20000)

	// drain 失败（CollectCounters 报错），apply 仍必须成功（记 gap）。
	if err := h.syncNftablesNode(fixture.nodeID); err != nil {
		t.Fatalf("reconcile should succeed despite drain failure, got %v", err)
	}
	if manager.reconcileHit != 1 {
		t.Fatalf("expected apply to run, got %d hits", manager.reconcileHit)
	}
}

func TestDrainAdvancesBaseline(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	manager := &fakeNftablesManager{}
	h.nftablesManager = newSerializedNftManager(manager)
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	tunnelID := seedTunnelForNftables(t, h, "nft-tunnel-baseline", fixture.nodeID)
	forward := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:7003", 20000)

	if err := h.syncNftablesNode(fixture.nodeID); err != nil {
		t.Fatalf("initial reconcile: %v", err)
	}
	manager.counterSamples = []runtimenft.CounterSample{
		{ForwardID: forward.ID, Direction: "to-target", Protocol: "tcp", Bytes: 8000, Packets: 80},
	}

	now := time.Now()
	if !h.drainNftCountersBeforeReconcile(fixture.nodeID, mustGetSSHConfig(t, h, fixture.nodeID), now) {
		t.Fatalf("drain failed")
	}
	// 基线已推进到 8000：再次 drain 同样采样不得产生重复计费。
	userMid, _ := h.repo.GetUserByID(1)
	if !h.drainNftCountersBeforeReconcile(fixture.nodeID, mustGetSSHConfig(t, h, fixture.nodeID), now) {
		t.Fatalf("second drain failed")
	}
	userAfter, _ := h.repo.GetUserByID(1)
	if userAfter.InFlow != userMid.InFlow {
		t.Fatalf("double counting: mid=%d after=%d", userMid.InFlow, userAfter.InFlow)
	}
	// 且 counter state 基线确为 8000。
	states, _ := h.repo.GetNftCounterStatesByNode(fixture.nodeID)
	found := false
	for _, s := range states {
		if s.ForwardID == forward.ID && s.Bytes == 8000 {
			found = true
		}
	}
	if !found {
		t.Fatalf("baseline not advanced to 8000: %+v", states)
	}
}

func mustGetSSHConfig(t *testing.T, h *Handler, nodeID int64) *model.NodeSSHConfig {
	t.Helper()
	cfg, err := h.repo.GetNodeSSHConfig(nodeID)
	if err != nil {
		t.Fatalf("get ssh config: %v", err)
	}
	return cfg
}
