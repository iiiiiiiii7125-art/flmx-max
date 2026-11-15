package handler

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
)

// ---- E-02 / E-03: companion split visibility + materialization ----
//
// E-02: on a 2.1.13 agent the split is silently absent — the companion
// stays on the main kernel, the repush receipt says redeployed:true
// and kernel/list shows only singbox, with no surface saying the
// split did not take effect because the agent is too old. The
// fail-closed behavior itself is correct; the fix surfaces the
// reason on the repush receipt and on the main kernel view.
//
// E-03: the split only materialized on agent reconnect / re-push —
// reconcileSSMetering and applyKernelsForInbounds never ensured it,
// so a companion created on a split-capable agent waited on the main
// kernel until a manual restart. The fix ensures the split on both
// paths, so it materializes on the same beat / push.

// E-03 (apply path): pushing an origin whose companion still sits on
// the main kernel must move the companion onto the companion kernel
// first and apply both kernels — no reconnect needed.
func TestE03ApplyMaterializesSplitWithoutReconnect(t *testing.T) {
	a := newNodeSwapFakeAgent(t)
	origin, comp := c03bSplitFixtureOnFakeAgent(t, a, false)
	if comp.KernelID != origin.KernelID {
		t.Fatalf("fixture must start pre-split (companion on main kernel), got companion kernel %d origin kernel %d", comp.KernelID, origin.KernelID)
	}

	if err := a.h.applyKernelsForInbounds([]int64{origin.ID}); err != nil {
		t.Fatalf("E-03 push: applyKernelsForInbounds must succeed, got %v", err)
	}
	after, err := a.h.repo.GetCompanionInbound(origin.ID)
	if err != nil || after == nil {
		t.Fatalf("E-03 push: companion must still exist: %v %v", after, err)
	}
	if after.KernelID == origin.KernelID {
		t.Fatalf("E-03 push: companion must leave the main kernel %d after the push, still on %d", origin.KernelID, after.KernelID)
	}
	types := c03bKernelApplyTypes(t, a)
	hasMain, hasCompanion := false, false
	for _, typ := range types {
		if typ == kernel.TypeSingbox {
			hasMain = true
		}
		if typ == kernel.TypeSingboxCompanion {
			hasCompanion = true
		}
	}
	if !hasMain || !hasCompanion {
		t.Fatalf("E-03 push: expected KernelApply for both main and companion kernels, got %v", types)
	}
}

// E-03 (reconcile path): the sync beat must move a companion created
// on a split-capable agent onto the companion kernel by itself.
func TestE03ReconcileMaterializesSplitWithoutReconnect(t *testing.T) {
	a := newNodeSwapFakeAgent(t)
	origin, comp := c03bSplitFixtureOnFakeAgent(t, a, false)
	nowMs := time.Now().UnixMilli()
	sub := &model.Subscription{Name: "e03-a", Kind: model.SubscriptionShare, Token: strings.Repeat("e", 32),
		UUID: "u-e03-a", Password: "e03-password-a", Enabled: 1, SourceAll: 1, AppliedActive: 1,
		CreatedTime: nowMs, UpdatedTime: nowMs}
	if err := a.h.repo.SaveSubscription(sub); err != nil {
		t.Fatal(err)
	}
	if comp.KernelID != origin.KernelID {
		t.Fatalf("fixture must start pre-split, got companion kernel %d origin kernel %d", comp.KernelID, origin.KernelID)
	}

	a.h.reconcileSSMetering()
	after, err := a.h.repo.GetCompanionInbound(origin.ID)
	if err != nil || after == nil {
		t.Fatalf("E-03 reconcile: companion must still exist: %v %v", after, err)
	}
	if after.KernelID == origin.KernelID {
		t.Fatalf("E-03 reconcile: companion must leave the main kernel %d after the beat, still on %d", origin.KernelID, after.KernelID)
	}
	types := c03bKernelApplyTypes(t, a)
	hasCompanion := false
	for _, typ := range types {
		if typ == kernel.TypeSingboxCompanion {
			hasCompanion = true
		}
	}
	if !hasCompanion {
		t.Fatalf("E-03 reconcile: expected a companion KernelApply, got %v", types)
	}
}

// E-03 guard: on an old agent the ensure stays a no-op — the
// companion stays on the main kernel (fail-closed) and no companion
// kernel row is created.
func TestE03OldAgentStaysOnMainKernel(t *testing.T) {
	a := newNodeSwapFakeAgent(t)
	origin, comp := c03bSplitFixtureOnFakeAgent(t, a, false)
	if err := a.h.repo.DB().Exec("UPDATE node SET version='2.1.13' WHERE id=1").Error; err != nil {
		t.Fatal(err)
	}

	if err := a.h.applyKernelsForInbounds([]int64{origin.ID}); err != nil {
		t.Fatalf("E-03 old agent: applyKernelsForInbounds must succeed, got %v", err)
	}
	after, err := a.h.repo.GetCompanionInbound(origin.ID)
	if err != nil || after == nil {
		t.Fatalf("E-03 old agent: companion must still exist: %v %v", after, err)
	}
	if after.KernelID != origin.KernelID {
		t.Fatalf("E-03 old agent: companion must stay on the main kernel %d, moved to %d", origin.KernelID, after.KernelID)
	}
	if _, active := a.h.companionKernelIDForNode(1); active {
		t.Fatalf("E-03 old agent: no companion kernel row may be created for a 2.1.13 agent")
	}
	types := c03bKernelApplyTypes(t, a)
	if len(types) != 1 || types[0] != kernel.TypeSingbox {
		t.Fatalf("E-03 old agent: expected a single main KernelApply, got %v", types)
	}
	_ = comp
}

// E-02 (reason): an old agent with enabled companions reports why
// the split is absent; a new-enough agent, an active split, or no
// companions report nothing.
func TestE02SplitPendingReason(t *testing.T) {
	a := newNodeSwapFakeAgent(t)
	origin, _ := c03bSplitFixtureOnFakeAgent(t, a, false)
	if err := a.h.repo.DB().Exec("UPDATE node SET version='2.1.13' WHERE id=1").Error; err != nil {
		t.Fatal(err)
	}
	reason := a.h.companionSplitPendingReason(1)
	if !strings.Contains(reason, "2.1.13") || !strings.Contains(reason, companionAgentMinVersion) {
		t.Fatalf("E-02: 2.1.13 node with companions must report the version reason, got %q", reason)
	}

	// New-enough agent: the split is (transiently) unmaterialized,
	// not version-blocked — E-03 materializes it on the next beat,
	// so no version note is owed.
	if err := a.h.repo.DB().Exec("UPDATE node SET version='2.1.14' WHERE id=1").Error; err != nil {
		t.Fatal(err)
	}
	if got := a.h.companionSplitPendingReason(1); got != "" {
		t.Fatalf("E-02: 2.1.14 node must report no pending reason, got %q", got)
	}

	// Active split: nothing pending even on an old agent (the row
	// exists, the split is in effect).
	b := newNodeSwapFakeAgent(t)
	c03bSplitFixtureOnFakeAgent(t, b, true)
	if err := b.h.repo.DB().Exec("UPDATE node SET version='2.1.13' WHERE id=1").Error; err != nil {
		t.Fatal(err)
	}
	if got := b.h.companionSplitPendingReason(1); got != "" {
		t.Fatalf("E-02: active split must report no pending reason, got %q", got)
	}

	// No companions at all: nothing to report.
	c := newNodeSwapFakeAgent(t)
	if err := c.h.repo.DB().Exec("UPDATE node SET version='2.1.13' WHERE id=1").Error; err != nil {
		t.Fatal(err)
	}
	if got := c.h.companionSplitPendingReason(1); got != "" {
		t.Fatalf("E-02: node without companions must report no pending reason, got %q", got)
	}
	_ = origin
}

// E-02 (repush receipt): the repush of a 2.1.13 node with companions
// must warn that the split did not take effect, in the warning text
// and in the data payload — not a clean redeployed:true.
func TestE02RepushReceiptWarnsOldAgent(t *testing.T) {
	a := newNodeSwapFakeAgent(t)
	c03bSplitFixtureOnFakeAgent(t, a, false)
	if err := a.h.repo.DB().Exec("UPDATE node SET version='2.1.13' WHERE id=1").Error; err != nil {
		t.Fatal(err)
	}

	body := r2b2AuthPost(t, a.h.nodeConnlogRepush, "/api/v1/node/connlog-repush", map[string]interface{}{"nodeId": 1}, "")
	var env struct {
		Code    int                    `json:"code"`
		Msg     string                 `json:"msg"`
		Warning string                 `json:"warning"`
		Data    map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("decode receipt: %v (body=%s)", err, body)
	}
	if env.Code != 0 {
		t.Fatalf("E-02 repush receipt code = %d, want 0 (body=%s)", env.Code, body)
	}
	if !strings.Contains(env.Warning, "2.1.13") || !strings.Contains(env.Warning, companionAgentMinVersion) {
		t.Fatalf("E-02 repush receipt warning must name the version gap, got warning=%q (body=%s)", env.Warning, body)
	}
	note, _ := env.Data["companionSplitNote"].(string)
	if !strings.Contains(note, "2.1.13") {
		t.Fatalf("E-02 repush receipt data.companionSplitNote must carry the reason, got %q (body=%s)", note, body)
	}
}

// E-02 (kernel view): the main sing-box row carries the pending
// reason so the node page shows why the split is absent.
func TestE02KernelViewCarriesSplitNote(t *testing.T) {
	a := newNodeSwapFakeAgent(t)
	c03bSplitFixtureOnFakeAgent(t, a, false)
	if err := a.h.repo.DB().Exec("UPDATE node SET version='2.1.13' WHERE id=1").Error; err != nil {
		t.Fatal(err)
	}

	body := r2b2AuthPost(t, a.h.kernelList, "/api/v1/kernel/list", map[string]interface{}{"nodeId": 1}, "")
	var env struct {
		Code int `json:"code"`
		Data []struct {
			KernelType         string `json:"kernelType"`
			CompanionSplitNote string `json:"companionSplitNote"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("decode kernel list: %v (body=%s)", err, body)
	}
	if env.Code != 0 {
		t.Fatalf("E-02 kernel list code = %d, want 0 (body=%s)", env.Code, body)
	}
	found := false
	for _, v := range env.Data {
		if v.KernelType == kernel.TypeSingbox {
			found = true
			if !strings.Contains(v.CompanionSplitNote, "2.1.13") || !strings.Contains(v.CompanionSplitNote, companionAgentMinVersion) {
				t.Fatalf("E-02 kernel view: main sing-box row must carry the split note, got %q", v.CompanionSplitNote)
			}
		}
	}
	if !found {
		t.Fatalf("E-02 kernel view: no sing-box row in the list (body=%s)", body)
	}
}
