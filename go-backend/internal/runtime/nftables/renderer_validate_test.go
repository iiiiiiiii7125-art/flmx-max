package nftables

import (
	"context"
	"strings"
	"testing"
)

// C3-01 regression: Rule.TargetHost is the one free-text value RenderTable
// interpolates into the nft script. The renderer layer must reject
// anything that is neither an IP literal nor a strict hostname before the
// text reaches `nft -f` on the node — production constructors gate on IPs
// today, but the render/apply layer is the single point every current and
// future constructor flows through.

func TestValidateTargetHostAcceptsIPsAndStrictHostnames(t *testing.T) {
	valid := []string{
		"192.0.2.1",
		"10.0.0.1",
		"::1",
		"[2001:db8::1]",
		"example.com",
		"a-b.example-1.com",
		"host",
		"  198.51.100.20  ",
	}
	for _, host := range valid {
		if err := ValidateTargetHost(host); err != nil {
			t.Fatalf("expected %q to be accepted, got %v", host, err)
		}
	}
}

func TestValidateTargetHostRejectsInjectionPayloads(t *testing.T) {
	invalid := []string{
		"",
		"   ",
		"10.0.0.1\n } add rule inet flmx prerouting drop",
		"10.0.0.1; drop",
		"10.0.0.1 }",
		"exa mple.com",
		"exam\tple.com",
		"example.com#comment",
		`example".com`,
		"example'.com",
		"a..b",
		".example.com",
		"example.com.",
		"-bad.com",
		"bad-.com",
		"exa_mple.com",
		strings.Repeat("a", 64) + ".com",
		"2001:db8::1::2",
		"[::1]]extra",
	}
	for _, host := range invalid {
		if err := ValidateTargetHost(host); err == nil {
			t.Fatalf("expected %q to be rejected", host)
		}
	}
}

func TestValidatePlanRejectsRuleWithBadTarget(t *testing.T) {
	plan := NodePlan{
		NodeID: 7,
		Rules: []Rule{
			{ForwardID: 1, InPort: 24000, TargetHost: "198.51.100.20", TargetPort: 443, Protocols: []string{"tcp"}},
			{ForwardID: 2, InPort: 24001, TargetHost: "10.0.0.1\n } malicious", TargetPort: 443, Protocols: []string{"tcp"}},
		},
	}
	err := ValidatePlan(plan)
	if err == nil {
		t.Fatalf("expected plan validation to reject the injected target")
	}
	if !strings.Contains(err.Error(), "2") {
		t.Fatalf("error must name the offending forward, got %q", err)
	}
}

func TestManagerReconcileRejectsBadTargetBeforeReachingNode(t *testing.T) {
	runner := &fakeRunner{}
	manager := NewManager(runner)
	plan := NodePlan{
		NodeID: 7,
		Rules:  []Rule{{ForwardID: 42, InPort: 24000, TargetHost: "10.0.0.1; add chain", TargetPort: 443, Protocols: []string{"tcp"}}},
	}
	if _, err := manager.Reconcile(context.Background(), SSHConfig{Host: "203.0.113.10", Port: 22, Username: "root"}, plan); err == nil {
		t.Fatalf("expected Reconcile to reject an invalid target host")
	}
	if len(runner.scripts) != 0 {
		t.Fatalf("no script may be sent to the node for an invalid plan, got %d", len(runner.scripts))
	}
}
