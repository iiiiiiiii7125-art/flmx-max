package kernel

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// F2-DBL-01: per-agent socket-mark base — two agents on one host must
// mint different marks for the same subscription, so the same physical
// packets are counted once, not once per agent.

func TestUserMarkBaseForAgent(t *testing.T) {
	// Node ids are sequential; co-located agents (a handful per
	// host) get distinct bases.
	a, b := UserMarkBaseForAgent(75), UserMarkBaseForAgent(76)
	if a == b {
		t.Fatalf("distinct nodes must get distinct bases: %x vs %x", a, b)
	}
	if UserMarkBaseForAgent(75) != a {
		t.Fatal("same node must get the same base")
	}
	// Layout: top byte 0x47, agent byte in bits 16-23, low 16 bits 0.
	if a != 0x474B0000 {
		t.Fatalf("node 75 base: got 0x%x, want 0x474B0000", a)
	}
	if UserMarkBaseForAgent(0) != 0x47000000 {
		t.Fatalf("node 0 base: got 0x%x", UserMarkBaseForAgent(0))
	}
	// Documented collision boundary: the agent byte is the node id's
	// low byte, so ids 256 apart share a base. That needs two
	// co-located nodes with ids exactly 256 apart — and even then
	// only those two double-count (the old behaviour), never the
	// whole host.
	if UserMarkBaseForAgent(256) != UserMarkBaseForAgent(0) {
		t.Fatal("node ids 256 apart must share a base (low byte only)")
	}
}

// F-R4-01: the per-agent base must never equal the legacy base. The old
// formula 0x46000000 | (nodeID&0xff)<<16 minted 0x46560000 for node ids
// ≡ 86 (mod 256) — exactly LegacyUserMarkBase — so a new agent on such
// a node double-counted with every old agent on the host during the
// transition. The per-agent tag is 0x47, disjoint from legacy by
// construction; prove it over the whole byte space, not just 86.
func TestUserMarkBaseForAgentNeverEqualsLegacy(t *testing.T) {
	for b := int64(0); b < 256; b++ {
		if got := UserMarkBaseForAgent(b); got == LegacyUserMarkBase {
			t.Fatalf("node id low byte %d mints the legacy base 0x%x", b, got)
		}
	}
	if got := UserMarkBaseForAgent(86); got != 0x47560000 {
		t.Fatalf("node 86 base: got 0x%x, want 0x47560000", got)
	}
	if got := UserMarkBaseForAgent(86 + 256); got != 0x47560000 {
		t.Fatalf("node 342 base: got 0x%x, want 0x47560000", got)
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
	base := UserMarkBaseForAgent(75) // 0x474B0000
	want := float64(0x474B0007)
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
