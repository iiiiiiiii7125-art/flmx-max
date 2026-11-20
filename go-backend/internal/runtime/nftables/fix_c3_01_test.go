package nftables

import (
	"strings"
	"testing"
)

// R18-C3-01: the pause/quota table must be isolated per install
// like the main table. The old plain shared table flmx_quota let
// another panel's hourly orphan sweep unblock this panel's paused
// ports, and a Clear on one panel wiped every panel's blocks.

func TestPauseTableNameForInstall(t *testing.T) {
	def := PauseTableNameForInstall("", "")
	if def != "flmx_quota_e3b0c442" {
		t.Fatalf("default instance pause table must be flmx_quota_e3b0c442, got %q", def)
	}
	if def == "flmx_quota" {
		t.Fatal("pause table must never be the plain shared name")
	}
	blue := PauseTableNameForInstall("blue", "")
	if blue != "flmx_quota_16477688" {
		t.Fatalf("instance %q pause table must be flmx_quota_16477688, got %q", "blue", blue)
	}
	// Same suffix as the main table, same install: the two tables of
	// one install share one identity.
	inst := PauseTableNameForInstall("", "11111111-1111-1111-1111-111111111111")
	main := TableNameForInstall("", "11111111-1111-1111-1111-111111111111")
	if inst != "flmx_quota_"+strings.TrimPrefix(main, "flmx_") {
		t.Fatalf("pause table %q must share the main table's suffix %q", inst, main)
	}
	if inst == def || inst == blue {
		t.Fatalf("different installs must get different pause tables: %q vs %q/%q", inst, def, blue)
	}
	if got := PauseTableNameForInstance("blue"); got != blue {
		t.Fatalf("PauseTableNameForInstance must match the install form without identity, got %q", got)
	}
}

func TestRenderPauseTableForInstallIsolated(t *testing.T) {
	table := RenderPauseTableForInstall("", "11111111-1111-1111-1111-111111111111")
	if !strings.Contains(table, "table inet "+PauseTableNameForInstall("", "11111111-1111-1111-1111-111111111111")+" {") {
		t.Fatalf("rendered pause table must use the install table:\n%s", table)
	}
	if strings.Contains(table, "table inet flmx_quota {") {
		t.Fatalf("rendered pause table must not use the plain shared table:\n%s", table)
	}
	// Default rendering is the isolated default table too.
	def := RenderPauseTable()
	if !strings.Contains(def, "table inet flmx_quota_e3b0c442 {") {
		t.Fatalf("default pause table must render into flmx_quota_e3b0c442:\n%s", def)
	}
}

func TestBuildSetBlockedCommandUsesIsolatedTable(t *testing.T) {
	table := PauseTableNameForInstall("blue", "")
	cmd := buildSetBlockedCommand("nft", table, 20000, true, false)
	for _, want := range []string{
		"if ! nft list table inet flmx_quota_16477688 >/dev/null 2>&1; then",
		"table inet flmx_quota_16477688 {",
		"nft add element inet flmx_quota_16477688 blocked_ports '{ 20000 }'",
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("block command missing %q:\n%s", want, cmd)
		}
	}
	// No plain-table reference as the operating table: the block
	// must not touch the shared table at all.
	if strings.Contains(cmd, "list table inet flmx_quota {") || strings.Contains(cmd, "delete table inet flmx_quota") {
		t.Fatalf("block command must never operate on the plain shared table:\n%s", cmd)
	}
	if strings.Contains(cmd, "delete table") {
		t.Fatalf("block command must not delete any table:\n%s", cmd)
	}
	// R18-R2-C3-03: no blind adopt of the pre-upgrade plain table
	// on table creation. The old one-shot migration copied every
	// plain-table element into this install's table — including
	// ports another panel had blocked. The adopt is also
	// unnecessary: every sync re-blocks this install's full DB
	// expected set port by port, and the plain table itself is
	// never deleted, so it keeps enforcing its ports until the
	// resume path clears each residue element.
	if strings.Contains(cmd, "list table inet flmx_quota >/dev/null 2>&1") {
		t.Fatalf("block command must not read the plain shared table (no blind adopt):\n%s", cmd)
	}
	if strings.Contains(cmd, "list set inet flmx_quota ") {
		t.Fatalf("block command must not copy elements out of the plain shared table:\n%s", cmd)
	}
}

func TestBuildSetBlockedCommandUnblockIsolatedAndClearsLegacyResidue(t *testing.T) {
	table := PauseTableNameForInstall("blue", "")
	cmd := buildSetBlockedCommand("nft", table, 20001, false, true)
	for _, want := range []string{
		"if nft list table inet flmx_quota_16477688 >/dev/null 2>&1; then",
		"nft delete element inet flmx_quota_16477688 blocked_ports '{ 20001 }'",
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("unblock command missing %q:\n%s", want, cmd)
		}
	}
	if strings.Contains(cmd, "add element") || strings.Contains(cmd, "table inet flmx_quota_16477688 {") {
		t.Fatalf("unblock command must not create the table or add elements:\n%s", cmd)
	}
	// A port paused before the upgrade stays blocked by the plain
	// table's residue; resuming must clear that residue too, or the
	// resume would silently keep dropping traffic.
	if !strings.Contains(cmd, "nft delete element inet flmx_quota blocked_ports '{ 20001 }'") {
		t.Fatalf("unblock command must also clear the pre-upgrade plain-table residue for the port:\n%s", cmd)
	}
}

func TestBuildClearBlocksCommandDeletesOnlyIsolatedTable(t *testing.T) {
	cmd := buildClearBlocksCommand("nft", PauseTableNameForInstall("blue", ""))
	for _, want := range []string{
		"if nft list table inet flmx_quota_16477688 >/dev/null 2>&1; then",
		"nft delete table inet flmx_quota_16477688",
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("clear-blocks command missing %q:\n%s", want, cmd)
		}
	}
	// Clear must never delete the plain shared table: another panel
	// may still be blocking ports in it.
	if strings.Contains(cmd, "delete table inet flmx_quota\n") || strings.Contains(cmd, "delete table inet flmx_quota ") && !strings.Contains(cmd, "delete table inet flmx_quota_16477688") {
		t.Fatalf("clear-blocks command must never delete the plain shared table:\n%s", cmd)
	}
}

func TestBuildListBlockedPortsCommandIsolated(t *testing.T) {
	cmd := buildListBlockedPortsCommand("nft", PauseTableNameForInstall("blue", ""))
	for _, want := range []string{
		"if nft list table inet flmx_quota_16477688 >/dev/null 2>&1; then",
		"nft -j list set inet flmx_quota_16477688 blocked_ports",
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("list blocked ports command missing %q:\n%s", want, cmd)
		}
	}
	// The sweep reads this panel's table only: another panel's
	// blocked ports never appear here, so they can never be judged
	// "orphan" and unblocked.
	if strings.Contains(cmd, "flmx_quota blocked_ports") || strings.Contains(cmd, "flmx_quota ") && !strings.Contains(cmd, "flmx_quota_16477688") {
		t.Fatalf("list command must not read the plain shared table:\n%s", cmd)
	}
	for _, bad := range []string{"add element", "delete element", "table inet flmx_quota_16477688 {"} {
		if strings.Contains(cmd, bad) {
			t.Fatalf("list command must not mutate the set (%q):\n%s", bad, cmd)
		}
	}
}

func TestSSHRunnerPauseTableFollowsInstall(t *testing.T) {
	runner := NewSSHRunnerWithInstall("blue", "11111111-1111-1111-1111-111111111111")
	if got := runner.pauseTableName(); got != PauseTableNameForInstall("blue", "11111111-1111-1111-1111-111111111111") {
		t.Fatalf("runner pause table must follow its install, got %q", got)
	}
	def := NewSSHRunner()
	if got := def.pauseTableName(); got != "flmx_quota_e3b0c442" {
		t.Fatalf("default runner pause table must be flmx_quota_e3b0c442, got %q", got)
	}
	// Two panels blocking the same port build commands against
	// different tables: neither side can unblock the other's port.
	a := buildSetBlockedCommand("nft", NewSSHRunnerWithInstall("", "11111111-1111-1111-1111-111111111111").pauseTableName(), 20000, true, false)
	b := buildSetBlockedCommand("nft", NewSSHRunnerWithInstall("", "22222222-2222-2222-2222-222222222222").pauseTableName(), 20000, true, false)
	if a == b {
		t.Fatal("two installs blocking the same port must build different commands")
	}
}

func TestManagerWithInstallPassesIdentityToPauseRunner(t *testing.T) {
	manager := NewManagerWithInstall(nil, "blue", "11111111-1111-1111-1111-111111111111")
	runner, ok := manager.runner.(*SSHRunner)
	if !ok {
		t.Fatalf("default runner must be an SSHRunner, got %T", manager.runner)
	}
	if runner.instance != "blue" || runner.installID != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("manager must hand its instance/identity to the default pause runner, got %q/%q", runner.instance, runner.installID)
	}
	if got := runner.pauseTableName(); got != PauseTableNameForInstall("blue", "11111111-1111-1111-1111-111111111111") {
		t.Fatalf("manager's pause runner must use the install pause table, got %q", got)
	}
}
