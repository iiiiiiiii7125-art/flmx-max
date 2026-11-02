package socket

import (
	"strings"
	"testing"
)

// Release-gate re-verification of R4-N2: a flapping new binary pushes
// the systemd unit into the start-rate-limited failed state, and the
// rollback's bare "systemctl start" was refused ("Start request
// repeated too quickly"), leaving the node down despite a successful
// binary restore. Every scripted start in the upgrade/rollback scripts
// must clear the failed state first under systemd; OpenRC has no such
// limiter and must stay untouched.
func TestScriptedStartsResetFailedUnderSystemd(t *testing.T) {
	restart, err := buildAgentRestartScriptFor(serviceManagerSystemd, "/tmp/agent.new", "/etc/flux_agent/flux_agent", "/etc/flux_agent/flux_agent.old")
	if err != nil {
		t.Fatalf("buildAgentRestartScriptFor: %v", err)
	}
	// The invariant is per-start, not a frozen count: every scripted
	// start must be emitted by scriptStartCommand as the adjacent
	// pair "reset-failed …; systemctl start …". F-RM-01 added a
	// third legitimate start (restarting the previous version after
	// a failed swap), so assert pairing and adjacency for all of
	// them instead of the historical count of two.
	starts := strings.Count(restart, "systemctl start flux_agent")
	resets := strings.Count(restart, "systemctl reset-failed flux_agent")
	if starts < 2 {
		t.Fatalf("upgrade restart script: want at least the upgrade and rollback starts, got %d:\n%s", starts, restart)
	}
	if resets != starts {
		t.Fatalf("upgrade restart script: want reset-failed before every start, got %d resets for %d starts:\n%s", resets, starts, restart)
	}
	if paired := strings.Count(restart, "systemctl reset-failed flux_agent 2>/dev/null; systemctl start flux_agent"); paired != starts {
		t.Fatalf("upgrade restart script: every start must immediately follow its reset-failed, got %d paired of %d starts:\n%s", paired, starts, restart)
	}

	rollback, err := buildAgentRollbackScriptFor(serviceManagerSystemd, "/etc/flux_agent/flux_agent.old", "/etc/flux_agent/flux_agent")
	if err != nil {
		t.Fatalf("buildAgentRollbackScriptFor: %v", err)
	}
	if !strings.Contains(rollback, "systemctl reset-failed flux_agent") {
		t.Fatalf("manual rollback script missing reset-failed:\n%s", rollback)
	}

	restartRC, err := buildAgentRestartScriptFor(serviceManagerOpenRC, "/tmp/agent.new", "/etc/flux_agent/flux_agent", "/etc/flux_agent/flux_agent.old")
	if err != nil {
		t.Fatalf("buildAgentRestartScriptFor openrc: %v", err)
	}
	if strings.Contains(restartRC, "reset-failed") {
		t.Fatalf("openrc script must not reference systemctl reset-failed:\n%s", restartRC)
	}
}
