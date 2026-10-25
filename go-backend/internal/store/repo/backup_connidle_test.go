package repo

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// connIdle backup round-trip: the backup uses an explicit field list
// (InboundBackup), so a new column only survives export/import if it is
// wired at all three places (struct, export, restore incl. the
// OnConflict update columns). A dropped wire would silently reset the
// timeout to unset on every restore.
func TestBackupConnIdleRoundTrip(t *testing.T) {
	now := time.Now().UnixMilli()
	src, err := Open(filepath.Join(t.TempDir(), "src.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	createRow(t, src, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "26000-26100",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	createRow(t, src, &model.NodeKernel{ID: 1, NodeID: 1, KernelType: "xray", Version: "latest", Enabled: 1, CreatedTime: now})

	set := &model.NodeInbound{KernelID: 1, NodeID: 1, Name: "with-timeout", Protocol: "vless", Port: 21001,
		Settings: `{"uuid":"0b6e4c38-8f0e-4a3e-9d2d-7a1c2b3c4d5e"}`, Transport: `{"type":"tcp"}`, Security: `{"type":"none"}`,
		ConnIdleSeconds: 180, CreatedTime: now, UpdatedTime: now}
	if err := src.CreateNodeInbound(set); err != nil {
		t.Fatal(err)
	}
	unset := &model.NodeInbound{KernelID: 1, NodeID: 1, Name: "no-timeout", Protocol: "vless", Port: 21002,
		Settings: `{"uuid":"0b6e4c38-8f0e-4a3e-9d2d-7a1c2b3c4d5e"}`, Transport: `{"type":"tcp"}`, Security: `{"type":"none"}`,
		CreatedTime: now, UpdatedTime: now}
	if err := src.CreateNodeInbound(unset); err != nil {
		t.Fatal(err)
	}
	// Unlimited (-1) is a plain int in the backup too: it must survive
	// export/import unchanged, not collapse to 0 (unset).
	unlimited := &model.NodeInbound{KernelID: 1, NodeID: 1, Name: "unlimited", Protocol: "vless", Port: 21003,
		Settings: `{"uuid":"0b6e4c38-8f0e-4a3e-9d2d-7a1c2b3c4d5e"}`, Transport: `{"type":"tcp"}`, Security: `{"type":"none"}`,
		ConnIdleSeconds: -1, CreatedTime: now, UpdatedTime: now}
	if err := src.CreateNodeInbound(unlimited); err != nil {
		t.Fatal(err)
	}

	backup, err := src.ExportPartial([]string{"inbounds"})
	if err != nil {
		t.Fatal(err)
	}
	if backup.Inbounds == nil || len(backup.Inbounds.Inbounds) != 3 {
		t.Fatalf("exported inbounds = %v", backup.Inbounds)
	}
	for _, b := range backup.Inbounds.Inbounds {
		want := 0
		if b.Name == "with-timeout" {
			want = 180
		}
		if b.Name == "unlimited" {
			want = -1
		}
		if b.ConnIdleSeconds != want {
			t.Fatalf("exported %q connIdleSeconds = %d, want %d", b.Name, b.ConnIdleSeconds, want)
		}
	}

	dst, err := Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()
	createRow(t, dst, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "26000-26100",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	createRow(t, dst, &model.NodeKernel{ID: 1, NodeID: 1, KernelType: "xray", Version: "latest", Enabled: 1, CreatedTime: now})

	if _, err := dst.Import(backup, []string{"inbounds"}); err != nil {
		t.Fatal(err)
	}
	got, err := dst.GetNodeInbound(set.ID)
	if err != nil || got == nil {
		t.Fatalf("restored row: %v", err)
	}
	if got.ConnIdleSeconds != 180 {
		t.Fatalf("restored connIdleSeconds = %d, want 180", got.ConnIdleSeconds)
	}
	got, err = dst.GetNodeInbound(unset.ID)
	if err != nil || got == nil {
		t.Fatalf("restored unset row: %v", err)
	}
	if got.ConnIdleSeconds != 0 {
		t.Fatalf("restored unset connIdleSeconds = %d, want 0", got.ConnIdleSeconds)
	}
	got, err = dst.GetNodeInbound(unlimited.ID)
	if err != nil || got == nil {
		t.Fatalf("restored unlimited row: %v", err)
	}
	if got.ConnIdleSeconds != -1 {
		t.Fatalf("restored unlimited connIdleSeconds = %d, want -1", got.ConnIdleSeconds)
	}
}
