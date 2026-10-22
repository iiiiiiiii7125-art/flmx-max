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
	if got := strings.Count(restart, "systemctl reset-failed flux_agent"); got != 2 {
		t.Fatalf("upgrade restart script: want reset-failed before both starts, got %d occurrences:\n%s", got, restart)
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
