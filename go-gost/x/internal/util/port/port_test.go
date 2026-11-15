package port

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func c04WriteFakeBin(t *testing.T, dir, name, script string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
}

// Both killers missing: a real error naming both mechanisms (old code
// silently returned nil and let connections drain for ~2 minutes).
func TestForceClosePortConnectionsBothMissing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	err := ForceClosePortConnections("127.0.0.1:24510")
	if err == nil {
		t.Fatal("expected error when neither ss nor tcpkill exists")
	}
	if !strings.Contains(err.Error(), "ss") || !strings.Contains(err.Error(), "tcpkill") {
		t.Fatalf("error must name both mechanisms, got %v", err)
	}
}

// Bad addresses are real errors (old code silently returned nil).
func TestForceClosePortConnectionsBadAddr(t *testing.T) {
	for _, addr := range []string{"", "noport", "127.0.0.1:99999", "127.0.0.1:0", "127.0.0.1:notaport"} {
		if err := ForceClosePortConnections(addr); err == nil {
			t.Fatalf("addr %q must fail", addr)
		}
	}
}

// Fake ss wins: invoked with the sport filter, tcpkill never attempted.
func TestForceClosePortConnectionsUsesSSFirst(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "ss.log")
	c04WriteFakeBin(t, dir, "ss", "#!/bin/sh\necho \"$@\" >> \""+logFile+"\"\nexit 0\n")
	t.Setenv("PATH", dir)
	if err := ForceClosePortConnections("127.0.0.1:24510"); err != nil {
		t.Fatalf("fake-ss kill: %v", err)
	}
	b, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("ss was not invoked: %v", err)
	}
	if !strings.Contains(string(b), "-K") || !strings.Contains(string(b), "sport = :24510") {
		t.Fatalf("ss args = %q, want -K with sport filter for 24510", string(b))
	}
}

// tcpkill fallback: invoked with the port, runs its ~2s window, and is
// reaped — no zombie left behind (audit low-9).
func TestForceClosePortConnectionsTcpkillFallbackReaps(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "tcpkill.pid")
	// Builtin-only loop: no external command, so the restricted PATH
	// cannot make the fake exit early.
	c04WriteFakeBin(t, dir, "tcpkill", "#!/bin/sh\necho $$ > \""+pidFile+"\"\nwhile :; do :; done\n")
	t.Setenv("PATH", dir)
	start := time.Now()
	if err := ForceClosePortConnections("127.0.0.1:24510"); err != nil {
		t.Fatalf("tcpkill fallback: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 2*time.Second || elapsed > 10*time.Second {
		t.Fatalf("tcpkill fallback took %v, want ~2s window", elapsed)
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("tcpkill was not invoked: %v", err)
	}
	pid := strings.TrimSpace(string(b))
	// Reaped => /proc/<pid> gone. A zombie would still have a /proc
	// entry in state Z; a leaked live process would still be running.
	if _, err := os.Stat("/proc/" + pid); err == nil {
		st, _ := os.ReadFile("/proc/" + pid + "/stat")
		t.Fatalf("tcpkill process %s still present after ForceClose (stat %q) — not reaped", pid, string(st))
	}
}
