package kernel

import (
	"strings"
	"testing"
)

// Combination regression (game-chain audit, 2026-11-11): an SS2022
// inbound whose chained egress is an imported SOCKS5 proxy. The
// three legs of the user's production stack (SS2022 inbound, group
// tunnel relay, socks chain outbound) were each unit-covered in
// isolation, but nothing rendered this composition — the sing-box
// half lives here, the tunnel-generation half in
// internal/http/handler/combo_ss2022_tunnel_test.go. The composition
// must stay fail-closed: every byte of the chained inbound leaves
// via the socks outbound, and the outbound's shape is the minimal
// verbatim form (host/port as stored, version "5", credentials
// only when set) with no direct fallback.
func TestComboSS2022InboundSocksChainSingbox(t *testing.T) {
	via := Endpoint{Host: "proxy.example.com", Port: 12345, Direct: true, Proxy: Proxy{
		"type": "socks5", "server": "proxy.example.com", "port": 12345,
		"username": "combo-user", "password": "combo-pass"}}
	in := Inbound{ID: 3, Protocol: ProtocolShadowsocks, Port: 11080,
		Settings: Settings{Method: "2022-blake3-aes-128-gcm", Password: "MTIzNDU2Nzg5MDEyMzQ1Ng=="},
		Users:    chainUser, Via: &via}
	plain := Inbound{ID: 4, Protocol: ProtocolTrojan, Port: 444, Settings: Settings{Password: "p"},
		Security: Security{Type: SecurityTLS, SNI: "a.example.com", CertPEM: "c", KeyPEM: "k"}, Users: chainUser}
	cfg := renderJSON(t, TypeSingbox, []Inbound{in, plain})

	// The SS2022 inbound itself: no network restriction is
	// rendered, so sing-box's TCP+UDP dual-stack default applies.
	var ssInbound map[string]interface{}
	for _, raw := range cfg["inbounds"].([]interface{}) {
		m := raw.(map[string]interface{})
		if m["type"] == "shadowsocks" {
			ssInbound = m
		}
	}
	if ssInbound == nil {
		t.Fatalf("no shadowsocks inbound rendered: %v", cfg["inbounds"])
	}
	if ssInbound["method"] != "2022-blake3-aes-128-gcm" {
		t.Fatalf("ss2022 method: %v", ssInbound)
	}
	if _, ok := ssInbound["network"]; ok {
		t.Fatalf("ss2022 inbound must not render a network restriction: %v", ssInbound)
	}

	ob := outboundByTag(cfg, "via-3")
	if ob == nil || ob["type"] != "socks" || ob["version"] != "5" ||
		ob["server"] != "proxy.example.com" || ob["server_port"] != float64(12345) ||
		ob["username"] != "combo-user" || ob["password"] != "combo-pass" ||
		ob["routing_mark"] != nil {
		t.Fatalf("socks chain outbound: %v", ob)
	}
	if u := outboundByTag(cfg, "via-3-u7"); u != nil {
		// Shadowsocks is not a per-user protocol in this kernel
		// model (SupportsUsers), so no counted-user clone of the
		// chain outbound is rendered for it — unlike the VLESS/
		// Trojan shapes. The chain rule below is unconditional on
		// the inbound tag instead.
		t.Fatalf("ss2022 chain must not render a counted-user clone: %v", u)
	}

	route := cfg["route"].(map[string]interface{})
	got := strings.Join(ruleOutbounds(route["rules"].([]interface{}), "outbound"), ",")
	// The private-range reject guard precedes the chain rules, and
	// the chain rules precede the per-user direct rules: nothing of
	// the chained inbound can fall through to "final: direct".
	if got != "reject,via-3,flmx-u7" || route["final"] != "direct" {
		t.Fatalf("rule order: %s (final=%v)", got, route["final"])
	}
}
