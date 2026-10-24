package kernel

import (
	"encoding/json"
	"strings"
	"testing"
)

// connIdle: per-inbound Xray policy.levels[].connIdle (seconds).
// 0 = unset: no policy segment is emitted and Xray's level-0 default
// (300s) applies. 1..MaxConnIdleSeconds are the only accepted values;
// 0 is never written to the config as an explicit value.

func renderConnIdle(t *testing.T, kernelType string, inbounds []Inbound) map[string]interface{} {
	t.Helper()
	out, err := Render(kernelType, inbounds)
	if err != nil {
		t.Fatalf("render %s: %v", kernelType, err)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(out.Files[0].Content), &parsed); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	return parsed
}

func policyLevels(t *testing.T, parsed map[string]interface{}) map[string]interface{} {
	t.Helper()
	policy, ok := parsed["policy"].(map[string]interface{})
	if !ok {
		t.Fatalf("policy missing: %v", parsed["policy"])
	}
	levels, ok := policy["levels"].(map[string]interface{})
	if !ok {
		t.Fatalf("policy.levels missing: %v", policy)
	}
	return levels
}

func connIdleClients(t *testing.T, parsed map[string]interface{}, idx int) []interface{} {
	t.Helper()
	inbounds, ok := parsed["inbounds"].([]interface{})
	if !ok || idx >= len(inbounds) {
		t.Fatalf("inbounds[%d] missing", idx)
	}
	item, _ := inbounds[idx].(map[string]interface{})
	settings, _ := item["settings"].(map[string]interface{})
	clients, _ := settings["clients"].([]interface{})
	return clients
}

func TestConnIdleUnsetRendersNoPolicy(t *testing.T) {
	in := Inbound{ID: 1, Protocol: ProtocolVLESS, Port: 443, Settings: Settings{UUID: testUUID}, Security: Security{Type: SecurityNone}}
	parsed := renderConnIdle(t, TypeXray, []Inbound{in})
	if _, ok := parsed["policy"]; ok {
		t.Fatalf("unset connIdle must not emit a policy segment: %v", parsed["policy"])
	}
}

func TestConnIdleSingleInbound(t *testing.T) {
	in := Inbound{ID: 7, Protocol: ProtocolVLESS, Port: 443, Settings: Settings{UUID: testUUID}, Security: Security{Type: SecurityNone}, ConnIdleSeconds: 120,
		Users: []User{{ID: 3, UUID: testUUID}}}
	parsed := renderConnIdle(t, TypeXray, []Inbound{in})
	levels := policyLevels(t, parsed)
	l1, ok := levels["1"].(map[string]interface{})
	if !ok {
		t.Fatalf("level 1 missing: %v", levels)
	}
	if l1["connIdle"] != float64(120) {
		t.Fatalf("level 1 connIdle = %v, want 120", l1["connIdle"])
	}
	clients := connIdleClients(t, parsed, 0)
	if len(clients) != 2 {
		t.Fatalf("clients = %d, want 2 (owner + user)", len(clients))
	}
	for i, c := range clients {
		if c.(map[string]interface{})["level"] != float64(1) {
			t.Fatalf("clients[%d].level = %v, want 1", i, c.(map[string]interface{})["level"])
		}
	}
}

func TestConnIdleMultipleInboundsDistinctLevels(t *testing.T) {
	// Deliberately out of ID order: levels follow inbound ID, not input order.
	a := Inbound{ID: 9, Protocol: ProtocolVLESS, Port: 8443, Settings: Settings{UUID: testUUID}, Security: Security{Type: SecurityNone}, ConnIdleSeconds: 600}
	b := Inbound{ID: 3, Protocol: ProtocolVMess, Port: 8444, Settings: Settings{UUID: testUUID}, Security: Security{Type: SecurityNone}, ConnIdleSeconds: 120}
	c := Inbound{ID: 5, Protocol: ProtocolTrojan, Port: 8445, Settings: Settings{Password: "secret123"}, Security: Security{Type: SecurityNone}}
	parsed := renderConnIdle(t, TypeXray, []Inbound{a, b, c})
	levels := policyLevels(t, parsed)
	if len(levels) != 2 {
		t.Fatalf("levels = %d, want 2 (unset inbound gets none)", len(levels))
	}
	if levels["1"].(map[string]interface{})["connIdle"] != float64(120) {
		t.Fatalf("level 1 (inbound 3) connIdle = %v, want 120", levels["1"])
	}
	if levels["2"].(map[string]interface{})["connIdle"] != float64(600) {
		t.Fatalf("level 2 (inbound 9) connIdle = %v, want 600", levels["2"])
	}
	// Rendered inbounds are ID-sorted: [3, 5, 9]; only 3 and 9 carry levels.
	if got := connIdleClients(t, parsed, 0)[0].(map[string]interface{})["level"]; got != float64(1) {
		t.Fatalf("inbound 3 client level = %v, want 1", got)
	}
	if got := connIdleClients(t, parsed, 2)[0].(map[string]interface{})["level"]; got != float64(2) {
		t.Fatalf("inbound 9 client level = %v, want 2", got)
	}
	// The unset inbound (ID 5) must not gain a level field at all.
	if _, ok := connIdleClients(t, parsed, 1)[0].(map[string]interface{})["level"]; ok {
		t.Fatalf("unset inbound must not carry a level")
	}
}

func TestConnIdleShadowsocks(t *testing.T) {
	// Single-password SS: the top-level settings carry the level (upstream
	// Xray ShadowsocksServerConfig.level), so the primary credential is
	// covered, not just subscription-user clients.
	in := Inbound{ID: 2, Protocol: ProtocolShadowsocks, Port: 8388, Settings: Settings{Method: DefaultSSMethod, Password: "QUJDREVGR0hJSktMTU5PUA=="}, Security: Security{Type: SecurityNone}, ConnIdleSeconds: 300}
	parsed := renderConnIdle(t, TypeXray, []Inbound{in})
	levels := policyLevels(t, parsed)
	if levels["1"].(map[string]interface{})["connIdle"] != float64(300) {
		t.Fatalf("level 1 connIdle = %v, want 300", levels["1"])
	}
	inbounds := parsed["inbounds"].([]interface{})
	settings := inbounds[0].(map[string]interface{})["settings"].(map[string]interface{})
	if settings["level"] != float64(1) {
		t.Fatalf("ss settings.level = %v, want 1", settings["level"])
	}
	// Companion (multi-user) SS: user clients carry the level too.
	comp := Inbound{ID: 4, Protocol: ProtocolShadowsocks, Port: 8389, Settings: Settings{Method: DefaultSSMethod, Password: "QUJDREVGR0hJSktMTU5PUA=="}, Security: Security{Type: SecurityNone}, ConnIdleSeconds: 60,
		Companion: true, Users: []User{{ID: 9, Password: "QUJDREVGR0hJSktMTU5PUA=="}}}
	parsed = renderConnIdle(t, TypeXray, []Inbound{comp})
	clients := connIdleClients(t, parsed, 0)
	if len(clients) != 1 {
		t.Fatalf("companion clients = %d, want 1", len(clients))
	}
	if clients[0].(map[string]interface{})["level"] != float64(1) {
		t.Fatalf("companion client level = %v, want 1", clients[0].(map[string]interface{})["level"])
	}
}

func TestConnIdleNormalize(t *testing.T) {
	base := func() Inbound {
		return Inbound{ID: 1, Protocol: ProtocolVLESS, Port: 443, Settings: Settings{UUID: testUUID}, Security: Security{Type: SecurityNone}}
	}
	valid := []int{0, 1, 300, MaxConnIdleSeconds}
	for _, v := range valid {
		in := base()
		in.ConnIdleSeconds = v
		if err := Normalize(TypeXray, &in); err != nil {
			t.Fatalf("connIdle %d must be accepted on xray: %v", v, err)
		}
	}
	invalid := []int{-1, MaxConnIdleSeconds + 1}
	for _, v := range invalid {
		in := base()
		in.ConnIdleSeconds = v
		if err := Normalize(TypeXray, &in); err == nil {
			t.Fatalf("connIdle %d must be rejected", v)
		} else if !strings.Contains(err.Error(), "空闲超时") {
			t.Fatalf("connIdle %d error must name the field: %v", v, err)
		}
	}
	// Non-Xray kernels have no connIdle: an explicit value is refused with
	// a plain-language reason instead of being silently dropped. Each
	// case uses a protocol its kernel actually serves, so the failure is
	// the connIdle gate itself, not an unrelated combo rejection.
	snell := Inbound{ID: 1, Protocol: ProtocolSnell, Port: 1234, Settings: Settings{Password: "secret-password-1"}, Security: Security{Type: SecurityNone}, ConnIdleSeconds: 120}
	if err := Normalize(TypeSnell, &snell); err == nil {
		t.Fatal("connIdle on snell must be rejected")
	} else if !strings.Contains(err.Error(), "空闲超时") {
		t.Fatalf("connIdle on snell error must name the field: %v", err)
	}
	{
		in := base()
		in.ConnIdleSeconds = 120
		if err := Normalize(TypeSingbox, &in); err == nil {
			t.Fatal("connIdle on sing-box must be rejected")
		} else if !strings.Contains(err.Error(), "空闲超时") {
			t.Fatalf("connIdle on sing-box error must name the field: %v", err)
		}
	}
	// Unset stays valid everywhere.
	in := base()
	if err := Normalize(TypeSingbox, &in); err != nil {
		t.Fatalf("unset connIdle must be accepted on sing-box: %v", err)
	}
}

func TestConnIdleSingboxRenderIgnores(t *testing.T) {
	// Legacy stored rows could carry the value; the sing-box renderer must
	// never emit a policy segment for it (there is no sing-box equivalent).
	in := Inbound{ID: 1, Protocol: ProtocolVLESS, Port: 443, Settings: Settings{UUID: testUUID}, Security: Security{Type: SecurityNone}, ConnIdleSeconds: 120}
	out, err := Render(TypeSingbox, []Inbound{in})
	if err != nil {
		t.Fatalf("render sing-box: %v", err)
	}
	if strings.Contains(out.Files[0].Content, "connIdle") || strings.Contains(out.Files[0].Content, "\"policy\"") {
		t.Fatalf("sing-box render must not emit connIdle/policy: %s", out.Files[0].Content)
	}
}

func TestConnIdleLevelCap(t *testing.T) {
	// Xray SS server level is a byte (0-255); levels are allocated 1..N,
	// so a render needing more than 255 distinct levels must fail closed
	// with a named inbound instead of overflowing the byte.
	inbounds := make([]Inbound, 0, 256)
	for i := 1; i <= 256; i++ {
		inbounds = append(inbounds, Inbound{ID: int64(i), Protocol: ProtocolVLESS, Port: 10000 + i, Settings: Settings{UUID: testUUID}, Security: Security{Type: SecurityNone}, ConnIdleSeconds: 120})
	}
	if _, err := Render(TypeXray, inbounds); err == nil {
		t.Fatal("256 connIdle inbounds must fail closed at the level cap")
	}
}

// --- connIdle exit-side enforcement (fix-connidle-level) ---
//
// Root cause (Xray-core v25.9.5 and v26.3.27, identical here): the VLESS
// inbound never installs a user-level idle timer —
// proxy/vless/inbound/inbound.go uses ForLevel(0) for the handshake
// deadline and no CancelAfterInactivity in the normal path (VMess,
// Trojan and SS server all re-fetch ForLevel(request.User.Level)).
// The data-path timer for a VLESS→freedom session is installed solely
// by the freedom outbound (proxy/freedom/freedom.go), from its own
// configured userLevel (default 0). The level on the client alone
// therefore cannot retime the session: the exit must carry the level.
// Same for chain exits: VLESS/VMess/Trojan/SS outbound clients time
// from their account user's level, and the panel's chain outbounds
// carried no level. So the render must (a) give each idle inbound a
// dedicated freedom outbound with settings.userLevel + an inboundTag
// rule, and (b) stamp the level on chain outbound users.

func connIdleOutbounds(t *testing.T, parsed map[string]interface{}) []interface{} {
	t.Helper()
	obs, ok := parsed["outbounds"].([]interface{})
	if !ok {
		t.Fatalf("outbounds missing")
	}
	return obs
}

func connIdleOutbound(t *testing.T, parsed map[string]interface{}, tag string) map[string]interface{} {
	t.Helper()
	for _, o := range connIdleOutbounds(t, parsed) {
		if m, _ := o.(map[string]interface{}); m["tag"] == tag {
			return m
		}
	}
	return nil
}

func connIdleRules(t *testing.T, parsed map[string]interface{}) []interface{} {
	t.Helper()
	routing, ok := parsed["routing"].(map[string]interface{})
	if !ok {
		t.Fatalf("routing missing (no rules rendered?)")
	}
	rules, ok := routing["rules"].([]interface{})
	if !ok {
		t.Fatalf("routing.rules missing")
	}
	return rules
}

func connIdleUserLevel(t *testing.T, ob map[string]interface{}) float64 {
	t.Helper()
	if ob == nil {
		t.Fatal("outbound missing")
	}
	settings, _ := ob["settings"].(map[string]interface{})
	if settings == nil {
		t.Fatalf("outbound %v has no settings", ob["tag"])
	}
	lvl, ok := settings["userLevel"].(float64)
	if !ok {
		t.Fatalf("outbound %v settings.userLevel missing: %v", ob["tag"], settings)
	}
	return lvl
}

func TestConnIdleDirectOutbound(t *testing.T) {
	// Idle inbound, owner credential only: a dedicated freedom outbound
	// carrying the level, reached by an inboundTag rule; the shared
	// direct outbound must not gain a userLevel (level-0 sessions stay).
	in := Inbound{ID: 7, Protocol: ProtocolVLESS, Port: 443, Settings: Settings{UUID: testUUID}, Security: Security{Type: SecurityNone}, ConnIdleSeconds: 5}
	parsed := renderConnIdle(t, TypeXray, []Inbound{in})
	ob := connIdleOutbound(t, parsed, "flmx-idle-7")
	if ob == nil {
		t.Fatalf("dedicated idle outbound flmx-idle-7 missing: %v", parsed["outbounds"])
	}
	if ob["protocol"] != "freedom" {
		t.Fatalf("idle outbound protocol = %v, want freedom", ob["protocol"])
	}
	if got := connIdleUserLevel(t, ob); got != 1 {
		t.Fatalf("idle outbound userLevel = %v, want 1", got)
	}
	if d := connIdleOutbound(t, parsed, "direct"); d != nil {
		if s, _ := d["settings"].(map[string]interface{}); s != nil {
			if _, ok := s["userLevel"]; ok {
				t.Fatalf("shared direct outbound must not gain userLevel: %v", s)
			}
		}
	}
	rules := connIdleRules(t, parsed)
	found := false
	for _, r := range rules {
		m := r.(map[string]interface{})
		if m["outboundTag"] == "flmx-idle-7" {
			tags, _ := m["inboundTag"].([]interface{})
			if len(tags) == 1 && tags[0] == "in-7" && m["user"] == nil {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("no inboundTag rule routing in-7 to flmx-idle-7: %v", rules)
	}
}

func TestConnIdleMarkedUserKeepsMark(t *testing.T) {
	// A counted user on an idle inbound needs BOTH the level and the
	// socket mark: a combined outbound (userLevel + mark) reached by a
	// more specific inboundTag+user rule, ahead of both the plain idle
	// rule and the user's mark rule. The mark outbound itself must not
	// gain a userLevel — it is shared across the user's inbounds.
	in := Inbound{ID: 7, Protocol: ProtocolVLESS, Port: 443, Settings: Settings{UUID: testUUID}, Security: Security{Type: SecurityNone}, ConnIdleSeconds: 5,
		Users: []User{{ID: 3, UUID: testUUID}}}
	parsed := renderConnIdle(t, TypeXray, []Inbound{in})
	comb := connIdleOutbound(t, parsed, "flmx-idle-7-u3")
	if comb == nil {
		t.Fatalf("combined idle outbound flmx-idle-7-u3 missing: %v", parsed["outbounds"])
	}
	if got := connIdleUserLevel(t, comb); got != 1 {
		t.Fatalf("combined outbound userLevel = %v, want 1", got)
	}
	stream, _ := comb["streamSettings"].(map[string]interface{})
	sockopt, _ := stream["sockopt"].(map[string]interface{})
	if sockopt == nil || sockopt["mark"] != float64(UserMark(3)) {
		t.Fatalf("combined outbound mark = %v, want %d", sockopt, UserMark(3))
	}
	rules := connIdleRules(t, parsed)
	order := ruleOutbounds(rules, "outboundTag")
	if got := strings.Join(order, ","); got != "flmx-idle-7-u3,flmx-idle-7,flmx-u3" {
		t.Fatalf("rule order = %s, want flmx-idle-7-u3,flmx-idle-7,flmx-u3", got)
	}
	if m := connIdleOutbound(t, parsed, "flmx-u3"); m != nil {
		if s, _ := m["settings"].(map[string]interface{}); s != nil {
			if _, ok := s["userLevel"]; ok {
				t.Fatalf("shared mark outbound must not gain userLevel: %v", s)
			}
		}
	}
}

func TestConnIdleMultipleInboundsIsolation(t *testing.T) {
	// Two idle inbounds get distinct levels, outbounds and rules; the
	// unset inbound gets none of either and keeps the shared direct exit.
	a := Inbound{ID: 9, Protocol: ProtocolVLESS, Port: 8443, Settings: Settings{UUID: testUUID}, Security: Security{Type: SecurityNone}, ConnIdleSeconds: 600}
	b := Inbound{ID: 3, Protocol: ProtocolVMess, Port: 8444, Settings: Settings{UUID: testUUID}, Security: Security{Type: SecurityNone}, ConnIdleSeconds: 120}
	c := Inbound{ID: 5, Protocol: ProtocolTrojan, Port: 8445, Settings: Settings{Password: "secret123"}, Security: Security{Type: SecurityNone}}
	parsed := renderConnIdle(t, TypeXray, []Inbound{a, b, c})
	if got := connIdleUserLevel(t, connIdleOutbound(t, parsed, "flmx-idle-3")); got != 1 {
		t.Fatalf("flmx-idle-3 userLevel = %v, want 1", got)
	}
	if got := connIdleUserLevel(t, connIdleOutbound(t, parsed, "flmx-idle-9")); got != 2 {
		t.Fatalf("flmx-idle-9 userLevel = %v, want 2", got)
	}
	rules := connIdleRules(t, parsed)
	for _, r := range rules {
		m := r.(map[string]interface{})
		tags, _ := m["inboundTag"].([]interface{})
		for _, tag := range tags {
			if tag == "in-5" {
				t.Fatalf("unset inbound must not gain an idle rule: %v", m)
			}
		}
	}
	if got := strings.Join(ruleOutbounds(rules, "outboundTag"), ","); got != "flmx-idle-3,flmx-idle-9" {
		t.Fatalf("rule order = %s, want flmx-idle-3,flmx-idle-9", got)
	}
}

func TestConnIdleChainOutboundLevel(t *testing.T) {
	// Chained idle inbound: no direct idle rule (the chain rules match
	// first, so it would be dead) — instead the chain outbound's user
	// carries the level, because the chain client's idle timer reads
	// the account user's level (vless/outbound: ForLevel(request.User.Level)).
	target := Inbound{ID: 9, Protocol: ProtocolVLESS, Port: 8443,
		Settings:  Settings{UUID: "22222222-2222-4222-8222-222222222222"},
		Transport: Transport{Type: TransportTCP}, Security: Security{Type: SecurityNone}}
	src := Inbound{ID: 1, Protocol: ProtocolVLESS, Port: 1000, Settings: Settings{UUID: testUUID},
		Security: Security{Type: SecurityNone}, Users: chainUser, ConnIdleSeconds: 120,
		Via: &Endpoint{Host: "5.6.7.8", Port: 8443, Direct: true, Inbound: target}}
	parsed := renderConnIdle(t, TypeXray, []Inbound{src})
	if ob := connIdleOutbound(t, parsed, "flmx-idle-1"); ob != nil {
		t.Fatalf("chained inbound must not get a direct idle outbound: %v", ob)
	}
	chainUserLevel := func(tag string) float64 {
		t.Helper()
		ob := connIdleOutbound(t, parsed, tag)
		if ob == nil {
			t.Fatalf("chain outbound %s missing", tag)
		}
		settings, _ := ob["settings"].(map[string]interface{})
		vnext, _ := settings["vnext"].([]interface{})
		if len(vnext) == 0 {
			t.Fatalf("chain outbound %s has no vnext", tag)
		}
		users, _ := vnext[0].(map[string]interface{})["users"].([]interface{})
		if len(users) == 0 {
			t.Fatalf("chain outbound %s has no users", tag)
		}
		lvl, ok := users[0].(map[string]interface{})["level"].(float64)
		if !ok {
			t.Fatalf("chain outbound %s user has no level: %v", tag, users[0])
		}
		return lvl
	}
	if got := chainUserLevel("via-1"); got != 1 {
		t.Fatalf("via-1 user level = %v, want 1", got)
	}
	if got := chainUserLevel("via-1-u7"); got != 1 {
		t.Fatalf("via-1-u7 user level = %v, want 1", got)
	}
	rules := connIdleRules(t, parsed)
	if got := strings.Join(ruleOutbounds(rules, "outboundTag"), ","); got != "via-1-u7,via-1,flmx-u7" {
		t.Fatalf("rule order = %s, want via-1-u7,via-1,flmx-u7", got)
	}
}

func TestConnIdleSplitDirectUsesIdleOutbound(t *testing.T) {
	// Split-only idle inbound: split-direct matches must leave through
	// the idle outbounds (not the shared direct / mark outbounds), and
	// unmatched traffic still falls through to the plain idle rule.
	in := Inbound{ID: 1, Protocol: ProtocolTrojan, Port: 443, Settings: Settings{Password: "p"},
		Security: Security{Type: SecurityTLS, SNI: "a.example.com", CertPEM: "c", KeyPEM: "k"}, Users: chainUser, ConnIdleSeconds: 120,
		Split: []SplitRule{{Domains: []string{"example.com"}, IPs: []string{"1.2.3.0/24"}, Action: SplitDirect}}}
	parsed := renderConnIdle(t, TypeXray, []Inbound{in})
	rules := connIdleRules(t, parsed)
	got := strings.Join(ruleOutbounds(rules, "outboundTag"), ",")
	want := "flmx-idle-1-u7,flmx-idle-1,flmx-idle-1-u7,flmx-idle-1,flmx-idle-1-u7,flmx-idle-1,flmx-u7"
	if got != want {
		t.Fatalf("rule order = %s, want %s", got, want)
	}
}
