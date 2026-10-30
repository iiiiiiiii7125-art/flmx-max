package handler

import (
	"net/http/httptest"
	"testing"

	"go-backend/internal/store/model"
)

// S-05: nftables 模式下暂停/恢复必须真正使规则失效/恢复。
// 修复前：forwardPause/forwardResume 只调用 controlForwardServices (操作 gost service)，
//
//	nftables 模式下为空操作，暂停后流量仍通。
//
// 修复后：nftables 模式下按目标状态生成 plan 先行 reconcile（数据面优先），
//
//	成功后再写 DB status——暂停的转发被排除出 plan、恢复后回到 plan。
//
// X02-001（审计闭环 Round 1 / C17）：本文件两条用例原为纯 t.Log 桩、零断言，
// 回归面实际由 forward_pause_resume_d12_test.go 的真断言守门，桩只剩误导
// 命名与虚报覆盖。现改为走 forwardPause/forwardResume 全链的真断言（夹具与
// helper 复用 D12 / forward_delete_pending_d1_test.go），并补 D12 未覆盖的
// 残余格：重复暂停/恢复的幂等（TestS05_PauseResumeIdempotent）。

func planRuleCountForForward(manager *fakeNftablesManager, forwardID int64) int {
	n := 0
	for _, rule := range manager.lastPlan.Rules {
		if rule.ForwardID == forwardID {
			n++
		}
	}
	return n
}

func TestS05_NftPauseUsesReconcile(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	forward := seedNftForwardForDelete(t, h, fixture)
	manager := h.nftablesManager.Unwrap().(*fakeNftablesManager)

	// 基线：节点同步后 plan 应包含该转发（夹具每转发单端口 = 恰一条规则）。
	if err := h.syncNftablesNode(fixture.nodeID); err != nil {
		t.Fatalf("baseline sync: %v", err)
	}
	if n := planRuleCountForForward(manager, forward.ID); n != 1 {
		t.Fatalf("baseline plan must contain forward %d exactly once, got %d rules", forward.ID, n)
	}

	// 暂停：必须实际触发 reconcile（命中数增加），且 plan 不再含该转发。
	hitsBefore := manager.reconcileHit
	res := httptest.NewRecorder()
	h.forwardPause(res, newAuthenticatedJSONRequest(t, map[string]any{"id": forward.ID}))
	if code, msg := decodePendingEnvelopeCode(t, res); code != 0 {
		t.Fatalf("pause must succeed, got code=%d msg=%q", code, msg)
	}
	if manager.reconcileHit <= hitsBefore {
		t.Fatalf("S-05: pause must reconcile the node (hits %d -> %d)", hitsBefore, manager.reconcileHit)
	}
	if n := planRuleCountForForward(manager, forward.ID); n != 0 {
		t.Fatalf("S-05: paused forward %d still in reconcile plan (%d rules)", forward.ID, n)
	}
	fr, err := h.repo.GetForwardRecord(forward.ID)
	if err != nil || fr == nil {
		t.Fatalf("get forward after pause: %v", err)
	}
	if fr.Status != model.ForwardStatusPaused {
		t.Fatalf("expected DB status paused(%d) after pause, got %d", model.ForwardStatusPaused, fr.Status)
	}

	// 恢复：同样必须触发 reconcile，且 plan 中恰回一条规则（不累积）。
	hitsBefore = manager.reconcileHit
	res = httptest.NewRecorder()
	h.forwardResume(res, newAuthenticatedJSONRequest(t, map[string]any{"id": forward.ID}))
	if code, msg := decodePendingEnvelopeCode(t, res); code != 0 {
		t.Fatalf("resume must succeed, got code=%d msg=%q", code, msg)
	}
	if manager.reconcileHit <= hitsBefore {
		t.Fatalf("S-05: resume must reconcile the node (hits %d -> %d)", hitsBefore, manager.reconcileHit)
	}
	if n := planRuleCountForForward(manager, forward.ID); n != 1 {
		t.Fatalf("S-05: resumed forward %d must appear exactly once in plan, got %d rules", forward.ID, n)
	}
	fr, err = h.repo.GetForwardRecord(forward.ID)
	if err != nil || fr == nil {
		t.Fatalf("get forward after resume: %v", err)
	}
	if fr.Status != model.ForwardStatusActive {
		t.Fatalf("expected DB status active(%d) after resume, got %d", model.ForwardStatusActive, fr.Status)
	}
}

// TestS05_PauseResumeIdempotent 是 X02-001 复核指出的残余缺口：重复暂停 /
// 重复恢复此前无专门真断言，仅靠 reconcile 声明式性质推定。重复操作必须
// 仍返回成功、状态不漂移、plan 不累积重复规则。
func TestS05_PauseResumeIdempotent(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	forward := seedNftForwardForDelete(t, h, fixture)
	manager := h.nftablesManager.Unwrap().(*fakeNftablesManager)

	pause := func() {
		t.Helper()
		res := httptest.NewRecorder()
		h.forwardPause(res, newAuthenticatedJSONRequest(t, map[string]any{"id": forward.ID}))
		if code, msg := decodePendingEnvelopeCode(t, res); code != 0 {
			t.Fatalf("repeated pause must succeed, got code=%d msg=%q", code, msg)
		}
	}
	resume := func() {
		t.Helper()
		res := httptest.NewRecorder()
		h.forwardResume(res, newAuthenticatedJSONRequest(t, map[string]any{"id": forward.ID}))
		if code, msg := decodePendingEnvelopeCode(t, res); code != 0 {
			t.Fatalf("repeated resume must succeed, got code=%d msg=%q", code, msg)
		}
	}
	status := func() int {
		t.Helper()
		fr, err := h.repo.GetForwardRecord(forward.ID)
		if err != nil || fr == nil {
			t.Fatalf("get forward: %v", err)
		}
		return fr.Status
	}

	// 重复暂停：状态保持 paused，plan 始终不含该转发。
	pause()
	pause()
	if got := status(); got != model.ForwardStatusPaused {
		t.Fatalf("double pause must keep status paused, got %d", got)
	}
	if n := planRuleCountForForward(manager, forward.ID); n != 0 {
		t.Fatalf("double pause: forward must stay out of plan, got %d rules", n)
	}

	// 重复恢复：状态保持 active，plan 恰一条规则（声明式同步不累积）。
	resume()
	resume()
	if got := status(); got != model.ForwardStatusActive {
		t.Fatalf("double resume must keep status active, got %d", got)
	}
	if n := planRuleCountForForward(manager, forward.ID); n != 1 {
		t.Fatalf("double resume must not accumulate rules, got %d rules", n)
	}

	// 再暂停一次仍收敛到 paused 且不在 plan。
	pause()
	if got := status(); got != model.ForwardStatusPaused {
		t.Fatalf("pause after double resume must converge to paused, got %d", got)
	}
	if n := planRuleCountForForward(manager, forward.ID); n != 0 {
		t.Fatalf("pause after double resume: forward must be out of plan, got %d rules", n)
	}
}
