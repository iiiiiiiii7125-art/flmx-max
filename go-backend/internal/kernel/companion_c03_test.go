package kernel

import (
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
	comp, err := Render(TypeSingboxCompanion, []Inbound{in})
	if err != nil {
		t.Fatalf("render companion: %v", err)
	}
	if len(comp.Files) != 1 || comp.Files[0].Name != "config.json" {
		t.Fatalf("companion files = %+v, want one config.json", comp.Files)
	}
	if comp.Files[0].Content != main.Files[0].Content {
		t.Fatalf("companion config differs from sing-box config:\ncompanion: %s\nsing-box: %s", comp.Files[0].Content, main.Files[0].Content)
	}
	if comp.Hash != main.Hash {
		t.Fatalf("companion hash %q != sing-box hash %q", comp.Hash, main.Hash)
	}
	if strings.Join(comp.Args, " ") != "run -c {RUN_DIR}/config.json" {
		t.Fatalf("companion args = %v, want sing-box run args", comp.Args)
	}
}
