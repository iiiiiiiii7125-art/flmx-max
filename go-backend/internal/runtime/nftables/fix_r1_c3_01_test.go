package nftables

import (
	"strings"
	"testing"
)

// R18-R2-C3-01: existing deployments never received an install
// identity (FLMX_INSTALL_ID was only written on the new-install
// path), so two default-instance panels kept sharing
// flmx_e3b0c442 and deleting each other's tables. The upgrade
// paths now backfill the identity (see ensure_panel_install_id
// in panel_install.sh and installIDPin in the upgrade handler).
// These tests pin the runtime side of that story: once two
// installs hold different identities their tables differ and
// their syncs can no longer delete each other, the backfilled
// table carries exactly the same rules as the legacy table (no
// rule loss on the switch), and the upgrade sync never deletes
// the legacy table itself (it may still be another panel's live
// table — it stays as documented residue).

func TestTwoDefaultInstallsGetDifferentTablesAfterBackfill(t *testing.T) {
	legacy := TableNameForInstance("")
	if legacy != "flmx_e3b0c442" {
		t.Fatalf("legacy default table must be flmx_e3b0c442, got %q", legacy)
	}
	a := TableNameForInstall("", "11111111-1111-1111-1111-111111111111")
	b := TableNameForInstall("", "22222222-2222-2222-2222-222222222222")
	if a == legacy || b == legacy {
		t.Fatalf("backfilled installs must leave the legacy table: a=%q b=%q legacy=%q", a, b, legacy)
	}
	if a == b {
		t.Fatalf("two default-instance installs with different identities must get different tables: %q", a)
	}
	pauseA := PauseTableNameForInstall("", "11111111-1111-1111-1111-111111111111")
	pauseB := PauseTableNameForInstall("", "22222222-2222-2222-2222-222222222222")
	if pauseA == pauseB {
		t.Fatalf("two installs must get different pause tables: %q", pauseA)
	}
	if pauseA == PauseTableNameForInstance("") || pauseB == PauseTableNameForInstance("") {
		t.Fatalf("backfilled installs must leave the legacy pause table")
	}
}

func TestBackfilledTableCarriesSameRulesAsLegacyTable(t *testing.T) {
	rules := []Rule{
		{ForwardID: 7, InPort: 24000, TargetHost: "198.51.100.20", TargetPort: 443, Protocols: []string{"tcp", "udp"}},
		{ForwardID: 8, InPort: 24001, BindIP: "192.0.2.10", TargetHost: "198.51.100.20", TargetPort: 8443, Protocols: []string{"tcp"}},
	}
	legacy := RenderTable(NodePlan{NodeID: 10, Rules: rules})
	backfilled := RenderTable(NodePlan{NodeID: 10, InstallID: "11111111-1111-1111-1111-111111111111", Rules: rules})
	// The only permitted difference is the table name itself: the
	// rule set (and therefore the forwarding behaviour) must be
	// identical, so switching tables on backfill loses no rules.
	// (The name also appears inside rule comments, so normalize
	// every occurrence, not just the table header line.)
	normalize := func(s, table string) string {
		return strings.ReplaceAll(s, table, "<table>")
	}
	if normalize(legacy, TableNameForInstance("")) != normalize(backfilled, TableNameForInstall("", "11111111-1111-1111-1111-111111111111")) {
		t.Fatalf("backfilled rendering must carry the same rules as the legacy rendering:\n--- legacy ---\n%s\n--- backfilled ---\n%s", legacy, backfilled)
	}
	if !strings.Contains(backfilled, "dnat ip to 198.51.100.20:443") || !strings.Contains(backfilled, "dnat ip to 198.51.100.20:8443") {
		t.Fatalf("backfilled rendering lost rules:\n%s", backfilled)
	}
}

func TestUpgradeSyncNeverDeletesLegacyTable(t *testing.T) {
	// The first sync after backfill rebuilds the isolated table
	// from the DB rule set; the legacy table flmx_e3b0c442 may
	// still be another panel's live table, so the apply batch
	// must never delete it. The plain pre-isolation tables
	// (flmx/flvx) are still migrated away under the B-01 proof.
	script := RenderTable(NodePlan{NodeID: 10, InstallID: "11111111-1111-1111-1111-111111111111"})
	cmd := buildApplyCommand("nft", TableNameForInstall("", "11111111-1111-1111-1111-111111111111"), script, true)
	if strings.Contains(cmd, "delete table inet flmx_e3b0c442") {
		t.Fatalf("upgrade sync must never delete the legacy table (another panel may still live in it):\n%s", cmd)
	}
	if !strings.Contains(cmd, "'delete table inet flmx'") || !strings.Contains(cmd, "'delete table inet flvx'") {
		t.Fatalf("upgrade sync must still migrate the plain pre-isolation tables under proof:\n%s", cmd)
	}
}
