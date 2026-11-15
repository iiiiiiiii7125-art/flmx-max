package port

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

// TestForceClosePortConnectionsReapsTcpkill pins 低-9: the tcpkill
// child used to be killed but never waited on, leaving a defunct
// zombie per service stop. A fake tcpkill (sleep) on PATH stands in
// for the real binary; after the 2s kill window no new zombie child
// of the test process may exist.
func TestForceClosePortConnectionsReapsTcpkill(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("zombie observation needs /proc")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tcpkill"), []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatalf("write fake tcpkill: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	before := countChildZombies(t)
	if err := ForceClosePortConnections("127.0.0.1:12345"); err != nil {
		t.Fatalf("ForceClosePortConnections: %v", err)
	}
	// The kill fires 2s after Start; wait past it before the first
	// check, otherwise the test could pass before the child even died.
	time.Sleep(2600 * time.Millisecond)
	deadline := time.Now().Add(3 * time.Second)
	for {
		after := countChildZombies(t)
		if after <= before {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("zombie tcpkill children leaked: before=%d after=%d", before, after)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
