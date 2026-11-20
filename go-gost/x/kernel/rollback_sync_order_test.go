package kernel

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// placeCrashKernelTimed is placeCrashKernel with a nanosecond
// timestamp on every working launch, so a test can order a launch
// against the nft script loads recorded by the whitelist stub
// (same machine clock: stub uses time.Now, the script uses
// `date +%s%N`).
func placeCrashKernelTimed(t *testing.T, m *Manager, kernelType, version, binName, marker string) {
	t.Helper()
	binDir := m.binDir(kernelType, version)
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"version\" ] || [ \"$1\" = \"-v\" ]; then echo \"" + kernelType + " " + version + " (fake)\"; exit 0; fi\n" +
		"if grep -q crash \"$3\"; then echo start-crash >> \"" + marker + "\"; echo \"config error\"; exit 1; fi\n" +
		"echo \"start-ok $(date +%s%N)\" >> \"" + marker + "\"\n" +
		"exec sleep 300\n"
	bin := filepath.Join(binDir, binName)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
}

// timedStartNanos returns the launch timestamps (Unix nanos) of
// every working launch recorded in marker, in launch order.
func timedStartNanos(t *testing.T, marker string) []int64 {
	t.Helper()
	b, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	var out []int64
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "start-ok" {
			continue
		}
		n, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			t.Fatalf("marker line %q has no nanosecond timestamp: %v", line, err)
		}
		out = append(out, n)
	}
	return out
}

// R18-R2-D3-02: the rollback re-sync used to run after
// startProcesses + waitStable, so a guarded previous config
// listened under the NEW request's rules for the whole probe
// window (typical ~1.5s, up to ~11.5s on a slow-restoring
// kernel). The re-sync must land BEFORE the rolled-back process
// starts — same order as Apply and syncGuardBeforeRestart. This
// test orders the rollback's whitelist script against the
// rollback launch: on the old order the script lands ~1.5s after
// the launch; on the fixed order it lands before it.
func TestApplyRollbackResyncsBeforeRestart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as fake kernel")
	}
	stub := stubWhitelistNft(t)
	m := NewManager(t.TempDir())
	t.Cleanup(m.Shutdown)
	marker := filepath.Join(t.TempDir(), "starts")
	placeCrashKernelTimed(t, m, "xray", "9.9.9", "xray", marker)

	v1 := whitelistRequest("xray", "9.9.9", "xray", "cfg-v1", []string{"203.0.113.0/24"})
	if st, err := m.Apply(v1); err != nil || !st.Running {
		t.Fatalf("v1 apply: state=%+v err=%v", st, err)
	}

	v2 := whitelistRequest("xray", "9.9.9", "xray", `{"crash":true}`, nil)
	st, err := m.Apply(v2)
	if err == nil || !strings.Contains(err.Error(), "已恢复") {
		t.Fatalf("crashing unguarded config must roll back, got state=%+v err=%v", st, err)
	}
	if !st.Running {
		t.Fatalf("rolled-back kernel must be running: %+v", st)
	}
	starts := timedStartNanos(t, marker)
	if len(starts) != 2 {
		t.Fatalf("want exactly 2 working starts (v1 + rollback), got %d", len(starts))
	}
	// The rollback's whitelist script is the last script carrying
	// v1's whitelist (v1 sync, then the v2 sync without it, then
	// the rollback re-sync).
	lastWL := -1
	for i, s := range stub.scripts {
		if strings.Contains(s, "203.0.113.0/24") {
			lastWL = i
		}
	}
	if lastWL < 0 {
		t.Fatal("no applied script carries the v1 whitelist")
	}
	if len(stub.scriptTimes) != len(stub.scripts) {
		t.Fatalf("stub recorded %d times for %d scripts", len(stub.scriptTimes), len(stub.scripts))
	}
	resyncAt := stub.scriptTimes[lastWL].UnixNano()
	rollbackStart := starts[1]
	if !(resyncAt < rollbackStart) {
		t.Fatalf("rollback re-sync must land BEFORE the rolled-back process starts: whitelist script at %d, rollback launch at %d (gap %v)", resyncAt, rollbackStart, time.Duration(resyncAt-rollbackStart))
	}
}
