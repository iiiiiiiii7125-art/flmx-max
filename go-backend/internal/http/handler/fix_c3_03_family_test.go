package handler

import (
	"strings"
	"testing"
)

// R18-C3-03: the intake must reject a BindIP/target pair of
// different address families (v6 inIp + v4 target, or the reverse)
// before the forward lands in the DB — otherwise the mixed rule
// renders into a ruleset nft refuses, and the whole node's sync
// freezes on every attempt.
func TestValidateNftablesForwardRequestRejectsMixedFamily(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	tunnelID := seedTunnelForNftables(t, h, "nft-tunnel-family", fixture.nodeID)
	tunnel, err := h.getTunnelRecord(tunnelID)
	if err != nil {
		t.Fatalf("load tunnel: %v", err)
	}

	mixed := []struct{ bindIP, target string }{
		{"2001:db8::10", "198.51.100.20:443"},
		{"[2001:db8::10]", "198.51.100.20:443"},
		{"192.0.2.10", "[2001:db8::20]:443"},
	}
	for _, tc := range mixed {
		err := h.validateNftablesForwardRequest(tunnel, tc.target, []int64{fixture.nodeID}, tc.bindIP)
		if err == nil {
			t.Fatalf("BindIP %q + target %q must be rejected", tc.bindIP, tc.target)
		}
		if !strings.Contains(err.Error(), "地址族") {
			t.Fatalf("mixed-family rejection must name the family cause, got %q", err)
		}
	}

	// 对照：同族与无 BindIP 仍放行。
	allowed := []struct{ bindIP, target string }{
		{"", "198.51.100.20:443"},
		{"", "[2001:db8::20]:443"},
		{"192.0.2.10", "198.51.100.20:443"},
		{"2001:db8::10", "[2001:db8::20]:443"},
	}
	for _, tc := range allowed {
		if err := h.validateNftablesForwardRequest(tunnel, tc.target, []int64{fixture.nodeID}, tc.bindIP); err != nil {
			t.Fatalf("BindIP %q + target %q must pass: %v", tc.bindIP, tc.target, err)
		}
	}
}

// R18-C3-03: effectiveNftBindIP — an explicitly submitted inIp
// wins; otherwise the stored per-port inIp is used, so editing
// only the target cannot slip a mixed family past the intake.
func TestEffectiveNftBindIP(t *testing.T) {
	req := map[string]interface{}{"inIp": " 2001:db8::10 "}
	if got := effectiveNftBindIP(req, nil); got != "2001:db8::10" {
		t.Fatalf("submitted inIp must win, got %q", got)
	}
	req = map[string]interface{}{}
	ports := []forwardPortRecord{{NodeID: 1, Port: 24000, InIP: ""}, {NodeID: 1, Port: 24001, InIP: "192.0.2.10"}}
	if got := effectiveNftBindIP(req, ports); got != "192.0.2.10" {
		t.Fatalf("stored inIp must be used when nothing is submitted, got %q", got)
	}
	if got := effectiveNftBindIP(req, nil); got != "" {
		t.Fatalf("no submitted or stored inIp must yield empty, got %q", got)
	}
}
