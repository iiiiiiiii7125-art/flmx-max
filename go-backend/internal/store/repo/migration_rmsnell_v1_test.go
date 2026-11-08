package repo

import (
	"path/filepath"
	"testing"

	gsqlite "github.com/glebarez/sqlite"
	"go-backend/internal/store/model"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// DEV-RM-SNELL v1 migration: built-in Snell inbounds (kernel-managed
// rows, kernel_id > 0) are disabled and Snell kernel rows have their
// installed state cleared; imported Snell nodes (kernel_id = 0) are
// NOT part of the removal and must stay untouched, as must every
// other protocol. Rows are preserved (disable, not delete) and the
// migration is idempotent via its schema_migration version record.

func openRMSnellDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(gsqlite.Open(filepath.Join(t.TempDir(), "rmsnell.db")), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.AutoMigrate(&model.NodeKernel{}, &model.NodeInbound{}, &schemaMigration{}); err != nil {
		t.Fatalf("craft tables: %v", err)
	}
	return db
}

func rmsnellKernel(t *testing.T, db *gorm.DB, kernelType, installed string, enabled int) *model.NodeKernel {
	t.Helper()
	k := &model.NodeKernel{
		NodeID:           1,
		KernelType:       kernelType,
		Version:          "latest",
		InstalledVersion: installed,
		Status:           "running",
		ConfigHash:       "cfg-" + kernelType,
		AppliedHash:      "app-" + kernelType,
		Enabled:          enabled,
		CreatedTime:      1,
		UpdatedTime:      1,
	}
	if err := db.Create(k).Error; err != nil {
		t.Fatalf("seed kernel %s: %v", kernelType, err)
	}
	return k
}

func rmsnellInbound(t *testing.T, db *gorm.DB, kernelID int64, name, protocol string, enabled int) *model.NodeInbound {
	t.Helper()
	in := &model.NodeInbound{
		KernelID:    kernelID,
		NodeID:      1,
		Name:        name,
		Protocol:    protocol,
		Port:        20000,
		Enabled:     enabled,
		CreatedTime: 1,
		UpdatedTime: 1,
	}
	if err := db.Create(in).Error; err != nil {
		t.Fatalf("seed inbound %s: %v", name, err)
	}
	return in
}

func rmsnellReloadInbound(t *testing.T, db *gorm.DB, id int64) model.NodeInbound {
	t.Helper()
	var in model.NodeInbound
	if err := db.First(&in, id).Error; err != nil {
		t.Fatalf("reload inbound %d: %v", id, err)
	}
	return in
}

func rmsnellReloadKernel(t *testing.T, db *gorm.DB, id int64) model.NodeKernel {
	t.Helper()
	var k model.NodeKernel
	if err := db.First(&k, id).Error; err != nil {
		t.Fatalf("reload kernel %d: %v", id, err)
	}
	return k
}

func TestRMSnellMigrationDisablesBuiltinKeepsImported(t *testing.T) {
	db := openRMSnellDB(t)
	snellK := rmsnellKernel(t, db, "snell", "5.0.1", 1)
	sbK := rmsnellKernel(t, db, "singbox", "1.12.0", 1)

	onSnellKernel := rmsnellInbound(t, db, snellK.ID, "snell-on-snell-kernel", "snell", 1)
	onSingbox := rmsnellInbound(t, db, sbK.ID, "snell-on-singbox", "snell", 1)
	vless := rmsnellInbound(t, db, sbK.ID, "vless-on-singbox", "vless", 1)
	imported := rmsnellInbound(t, db, 0, "imported-snell", "snell", 1)
	alreadyOff := rmsnellInbound(t, db, snellK.ID, "snell-already-off", "snell", 0)

	if err := migrateV1SnellInboundRemoved(db); err != nil {
		t.Fatalf("migration: %v", err)
	}

	if got := rmsnellReloadInbound(t, db, onSnellKernel.ID); got.Enabled != 0 {
		t.Fatalf("snell inbound on snell kernel must be disabled, enabled=%d", got.Enabled)
	}
	if got := rmsnellReloadInbound(t, db, onSingbox.ID); got.Enabled != 0 {
		t.Fatalf("snell inbound on sing-box kernel must be disabled, enabled=%d", got.Enabled)
	}
	if got := rmsnellReloadInbound(t, db, vless.ID); got.Enabled != 1 {
		t.Fatalf("vless inbound must stay enabled, enabled=%d", got.Enabled)
	}
	if got := rmsnellReloadInbound(t, db, imported.ID); got.Enabled != 1 {
		t.Fatalf("imported snell node (kernel_id=0) must stay enabled, enabled=%d", got.Enabled)
	}
	if got := rmsnellReloadInbound(t, db, alreadyOff.ID); got.Enabled != 0 {
		t.Fatalf("already-disabled inbound must stay disabled, enabled=%d", got.Enabled)
	}

	k := rmsnellReloadKernel(t, db, snellK.ID)
	if k.Enabled != 0 || k.InstalledVersion != "" || k.ConfigHash != "" || k.AppliedHash != "" || k.Status != "pending" || k.LastError == "" {
		t.Fatalf("snell kernel row must be disabled with cleared state, got %+v", k)
	}
	k2 := rmsnellReloadKernel(t, db, sbK.ID)
	if k2.Enabled != 1 || k2.InstalledVersion != "1.12.0" || k2.Status != "running" {
		t.Fatalf("sing-box kernel row must be untouched, got %+v", k2)
	}

	// Rows are preserved, not deleted.
	var n int64
	if err := db.Model(&model.NodeInbound{}).Count(&n).Error; err != nil || n != 5 {
		t.Fatalf("all 5 inbound rows must survive, count=%d err=%v", n, err)
	}

	// Version recorded exactly once; second run is a no-op.
	var versions int64
	if err := db.Model(&schemaMigration{}).Where("version = ?", migrationRMSnellV1Version).Count(&versions).Error; err != nil || versions != 1 {
		t.Fatalf("migration version must be recorded once, count=%d err=%v", versions, err)
	}
	if err := migrateV1SnellInboundRemoved(db); err != nil {
		t.Fatalf("second migration run: %v", err)
	}
	if got := rmsnellReloadInbound(t, db, imported.ID); got.Enabled != 1 {
		t.Fatalf("imported snell node must still be enabled after re-run, enabled=%d", got.Enabled)
	}
	if err := db.Model(&schemaMigration{}).Where("version = ?", migrationRMSnellV1Version).Count(&versions).Error; err != nil || versions != 1 {
		t.Fatalf("version must still be recorded once after re-run, count=%d err=%v", versions, err)
	}
}

// A database with no Snell stock at all: the migration records its
// version and changes nothing.
func TestRMSnellMigrationNoSnellStock(t *testing.T) {
	db := openRMSnellDB(t)
	sbK := rmsnellKernel(t, db, "singbox", "1.12.0", 1)
	vless := rmsnellInbound(t, db, sbK.ID, "vless-only", "vless", 1)

	if err := migrateV1SnellInboundRemoved(db); err != nil {
		t.Fatalf("migration: %v", err)
	}
	if got := rmsnellReloadInbound(t, db, vless.ID); got.Enabled != 1 {
		t.Fatalf("vless inbound must stay enabled, enabled=%d", got.Enabled)
	}
	var versions int64
	if err := db.Model(&schemaMigration{}).Where("version = ?", migrationRMSnellV1Version).Count(&versions).Error; err != nil || versions != 1 {
		t.Fatalf("migration version must be recorded, count=%d err=%v", versions, err)
	}
}
