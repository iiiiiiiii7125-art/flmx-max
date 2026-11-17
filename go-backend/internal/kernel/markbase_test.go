package kernel

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// E-01: the per-agent socket-mark base is derived from the node's
// secret, not the node id — two panels' node 1 must mint different
// bases, or their nftables tables count each other's traffic into
// the same subscription.

func TestUserMarkBaseForAgent(t *testing.T) {
	// Same node id, different secrets (two panels' node 1): the
	// E-01 property — different bases.
	a := UserMarkBaseForAgent(1, "0123456789abcdef0123456789abcdef")
	b := UserMarkBaseForAgent(1, "fedcba9876543210fedcba9876543210")
	if a == b {
		t.Fatalf("same node id on different panels must get different bases: %x vs %x", a, b)
	}
	// Pinned literals (SHA-256("flmx:user-mark-v2:"+secret), first byte).
	if a != 0x47170000 {
		t.Fatalf("secret A base: got 0x%x, want 0x47170000", a)
	}
	if b != 0x477a0000 {
		t.Fatalf("secret B base: got 0x%x, want 0x477a0000", b)
	}
	// Determinism: same (node, secret) mints the same base.
	if UserMarkBaseForAgent(1, "0123456789abcdef0123456789abcdef") != a {
		t.Fatal("same node and secret must get the same base")
	}
	// The node id no longer moves the base: the secret is the identity.
	if UserMarkBaseForAgent(75, "0123456789abcdef0123456789abcdef") != a {
		t.Fatal("same secret must mint the same base regardless of node id")
	}
	// Layout: top byte 0x47, agent byte in bits 16-23, low 16 bits 0.
	if a&0xffff != 0 || a>>24 != 0x47 {
		t.Fatalf("base layout: got 0x%x", a)
	}
	// Empty secret falls back to the v1 derivation (node id low
	// byte) — defensive only, production rows always carry a secret.
	if got := UserMarkBaseForAgent(75, ""); got != 0x474B0000 {
		t.Fatalf("empty secret must fall back to v1: got 0x%x, want 0x474B0000", got)
	}
	if UserMarkBaseForAgent(256, "") != UserMarkBaseForAgent(0, "") {
		t.Fatal("empty-secret fallback: node ids 256 apart must share a base (low byte only)")
	}
}

// F-R4-01 / E-01: the per-agent base must never equal the legacy base.
// The 0x47 tag is disjoint from legacy by construction, for every
// agent byte — prove it over the whole byte space (v1 fallback) and
// for a set of v2 secrets.
func TestUserMarkBaseForAgentNeverEqualsLegacy(t *testing.T) {
	for b := int64(0); b < 256; b++ {
		if got := UserMarkBaseForAgent(b, ""); got == LegacyUserMarkBase {
			t.Fatalf("empty-secret fallback, node id low byte %d mints the legacy base 0x%x", b, got)
		}
	}
	for _, secret := range []string{
		"0123456789abcdef0123456789abcdef",
		"fedcba9876543210fedcba9876543210",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"11111111111111111111111111111111",
	} {
		if got := UserMarkBaseForAgent(1, secret); got == LegacyUserMarkBase {
			t.Fatalf("secret %q mints the legacy base 0x%x", secret, got)
		}
	}
	if got := UserMarkBaseForAgent(86, ""); got != 0x47560000 {
		t.Fatalf("node 86 fallback base: got 0x%x, want 0x47560000", got)
	}
}

func TestUserMarkForAgent(t *testing.T) {
	if got := UserMarkForAgent(0x474B0000, 7); got != 0x474B0007 {
		t.Fatalf("per-agent mark: got 0x%x, want 0x474B0007", got)
	}
	// Base 0 means legacy.
	if got := UserMarkForAgent(0, 7); got != UserMark(7) || got != 0x46560007 {
		t.Fatalf("base 0 must mean legacy: got 0x%x", got)
	}
	// Id bounds are base-independent.
	if UserMarkForAgent(0x474B0000, 0) != 0 || UserMarkForAgent(0x474B0000, 0x10000) != 0 {
		t.Fatal("out-of-range ids must mint no mark")
	}
	if UserMarkForAgent(0x474B0000, 0xffff) != 0x474Bffff {
		t.Fatal("max id must fit")
	}
}

func TestUserFromMarkBothLayouts(t *testing.T) {
	if got := UserFromMark(0x46560007); got != 7 {
		t.Fatalf("legacy mark must decode: got %d", got)
	}
	if got := UserFromMark(0x474B0007); got != 7 {
		t.Fatalf("per-agent mark must decode: got %d", got)
	}
	if got := UserFromMark(0x47010007); got != 7 {
		t.Fatalf("another agent byte must decode: got %d", got)
	}
	// The pre-fix 0x46 per-agent tag still decodes (transition).
	if got := UserFromMark(0x464B0007); got != 7 {
		t.Fatalf("pre-fix per-agent mark must still decode: got %d", got)
	}
	for _, m := range []int64{0x12345678, 0x48000007, 0x4656_0000_0007, 0} {
		if got := UserFromMark(m); got != 0 {
			t.Fatalf("foreign mark 0x%x must decode to 0, got %d", m, got)
		}
	}
}

func renderJSONWithOpts(t *testing.T, kernelType string, inbounds []Inbound, opts RenderOptions) (map[string]interface{}, string) {
	t.Helper()
	r, err := RenderWith(kernelType, inbounds, opts)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal([]byte(r.Files[0].Content), &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg, r.Files[0].Content
}

func markOfSingboxOutbound(t *testing.T, cfg map[string]interface{}, tag string) float64 {
	t.Helper()
	ob := outboundByTag(cfg, tag)
	if ob == nil {
		t.Fatalf("outbound %s missing", tag)
	}
	m, ok := ob["routing_mark"].(float64)
	if !ok {
		t.Fatalf("outbound %s has no routing_mark: %v", tag, ob)
	}
	return m
}

func markOfXrayOutbound(t *testing.T, cfg map[string]interface{}, tag string) float64 {
	t.Helper()
	ob := outboundByTag(cfg, tag)
	if ob == nil {
		t.Fatalf("outbound %s missing", tag)
	}
	ss, ok := ob["streamSettings"].(map[string]interface{})
	if !ok {
		t.Fatalf("outbound %s has no streamSettings: %v", tag, ob)
	}
	so, ok := ss["sockopt"].(map[string]interface{})
	if !ok {
		t.Fatalf("outbound %s has no sockopt: %v", tag, ob)
	}
	m, ok := so["mark"].(float64)
	if !ok {
		t.Fatalf("outbound %s has no sockopt.mark: %v", tag, ob)
	}
	return m
}

func TestRenderPerAgentUserMark(t *testing.T) {
	base := UserMarkBaseForAgent(75, "0123456789abcdef0123456789abcdef") // 0x47170000
	want := float64(0x47170007)
	legacy := fmt.Sprint(0x46560007)

	via := Endpoint{Host: "1.2.3.4", Port: 443, Direct: true, Proxy: Proxy{
		"type": "trojan", "server": "1.2.3.4", "port": 443, "password": "tpw", "sni": "t.example.com"}}
	chained := Inbound{ID: 1, Protocol: ProtocolVLESS, Port: 443, Settings: Settings{UUID: "00000000-0000-4000-8000-000000000000"},
		Security: Security{Type: SecurityNone}, Users: chainUser, Via: &via}

	// sing-box: direct + chained + split outbounds all mint the
	// per-agent mark, and the legacy mark appears nowhere.
	// (chained and split are rendered separately: both fixtures use
	// inbound ID 1, so their tags would collide in one config.)
	cfg, raw := renderJSONWithOpts(t, TypeSingbox, []Inbound{chained}, RenderOptions{UserMarkBase: base})
	if got := markOfSingboxOutbound(t, cfg, "flmx-u7"); got != want {
		t.Fatalf("sing-box direct user outbound mark: got %v, want %v", got, want)
	}
	if got := markOfSingboxOutbound(t, cfg, "via-1-u7"); got != want {
		t.Fatalf("sing-box chained user outbound mark: got %v, want %v", got, want)
	}
	if strings.Contains(raw, legacy) {
		t.Fatalf("sing-box config must not contain the legacy mark %s", legacy)
	}
	cfg, raw = renderJSONWithOpts(t, TypeSingbox, []Inbound{splitInbound()}, RenderOptions{UserMarkBase: base})
	if got := markOfSingboxOutbound(t, cfg, "flmx-u7"); got != want {
		t.Fatalf("sing-box direct user outbound mark (split): got %v, want %v", got, want)
	}
	if got := markOfSingboxOutbound(t, cfg, "via-1-s1-u7"); got != want {
		t.Fatalf("sing-box split user outbound mark: got %v, want %v", got, want)
	}
	if strings.Contains(raw, legacy) {
		t.Fatalf("sing-box split config must not contain the legacy mark %s", legacy)
	}

	// xray: same, through sockopt.mark.
	cfg, raw = renderJSONWithOpts(t, TypeXray, []Inbound{chained}, RenderOptions{UserMarkBase: base})
	if got := markOfXrayOutbound(t, cfg, "flmx-u7"); got != want {
		t.Fatalf("xray direct user outbound mark: got %v, want %v", got, want)
	}
	if got := markOfXrayOutbound(t, cfg, "via-1-u7"); got != want {
		t.Fatalf("xray chained user outbound mark: got %v, want %v", got, want)
	}
	if strings.Contains(raw, legacy) {
		t.Fatalf("xray config must not contain the legacy mark %s", legacy)
	}
	cfg, raw = renderJSONWithOpts(t, TypeXray, []Inbound{splitInbound()}, RenderOptions{UserMarkBase: base})
	if got := markOfXrayOutbound(t, cfg, "flmx-u7"); got != want {
		t.Fatalf("xray direct user outbound mark (split): got %v, want %v", got, want)
	}
	if got := markOfXrayOutbound(t, cfg, "via-1-s1-u7"); got != want {
		t.Fatalf("xray split user outbound mark: got %v, want %v", got, want)
	}
	if strings.Contains(raw, legacy) {
		t.Fatalf("xray split config must not contain the legacy mark %s", legacy)
	}

	// No base (legacy Render path) still mints the legacy mark —
	// single-agent behaviour is unchanged.
	cfg = renderJSON(t, TypeSingbox, []Inbound{chained})
	if got := markOfSingboxOutbound(t, cfg, "flmx-u7"); got != float64(0x46560007) {
		t.Fatalf("legacy render must mint the legacy mark: got %v", got)
	}
}
