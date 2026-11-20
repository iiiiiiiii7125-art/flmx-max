package nftables

import (
	"context"
	"strings"
	"testing"
)

// R18-C3-04: a hostname BindIP (inIp) is silently dropped by the
// renderer — bindIPFamilyIP returns ok=false for non-IP values and
// the DNAT rule renders with no daddr restriction, byte-identical
// to "no BindIP at all". A forward the admin believed was bound to
// one address actually listens on all addresses (fail-open). Both
// gates must reject a non-IP BindIP fail-closed: ValidatePlan at
// render time, validateNftablesForwardRequest at intake.
func TestValidateBindIP(t *testing.T) {
	rejected := []string{
		"node.example.com",
		"node.example.com:8080",
		"example.com",
		"[node.example.com]",
		"fe80::1%eth0",
	}
	for _, bindIP := range rejected {
		if err := ValidateBindIP(bindIP); err == nil {
			t.Fatalf("BindIP %q must be rejected", bindIP)
		} else if !strings.Contains(err.Error(), bindIP) {
			t.Fatalf("BindIP %q rejection must name the submitted value, got %q", bindIP, err)
		}
	}
	allowed := []string{
		"",
		"0.0.0.0",
		"::",
		"192.0.2.10",
		"2001:db8::10",
		"[2001:db8::10]",
		"192.0.2.10:8080",
		"[2001:db8::10]:8080",
	}
	for _, bindIP := range allowed {
		if err := ValidateBindIP(bindIP); err != nil {
			t.Fatalf("BindIP %q must pass: %v", bindIP, err)
		}
	}
}

func TestValidatePlanRejectsHostnameBindIP(t *testing.T) {
	plan := NodePlan{
		NodeID: 7,
		Rules: []Rule{
			{ForwardID: 42, InPort: 24000, BindIP: "node.example.com", TargetHost: "198.51.100.20", TargetPort: 443, Protocols: []string{"tcp"}},
		},
	}
	err := ValidatePlan(plan)
	if err == nil {
		t.Fatal("plan with a hostname BindIP must be rejected")
	}
	if !strings.Contains(err.Error(), "42") {
		t.Fatalf("rejection must name the offending forward ID, got %q", err)
	}
	good := NodePlan{
		NodeID: 7,
		Rules: []Rule{
			{ForwardID: 42, InPort: 24000, BindIP: "192.0.2.10", TargetHost: "198.51.100.20", TargetPort: 443, Protocols: []string{"tcp"}},
			{ForwardID: 43, InPort: 24001, TargetHost: "198.51.100.20", TargetPort: 443, Protocols: []string{"tcp", "udp"}},
		},
	}
	if err := ValidatePlan(good); err != nil {
		t.Fatalf("IP BindIP plan must pass: %v", err)
	}
}

func TestManagerReconcileRejectsHostnameBindIPWithoutApplying(t *testing.T) {
	runner := &fakeRunner{}
	manager := NewManager(runner)
	plan := NodePlan{
		NodeID: 7,
		Rules:  []Rule{{ForwardID: 42, InPort: 24000, BindIP: "node.example.com", TargetHost: "198.51.100.20", TargetPort: 443, Protocols: []string{"tcp"}}},
	}
	if _, err := manager.Reconcile(context.Background(), SSHConfig{}, plan); err == nil {
		t.Fatal("Reconcile must reject a hostname BindIP plan")
	} else if !strings.Contains(err.Error(), "42") {
		t.Fatalf("Reconcile rejection must name the forward, got %q", err)
	}
	if len(runner.scripts) != 0 {
		t.Fatalf("rejected plan must never reach the node: %d scripts applied", len(runner.scripts))
	}
}
