package repo

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// Companion inbounds (SS subscription metering) are derived rows: the
// partial unique index on companion_of must allow any number of plain
// inbounds (companion_of = 0) but at most one companion per origin.
func TestCompanionPartialUniqueIndex(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "companion.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	now := time.Now().UnixMilli()
	mk := func(name string, companionOf int64, port int) error {
		return r.CreateNodeInbound(&model.NodeInbound{
			KernelID: 1, NodeID: 1, Name: name, Protocol: "shadowsocks",
			Port: port, CompanionOf: companionOf, CreatedTime: now, UpdatedTime: now,
		})
	}
	if err := mk("a", 0, 10001); err != nil {
		t.Fatalf("plain inbound a: %v", err)
	}
	if err := mk("b", 0, 10002); err != nil {
		t.Fatalf("plain inbound b (second companion_of=0 row) must be allowed: %v", err)
	}
	if err := mk("a-companion", 1, 10003); err != nil {
		t.Fatalf("first companion of origin 1: %v", err)
	}
	if err := mk("a-companion-dup", 1, 10004); err == nil {
		t.Fatal("a second companion for the same origin must be rejected by the partial unique index")
	} else if !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("duplicate companion must fail on the unique index, got: %v", err)
	}
	comp, err := r.GetCompanionInbound(1)
	if err != nil || comp == nil || comp.Name != "a-companion" {
		t.Fatalf("GetCompanionInbound: %v %+v", err, comp)
	}
	all, err := r.ListCompanionInbounds()
	if err != nil || len(all) != 1 {
		t.Fatalf("ListCompanionInbounds: %v (%d rows)", err, len(all))
	}
}

// F7 (B-05): a port conflict caused by a metering companion must be
// reported as such — the generic "other node service" label sends
// users hunting for a service that does not appear in their lists
// and that they cannot move or delete directly.
func TestNodePortRangeOccupantNamesCompanion(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "occ.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	now := time.Now().UnixMilli()
	createRow(t, r, &model.NodeInbound{ID: 76, KernelID: 1, NodeID: 7, Name: "plain", Protocol: "vless",
		Port: 9100, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.NodeInbound{ID: 77, KernelID: 1, NodeID: 7, Name: "comp", Protocol: "shadowsocks",
		Port: 9200, CompanionOf: 5, CreatedTime: now, UpdatedTime: now})

	occ, err := r.NodePortRangeOccupant(7, 9200, 9200, 0)
	if err != nil || occ != "SS 计量伴生入站" {
		t.Fatalf("companion occupant must be named, got %q (%v)", occ, err)
	}
	occ, err = r.NodePortRangeOccupant(7, 9100, 9100, 0)
	if err != nil || occ != "其他节点服务" {
		t.Fatalf("plain inbound occupant keeps the generic label, got %q (%v)", occ, err)
	}
	occ, err = r.NodePortRangeOccupant(7, 9300, 9300, 0)
	if err != nil || occ != "" {
		t.Fatalf("free range must report no occupant, got %q (%v)", occ, err)
	}
	occ, err = r.NodePortRangeOccupant(7, 9200, 9200, 77)
	if err != nil || occ != "" {
		t.Fatalf("excluding the companion itself must clear the report, got %q (%v)", occ, err)
	}
}

// F6 (B-07): restoring a backup whose inbound id collides with an
// existing companion row (cross-panel restore) must clear the row's
// companion marker — restored rows are origins; a leftover marker
// would hide the row from every list and expose it to reconcile
// recycling under another origin's lifecycle.
func TestBackupRestoreClearsCollidingCompanionMarker(t *testing.T) {
	now := time.Now().UnixMilli()
	src, err := Open(filepath.Join(t.TempDir(), "src.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	createRow(t, src, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "26000-26100",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	createRow(t, src, &model.NodeKernel{ID: 1, NodeID: 1, KernelType: "xray", Version: "latest", Enabled: 1, CreatedTime: now})
	origin := &model.NodeInbound{KernelID: 1, NodeID: 1, Name: "ss-origin", Protocol: "shadowsocks",
		Port: 8388, Settings: `{"method":"2022-blake3-aes-128-gcm","password":"shared"}`,
		MeteringEnabled: 1, CreatedTime: now, UpdatedTime: now}
	if err := src.CreateNodeInbound(origin); err != nil {
		t.Fatal(err)
	}
	backup, err := src.ExportPartial([]string{"inbounds"})
	if err != nil {
		t.Fatal(err)
	}

	dst, err := Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()
	createRow(t, dst, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "26000-26100",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	createRow(t, dst, &model.NodeKernel{ID: 1, NodeID: 1, KernelType: "xray", Version: "latest", Enabled: 1, CreatedTime: now})
	// A companion row squatting on the id the backup's origin carries.
	squatter := &model.NodeInbound{ID: origin.ID, KernelID: 1, NodeID: 1, Name: "foreign-companion",
		Protocol: "shadowsocks", Port: 31009,
		Settings:    `{"method":"2022-blake3-aes-128-gcm","password":"ipsk"}`,
		CompanionOf: 4242, CreatedTime: now, UpdatedTime: now}
	createRow(t, dst, squatter)

	if _, err := dst.Import(backup, []string{"inbounds"}); err != nil {
		t.Fatal(err)
	}
	row, err := dst.GetNodeInbound(origin.ID)
	if err != nil || row == nil {
		t.Fatalf("restored row: %v", err)
	}
	if row.CompanionOf != 0 {
		t.Fatalf("restored origin must not keep the squatter's companion marker, companionOf=%d", row.CompanionOf)
	}
	if row.Name != "ss-origin" || row.MeteringEnabled != 1 {
		t.Fatalf("restored row must carry the backup's content: %+v", row)
	}
}

// Backups carry configuration, not derived state: companion rows are
// excluded, while the origin's metering switch round-trips.
func TestBackupExcludesCompanionInbounds(t *testing.T) {
	src, err := Open(filepath.Join(t.TempDir(), "src.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	now := time.Now().UnixMilli()
	createRow(t, src, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "26000-26100",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	createRow(t, src, &model.NodeKernel{ID: 1, NodeID: 1, KernelType: "xray", Version: "latest", Enabled: 1, CreatedTime: now})
	origin := &model.NodeInbound{KernelID: 1, NodeID: 1, Name: "ss-origin", Protocol: "shadowsocks",
		Port: 8388, Settings: `{"method":"2022-blake3-aes-128-gcm","password":"shared"}`,
		MeteringEnabled: 1, CreatedTime: now, UpdatedTime: now}
	if err := src.CreateNodeInbound(origin); err != nil {
		t.Fatal(err)
	}
	comp := &model.NodeInbound{KernelID: 1, NodeID: 1, Name: "ss-origin（计量）", Protocol: "shadowsocks",
		Port: 31009, Settings: `{"method":"2022-blake3-aes-128-gcm","password":"ipsk"}`,
		CompanionOf: origin.ID, CreatedTime: now, UpdatedTime: now}
	if err := src.CreateNodeInbound(comp); err != nil {
		t.Fatal(err)
	}
	backup, err := src.ExportPartial([]string{"inbounds"})
	if err != nil {
		t.Fatal(err)
	}
	if backup.Inbounds == nil || len(backup.Inbounds.Inbounds) != 1 {
		t.Fatalf("backup must contain exactly the origin, got %+v", backup.Inbounds)
	}
	item := backup.Inbounds.Inbounds[0]
	if item.Name != "ss-origin" || item.MeteringEnabled != 1 {
		t.Fatalf("origin must round-trip with its metering switch: %+v", item)
	}

	dst, err := Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()
	createRow(t, dst, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "26000-26100",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	if _, err := dst.Import(backup, []string{"inbounds"}); err != nil {
		t.Fatal(err)
	}
	rows, err := dst.ListNodeInbounds()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("imported panel must hold exactly one inbound, got %d", len(rows))
	}
	if rows[0].CompanionOf != 0 || rows[0].MeteringEnabled != 1 {
		t.Fatalf("imported origin: companionOf=%d metering=%d", rows[0].CompanionOf, rows[0].MeteringEnabled)
	}
}
