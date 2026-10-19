package handler

import (
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R5-B5 D15: pending-delete 行不得被任何激活路径复活。
// 可达性证据：T-C 发现 applyUserQuotaRelease 用无过滤的 getForwardRecord。

func TestQuotaReleaseDoesNotResurrectPending(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	manager := &fakeNftablesManager{}
	h.nftablesManager = newSerializedNftManager(manager)
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	tunnelID := seedTunnelForNftables(t, h, "nft-tunnel-d15", fixture.nodeID)
	forward := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:8001")

	// 标记为 pending-delete。
	nowTime := time.Now()
	now := nowTime.UnixMilli()
	if err := h.repo.MarkForwardPendingDelete(forward.ID, fixture.nodeID, nowTime); err != nil {
		t.Fatalf("mark pending: %v", err)
	}

	// 配额释放（含该 pending 转发）：不得复活。
	release := &repo.UserQuotaRelease{
		UserID:      1,
		UnblockUser: true,
		ForwardIDs:  []int64{forward.ID},
	}
	h.applyUserQuotaRelease(release, now)

	// 状态仍为 pending，未被置为 active。
	fr := mustGetForward(t, h, forward.ID)
	if fr.Status != model.ForwardStatusPendingDelete {
		t.Fatalf("pending resurrected to %d", fr.Status)
	}
	// 未产生 reconcile（规则未被重新加入）。
	if manager.reconcileCount() != 0 {
		t.Fatalf("reconcile should not run for pending, got %d", manager.reconcileCount())
	}
}

func TestActivateNormalPausedStillWorks(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	tunnelID := seedTunnelForNftables(t, h, "nft-tunnel-d15b", fixture.nodeID)
	forward := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:8002")

	now := time.Now().UnixMilli()
	if err := h.repo.UpdateForwardStatus(forward.ID, model.ForwardStatusPaused, now); err != nil {
		t.Fatalf("pause: %v", err)
	}

	// 正常 paused 行可被激活（不得误伤）。
	if err := h.ActivateForwardIfNotPending(forward.ID, now); err != nil {
		t.Fatalf("activate paused: %v", err)
	}
	fr := mustGetForward(t, h, forward.ID)
	if fr.Status != model.ForwardStatusActive {
		t.Fatalf("expected active, got %d", fr.Status)
	}
}

func mustGetForward(t *testing.T, h *Handler, id int64) *forwardRecord {
	t.Helper()
	fr, err := h.getForwardRecord(id)
	if err != nil {
		t.Fatalf("get forward: %v", err)
	}
	return fr
}
