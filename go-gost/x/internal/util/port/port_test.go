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

// A-NEW: ss -K runs on BOTH directions with no state qualifier. The
// old filter `state established ( sport = :PORT )` missed bulk relay
// flows, whose sockets leave ESTABLISHED (FIN-WAIT-2/CLOSE-WAIT)
// before the pause lands; the peer-side socket (dport) is the one
// holding the undelivered buffered data.
func TestForceClosePortConnectionsKillsBothDirections(t *testing.T) {
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
	args := string(b)
	if !strings.Contains(args, "sport = :24510") || !strings.Contains(args, "dport = :24510") {
		t.Fatalf("ss args = %q, want -K on both sport and dport for 24510", args)
	}
	if strings.Contains(args, "established") {
		t.Fatalf("ss args = %q, must not filter on state (bulk flows are FIN-WAIT-2/CLOSE-WAIT by pause time)", args)
	}
}

// countSSKilled counts one line per socket ss -K reports destroying;
// the header line is not a socket.
func TestCountSSKilled(t *testing.T) {
	header := "Netid State Recv-Q Send-Q Local Address:Port Peer Address:Port Process\n"
	if n := countSSKilled(header); n != 0 {
		t.Fatalf("header only = %d, want 0", n)
	}
	out := header +
		"tcp   ESTAB 0      0          127.0.0.1:45608    127.0.0.1:29340\n" +
		"tcp   FIN-WAIT-2 0 0          [::ffff:127.0.0.1]:29331 [::ffff:127.0.0.1]:60292\n"
	if n := countSSKilled(out); n != 2 {
		t.Fatalf("two socket lines = %d, want 2", n)
	}
	if n := countSSKilled(""); n != 0 {
		t.Fatalf("empty output = %d, want 0", n)
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
