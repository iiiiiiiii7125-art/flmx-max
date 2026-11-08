package socket

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// GARBAGE-AUDIT A2: agent-upgrade.log grew without bound (one
// appended section per upgrade/rollback). Both generated scripts
// must prepend the size-cap segment before redirecting output into
// the log, and the segment must actually truncate an over-cap log
// while leaving an under-cap log untouched.

func TestAgentUpgradeLogCapSegmentPrependedToBothScripts(t *testing.T) {
	restart, err := buildAgentRestartScriptFor(serviceManagerSystemd, "/etc/flmx_agent/flmx_agent.new", "/etc/flmx_agent/flmx_agent", "/etc/flmx_agent/flmx_agent.old")
	if err != nil {
		t.Fatal(err)
	}
	rollback, err := buildAgentRollbackScriptFor(serviceManagerSystemd, "/etc/flmx_agent/flmx_agent.old", "/etc/flmx_agent/flmx_agent")
	if err != nil {
		t.Fatal(err)
	}
	for name, script := range map[string]string{"restart": restart, "rollback": rollback} {
		capIdx := strings.Index(script, "_flmx_log_size")
		execIdx := strings.Index(script, "exec >>")
		if capIdx < 0 || execIdx < 0 || capIdx > execIdx {
			t.Fatalf("%s script must run the log cap segment before the exec redirect (cap=%d exec=%d)", name, capIdx, execIdx)
		}
		if !strings.Contains(script, "1048576") {
			t.Fatalf("%s script must carry the 1MB cap literal", name)
		}
	}
}

func TestAgentUpgradeLogCapSegmentTruncatesOnlyOverCap(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "agent-upgrade.log")
	segment := agentUpgradeLogCapSegment(logPath)

	over := make([]byte, agentUpgradeLogMaxBytes+1)
	if err := os.WriteFile(logPath, over, 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("sh", "-c", segment).CombinedOutput(); err != nil {
		t.Fatalf("cap segment must run cleanly on an over-cap log: %v (%s)", err, out)
	}
	if info, err := os.Stat(logPath); err != nil || info.Size() != 0 {
		t.Fatalf("over-cap log must be truncated to 0, size=%v err=%v", info, err)
	}

	small := []byte("recent upgrade record\n")
	if err := os.WriteFile(logPath, small, 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("sh", "-c", segment).CombinedOutput(); err != nil {
		t.Fatalf("cap segment must run cleanly on an under-cap log: %v (%s)", err, out)
	}
	got, err := os.ReadFile(logPath)
	if err != nil || string(got) != string(small) {
		t.Fatalf("under-cap log must be untouched, got %q err=%v", got, err)
	}
}
