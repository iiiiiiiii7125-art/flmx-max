package handler

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// FIX-B A4-001：日/月配额执法 enforceUserQuotaIfNeeded 原先无 nftables 分支——
// 对 nft 转发调 controlForwardServices("PauseService") 必然 not-found，函数仅
// 记日志 continue，转发不暂停、规则不撤、继续转发继续扣费，且不进 pausedIDs。
// 本测试要求：nft 转发与 gost 同语义被真暂停（按节点 reconcile-exclude）、
// DB 置 paused、且进入配额恢复依据（PausedForwardIDs）。

func TestEnforceUserQuotaPausesNftForwards(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	manager := &fakeNftablesManager{}
	h.nftablesManager = newSerializedNftManager(manager)
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	tunnelID := seedTunnelForNftables(t, h, "nft-tunnel-enforce", fixture.nodeID)
	f1 := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:9001", 21000)
	f2 := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:9002", 21001)

	r := h.repo
	now := time.Now()
	nowMs := now.UnixMilli()
	if _, err := r.AddUserQuotaUsage(1, 0, now); err != nil {
		t.Fatalf("init quota row: %v", err)
	}
	if err := r.DB().Exec(`UPDATE user_quota SET daily_limit_gb = 1, monthly_limit_gb = 0, daily_used_bytes = ?, monthly_used_bytes = 0 WHERE user_id = 1`,
		int64(3)<<30).Error; err != nil {
		t.Fatalf("seed quota usage: %v", err)
	}
	view, err := r.GetUserQuotaView(1, now)
	if err != nil || view == nil {
		t.Fatalf("quota view: %v / %v", view, err)
	}
	if !isUserQuotaExceeded(view) {
		t.Fatalf("seeded quota must be exceeded: %+v", view)
	}

	h.enforceUserQuotaIfNeeded(1, view)

	// 同节点只 reconcile 一次，且 plan 不含被暂停转发（规则真移除）。
	if manager.reconcileHit != 1 {
		t.Fatalf("expected 1 reconcile for nft node, got %d", manager.reconcileHit)
	}
	for _, rule := range manager.lastPlan.Rules {
		if rule.ForwardID == f1.ID || rule.ForwardID == f2.ID {
			t.Fatalf("quota-exceeded nft forward %d still in plan (not paused)", rule.ForwardID)
		}
	}
	// DB 状态 paused。
	for _, f := range []*forwardRecord{f1, f2} {
		fr, _ := h.repo.GetForwardRecord(f.ID)
		if fr.Status != model.ForwardStatusPaused {
			t.Fatalf("expected paused, got %d for forward %d", fr.Status, f.ID)
		}
	}
	// 恢复依据：PausedForwardIDs 落库且含两条转发（恢复侧据此解暂停）。
	view2, err := r.GetUserQuotaView(1, time.Now())
	if err != nil || view2 == nil {
		t.Fatalf("quota view after enforce: %v / %v", view2, err)
	}
	if view2.DisabledByQuota != 1 {
		t.Fatalf("expected disabled_by_quota=1, got %d", view2.DisabledByQuota)
	}
	for _, f := range []*forwardRecord{f1, f2} {
		want := strconv.FormatInt(f.ID, 10)
		found := false
		for _, part := range strings.Split(view2.PausedForwardIDs, ",") {
			if strings.TrimSpace(part) == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("paused forward %d missing from PausedForwardIDs %q", f.ID, view2.PausedForwardIDs)
		}
	}
	_ = nowMs
}

func TestEnforceUserQuotaNftReconcileFailKeepsActive(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	manager := &fakeNftablesManager{reconcileErr: errFakeSSHDown}
	h.nftablesManager = newSerializedNftManager(manager)
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	tunnelID := seedTunnelForNftables(t, h, "nft-tunnel-enforce-fail", fixture.nodeID)
	f1 := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:9001", 21010)

	r := h.repo
	now := time.Now()
	if _, err := r.AddUserQuotaUsage(1, 0, now); err != nil {
		t.Fatalf("init quota row: %v", err)
	}
	if err := r.DB().Exec(`UPDATE user_quota SET daily_limit_gb = 1, monthly_limit_gb = 0, daily_used_bytes = ?, monthly_used_bytes = 0 WHERE user_id = 1`,
		int64(3)<<30).Error; err != nil {
		t.Fatalf("seed quota usage: %v", err)
	}
	view, err := r.GetUserQuotaView(1, now)
	if err != nil || view == nil {
		t.Fatalf("quota view: %v / %v", view, err)
	}

	h.enforceUserQuotaIfNeeded(1, view)

	fr, _ := h.repo.GetForwardRecord(f1.ID)
	if fr.Status != model.ForwardStatusActive {
		t.Fatalf("expected active when nft reconcile fails, got %d", fr.Status)
	}
	view2, err := r.GetUserQuotaView(1, time.Now())
	if err != nil || view2 == nil {
		t.Fatalf("quota view after enforce: %v / %v", view2, err)
	}
	if strings.Contains(view2.PausedForwardIDs, strconv.FormatInt(f1.ID, 10)) {
		t.Fatalf("failed nft forward %d must not be in PausedForwardIDs %q", f1.ID, view2.PausedForwardIDs)
	}
}
