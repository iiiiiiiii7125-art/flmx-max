package handler

import (
	"net/http/httptest"
	"testing"

	"go-backend/internal/store/model"
)

// R5-B3 D1-012: pause/resume 数据面优先（D6）。
// 可达性证据：原 forwardPause 先写 DB（_ = UpdateForwardStatus）后 reconcile，
// reconcile 失败即“DB=paused 但规则仍在”——继续计费且用户以为已停。

func TestForwardPauseNftDataPlaneFirst(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	forward := seedNftForwardForDelete(t, h, fixture)
	manager := h.nftablesManager.(*fakeNftablesManager)

	req := newAuthenticatedJSONRequest(t, map[string]any{"id": forward.ID})
	res := httptest.NewRecorder()
	h.forwardPause(res, req)

	if code, _ := decodePendingEnvelopeCode(t, res); code != 0 {
		t.Fatalf("expected success, got %d", code)
	}
	// plan 不得含该转发（规则已下线）。
	for _, rule := range manager.lastPlan.Rules {
		if rule.ForwardID == forward.ID {
			t.Fatalf("paused forward still in plan")
		}
	}
	fr, _ := h.repo.GetForwardRecord(forward.ID)
	if fr.Status != model.ForwardStatusPaused {
		t.Fatalf("expected status=0, got %d", fr.Status)
	}
}

func TestForwardPauseNftReconcileFailKeepsActive(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	forward := seedNftForwardForDelete(t, h, fixture)
	manager := &fakeNftablesManager{reconcileErr: errFakeSSHDown}
	h.nftablesManager = manager

	req := newAuthenticatedJSONRequest(t, map[string]any{"id": forward.ID})
	res := httptest.NewRecorder()
	h.forwardPause(res, req)

	if code, _ := decodePendingEnvelopeCode(t, res); code == 0 {
		t.Fatalf("expected error when reconcile fails")
	}
	// D6: DB 保持 active，不允许“DB=paused 但规则仍在”。
	fr, _ := h.repo.GetForwardRecord(forward.ID)
	if fr == nil || fr.Status != model.ForwardStatusActive {
		t.Fatalf("DB must stay active on reconcile failure, got %+v", fr)
	}
	// 下次 reconcile 可收敛：恢复后 plan 含该转发。
	manager.reconcileErr = nil
	if err := h.syncNftablesNode(fixture.nodeID); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	found := false
	for _, rule := range manager.lastPlan.Rules {
		if rule.ForwardID == forward.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("forward missing from plan after recovery")
	}
}

func TestForwardResumeNftReconcileFailKeepsPaused(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	forward := seedNftForwardForDelete(t, h, fixture)
	manager := h.nftablesManager.(*fakeNftablesManager)

	// 先暂停成功。
	pauseReq := newAuthenticatedJSONRequest(t, map[string]any{"id": forward.ID})
	h.forwardPause(httptest.NewRecorder(), pauseReq)

	// 恢复时节点离线。
	manager.reconcileErr = errFakeSSHDown
	resumeReq := newAuthenticatedJSONRequest(t, map[string]any{"id": forward.ID})
	res := httptest.NewRecorder()
	h.forwardResume(res, resumeReq)

	if code, _ := decodePendingEnvelopeCode(t, res); code == 0 {
		t.Fatalf("expected error when reconcile fails")
	}
	// D6: DB 保持 paused，无黑洞（规则未加上但 DB 声称 active 才是黑洞）。
	fr, _ := h.repo.GetForwardRecord(forward.ID)
	if fr == nil || fr.Status != model.ForwardStatusPaused {
		t.Fatalf("DB must stay paused on reconcile failure, got %+v", fr)
	}
}

func TestForwardResumeNftAddsRule(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	forward := seedNftForwardForDelete(t, h, fixture)
	manager := h.nftablesManager.(*fakeNftablesManager)

	pauseReq := newAuthenticatedJSONRequest(t, map[string]any{"id": forward.ID})
	h.forwardPause(httptest.NewRecorder(), pauseReq)

	resumeReq := newAuthenticatedJSONRequest(t, map[string]any{"id": forward.ID})
	res := httptest.NewRecorder()
	h.forwardResume(res, resumeReq)

	if code, _ := decodePendingEnvelopeCode(t, res); code != 0 {
		t.Fatalf("expected success, got %d", code)
	}
	found := false
	for _, rule := range manager.lastPlan.Rules {
		if rule.ForwardID == forward.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("resumed forward not in plan")
	}
	fr, _ := h.repo.GetForwardRecord(forward.ID)
	if fr.Status != model.ForwardStatusActive {
		t.Fatalf("expected status=1, got %d", fr.Status)
	}
}
