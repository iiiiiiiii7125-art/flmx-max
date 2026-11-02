package kernel

import (
	"net/netip"
	"reflect"
	"testing"
)

// R7-001 domain form (RM8 S1(b)): the head guard only matches
// IP-literal destinations, so domain destinations that resolve to
// private addresses slipped past it to the direct exit. The render
// now pairs a route "resolve" action with the same private-range
// reject rule ahead of every direct-exit rule (matcher-conditioned,
// so chained and split-proxy traffic keeps its domain and resolves
// at the far end) plus one unconditional pair for the final
// fallthrough. These tests pin that structure on the rendered JSON;
// the sing-box 1.14.4 runtime behavior behind it (any private address
// in the answer rejects the connection, chain outbounds receive the
// domain unchanged) was verified live when the fix was written.

func s1bDirectTags(cfg map[string]interface{}) map[string]bool {
	tags := map[string]bool{}
	for _, v := range cfg["outbounds"].([]interface{}) {
		ob := v.(map[string]interface{})
		if ob["type"] == "direct" {
			if tag, ok := ob["tag"].(string); ok && tag != "" {
				tags[tag] = true
			}
		}
	}
	return tags
}

// s1bMatchers returns a rule's conditions: every key but its outbound
// selection and action.
func s1bMatchers(rule map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{}
	for k, v := range rule {
		if k == "outbound" || k == "action" {
			continue
		}
		out[k] = v
	}
	return out
}

func s1bIsResolve(rule map[string]interface{}) bool {
	return rule["action"] == "resolve"
}

// s1bPairMember reports whether rules[i] belongs to a domain-guard
// pair (or the fallthrough pair): every resolve rule in a render is
// a pair half, and a guard-shaped reject is a pair half when it
// carries conditions or closes the list behind the bare resolve.
// The unconditional head guard is not a member. Sequence-style
// assertions in the older chain/egress/split tests skip members so
// they keep measuring the substantive routing order; the pair
// placement itself is gated by s1bAssertDomainGuard.
func s1bPairMember(rules []interface{}, i int) bool {
	m := rules[i].(map[string]interface{})
	if m["action"] == "resolve" {
		return true
	}
	if s1bIsReject(m) {
		if len(m) > 2 {
			return true
		}
		if i == len(rules)-1 && i > 0 {
			prev := rules[i-1].(map[string]interface{})
			return len(prev) == 1 && prev["action"] == "resolve"
		}
	}
	return false
}

// s1bIsReject reports whether a rule is a guard reject: an action
// reject whose ip_cidr is a non-empty subset of the canonical guard
// set. A pair half carries the intersection of its anchor's own
// ip_cidr condition with the set (R8-004), so a subset — not only the
// full set — is the guard shape here; the head guard and the
// fallthrough reject carry the full set and are pinned exactly by
// the G1 tests.
func s1bIsReject(rule map[string]interface{}) bool {
	if rule["action"] != "reject" {
		return false
	}
	raw, ok := rule["ip_cidr"].([]interface{})
	if !ok || len(raw) == 0 {
		return false
	}
	canon := map[string]bool{}
	for _, c := range guardPrivateCIDRs() {
		canon[c] = true
	}
	for _, v := range raw {
		s, ok := v.(string)
		if !ok || !canon[s] {
			return false
		}
	}
	return true
}

// s1bAnchorCIDRs returns a rule's own ip_cidr condition strings, or
// nil when the rule carries none.
func s1bAnchorCIDRs(rule map[string]interface{}) []string {
	raw, ok := rule["ip_cidr"].([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// s1bCIDRIntersection independently recomputes (test-side, netip)
// the overlap of the given CIDRs with the canonical guard set.
func s1bCIDRIntersection(cidrs []string) []string {
	var canon []netip.Prefix
	for _, g := range guardPrivateCIDRs() {
		if p, err := netip.ParsePrefix(g); err == nil {
			canon = append(canon, p.Masked())
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, c := range cidrs {
		p, err := netip.ParsePrefix(c)
		if err != nil {
			continue
		}
		p = p.Masked()
		for _, g := range canon {
			var inter netip.Prefix
			switch {
			case g.Bits() <= p.Bits() && g.Contains(p.Addr()):
				inter = p
			case p.Bits() <= g.Bits() && p.Contains(g.Addr()):
				inter = g
			default:
				continue
			}
			if s := inter.String(); !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	return out
}

// s1bPairExempt reports whether a direct-exit rule needs no guard
// pair: it carries an ip_cidr condition disjoint from the canonical
// set, so it can only route to public ranges and the render emits no
// pair for it (the fallthrough pair still closes the list).
func s1bPairExempt(rule map[string]interface{}) bool {
	ac := s1bAnchorCIDRs(rule)
	return ac != nil && len(s1bCIDRIntersection(ac)) == 0
}

// s1bAssertPair checks that rules[i-2] and rules[i-1] form the guard
// pair of the direct-exit rule at i: a resolve and a reject carrying
// exactly the anchor's conditions (the reject plus the ip_cidr set).
func s1bAssertPair(t *testing.T, rules []interface{}, i int) {
	t.Helper()
	anchor := rules[i].(map[string]interface{})
	resolve := rules[i-2].(map[string]interface{})
	reject := rules[i-1].(map[string]interface{})
	if !s1bIsResolve(resolve) {
		t.Fatalf("rule %d: direct exit %v not preceded by a resolve rule: %v", i, anchor, rules)
	}
	if !s1bIsReject(reject) {
		t.Fatalf("rule %d: direct exit %v not preceded by the private reject: %v", i, anchor, rules)
	}
	if want := s1bMatchers(anchor); !reflect.DeepEqual(s1bMatchers(resolve), want) {
		t.Fatalf("rule %d: resolve conditions %v must equal the anchor's %v", i, s1bMatchers(resolve), want)
	}
	rejectMatchers := s1bMatchers(reject)
	delete(rejectMatchers, "ip_cidr")
	want := s1bMatchers(anchor)
	delete(want, "ip_cidr")
	if !reflect.DeepEqual(rejectMatchers, want) {
		t.Fatalf("rule %d: reject conditions %v must equal the anchor's %v plus the ip_cidr set", i, rejectMatchers, want)
	}
	// The reject's ip_cidr is the anchor's own ip_cidr condition
	// intersected with the canonical set — the full set when the
	// anchor carries no ip_cidr condition.
	wantCIDRs := guardPrivateCIDRs()
	if ac := s1bAnchorCIDRs(anchor); ac != nil {
		wantCIDRs = s1bCIDRIntersection(ac)
	}
	gotSet := map[string]bool{}
	for _, s := range s1bAnchorCIDRs(reject) {
		gotSet[s] = true
	}
	wantSet := map[string]bool{}
	for _, s := range wantCIDRs {
		wantSet[s] = true
	}
	if !reflect.DeepEqual(gotSet, wantSet) {
		t.Fatalf("rule %d: reject ip_cidr %v must be the anchor∩c​anonical intersection %v", i, gotSet, wantSet)
	}
	if _, leaked := resolve["outbound"]; leaked {
		t.Fatalf("rule %d: resolve half must not select an outbound: %v", i, resolve)
	}
}

// s1bAssertDomainGuard is the standing gate over a rendered sing-box
// config: every direct-exit rule carries its conditioned pair (one
// exemption: a direct-exit rule whose own ip_cidr condition is
// disjoint from the canonical set can only route to public ranges
// and gets no pair), no other rule gains one, and the list ends with
// the unconditional fallthrough pair behind a "direct" final.
func s1bAssertDomainGuard(t *testing.T, cfg map[string]interface{}) {
	t.Helper()
	rules := g1RouteRules(t, cfg)
	direct := s1bDirectTags(cfg)
	pairs := 0
	for i, v := range rules {
		rule := v.(map[string]interface{})
		tag, _ := rule["outbound"].(string)
		if tag != "" && direct[tag] {
			if s1bPairExempt(rule) {
				continue // public-only ip_cidr anchor: no pair by design
			}
			if i < 2 {
				t.Fatalf("direct rule %d has no room for its guard pair: %v", i, rules)
			}
			s1bAssertPair(t, rules, i)
			pairs++
		}
	}
	n := len(rules)
	if n < 2 || !s1bIsResolve(rules[n-2].(map[string]interface{})) || !s1bIsReject(rules[n-1].(map[string]interface{})) {
		t.Fatalf("rule list must end with the fallthrough resolve + reject pair: %v", rules)
	}
	if tail := s1bMatchers(rules[n-2].(map[string]interface{})); len(tail) != 0 {
		t.Fatalf("fallthrough resolve must be unconditional, got conditions %v", tail)
	}
	if tail := s1bMatchers(rules[n-1].(map[string]interface{})); len(tail) != 1 {
		t.Fatalf("fallthrough reject must carry only the ip_cidr set, got %v", tail)
	}
	resolves := 0
	for _, v := range rules {
		if s1bIsResolve(v.(map[string]interface{})) {
			resolves++
		}
	}
	if resolves != pairs+1 {
		t.Fatalf("expected one resolve per direct exit plus the fallthrough, got %d resolves for %d direct rules: %v", resolves, pairs, rules)
	}
}

func TestSingboxDomainGuardPlainInbound(t *testing.T) {
	in := Inbound{ID: 1, Protocol: ProtocolVLESS, Port: 443, Settings: Settings{UUID: "00000000-0000-4000-8000-000000000000"}}
	cfg := renderJSON(t, TypeSingbox, []Inbound{in})
	rules := g1RouteRules(t, cfg)
	if len(rules) != 3 {
		t.Fatalf("head guard plus fallthrough pair expected, got %v", rules)
	}
	// One predicate, two forms: the fallthrough reject is the head
	// guard's exact shape.
	if !reflect.DeepEqual(rules[0], rules[2]) {
		t.Fatalf("fallthrough reject %v must equal the head guard %v", rules[2], rules[0])
	}
	if !s1bIsResolve(rules[1].(map[string]interface{})) || len(rules[1].(map[string]interface{})) != 1 {
		t.Fatalf("fallthrough resolve must be the bare resolve action, got %v", rules[1])
	}
	s1bAssertDomainGuard(t, cfg)
}

func TestSingboxDomainGuardSplitMix(t *testing.T) {
	// splitInbound mixes split-proxy (rule sets), split-block,
	// split-direct (domain + IP matchers) and counted users.
	cfg := renderJSON(t, TypeSingbox, []Inbound{splitInbound()})
	s1bAssertDomainGuard(t, cfg)
	rules := g1RouteRules(t, cfg)
	direct := s1bDirectTags(cfg)
	// The split-direct owner rule is guarded by a pair that keeps its
	// domain condition: that is what lets the guard see the resolved
	// addresses of the very connections the rule would take.
	found := false
	for i, v := range rules {
		rule := v.(map[string]interface{})
		if rule["outbound"] == "direct" {
			if _, ok := rule["domain_suffix"]; ok {
				s1bAssertPair(t, rules, i)
				pair := rules[i-2].(map[string]interface{})
				if _, ok := pair["domain_suffix"]; !ok {
					t.Fatalf("split-direct guard pair lost the domain condition: %v", pair)
				}
				found = true
			}
		}
		if tag, _ := rule["outbound"].(string); tag != "" && !direct[tag] {
			if i >= 2 && s1bIsResolve(rules[i-2].(map[string]interface{})) && s1bIsReject(rules[i-1].(map[string]interface{})) {
				if reflect.DeepEqual(s1bMatchers(rules[i-2].(map[string]interface{})), s1bMatchers(rule)) {
					t.Fatalf("proxy rule %d gained a guard pair and would lose its domain: %v", i, rules)
				}
			}
		}
	}
	if !found {
		t.Fatalf("split-direct domain rule not found: %v", rules)
	}
}

func TestSingboxDomainGuardChainKeepsDomain(t *testing.T) {
	via := Endpoint{Host: "1.2.3.4", Port: 443, Proxy: Proxy{
		"type": "trojan", "server": "1.2.3.4", "port": 443, "password": "tpw", "sni": "t.example.com"}}
	in := Inbound{ID: 1, Protocol: ProtocolVLESS, Port: 443, Settings: Settings{UUID: "00000000-0000-4000-8000-000000000000"},
		Users: chainUser, Via: &via}
	cfg := renderJSON(t, TypeSingbox, []Inbound{in})
	s1bAssertDomainGuard(t, cfg)
	rules := g1RouteRules(t, cfg)
	// No chain rule is preceded by a pair, so chained destinations
	// keep their domain form and resolve at the far end; the only
	// resolves guard the per-user direct rule and the fallthrough.
	for i, v := range rules {
		rule := v.(map[string]interface{})
		if tag, _ := rule["outbound"].(string); len(tag) > 4 && tag[:4] == "via-" {
			if i >= 2 && s1bIsResolve(rules[i-2].(map[string]interface{})) {
				t.Fatalf("chain rule %d gained a guard pair and would lose its domain: %v", i, rules)
			}
		}
	}
}

func TestSingboxDomainGuardOutboundIPPolicy(t *testing.T) {
	plain := Inbound{ID: 1, Protocol: ProtocolVLESS, Port: 8388,
		Settings: Settings{UUID: "00000000-0000-4000-8000-000000000000"}, OutboundIPStrategy: OutboundIPv4Only}
	cfg := renderJSON(t, TypeSingbox, []Inbound{plain})
	s1bAssertDomainGuard(t, cfg)
	// The policy pass still installs its resolvers and default.
	dns := cfg["dns"].(map[string]interface{})
	if servers := dns["servers"].([]interface{}); len(servers) == 0 {
		t.Fatalf("policy resolvers missing after the domain guard pass: %v", cfg)
	}
	route := cfg["route"].(map[string]interface{})
	if _, ok := route["default_domain_resolver"]; !ok {
		t.Fatalf("default_domain_resolver missing after the domain guard pass: %v", route)
	}

	split := splitInbound()
	split.OutboundIPStrategy = OutboundIPv4Only
	cfg = renderJSON(t, TypeSingbox, []Inbound{split})
	s1bAssertDomainGuard(t, cfg)
}

func TestSingboxDomainGuardAcrossProtocols(t *testing.T) {
	// Every sing-box inbound form exits through the same route; the
	// gate must hold for each, including the shadowtls-fronted pair
	// and the UDP protocols.
	inbounds := []Inbound{
		{ID: 1, Protocol: ProtocolShadowsocks, Port: 8388, Settings: Settings{Method: "aes-128-gcm", Password: "p"}},
		{ID: 2, Protocol: ProtocolShadowsocks, Port: 8389, Settings: Settings{Method: "aes-128-gcm", Password: "p"},
			Security: Security{Type: SecurityShadowTLS, ShadowTLSPassword: "sp", SNI: "a.example.com"}},
		{ID: 3, Protocol: ProtocolHysteria2, Port: 443, Settings: Settings{Password: "p"},
			Security: Security{Type: SecurityTLS, SNI: "a.example.com", CertPEM: "c", KeyPEM: "k"}},
		{ID: 4, Protocol: ProtocolTUIC, Port: 8443, Settings: Settings{UUID: "00000000-0000-4000-8000-000000000000", Password: "p"},
			Security: Security{Type: SecurityTLS, SNI: "a.example.com", CertPEM: "c", KeyPEM: "k"}},
		{ID: 5, Protocol: ProtocolAnyTLS, Port: 8444, Settings: Settings{Password: "p"},
			Security: Security{Type: SecurityTLS, SNI: "a.example.com", CertPEM: "c", KeyPEM: "k"}},
		{ID: 6, Protocol: ProtocolVMess, Port: 8445, Settings: Settings{UUID: "00000000-0000-4000-8000-000000000000"}},
		{ID: 7, Protocol: ProtocolTrojan, Port: 8446, Settings: Settings{Password: "p"}},
	}
	for _, in := range inbounds {
		cfg := renderJSON(t, TypeSingbox, []Inbound{in})
		s1bAssertDomainGuard(t, cfg)
	}
	cfg := renderJSON(t, TypeSingbox, inbounds)
	s1bAssertDomainGuard(t, cfg)
}
