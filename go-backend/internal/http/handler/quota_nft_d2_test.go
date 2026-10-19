package handler

import (
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R5-B3 D1-002: 配额超限时 nft 转发的规则必须真正移除（原 pauseForwardRecords
// 只调了 gost 的 controlForwardServices，nft 规则残留 → 继续产生流量、继续扣配额）。
// 可达性证据：原实现对 nft 转发无任何数据面操作。

func TestQuotaPauseNftRemovesRules(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	manager := &fakeNftablesManager{}
	h.nftablesManager = newSerializedNftManager(manager)
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	tunnelID := seedTunnelForNftables(t, h, "nft-tunnel-quota", fixture.nodeID)
	f1 := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:9001", 20000)
	f2 := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:9002", 20001)

	now := time.Now().UnixMilli()
	h.pauseForwardRecords([]forwardRecord{*f1, *f2}, now)

	// R1: 同节点多转发只触发 1 次 reconcile。
	if manager.reconcileHit != 1 {
		t.Fatalf("expected 1 reconcile for same node, got %d", manager.reconcileHit)
	}
	// plan 不含被暂停的两个转发（规则实际移除）。
	for _, rule := range manager.lastPlan.Rules {
		if rule.ForwardID == f1.ID || rule.ForwardID == f2.ID {
			t.Fatalf("quota-paused forward %d still in plan", rule.ForwardID)
		}
	}
	// DB 状态为 paused。
	for _, f := range []*forwardRecord{f1, f2} {
		fr, _ := h.repo.GetForwardRecord(f.ID)
		if fr.Status != model.ForwardStatusPaused {
			t.Fatalf("expected paused, got %d for %d", fr.Status, f.ID)
		}
	}
}

func TestQuotaPauseNftReconcileFailKeepsActive(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	forward := seedNftForwardForDelete(t, h, fixture)
	manager := &fakeNftablesManager{reconcileErr: errFakeSSHDown}
	h.nftablesManager = newSerializedNftManager(manager)

	h.pauseForwardRecords([]forwardRecord{*forward}, time.Now().UnixMilli())

	// 失败时 DB 保持 active（下次采集循环重试），不静默计费。
	fr, _ := h.repo.GetForwardRecord(forward.ID)
	if fr.Status != model.ForwardStatusActive {
		t.Fatalf("expected active on failure, got %d", fr.Status)
	}
}
