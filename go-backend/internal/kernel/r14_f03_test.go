package kernel

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

// R14-F03 (A5-01, P1): chained inbound + idle timeout + split-direct
// referenced idle outbounds that were never emitted (xrayConnIdleRoutes
// skipped Via != nil inbounds while xraySplitRoutes unconditionally
// pointed split-direct matches at flmx-idle-<id>), so Xray logged
// "non existing outTag: flmx-idle-<id>" and the timeout silently ran
// the level-0 default (and counted users lost their socket mark).
//
// Regression probe: the A5 7-scenario set. Every routing rule's
// outboundTag must resolve to an emitted outbound tag in every
// scenario. Written RED first: chained-idle-split-direct and
// all-together fail before the fix (dangling flmx-idle-3 /
// flmx-idle-3-u7).

func r14f03ChainTarget() Inbound {
	return Inbound{ID: 9, Protocol: ProtocolVLESS, Port: 8443,
		Settings:  Settings{UUID: "22222222-2222-4222-8222-222222222222"},
		Transport: Transport{Type: TransportTCP}, Security: Security{Type: SecurityNone}}
}

func r14f03ChainVia() *Endpoint {
	return &Endpoint{Host: "5.6.7.8", Port: 8443, Direct: true, Inbound: r14f03ChainTarget()}
}

func r14f03SplitDirectRule() SplitRule {
	return SplitRule{Domains: []string{"example.com"}, IPs: []string{"1.2.3.0/24"}, Action: SplitDirect}
}

func r14f03SplitProxyRule() SplitRule {
	unlock := Endpoint{Host: "5.6.7.8", Port: 8388, Proxy: Proxy{"type": "ss", "server": "5.6.7.8", "port": 8388,
		"cipher": "aes-128-gcm", "password": "pw"}}
	return SplitRule{Categories: []string{"netflix"}, Action: SplitProxy, OutboundID: 9, Via: &unlock}
}

func r14f03SplitBlockRule() SplitRule {
	return SplitRule{Categories: []string{"ads"}, Action: SplitBlock}
}

func r14f03BaseInbound() Inbound {
	return Inbound{ID: 3, Protocol: ProtocolVLESS, Port: 443, Settings: Settings{UUID: testUUID},
		Transport: Transport{Type: TransportTCP}, Security: Security{Type: SecurityNone},
		Users: chainUser, ConnIdleSeconds: 5}
}

// assertAllRuleTagsResolve is the A5 probe invariant: no routing rule
// may reference an outbound tag the config does not emit.
func assertAllRuleTagsResolve(t *testing.T, parsed map[string]interface{}) {
	t.Helper()
	tags := map[string]bool{}
	for _, o := range connIdleOutbounds(t, parsed) {
		if m, _ := o.(map[string]interface{}); m != nil {
			if tag, _ := m["tag"].(string); tag != "" {
				tags[tag] = true
			}
		}
	}
	for _, r := range connIdleRules(t, parsed) {
		m := r.(map[string]interface{})
		tag, _ := m["outboundTag"].(string)
		if tag == "" {
			continue
		}
		if !tags[tag] {
			t.Fatalf("rule references unemitted outboundTag %q: %v (outbounds: %v)", tag, m, parsed["outbounds"])
		}
	}
}

func TestR14F03RoutingTagsResolve(t *testing.T) {
	scenarios := []struct {
		name string
		in   Inbound
	}{
		{"direct-idle", r14f03BaseInbound()},
		{"chained-idle", func() Inbound { in := r14f03BaseInbound(); in.Via = r14f03ChainVia(); return in }()},
		{"split-direct-idle", func() Inbound { in := r14f03BaseInbound(); in.Split = []SplitRule{r14f03SplitDirectRule()}; return in }()},
		{"split-block", func() Inbound { in := r14f03BaseInbound(); in.Split = []SplitRule{r14f03SplitBlockRule()}; return in }()},
		{"split-proxy", func() Inbound { in := r14f03BaseInbound(); in.Split = []SplitRule{r14f03SplitProxyRule()}; return in }()},
		{"chained-idle-split-direct", func() Inbound {
			in := r14f03BaseInbound()
			in.Via = r14f03ChainVia()
			in.Split = []SplitRule{r14f03SplitDirectRule()}
			return in
		}()},
		{"all-together", func() Inbound {
			in := r14f03BaseInbound()
			in.Via = r14f03ChainVia()
			in.Split = []SplitRule{r14f03SplitDirectRule(), r14f03SplitProxyRule(), r14f03SplitBlockRule()}
			return in
		}()},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			parsed := renderConnIdle(t, TypeXray, []Inbound{sc.in})
			assertAllRuleTagsResolve(t, parsed)
		})
	}
}

func TestR14F03ChainedIdleSplitDirect(t *testing.T) {
	// Targeted: the chained idle inbound's split-direct matches leave
	// through dedicated idle outbounds carrying the level (owner) and
	// the level + socket mark (counted user); the default exit stays
	// the chain outbound; no dead catch-all idle rule is emitted (the
	// chain default rule matches first).
	in := r14f03BaseInbound()
	in.Via = r14f03ChainVia()
	in.Split = []SplitRule{r14f03SplitDirectRule()}
	parsed := renderConnIdle(t, TypeXray, []Inbound{in})

	ob := connIdleOutbound(t, parsed, "flmx-idle-3")
	if ob == nil {
		t.Fatalf("chained idle inbound with split-direct must emit flmx-idle-3: %v", parsed["outbounds"])
	}
	if ob["protocol"] != "freedom" {
		t.Fatalf("flmx-idle-3 protocol = %v, want freedom", ob["protocol"])
	}
	if got := connIdleUserLevel(t, ob); got != 1 {
		t.Fatalf("flmx-idle-3 userLevel = %v, want 1", got)
	}
	comb := connIdleOutbound(t, parsed, "flmx-idle-3-u7")
	if comb == nil {
		t.Fatalf("chained idle inbound with split-direct must emit flmx-idle-3-u7: %v", parsed["outbounds"])
	}
	if got := connIdleUserLevel(t, comb); got != 1 {
		t.Fatalf("flmx-idle-3-u7 userLevel = %v, want 1", got)
	}
	stream, _ := comb["streamSettings"].(map[string]interface{})
	sockopt, _ := stream["sockopt"].(map[string]interface{})
	if sockopt == nil || sockopt["mark"] != float64(UserMark(7)) {
		t.Fatalf("flmx-idle-3-u7 mark = %v, want %d (metering must survive the split-direct leg)", sockopt, UserMark(7))
	}
	// Chain default exit still carries the level on its user.
	chainOb := connIdleOutbound(t, parsed, "via-3")
	if chainOb == nil {
		t.Fatalf("chain outbound via-3 missing")
	}
	settings, _ := chainOb["settings"].(map[string]interface{})
	vnext, _ := settings["vnext"].([]interface{})
	users, _ := vnext[0].(map[string]interface{})["users"].([]interface{})
	if users[0].(map[string]interface{})["level"] != float64(1) {
		t.Fatalf("via-3 user level = %v, want 1", users[0])
	}
	rules := connIdleRules(t, parsed)
	// Split-direct matches (domain + IP fragments, user first) then
	// the chain default rules, then the user's mark rule. No plain
	// idle catch-all: it would be dead behind the chain rule.
	if got := strings.Join(ruleOutbounds(rules, "outboundTag"), ","); got != "flmx-idle-3-u7,flmx-idle-3,flmx-idle-3-u7,flmx-idle-3,via-3-u7,via-3,flmx-u7" {
		t.Fatalf("rule order = %s, want flmx-idle-3-u7,flmx-idle-3,flmx-idle-3-u7,flmx-idle-3,via-3-u7,via-3,flmx-u7", got)
	}
	for _, r := range rules {
		m := r.(map[string]interface{})
		if m["outboundTag"] != "flmx-idle-3" && m["outboundTag"] != "flmx-idle-3-u7" {
			continue
		}
		if m["domain"] == nil && m["ip"] == nil && m["protocol"] == nil {
			t.Fatalf("dead catch-all idle rule for chained inbound: %v", m)
		}
	}
}

func TestR14F03ChainedIdleNoSplitDirectGetsNoIdleOutbound(t *testing.T) {
	// Guard the other side of the emission rule: a chained idle
	// inbound with no split-direct rule still gets no idle outbound
	// (its default exit is the chain outbound, which carries the
	// level) — TestConnIdleChainOutboundLevel's contract, kept.
	in := r14f03BaseInbound()
	in.Via = r14f03ChainVia()
	parsed := renderConnIdle(t, TypeXray, []Inbound{in})
	if ob := connIdleOutbound(t, parsed, "flmx-idle-3"); ob != nil {
		t.Fatalf("chained idle inbound without split-direct must not get an idle outbound: %v", ob)
	}
	assertAllRuleTagsResolve(t, parsed)
}

func TestR14F03SplitFallbackWhenIdleOutboundMissing(t *testing.T) {
	// Layer ② in isolation: xraySplitRoutes must never emit a
	// dangling idle reference even when the idle outbound was not
	// emitted (whatever the reason). It falls back to the shared
	// direct / mark outbounds — the mark survives, only the timeout
	// degrades to level-0 — and logs the degradation.
	in := r14f03BaseInbound()
	in.Split = []SplitRule{r14f03SplitDirectRule()}
	levels := map[int64]int{3: 1}
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)
	_, rules, err := xraySplitRoutes(in, levels, map[int64]bool{})
	if err != nil {
		t.Fatalf("xraySplitRoutes: %v", err)
	}
	for _, r := range rules {
		m := r.(map[string]interface{})
		if tag, _ := m["outboundTag"].(string); strings.HasPrefix(tag, "flmx-idle-") {
			t.Fatalf("fallback must not reference idle outbound %q: %v", tag, m)
		}
	}
	if got := strings.Join(ruleOutbounds(rules, "outboundTag"), ","); got != "flmx-u7,direct,flmx-u7,direct" {
		t.Fatalf("fallback rule order = %s, want flmx-u7,direct,flmx-u7,direct", got)
	}
	if !strings.Contains(buf.String(), "入站 3") || !strings.Contains(buf.String(), "flmx-idle-3") {
		t.Fatalf("fallback must log the degradation, got log: %q", buf.String())
	}
}
