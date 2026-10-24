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
