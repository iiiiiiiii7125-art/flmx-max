package kernel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// postureStub models the live nft table for the R2-002 test: loads
// rebuild it (clearing any tamper), listings render it, and both the
// load failure and the out-of-band tamper are under the test's
// control. The tamper matters: the sync's content fingerprint
// short-circuits an unchanged, verified table, so a gate call only
// reaches the load — and can only fail — once the live content no
// longer matches.
type postureStub struct {
	failLoad bool
	tampered bool
	applied  bool
}

func stubPostureNft(t *testing.T) *postureStub {
	t.Helper()
	s := &postureStub{}
	oldRunner, oldAvail := nftRunner, nftAvailable
	nftRunner = func(_ context.Context, stdin string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case joined == "-f -":
			if s.failLoad {
				return nil, errors.New("nft load failed (stub)")
			}
			s.applied = true
			s.tampered = false
			return nil, nil
		case strings.HasPrefix(joined, "list table"):
			if !s.applied {
				return nil, errors.New("no table (stub)")
			}
			rule := "\t\ttcp dport 443 counter name \"in_7_tcp\"\n"
			if s.tampered {
				rule = ""
			}
			return []byte("table inet flmx_kernel {\n\tchain input {\n" + rule + "\t}\n}\n"), nil
		}
		return nil, fmt.Errorf("unexpected nft call %q", joined)
	}
	nftAvailable = func() error { return nil }
	t.Cleanup(func() { nftRunner, nftAvailable = oldRunner, oldAvail })
	return s
}

// Fix round 2 pinned tests (LEDGER2 R2-001 / R2-002 / R2-029 /
// R2-030). Each test encodes the mechanism the audit found broken;
// they fail on the pre-fix tree and pass after.

// R2-001: stopping a kernel must withdraw its nft rules. The desired
// state derivation used to ignore Enabled, so the stop sync rendered
// the identical ruleset (and usually short-circuited as "unchanged"),
// leaving the whitelist drop rules and counters of a stopped inbound
// in the table indefinitely.
func TestControlStopWithdrawsNftRules(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as fake kernel")
	}
	rec := stubNft(t)
	m := NewManager(t.TempDir())
	t.Cleanup(m.Shutdown)
	marker := filepath.Join(t.TempDir(), "starts")
	placeMarkerKernel(t, m, "xray", "9.9.9", "xray", marker)

	req := whitelistRequest("xray", "9.9.9", "xray", "cfg-v1", []string{"203.0.113.0/24"})
	req.UserMarks = []int64{42}
	if st, err := m.Apply(req); err != nil || !st.Running {
		t.Fatalf("apply: state=%+v err=%v", st, err)
	}
	last := rec.scripts[len(rec.scripts)-1]
	for _, want := range []string{"allow4_7", "in_7_tcp", "uup_42"} {
		if !strings.Contains(last, want) {
			t.Fatalf("running ruleset must contain %q:\n%s", want, last)
		}
	}
	before := len(rec.scripts)

	if _, err := m.Control("xray", "stop"); err != nil {
		t.Fatalf("control stop: %v", err)
	}
	if len(rec.scripts) <= before {
		t.Fatal("stop must load a reduced ruleset; the stopped kernel's rules were left in place")
	}
	last = rec.scripts[len(rec.scripts)-1]
	for _, gone := range []string{"allow4_7", "in_7_tcp", "uup_42", "dport 443"} {
		if strings.Contains(last, gone) {
			t.Fatalf("stopped kernel's rule %q must be withdrawn:\n%s", gone, last)
		}
	}
}

// R2-001, Apply surface: disabling via Apply (Enabled=false, ports
// still carried in the request) must likewise withdraw the rules,
// while other still-enabled kernels keep theirs.
func TestDisableApplyWithdrawsNftRules(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as fake kernel")
	}
	rec := stubNft(t)
	m := NewManager(t.TempDir())
	t.Cleanup(m.Shutdown)
	marker := filepath.Join(t.TempDir(), "starts")
	placeMarkerKernel(t, m, "xray", "9.9.9", "xray", marker)
	placeMarkerKernel(t, m, "singbox", "1.0.0", "sing-box", marker)

	wl := whitelistRequest("xray", "9.9.9", "xray", "cfg-v1", []string{"203.0.113.0/24"})
	if st, err := m.Apply(wl); err != nil || !st.Running {
		t.Fatalf("apply xray: state=%+v err=%v", st, err)
	}
	plain := whitelistRequest("singbox", "1.0.0", "sing-box", "cfg-sb", nil)
	plain.Ports = []Port{{InboundID: 9, Port: 8443, Network: "tcp"}}
	if st, err := m.Apply(plain); err != nil || !st.Running {
		t.Fatalf("apply singbox: state=%+v err=%v", st, err)
	}
	before := len(rec.scripts)

	off := whitelistRequest("xray", "9.9.9", "xray", "cfg-v2", []string{"203.0.113.0/24"})
	off.Enabled = false
	if st, err := m.Apply(off); err != nil || st.Running {
		t.Fatalf("disable apply: state=%+v err=%v", st, err)
	}
	if len(rec.scripts) <= before {
		t.Fatal("disabling must load a reduced ruleset; the disabled kernel's rules were left in place")
	}
	last := rec.scripts[len(rec.scripts)-1]
	for _, gone := range []string{"allow4_7", "in_7_tcp", "dport 443"} {
		if strings.Contains(last, gone) {
			t.Fatalf("disabled kernel's rule %q must be withdrawn:\n%s", gone, last)
		}
	}
	if !strings.Contains(last, "in_9_tcp") {
		t.Fatalf("the still-enabled kernel's counters must survive another kernel's disable:\n%s", last)
	}
}

// R2-002: the supervisor relaunch gate used to be bound to the
// posture at process creation. Adding a whitelist to a running
// kernel under an unchanged config hash does not restart the
// process, so the gate must be consulted against the instance's
// CURRENT posture at relaunch time: silent (nil) while unguarded,
// fail-closed once the whitelist lands, without any restart.
func TestWhitelistAddedSameHashArmsRelaunchGuard(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as fake kernel")
	}
	stub := stubPostureNft(t)
	m := NewManager(t.TempDir())
	t.Cleanup(m.Shutdown)
	marker := filepath.Join(t.TempDir(), "starts")
	placeMarkerKernel(t, m, "xray", "9.9.9", "xray", marker)

	v1 := whitelistRequest("xray", "9.9.9", "xray", "cfg-v1", nil)
	if st, err := m.Apply(v1); err != nil || !st.Running {
		t.Fatalf("v1 apply: state=%+v err=%v", st, err)
	}
	if got := markerStarts(t, marker); got != 1 {
		t.Fatalf("v1 must have started once, got %d", got)
	}
	m.mu.Lock()
	proc := m.instances["xray"].procs[0]
	m.mu.Unlock()
	if proc.relaunchGuard == nil {
		t.Fatal("every supervised process must carry the posture gate")
	}
	// Unguarded posture: the gate passes even while the table is
	// tampered and nft is broken — counting alone never blocks a
	// relaunch.
	stub.failLoad = true
	stub.tampered = true
	if err := proc.relaunchGuard(); err != nil {
		t.Fatalf("unguarded kernel's relaunch gate must pass, got %v", err)
	}

	// Same config (same hash), whitelist added: no restart, but the
	// very same process object must now be gated.
	v2 := whitelistRequest("xray", "9.9.9", "xray", "cfg-v1", []string{"203.0.113.0/24"})
	if v2.Hash != v1.Hash {
		t.Fatal("test premise broken: the whitelist must not change the config hash")
	}
	stub.failLoad = false
	if st, err := m.Apply(v2); err != nil || !st.Running {
		t.Fatalf("v2 apply: state=%+v err=%v", st, err)
	}
	if got := markerStarts(t, marker); got != 1 {
		t.Fatalf("same-hash apply must not restart the process, got %d starts", got)
	}
	m.mu.Lock()
	proc2 := m.instances["xray"].procs[0]
	m.mu.Unlock()
	if proc2 != proc {
		t.Fatal("test premise broken: the process object must be the surviving one")
	}
	// The table was edited out-of-band and cannot be rebuilt: the
	// surviving process's relaunch gate must now fail closed.
	stub.tampered = true
	stub.failLoad = true
	if err := proc.relaunchGuard(); err == nil {
		t.Fatal("after the whitelist lands, the surviving process's relaunch gate must fail closed while its rules cannot be installed")
	}
	stub.failLoad = false
	if err := proc.relaunchGuard(); err != nil {
		t.Fatalf("with a healthy sync the gate must pass again, got %v", err)
	}
}

// R2-029: Control start/restart discarded waitStable's result, so a
// kernel that died during the probe window still earned a success
// receipt (Apply reports the same event as an error). The Enabled
// flag stays persisted either way — the panel-driven retry and the
// Restore self-heal keep working; only the receipt changes.
func TestControlStartReportsStartFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as fake kernel")
	}
	stubWhitelistNft(t)
	m := NewManager(t.TempDir())
	t.Cleanup(m.Shutdown)
	marker := filepath.Join(t.TempDir(), "starts")
	placeMarkerKernel(t, m, "xray", "9.9.9", "xray", marker)

	req := whitelistRequest("xray", "9.9.9", "xray", "cfg-v1", nil)
	if st, err := m.Apply(req); err != nil || !st.Running {
		t.Fatalf("apply: state=%+v err=%v", st, err)
	}
	if _, err := m.Control("xray", "stop"); err != nil {
		t.Fatalf("control stop: %v", err)
	}

	// The binary breaks while the kernel is down (bad upgrade on
	// disk): the next start launches a process that exits at once.
	bin := filepath.Join(m.binDir("xray", "9.9.9"), "xray")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := m.Control("xray", "start")
	if err == nil || !strings.Contains(err.Error(), "内核启动失败") {
		t.Fatalf("control start of a kernel that dies in the probe window must return the start failure, got state=%+v err=%v", st, err)
	}
	if !st.Enabled {
		t.Fatal("Enabled must stay persisted so Restore retries the start (self-heal semantics unchanged)")
	}
}
