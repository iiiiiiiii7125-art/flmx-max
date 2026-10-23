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
)

const (
	meteringMethod   = "2022-blake3-aes-128-gcm"
	meteringSharedPW = "AAAAAAAAAAAAAAAAAAAAAA==" // synthetic shared SS password fixture
	meteringSubPW    = "sub-password-a"
)

// seedMeteringFixture builds one node + xray kernel + an SS2022 origin
// inbound with metering switched on, plus one active share
// subscription sourcing everything (the metering demand).
func seedMeteringFixture(t *testing.T) (*repo.Repository, *Handler, *model.NodeKernel, *model.NodeInbound, *model.Subscription) {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/ssm.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	now := time.Now().UnixMilli()
	if _, err := r.CreateUser("admin", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now); err != nil {
		t.Fatal(err)
	}
	nodeID := seedRelayTargetNode(t, r, "m-node", "203.0.113.20")
	k := seedChainKernel(t, r, nodeID, kernel.TypeXray)
	origin := seedChainInbound(t, r, k, "m-ss", kernel.Inbound{Protocol: kernel.ProtocolShadowsocks, Port: 8388,
		Settings: kernel.Settings{Method: meteringMethod, Password: meteringSharedPW}}, 0)
	origin.MeteringEnabled = 1
	origin.UpdatedTime = now
	if err := r.SaveNodeInbound(origin); err != nil {
		t.Fatal(err)
	}
	sub := &model.Subscription{Name: "m-sub", Kind: model.SubscriptionShare, Token: strings.Repeat("d", 32),
		UUID: "u-m", Password: meteringSubPW, Enabled: 1, SourceAll: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.SaveSubscription(sub); err != nil {
		t.Fatal(err)
	}
	// The panel seeds a default share subscription of its own; disable
	// every other subscription so demand in these tests comes from the
	// fixture subscription alone.
	if subs, err := r.ListSubscriptions(); err == nil {
		for i := range subs {
			if subs[i].ID != sub.ID && subs[i].Enabled != 0 {
				subs[i].Enabled = 0
				_ = r.SaveSubscription(&subs[i])
			}
		}
	}
	return r, h, k, origin, sub
}

func TestSSMeteringReconcileLifecycle(t *testing.T) {
	r, h, _, origin, sub := seedMeteringFixture(t)

	h.reconcileSSMetering()
	comp, err := r.GetCompanionInbound(origin.ID)
	if err != nil || comp == nil {
		t.Fatalf("companion must be materialized: %v %v", comp, err)
	}
	if comp.Port == origin.Port || comp.Port <= 0 {
		t.Fatalf("companion needs its own port, got %d (origin %d)", comp.Port, origin.Port)
	}
	if comp.KernelID != origin.KernelID || comp.NodeID != origin.NodeID {
		t.Fatal("companion must live on the origin's kernel and node")
	}
	compIn, err := inboundFromModel(*comp)
	if err != nil {
		t.Fatal(err)
	}
	originIn, err := inboundFromModel(*origin)
	if err != nil {
		t.Fatal(err)
	}
	if compIn.Settings.Password == "" || compIn.Settings.Password == originIn.Settings.Password {
		t.Fatal("companion must carry a fresh identity PSK, never the shared password")
	}
	if compIn.Settings.Method != originIn.Settings.Method {
		t.Fatal("companion method must mirror the origin")
	}
	if !strings.HasSuffix(comp.Name, "（计量）") {
		t.Fatalf("companion name must be marked, got %q", comp.Name)
	}
	firstID, firstPort, firstPSK := comp.ID, comp.Port, compIn.Settings.Password

	// Idempotent: a second reconcile changes nothing.
	h.reconcileSSMetering()
	again, err := r.GetCompanionInbound(origin.ID)
	if err != nil || again == nil {
		t.Fatal("companion must survive a second reconcile")
	}
	againIn, _ := inboundFromModel(*again)
	if again.ID != firstID || again.Port != firstPort || againIn.Settings.Password != firstPSK {
		t.Fatal("reconcile must be idempotent (same row, port and identity PSK)")
	}
	if all, _ := r.ListCompanionInbounds(); len(all) != 1 {
		t.Fatalf("exactly one companion expected, got %d", len(all))
	}

	// Demand loss recycles the companion.
	sub.Enabled = 0
	sub.UpdatedTime = time.Now().UnixMilli()
	if err := r.SaveSubscription(sub); err != nil {
		t.Fatal(err)
	}
	h.reconcileSSMetering()
	if comp, _ := r.GetCompanionInbound(origin.ID); comp != nil {
		t.Fatal("companion must be recycled when no active share subscription demands it")
	}

	// Demand back + switch off: still no companion.
	sub.Enabled = 1
	if err := r.SaveSubscription(sub); err != nil {
		t.Fatal(err)
	}
	origin.MeteringEnabled = 0
	if err := r.SaveNodeInbound(origin); err != nil {
		t.Fatal(err)
	}
	h.reconcileSSMetering()
	if comp, _ := r.GetCompanionInbound(origin.ID); comp != nil {
		t.Fatal("companion must not exist while the switch is off")
	}
}

func TestSSMeteringUsersRemapAndEndpointRetarget(t *testing.T) {
	r, h, k, origin, sub := seedMeteringFixture(t)
	h.reconcileSSMetering()
	comp, err := r.GetCompanionInbound(origin.ID)
	if err != nil || comp == nil {
		t.Fatal("companion must exist")
	}
	// R1-F4 (B-03): pull-side retargeting now requires the companion's
	// config to be acknowledged by the agent; simulate the ack. The
	// user-mount remap below is config-space and unaffected.
	markKernelApplied(t, r, h, k)
	compIn, _ := inboundFromModel(*comp)

	// The kernel user mount moves from the origin to the companion.
	users := h.activeUsersByInbound(time.Now())
	if len(users[origin.ID]) != 0 {
		t.Fatalf("origin must not carry subscription users, got %v", users[origin.ID])
	}
	if len(users[comp.ID]) != 1 || users[comp.ID][0].ID != sub.ID {
		t.Fatalf("companion must carry the share subscription, got %v", users)
	}

	rows, err := r.ListNodeInbounds()
	if err != nil {
		t.Fatal(err)
	}
	eps := h.subEndpoints(sub, rows)
	if len(eps) != 1 {
		t.Fatalf("exactly one endpoint expected, got %d", len(eps))
	}
	ep := eps[0]
	if ep.Inbound.ID != comp.ID || ep.Port != comp.Port || ep.CompanionOf != origin.ID {
		t.Fatalf("share endpoint must be retargeted to the companion: id=%d port=%d companionOf=%d",
			ep.Inbound.ID, ep.Port, ep.CompanionOf)
	}
	wantPW := compIn.Settings.Password + ":" + kernel.SSUserPSK(meteringSubPW, sub.ID, meteringMethod)
	if ep.Inbound.Settings.Password != wantPW {
		t.Fatal("share endpoint must carry the combined iPSK:uPSK password")
	}
	if !ep.Direct || ep.Proxy != nil {
		t.Fatal("the retargeted endpoint must stay a direct endpoint")
	}

	// The assignment filter still answers to the origin's id.
	filtered := filterEndpointsByUserAssignments(eps, []repo.UserInboundAssignment{{InboundID: origin.ID}})
	if len(filtered) != 1 {
		t.Fatal("assignment on the origin must keep the retargeted endpoint")
	}
	if got := filterEndpointsByUserAssignments(eps, nil); len(got) != 0 {
		t.Fatal("no assignments must filter the endpoint out")
	}

	// A subscription naming the companion itself as a source gets
	// nothing: companions are never sources.
	crafted := &model.Subscription{Name: "crafted", Kind: model.SubscriptionShare, Token: strings.Repeat("e", 32),
		UUID: "u-c", Password: "x", Enabled: 1, InboundIDs: strconv.FormatInt(comp.ID, 10),
		CreatedTime: time.Now().UnixMilli(), UpdatedTime: time.Now().UnixMilli()}
	if eps := h.subEndpoints(crafted, rows); len(eps) != 0 {
		t.Fatalf("companion must never be a subscription source, got %d endpoints", len(eps))
	}
}

// Relay endpoints are never retargeted: relay traffic lands on the
// origin's port with the shared password, so the relay endpoint keeps
// pointing at the origin while the direct one moves to the companion.
func TestSSMeteringRelayEndpointNotRetargeted(t *testing.T) {
	r, h, k, origin, sub := seedMeteringFixture(t)
	h.reconcileSSMetering()
	comp, _ := r.GetCompanionInbound(origin.ID)
	if comp == nil {
		t.Fatal("companion must exist")
	}
	// R1-F4 (B-03): same applied-ack precondition as the retarget test
	// above; this test's subject is the relay endpoint staying put.
	markKernelApplied(t, r, h, k)
	now := time.Now().UnixMilli()
	frontID := seedRelayTargetNode(t, r, "front", "198.51.100.1")
	if err := r.DB().Exec(`UPDATE node SET status = 1`).Error; err != nil {
		t.Fatal(err)
	}
	tunnelID := seedRelayTargetTunnel(t, r, "tun", frontID, 30000, origin.NodeID)
	if err := r.DB().Exec(`UPDATE tunnel SET status = 1`).Error; err != nil {
		t.Fatal(err)
	}
	forwardID, err := r.CreateForwardTx(1, "admin", "relay-fwd", tunnelID, "203.0.113.20:8388", "fifo", now, 1,
		[]int64{frontID}, 30005, "", nil, 0, 0, nil, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.CreateInboundRelay(&model.NodeInboundRelay{InboundID: origin.ID, ForwardID: forwardID, Name: "relay", Managed: 1, CreatedTime: now}); err != nil {
		t.Fatal(err)
	}
	rows, err := r.ListNodeInbounds()
	if err != nil {
		t.Fatal(err)
	}
	eps := h.subEndpoints(sub, rows)
	var direct, relay *kernel.Endpoint
	for i := range eps {
		if eps[i].RelayID > 0 {
			relay = &eps[i]
		} else {
			direct = &eps[i]
		}
	}
	if direct == nil || relay == nil {
		t.Fatalf("want one direct and one relay endpoint, got %+v", eps)
	}
	if direct.Inbound.ID != comp.ID || direct.CompanionOf != origin.ID {
		t.Fatal("direct endpoint must be retargeted to the companion")
	}
	if relay.Inbound.ID != origin.ID || relay.CompanionOf != 0 {
		t.Fatalf("relay endpoint must stay on the origin: id=%d companionOf=%d", relay.Inbound.ID, relay.CompanionOf)
	}
	if relay.Inbound.Settings.Password != meteringSharedPW {
		t.Fatal("relay endpoint must keep the shared password")
	}
}

func TestSSMeteringWantedMatrix(t *testing.T) {
	_, h, _, origin, sub := seedMeteringFixture(t)
	demand := map[int64][]kernel.User{origin.ID: {{ID: sub.ID, Password: meteringSubPW}}}
	rowFor := func(mut func(*model.NodeInbound, *kernel.Inbound)) *model.NodeInbound {
		row := *origin
		in, err := inboundFromModel(row)
		if err != nil {
			t.Fatal(err)
		}
		if mut != nil {
			mut(&row, &in)
			if err := applyInboundToModel(in, &row); err != nil {
				t.Fatal(err)
			}
		}
		return &row
	}
	cases := []struct {
		name string
		row  *model.NodeInbound
		dem  map[int64][]kernel.User
		want bool
	}{
		{"baseline", rowFor(nil), demand, true},
		{"switch off", rowFor(func(row *model.NodeInbound, in *kernel.Inbound) { row.MeteringEnabled = 0 }), demand, false},
		{"no demand", rowFor(nil), nil, false},
		{"legacy method", rowFor(func(row *model.NodeInbound, in *kernel.Inbound) {
			in.Settings.Method = "aes-256-gcm"
			in.Settings.Password = "legacy-shared"
		}), demand, false},
		{"shadowtls front", rowFor(func(row *model.NodeInbound, in *kernel.Inbound) {
			in.Security = kernel.Security{Type: kernel.SecurityShadowTLS, SNI: "www.microsoft.com", ShadowTLSPassword: "stls-pw"}
		}), demand, false},
		{"relay only", rowFor(func(row *model.NodeInbound, in *kernel.Inbound) { row.RelayOnly = 1 }), demand, false},
		{"proxy protocol", rowFor(func(row *model.NodeInbound, in *kernel.Inbound) { row.AcceptProxyProtocol = 1 }), demand, false},
		{"chained exit", rowFor(func(row *model.NodeInbound, in *kernel.Inbound) { row.OutboundID = 42 }), demand, false},
		{"split rules", rowFor(func(row *model.NodeInbound, in *kernel.Inbound) {
			row.SplitRules = `[{"domains":["example.com"],"action":"direct"}]`
		}), demand, false},
	}
	for _, c := range cases {
		if got := h.ssMeteringWanted(c.row, c.dem); got != c.want {
			t.Fatalf("%s: ssMeteringWanted = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSSMeteringOriginDeleteCascades(t *testing.T) {
	r, h, _, origin, _ := seedMeteringFixture(t)
	h.reconcileSSMetering()
	if comp, _ := r.GetCompanionInbound(origin.ID); comp == nil {
		t.Fatal("companion must exist before the delete")
	}
	if _, err := r.CreateUser("admin2", "hash", 0, time.Now().UnixMilli()+86400000, 1, 1, 100, 1, 0, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	adminID := int64(1)
	w := httptest.NewRecorder()
	h.inboundDelete(w, claimsCtxReq(http.MethodPost, "/api/v1/inbound/delete", "",
		`{"id":`+strconv.FormatInt(origin.ID, 10)+`}`, adminID, 0))
	var env struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil || env.Code != 0 {
		t.Fatalf("delete must succeed: code=%d body=%s", env.Code, w.Body.String())
	}
	if row, _ := r.GetNodeInbound(origin.ID); row != nil {
		t.Fatal("origin must be deleted")
	}
	if comp, _ := r.GetCompanionInbound(origin.ID); comp != nil {
		t.Fatal("deleting the origin must delete its companion")
	}
}

func TestSSMeteringPayloadGate(t *testing.T) {
	r, h, k, origin, sub := seedMeteringFixture(t)
	h.reconcileSSMetering()
	comp, _ := r.GetCompanionInbound(origin.ID)
	if comp == nil {
		t.Fatal("companion must exist")
	}
	payload, _, err := h.buildKernelApplyPayload(k)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(payload)
	upsk := kernel.SSUserPSK(meteringSubPW, sub.ID, meteringMethod)
	if !strings.Contains(string(raw), upsk) {
		t.Fatal("kernel payload must carry the companion's derived uPSK client")
	}
	if !strings.Contains(string(raw), "u"+strconv.FormatInt(sub.ID, 10)+"@flmx") {
		t.Fatal("kernel payload must carry the companion's user identity")
	}

	// Disabling the origin at the row level must silence the companion
	// in the very next payload (render-time gate), even before any
	// reconcile mirrors the flag.
	origin.Enabled = 0
	if err := r.SaveNodeInbound(origin); err != nil {
		t.Fatal(err)
	}
	payload, _, err = h.buildKernelApplyPayload(k)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(payload)
	if strings.Contains(string(raw), upsk) {
		t.Fatal("a disabled origin's companion must not render")
	}
}
