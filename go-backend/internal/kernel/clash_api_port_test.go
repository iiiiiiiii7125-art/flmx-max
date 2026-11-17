package kernel

import (
	"encoding/json"
	"fmt"
	"testing"
)

// R2C-01: the companion's Clash API port is derived per agent from the
// node secret — no fixed port, no shared default.
func TestClashAPIPortForSecret(t *testing.T) {
	if got := ClashAPIPortForSecret(""); got != 0 {
		t.Fatalf("ClashAPIPortForSecret(\"\") = %d, want 0 (not configured)", got)
	}
	// Deterministic: same secret always mints the same port.
	if a, b := ClashAPIPortForSecret("node-secret-1"), ClashAPIPortForSecret("node-secret-1"); a != b {
		t.Fatalf("ClashAPIPortForSecret not deterministic: %d != %d", a, b)
	}
	// Range: every secret maps into 41000-41999; 40900 is never
	// produced (it is outside the range by construction).
	for i := 0; i < 1000; i++ {
		p := ClashAPIPortForSecret(fmt.Sprintf("r2c01-secret-%04d", i))
		if p < companionClashAPIPortMin || p > companionClashAPIPortMax {
			t.Fatalf("ClashAPIPortForSecret(%q) = %d, want in [%d, %d]", fmt.Sprintf("r2c01-secret-%04d", i), p, companionClashAPIPortMin, companionClashAPIPortMax)
		}
		if p == 40900 {
			t.Fatalf("ClashAPIPortForSecret produced the old fixed port 40900")
		}
	}
	// Different secrets usually mint different ports (spot check, not
	// a strict distinctness assert: 1/1000 pairwise collisions are
	// possible by construction and handled by the agent fallback).
	if a, b := ClashAPIPortForSecret("panel-a-node-1"), ClashAPIPortForSecret("panel-b-node-1"); a == b {
		t.Fatalf("ClashAPIPortForSecret: two panels' node 1 minted the same port %d", a)
	}
}

// R2C-01: port 0 (node unreadable) omits the Clash API block —
// fail-closed, the agent falls back to reload for removals.
func TestCompanionOmitsClashAPIWhenPortZero(t *testing.T) {
	in := Inbound{ID: 1, Protocol: ProtocolVLESS, Port: 443, Settings: Settings{UUID: testUUID}, Security: Security{Type: SecurityNone}}
	main, err := Render(TypeSingbox, []Inbound{in})
	if err != nil {
		t.Fatalf("render sing-box: %v", err)
	}
	comp, err := Render(TypeSingboxCompanion, []Inbound{in})
	if err != nil {
		t.Fatalf("render companion: %v", err)
	}
	var compCfg map[string]interface{}
	if err := json.Unmarshal([]byte(comp.Files[0].Content), &compCfg); err != nil {
		t.Fatalf("parse companion config: %v", err)
	}
	if _, ok := compCfg["experimental"]; ok {
		t.Fatalf("companion config with port 0 unexpectedly has experimental: %v", compCfg["experimental"])
	}
	if main.Files[0].Content != comp.Files[0].Content {
		t.Fatalf("companion config with port 0 differs from sing-box config:\ncompanion: %s\nsing-box: %s", comp.Files[0].Content, main.Files[0].Content)
	}
}
