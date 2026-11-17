package handler

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
)

// ---- C-03 split follow-up: credential pushes must reach the companion ----
//
// The split moved companions onto their own sing-box instance, but
// applyKernelsForInbounds still pushed only the origin kernels — so a
// subscription disable left the disabled user's credential serving on
// the companion (live repro: q-sub1 disabled, companion config still
// listed both users, no companion KernelApply arrived). The fix expands
// every origin to its companion's kernel inside applyKernelsForInbounds,
// once for all five callers (sync loop, save, delete, rotate,
// respondAfterSubPush).

// c03bSplitFixtureOnFakeAgent seeds main + companion kernels and an
// SS2022 origin + companion on the fake agent's online node (1), stamps
// the node 2.1.14, and (when split) moves the companion onto the
// companion kernel. Shape mirrors c03SplitFixture, on the WS harness
// so KernelApply commands are observable.
func c03bSplitFixtureOnFakeAgent(t *testing.T, a *nodeSwapFakeAgent, split bool) (*model.NodeInbound, *model.NodeInbound) {
	t.Helper()
	r := a.h.repo
	if err := r.DB().Exec("UPDATE node SET version='2.1.14' WHERE id=1").Error; err != nil {
		t.Fatal(err)
	}
	main := seedChainKernel(t, r, 1, kernel.TypeSingbox)
	origin := seedChainInbound(t, r, main, "c03b-ss", kernel.Inbound{Protocol: kernel.ProtocolShadowsocks, Port: 8388,
		Settings: kernel.Settings{Method: meteringMethod, Password: meteringSharedPW}}, 0)
	// Metering must be on: ssMeteringWanted gates demand on it, and a
	// reconcile with it off deletes the companion (its own kernel push).
	origin.MeteringEnabled = 1
	origin.UpdatedTime = time.Now().UnixMilli()
	if err := r.SaveNodeInbound(origin); err != nil {
		t.Fatal(err)
	}
	if !a.h.createCompanion(origin, time.Now().UnixMilli()) {
		t.Fatal("createCompanion must succeed")
	}
	comp, err := r.GetCompanionInbound(origin.ID)
	if err != nil || comp == nil {
		t.Fatalf("companion must be materialized: %v %v", comp, err)
	}
	if split {
		compKernel := seedChainKernel(t, r, 1, kernel.TypeSingboxCompanion)
		comp.KernelID = compKernel.ID
		comp.UpdatedTime = time.Now().UnixMilli()
		if err := r.SaveNodeInbound(comp); err != nil {
			t.Fatal(err)
		}
	}
	return origin, comp
}

// c03bKernelApplyTypes extracts the payload "type" of every KernelApply
// command the fake agent received, in order.
func c03bKernelApplyTypes(t *testing.T, a *nodeSwapFakeAgent) []string {
	t.Helper()
	var types []string
	for _, cmd := range a.commandsOfType("KernelApply") {
		var payload struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(cmd.Data, &payload); err != nil {
			t.Fatalf("decode KernelApply payload: %v", err)
		}
		types = append(types, payload.Type)
	}
	return types
}

// c03bKernelApplyConfig returns the concatenated rendered config of the
// latest KernelApply command whose payload type matches want.
func c03bKernelApplyConfig(t *testing.T, a *nodeSwapFakeAgent, want string) string {
	t.Helper()
	var found string
	for _, cmd := range a.commandsOfType("KernelApply") {
		var payload struct {
			Type  string        `json:"type"`
			Files []kernel.File `json:"files"`
		}
		if err := json.Unmarshal(cmd.Data, &payload); err != nil {
			t.Fatalf("decode KernelApply payload: %v", err)
		}
		if payload.Type != want {
			continue
		}
		var sb strings.Builder
		for _, f := range payload.Files {
			sb.WriteString(f.Content)
		}
		found = sb.String()
	}
	if found == "" {
		t.Fatalf("no KernelApply for type %q", want)
	}
	return found
}

// Push of an origin must apply the companion kernel too after the split.
func TestC03PushAppliesCompanionKernel(t *testing.T) {
	a := newNodeSwapFakeAgent(t)
	origin, _ := c03bSplitFixtureOnFakeAgent(t, a, true)

	if err := a.h.applyKernelsForInbounds([]int64{origin.ID}); err != nil {
		t.Fatalf("C-03 push: applyKernelsForInbounds must succeed, got %v", err)
	}
	types := c03bKernelApplyTypes(t, a)
	if len(types) != 2 {
		t.Fatalf("C-03 push: expected 2 KernelApply commands (main + companion), got %v", types)
	}
	want := map[string]bool{kernel.TypeSingbox: true, kernel.TypeSingboxCompanion: true}
	for _, typ := range types {
		if !want[typ] {
			t.Fatalf("C-03 push: unexpected KernelApply type %q in %v", typ, types)
		}
		delete(want, typ)
	}
	if len(want) != 0 {
		t.Fatalf("C-03 push: missing KernelApply types %v in %v", want, types)
	}
}

// E-03 supersedes the old pre-split dedupe: a push no longer leaves
// a pre-split companion on the main kernel — it materializes the
// split first (companion moves onto the companion kernel) and applies
// both kernels. The no-double-push property still holds: the two
// applies target distinct kernels (deduped via KernelID).
func TestC03PushMaterializesPreSplitCompanion(t *testing.T) {
	a := newNodeSwapFakeAgent(t)
	origin, _ := c03bSplitFixtureOnFakeAgent(t, a, false)

	if err := a.h.applyKernelsForInbounds([]int64{origin.ID}); err != nil {
		t.Fatalf("C-03 push: applyKernelsForInbounds must succeed, got %v", err)
	}
	after, err := a.h.repo.GetCompanionInbound(origin.ID)
	if err != nil || after == nil {
		t.Fatalf("C-03 push: companion must still exist: %v %v", after, err)
	}
	if after.KernelID == origin.KernelID {
		t.Fatalf("C-03 push (E-03): pre-split companion must move off the main kernel %d on push, still on %d", origin.KernelID, after.KernelID)
	}
	types := c03bKernelApplyTypes(t, a)
	if len(types) != 2 {
		t.Fatalf("C-03 push (E-03): pre-split push must apply both main and companion kernels, got %v", types)
	}
	want := map[string]bool{kernel.TypeSingbox: true, kernel.TypeSingboxCompanion: true}
	for _, typ := range types {
		if !want[typ] {
			t.Fatalf("C-03 push (E-03): unexpected KernelApply type %q in %v", typ, types)
		}
		delete(want, typ)
	}
	if len(want) != 0 {
		t.Fatalf("C-03 push (E-03): missing KernelApply types %v in %v", want, types)
	}
}

// End-to-end (live repro): disabling one of two share subscriptions must
// re-render the companion without the disabled user's credential, while
// the other subscription keeps demand alive so the companion survives.
func TestC03SyncDisableReappliesCompanion(t *testing.T) {
	a := newNodeSwapFakeAgent(t)
	origin, _ := c03bSplitFixtureOnFakeAgent(t, a, true)
	now := time.Now()
	nowMs := now.UnixMilli()
	subA := &model.Subscription{Name: "c03b-a", Kind: model.SubscriptionShare, Token: strings.Repeat("a", 32),
		UUID: "u-c03b-a", Password: "c03b-password-a", Enabled: 1, SourceAll: 1, AppliedActive: 1,
		CreatedTime: nowMs, UpdatedTime: nowMs}
	subB := &model.Subscription{Name: "c03b-b", Kind: model.SubscriptionShare, Token: strings.Repeat("b", 32),
		UUID: "u-c03b-b", Password: "c03b-password-b", Enabled: 1, SourceAll: 1, AppliedActive: 1,
		CreatedTime: nowMs, UpdatedTime: nowMs}
	for _, s := range []*model.Subscription{subA, subB} {
		if err := a.h.repo.SaveSubscription(s); err != nil {
			t.Fatal(err)
		}
	}

	// The metering reconcile must not cover the disable on its own: its
	// dirty-check compares stored companion fields, not the rendered
	// users, so a subscription disable leaves it clean.
	subA.Enabled = 0
	if err := a.h.repo.SaveSubscription(subA); err != nil {
		t.Fatal(err)
	}
	before := len(a.commandsOfType("KernelApply"))
	a.h.reconcileSSMetering()
	if got := len(a.commandsOfType("KernelApply")); got != before {
		t.Fatalf("C-03: metering reconcile must not push on subscription disable (got %d KernelApply, want %d) — the sync loop owns the re-render", got, before)
	}

	a.h.syncSubscriptions(now)
	types := c03bKernelApplyTypes(t, a)
	hasCompanion := false
	for _, typ := range types {
		if typ == kernel.TypeSingboxCompanion {
			hasCompanion = true
		}
	}
	if !hasCompanion {
		t.Fatalf("C-03: disabling a subscription must apply the companion kernel, got KernelApply types %v", types)
	}
	// Rendered SS2022 user passwords are derived uPSKs, not the literal
	// subscription passwords (SSUserPSK: HMAC over password+sub id+method).
	cfg := c03bKernelApplyConfig(t, a, kernel.TypeSingboxCompanion)
	pskA := kernel.SSUserPSK("c03b-password-a", subA.ID, meteringMethod)
	pskB := kernel.SSUserPSK("c03b-password-b", subB.ID, meteringMethod)
	if pskA == "" || pskB == "" {
		t.Fatalf("C-03: derived uPSKs must be non-empty, got %q / %q", pskA, pskB)
	}
	if strings.Contains(cfg, pskA) {
		t.Fatalf("C-03: companion config must drop the disabled subscription's credential (uPSK %s):\n%s", pskA, cfg)
	}
	if !strings.Contains(cfg, pskB) {
		t.Fatalf("C-03: companion config must keep the surviving subscription's credential (uPSK %s):\n%s", pskB, cfg)
	}
	after, err := a.h.repo.GetSubscription(subA.ID)
	if err != nil || after == nil {
		t.Fatalf("read back disabled subscription: %v", err)
	}
	if after.AppliedActive != 0 {
		t.Fatalf("C-03: disabled subscription AppliedActive = %d, want 0", after.AppliedActive)
	}
	_ = origin
}
