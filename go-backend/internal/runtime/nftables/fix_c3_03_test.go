package nftables

import (
	"context"
	"strings"
	"testing"
)

// R18-C3-03: a BindIP and a target of different address families
// (v6 inIp + v4 target, or the reverse) render into a ruleset nft
// rejects with `conflicting protocols`, and the intake had no
// check at all — one such forward froze the whole node's sync.
// Both gates must reject mixed families fail-closed: ValidatePlan
// at render time, validateNftablesForwardRequest at intake.

func TestValidateBindIPTargetFamily(t *testing.T) {
	mixed := []struct{ bindIP, target string }{
		{"2001:db8::10", "198.51.100.20"},
		{"[2001:db8::10]", "198.51.100.20"},
		{"[2001:db8::10]:8080", "198.51.100.20"},
		{"192.0.2.10", "2001:db8::20"},
		{"192.0.2.10:8080", "[2001:db8::20]"},
	}
	for _, tc := range mixed {
		err := ValidateBindIPTargetFamily(tc.bindIP, tc.target)
		if err == nil {
			t.Fatalf("BindIP %q + target %q must be rejected", tc.bindIP, tc.target)
		}
		// The message names the normalized (bracket/port-stripped)
		// BindIP and target, not the raw submitted forms.
		_, bindNorm, _ := bindIPFamilyIP(tc.bindIP)
		if !strings.Contains(err.Error(), bindNorm) {
			t.Fatalf("rejection must name the BindIP %q, got %q", bindNorm, err)
		}
		if !strings.Contains(err.Error(), strings.Trim(tc.target, "[]")) {
			t.Fatalf("rejection must name the target, got %q", err)
		}
	}
	allowed := []struct{ bindIP, target string }{
		{"", "198.51.100.20"},
		{"", "2001:db8::20"},
		{"192.0.2.10", "198.51.100.20"},
		{"2001:db8::10", "2001:db8::20"},
		{"[2001:db8::10]", "[2001:db8::20]"},
		// Hostname BindIP has no parseable family (R18-C3-04, out of
		// scope here): the family gate cannot judge it, so it skips.
		{"node.example.com", "198.51.100.20"},
		// Hostname target has no family either: same skip.
		{"192.0.2.10", "example.com"},
		{"", "example.com"},
	}
	for _, tc := range allowed {
		if err := ValidateBindIPTargetFamily(tc.bindIP, tc.target); err != nil {
			t.Fatalf("BindIP %q + target %q must pass: %v", tc.bindIP, tc.target, err)
		}
	}
}

func TestValidatePlanRejectsMixedAddressFamily(t *testing.T) {
	plan := NodePlan{
		NodeID: 7,
		Rules: []Rule{
			{ForwardID: 42, InPort: 24000, BindIP: "2001:db8::10", TargetHost: "198.51.100.20", TargetPort: 443, Protocols: []string{"tcp"}},
			{ForwardID: 43, InPort: 24001, BindIP: "192.0.2.10", TargetHost: "2001:db8::20", TargetPort: 443, Protocols: []string{"tcp"}},
		},
	}
	err := ValidatePlan(plan)
	if err == nil {
		t.Fatal("plan with mixed-family rules must be rejected")
	}
	if !strings.Contains(err.Error(), "42") {
		t.Fatalf("rejection must name the offending forward ID, got %q", err)
	}
	// One bad rule is enough: the first offending forward is named.
	good := NodePlan{
		NodeID: 7,
		Rules: []Rule{
			{ForwardID: 42, InPort: 24000, BindIP: "2001:db8::10", TargetHost: "2001:db8::20", TargetPort: 443, Protocols: []string{"tcp"}},
			{ForwardID: 43, InPort: 24001, TargetHost: "198.51.100.20", TargetPort: 443, Protocols: []string{"tcp", "udp"}},
		},
	}
	if err := ValidatePlan(good); err != nil {
		t.Fatalf("matching-family plan must pass: %v", err)
	}
}

func TestManagerReconcileRejectsMixedFamilyWithoutApplying(t *testing.T) {
	runner := &fakeRunner{}
	manager := NewManager(runner)
	plan := NodePlan{
		NodeID: 7,
		Rules:  []Rule{{ForwardID: 42, InPort: 24000, BindIP: "2001:db8::10", TargetHost: "198.51.100.20", TargetPort: 443, Protocols: []string{"tcp"}}},
	}
	if _, err := manager.Reconcile(context.Background(), SSHConfig{}, plan); err == nil {
		t.Fatal("Reconcile must reject a mixed-family plan")
	} else if !strings.Contains(err.Error(), "42") {
		t.Fatalf("Reconcile rejection must name the forward, got %q", err)
	}
	if len(runner.scripts) != 0 {
		t.Fatalf("rejected plan must never reach the node: %d scripts applied", len(runner.scripts))
	}
}
