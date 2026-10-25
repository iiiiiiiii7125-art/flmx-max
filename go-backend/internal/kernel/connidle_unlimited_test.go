package kernel

import (
	"strings"
	"testing"
)

// connIdle "不限": ConnIdleSeconds = -1 is the unlimited sentinel.
// -1 renders as UnlimitedConnIdleSeconds (31536000 = 365d) in
// policy.levels[].connIdle; the Xray (N, 2N] window then closes a
// session only after 1–2 years idle, which is "never" for any real
// deployment (panel upgrades / node restarts end sessions sooner).
// 0 stays "unset" (Xray default 300s); stock values are untouched.

func TestConnIdleUnlimitedNormalize(t *testing.T) {
	base := func() Inbound {
		return Inbound{ID: 1, Protocol: ProtocolVLESS, Port: 443, Settings: Settings{UUID: testUUID}, Security: Security{Type: SecurityNone}}
	}
	// -1 is accepted on Xray only.
	in := base()
	in.ConnIdleSeconds = UnlimitedConnIdleSentinel
	if err := Normalize(TypeXray, &in); err != nil {
		t.Fatalf("connIdle -1 (unlimited) must be accepted on xray: %v", err)
	}
	// One below the sentinel and one above the cap are rejected, and
	// the error must name the field.
	for _, v := range []int{UnlimitedConnIdleSentinel - 1, MaxConnIdleSeconds + 1} {
		in := base()
		in.ConnIdleSeconds = v
		if err := Normalize(TypeXray, &in); err == nil {
			t.Fatalf("connIdle %d must be rejected", v)
		} else if !strings.Contains(err.Error(), "空闲超时") {
			t.Fatalf("connIdle %d error must name the field: %v", v, err)
		}
	}
	// Non-Xray kernels refuse -1 exactly like any other set value.
	snell := Inbound{ID: 1, Protocol: ProtocolSnell, Port: 1234, Settings: Settings{Password: "secret-password-1"}, Security: Security{Type: SecurityNone}, ConnIdleSeconds: UnlimitedConnIdleSentinel}
	if err := Normalize(TypeSnell, &snell); err == nil {
		t.Fatal("connIdle -1 (unlimited) on snell must be rejected")
	} else if !strings.Contains(err.Error(), "空闲超时") {
		t.Fatalf("connIdle -1 on snell error must name the field: %v", err)
	}
	{
		in := base()
		in.ConnIdleSeconds = UnlimitedConnIdleSentinel
		if err := Normalize(TypeSingbox, &in); err == nil {
			t.Fatal("connIdle -1 (unlimited) on sing-box must be rejected")
		} else if !strings.Contains(err.Error(), "空闲超时") {
			t.Fatalf("connIdle -1 on sing-box error must name the field: %v", err)
		}
	}
}

func TestConnIdleUnlimitedRendersOneYear(t *testing.T) {
	in := Inbound{ID: 7, Protocol: ProtocolVLESS, Port: 443, Settings: Settings{UUID: testUUID}, Security: Security{Type: SecurityNone}, ConnIdleSeconds: UnlimitedConnIdleSentinel,
		Users: []User{{ID: 3, UUID: testUUID}}}
	parsed := renderConnIdle(t, TypeXray, []Inbound{in})
	levels := policyLevels(t, parsed)
	l1, ok := levels["1"].(map[string]interface{})
	if !ok {
		t.Fatalf("level 1 missing: %v", levels)
	}
	if l1["connIdle"] != float64(UnlimitedConnIdleSeconds) {
		t.Fatalf("level 1 connIdle = %v, want %d (unlimited renders as one year)", l1["connIdle"], UnlimitedConnIdleSeconds)
	}
	// The unlimited inbound still gets its dedicated idle outbound and
	// rule — the execution chain is unchanged, only the value differs.
	if ob := connIdleOutbound(t, parsed, "flmx-idle-7"); ob == nil {
		t.Fatalf("dedicated idle outbound flmx-idle-7 missing for unlimited inbound: %v", parsed["outbounds"])
	} else if got := connIdleUserLevel(t, ob); got != 1 {
		t.Fatalf("idle outbound userLevel = %v, want 1", got)
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

func TestConnIdleUnlimitedSharesLevels(t *testing.T) {
	// An unlimited inbound and a normal one share the level allocator:
	// ID order decides, the sentinel is just another set value.
	a := Inbound{ID: 9, Protocol: ProtocolVLESS, Port: 8443, Settings: Settings{UUID: testUUID}, Security: Security{Type: SecurityNone}, ConnIdleSeconds: UnlimitedConnIdleSentinel}
	b := Inbound{ID: 3, Protocol: ProtocolVMess, Port: 8444, Settings: Settings{UUID: testUUID}, Security: Security{Type: SecurityNone}, ConnIdleSeconds: 120}
	parsed := renderConnIdle(t, TypeXray, []Inbound{a, b})
	levels := policyLevels(t, parsed)
	if len(levels) != 2 {
		t.Fatalf("levels = %d, want 2", len(levels))
	}
	if levels["1"].(map[string]interface{})["connIdle"] != float64(120) {
		t.Fatalf("level 1 (inbound 3) connIdle = %v, want 120", levels["1"])
	}
	if levels["2"].(map[string]interface{})["connIdle"] != float64(UnlimitedConnIdleSeconds) {
		t.Fatalf("level 2 (inbound 9) connIdle = %v, want %d", levels["2"], UnlimitedConnIdleSeconds)
	}
}
