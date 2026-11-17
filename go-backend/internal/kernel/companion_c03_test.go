package kernel

import (
	"encoding/json"
	"strings"
	"testing"
)

// C-03: the metering companion is a managed second sing-box instance.
// Lookup must resolve it (payload building needs the sing-box catalog
// entry), the install catalog must NOT list it, EffectiveType must fold
// it onto sing-box for every render-side type comparison, and rendering
// it must be byte-identical to rendering the same inbounds on sing-box.
func TestCompanionLookupResolvesSingboxClone(t *testing.T) {
	info, ok := Lookup(TypeSingboxCompanion)
	if !ok {
		t.Fatal("Lookup(singbox-companion) must resolve")
	}
	if info.Type != TypeSingboxCompanion {
		t.Fatalf("Lookup type = %q, want %q", info.Type, TypeSingboxCompanion)
	}
	if !info.Implemented {
		t.Fatal("companion must be Implemented (payload building needs it)")
	}
	if info.Relay {
		t.Fatal("companion must not be Relay (must not route to the realm payload)")
	}
	if !ComboSupported(TypeSingboxCompanion, ProtocolShadowsocks, TransportTCP, SecurityNone) {
		t.Fatal("companion must support the shadowsocks/TCP/none combo like sing-box")
	}
	if got := CanonicalKernelType(TypeSingboxCompanion); got != TypeSingboxCompanion {
		t.Fatalf("CanonicalKernelType(companion) = %q, want %q", got, TypeSingboxCompanion)
	}
	// Hyphen/case variants fold the same way.
	for _, in := range []string{"singbox-companion", "Singbox-Companion", " singbox-companion "} {
		if info, ok := Lookup(in); !ok || info.Type != TypeSingboxCompanion {
			t.Fatalf("Lookup(%q) = %+v %v, want companion", in, info, ok)
		}
	}
}

func TestCompanionNotInCatalog(t *testing.T) {
	for _, info := range Catalog() {
		if info.Type == TypeSingboxCompanion {
			t.Fatal("Catalog must not list the companion: it is managed, not installable")
		}
	}
}

func TestCompanionEffectiveType(t *testing.T) {
	if got := EffectiveType(TypeSingboxCompanion); got != TypeSingbox {
		t.Fatalf("EffectiveType(companion) = %q, want %q", got, TypeSingbox)
	}
	if got := EffectiveType(TypeSingbox); got != TypeSingbox {
		t.Fatalf("EffectiveType(singbox) = %q, want %q", got, TypeSingbox)
	}
	if got := EffectiveType(TypeXray); got != TypeXray {
		t.Fatalf("EffectiveType(xray) = %q, want %q", got, TypeXray)
	}
	// The outbound-IP policy gate is the sharp edge: a non-auto
	// strategy on the raw companion type must not be rejected.
	if _, err := NormalizeOutboundIPStrategy(EffectiveType(TypeSingboxCompanion), OutboundIPv4Only); err != nil {
		t.Fatalf("non-auto outbound-IP strategy on the companion render type: %v", err)
	}
	if _, err := NormalizeOutboundIPStrategy(TypeSingboxCompanion, OutboundIPv4Only); err == nil {
		t.Fatal("raw companion type must still be rejected by the policy gate (callers must normalize)")
	}
}

func TestCompanionRendersLikeSingbox(t *testing.T) {
	in := Inbound{ID: 1, Protocol: ProtocolVLESS, Port: 443, Settings: Settings{UUID: testUUID}, Security: Security{Type: SecurityNone}}
	main, err := Render(TypeSingbox, []Inbound{in})
	if err != nil {
		t.Fatalf("render sing-box: %v", err)
	}
	// R2C-01: the companion's Clash API port is per-agent (passed in
	// RenderOptions); the render must carry exactly that port.
	comp, err := RenderWith(TypeSingboxCompanion, []Inbound{in}, RenderOptions{ClashAPIPort: 41234})
	if err != nil {
		t.Fatalf("render companion: %v", err)
	}
	if len(comp.Files) != 1 || comp.Files[0].Name != "config.json" {
		t.Fatalf("companion files = %+v, want one config.json", comp.Files)
	}
	// C-(4): the companion differs from the main sing-box config in
	// exactly one place — the Clash API (selective per-user kill).
	// Everything else renders identically.
	var mainCfg, compCfg map[string]interface{}
	if err := json.Unmarshal([]byte(main.Files[0].Content), &mainCfg); err != nil {
		t.Fatalf("parse sing-box config: %v", err)
	}
	if err := json.Unmarshal([]byte(comp.Files[0].Content), &compCfg); err != nil {
		t.Fatalf("parse companion config: %v", err)
	}
	if _, ok := mainCfg["experimental"]; ok {
		t.Fatalf("sing-box config unexpectedly has experimental: %v", mainCfg["experimental"])
	}
	exp, ok := compCfg["experimental"].(map[string]interface{})
	if !ok {
		t.Fatalf("companion config has no experimental section")
	}
	clash, ok := exp["clash_api"].(map[string]interface{})
	if !ok || clash["external_controller"] != "127.0.0.1:41234" {
		t.Fatalf("companion clash_api = %v, want external_controller %q", exp["clash_api"], "127.0.0.1:41234")
	}
	delete(exp, "clash_api")
	if len(exp) == 0 {
		delete(compCfg, "experimental")
	}
	mainJSON, _ := json.Marshal(mainCfg)
	compJSON, _ := json.Marshal(compCfg)
	if string(mainJSON) != string(compJSON) {
		t.Fatalf("companion config differs from sing-box config beyond the Clash API:\ncompanion: %s\nsing-box: %s", compJSON, mainJSON)
	}
	if strings.Join(comp.Args, " ") != "run -c {RUN_DIR}/config.json" {
		t.Fatalf("companion args = %v, want sing-box run args", comp.Args)
	}
}
