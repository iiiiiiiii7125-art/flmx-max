package handler

import (
	"strings"
	"testing"
)

// R18-C3-04: the intake must reject a hostname BindIP (inIp) before
// the forward lands in the DB — the renderer silently drops a
// non-IP BindIP, so the forward would listen on all addresses
// while the admin believes it is bound to one.
func TestValidateNftablesForwardRequestRejectsHostnameBindIP(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	tunnelID := seedTunnelForNftables(t, h, "nft-tunnel-hostbind", fixture.nodeID)
	tunnel, err := h.getTunnelRecord(tunnelID)
	if err != nil {
		t.Fatalf("load tunnel: %v", err)
	}

	rejected := []string{"node.example.com", "node.example.com:8080"}
	for _, bindIP := range rejected {
		err := h.validateNftablesForwardRequest(tunnel, "198.51.100.20:443", []int64{fixture.nodeID}, bindIP)
		if err == nil {
			t.Fatalf("BindIP %q must be rejected", bindIP)
		}
		if !strings.Contains(err.Error(), bindIP) {
			t.Fatalf("hostname BindIP rejection must name the submitted value, got %q", err)
		}
	}

	// 对照：IP 字面量与无 BindIP 仍放行。
	allowed := []string{"", "192.0.2.10", "2001:db8::10", "[2001:db8::10]:8080"}
	for _, bindIP := range allowed {
		target := "198.51.100.20:443"
		if strings.Contains(bindIP, "db8") {
			target = "[2001:db8::20]:443"
		}
		if err := h.validateNftablesForwardRequest(tunnel, target, []int64{fixture.nodeID}, bindIP); err != nil {
			t.Fatalf("BindIP %q + target %q must pass: %v", bindIP, target, err)
		}
	}
}
