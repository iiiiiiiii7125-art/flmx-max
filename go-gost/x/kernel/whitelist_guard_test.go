package kernel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Inbound whitelist fail-closed tests. A whitelisted port is only
// protected while its nft rules are installed, so a whitelist kernel
// must never run when the ruleset sync fails or nftables is missing,
// and a failed sync must also stop guarded kernels that were already
// running.

type whitelistNftStub struct {
	failLoad      bool // script loads ("-f -") fail
	failLoadAfter int  // fail script loads once this many loads already succeeded (0 = disabled)
	tableGone     bool // "list table" fails even after a successful load
	available     bool
	scripts       []string
	// scriptTimes records when each applied script landed, in the
	// same order as scripts (R18-R2-D3-02: proves a re-sync
	// happened before — not after — the process it guards).
	scriptTimes []time.Time
	// scopeTable, when set, confines the stub's budget and script
	// record to loads for one nft table (R18-R2-D3-02-scope).
	// The kernel package runs many managers in one process, and
	// leaked background syncs — verify-loop ticks, supervisor
	// relaunch guards, geo/rule-set restarts of managers from
	// earlier tests — legitimately call the global nftRunner while
	// a later test's stub is live. Counting those foreign loads
	// against this test's failLoadAfter budget (or in its script
	// record) makes count-sensitive tests fail whenever one lands
	// in their window; the budget must count only the manager
	// under test. Unset means the old global counting.
	scopeTable string
}

// scopeToTable confines the stub to one manager's nft table: loads
// for any other table still succeed (the other manager's sync is
// unaffected) but consume no budget and leave no record here. The
// table name comes from the manager under test
// (m.nft.tableName()); it is stable for the manager's lifetime
// (derived from its baseDir).
func (s *whitelistNftStub) scopeToTable(table string) {
	s.scopeTable = table
}

// scriptConcernsTable reports whether an nft script installs or
// deletes the named table. The match is on the exact "table inet
// <name>" header token, so flmx_kernel_80070693 never matches
// flmx_kernel_204f7d22 or the plain flmx_kernel.
func scriptConcernsTable(script, table string) bool {
	if table == "" {
		return true
	}
	return strings.Contains(script, "table inet "+table+" {") ||
		strings.Contains(script, "table inet "+table+"\n")
}

func stubWhitelistNft(t *testing.T) *whitelistNftStub {
	t.Helper()
	s := &whitelistNftStub{available: true}
	oldRunner, oldAvail, oldInstall := nftRunner, nftAvailable, installRunner
	nftRunner = func(_ context.Context, stdin string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case joined == "-f -":
			// Scoped stubs ignore foreign-table loads: they are
			// other managers' background syncs, not this test's.
			if s.scopeTable != "" && !scriptConcernsTable(stdin, s.scopeTable) {
				return nil, nil
			}
			if s.failLoad || (s.failLoadAfter > 0 && len(s.scripts) >= s.failLoadAfter) {
				return nil, errors.New("nft load failed (stub)")
			}
			s.scripts = append(s.scripts, stdin)
			s.scriptTimes = append(s.scriptTimes, time.Now())
			return nil, nil
		case strings.HasPrefix(joined, "list table"):
			if s.tableGone || len(s.scripts) == 0 {
				return nil, errors.New("no table (stub)")
			}
			return []byte("table inet flmx_kernel {}"), nil
		}
		return nil, fmt.Errorf("unexpected nft call %q", joined)
	}
	nftAvailable = func() error {
		if !s.available {
			return errors.New("nft 不存在 (stub)")
		}
		return nil
	}
	// 自动安装桩：本文件断言的是 fail-closed，安装器不得在测试里
	// 触碰真实包管理器——一律返回失败，让缺 nft 的用例走明错分支。
	installRunner = func(_ context.Context, name string, args ...string) ([]byte, error) {
		return nil, fmt.Errorf("install disabled in test (stub): %s %s", name, strings.Join(args, " "))
	}
	t.Cleanup(func() { nftRunner, nftAvailable, installRunner = oldRunner, oldAvail, oldInstall })
	return s
}

// placeMarkerKernel pre-installs a fake kernel binary that appends a
// line to marker every time it is actually launched as a server, so a
// test can prove a blocked kernel was never started at all (as opposed
// to started and then stopped again).
func placeMarkerKernel(t *testing.T, m *Manager, kernelType, version, binName, marker string) {
	t.Helper()
	binDir := m.binDir(kernelType, version)
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"version\" ] || [ \"$1\" = \"-v\" ]; then echo \"" + kernelType + " " + version + " (fake)\"; exit 0; fi\n" +
		"echo start >> \"" + marker + "\"\n" +
		"exec sleep 300\n"
	bin := filepath.Join(binDir, binName)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
}

func markerStarts(t *testing.T, marker string) int {
	t.Helper()
	b, err := os.ReadFile(marker)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(b), "start\n")
}

func whitelistRequest(kernelType, version, binName, config string, allow []string) ApplyRequest {
	return ApplyRequest{
		Type:    kernelType,
		Version: version,
		Enabled: true,
		Download: &Download{
			URL:     "https://example.invalid/kernel.bin",
			BinName: binName,
			Archive: "raw",
		},
		Files: []File{{Name: "config.json", Content: config}},
		Args:  []string{"run", "-c", "{RUN_DIR}/config.json"},
		Hash:  hashString(config),
		Ports: []Port{{InboundID: 7, Port: 443, Network: "tcp", Allow: allow}},
	}
}

func TestReqNeedsNftGuard(t *testing.T) {
	plain := ApplyRequest{Type: "xray", Enabled: true, Ports: []Port{{InboundID: 1, Port: 443}}}
	if reqNeedsNftGuard(plain) {
		t.Fatal("plain kernel must not need the nft guard")
	}
	whitelisted := plain
	whitelisted.Ports = []Port{{InboundID: 1, Port: 443, Allow: []string{"203.0.113.0/24"}}}
	if !reqNeedsNftGuard(whitelisted) {
		t.Fatal("whitelisted kernel must need the nft guard")
	}
	disabled := whitelisted
	disabled.Enabled = false
	if reqNeedsNftGuard(disabled) {
		t.Fatal("disabled kernel runs nothing and needs no guard")
	}
}

func TestApplyWhitelistFailsClosedOnSyncError(t *testing.T) {
	stub := stubWhitelistNft(t)
	stub.failLoad = true
	m := NewManager(t.TempDir())
	t.Cleanup(m.Shutdown)
	marker := filepath.Join(t.TempDir(), "starts")
	placeMarkerKernel(t, m, "xray", "9.9.9", "xray", marker)

	req := whitelistRequest("xray", "9.9.9", "xray", "cfg-v1", []string{"203.0.113.0/24"})
	st, err := m.Apply(req)
	if err == nil || !strings.Contains(err.Error(), "白名单") {
		t.Fatalf("whitelist apply with failing nft sync must fail closed, got state=%+v err=%v", st, err)
	}
	if st.Running {
		t.Fatal("whitelisted kernel must not run while its rules are not installed")
	}
	if got := markerStarts(t, marker); got != 0 {
		t.Fatalf("kernel must never start before its rules are installed, got %d starts", got)
	}
	m.mu.Lock()
	n := len(m.instances["xray"].procs)
	m.mu.Unlock()
	if n != 0 {
		t.Fatal("no process may survive a fail-closed apply")
	}
}

func TestApplyWhitelistFailsClosedWithoutNft(t *testing.T) {
	stub := stubWhitelistNft(t)
	stub.available = false
	m := NewManager(t.TempDir())
	t.Cleanup(m.Shutdown)
	marker := filepath.Join(t.TempDir(), "starts")
	placeMarkerKernel(t, m, "xray", "9.9.9", "xray", marker)

	req := whitelistRequest("xray", "9.9.9", "xray", "cfg-v1", []string{"203.0.113.0/24"})
	st, err := m.Apply(req)
	if err == nil || !strings.Contains(err.Error(), "nftables") {
		t.Fatalf("whitelist apply without nftables must fail closed, got state=%+v err=%v", st, err)
	}
	if st.Running {
		t.Fatal("whitelisted kernel must not run without nftables")
	}
	if got := markerStarts(t, marker); got != 0 {
		t.Fatalf("kernel must never start without nftables, got %d starts", got)
	}
}

func TestApplyWithoutWhitelistRunsDespiteSyncFailure(t *testing.T) {
	stub := stubWhitelistNft(t)
	stub.failLoad = true
	m := NewManager(t.TempDir())
	t.Cleanup(m.Shutdown)
	marker := filepath.Join(t.TempDir(), "starts")
	placeMarkerKernel(t, m, "xray", "9.9.9", "xray", marker)

	req := whitelistRequest("xray", "9.9.9", "xray", "cfg-v1", nil)
	st, err := m.Apply(req)
	if err != nil {
		t.Fatalf("unprotected kernel must tolerate a counting sync failure: %v", err)
	}
	if !st.Running {
		t.Fatalf("unprotected kernel should be running: %+v", st)
	}
	if got := markerStarts(t, marker); got != 1 {
		t.Fatalf("unprotected kernel should have started once, got %d", got)
	}
}

func TestWhitelistSyncFailureStopsOtherGuardedKernel(t *testing.T) {
	stub := stubWhitelistNft(t)
	m := NewManager(t.TempDir())
	t.Cleanup(m.Shutdown)
	marker := filepath.Join(t.TempDir(), "starts")
	placeMarkerKernel(t, m, "xray", "9.9.9", "xray", marker)
	placeMarkerKernel(t, m, "singbox", "1.0.0", "sing-box", marker)

	wl := whitelistRequest("xray", "9.9.9", "xray", "cfg-wl", []string{"203.0.113.0/24"})
	if st, err := m.Apply(wl); err != nil || !st.Running {
		t.Fatalf("whitelist apply with working nft: state=%+v err=%v", st, err)
	}

	// The next table generation fails to load: the whitelisted xray
	// loses its ruleset and must stop, while the unprotected sing-box
	// that triggered the sync still comes up (counting is degraded,
	// nothing else).
	stub.failLoad = true
	plain := whitelistRequest("singbox", "1.0.0", "sing-box", "cfg-sb", nil)
	plain.Ports = []Port{{InboundID: 9, Port: 8443, Network: "tcp"}}
	st, err := m.Apply(plain)
	if err != nil {
		t.Fatalf("unprotected apply must not fail on a counting sync error: %v", err)
	}
	if !st.Running {
		t.Fatalf("unprotected kernel should be running: %+v", st)
	}
	xst := m.state("xray", false)
	if xst.Running {
		t.Fatal("whitelisted kernel must stop when the sync carrying its rules fails")
	}
	if !strings.Contains(xst.Error, "白名单") {
		t.Fatalf("whitelisted kernel must carry the fail-closed error, got %q", xst.Error)
	}
}

func TestWhitelistSyncFailureDoesNotRollBack(t *testing.T) {
	stub := stubWhitelistNft(t)
	m := NewManager(t.TempDir())
	t.Cleanup(m.Shutdown)
	marker := filepath.Join(t.TempDir(), "starts")
	placeMarkerKernel(t, m, "xray", "9.9.9", "xray", marker)

	v1 := whitelistRequest("xray", "9.9.9", "xray", "cfg-v1", []string{"203.0.113.0/24"})
	if st, err := m.Apply(v1); err != nil || !st.Running {
		t.Fatalf("v1 apply: state=%+v err=%v", st, err)
	}
	if got := markerStarts(t, marker); got != 1 {
		t.Fatalf("v1 should have started once, got %d", got)
	}

	// A new config whose ruleset (new whitelist) cannot be installed:
	// the kernel must go down, not fall back to serving the previous
	// configuration under a failed apply.
	stub.failLoad = true
	v2 := whitelistRequest("xray", "9.9.9", "xray", "cfg-v2", []string{"198.51.100.0/24"})
	st, err := m.Apply(v2)
	if err == nil || !strings.Contains(err.Error(), "白名单") {
		t.Fatalf("v2 apply must fail closed, got state=%+v err=%v", st, err)
	}
	if st.Running {
		t.Fatal("kernel must not serve the previous config after a fail-closed apply")
	}
	if got := markerStarts(t, marker); got != 1 {
		t.Fatalf("no rollback start may happen under a failed sync, got %d starts", got)
	}
}

func TestControlWhitelistStartFailsClosed(t *testing.T) {
	stub := stubWhitelistNft(t)
	m := NewManager(t.TempDir())
	t.Cleanup(m.Shutdown)
	marker := filepath.Join(t.TempDir(), "starts")
	placeMarkerKernel(t, m, "xray", "9.9.9", "xray", marker)

	req := whitelistRequest("xray", "9.9.9", "xray", "cfg-v1", []string{"203.0.113.0/24"})
	if st, err := m.Apply(req); err != nil || !st.Running {
		t.Fatalf("apply: state=%+v err=%v", st, err)
	}
	if _, err := m.Control("xray", "stop"); err != nil {
		t.Fatalf("control stop: %v", err)
	}

	// The table is gone and the reload fails: start must fail closed
	// and the kernel must stay down.
	stub.tableGone = true
	stub.failLoad = true
	st, err := m.Control("xray", "start")
	if err == nil || !strings.Contains(err.Error(), "白名单") {
		t.Fatalf("control start with failing nft sync must fail closed, got state=%+v err=%v", st, err)
	}
	if st.Running {
		t.Fatal("whitelisted kernel must stay down when its rules cannot be installed")
	}
	if got := markerStarts(t, marker); got != 1 {
		t.Fatalf("kernel must not start before its rules are installed, got %d starts", got)
	}
}

// TestStubScopeIgnoresForeignTableLoads (R18-R2-D3-02-scope):
// a scoped stub spends its failLoadAfter budget only on loads for
// its own table. Loads for another manager's table — the shape a
// leaked verify tick / supervisor relaunch guard / geo restart
// from an earlier test takes while this stub is live — succeed
// (the other manager's sync is unaffected) but consume no budget
// and leave no record, so count-sensitive tests cannot be moved
// by background syncs they do not own.
func TestStubScopeIgnoresForeignTableLoads(t *testing.T) {
	stub := stubWhitelistNft(t)
	stub.failLoadAfter = 1
	stub.scopeToTable("flmx_kernel_aaaaaaaa")
	ctx := context.Background()
	foreign := "table inet flmx_kernel_bbbbbbbb {}\ndelete table inet flmx_kernel_bbbbbbbb\ntable inet flmx_kernel_bbbbbbbb {\n}\n"
	own := "table inet flmx_kernel_aaaaaaaa {}\ndelete table inet flmx_kernel_aaaaaaaa\ntable inet flmx_kernel_aaaaaaaa {\n}\n"

	if _, err := nftRunner(ctx, foreign, "-f", "-"); err != nil {
		t.Fatalf("foreign-table load must succeed, got %v", err)
	}
	if len(stub.scripts) != 0 {
		t.Fatalf("foreign-table load must not be recorded, got %d scripts", len(stub.scripts))
	}
	if _, err := nftRunner(ctx, own, "-f", "-"); err != nil {
		t.Fatalf("first own-table load must succeed, got %v", err)
	}
	if len(stub.scripts) != 1 {
		t.Fatalf("own-table load must be recorded, got %d scripts", len(stub.scripts))
	}
	if _, err := nftRunner(ctx, foreign, "-f", "-"); err != nil {
		t.Fatalf("foreign-table load must still succeed after the budget is spent, got %v", err)
	}
	if _, err := nftRunner(ctx, own, "-f", "-"); err == nil {
		t.Fatal("second own-table load must fail once failLoadAfter is spent")
	}
	if len(stub.scripts) != 1 {
		t.Fatalf("failed own-table load must not append, got %d scripts", len(stub.scripts))
	}
	if len(stub.scriptTimes) != len(stub.scripts) {
		t.Fatalf("scriptTimes must track scripts, got %d times for %d scripts", len(stub.scriptTimes), len(stub.scripts))
	}
}
