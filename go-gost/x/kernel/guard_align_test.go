package kernel

import (
	"encoding/json"
	"strings"
	"testing"
)

// C3-01: the whitelist rendering key and the guard predicate key must
// be the same predicate. portsHaveWhitelist keys on len(Allow) > 0, so
// an empty-but-present allow list ("allow": []) is NOT a whitelist:
// it must render no guard, exactly like an absent allow field. Before
// the alignment expandGuards keyed its skip on Allow == nil, so the
// empty form rendered an empty allow set plus drop rules for the port
// (drop-all) while the kernel was not classified as protected — the
// sync failure paths that keep protected kernels down never covered
// it.

func TestExpandGuardsEmptyAllowSameAsAbsent(t *testing.T) {
	absent := []Port{{InboundID: 7, Port: 443, Network: "tcp"}}
	empty := []Port{{InboundID: 7, Port: 443, Network: "tcp", Allow: []string{}}}
	if _, ok := expandGuards(absent)[7]; ok {
		t.Fatal("absent allow must not produce a guard entry")
	}
	if _, ok := expandGuards(empty)[7]; ok {
		t.Fatal("empty allow must not produce a guard entry (same as absent)")
	}
	seeded := []Port{{InboundID: 7, Port: 443, Network: "tcp", Allow: []string{"127.0.0.0/8", "::1/128"}}}
	g, ok := expandGuards(seeded)[7]
	if !ok {
		t.Fatal("seeded allow (panel loopback seed set) must produce a guard entry")
	}
	if len(g.v4) != 1 || g.v4[0] != "127.0.0.0/8" || len(g.v6) != 1 || g.v6[0] != "::1/128" {
		t.Fatalf("seed set must split by family unchanged, got %+v", g)
	}
}

func TestEmptyAllowClassificationMatchesRendering(t *testing.T) {
	shapes := map[string][]Port{
		"absent": {{InboundID: 7, Port: 443, Network: "tcp"}},
		"empty":  {{InboundID: 7, Port: 443, Network: "tcp", Allow: []string{}}},
	}
	for name, ports := range shapes {
		if portsHaveWhitelist(ports) {
			t.Fatalf("%s: must not be classified as whitelisted", name)
		}
		req := ApplyRequest{Type: "xray", Enabled: true, Ports: ports}
		if reqNeedsNftGuard(req) {
			t.Fatalf("%s: must not be classified as protected", name)
		}
		script := nftScript(expandPorts(ports), expandGuards(ports), nil, nil)
		if strings.Contains(script, "allow4_7") || strings.Contains(script, "allow6_7") ||
			strings.Contains(script, "drop") {
			t.Fatalf("%s: script must carry no guard for the port:\n%s", name, script)
		}
	}
	// The two shapes must render byte-identical scripts.
	a := nftScript(expandPorts(shapes["absent"]), expandGuards(shapes["absent"]), nil, nil)
	b := nftScript(expandPorts(shapes["empty"]), expandGuards(shapes["empty"]), nil, nil)
	if a != b {
		t.Fatalf("absent and empty allow must render identical scripts:\n--- absent ---\n%s\n--- empty ---\n%s", a, b)
	}
}

func TestEmptyAllowWireFormSameAsAbsent(t *testing.T) {
	var absent, empty Port
	if err := json.Unmarshal([]byte(`{"inboundId":7,"port":443,"network":"tcp"}`), &absent); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"inboundId":7,"port":443,"network":"tcp","allow":[]}`), &empty); err != nil {
		t.Fatal(err)
	}
	if empty.Allow == nil {
		t.Fatal("test premise broken: \"allow\":[] must decode to a non-nil empty slice")
	}
	pa, pe := []Port{absent}, []Port{empty}
	if portsHaveWhitelist(pa) != portsHaveWhitelist(pe) {
		t.Fatal("wire forms must classify identically")
	}
	sa := nftScript(expandPorts(pa), expandGuards(pa), nil, nil)
	se := nftScript(expandPorts(pe), expandGuards(pe), nil, nil)
	if sa != se {
		t.Fatalf("wire forms must render identically:\n--- absent ---\n%s\n--- empty ---\n%s", sa, se)
	}
}

// TestGuardScriptSeedSetUnchanged pins the current panel payload
// shape: the panel always sends the loopback seed set for a
// whitelisted inbound, and its rendering (seeded allow sets plus
// drop rules) must not move under the alignment.
func TestGuardScriptSeedSetUnchanged(t *testing.T) {
	ports := []Port{{InboundID: 7, Port: 443, Network: "tcp", Allow: []string{"127.0.0.0/8", "::1/128", "203.0.113.0/24"}}}
	if !portsHaveWhitelist(ports) {
		t.Fatal("seeded allow must be classified as whitelisted")
	}
	script := nftScript(expandPorts(ports), expandGuards(ports), nil, nil)
	for _, want := range []string{
		"set allow4_7", "set allow6_7",
		"127.0.0.0/8", "203.0.113.0/24", "::1/128",
		"ip saddr != @allow4_7 drop", "ip6 saddr != @allow6_7 drop",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("seeded script missing %q:\n%s", want, script)
		}
	}
}
