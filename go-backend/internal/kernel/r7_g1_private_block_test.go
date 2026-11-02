package kernel

import (
	"strings"
	"testing"
)

// R7-001: sing-box renders must reject private destinations like the
// Xray freedom outbound does (D-GEO-01). The guard is a route rule
// {"ip_is_private": true, "action": "reject"} ahead of every chained,
// split and per-user rule, present even when no other rule exists.

func g1RouteRules(t *testing.T, cfg map[string]interface{}) []interface{} {
	t.Helper()
	route, ok := cfg["route"].(map[string]interface{})
	if !ok {
		t.Fatalf("sing-box render must always carry a route with the private guard: %v", cfg)
	}
	if route["final"] != "direct" {
		t.Fatalf("route final must stay direct: %v", route)
	}
	return route["rules"].([]interface{})
}

func privateGuardIndex(rules []interface{}) int {
	for i, r := range rules {
		m := r.(map[string]interface{})
		if m["ip_is_private"] == true && m["action"] == "reject" {
			return i
		}
	}
	return -1
}

func TestSingboxPrivateGuardPlainInbound(t *testing.T) {
	// No chain, no split, no counted users: the old render emitted no
	// route at all, so this is the case that leaked by default.
	for _, in := range []Inbound{
		{ID: 1, Protocol: ProtocolVLESS, Port: 443, Settings: Settings{UUID: "00000000-0000-4000-8000-000000000000"}},
		{ID: 2, Protocol: ProtocolHysteria2, Port: 443, Settings: Settings{Password: "p"},
			Security: Security{Type: SecurityTLS, SNI: "a.example.com", CertPEM: "c", KeyPEM: "k"}},
		{ID: 3, Protocol: ProtocolTUIC, Port: 8443, Settings: Settings{UUID: "00000000-0000-4000-8000-000000000000", Password: "p"},
			Security: Security{Type: SecurityTLS, SNI: "a.example.com", CertPEM: "c", KeyPEM: "k"}},
	} {
		cfg := renderJSON(t, TypeSingbox, []Inbound{in})
		rules := g1RouteRules(t, cfg)
		// The head guard plus the unconditional resolve + reject pair
		// the domain form (S1(b)) appends for the final fallthrough.
		if len(rules) != 3 {
			t.Fatalf("inbound %d (%s): guard plus fallthrough pair expected, got %v", in.ID, in.Protocol, rules)
		}
		guard := rules[0].(map[string]interface{})
		if len(guard) != 2 || guard["ip_is_private"] != true || guard["action"] != "reject" {
			t.Fatalf("inbound %d: guard must be exactly ip_is_private -> reject, got %v", in.ID, guard)
		}
	}
}

func TestSingboxPrivateGuardPrecedesSplitAndUserRules(t *testing.T) {
	// A split rule matching a private CIDR directly, plus one sending
	// example traffic to a proxy, plus counted users: the guard must
	// still win over all of them.
	in := splitInbound()
	in.Split = append(in.Split, SplitRule{IPs: []string{"10.0.0.0/8"}, Action: SplitDirect})
	cfg := renderJSON(t, TypeSingbox, []Inbound{in})
	rules := g1RouteRules(t, cfg)
	guard := privateGuardIndex(rules)
	if guard < 0 {
		t.Fatalf("guard missing: %v", rules)
	}
	for i, r := range rules {
		m := r.(map[string]interface{})
		if m["action"] == "sniff" {
			if i > guard {
				t.Fatalf("sniff rules stay ahead of the guard: %v", rules)
			}
			continue
		}
		if i == guard {
			continue
		}
		if i < guard {
			t.Fatalf("rule %d precedes the guard and could steer private traffic: %v", i, m)
		}
	}
	// The private-CIDR split rule itself is still rendered (the panel
	// keeps user configuration); it is simply unreachable for private
	// destinations because the guard matches first.
	found := false
	for _, r := range rules {
		m := r.(map[string]interface{})
		if cidrs, ok := m["ip_cidr"].([]interface{}); ok {
			for _, c := range cidrs {
				if c == "10.0.0.0/8" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatalf("the user's private split rule should still be rendered: %v", rules)
	}
}

func TestSingboxPrivateGuardPrecedesChainRules(t *testing.T) {
	via := Endpoint{Host: "1.2.3.4", Port: 443, Proxy: Proxy{
		"type": "trojan", "server": "1.2.3.4", "port": 443, "password": "tpw", "sni": "t.example.com"}}
	in := Inbound{ID: 1, Protocol: ProtocolVLESS, Port: 443, Settings: Settings{UUID: "00000000-0000-4000-8000-000000000000"},
		Users: chainUser, Via: &via}
	cfg := renderJSON(t, TypeSingbox, []Inbound{in})
	rules := g1RouteRules(t, cfg)
	if guard := privateGuardIndex(rules); guard != 0 {
		t.Fatalf("guard must be the first rule ahead of the chain rules, index %d: %v", guard, rules)
	}
}

func TestSingboxPrivateGuardSurvivesOutboundIPPolicy(t *testing.T) {
	// applyOutboundIPPolicies reorders rules (sniff first, then scoped,
	// fallbacks, shared): the guard has no inbound tag, so without its
	// preprocess classification it would be demoted behind the direct
	// fallback and split rules of a restricted inbound.
	plain := Inbound{ID: 1, Protocol: ProtocolVLESS, Port: 8388,
		Settings: Settings{UUID: "00000000-0000-4000-8000-000000000000"}, OutboundIPStrategy: OutboundIPv4Only}
	cfg := renderJSON(t, TypeSingbox, []Inbound{plain})
	rules := g1RouteRules(t, cfg)
	if guard := privateGuardIndex(rules); guard != 0 {
		t.Fatalf("guard must stay first under an outbound IP policy, index %d: %v", guard, rules)
	}

	split := splitInbound()
	split.OutboundIPStrategy = OutboundIPv4Only
	cfg = renderJSON(t, TypeSingbox, []Inbound{split})
	rules = g1RouteRules(t, cfg)
	guard := privateGuardIndex(rules)
	if guard < 0 {
		t.Fatalf("guard missing under an outbound IP policy: %v", rules)
	}
	for i, r := range rules {
		m := r.(map[string]interface{})
		if _, isOutbound := m["outbound"].(string); isOutbound && i < guard {
			t.Fatalf("outbound rule %d precedes the guard under an outbound IP policy: %v", i, rules)
		}
	}
}

func TestXrayRenderHasNoSingboxGuard(t *testing.T) {
	// Xray blocks private destinations inside freedom itself; its
	// render must not gain the sing-box rule (or any ip_is_private
	// field) as a side effect of this fix.
	for _, in := range []Inbound{
		{ID: 1, Protocol: ProtocolVLESS, Port: 443, Settings: Settings{UUID: "00000000-0000-4000-8000-000000000000"}},
		splitInbound(),
	} {
		r, err := Render(TypeXray, []Inbound{in})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(r.Files[0].Content, "ip_is_private") {
			t.Fatalf("xray render must stay free of the sing-box guard:\n%s", r.Files[0].Content)
		}
	}
}
