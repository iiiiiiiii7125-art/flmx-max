package socket

import (
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- P1-a: init system abstraction (A11-132-01) ---

func probeWith(alpine bool, systemdRuntime bool, bins map[string]bool) serviceManagerProbe {
	return serviceManagerProbe{
		lookPath: func(name string) (string, error) {
			if bins[name] {
				return "/usr/bin/" + name, nil
			}
			return "", errors.New("not found")
		},
		fileExists: func(path string) bool {
			return alpine && path == "/etc/alpine-release"
		},
		dirExists: func(path string) bool {
			return systemdRuntime && path == "/run/systemd/system"
		},
	}
}

func TestDetectServiceManagerOpenRCOnAlpine(t *testing.T) {
	// install.sh: Alpine wins even if a systemctl binary happens to exist.
	mgr, err := detectServiceManagerWith(probeWith(true, false, map[string]bool{"rc-service": true, "rc-update": true, "systemctl": true}))
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if mgr != serviceManagerOpenRC {
		t.Fatalf("expected openrc, got %q", mgr)
	}
}

func TestDetectServiceManagerSystemd(t *testing.T) {
	mgr, err := detectServiceManagerWith(probeWith(false, true, map[string]bool{"systemctl": true}))
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if mgr != serviceManagerSystemd {
		t.Fatalf("expected systemd, got %q", mgr)
	}
}

func TestDetectServiceManagerOpenRCFallback(t *testing.T) {
	mgr, err := detectServiceManagerWith(probeWith(false, false, map[string]bool{"rc-service": true, "rc-update": true}))
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if mgr != serviceManagerOpenRC {
		t.Fatalf("expected openrc, got %q", mgr)
	}
}

func TestDetectServiceManagerUnsupported(t *testing.T) {
	_, err := detectServiceManagerWith(probeWith(false, false, map[string]bool{}))
	if err == nil {
		t.Fatal("expected an explicit unsupported-init error, got nil")
	}
	if !strings.Contains(err.Error(), "systemd") || !strings.Contains(err.Error(), "OpenRC") {
		t.Fatalf("error should name the supported managers, got: %v", err)
	}
}

func TestBuildAgentRestartScriptSystemdHasLocalRollback(t *testing.T) {
	script, err := buildAgentRestartScriptFor(serviceManagerSystemd, "/etc/flux_agent/flux_agent.new", "/etc/flux_agent/flux_agent", "/etc/flux_agent/flux_agent.old")
	if err != nil {
		t.Fatalf("build script: %v", err)
	}
	for _, want := range []string{
		"systemctl stop flux_agent",
		"mv /etc/flux_agent/flux_agent.new /etc/flux_agent/flux_agent",
		"systemctl start flux_agent",
		"systemctl is-active --quiet flux_agent",
		"cp /etc/flux_agent/flux_agent.old /etc/flux_agent/flux_agent", // local auto-rollback
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("systemd restart script missing %q:\n%s", want, script)
		}
	}
}

func TestBuildAgentRestartScriptOpenRC(t *testing.T) {
	script, err := buildAgentRestartScriptFor(serviceManagerOpenRC, "/etc/flux_agent/flux_agent.new", "/etc/flux_agent/flux_agent", "/etc/flux_agent/flux_agent.old")
	if err != nil {
		t.Fatalf("build script: %v", err)
	}
	for _, want := range []string{
		"rc-service flux_agent stop",
		"mv /etc/flux_agent/flux_agent.new /etc/flux_agent/flux_agent",
		"rc-service flux_agent start",
		"cp /etc/flux_agent/flux_agent.old /etc/flux_agent/flux_agent",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("openrc restart script missing %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "systemctl") || strings.Contains(script, "systemd-run") {
		t.Fatalf("openrc restart script must not reference systemd:\n%s", script)
	}
}

func TestBuildAgentRestartScriptUnsupportedManager(t *testing.T) {
	if _, err := buildAgentRestartScriptFor(serviceManager("plan9"), "/tmp/a.new", "/tmp/a", "/tmp/a.old"); err == nil {
		t.Fatal("expected error for unknown service manager")
	}
}

func TestAgentControlScriptCommandPerManager(t *testing.T) {
	name, args, err := agentControlScriptCommand(serviceManagerSystemd, "echo hi")
	if err != nil || name != "systemd-run" {
		t.Fatalf("systemd launch = %q, %v", name, err)
	}
	_ = args

	name, _, err = agentControlScriptCommand(serviceManagerOpenRC, "echo hi")
	if err != nil || name != "setsid" {
		t.Fatalf("openrc launch = %q, %v (want setsid: escapes the agent's session so rc-service stop cannot kill the script)", name, err)
	}

	if _, _, err := agentControlScriptCommand(serviceManager("plan9"), "echo hi"); err == nil {
		t.Fatal("expected error for unknown service manager")
	}
}

// --- P1-b: upgrade safety chain (A11-132-02) ---

func TestBackupCurrentAgentBinaryCopies(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "flux_agent")
	backup := filepath.Join(dir, "flux_agent.old")
	if err := os.WriteFile(bin, []byte("old-binary"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := backupCurrentAgentBinary(bin, backup); err != nil {
		t.Fatalf("backup: %v", err)
	}
	data, err := os.ReadFile(backup)
	if err != nil || string(data) != "old-binary" {
		t.Fatalf("backup content = %q, %v", data, err)
	}
}

// Backup write failure must abort the upgrade instead of being swallowed
// (previously: _ = os.WriteFile(backupPath, ...) and the upgrade proceeded,
// leaving no rollback copy).
func TestBackupCurrentAgentBinaryWriteFailureAborts(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "flux_agent")
	if err := os.WriteFile(bin, []byte("old-binary"), 0755); err != nil {
		t.Fatal(err)
	}
	badBackup := filepath.Join(dir, "missing-dir", "flux_agent.old")
	if err := backupCurrentAgentBinary(bin, badBackup); err == nil {
		t.Fatal("expected backup write failure to surface as an error")
	}
}

func TestBackupCurrentAgentBinaryNoExistingBinary(t *testing.T) {
	dir := t.TempDir()
	if err := backupCurrentAgentBinary(filepath.Join(dir, "nope"), filepath.Join(dir, "b.old")); err != nil {
		t.Fatalf("missing current binary should not fail the backup step: %v", err)
	}
}

func writeFakeBinary(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake_agent")
	if err := os.WriteFile(path, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSmokeTestAgentBinary(t *testing.T) {
	ok := writeFakeBinary(t, "#!/bin/sh\necho 'flux_agent version 1.3.3'\nexit 0\n")
	if err := smokeTestAgentBinary(ok); err != nil {
		t.Fatalf("smoke test should pass for an executable binary: %v", err)
	}

	bad := writeFakeBinary(t, "#!/bin/sh\nexit 3\n")
	if err := smokeTestAgentBinary(bad); err == nil {
		t.Fatal("smoke test must fail when -V exits non-zero (incompatible/corrupt binary)")
	}

	if err := smokeTestAgentBinary(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("smoke test must fail for a missing binary")
	}
}

// --- D1-08: checksum body must be size-bounded ---

func TestReadAgentChecksum(t *testing.T) {
	hash, err := readAgentChecksum(strings.NewReader("abc123def  gost-amd64\n"))
	if err != nil {
		t.Fatalf("read checksum: %v", err)
	}
	if hash != "abc123def" {
		t.Fatalf("hash = %q", hash)
	}

	if _, err := readAgentChecksum(strings.NewReader("   \n")); err == nil {
		t.Fatal("empty checksum must be rejected")
	}

	huge := strings.NewReader(strings.Repeat("a", maxAgentChecksumSize+1))
	if _, err := readAgentChecksum(huge); err == nil {
		t.Fatal("oversized checksum body must be rejected (was an unbounded ReadAll)")
	}
}

// --- D1-03: gzip decompression must be size-bounded ---

func gzipPayload(t *testing.T, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDecompressCommandPayload(t *testing.T) {
	out, err := decompressCommandPayload(gzipPayload(t, []byte(`{"type":"TcpPing"}`)))
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	if string(out) != `{"type":"TcpPing"}` {
		t.Fatalf("payload = %q", out)
	}

	// A small compressed blob inflating past the cap must be rejected
	// (gzip bomb), not buffered unboundedly into memory.
	bomb := gzipPayload(t, make([]byte, maxDecompressedCommandSize+1))
	if _, err := decompressCommandPayload(bomb); err == nil {
		t.Fatal("expected oversized decompressed payload to be rejected")
	}

	if _, err := decompressCommandPayload([]byte("not gzip")); err == nil {
		t.Fatal("expected garbage payload to fail")
	}
}

// --- D1-02: encrypted command timestamp window (±5 min) ---

func TestValidateEncryptedTimestamp(t *testing.T) {
	now := time.Unix(1_760_000_000, 0)

	// The panel sends UnixMilli.
	if err := validateEncryptedTimestamp(now.UnixMilli(), now); err != nil {
		t.Fatalf("fresh millisecond timestamp rejected: %v", err)
	}
	if err := validateEncryptedTimestamp(now.Add(-4*time.Minute).UnixMilli(), now); err != nil {
		t.Fatalf("4-minute-old timestamp rejected: %v", err)
	}
	if err := validateEncryptedTimestamp(now.Add(-10*time.Minute).UnixMilli(), now); err == nil {
		t.Fatal("10-minute-old (replayed) timestamp must be rejected")
	}
	if err := validateEncryptedTimestamp(now.Add(10*time.Minute).UnixMilli(), now); err == nil {
		t.Fatal("10-minute-future timestamp must be rejected")
	}
	if err := validateEncryptedTimestamp(0, now); err == nil {
		t.Fatal("zero timestamp must be rejected")
	}
	// Seconds-based timestamps are tolerated for robustness.
	if err := validateEncryptedTimestamp(now.Unix(), now); err != nil {
		t.Fatalf("fresh seconds timestamp rejected: %v", err)
	}
}

// --- D1-04: ServiceMonitorCheck timeout clamp ---

func TestClampServiceMonitorTimeoutSec(t *testing.T) {
	cases := map[int]int{
		0:    5,
		-3:   5,
		5:    5,
		30:   30,
		60:   60,
		3600: 60, // was unbounded: one check could occupy a read-command slot for an hour
	}
	for in, want := range cases {
		if got := clampServiceMonitorTimeoutSec(in); got != want {
			t.Fatalf("clampServiceMonitorTimeoutSec(%d) = %d, want %d", in, got, want)
		}
	}
}

// --- D1-02 + D1-01: no silent wss->ws downgrade ---

func TestBuildWebSocketCandidatesNeverDowngrades(t *testing.T) {
	// Explicitly secure address: exactly one wss candidate, no ws fallback.
	candidates := buildWebSocketCandidates("wss://panel.example.com:443", "abc", "1.3.2", 0, 0, 0, "")
	if len(candidates) != 1 || !strings.HasPrefix(candidates[0], "wss://") {
		t.Fatalf("expected exactly one wss candidate, got %v", candidates)
	}

	// Bare address defaults to wss only (no silent plaintext fallback).
	candidates = buildWebSocketCandidates("panel.example.com:443", "abc", "1.3.2", 0, 0, 0, "")
	if len(candidates) != 1 || !strings.HasPrefix(candidates[0], "wss://") {
		t.Fatalf("expected exactly one wss candidate for bare address, got %v", candidates)
	}

	// Explicitly plain address stays ws (operator's explicit choice).
	candidates = buildWebSocketCandidates("ws://panel.example.com:8080", "abc", "1.3.2", 0, 0, 0, "")
	if len(candidates) != 1 || !strings.HasPrefix(candidates[0], "ws://") {
		t.Fatalf("expected exactly one ws candidate, got %v", candidates)
	}

	// Learned preference for ws (panel known to be plain) yields ws only.
	candidates = buildWebSocketCandidates("panel.example.com:8080", "abc", "1.3.2", 0, 0, 0, "ws")
	if len(candidates) != 1 || !strings.HasPrefix(candidates[0], "ws://") {
		t.Fatalf("expected exactly one ws candidate from preference, got %v", candidates)
	}
}
