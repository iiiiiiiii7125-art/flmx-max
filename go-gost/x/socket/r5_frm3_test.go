package socket

import (
	"path/filepath"
	"strings"
	"testing"
)

// FRM3 regression gate: the manual rollback script's cp was
// unchecked — the same false-success shape FRM1 fixed on the
// upgrade path. handleRollbackAgent stats the backup before
// launching, but the script copies seconds later (after sleep +
// service stop); a backup that vanished or became unreadable in
// between left the script starting the CURRENT binary, passing the
// health check and logging 'rollback restart ok' for a rollback
// that never happened.

func TestAgentRollbackScriptReportsSwapFailure(t *testing.T) {
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "flux_agent")
	backupPath := binaryPath + ".old"
	writeFileForTest(t, binaryPath, "CURRENT-BINARY", 0755)
	// The backup is gone by the time the script's cp runs (deleted
	// after the handler's existence check): the copy cannot happen,
	// and the script must say so instead of logging a successful
	// rollback on the current binary.
	script, err := buildAgentRollbackScriptFor(serviceManagerSystemd, backupPath, binaryPath)
	if err != nil {
		t.Fatalf("buildAgentRollbackScriptFor: %v", err)
	}
	runRestartScriptForTest(t, script)

	if got := readFileForTest(t, binaryPath); got != "CURRENT-BINARY" {
		t.Fatalf("binary must stay on the current version when the rollback copy fails, content: %q", got)
	}
	log := readFileForTest(t, filepath.Join(dir, "agent-upgrade.log"))
	if strings.Contains(log, "rollback restart ok") {
		t.Fatalf("copy failure must not be logged as a successful rollback:\n%s", log)
	}
	if !strings.Contains(log, "agent rollback swap failed") {
		t.Fatalf("copy failure must be named in the upgrade log:\n%s", log)
	}
	if !strings.Contains(log, "agent rollback aborted") {
		t.Fatalf("aborted rollback must be recorded in the upgrade log:\n%s", log)
	}
}

func TestAgentRollbackScriptSwapSuccess(t *testing.T) {
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "flux_agent")
	backupPath := binaryPath + ".old"
	writeFileForTest(t, binaryPath, "CURRENT-BINARY", 0755)
	writeFileForTest(t, backupPath, "BACKUP-BINARY", 0755)

	script, err := buildAgentRollbackScriptFor(serviceManagerSystemd, backupPath, binaryPath)
	if err != nil {
		t.Fatalf("buildAgentRollbackScriptFor: %v", err)
	}
	runRestartScriptForTest(t, script)

	if got := readFileForTest(t, binaryPath); got != "BACKUP-BINARY" {
		t.Fatalf("binary not restored from backup by rollback script, content: %q", got)
	}
	log := readFileForTest(t, filepath.Join(dir, "agent-upgrade.log"))
	if !strings.Contains(log, "rollback restart ok") {
		t.Fatalf("successful rollback must log 'rollback restart ok':\n%s", log)
	}
	if strings.Contains(log, "agent rollback swap failed") {
		t.Fatalf("successful rollback must not log a swap failure:\n%s", log)
	}
}

func TestAgentRollbackScriptSwapGuardBothManagers(t *testing.T) {
	for _, manager := range []serviceManager{serviceManagerSystemd, serviceManagerOpenRC} {
		script, err := buildAgentRollbackScriptFor(manager, "/etc/flux_agent/flux_agent.old", "/etc/flux_agent/flux_agent")
		if err != nil {
			t.Fatalf("buildAgentRollbackScriptFor %s: %v", manager, err)
		}
		for _, want := range []string{
			// FRM4: the guarded copy is staged atomically — mktemp
			// sibling, verified copy, mv into place.
			"rollback_tmp=$(mktemp /etc/flux_agent/.flux_agent.rollback-XXXXXX)",
			"cp /etc/flux_agent/flux_agent.old \"$rollback_tmp\"",
			"cmp /etc/flux_agent/flux_agent.old \"$rollback_tmp\"",
			"mv \"$rollback_tmp\" /etc/flux_agent/flux_agent",
			"agent rollback swap failed",
			"agent rollback aborted",
		} {
			if !strings.Contains(script, want) {
				t.Fatalf("%s rollback script missing %q:\n%s", manager, want, script)
			}
		}
		// 'rollback restart ok' is only reachable past the staged
		// swap guard.
		if guard, ok := strings.Index(script, "if rollback_tmp=$(mktemp"), strings.Index(script, "rollback restart ok"); guard < 0 || ok < guard {
			t.Fatalf("%s rollback script: 'rollback restart ok' must sit inside the staged swap guard:\n%s", manager, script)
		}
	}
}
