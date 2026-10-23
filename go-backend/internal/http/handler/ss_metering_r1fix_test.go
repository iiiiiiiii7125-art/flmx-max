package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/http/response"
	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// ---- SS metering round-1 audit fixes (F1–F8) ----
//
// Each test pins one adjudicated fix from audit-r1 (A/B/C reports in
// ~/workspace/ss-metering/audit-r1). They are written against the
// behaviour the audit demanded, not the behaviour the code had.

// meteringFormBody renders the full inbound-save form for a stored row
// (the shape the real frontend posts), then applies mut.
func meteringFormBody(t *testing.T, row *model.NodeInbound, mut func(*inboundRequest)) string {
	t.Helper()
	in, err := inboundFromModel(*row)
	if err != nil {
		t.Fatal(err)
	}
	req := inboundRequest{
		ID: row.ID, Name: row.Name, Protocol: in.Protocol, Listen: in.Listen, Port: in.Port,
		Settings: in.Settings, Transport: in.Transport, Security: in.Security,
		RelayOnly: row.RelayOnly == 1, AcceptProxyProtocol: row.AcceptProxyProtocol == 1,
		OutboundID: row.OutboundID,
	}
	if rules := rowSplitRules(*row); len(rules) > 0 {
		req.SplitRules = rules
	}
	if mut != nil {
		mut(&req)
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func callSplitSave(t *testing.T, h *Handler, body string) response.R {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/inbound/split-save", strings.NewReader(body))
	h.inboundSplitSave(rec, req)
	var out response.R
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode split save resp: %v (body=%s)", err, rec.Body.String())
	}
	return out
}

func boolPtr(b bool) *bool { return &b }

// F2 (B-02/B-04/B-06/C-02a + 普查新发现): every write surface that
// takes an inbound id must refuse companion rows with the family
// message; companions must also be unusable as chain/split targets
// and as subscription sources at save-validation time.
func TestSSMeteringCompanionWriteGuards(t *testing.T) {
	r, h, _, origin, _ := seedMeteringFixture(t)
	h.reconcileSSMetering()
	comp, err := r.GetCompanionInbound(origin.ID)
	if err != nil || comp == nil {
		t.Fatal("companion must exist")
	}
	targetNode := seedRelayTargetNode(t, r, "m-target-node", "203.0.113.30")
	targetKernel := seedChainKernel(t, r, targetNode, kernel.TypeXray)
	target := seedChainInbound(t, r, targetKernel, "m-target", kernel.Inbound{
		Protocol: kernel.ProtocolVLESS, Port: 443,
		Security: kernel.Security{Type: kernel.SecurityReality},
	}, 0)

	// (B-02) split-save on the companion itself.
	out := callSplitSave(t, h, `{"id":`+itoa(comp.ID)+`,"outboundId":`+itoa(target.ID)+`}`)
	if out.Code == 0 || !strings.Contains(out.Msg, "计量伴生入站") {
		t.Fatalf("split-save on a companion must be refused, got %+v", out)
	}
	if got, _ := r.GetNodeInbound(comp.ID); got.OutboundID != 0 {
		t.Fatal("refused split-save must not persist an exit on the companion")
	}

	// (chain target) a companion id as another inbound's chained exit
	// or split target must fail save-time chain validation.
	viaRow := *origin
	viaRow.OutboundID = comp.ID
	if err := h.validateChain(viaRow, kernel.TypeXray); err == nil || !strings.Contains(err.Error(), "计量伴生入站") {
		t.Fatalf("companion as chained exit must be rejected, got %v", err)
	}
	splitRow := *origin
	splitRow.SplitRules = `[{"domains":["example.com"],"action":"proxy","outboundId":` + itoa(comp.ID) + `}]`
	if err := h.validateChain(splitRow, kernel.TypeXray); err == nil || !strings.Contains(err.Error(), "计量伴生入站") {
		t.Fatalf("companion as split target must be rejected, got %v", err)
	}

	// (C-02a) relay create on the companion.
	frontID := seedRelayTargetNode(t, r, "m-front", "198.51.100.1")
	tunnelID := seedRelayTargetTunnel(t, r, "m-tun", frontID, 30000, origin.NodeID)
	rec := httptest.NewRecorder()
	h.inboundRelayCreate(rec, claimsCtxReq(http.MethodPost, "/api/v1/inbound/relay/create", "",
		`{"inboundId":`+itoa(comp.ID)+`,"tunnelId":`+itoa(tunnelID)+`,"inPort":30011,"name":"bad-relay"}`, 1, 0))
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Code == 0 || !strings.Contains(env.Msg, "计量伴生入站") {
		t.Fatalf("relay create on a companion must be refused: code=%d msg=%q body=%s", env.Code, env.Msg, rec.Body.String())
	}
	if relays, err := r.ListNodeInboundRelays(); err != nil || len(relays) != 0 {
		t.Fatalf("no relay may be created on a companion, got %v (%v)", relays, err)
	}

	// (B-04) traffic reset on the companion; the origin stays resettable.
	if err := r.DB().Exec(`UPDATE node_inbound SET upload_bytes = 12345 WHERE id = ?`, comp.ID).Error; err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	h.inboundResetTraffic(rec, claimsCtxReq(http.MethodPost, "/api/v1/inbound/reset-traffic", "",
		`{"id":`+itoa(comp.ID)+`}`, 1, 0))
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Code == 0 || !strings.Contains(env.Msg, "计量伴生入站") {
		t.Fatalf("traffic reset on a companion must be refused: code=%d msg=%q", env.Code, env.Msg)
	}
	if got, _ := r.GetNodeInbound(comp.ID); got.UploadBytes != 12345 {
		t.Fatal("refused reset must not clear the companion's counters")
	}

	// (B-06) subscription save validation must not accept a companion
	// as a source (it would be stored, then silently dropped by every
	// reader — the assignment surface already refuses companions).
	rows, err := r.ListNodeInbounds()
	if err != nil {
		t.Fatal(err)
	}
	req := subscriptionRequest{Name: "v", InboundIDs: []int64{origin.ID, comp.ID}}
	ids, err := req.validate(rows)
	if err != nil {
		t.Fatalf("validate must not fail on a mixed list: %v", err)
	}
	if len(ids) != 1 || ids[0] != origin.ID {
		t.Fatalf("companion id must be dropped at validation, got ids=%v", ids)
	}
	req = subscriptionRequest{Name: "v", InboundIDs: []int64{comp.ID}}
	if _, err := req.validate(rows); err == nil {
		t.Fatal("a companion-only source list must fail validation like an empty one")
	}
}

// F3 (C-02b): when reconcile recycles a companion, it must tear down
// the companion's relays (including their materialized forwards)
// exactly like the origin-delete path does — repo row deletion alone
// leaves the forward behind.
func TestSSMeteringRecycleTearsDownCompanionRelays(t *testing.T) {
	r, h, _, origin, sub := seedMeteringFixture(t)
	h.reconcileSSMetering()
	comp, err := r.GetCompanionInbound(origin.ID)
	if err != nil || comp == nil {
		t.Fatal("companion must exist")
	}
	// Attach a managed relay directly at the repo level (the state a
	// relay created before the create-side guard, or via any future
	// leak, would leave behind).
	now := time.Now().UnixMilli()
	frontID := seedRelayTargetNode(t, r, "m-front", "198.51.100.1")
	tunnelID := seedRelayTargetTunnel(t, r, "m-tun", frontID, 30000, origin.NodeID)
	forwardID, err := r.CreateForwardTx(1, "admin", "comp-relay-fwd", tunnelID, "203.0.113.20:8388", "fifo", now, 1,
		[]int64{frontID}, 30005, "", nil, 0, 0, nil, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.CreateInboundRelay(&model.NodeInboundRelay{InboundID: comp.ID, ForwardID: forwardID, Name: "comp-relay", Managed: 1, CreatedTime: now}); err != nil {
		t.Fatal(err)
	}

	// Demand gone -> reconcile recycles the companion.
	sub.Enabled = 0
	sub.UpdatedTime = time.Now().UnixMilli()
	if err := r.SaveSubscription(sub); err != nil {
		t.Fatal(err)
	}
	h.reconcileSSMetering()

	if comp, _ := r.GetCompanionInbound(origin.ID); comp != nil {
		t.Fatal("companion must be recycled")
	}
	if rels, err := r.ListInboundRelays(comp.ID); err != nil || len(rels) != 0 {
		t.Fatalf("companion relays must be gone, got %v (%v)", rels, err)
	}
	if fwd, err := h.getForwardRecord(forwardID); err == nil && fwd != nil {
		t.Fatal("the recycled companion's materialized forward must be deleted, not left dangling")
	}
}

// markKernelApplied simulates the node agent acknowledging the
// kernel's current rendered config: what a successful KernelApply
// plus status poll persist as config_hash/applied_hash.
func markKernelApplied(t *testing.T, r *repo.Repository, h *Handler, k *model.NodeKernel) {
	t.Helper()
	_, hash, err := h.buildKernelApplyPayload(k)
	if err != nil {
		t.Fatal(err)
	}
	if hash == "" {
		t.Fatal("payload hash must not be empty")
	}
	if err := r.UpdateNodeKernelFields(k.ID, map[string]interface{}{"config_hash": hash, "applied_hash": hash}); err != nil {
		t.Fatal(err)
	}
}

func meteringActiveOf(t *testing.T, h *Handler, originID int64) bool {
	t.Helper()
	rec := httptest.NewRecorder()
	h.inboundList(rec, httptest.NewRequest(http.MethodPost, "/api/v1/inbound/list", strings.NewReader("{}")))
	var env struct {
		Code int `json:"code"`
		Data []struct {
			ID             int64 `json:"id"`
			MeteringActive bool  `json:"meteringActive"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Code != 0 {
		t.Fatalf("inbound list: code=%d err=%v body=%s", env.Code, err, rec.Body.String())
	}
	for _, v := range env.Data {
		if v.ID == originID {
			return v.MeteringActive
		}
	}
	t.Fatal("origin missing from inbound list")
	return false
}

// F4 (B-03): retargeting (and the「计量中」badge) requires the
// companion's config to be acknowledged by the node agent. A
// companion that merely exists in the DB — node offline when it was
// materialized, apply failed — must not attract pulls: its port is
// not listening anywhere.
func TestSSMeteringRetargetRequiresAppliedConfig(t *testing.T) {
	r, h, k, origin, sub := seedMeteringFixture(t)
	h.reconcileSSMetering()
	comp, err := r.GetCompanionInbound(origin.ID)
	if err != nil || comp == nil {
		t.Fatal("companion must be materialized")
	}
	rows, err := r.ListNodeInbounds()
	if err != nil {
		t.Fatal(err)
	}

	// No agent in the harness: never applied -> pulls keep serving
	// the origin endpoint with the shared password.
	eps := h.subEndpoints(sub, rows)
	if len(eps) != 1 {
		t.Fatalf("exactly one endpoint expected, got %d", len(eps))
	}
	ep := eps[0]
	if ep.Inbound.ID != origin.ID || ep.CompanionOf != 0 || ep.Port != origin.Port {
		t.Fatalf("unapplied companion must not be retargeted: id=%d port=%d companionOf=%d",
			ep.Inbound.ID, ep.Port, ep.CompanionOf)
	}
	if ep.Inbound.Settings.Password != meteringSharedPW {
		t.Fatal("fallback endpoint must carry the origin's shared password")
	}
	if meteringActiveOf(t, h, origin.ID) {
		t.Fatal("badge must not claim metering before the companion config is applied")
	}

	// The agent acknowledges exactly the current config -> the gate
	// opens for both pulls and the badge.
	markKernelApplied(t, r, h, k)
	eps = h.subEndpoints(sub, rows)
	if len(eps) != 1 {
		t.Fatalf("exactly one endpoint expected, got %d", len(eps))
	}
	ep = eps[0]
	if ep.Inbound.ID != comp.ID || ep.CompanionOf != origin.ID || ep.Port != comp.Port {
		t.Fatalf("applied companion must be retargeted: id=%d port=%d companionOf=%d",
			ep.Inbound.ID, ep.Port, ep.CompanionOf)
	}
	if !meteringActiveOf(t, h, origin.ID) {
		t.Fatal("badge must report metering once the companion config is applied")
	}
}

// F5 (C-03): when the per-subscription credential cannot be derived
// for a retargeted companion endpoint (here: the subscription has no
// password), the endpoint must be dropped — never served with the
// companion's bare identity PSK, which is a working single-user
// credential.
func TestSSMeteringCompanionEndpointFailClosed(t *testing.T) {
	r, h, k, origin, sub := seedMeteringFixture(t)
	h.reconcileSSMetering()
	if comp, _ := r.GetCompanionInbound(origin.ID); comp == nil {
		t.Fatal("companion must exist")
	}
	markKernelApplied(t, r, h, k)
	rows, err := r.ListNodeInbounds()
	if err != nil {
		t.Fatal(err)
	}

	// Control: with the subscription password present, the companion
	// endpoint is served with the combined credential.
	eps := h.subEndpoints(sub, rows)
	if len(eps) != 1 || eps[0].CompanionOf != origin.ID ||
		!strings.Contains(eps[0].Inbound.Settings.Password, ":") {
		t.Fatalf("control pull must serve the combined companion credential, got %+v", eps)
	}

	// Password-less share subscription: derivation fails, so the
	// companion endpoint disappears instead of leaking the bare iPSK.
	bare := *sub
	bare.Password = ""
	eps = h.subEndpoints(&bare, rows)
	for _, e := range eps {
		if e.CompanionOf > 0 {
			t.Fatalf("companion endpoint must be dropped when derivation fails, got %+v", e)
		}
		if e.Inbound.Settings.Password != "" && !strings.Contains(e.Inbound.Settings.Password, ":") &&
			e.Inbound.Protocol == kernel.ProtocolShadowsocks {
			// Any surviving SS endpoint must be the origin (shared
			// password), never a bare companion row.
			comp2, _ := r.GetCompanionInbound(origin.ID)
			if comp2 != nil && e.Inbound.ID == comp2.ID {
				t.Fatal("bare companion inbound leaked as an endpoint")
			}
		}
	}
	if len(eps) != 0 {
		t.Fatalf("the only source is the metered origin: no endpoint may survive, got %d", len(eps))
	}
}

// F8 (+ B-02 self-healing): syncCompanion must mirror the origin's
// share/relay presentation fields onto the companion, and must clear
// any routing state a companion row picked up (its expected state is
// always direct: no chained exit, no split rules). A second
// reconcile over the converged state must be a no-op.
func TestSSMeteringSyncMirrorsShareFieldsAndClearsRouting(t *testing.T) {
	r, h, _, origin, _ := seedMeteringFixture(t)
	h.reconcileSSMetering()
	comp, err := r.GetCompanionInbound(origin.ID)
	if err != nil || comp == nil {
		t.Fatal("companion must exist")
	}

	// Drift the origin's mirrored fields and poison the companion
	// with routing state (the residue the B-02 write could leave).
	origin.ShareHost = "203.0.113.99"
	origin.ShareAddressMode = "custom"
	origin.Inx = 7
	origin.RelayWhitelist = 1
	origin.WhitelistExtra = `["10.0.0.0/8"]`
	if err := r.SaveNodeInbound(origin); err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Exec(`UPDATE node_inbound SET inx = 7 WHERE id = ?`, origin.ID).Error; err != nil {
		t.Fatal(err)
	}
	comp.OutboundID = 999
	comp.SplitRules = `[{"domains":["example.com"],"action":"proxy","outboundId":999}]`
	if err := r.SaveNodeInbound(comp); err != nil {
		t.Fatal(err)
	}

	h.reconcileSSMetering()
	got, err := r.GetCompanionInbound(origin.ID)
	if err != nil || got == nil {
		t.Fatal("companion must still exist")
	}
	if got.ShareHost != "203.0.113.99" || got.ShareAddressMode != "custom" || got.Inx != 7 ||
		got.RelayWhitelist != 1 || got.WhitelistExtra != `["10.0.0.0/8"]` {
		t.Fatalf("companion must mirror the origin's share/relay fields: %+v", got)
	}
	if got.OutboundID != 0 || got.SplitRules != "" {
		t.Fatalf("companion routing state must be cleared, got outbound=%d split=%q", got.OutboundID, got.SplitRules)
	}

	// Converged state: a further reconcile changes nothing.
	if err := r.DB().Exec(`UPDATE node_inbound SET updated_time = 111 WHERE id = ?`, got.ID).Error; err != nil {
		t.Fatal(err)
	}
	h.reconcileSSMetering()
	again, _ := r.GetCompanionInbound(origin.ID)
	if again == nil || again.UpdatedTime != 111 {
		t.Fatalf("converged companion must not be re-saved, updatedTime=%v", again)
	}
}

// F1 (A-01+B-01+C-01): enabling metering on a statically excluded
// origin (chained exit / split rules) must be rejected with a reason,
// and an already-metered origin must not be saved into an excluded
// shape without turning metering off first.
func TestSSMeteringSaveMeteringEligibilityGate(t *testing.T) {
	r, h, k, origin, _ := seedMeteringFixture(t)
	// A chainable target on a second node/kernel.
	targetNode := seedRelayTargetNode(t, r, "m-target-node", "203.0.113.30")
	targetKernel := seedChainKernel(t, r, targetNode, kernel.TypeXray)
	target := seedChainInbound(t, r, targetKernel, "m-target", kernel.Inbound{
		Protocol: kernel.ProtocolVLESS, Port: 443,
		Security: kernel.Security{Type: kernel.SecurityReality},
	}, 0)
	_ = k

	// Start from metering OFF so "explicitly enable" is observable.
	origin.MeteringEnabled = 0
	if err := r.SaveNodeInbound(origin); err != nil {
		t.Fatal(err)
	}

	// (a) Explicit enable + chained exit -> rejected, nothing stored.
	body := meteringFormBody(t, origin, func(req *inboundRequest) {
		req.MeteringEnabled = boolPtr(true)
		req.OutboundID = target.ID
	})
	if out := callInboundSave(t, h, true, body); out.Code == 0 {
		t.Fatalf("enable metering on a chained-exit origin must be rejected, got %+v", out)
	} else if !strings.Contains(out.Msg, "订阅计量") {
		t.Fatalf("rejection must name metering, got %q", out.Msg)
	}
	if got, _ := r.GetNodeInbound(origin.ID); got.MeteringEnabled != 0 || got.OutboundID != 0 {
		t.Fatalf("rejected save must not persist: metering=%d outbound=%d", got.MeteringEnabled, got.OutboundID)
	}

	// (b) Explicit enable + split rules -> rejected.
	body = meteringFormBody(t, origin, func(req *inboundRequest) {
		req.MeteringEnabled = boolPtr(true)
		req.SplitRules = []kernel.SplitRule{{Domains: []string{"example.com"}, Action: kernel.SplitDirect}}
	})
	if out := callInboundSave(t, h, true, body); out.Code == 0 {
		t.Fatalf("enable metering on a split origin must be rejected, got %+v", out)
	}
	if got, _ := r.GetNodeInbound(origin.ID); got.MeteringEnabled != 0 || strings.TrimSpace(got.SplitRules) != "" {
		t.Fatalf("rejected save must not persist: metering=%d split=%q", got.MeteringEnabled, got.SplitRules)
	}

	// (c) Plain explicit enable (no exclusion) -> accepted.
	body = meteringFormBody(t, origin, func(req *inboundRequest) {
		req.MeteringEnabled = boolPtr(true)
	})
	if out := callInboundSave(t, h, true, body); out.Code != 0 {
		t.Fatalf("plain enable must succeed: %+v", out)
	}
	if got, _ := r.GetNodeInbound(origin.ID); got.MeteringEnabled != 1 {
		t.Fatal("metering must be stored on")
	}

	// (d) Metering on + save introduces a chained exit (metering field
	// omitted, i.e. stays on) -> rejected with the turn-it-off hint.
	stored, _ := r.GetNodeInbound(origin.ID)
	body = meteringFormBody(t, stored, func(req *inboundRequest) {
		req.OutboundID = target.ID
	})
	out := callInboundSave(t, h, true, body)
	if out.Code == 0 {
		t.Fatalf("metered origin must not silently gain a chained exit, got %+v", out)
	}
	if !strings.Contains(out.Msg, "请先关闭订阅计量") {
		t.Fatalf("rejection must hint to turn metering off first, got %q", out.Msg)
	}
	if got, _ := r.GetNodeInbound(origin.ID); got.OutboundID != 0 || got.MeteringEnabled != 1 {
		t.Fatalf("rejected save must not persist: metering=%d outbound=%d", got.MeteringEnabled, got.OutboundID)
	}

	// (e) Same save but turning metering off in the same request ->
	// accepted (the admin resolves the conflict atomically).
	body = meteringFormBody(t, stored, func(req *inboundRequest) {
		req.MeteringEnabled = boolPtr(false)
		req.OutboundID = target.ID
	})
	if out := callInboundSave(t, h, true, body); out.Code != 0 {
		t.Fatalf("turning metering off while adding an exit must succeed: %+v", out)
	}
	if got, _ := r.GetNodeInbound(origin.ID); got.MeteringEnabled != 0 || got.OutboundID != target.ID {
		t.Fatalf("save must persist: metering=%d outbound=%d", got.MeteringEnabled, got.OutboundID)
	}
}

// F1 (B-08 + ③ on the split-save path): split-save on a metered origin
// must not introduce an exit/split, and a split-save that clears the
// exclusion must reconcile immediately (companion materializes in the
// same request, not one sync period later).
func TestSSMeteringSplitSaveGateAndReconcile(t *testing.T) {
	r, h, _, origin, _ := seedMeteringFixture(t)
	targetNode := seedRelayTargetNode(t, r, "m-target-node", "203.0.113.30")
	targetKernel := seedChainKernel(t, r, targetNode, kernel.TypeXray)
	target := seedChainInbound(t, r, targetKernel, "m-target", kernel.Inbound{
		Protocol: kernel.ProtocolVLESS, Port: 443,
		Security: kernel.Security{Type: kernel.SecurityReality},
	}, 0)

	// Metering is ON in the fixture.
	out := callSplitSave(t, h, `{"id":`+itoa(origin.ID)+`,"outboundId":`+itoa(target.ID)+`}`)
	if out.Code == 0 {
		t.Fatalf("split-save must not give a metered origin a chained exit, got %+v", out)
	}
	if !strings.Contains(out.Msg, "请先关闭订阅计量") {
		t.Fatalf("rejection must hint to turn metering off first, got %q", out.Msg)
	}
	if got, _ := r.GetNodeInbound(origin.ID); got.OutboundID != 0 {
		t.Fatal("rejected split-save must not persist the exit")
	}

	// Legacy state: switch on but a chained exit set behind the save
	// gate's back (direct row write) -> no companion can materialize.
	legacy, _ := r.GetNodeInbound(origin.ID)
	legacy.OutboundID = target.ID
	legacy.UpdatedTime = time.Now().UnixMilli()
	if err := r.SaveNodeInbound(legacy); err != nil {
		t.Fatal(err)
	}
	h.reconcileSSMetering()
	if comp, _ := r.GetCompanionInbound(origin.ID); comp != nil {
		t.Fatal("excluded origin must not have a companion")
	}
	// Clearing the exit through split-save must reconcile in-request.
	out = callSplitSave(t, h, `{"id":`+itoa(origin.ID)+`,"outboundId":0}`)
	if out.Code != 0 {
		t.Fatalf("clearing the exit must succeed: %+v", out)
	}
	if comp, _ := r.GetCompanionInbound(origin.ID); comp == nil {
		t.Fatal("split-save must trigger reconcile immediately: companion must exist right after the save")
	}
}
