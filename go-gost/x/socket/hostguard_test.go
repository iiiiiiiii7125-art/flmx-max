package socket

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hostGuardFixture builds a hostGuardEnv rooted at a temp dir:
// fake /proc/sys/net/netfilter leaves, fake /etc targets, stubbed
// statfs/runner/getenv/euid. Nothing touches the real host.
type hostGuardFixture struct {
	env     *hostGuardEnv
	root    string
	runArgs [][]string
	runErr  error
	envVars map[string]string
}

func newHostGuardFixture(t *testing.T) *hostGuardFixture {
	t.Helper()
	root := t.TempDir()
	f := &hostGuardFixture{root: root, envVars: map[string]string{}}
	nfDir := filepath.Join(root, "proc", "netfilter")
	if err := os.MkdirAll(nfDir, 0755); err != nil {
		t.Fatal(err)
	}
	f.env = &hostGuardEnv{
		netfilterDir:         nfDir,
		conntrackPersistPath: filepath.Join(root, "sysctl.d", "99-flmx-conntrack.conf"),
		journaldDropinPath:   filepath.Join(root, "journald.conf.d", "99-flmx.conf"),
		journaldConfPath:     filepath.Join(root, "journald.conf"),
		systemdRunDir:        filepath.Join(root, "systemd-run"),
		statfsTotal: func(string) (uint64, error) {
			return 4800000000, nil // ~4.8G root disk, the ECS1686 shape
		},
		runCmd: func(ctx context.Context, name string, args ...string) (string, error) {
			f.runArgs = append(f.runArgs, append([]string{name}, args...))
			return "", f.runErr
		},
		getenv: func(k string) string { return f.envVars[k] },
		euid:   func() int { return 0 },
	}
	return f
}

func (f *hostGuardFixture) setSysctl(t *testing.T, name, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.env.netfilterDir, name), []byte(value+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
}

func (f *hostGuardFixture) getSysctl(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.env.netfilterDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(raw))
}

// seedIncidentShape writes the ECS1686 pre-fix values.
func (f *hostGuardFixture) seedIncidentShape(t *testing.T) {
	t.Helper()
	f.setSysctl(t, "nf_conntrack_count", "1441")
	f.setSysctl(t, "nf_conntrack_max", "4096")
	f.setSysctl(t, "nf_conntrack_tcp_timeout_established", "432000")
	f.setSysctl(t, "nf_conntrack_tcp_timeout_time_wait", "120")
	f.setSysctl(t, "nf_conntrack_udp_timeout", "30")
	f.setSysctl(t, "nf_conntrack_udp_timeout_stream", "120")
}

func TestEnsureConntrackFloorConvergesIncidentShape(t *testing.T) {
	f := newHostGuardFixture(t)
	f.seedIncidentShape(t)

	res := ensureConntrackFloor(f.env, true)
	if res.Failed {
		t.Fatalf("unexpected failure: %v", res.Lines)
	}
	if !res.Changed {
		t.Fatalf("expected Changed, lines: %v", res.Lines)
	}
	if got := f.getSysctl(t, "nf_conntrack_max"); got != "131072" {
		t.Errorf("max = %s, want 131072", got)
	}
	if got := f.getSysctl(t, "nf_conntrack_tcp_timeout_established"); got != "1200" {
		t.Errorf("established = %s, want 1200", got)
	}
	if got := f.getSysctl(t, "nf_conntrack_tcp_timeout_time_wait"); got != "60" {
		t.Errorf("time_wait = %s, want 60", got)
	}
	// Values already inside the safe zone must not move (an
	// operator's tighter settings are never loosened).
	if got := f.getSysctl(t, "nf_conntrack_udp_timeout"); got != "30" {
		t.Errorf("udp_timeout = %s, want untouched 30", got)
	}
	if got := f.getSysctl(t, "nf_conntrack_udp_timeout_stream"); got != "120" {
		t.Errorf("udp_timeout_stream = %s, want untouched 120", got)
	}

	persist, err := os.ReadFile(f.env.conntrackPersistPath)
	if err != nil {
		t.Fatalf("persist file missing: %v", err)
	}
	for _, want := range []string{
		"net.netfilter.nf_conntrack_max=131072",
		"net.netfilter.nf_conntrack_tcp_timeout_established=1200",
		"net.netfilter.nf_conntrack_tcp_timeout_time_wait=60",
		"net.netfilter.nf_conntrack_udp_timeout=30",
		"net.netfilter.nf_conntrack_udp_timeout_stream=120",
	} {
		if !strings.Contains(string(persist), want) {
			t.Errorf("persist file missing %q:\n%s", want, persist)
		}
	}
	joined := strings.Join(res.Lines, "\n")
	if !strings.Contains(joined, "nf_conntrack_max 4096→131072") {
		t.Errorf("lines should name the convergence, got: %s", joined)
	}
}

func TestEnsureConntrackFloorIdempotent(t *testing.T) {
	f := newHostGuardFixture(t)
	f.seedIncidentShape(t)
	_ = ensureConntrackFloor(f.env, true)

	res := ensureConntrackFloor(f.env, false)
	if res.Changed || res.Failed {
		t.Fatalf("second run must be a no-op, got changed=%v failed=%v lines=%v", res.Changed, res.Failed, res.Lines)
	}
	if len(res.Lines) != 0 {
		t.Fatalf("non-verbose no-op must stay silent, got %v", res.Lines)
	}
}

func TestEnsureConntrackFloorKeepsSaferValues(t *testing.T) {
	f := newHostGuardFixture(t)
	f.setSysctl(t, "nf_conntrack_count", "10")
	f.setSysctl(t, "nf_conntrack_max", "262144")
	f.setSysctl(t, "nf_conntrack_tcp_timeout_established", "600")
	f.setSysctl(t, "nf_conntrack_tcp_timeout_time_wait", "30")
	f.setSysctl(t, "nf_conntrack_udp_timeout", "30")
	f.setSysctl(t, "nf_conntrack_udp_timeout_stream", "90")

	res := ensureConntrackFloor(f.env, true)
	if res.Failed {
		t.Fatalf("unexpected failure: %v", res.Lines)
	}
	if got := f.getSysctl(t, "nf_conntrack_max"); got != "262144" {
		t.Errorf("safer max must stay, got %s", got)
	}
	if got := f.getSysctl(t, "nf_conntrack_tcp_timeout_established"); got != "600" {
		t.Errorf("safer established must stay, got %s", got)
	}
	// Persisting the already-safe values is still a change (the
	// boot-time restore point), and must be reported as such.
	if !res.Changed {
		t.Errorf("expected Changed from persist write, lines: %v", res.Lines)
	}
	persist, _ := os.ReadFile(f.env.conntrackPersistPath)
	if !strings.Contains(string(persist), "nf_conntrack_max=262144") {
		t.Errorf("persist must carry the in-effect safer value:\n%s", persist)
	}
}

func TestEnsureConntrackFloorModuleAbsent(t *testing.T) {
	f := newHostGuardFixture(t) // no sysctl leaves seeded
	res := ensureConntrackFloor(f.env, true)
	if res.Failed || res.Changed {
		t.Fatalf("absent module must be a clean skip, got %+v", res)
	}
	if !strings.Contains(strings.Join(res.Lines, "\n"), "尚未加载") {
		t.Errorf("skip must be explained, got %v", res.Lines)
	}
	if _, err := os.Stat(f.env.conntrackPersistPath); !os.IsNotExist(err) {
		t.Errorf("no persist file may be written when the surface is absent")
	}
}

func TestEnsureConntrackFloorNonRootHonest(t *testing.T) {
	f := newHostGuardFixture(t)
	f.seedIncidentShape(t)
	f.env.euid = func() int { return 1000 }

	res := ensureConntrackFloor(f.env, true)
	if !res.Failed {
		t.Fatalf("non-root with out-of-bound values must report failure, got %v", res.Lines)
	}
	if got := f.getSysctl(t, "nf_conntrack_max"); got != "4096" {
		t.Errorf("non-root run must not write, max=%s", got)
	}
	if !strings.Contains(strings.Join(res.Lines, "\n"), "非 root") {
		t.Errorf("failure must name the cause, got %v", res.Lines)
	}
}

func TestEnsureConntrackFloorSkipEnv(t *testing.T) {
	f := newHostGuardFixture(t)
	f.seedIncidentShape(t)
	f.envVars["FLMX_SKIP_CONNTRACK"] = "1"

	res := ensureConntrackFloor(f.env, true)
	if res.Changed || res.Failed {
		t.Fatalf("skip must be a clean no-op, got %+v", res)
	}
	if got := f.getSysctl(t, "nf_conntrack_max"); got != "4096" {
		t.Errorf("skip must not write, max=%s", got)
	}
}

func TestReadConntrackState(t *testing.T) {
	f := newHostGuardFixture(t)
	if st := readConntrackState(f.env); st.Available {
		t.Fatalf("absent module must read unavailable")
	}
	f.seedIncidentShape(t)
	st := readConntrackState(f.env)
	if !st.Available || st.Count != 1441 || st.Max != 4096 {
		t.Fatalf("state = %+v, want {true 1441 4096}", st)
	}
	if !ConntrackBelowFloor(st) {
		t.Errorf("4096 must be below the floor")
	}
	f.setSysctl(t, "nf_conntrack_max", "131072")
	if ConntrackBelowFloor(readConntrackState(f.env)) {
		t.Errorf("131072 must not be below the floor")
	}
}

func (f *hostGuardFixture) seedJournald(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(f.env.journaldConfPath, []byte("[Journal]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(f.env.systemdRunDir, 0755); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureJournaldCapWritesDropinAndRestarts(t *testing.T) {
	f := newHostGuardFixture(t)
	f.seedJournald(t)

	res := ensureJournaldCap(f.env)
	if res.Failed || !res.Changed {
		t.Fatalf("expected changed success, got %+v", res)
	}
	content, err := os.ReadFile(f.env.journaldDropinPath)
	if err != nil {
		t.Fatalf("drop-in missing: %v", err)
	}
	// 4.8G disk → 5% ≈ 228M after MiB truncation.
	if !strings.Contains(string(content), "SystemMaxUse=228M") {
		t.Errorf("drop-in cap wrong:\n%s", content)
	}
	if !strings.Contains(string(content), "RuntimeMaxUse=64M") {
		t.Errorf("drop-in runtime cap wrong:\n%s", content)
	}
	if len(f.runArgs) != 1 || f.runArgs[0][0] != "systemctl" ||
		f.runArgs[0][1] != "restart" || f.runArgs[0][2] != "systemd-journald" {
		t.Fatalf("expected one journald restart, got %v", f.runArgs)
	}

	// Second run: in place, no restart again.
	res = ensureJournaldCap(f.env)
	if res.Changed || res.Failed {
		t.Fatalf("second run must be a no-op, got %+v", res)
	}
	if len(f.runArgs) != 1 {
		t.Fatalf("second run must not restart journald, got %v", f.runArgs)
	}
}

func TestEnsureJournaldCapTiers(t *testing.T) {
	cases := []struct {
		total uint64
		want  string
	}{
		{1000000000000, "SystemMaxUse=500M"}, // huge disk → ceiling
		{1000000000, "SystemMaxUse=100M"},    // tiny disk → floor
	}
	for _, tc := range cases {
		f := newHostGuardFixture(t)
		f.seedJournald(t)
		f.env.statfsTotal = func(string) (uint64, error) { return tc.total, nil }
		if res := ensureJournaldCap(f.env); res.Failed {
			t.Fatalf("total=%d: %v", tc.total, res.Lines)
		}
		content, _ := os.ReadFile(f.env.journaldDropinPath)
		if !strings.Contains(string(content), tc.want) {
			t.Errorf("total=%d: want %s in:\n%s", tc.total, tc.want, content)
		}
	}
	// statfs failure → field default 200M.
	f := newHostGuardFixture(t)
	f.seedJournald(t)
	f.env.statfsTotal = func(string) (uint64, error) { return 0, errors.New("boom") }
	if res := ensureJournaldCap(f.env); res.Failed {
		t.Fatalf("statfs failure must still cap at default: %v", res.Lines)
	}
	content, _ := os.ReadFile(f.env.journaldDropinPath)
	if !strings.Contains(string(content), "SystemMaxUse=200M") {
		t.Errorf("statfs failure must fall back to 200M:\n%s", content)
	}
}

func TestEnsureJournaldCapNoJournaldHost(t *testing.T) {
	f := newHostGuardFixture(t) // no journald.conf seeded
	res := ensureJournaldCap(f.env)
	if res.Failed || res.Changed {
		t.Fatalf("non-journald host must be a clean skip, got %+v", res)
	}
	if _, err := os.Stat(f.env.journaldDropinPath); !os.IsNotExist(err) {
		t.Errorf("no drop-in may be written on a non-journald host")
	}
}

func TestEnsureJournaldCapRestartFailureHonest(t *testing.T) {
	f := newHostGuardFixture(t)
	f.seedJournald(t)
	f.runErr = errors.New("unit busy")

	res := ensureJournaldCap(f.env)
	if !res.Failed {
		t.Fatalf("restart failure must surface, got %v", res.Lines)
	}
	joined := strings.Join(res.Lines, "\n")
	if !strings.Contains(joined, "已持久化") || !strings.Contains(joined, "失败") {
		t.Errorf("failure line must state persisted-but-not-live, got: %s", joined)
	}
	if _, err := os.Stat(f.env.journaldDropinPath); err != nil {
		t.Errorf("drop-in should still be persisted: %v", err)
	}
}
