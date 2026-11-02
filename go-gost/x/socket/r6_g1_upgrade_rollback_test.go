package socket

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// L2-02 (round-6) regression gate: the upgrade restart script's
// AUTO-rollback leg was the last bare cp onto the binary path —
// the same mechanism FRM4 eliminated from the manual rollback
// script. It copied the backup straight over the just-upgraded
// binary, unchecked, without stopping the service: a copy dying
// midway truncated the new binary, a copy against the still-
// running new binary could fail ETXTBSY, and the following
// start + health check could still log 'rollback succeeded' for
// a rollback that never happened.

// runUpgradeScriptFlappingNewBinary runs a generated upgrade
// restart script with a stateful systemctl stub: the FIRST health
// check sees an unstable MainPID (the new binary flapping), so
// the script takes the auto-rollback leg; later health checks see
// a stable PID and pass. An optional extra stub directory (the
// failing-cp stub) is prepended to PATH.
func runUpgradeScriptFlappingNewBinary(t *testing.T, script string, extraStubDir string) {
	t.Helper()
	stubDir := t.TempDir()
	countFile := filepath.Join(stubDir, "show-count")
	systemctl := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\nshow) n=$(cat %s 2>/dev/null || echo 0); n=$((n+1)); echo $n > %s; if [ $n -eq 2 ]; then printf '9999\\n'; else printf '4242\\n'; fi ;;\nesac\nexit 0\n", countFile, countFile)
	writeFileForTest(t, filepath.Join(stubDir, "systemctl"), systemctl, 0755)
	writeFileForTest(t, filepath.Join(stubDir, "sleep"), "#!/bin/sh\nexit 0\n", 0755)
	path := stubDir + string(os.PathListSeparator) + os.Getenv("PATH")
	if extraStubDir != "" {
		path = extraStubDir + string(os.PathListSeparator) + path
	}
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Env = append(os.Environ(), "PATH="+path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("upgrade restart script run failed: %v\noutput: %s", err, out)
	}
}

// failingCPStubDir returns a directory whose cp writes partial
// content to its destination (its last argument) and then fails —
// the disk-full shape. Where the partial write lands is exactly
// what distinguishes the old morphology (the binary itself) from
// the staged one (the throwaway temp sibling).
func failingCPStubDir(t *testing.T) string {
	t.Helper()
	stubDir := t.TempDir()
	writeFileForTest(t, filepath.Join(stubDir, "cp"), "#!/bin/sh\ndest=\nfor a in \"$@\"; do dest=$a; done\nprintf 'PARTIAL-COPY-' > \"$dest\"\nexit 1\n", 0755)
	return stubDir
}

func TestUpgradeAutoRollbackMidCopyFailureKeepsNewBinary(t *testing.T) {
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "flux_agent")
	backupPath := binaryPath + ".old"
	stagedPath := filepath.Join(dir, ".flux_agent.upgrade-1")
	writeFileForTest(t, binaryPath, "OLD-BINARY", 0755)
	writeFileForTest(t, backupPath, "OLD-BINARY", 0755)
	writeFileForTest(t, stagedPath, "NEW-BINARY", 0755)

	script, err := buildAgentRestartScriptFor(serviceManagerSystemd, stagedPath, binaryPath, backupPath)
	if err != nil {
		t.Fatalf("buildAgentRestartScriptFor: %v", err)
	}
	runUpgradeScriptFlappingNewBinary(t, script, failingCPStubDir(t))

	// The staged upgrade landed, the new binary flapped, the
	// auto-rollback's copy died midway. On the pre-L2-02
	// morphology the partial write lands on the binary itself;
	// the staged chain must leave the new binary byte-for-byte
	// intact.
	if got := readFileForTest(t, binaryPath); got != "NEW-BINARY" {
		t.Fatalf("failed auto-rollback copy must not touch the new binary, content: %q", got)
	}
	if leftovers, err := filepath.Glob(filepath.Join(dir, ".flux_agent.upgrade-rollback-*")); err != nil || len(leftovers) != 0 {
		t.Fatalf("staged rollback temp file must be removed after a failed swap, leftovers: %v (glob err: %v)", leftovers, err)
	}
	log := readFileForTest(t, filepath.Join(dir, "agent-upgrade.log"))
	if strings.Contains(log, "rollback succeeded") {
		t.Fatalf("failed auto-rollback must not be logged as a successful rollback:\n%s", log)
	}
	if !strings.Contains(log, "agent upgrade rollback swap failed") {
		t.Fatalf("failed auto-rollback swap must be named in the upgrade log:\n%s", log)
	}
	if !strings.Contains(log, "agent upgrade rollback aborted: new version still running") {
		t.Fatalf("aborted auto-rollback on the intact new version must be recorded:\n%s", log)
	}
}

func TestUpgradeAutoRollbackStagedSwapSuccess(t *testing.T) {
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "flux_agent")
	backupPath := binaryPath + ".old"
	stagedPath := filepath.Join(dir, ".flux_agent.upgrade-1")
	writeFileForTest(t, binaryPath, "OLD-BINARY", 0755)
	writeFileForTest(t, backupPath, "OLD-BINARY", 0755)
	writeFileForTest(t, stagedPath, "NEW-BINARY", 0755)

	script, err := buildAgentRestartScriptFor(serviceManagerSystemd, stagedPath, binaryPath, backupPath)
	if err != nil {
		t.Fatalf("buildAgentRestartScriptFor: %v", err)
	}
	runUpgradeScriptFlappingNewBinary(t, script, "")

	if got := readFileForTest(t, binaryPath); got != "OLD-BINARY" {
		t.Fatalf("binary not restored from backup by the auto-rollback swap, content: %q", got)
	}
	if info, err := os.Stat(binaryPath); err != nil || info.Mode().Perm() != 0755 {
		t.Fatalf("swapped-in binary must be mode 0755, got %v (stat err: %v)", info.Mode().Perm(), err)
	}
	if leftovers, err := filepath.Glob(filepath.Join(dir, ".flux_agent.upgrade-rollback-*")); err != nil || len(leftovers) != 0 {
		t.Fatalf("staged rollback temp file must be consumed by the swap's mv, leftovers: %v (glob err: %v)", leftovers, err)
	}
	log := readFileForTest(t, filepath.Join(dir, "agent-upgrade.log"))
	if !strings.Contains(log, "rollback succeeded") {
		t.Fatalf("successful auto-rollback must log 'rollback succeeded':\n%s", log)
	}
	if strings.Contains(log, "agent upgrade rollback swap failed") {
		t.Fatalf("successful auto-rollback must not log a swap failure:\n%s", log)
	}
}

func TestUpgradeAutoRollbackAtomicStagingBothManagers(t *testing.T) {
	stops := map[serviceManager]string{
		serviceManagerSystemd: "systemctl stop flux_agent",
		serviceManagerOpenRC:  "rc-service flux_agent stop",
	}
	for _, manager := range []serviceManager{serviceManagerSystemd, serviceManagerOpenRC} {
		script, err := buildAgentRestartScriptFor(manager, "/etc/flux_agent/.flux_agent.upgrade-1", "/etc/flux_agent/flux_agent", "/etc/flux_agent/flux_agent.old")
		if err != nil {
			t.Fatalf("buildAgentRestartScriptFor %s: %v", manager, err)
		}
		for _, want := range []string{
			"rollback_tmp=$(mktemp /etc/flux_agent/.flux_agent.upgrade-rollback-XXXXXX)",
			"cp /etc/flux_agent/flux_agent.old \"$rollback_tmp\"",
			"chmod 755 \"$rollback_tmp\"",
			"cmp /etc/flux_agent/flux_agent.old \"$rollback_tmp\"",
			"mv \"$rollback_tmp\" /etc/flux_agent/flux_agent",
			"rm -f \"$rollback_tmp\"",
			"agent upgrade rollback swap failed",
			"agent upgrade rollback aborted",
		} {
			if !strings.Contains(script, want) {
				t.Fatalf("%s restart script missing %q:\n%s", manager, want, script)
			}
		}
		// No copy may write the binary path directly any more.
		if strings.Contains(script, "cp /etc/flux_agent/flux_agent.old /etc/flux_agent/flux_agent") {
			t.Fatalf("%s restart script still copies the backup straight onto the binary path:\n%s", manager, script)
		}
		// Inside the auto-rollback leg, the service stop must come
		// before the staged copy (a possibly still-running new
		// binary makes the swap target busy), and 'rollback
		// succeeded' is only reachable past the staged mv.
		legStart := strings.Index(script, "rolling back")
		stopInLeg := strings.Index(script[legStart:], stops[manager])
		chainInLeg := strings.Index(script[legStart:], "rollback_tmp=$(mktemp")
		if stopInLeg < 0 || chainInLeg < 0 || stopInLeg > chainInLeg {
			t.Fatalf("%s restart script: auto-rollback leg must stop the service before the staged copy:\n%s", manager, script)
		}
		if mvIdx, okIdx := strings.Index(script, "mv \"$rollback_tmp\" /etc/flux_agent/flux_agent"), strings.Index(script, "rollback succeeded"); mvIdx < 0 || okIdx < mvIdx {
			t.Fatalf("%s restart script: 'rollback succeeded' must sit past the staged mv:\n%s", manager, script)
		}
	}
}
