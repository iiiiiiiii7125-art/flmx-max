package kernel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Tests for the A-02 / A-03 fix round: the B-02 fail-closed predicate
// ("a guarded kernel must never listen while its rules are not
// installed") is extended to the two restart surfaces that bypassed
// it — Apply's rollback and the internal restarts (geodata / rule-set
// refresh, supervisor relaunch) — and the nft sync's skip shortcut
// now verifies live table content instead of existence alone.

// placeCrashKernel pre-installs a fake kernel binary that records
// every launch in marker, distinguishing launches on a working config
// ("start-ok") from launches on a crashing one ("start-crash"), so a
// test can prove a rollback start did or did not happen even though
// the supervisor keeps relaunching the crashing process.
func placeCrashKernel(t *testing.T, m *Manager, kernelType, version, binName, marker string) {
	t.Helper()
	binDir := m.binDir(kernelType, version)
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"version\" ] || [ \"$1\" = \"-v\" ]; then echo \"" + kernelType + " " + version + " (fake)\"; exit 0; fi\n" +
		"if grep -q crash \"$3\"; then echo start-crash >> \"" + marker + "\"; echo \"config error\"; exit 1; fi\n" +
		"echo start-ok >> \"" + marker + "\"\n" +
		"exec sleep 300\n"
	bin := filepath.Join(binDir, binName)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
}

func countMarkerLines(t *testing.T, marker, token string) int {
	t.Helper()
	b, err := os.ReadFile(marker)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(b), token+"\n")
}

// A-02: the new config is unguarded and crashes; the previous config
// is guarded and known-good; the ruleset sync fails. Baseline rolled
// back into the guarded config anyway — starting it unguarded for
// the probe window before the post-apply sweep stopped it again.
// The rollback must now be skipped: the kernel stays down and the
// error is the guard failure, not a start failure.
func TestApplyRollbackSkippedWhenPrevGuardedAndSyncFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as fake kernel")
	}
	stub := stubWhitelistNft(t)
	m := NewManager(t.TempDir())
	t.Cleanup(m.Shutdown)
	marker := filepath.Join(t.TempDir(), "starts")
	placeCrashKernel(t, m, "xray", "9.9.9", "xray", marker)

	v1 := whitelistRequest("xray", "9.9.9", "xray", "cfg-v1", []string{"203.0.113.0/24"})
	st, err := m.Apply(v1)
	if err != nil || !st.Running {
		t.Fatalf("v1 apply: state=%+v err=%v", st, err)
	}
	if got := countMarkerLines(t, marker, "start-ok"); got != 1 {
		t.Fatalf("v1 must have started once, got %d", got)
	}

	stub.failLoad = true
	v2 := whitelistRequest("xray", "9.9.9", "xray", `{"crash":true}`, nil)
	st, err = m.Apply(v2)
	if err == nil || !strings.Contains(err.Error(), "白名单") {
		t.Fatalf("apply must fail closed with the guard error, got state=%+v err=%v", st, err)
	}
	if st.Running {
		t.Fatal("kernel must stay down: no unguarded rollback start")
	}
	if got := countMarkerLines(t, marker, "start-ok"); got != 1 {
		t.Fatalf("the guarded previous config must never be restarted, got %d working starts", got)
	}
	cfg, _ := os.ReadFile(filepath.Join(m.runDir("xray"), "config.json"))
	if string(cfg) != `{"crash":true}` {
		t.Fatalf("rollback must not restore the previous files, config = %q", cfg)
	}
}

// A-02 control: same shape, but the sync succeeds — the rollback into
// the guarded previous config is legitimate and must still happen.
func TestApplyRollbackStillRunsWhenPrevGuardedAndSyncOK(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as fake kernel")
	}
	stubWhitelistNft(t)
	m := NewManager(t.TempDir())
	t.Cleanup(m.Shutdown)
	marker := filepath.Join(t.TempDir(), "starts")
	placeCrashKernel(t, m, "xray", "9.9.9", "xray", marker)

	v1 := whitelistRequest("xray", "9.9.9", "xray", "cfg-v1", []string{"203.0.113.0/24"})
	if st, err := m.Apply(v1); err != nil || !st.Running {
		t.Fatalf("v1 apply: state=%+v err=%v", st, err)
	}

	v2 := whitelistRequest("xray", "9.9.9", "xray", `{"crash":true}`, nil)
	st, err := m.Apply(v2)
	if err == nil || !strings.Contains(err.Error(), "已恢复") {
		t.Fatalf("crashing unguarded config must roll back when the sync is healthy, got state=%+v err=%v", st, err)
	}
	if !st.Running {
		t.Fatalf("rolled-back kernel must be running: %+v", st)
	}
	if got := countMarkerLines(t, marker, "start-ok"); got != 2 {
		t.Fatalf("rollback must restart the previous config once, got %d working starts", got)
	}
	cfg, _ := os.ReadFile(filepath.Join(m.runDir("xray"), "config.json"))
	if string(cfg) != "cfg-v1" {
		t.Fatalf("rollback must restore the previous files, config = %q", cfg)
	}
}

// A-02 boundary: previous config unguarded and the sync failing does
// not block the rollback — only a guarded rollback target is skipped.
func TestApplyRollbackStillRunsWhenPrevUnguardedAndSyncFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as fake kernel")
	}
	stub := stubWhitelistNft(t)
	stub.failLoad = true // counting sync fails from the start; unguarded kernels tolerate it
	m := NewManager(t.TempDir())
	t.Cleanup(m.Shutdown)
	marker := filepath.Join(t.TempDir(), "starts")
	placeCrashKernel(t, m, "xray", "9.9.9", "xray", marker)

	v1 := whitelistRequest("xray", "9.9.9", "xray", "cfg-v1", nil)
	if st, err := m.Apply(v1); err != nil || !st.Running {
		t.Fatalf("v1 apply: state=%+v err=%v", st, err)
	}

	v2 := whitelistRequest("xray", "9.9.9", "xray", `{"crash":true}`, nil)
	st, err := m.Apply(v2)
	if err == nil || !strings.Contains(err.Error(), "已恢复") {
		t.Fatalf("unguarded previous config must still roll back under a failed sync, got state=%+v err=%v", st, err)
	}
	if !st.Running {
		t.Fatalf("rolled-back kernel must be running: %+v", st)
	}
	if got := countMarkerLines(t, marker, "start-ok"); got != 2 {
		t.Fatalf("rollback must restart the previous config once, got %d working starts", got)
	}
}

// liveTableStub models a kernel nft table whose rendered content the
// test controls: loads rebuild it wholesale (clearing any tamper),
// listings render it — or fail once the table is flushed externally.
type liveTableStub struct {
	applied  bool
	gone     bool
	tampered bool
	packets  int
	loads    int
}

const liveTableBody = "table inet flmx_kernel {\n" +
	"\tcounter in_7_tcp {\n" +
	"\t\tpackets %d bytes %d\n" +
	"\t}\n" +
	"\tchain input {\n" +
	"\t\ttype filter hook input priority -150; policy accept;\n" +
	"\t\ttcp dport 443 counter name \"in_7_tcp\"\n" +
	"\t}\n" +
	"}\n"

// liveTableTampered is the same table with the guard rule edited out
// in place: the table exists, its content no longer matches.
const liveTableTampered = "table inet flmx_kernel {\n" +
	"\tcounter in_7_tcp {\n" +
	"\t\tpackets %d bytes %d\n" +
	"\t}\n" +
	"\tchain input {\n" +
	"\t\ttype filter hook input priority -150; policy accept;\n" +
	"\t}\n" +
	"}\n"

func stubLiveTableNft(t *testing.T) *liveTableStub {
	t.Helper()
	s := &liveTableStub{}
	oldRunner, oldAvail := nftRunner, nftAvailable
	nftRunner = func(_ context.Context, stdin string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case joined == "-f -":
			s.loads++
			s.applied = true
			s.gone = false
			s.tampered = false
			return nil, nil
		case strings.HasPrefix(joined, "list table"):
			if !s.applied || s.gone {
				return nil, errors.New("no table (stub)")
			}
			body := liveTableBody
			if s.tampered {
				body = liveTableTampered
			}
			return []byte(fmt.Sprintf(body, s.packets, s.packets*10)), nil
		}
		return nil, fmt.Errorf("unexpected nft call %q", joined)
	}
	nftAvailable = func() error { return nil }
	t.Cleanup(func() { nftRunner, nftAvailable = oldRunner, oldAvail })
	return s
}

var guardSyncPorts = []Port{{InboundID: 7, Port: 443, Network: "tcp"}}

// A-03: the sync shortcut used to trust table existence. A table
// whose content was edited out-of-band must be detected and rebuilt,
// and once rebuilt the shortcut must engage again.
func TestSyncGuardReappliesWhenTableContentTampered(t *testing.T) {
	stub := stubLiveTableNft(t)
	n := newNftCounters(t.TempDir())
	if err := n.sync(guardSyncPorts, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if stub.loads != 1 {
		t.Fatalf("first sync must apply once, got %d loads", stub.loads)
	}
	if err := n.sync(guardSyncPorts, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if stub.loads != 1 {
		t.Fatalf("unchanged content must short-circuit, got %d loads", stub.loads)
	}
	stub.tampered = true
	if err := n.sync(guardSyncPorts, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if stub.loads != 2 {
		t.Fatalf("tampered table content must force a re-apply, got %d loads", stub.loads)
	}
	if err := n.sync(guardSyncPorts, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if stub.loads != 2 {
		t.Fatalf("rebuilt table must short-circuit again, got %d loads", stub.loads)
	}
}

// A-03: counter values move with traffic; they are not content. A
// busier table must still verify as unchanged.
func TestSyncGuardIgnoresCounterValueChanges(t *testing.T) {
	stub := stubLiveTableNft(t)
	n := newNftCounters(t.TempDir())
	if err := n.sync(guardSyncPorts, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}
	stub.packets = 987654
	if err := n.sync(guardSyncPorts, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if stub.loads != 1 {
		t.Fatalf("counter value changes must not force a re-apply, got %d loads", stub.loads)
	}
}

// A-03: a flushed table is rebuilt even though the signature is
// unchanged (this already fell through on the old existence check;
// pinned here alongside the content checks).
func TestSyncGuardReappliesWhenTableGone(t *testing.T) {
	stub := stubLiveTableNft(t)
	n := newNftCounters(t.TempDir())
	if err := n.sync(guardSyncPorts, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}
	stub.gone = true
	if err := n.sync(guardSyncPorts, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if stub.loads != 2 {
		t.Fatalf("a flushed table must be rebuilt, got %d loads", stub.loads)
	}
}

// A-03: state files written before content fingerprints existed hold
// a signature but no fingerprint. The first sync under the new code
// must re-apply once to arm the fingerprint, then short-circuit.
func TestSyncGuardRearmsFingerprintForLegacyState(t *testing.T) {
	stub := stubLiveTableNft(t)
	dir := t.TempDir()
	sig := hashString(nftScriptGuard(expandPorts(guardSyncPorts), expandGuards(guardSyncPorts), expandHops(guardSyncPorts), sortedUsers(nil), nil))
	legacy := `{"signature":"` + sig + `","epoch":"legacy"}`
	if err := os.WriteFile(filepath.Join(dir, "nft.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	n := newNftCounters(dir)
	if err := n.sync(guardSyncPorts, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if stub.loads != 1 {
		t.Fatalf("legacy state must re-apply once to arm the fingerprint, got %d loads", stub.loads)
	}
	if err := n.sync(guardSyncPorts, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if stub.loads != 1 {
		t.Fatalf("armed fingerprint must short-circuit, got %d loads", stub.loads)
	}
}

// A-03: the supervisor relaunch of a guarded process is gated — while
// the gate fails, the process runs exactly once (its first launch)
// and the gate is consulted on every backoff round.
func TestProcessRelaunchGuardBlocksRestart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as fake process")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "runs")
	script := filepath.Join(dir, "crasher.sh")
	body := "#!/bin/sh\necho run >> \"" + marker + "\"\nexit 1\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	p := newProcess(script, nil, dir, &logRing{})
	var gateCalls atomic.Int32
	p.relaunchGuard = func() error {
		gateCalls.Add(1)
		return errors.New("rules not ready (stub)")
	}
	p.start()
	defer p.stop()
	time.Sleep(4 * time.Second)
	if got := countMarkerLines(t, marker, "run"); got != 1 {
		t.Fatalf("gated process must launch exactly once, got %d", got)
	}
	if got := gateCalls.Load(); got < 2 {
		t.Fatalf("gate must be consulted on every relaunch round, got %d calls", got)
	}
}

// A-03: once the gate passes again, the supervisor relaunch resumes
// on its own — the gate defers restarts, it does not wedge them.
func TestProcessRelaunchGuardPassesAfterRecovery(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as fake process")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "runs")
	script := filepath.Join(dir, "crasher.sh")
	body := "#!/bin/sh\necho run >> \"" + marker + "\"\nexit 1\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	p := newProcess(script, nil, dir, &logRing{})
	var calls atomic.Int32
	p.relaunchGuard = func() error {
		if calls.Add(1) == 1 {
			return errors.New("rules not ready (stub)")
		}
		return nil
	}
	p.start()
	defer p.stop()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if countMarkerLines(t, marker, "run") >= 2 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("process must relaunch once the gate passes, runs=%d", countMarkerLines(t, marker, "run"))
}

// A-03 wiring: startProcesses attaches the relaunch gate to guarded
// instances' processes only; unguarded processes keep the bare
// supervisor behavior.
func TestGuardedProcessesCarryRelaunchGuard(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as fake kernel")
	}
	stubWhitelistNft(t)
	m := NewManager(t.TempDir())
	t.Cleanup(m.Shutdown)
	marker := filepath.Join(t.TempDir(), "starts")
	placeMarkerKernel(t, m, "xray", "9.9.9", "xray", marker)
	placeMarkerKernel(t, m, "singbox", "1.0.0", "sing-box", marker)

	guarded := whitelistRequest("xray", "9.9.9", "xray", "cfg-v1", []string{"203.0.113.0/24"})
	if st, err := m.Apply(guarded); err != nil || !st.Running {
		t.Fatalf("guarded apply: state=%+v err=%v", st, err)
	}
	m.mu.Lock()
	gprocs := m.instances["xray"].procs
	m.mu.Unlock()
	if len(gprocs) == 0 || gprocs[0].relaunchGuard == nil {
		t.Fatal("guarded kernel processes must carry the relaunch guard")
	}

	plain := whitelistRequest("singbox", "1.0.0", "sing-box", "cfg-v1", nil)
	plain.Ports = []Port{{InboundID: 9, Port: 8443, Network: "tcp"}}
	if st, err := m.Apply(plain); err != nil || !st.Running {
		t.Fatalf("plain apply: state=%+v err=%v", st, err)
	}
	m.mu.Lock()
	pprocs := m.instances["singbox"].procs
	m.mu.Unlock()
	if len(pprocs) == 0 || pprocs[0].relaunchGuard != nil {
		t.Fatal("unguarded kernel processes must not carry the relaunch guard")
	}
}

// A-03: a geodata refresh must not restart a guarded kernel while
// its rules cannot be installed. The files still swap (they take
// effect at the next start); the running process keeps serving the
// data it already loaded.
func TestSwapGeoFilesSkipsRestartWhenGuardSyncFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as fake kernel")
	}
	stub := stubWhitelistNft(t)
	m := NewManager(t.TempDir())
	t.Cleanup(m.Shutdown)
	marker := filepath.Join(t.TempDir(), "starts")
	placeMarkerKernel(t, m, "xray", "9.9.9", "xray", marker)

	req := whitelistRequest("xray", "9.9.9", "xray", "cfg-v1", []string{"203.0.113.0/24"})
	if st, err := m.Apply(req); err != nil || !st.Running {
		t.Fatalf("apply: state=%+v err=%v", st, err)
	}
	binDir := m.binDir("xray", "9.9.9")
	if err := os.WriteFile(filepath.Join(binDir, "geosite.dat"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "geosite.dat.new"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The table is gone and cannot be rebuilt: the restart gate must
	// refuse, exactly like Control start would.
	stub.tableGone = true
	stub.failLoad = true
	m.swapGeoFiles(geoJob{kernelType: "xray", binDir: binDir}, []string{"geosite.dat"})

	if got := markerStarts(t, marker); got != 1 {
		t.Fatalf("guarded kernel must not restart under a failed sync, got %d starts", got)
	}
	if st := m.state("xray", false); !st.Running {
		t.Fatalf("the running process must be left alone when the restart is skipped: %+v", st)
	}
	b, err := os.ReadFile(filepath.Join(binDir, "geosite.dat"))
	if err != nil || string(b) != "new" {
		t.Fatalf("the data files must still swap, got %q err=%v", b, err)
	}
	m.mu.Lock()
	inst := m.instances["xray"]
	m.mu.Unlock()
	if logs := strings.Join(inst.logs.tail(20), "\n"); !strings.Contains(logs, "未重启") {
		t.Fatalf("the skipped restart must be logged on the instance, logs:\n%s", logs)
	}
}

// A-03 control: with a healthy sync the same refresh restarts the
// guarded kernel as before.
func TestSwapGeoFilesRestartsGuardedKernelWhenSyncOK(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as fake kernel")
	}
	stubWhitelistNft(t)
	m := NewManager(t.TempDir())
	t.Cleanup(m.Shutdown)
	marker := filepath.Join(t.TempDir(), "starts")
	placeMarkerKernel(t, m, "xray", "9.9.9", "xray", marker)

	req := whitelistRequest("xray", "9.9.9", "xray", "cfg-v1", []string{"203.0.113.0/24"})
	if st, err := m.Apply(req); err != nil || !st.Running {
		t.Fatalf("apply: state=%+v err=%v", st, err)
	}
	binDir := m.binDir("xray", "9.9.9")
	if err := os.WriteFile(filepath.Join(binDir, "geosite.dat"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "geosite.dat.new"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}

	m.swapGeoFiles(geoJob{kernelType: "xray", binDir: binDir}, []string{"geosite.dat"})

	if got := markerStarts(t, marker); got != 2 {
		t.Fatalf("guarded kernel must restart once on a healthy refresh, got %d starts", got)
	}
	if st := m.state("xray", false); !st.Running {
		t.Fatalf("kernel must be running after the refresh restart: %+v", st)
	}
	b, err := os.ReadFile(filepath.Join(binDir, "geosite.dat"))
	if err != nil || string(b) != "new" {
		t.Fatalf("the data files must swap, got %q err=%v", b, err)
	}
}

// A-03: the rule-set refresh carries the same gate as geodata.
func TestSwapRuleSetsSkipsRestartWhenGuardSyncFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as fake kernel")
	}
	stub := stubWhitelistNft(t)
	m := NewManager(t.TempDir())
	t.Cleanup(m.Shutdown)
	marker := filepath.Join(t.TempDir(), "starts")
	placeMarkerKernel(t, m, "singbox", "1.0.0", "sing-box", marker)

	req := whitelistRequest("singbox", "1.0.0", "sing-box", "cfg-v1", []string{"203.0.113.0/24"})
	if st, err := m.Apply(req); err != nil || !st.Running {
		t.Fatalf("apply: state=%+v err=%v", st, err)
	}
	dir := filepath.Join(m.runDir("singbox"), ruleSetDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	s := &remoteRuleSet{tag: "geo", directURL: "https://example.invalid/geo.srs"}
	path := s.localPath(dir)
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".new", []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}

	stub.tableGone = true
	stub.failLoad = true
	job := ruleSetJob{kernelType: "singbox", configHash: req.Hash, dir: dir, sets: []*remoteRuleSet{s}}
	m.swapRuleSets(job, []*remoteRuleSet{s})

	if got := markerStarts(t, marker); got != 1 {
		t.Fatalf("guarded kernel must not restart under a failed sync, got %d starts", got)
	}
	if st := m.state("singbox", false); !st.Running {
		t.Fatalf("the running process must be left alone when the restart is skipped: %+v", st)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "new" {
		t.Fatalf("the rule-set files must still swap, got %q err=%v", b, err)
	}
	m.mu.Lock()
	inst := m.instances["singbox"]
	m.mu.Unlock()
	if logs := strings.Join(inst.logs.tail(20), "\n"); !strings.Contains(logs, "未重启") {
		t.Fatalf("the skipped restart must be logged on the instance, logs:\n%s", logs)
	}
}
