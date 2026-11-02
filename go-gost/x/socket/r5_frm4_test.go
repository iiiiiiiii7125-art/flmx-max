package socket

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// FRM4 regression gate: FRM3 guarded the rollback copy, but the
// copy still wrote straight onto the binary path — a copy dying
// midway (disk full) truncated the CURRENT binary, so the failure
// branch could name the failure yet the node was already bricked.
// The swap now stages through a unique temp sibling, verifies the
// staged copy byte-identical to the backup (cmp), and only then
// mv's it into place, mirroring the upgrade path's atomic shape.

// runRollbackScriptWithFailingCP runs a generated rollback script
// with the service manager stubbed as in runRestartScriptForTest,
// plus a cp stub that simulates a copy dying midway: it writes
// partial content to the destination (its last argument) and then
// fails, the disk-full shape. Where the partial write lands is
// exactly what distinguishes the old morphology (the binary
// itself) from the staged one (the throwaway temp sibling).
func runRollbackScriptWithFailingCP(t *testing.T, script string) {
	t.Helper()
	stubDir := t.TempDir()
	writeFileForTest(t, filepath.Join(stubDir, "systemctl"), "#!/bin/sh\ncase \"$1\" in\nshow) printf '4242\\n' ;;\nesac\nexit 0\n", 0755)
	writeFileForTest(t, filepath.Join(stubDir, "sleep"), "#!/bin/sh\nexit 0\n", 0755)
	writeFileForTest(t, filepath.Join(stubDir, "cp"), "#!/bin/sh\ndest=\nfor a in \"$@\"; do dest=$a; done\nprintf 'PARTIAL-COPY-' > \"$dest\"\nexit 1\n", 0755)
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Env = append(os.Environ(), "PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("rollback script run failed: %v\noutput: %s", err, out)
	}
}

func TestAgentRollbackScriptMidCopyFailureLeavesBinaryIntact(t *testing.T) {
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "flux_agent")
	backupPath := binaryPath + ".old"
	const current = "CURRENT-BINARY-INTACT-AND-COMPLETE"
	writeFileForTest(t, binaryPath, current, 0755)
	writeFileForTest(t, backupPath, "BACKUP-BINARY", 0755)

	script, err := buildAgentRollbackScriptFor(serviceManagerSystemd, backupPath, binaryPath)
	if err != nil {
		t.Fatalf("buildAgentRollbackScriptFor: %v", err)
	}
	runRollbackScriptWithFailingCP(t, script)

	// The core FRM4 assertion: a copy dying midway must leave the
	// current binary byte-for-byte intact. On the FRM3 morphology
	// (cp straight onto the binary path) the stub's partial write
	// lands on the binary itself and this fails.
	if got := readFileForTest(t, binaryPath); got != current {
		t.Fatalf("mid-copy failure must not touch the current binary, content: %q", got)
	}
	// The staged temp file is owned by the script and must be
	// cleaned up on the failure path.
	if leftovers, err := filepath.Glob(filepath.Join(dir, ".flux_agent.rollback-*")); err != nil || len(leftovers) != 0 {
		t.Fatalf("staged temp file must be removed after a failed swap, leftovers: %v (glob err: %v)", leftovers, err)
	}
	log := readFileForTest(t, filepath.Join(dir, "agent-upgrade.log"))
	if strings.Contains(log, "rollback restart ok") {
		t.Fatalf("mid-copy failure must not be logged as a successful rollback:\n%s", log)
	}
	if !strings.Contains(log, "agent rollback swap failed") {
		t.Fatalf("mid-copy failure must be named in the upgrade log:\n%s", log)
	}
	if !strings.Contains(log, "agent rollback aborted: current version still running") {
		t.Fatalf("aborted rollback on the intact current version must be recorded:\n%s", log)
	}
}

func TestAgentRollbackScriptStagedSwapSuccess(t *testing.T) {
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
		t.Fatalf("binary not restored from backup by staged rollback swap, content: %q", got)
	}
	// mktemp stages 0600 and cp onto an existing file keeps that
	// mode; the script's chmod must restore executability before
	// the mv, or the swapped-in binary could not run.
	if info, err := os.Stat(binaryPath); err != nil || info.Mode().Perm() != 0755 {
		t.Fatalf("swapped-in binary must be mode 0755, got %v (stat err: %v)", info.Mode().Perm(), err)
	}
	if leftovers, err := filepath.Glob(filepath.Join(dir, ".flux_agent.rollback-*")); err != nil || len(leftovers) != 0 {
		t.Fatalf("staged temp file must be consumed by the swap's mv, leftovers: %v (glob err: %v)", leftovers, err)
	}
	log := readFileForTest(t, filepath.Join(dir, "agent-upgrade.log"))
	if !strings.Contains(log, "rollback restart ok") {
		t.Fatalf("successful rollback must log 'rollback restart ok':\n%s", log)
	}
	if strings.Contains(log, "agent rollback swap failed") {
		t.Fatalf("successful rollback must not log a swap failure:\n%s", log)
	}
}

func TestAgentRollbackScriptAtomicStagingBothManagers(t *testing.T) {
	for _, manager := range []serviceManager{serviceManagerSystemd, serviceManagerOpenRC} {
		script, err := buildAgentRollbackScriptFor(manager, "/etc/flux_agent/flux_agent.old", "/etc/flux_agent/flux_agent")
		if err != nil {
			t.Fatalf("buildAgentRollbackScriptFor %s: %v", manager, err)
		}
		for _, want := range []string{
			"rollback_tmp=$(mktemp /etc/flux_agent/.flux_agent.rollback-XXXXXX)",
			"cp /etc/flux_agent/flux_agent.old \"$rollback_tmp\"",
			"chmod 755 \"$rollback_tmp\"",
			"cmp /etc/flux_agent/flux_agent.old \"$rollback_tmp\"",
			"mv \"$rollback_tmp\" /etc/flux_agent/flux_agent",
			"rm -f \"$rollback_tmp\"",
		} {
			if !strings.Contains(script, want) {
				t.Fatalf("%s rollback script missing %q:\n%s", manager, want, script)
			}
		}
		// No copy may write the binary path directly any more.
		if strings.Contains(script, "cp /etc/flux_agent/flux_agent.old /etc/flux_agent/flux_agent") {
			t.Fatalf("%s rollback script still copies straight onto the binary path:\n%s", manager, script)
		}
		// 'rollback restart ok' is only reachable past the atomic mv.
		if mvIdx, okIdx := strings.Index(script, "mv \"$rollback_tmp\" /etc/flux_agent/flux_agent"), strings.Index(script, "rollback restart ok"); mvIdx < 0 || okIdx < mvIdx {
			t.Fatalf("%s rollback script: 'rollback restart ok' must sit past the staged mv:\n%s", manager, script)
		}
	}
}
