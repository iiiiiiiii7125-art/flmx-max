package kernel

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// R18-C3-06 (M-4, with R18-D3-08 merged): removeTables deleted the
// still-running legacy generation agent's live table
// flmx_kernel_legacy on uninstall, silently dropping its inbound
// whitelist until its next sync rebuilt the table. Uninstall must
// delete the legacy table only when no legacy agent is alive —
// first check liveness, then delete. The migration-cleanup case
// (no live legacy agent) still deletes it; see
// TestRemoveTablesCanonicalDeletesLegacyToo.
func TestRemoveTablesSkipsLegacyTableWhenLegacyAgentAlive(t *testing.T) {
	stubKernelResolution(t, false, "/anywhere", nil, "/etc/flmx_agent/flmx_agent", nil)
	oldAlive := legacyAgentAlive
	legacyAgentAlive = func() bool { return true }
	t.Cleanup(func() { legacyAgentAlive = oldAlive })

	var scripts []string
	oldRunner := nftRunner
	nftRunner = func(_ context.Context, stdin string, args ...string) ([]byte, error) {
		scripts = append(scripts, stdin)
		return nil, nil
	}
	t.Cleanup(func() { nftRunner = oldRunner })

	n := newNftCounters(defaultBaseDir)
	n.state = nftState{}
	if err := n.removeTables(); err != nil {
		t.Fatalf("removeTables: %v", err)
	}
	if len(scripts) != 1 {
		t.Fatalf("removeTables must run one script, got %d", len(scripts))
	}
	script := scripts[0]
	if strings.Contains(script, "delete table inet flmx_kernel_legacy\n") {
		t.Fatalf("removeTables must not delete the live legacy agent's table: %s", script)
	}
	// Everything else is still uninstalled: own table, the
	// pre-rename residue and the hop probe table.
	for _, want := range []string{"delete table inet " + n.tableName() + "\n", "delete table inet flvx_kernel\n", "delete table inet flmx_hop_probe\n"} {
		if !strings.Contains(script, want) {
			t.Fatalf("removeTables must still delete %q: %s", want, script)
		}
	}
}

// The liveness probe itself: a process executing a binary under
// /etc/flux_agent (exe symlink or cmdline) is a live legacy agent;
// anything else is not. The probe scans a fake /proc tree so the
// test never depends on the machine it runs on.
func TestLegacyAgentRunningDetectsProcEntries(t *testing.T) {
	procRoot := t.TempDir()
	mkProc := func(pid, exe, cmdline string) {
		dir := filepath.Join(procRoot, pid)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if exe != "" {
			if err := os.Symlink(exe, filepath.Join(dir, "exe")); err != nil {
				t.Fatal(err)
			}
		}
		if cmdline != "" {
			if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(cmdline), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	mkProc("100", "/usr/bin/sing-box", "/usr/bin/sing-box\x00run\x00")
	mkProc("200", "/etc/flux_agent/flux_agent", "/etc/flux_agent/flux_agent\x00")
	mkProc("300", "/usr/bin/other", "/etc/flux_agent/kernels/something\x00")

	oldRoot := legacyAgentProcRoot
	legacyAgentProcRoot = procRoot
	t.Cleanup(func() { legacyAgentProcRoot = oldRoot })

	if !legacyAgentRunning() {
		t.Fatal("a process under /etc/flux_agent must count as a live legacy agent")
	}

	// Only unrelated processes: not alive.
	procRoot2 := t.TempDir()
	mkProc2 := func(pid, exe string) {
		dir := filepath.Join(procRoot2, pid)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(exe, filepath.Join(dir, "exe")); err != nil {
			t.Fatal(err)
		}
	}
	mkProc2("100", "/usr/bin/sing-box")
	mkProc2("400", "/etc/flmx_agent/flmx_agent")
	legacyAgentProcRoot = procRoot2
	if legacyAgentRunning() {
		t.Fatal("no process under /etc/flux_agent must count as no live legacy agent")
	}

	// An unreadable /proc must fail closed toward "alive": deleting
	// a possibly-live whitelist table is worse than leaving a
	// residue table behind.
	legacyAgentProcRoot = filepath.Join(t.TempDir(), "does-not-exist")
	if !legacyAgentRunning() {
		t.Fatal("an unreadable /proc must assume a legacy agent may be alive")
	}
}
