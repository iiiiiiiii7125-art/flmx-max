package socket

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// F-RM-01 regression gate: the staged upgrade binary must outlive
// handleUpgradeAgent's return, because the detached restart script
// only mv's it into place seconds later (sleep + service stop).
// FB's unconditional defer os.Remove deleted it at return time and
// every self-upgrade silently kept the old binary.

func writeFileForTest(t *testing.T, path, content string, perm os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readFileForTest(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// runRestartScriptForTest executes a generated restart script with
// the service manager stubbed out: systemctl reports a stable PID
// and active state, sleep is a no-op so the script's sequencing runs
// at test speed. The swap, cleanup and health decisions under test
// are the script's real ones.
func runRestartScriptForTest(t *testing.T, script string) {
	t.Helper()
	stubDir := t.TempDir()
	writeFileForTest(t, filepath.Join(stubDir, "systemctl"), "#!/bin/sh\ncase \"$1\" in\nshow) printf '4242\\n' ;;\nesac\nexit 0\n", 0755)
	writeFileForTest(t, filepath.Join(stubDir, "sleep"), "#!/bin/sh\nexit 0\n", 0755)
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Env = append(os.Environ(), "PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("restart script run failed: %v\noutput: %s", err, out)
	}
}

func TestUpgradeStagingHandoffKeepsFileAcrossHandlerReturn(t *testing.T) {
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "flux_agent")
	backupPath := binaryPath + ".old"
	writeFileForTest(t, binaryPath, "OLD-BINARY", 0755)
	writeFileForTest(t, backupPath, "OLD-BINARY", 0755)

	// Handler phase, in handleUpgradeAgent's shape: stage, fill,
	// hand off to the (about to be launched) restart script, return —
	// the deferred Close runs at return.
	var stagedPath string
	func() {
		staging, err := newUpgradeStaging(dir)
		if err != nil {
			t.Fatalf("newUpgradeStaging: %v", err)
		}
		defer staging.Close()
		stagedPath = staging.Path()
		writeFileForTest(t, stagedPath, "NEW-BINARY", 0755)
		staging.Handoff()
	}()

	// The F-RM-01 assertion: after the handler returns, the staged
	// binary must still be there for the script's delayed mv. With
	// the old unconditional-defer morphology this stat fails.
	if got := readFileForTest(t, stagedPath); got != "NEW-BINARY" {
		t.Fatalf("staged binary missing or altered after handler return: %q", got)
	}

	// Script phase: run the real generated restart script; it stops
	// the (stubbed) service, swaps the binary in and health-checks.
	script, err := buildAgentRestartScriptFor(serviceManagerSystemd, stagedPath, binaryPath, backupPath)
	if err != nil {
		t.Fatalf("buildAgentRestartScriptFor: %v", err)
	}
	runRestartScriptForTest(t, script)

	if got := readFileForTest(t, binaryPath); got != "NEW-BINARY" {
		t.Fatalf("binary not swapped in by restart script, content: %q", got)
	}
	if _, err := os.Stat(stagedPath); !os.IsNotExist(err) {
		t.Fatalf("staging file should be consumed by the script's mv, stat err: %v", err)
	}
	log := readFileForTest(t, filepath.Join(dir, "agent-upgrade.log"))
	if !strings.Contains(log, "agent upgrade restart ok") {
		t.Fatalf("upgrade log should record the successful restart:\n%s", log)
	}
}

func TestUpgradeStagingRemovedOnFailurePaths(t *testing.T) {
	dir := t.TempDir()

	// Explicit failure exit (download/verify/smoke style): Discard
	// removes the staging file at once.
	var explicitPath string
	func() {
		staging, err := newUpgradeStaging(dir)
		if err != nil {
			t.Fatalf("newUpgradeStaging: %v", err)
		}
		defer staging.Close()
		explicitPath = staging.Path()
		writeFileForTest(t, explicitPath, "PARTIAL", 0644)
		staging.Discard()
	}()
	if _, err := os.Stat(explicitPath); !os.IsNotExist(err) {
		t.Fatalf("staging file should be removed on explicit failure exit, stat err: %v", err)
	}

	// Bare failure exit with no explicit removal (checksum-fetch
	// style early return): the deferred Close backstop removes it.
	var backstopPath string
	func() error {
		staging, err := newUpgradeStaging(dir)
		if err != nil {
			t.Fatalf("newUpgradeStaging: %v", err)
		}
		defer staging.Close()
		backstopPath = staging.Path()
		writeFileForTest(t, backstopPath, "PARTIAL", 0644)
		return os.ErrInvalid
	}()
	if _, err := os.Stat(backstopPath); !os.IsNotExist(err) {
		t.Fatalf("staging file should be removed by Close backstop, stat err: %v", err)
	}

	// L2-05 uniqueness survives: concurrent stagings never share a
	// path, and discarding one leaves the other alone.
	a, err := newUpgradeStaging(dir)
	if err != nil {
		t.Fatalf("newUpgradeStaging a: %v", err)
	}
	b, err := newUpgradeStaging(dir)
	if err != nil {
		t.Fatalf("newUpgradeStaging b: %v", err)
	}
	defer b.Close()
	if a.Path() == b.Path() {
		t.Fatalf("staging paths must be unique per attempt, both %q", a.Path())
	}
	a.Discard()
	if _, err := os.Stat(b.Path()); err != nil {
		t.Fatalf("discarding one staging must not touch another: %v", err)
	}
}

func TestAgentRestartScriptReportsSwapFailure(t *testing.T) {
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "flux_agent")
	backupPath := binaryPath + ".old"
	writeFileForTest(t, binaryPath, "OLD-BINARY", 0755)
	writeFileForTest(t, backupPath, "OLD-BINARY", 0755)
	// The staged file vanished before the script ran (the F-RM-01
	// shape): the swap cannot happen, and the script must say so
	// instead of logging a successful upgrade on the old binary.
	missingStaged := filepath.Join(dir, ".flux_agent.upgrade-vanished")

	script, err := buildAgentRestartScriptFor(serviceManagerSystemd, missingStaged, binaryPath, backupPath)
	if err != nil {
		t.Fatalf("buildAgentRestartScriptFor: %v", err)
	}
	runRestartScriptForTest(t, script)

	if got := readFileForTest(t, binaryPath); got != "OLD-BINARY" {
		t.Fatalf("binary must stay on the old version when the swap fails, content: %q", got)
	}
	log := readFileForTest(t, filepath.Join(dir, "agent-upgrade.log"))
	if strings.Contains(log, "agent upgrade restart ok") {
		t.Fatalf("swap failure must not be logged as a successful upgrade:\n%s", log)
	}
	if !strings.Contains(log, "agent upgrade swap failed") {
		t.Fatalf("swap failure must be named in the upgrade log:\n%s", log)
	}
}

func TestAgentRestartScriptSwapGuardBothManagers(t *testing.T) {
	for _, manager := range []serviceManager{serviceManagerSystemd, serviceManagerOpenRC} {
		script, err := buildAgentRestartScriptFor(manager, "/etc/flux_agent/.flux_agent.upgrade-1", "/etc/flux_agent/flux_agent", "/etc/flux_agent/flux_agent.old")
		if err != nil {
			t.Fatalf("buildAgentRestartScriptFor %s: %v", manager, err)
		}
		for _, want := range []string{
			"if mv /etc/flux_agent/.flux_agent.upgrade-1 /etc/flux_agent/flux_agent; then",
			"agent upgrade swap failed",
			"rm -f /etc/flux_agent/.flux_agent.upgrade-1",
		} {
			if !strings.Contains(script, want) {
				t.Fatalf("%s restart script missing %q:\n%s", manager, want, script)
			}
		}
	}
}
