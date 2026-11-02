package kernel

import (
	"testing"
)

// R7-001 full-set gate: the private guard invariant — exactly one
// ip_is_private -> reject rule, preceded only by sniff rules — is
// asserted over the mechanically derived matrix of the sing-box
// render surface, not just the audited cases:
//   - protocols: the nine sing-box entries of capabilities.go
//     (Hysteria2, TUIC, AnyTLS, Naive, VLESS, VMess, Trojan,
//     Shadowsocks, Snell), matching the singboxInbounds switch 1:1;
//   - forms per protocol: plain, chained, split (a private-CIDR
//     direct split rule — the bypass shape), counted users where the
//     protocol supports them;
//   - plus the ShadowTLS-fronted forms, every outbound-IP-policy
//     form, and the empty render.
// A future render path or rule constructor that emits a sing-box
// route rule ahead of the guard, or drops it, fails this gate.

func assertGuardDominates(t *testing.T, label string, cfg map[string]interface{}) {
	t.Helper()
	rules := g1RouteRules(t, cfg)
	guards := 0
	for i, r := range rules {
		m := r.(map[string]interface{})
		if m["ip_is_private"] == true && m["action"] == "reject" {
			guards++
			continue
		}
		if guards == 0 && m["action"] != "sniff" {
			t.Fatalf("%s: rule %d precedes the private guard: %v (all rules: %v)", label, i, m, rules)
		}
	}
	if guards != 1 {
		t.Fatalf("%s: want exactly one private guard, got %d: %v", label, guards, rules)
	}
}

func g1MatrixInbounds() []Inbound {
	tls := Security{Type: SecurityTLS, SNI: "a.example.com", CertPEM: "c", KeyPEM: "k"}
	uuid := "00000000-0000-4000-8000-000000000000"
	return []Inbound{
		{ID: 1, Protocol: ProtocolVLESS, Port: 443, Settings: Settings{UUID: uuid}},
		{ID: 2, Protocol: ProtocolVMess, Port: 443, Settings: Settings{UUID: uuid}},
		{ID: 3, Protocol: ProtocolTrojan, Port: 443, Settings: Settings{Password: "p"}, Security: tls},
		{ID: 4, Protocol: ProtocolShadowsocks, Port: 443, Settings: Settings{Method: "aes-128-gcm", Password: "x"}},
		{ID: 5, Protocol: ProtocolSnell, Port: 443, Settings: Settings{Password: "psk-aaaaaaaa"}},
		{ID: 6, Protocol: ProtocolHysteria2, Port: 443, Settings: Settings{Password: "p"}, Security: tls},
		{ID: 7, Protocol: ProtocolTUIC, Port: 443, Settings: Settings{UUID: uuid, Password: "p"}, Security: tls},
		{ID: 8, Protocol: ProtocolAnyTLS, Port: 443, Settings: Settings{Password: "p"}, Security: tls},
		{ID: 9, Protocol: ProtocolNaive, Port: 443, Settings: Settings{Username: "u", Password: "p"}, Security: tls},
		// ShadowTLS-fronted forms (inbound detour to a loopback backend).
		{ID: 10, Protocol: ProtocolShadowsocks, Port: 443, Settings: Settings{Method: "aes-128-gcm", Password: "x"},
			Security: Security{Type: SecurityShadowTLS, SNI: "www.example.com", ShadowTLSPassword: "pw"}},
		{ID: 11, Protocol: ProtocolSnell, Port: 443, Settings: Settings{Password: "psk-cccccccc"},
			Security: Security{Type: SecurityShadowTLS, SNI: "www.example.com", ShadowTLSPassword: "pw"}},
	}
}

func TestSingboxPrivateGuardFullMatrix(t *testing.T) {
	opts := RenderOptions{SingboxSnell: true, SingboxNaive: true, SingboxHTTPClient: true}
	via := &Endpoint{Host: "1.2.3.4", Port: 443, Proxy: Proxy{
		"type": "trojan", "server": "1.2.3.4", "port": 443, "password": "tpw", "sni": "t.example.com"}}
	privateSplit := []SplitRule{{IPs: []string{"10.0.0.0/8"}, Action: SplitDirect}}

	render := func(label string, ins []Inbound) map[string]interface{} {
		t.Helper()
		r, err := RenderWith(TypeSingbox, ins, opts)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		return renderJSONString(t, r.Files[0].Content)
	}

	for _, base := range g1MatrixInbounds() {
		label := base.Protocol + "/" + base.Tag()
		assertGuardDominates(t, label+" plain", render(label, []Inbound{base}))

		chained := base
		chained.Via = via
		assertGuardDominates(t, label+" chained", render(label, []Inbound{chained}))

		split := base
		split.Split = privateSplit
		assertGuardDominates(t, label+" split-private-direct", render(label, []Inbound{split}))

		if base.SupportsUsers() {
			counted := base
			counted.Users = chainUser
			assertGuardDominates(t, label+" counted-user", render(label, []Inbound{counted}))
		}
	}

	// Every inbound at once (shared render, counted users included).
	var all []Inbound
	for _, base := range g1MatrixInbounds() {
		if base.SupportsUsers() {
			base.Users = chainUser
		}
		all = append(all, base)
	}
	assertGuardDominates(t, "all-inbounds", render("all", all))

	// Empty render still carries the guard and a final.
	assertGuardDominates(t, "empty", render("empty", nil))
}

func TestSingboxPrivateGuardOutboundIPPolicies(t *testing.T) {
	for _, policy := range []string{OutboundIPv4Only, OutboundIPv6Only, OutboundPreferIPv4, OutboundPreferIPv6} {
		plain := Inbound{ID: 1, Protocol: ProtocolVLESS, Listen: "::", Port: 8388,
			Settings: Settings{UUID: "00000000-0000-4000-8000-000000000000"},
			Users:    chainUser, OutboundIPStrategy: policy}
		assertGuardDominates(t, policy+" plain", renderJSON(t, TypeSingbox, []Inbound{plain}))

		split := splitInbound()
		split.OutboundIPStrategy = policy
		assertGuardDominates(t, policy+" split", renderJSON(t, TypeSingbox, []Inbound{split}))

		chained := plain
		chained.Via = &Endpoint{Host: "1.2.3.4", Port: 443, Proxy: Proxy{
			"type": "trojan", "server": "1.2.3.4", "port": 443, "password": "tpw", "sni": "t.example.com"}}
		assertGuardDominates(t, policy+" chained", renderJSON(t, TypeSingbox, []Inbound{chained}))
	}
}
