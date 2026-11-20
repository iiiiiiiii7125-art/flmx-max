package nftables

import (
	"strings"
	"testing"
)

// R18-R2-C3-03: the pause-table upgrade migration claimed other
// panels' blocked ports, and the unblock path cleared other panels'
// elements out of the shared plain table.
//
// Migration leg: building the isolated pause table used to copy
// every element of the plain flmx_quota set into this install's
// table — including ports another (not yet upgraded) panel had
// blocked there — so this panel enforced, and its hourly sweep
// later unblocked, ports it does not own. The adopt is gone: the
// block command must not read the plain table at all.
//
// Restore leg: unblocking must clear the pre-upgrade plain-table
// residue only when the caller knows this panel paused the port
// (sync resume-convergence, cleanupLegacy=true). The orphan sweep
// unblocks ports with no owning forward and must never touch the
// plain table (cleanupLegacy=false): an element there is more
// likely another panel's block, and clearing it on port reuse is
// the resume path's job.

func TestBuildSetBlockedCommandBlockClaimsNoForeignPorts(t *testing.T) {
	cmd := buildSetBlockedCommand("nft", PauseTableNameForInstall("", "11111111-1111-1111-1111-111111111111"), 20000, true, false)
	for _, want := range []string{
		"table inet flmx_quota_",
		"nft add element inet flmx_quota_",
		"blocked_ports '{ 20000 }'",
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("block command missing %q:\n%s", want, cmd)
		}
	}
	// The plain shared table must not be read, listed or copied
	// from: any element there may belong to another panel.
	for _, bad := range []string{
		"list table inet flmx_quota ",
		"list set inet flmx_quota ",
		"add element inet flmx_quota ",
		"delete table inet flmx_quota",
	} {
		if strings.Contains(cmd, bad) {
			t.Fatalf("block command must not touch the plain shared table (%q):\n%s", bad, cmd)
		}
	}
}

func TestBuildSetBlockedCommandUnblockSweepKeepsPlainTable(t *testing.T) {
	// cleanupLegacy=false is the orphan-sweep leg: the unblock
	// only removes the element from this install's isolated
	// table and leaves the plain table alone.
	cmd := buildSetBlockedCommand("nft", PauseTableNameForInstall("blue", ""), 20001, false, false)
	if !strings.Contains(cmd, "nft delete element inet flmx_quota_16477688 blocked_ports '{ 20001 }'") {
		t.Fatalf("unblock command must delete the element from the isolated table:\n%s", cmd)
	}
	if strings.Contains(cmd, "flmx_quota blocked_ports") {
		t.Fatalf("sweep unblock must never touch the plain shared table:\n%s", cmd)
	}
}

func TestBuildSetBlockedCommandUnblockResumeClearsLegacyResidue(t *testing.T) {
	// cleanupLegacy=true is the sync resume-convergence leg: the
	// port belongs to this panel's own forward being resumed, so
	// the pre-upgrade plain-table residue must be cleared too, or
	// the resume would silently keep dropping traffic.
	cmd := buildSetBlockedCommand("nft", PauseTableNameForInstall("blue", ""), 20001, false, true)
	for _, want := range []string{
		"nft delete element inet flmx_quota_16477688 blocked_ports '{ 20001 }'",
		"nft delete element inet flmx_quota blocked_ports '{ 20001 }'",
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("resume unblock missing %q:\n%s", want, cmd)
		}
	}
}
