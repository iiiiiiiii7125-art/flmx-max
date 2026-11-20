package nftables

import (
	"context"
	"strings"
	"testing"
)

// R18-C3-05: a hostname target passes ValidateTargetHost and its
// DNAT renders and forwards traffic, but the forward chain skips
// the rule entirely (family == ""), so the traffic is never
// metered. ValidatePlan must reject hostname targets fail-closed,
// in line with the intake (validateNftablesForwardRequest already
// only accepts IP targets) — one rule for both gates, no silent
// zero-metering. RenderTable keeps its skip as a last-resort guard
// and now logs it with the forward ID instead of dropping the
// counting silently.
func TestValidatePlanRejectsHostnameTarget(t *testing.T) {
	plan := NodePlan{
		NodeID: 7,
		Rules: []Rule{
			{ForwardID: 42, InPort: 24000, TargetHost: "example.com", TargetPort: 443, Protocols: []string{"tcp"}},
		},
	}
	err := ValidatePlan(plan)
	if err == nil {
		t.Fatal("plan with a hostname target must be rejected")
	}
	if !strings.Contains(err.Error(), "42") {
		t.Fatalf("rejection must name the offending forward ID, got %q", err)
	}
	if !strings.Contains(err.Error(), "example.com") {
		t.Fatalf("rejection must name the hostname target, got %q", err)
	}
	good := NodePlan{
		NodeID: 7,
		Rules: []Rule{
			{ForwardID: 42, InPort: 24000, TargetHost: "198.51.100.20", TargetPort: 443, Protocols: []string{"tcp"}},
			{ForwardID: 43, InPort: 24001, TargetHost: "2001:db8::20", TargetPort: 443, Protocols: []string{"tcp", "udp"}},
		},
	}
	if err := ValidatePlan(good); err != nil {
		t.Fatalf("IP target plan must pass: %v", err)
	}
}

func TestManagerReconcileRejectsHostnameTargetWithoutApplying(t *testing.T) {
	runner := &fakeRunner{}
	manager := NewManager(runner)
	plan := NodePlan{
		NodeID: 7,
		Rules:  []Rule{{ForwardID: 42, InPort: 24000, TargetHost: "example.com", TargetPort: 443, Protocols: []string{"tcp"}}},
	}
	if _, err := manager.Reconcile(context.Background(), SSHConfig{}, plan); err == nil {
		t.Fatal("Reconcile must reject a hostname target plan")
	} else if !strings.Contains(err.Error(), "42") {
		t.Fatalf("Reconcile rejection must name the forward, got %q", err)
	}
	if len(runner.scripts) != 0 {
		t.Fatalf("rejected plan must never reach the node: %d scripts applied", len(runner.scripts))
	}
}
