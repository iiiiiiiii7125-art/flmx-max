package handler

import (
	"encoding/json"
	"testing"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// S05 (AUDIT-HIST): a create that omits meteringEnabled must default the
// SS subscription-metering switch ON when the inbound can carry metering
// (SS2022, no ShadowTLS front, direct-serving shape). The old default-off
// (frontend emptyForm false + backend zero value) made every default-path
// SS2022 inbound silently unmetered — subscription traffic stuck at 0/0
// (Issue B) until the user found the switch.
func TestS05CreateDefaultsMeteringOnForEligibleSS2022(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/metering-default.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	nodeID := seedRelayTargetNode(t, r, "meter-node", "198.51.100.23")
	box := seedChainKernel(t, r, nodeID, kernel.TypeSingbox)
	seed := seedChainInbound(t, r, box, "meter-seed", kernel.Inbound{
		Protocol: kernel.ProtocolShadowsocks, Port: 25401,
		Settings: kernel.Settings{Method: "2022-blake3-aes-128-gcm", Password: "AAECAwQFBgcICQoLDA0ODw=="},
		Security: kernel.Security{Type: kernel.SecurityNone},
	}, 0)
	old, err := inboundFromModel(*seed)
	if err != nil {
		t.Fatal(err)
	}

	create := func(name string, port int, body func(*inboundRequest)) *model.NodeInbound {
		reqBody := inboundRequest{
			NodeID: nodeID, KernelType: kernel.TypeSingbox, Name: name, Port: port,
			Protocol: kernel.ProtocolShadowsocks, Listen: "::",
			Settings: old.Settings, Transport: old.Transport, Security: old.Security,
		}
		if body != nil {
			body(&reqBody)
		}
		raw, err := json.Marshal(reqBody)
		if err != nil {
			t.Fatal(err)
		}
		if out := callInboundSave(t, h, false, string(raw)); out.Code != 0 {
			t.Fatalf("create %s must succeed: %+v", name, out)
		}
		rows, err := r.ListNodeInbounds()
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.Name == name {
				return &row
			}
		}
		t.Fatalf("created inbound %s not found", name)
		return nil
	}

	// (a) Omitted field + eligible SS2022 => switch ON.
	if got := create("meter-default-on", 25411, nil); got.MeteringEnabled != 1 {
		t.Fatalf("omitted meteringEnabled on eligible SS2022 must default ON, got %d", got.MeteringEnabled)
	}
	// (b) Explicit false still opts out.
	if got := create("meter-explicit-off", 25412, func(b *inboundRequest) {
		f := false
		b.MeteringEnabled = &f
	}); got.MeteringEnabled != 0 {
		t.Fatalf("explicit meteringEnabled=false must stay OFF, got %d", got.MeteringEnabled)
	}
	// (c) Omitted field + ineligible protocol (VLESS) => stays OFF, and
	// the create must not be refused.
	if got := create("meter-vless", 25413, func(b *inboundRequest) {
		b.Protocol = kernel.ProtocolVLESS
		b.Settings = kernel.Settings{UUID: "11111111-2222-3333-4444-555555555555"}
		b.Security = kernel.Security{Type: kernel.SecurityNone}
	}); got.MeteringEnabled != 0 {
		t.Fatalf("omitted meteringEnabled on VLESS must stay OFF, got %d", got.MeteringEnabled)
	}
	// (d) Omitted field + shape-blocked (relay-only) SS2022 => stays OFF.
	if got := create("meter-relay-only", 25414, func(b *inboundRequest) {
		b.RelayOnly = true
	}); got.MeteringEnabled != 0 {
		t.Fatalf("omitted meteringEnabled on relay-only SS2022 must stay OFF, got %d", got.MeteringEnabled)
	}

	// (e) Update never defaults: omitting the field keeps the stored
	// switch (here OFF) instead of turning metering on.
	updateBody := inboundRequest{
		ID: seed.ID, Name: "meter-seed-renamed", Protocol: old.Protocol, Listen: old.Listen,
		Settings: old.Settings, Transport: old.Transport, Security: old.Security,
	}
	raw, err := json.Marshal(updateBody)
	if err != nil {
		t.Fatal(err)
	}
	if out := callInboundSave(t, h, true, string(raw)); out.Code != 0 {
		t.Fatalf("update must succeed: %+v", out)
	}
	got, err := r.GetNodeInbound(seed.ID)
	if err != nil || got == nil {
		t.Fatalf("reload inbound: %v", err)
	}
	if got.MeteringEnabled != 0 {
		t.Fatalf("update omitting meteringEnabled must keep stored OFF, got %d", got.MeteringEnabled)
	}
}

// FIX-031 (AUDIT-HIST), panel half: the kernel apply payload must carry
// the inbound's listen address as the ports entry's bind, so the agent
// can tighten its nft counter rules to that address. A default-listen
// inbound renders bind "::" (the agent treats it as wildcard).
func TestFix031ApplyPayloadCarriesBindIP(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/bind-payload.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	nodeID := seedRelayTargetNode(t, r, "bind-node", "198.51.100.24")
	box := seedChainKernel(t, r, nodeID, kernel.TypeSingbox)
	ss := kernel.Settings{Method: "2022-blake3-aes-128-gcm", Password: "AAECAwQFBgcICQoLDA0ODw=="}
	seedChainInbound(t, r, box, "bind-bound", kernel.Inbound{
		Protocol: kernel.ProtocolShadowsocks, Port: 25421, Listen: "127.0.0.1",
		Settings: ss, Security: kernel.Security{Type: kernel.SecurityNone},
	}, 0)
	seedChainInbound(t, r, box, "bind-default", kernel.Inbound{
		Protocol: kernel.ProtocolShadowsocks, Port: 25422,
		Settings: ss, Security: kernel.Security{Type: kernel.SecurityNone},
	}, 0)

	payload, _, err := h.buildKernelApplyPayload(box)
	if err != nil {
		t.Fatal(err)
	}
	ports, ok := payload["ports"].([]map[string]interface{})
	if !ok {
		t.Fatalf("payload ports = %#v, want []map[string]interface{}", payload["ports"])
	}
	binds := map[int64]string{}
	for _, p := range ports {
		id, _ := p["inboundId"].(int64)
		bind, _ := p["bind"].(string)
		binds[id] = bind
	}
	rows, err := r.ListNodeInbounds()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		want := "::"
		if row.Name == "bind-bound" {
			want = "127.0.0.1"
		}
		if binds[row.ID] != want {
			t.Fatalf("inbound %s (id %d) bind = %q, want %q (all ports: %#v)", row.Name, row.ID, binds[row.ID], want, ports)
		}
	}
}
