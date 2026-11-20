package nftables

import (
	"context"
	"strings"
	"testing"
)

// R18-C3-02: the isolated nft table name must be derived from a
// per-install identity, not only from the manually filled
// FLMX_INSTANCE key. Two default-instance panels on different
// hosts co-managing one node used to share flmx_e3b0c442 and
// delete each other's tables on every sync.

func TestTableNameForInstallDistinctPerInstall(t *testing.T) {
	a := TableNameForInstall("", "11111111-1111-1111-1111-111111111111")
	b := TableNameForInstall("", "22222222-2222-2222-2222-222222222222")
	if a == b {
		t.Fatalf("different install IDs must get different tables, both %q", a)
	}
	if a == "flmx_e3b0c442" || b == "flmx_e3b0c442" {
		t.Fatalf("an install with an identity must not land on the legacy default table flmx_e3b0c442: %q %q", a, b)
	}
	if again := TableNameForInstall("", "11111111-1111-1111-1111-111111111111"); again != a {
		t.Fatalf("table derivation must be stable: %q vs %q", again, a)
	}
	// The instance key still participates: same install, different
	// instance keys must not collide either.
	c := TableNameForInstall("blue", "11111111-1111-1111-1111-111111111111")
	if c == a {
		t.Fatalf("different instances on the same install must get different tables, both %q", a)
	}
	for _, table := range []string{a, b, c} {
		if table == plainTableName || table == legacyTableName {
			t.Fatalf("install table must never be a plain name, got %q", table)
		}
		if len(table) != len("flmx_")+8 {
			t.Fatalf("install table must be flmx_<8 hex>, got %q", table)
		}
	}
}

func TestTableNameForInstallLegacyFallback(t *testing.T) {
	// No install identity (empty or invalid): the legacy derivation
	// is kept verbatim, so already-deployed single-panel installs
	// keep their table and lose no rules on upgrade.
	for _, installID := range []string{"", "  ", "has space", "bad/key", "bad\nid"} {
		if got := TableNameForInstall("", installID); got != TableNameForInstance("") {
			t.Fatalf("install ID %q must fall back to the legacy default table %q, got %q", installID, TableNameForInstance(""), got)
		}
		if got := TableNameForInstall("blue", installID); got != TableNameForInstance("blue") {
			t.Fatalf("install ID %q must fall back to the legacy instance table %q, got %q", installID, TableNameForInstance("blue"), got)
		}
	}
}

func TestNodePlanTableNameFollowsInstallID(t *testing.T) {
	withID := NodePlan{NodeID: 7, InstanceID: "", InstallID: "11111111-1111-1111-1111-111111111111"}
	if got := withID.TableName(); got != TableNameForInstall("", withID.InstallID) {
		t.Fatalf("plan table must follow its install ID, got %q", got)
	}
	// Zero plan keeps the legacy default table (smooth migration).
	if got := (NodePlan{}).TableName(); got != "flmx_e3b0c442" {
		t.Fatalf("zero plan must keep the legacy default table, got %q", got)
	}
	script := RenderTable(NodePlan{
		NodeID: 7, InstallID: "11111111-1111-1111-1111-111111111111",
		Rules: []Rule{{ForwardID: 42, InPort: 24000, TargetHost: "198.51.100.20", TargetPort: 443, Protocols: []string{"tcp", "udp"}}},
	})
	if !strings.Contains(script, "table inet "+withID.TableName()+" {") {
		t.Fatalf("rendered table must use the install-derived name:\n%s", script)
	}
	if strings.Contains(script, "table inet flmx_e3b0c442 {") {
		t.Fatalf("rendered table must not fall back to the shared default name:\n%s", script)
	}
}

func TestManagerWithInstallUsesInstallTable(t *testing.T) {
	runner := &fakeRunner{listJSON: []byte(`{"nftables": []}`)}
	manager := NewManagerWithInstall(runner, "", "11111111-1111-1111-1111-111111111111")
	if _, err := manager.CollectCounters(context.Background(), SSHConfig{}); err != nil {
		t.Fatalf("CollectCounters: %v", err)
	}
	if len(runner.tables) != 1 {
		t.Fatalf("expected 1 table read, got %v", runner.tables)
	}
	want := TableNameForInstall("", "11111111-1111-1111-1111-111111111111")
	if runner.tables[0] != want {
		t.Fatalf("manager must collect from the install table %q, got %q", want, runner.tables[0])
	}
	// NewManagerWithInstance keeps the legacy table (no identity).
	runner2 := &fakeRunner{listJSON: []byte(`{"nftables": []}`)}
	legacy := NewManagerWithInstance(runner2, "")
	if _, err := legacy.CollectCounters(context.Background(), SSHConfig{}); err != nil {
		t.Fatalf("CollectCounters: %v", err)
	}
	if runner2.tables[0] != "flmx_e3b0c442" {
		t.Fatalf("instance-only manager must keep the legacy default table, got %q", runner2.tables[0])
	}
}
