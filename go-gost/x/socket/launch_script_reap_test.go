package socket

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// countChildZombies counts defunct (state Z) children of the test
// process by scanning /proc — the only way to observe a reaped vs
// leaked child from the parent side.
func countChildZombies(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatalf("read /proc: %v", err)
	}
	self := os.Getpid()
	count := 0
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if err != nil {
			continue
		}
		// stat: pid (comm) state ppid ... — comm may contain spaces
		// and parens, so split after the last ')'.
		rest := string(data[strings.LastIndex(string(data), ")")+1:])
		fields := strings.Fields(rest)
		if len(fields) < 2 {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		if ppid == self && fields[0] == "Z" {
			count++
		}
	}
	return count
}

// TestLaunchAgentScriptsReapChildren pins 低-9 for the two detached
// script launchers: launchAgentControlScript (systemd-run/setsid)
// and launchAgentSelfRestartScript (setsid sh). Both used to return
// exec.Command(...).Start() with nobody ever calling Wait, so each
// launched script left a defunct zombie until the agent itself died.
// The scripts here are "true" (exit at once); after a short grace
// period no new zombie child of the test process may exist.
func TestLaunchAgentScriptsReapChildren(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("zombie observation needs /proc")
	}
	before := countChildZombies(t)
	if err := launchAgentControlScript(serviceManagerOpenRC, "true"); err != nil {
		t.Fatalf("launchAgentControlScript: %v", err)
	}
	if err := launchAgentSelfRestartScript("true"); err != nil {
		t.Fatalf("launchAgentSelfRestartScript: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		after := countChildZombies(t)
		if after <= before {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("zombie script children leaked: before=%d after=%d", before, after)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
