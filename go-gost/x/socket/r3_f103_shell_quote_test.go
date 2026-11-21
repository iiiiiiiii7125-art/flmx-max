package socket

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// R18-R3-F1-03: the upgrade/rollback scripts spliced binary, staged,
// backup, log and marker paths into mv/cp/rm/mktemp/cmp/[ -f ] and
// the exec redirect unquoted, so any install dir containing a space
// broke the upgrade mid-swap. Every path word must be single-quoted
// (quoteShellPath / tuneShellQuote); an empty path fails closed at
// build time instead of reaching the shell.

func TestR3F103SpaceDirScriptsQuotePaths(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "flmx dir with space")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(dir, "flmx_agent.new")
	bin := filepath.Join(dir, "flmx_agent")
	bak := filepath.Join(dir, "flmx_agent.old")
	logPath := filepath.Join(dir, "agent-upgrade.log")
	marker := filepath.Join(dir, agentConnectedMarkerName)

	restart, err := buildAgentRestartScriptForCap(serviceManagerSystemd, tmp, bin, bak, agentTargetMarkerCapable, "2.1.18")
	if err != nil {
		t.Fatalf("buildAgentRestartScriptForCap: %v", err)
	}
	rollback, err := buildAgentRollbackScriptForCap(serviceManagerSystemd, bak, bin, agentTargetMarkerCapable, "2.1.18")
	if err != nil {
		t.Fatalf("buildAgentRollbackScriptForCap: %v", err)
	}
	selfCtx := &agentSelfRestartContext{pid: 12345, args: nil, workDir: dir}
	selfRestart, err := buildAgentRestartScriptSelfCap(tmp, bin, bak, selfCtx, agentTargetMarkerCapable, "2.1.18")
	if err != nil {
		t.Fatalf("buildAgentRestartScriptSelfCap: %v", err)
	}
	selfRollback, err := buildAgentRollbackScriptSelfCap(bak, bin, selfCtx, agentTargetMarkerCapable, "2.1.18")
	if err != nil {
		t.Fatalf("buildAgentRollbackScriptSelfCap: %v", err)
	}

	// Wants shared by all four scripts (swap legs, log cap, marker
	// gate) plus the ones only the restart scripts carry (main
	// swap + its abort cleanup + the rollback leg's backup test).
	commonQuoted := []string{
		"cp '" + bak + "' \"$rollback_tmp\"",
		"cmp '" + bak + "' \"$rollback_tmp\"",
		"mv \"$rollback_tmp\" '" + bin + "'",
		"exec >>'" + logPath + "' 2>&1",
		"[ -f '" + marker + "' ]",
		"wc -c < '" + logPath + "'",
		"head -n 1 '" + marker + "'",
	}
	restartQuoted := []string{
		"mv '" + tmp + "' '" + bin + "'",
		"rm -f '" + tmp + "'",
		"[ -f '" + bak + "' ]",
		"rollback_tmp=$(mktemp '" + dir + "/.flmx_agent.upgrade-rollback-XXXXXX')",
	}
	rollbackQuoted := []string{
		"rollback_tmp=$(mktemp '" + dir + "/.flmx_agent.rollback-XXXXXX')",
	}
	commonBanned := []string{
		"cp " + bak, "cmp " + bak,
		"exec >>" + logPath,
		"mktemp " + dir,
		"head -n 1 " + marker,
	}
	restartBanned := []string{
		"mv " + tmp, "mv " + bin,
		"rm -f " + tmp, "[ -f " + bak,
	}
	for _, tc := range []struct {
		name   string
		script string
		quoted []string
		banned []string
	}{
		{"restart", restart, append(append([]string{}, commonQuoted...), restartQuoted...), append(append([]string{}, commonBanned...), restartBanned...)},
		{"rollback", rollback, append(append([]string{}, commonQuoted...), rollbackQuoted...), commonBanned},
		{"self-restart", selfRestart, append(append([]string{}, commonQuoted...), restartQuoted...), append(append([]string{}, commonBanned...), restartBanned...)},
		{"self-rollback", selfRollback, append(append([]string{}, commonQuoted...), rollbackQuoted...), commonBanned},
	} {
		for _, want := range tc.quoted {
			if !strings.Contains(tc.script, want) {
				t.Fatalf("%s script missing quoted %q:\n%s", tc.name, want, tc.script)
			}
		}
		// No unquoted path word may survive: `mv <dir>` without
		// the quote, `cp <backup>` without it, `exec >> <log>`.
		for _, banned := range tc.banned {
			if strings.Contains(tc.script, banned) {
				t.Fatalf("%s script still splices unquoted %q:\n%s", tc.name, banned, tc.script)
			}
		}
	}
	// A quote inside the path must be escaped, never left to
	// terminate the quoted word (exercises the helper through
	// the builders, so the test still compiles on old code).
	quoteDir := filepath.Join(t.TempDir(), "flmx'dir")
	quoteTmp := filepath.Join(quoteDir, "flmx_agent.new")
	quoteBin := filepath.Join(quoteDir, "flmx_agent")
	quoteScript, err := buildAgentRestartScriptForCap(serviceManagerSystemd, quoteTmp, quoteBin, filepath.Join(quoteDir, "flmx_agent.old"), agentTargetMarkerCapable, "2.1.18")
	if err != nil {
		t.Fatalf("buildAgentRestartScriptForCap: %v", err)
	}
	if want := "mv '" + strings.ReplaceAll(quoteTmp, "'", `'"'"'`) + "' '" + strings.ReplaceAll(quoteBin, "'", `'"'"'`) + "'"; !strings.Contains(quoteScript, want) {
		t.Fatalf("script must escape the quote inside the path, want %q:\n%s", want, quoteScript)
	}
}

func TestR3F103EmptyPathFailsClosed(t *testing.T) {
	if _, err := buildAgentRestartScriptForCap(serviceManagerSystemd, "", "/etc/flmx_agent/flmx_agent", "/etc/flmx_agent/flmx_agent.old", agentTargetMarkerCapable, "2.1.18"); err == nil {
		t.Fatal("empty staged path must fail closed")
	}
	if _, err := buildAgentRestartScriptForCap(serviceManagerSystemd, "/etc/flmx_agent/flmx_agent.new", "", "/etc/flmx_agent/flmx_agent.old", agentTargetMarkerCapable, "2.1.18"); err == nil {
		t.Fatal("empty binary path must fail closed")
	}
	if _, err := buildAgentRollbackScriptForCap(serviceManagerSystemd, "", "/etc/flmx_agent/flmx_agent", agentTargetMarkerCapable, "2.1.18"); err == nil {
		t.Fatal("empty backup path must fail closed")
	}
	selfCtx := &agentSelfRestartContext{pid: 12345, workDir: "/tmp"}
	if _, err := buildAgentRestartScriptSelfCap("/tmp/a.new", "/tmp/a", "", selfCtx, agentTargetMarkerCapable, "2.1.18"); err == nil {
		t.Fatal("empty backup path (self restart) must fail closed")
	}
	if _, err := buildAgentRollbackScriptSelfCap("/tmp/a.old", "", selfCtx, agentTargetMarkerCapable, "2.1.18"); err == nil {
		t.Fatal("empty binary path (self rollback) must fail closed")
	}
}

// writeR3F103FakeAgentBinary drops a stand-in "binary" in the space dir:
// -V answers a real banner so the capability probe succeeds, and
// otherwise it records its pid and sleeps so the health check can
// sample it as a live process.
func writeR3F103FakeAgentBinary(t *testing.T, path, version, marker, pidFile string) {
	t.Helper()
	script := "#!/bin/sh\nif [ \"$1\" = \"-V\" ]; then echo \"gost " + version + "\"; exit 0; fi\n# " + marker + "\necho $$ > \"" + pidFile + "\"\nsleep 30\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func startSacrificialSleep(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Reap promptly in the background: without this the killed
	// sleep stays a zombie child of the test process, kill -0
	// keeps succeeding on it, and the script (correctly) aborts
	// with "previous agent process did not exit".
	go func() { _ = cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	return cmd
}

// killPidFileAgent stops the fake agent a script relaunched (its pid
// lands in pidFile), so no sleep process outlives the test.
func killPidFileAgent(t *testing.T, pidFile string) {
	t.Helper()
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("relaunched agent must record its pid in %s: %v", pidFile, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatalf("pid file %q unparsable: %v", b, err)
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("FindProcess(%d): %v", pid, err)
	}
	_ = p.Kill()
}

func TestR3F103SelfRestartRunsInSpaceDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "flmx dir with space")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(dir, "flmx_agent.new")
	bin := filepath.Join(dir, "flmx_agent")
	bak := filepath.Join(dir, "flmx_agent.old")
	pidFile := filepath.Join(dir, "running.pid")
	writeR3F103FakeAgentBinary(t, tmp, "2.1.19", "NEW-BINARY", pidFile)
	writeR3F103FakeAgentBinary(t, bin, "2.1.18", "OLD-BINARY", pidFile)
	writeR3F103FakeAgentBinary(t, bak, "2.1.18", "OLD-BACKUP", pidFile)

	prev := startSacrificialSleep(t)
	self := &agentSelfRestartContext{pid: prev.Process.Pid, args: nil, workDir: dir}
	script, err := buildAgentRestartScriptSelf(tmp, bin, bak, self)
	if err != nil {
		t.Fatalf("buildAgentRestartScriptSelf: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "sh", "-c", script).Run(); err != nil {
		log, _ := os.ReadFile(filepath.Join(dir, "agent-upgrade.log"))
		t.Fatalf("self restart script in space dir failed: %v\nlog:\n%s\nscript:\n%s", err, log, script)
	}
	t.Cleanup(func() { killPidFileAgent(t, pidFile) })

	got, err := os.ReadFile(bin)
	if err != nil || !strings.Contains(string(got), "NEW-BINARY") {
		t.Fatalf("staged binary must have been moved into place, bin=%q err=%v", got, err)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatalf("staged file must be consumed by the mv (err=%v)", err)
	}
	log, err := os.ReadFile(filepath.Join(dir, "agent-upgrade.log"))
	if err != nil || !strings.Contains(string(log), "agent upgrade restart ok") {
		t.Fatalf("log must record 'agent upgrade restart ok', log=%q err=%v", log, err)
	}
}

func TestR3F103SelfRollbackRunsInSpaceDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "flmx dir with space")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "flmx_agent")
	bak := filepath.Join(dir, "flmx_agent.old")
	pidFile := filepath.Join(dir, "running.pid")
	writeR3F103FakeAgentBinary(t, bin, "2.1.19", "CURRENT-BINARY", pidFile)
	writeR3F103FakeAgentBinary(t, bak, "2.1.18", "OLD-BACKUP", pidFile)

	prev := startSacrificialSleep(t)
	self := &agentSelfRestartContext{pid: prev.Process.Pid, args: nil, workDir: dir}
	script, err := buildAgentRollbackScriptSelf(bak, bin, self)
	if err != nil {
		t.Fatalf("buildAgentRollbackScriptSelf: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "sh", "-c", script).Run(); err != nil {
		log, _ := os.ReadFile(filepath.Join(dir, "agent-upgrade.log"))
		t.Fatalf("self rollback script in space dir failed: %v\nlog:\n%s\nscript:\n%s", err, log, script)
	}
	t.Cleanup(func() { killPidFileAgent(t, pidFile) })

	got, err := os.ReadFile(bin)
	if err != nil || !strings.Contains(string(got), "OLD-BACKUP") {
		t.Fatalf("backup must have been swapped into place, bin=%q err=%v", got, err)
	}
	log, err := os.ReadFile(filepath.Join(dir, "agent-upgrade.log"))
	if err != nil || !strings.Contains(string(log), "rollback restart ok") {
		t.Fatalf("log must record 'rollback restart ok', log=%q err=%v", log, err)
	}
}
