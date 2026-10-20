package repo

import (
	"path/filepath"
	"strings"
	"testing"

	gsqlite "github.com/glebarez/sqlite"
	"go-backend/internal/store/model"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// F-003 (B4): imported nodes are node_inbound rows with kernel_id=0 /
// node_id=0 (they are remote endpoints, not local listeners). The R5-B4 v1
// unique index on node_inbound(node_id, port) therefore rejects the second
// imported node that happens to share a port with another imported node on
// a different host. The uniqueness invariant only has meaning for
// kernel-managed inbounds (kernel_id > 0), so the index must be partial:
// UNIQUE(node_id, port) WHERE kernel_id > 0.

func openB4FileDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(gsqlite.Open(filepath.Join(t.TempDir(), "b4.db")), &gorm.Config{
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
	return db
}

func nodeInboundIndexDef(t *testing.T, db *gorm.DB) string {
	t.Helper()
	var def string
	if err := db.Raw(`SELECT sql FROM sqlite_master WHERE type='index' AND name='idx_node_inbound_node_port'`).Scan(&def).Error; err != nil {
		t.Fatalf("read index definition: %v", err)
	}
	return def
}

func indexIsPartial(def string) bool {
	lower := strings.ToLower(def)
	return strings.Contains(lower, "where") && strings.Contains(lower, "kernel_id")
}

func b4Inbound(kernelID, nodeID int64, host string, port int) *model.NodeInbound {
	return &model.NodeInbound{
		KernelID:    kernelID,
		NodeID:      nodeID,
		Name:        "n",
		Protocol:    "trojan",
		Listen:      host,
		Port:        port,
		Enabled:     1,
		CreatedTime: 1,
		UpdatedTime: 1,
	}
}

// Fresh database: after the full migration chain the node_inbound unique
// index must be partial, two imported nodes may share a port (different
// hosts), and two kernel inbounds on the same node+port must still clash.
func TestNodeInboundPartialUniqueFreshDB(t *testing.T) {
	db := openB4FileDB(t)
	if err := autoMigrateAll(db); err != nil {
		t.Fatalf("autoMigrateAll: %v", err)
	}
	def := nodeInboundIndexDef(t, db)
	if def == "" {
		t.Fatal("idx_node_inbound_node_port missing on fresh DB")
	}
	if !indexIsPartial(def) {
		t.Fatalf("fresh DB index must be partial (WHERE kernel_id > 0), got: %s", def)
	}
	if err := db.Create(b4Inbound(0, 0, "a.example.com", 443)).Error; err != nil {
		t.Fatalf("first imported node: %v", err)
	}
	if err := db.Create(b4Inbound(0, 0, "b.example.com", 443)).Error; err != nil {
		t.Fatalf("second imported node on same port but different host must succeed (F-003): %v", err)
	}
	if err := db.Create(b4Inbound(7, 3, "203.0.113.1", 443)).Error; err != nil {
		t.Fatalf("first kernel inbound: %v", err)
	}
	if err := db.Create(b4Inbound(7, 3, "203.0.113.1", 443)).Error; err == nil {
		t.Fatal("second kernel inbound with same node_id+port must still violate uniqueness")
	}
}

// Existing database where v1 already created the FULL unique index:
// the upgrade must convert it to the partial definition in place
// (migration v2: explicit DROP + CREATE; the v1 name-exists idempotency
// check must not leave the old definition behind — FINAL-REVIEW 修正 3).
func TestNodeInboundPartialUniqueUpgradesExistingFullIndex(t *testing.T) {
	db := openB4FileDB(t)
	// Hand-craft the pre-fix state: tables, old full index, v1 recorded.
	if err := db.AutoMigrate(&model.ForwardPort{}, &model.ChainTunnel{}, &model.NodeInbound{}, &model.User{}, &schemaMigration{}); err != nil {
		t.Fatalf("craft tables: %v", err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX idx_node_inbound_node_port ON node_inbound (node_id, port)`).Error; err != nil {
		t.Fatalf("craft old index: %v", err)
	}
	if err := db.Create(&schemaMigration{Version: migrationV1Version, AppliedAt: 1}).Error; err != nil {
		t.Fatalf("craft v1 record: %v", err)
	}
	if err := db.Create(b4Inbound(7, 3, "203.0.113.1", 443)).Error; err != nil {
		t.Fatalf("seed kernel inbound: %v", err)
	}
	if err := db.Create(b4Inbound(0, 0, "a.example.com", 443)).Error; err != nil {
		t.Fatalf("seed imported inbound: %v", err)
	}

	if err := autoMigrateAll(db); err != nil {
		t.Fatalf("autoMigrateAll (upgrade path): %v", err)
	}
	def := nodeInboundIndexDef(t, db)
	if !indexIsPartial(def) {
		t.Fatalf("existing full index must be converted to partial on upgrade, got: %s", def)
	}
	if err := db.Create(b4Inbound(0, 0, "b.example.com", 443)).Error; err != nil {
		t.Fatalf("imported node sharing port after upgrade must succeed (F-003): %v", err)
	}
	if err := db.Create(b4Inbound(7, 3, "203.0.113.1", 443)).Error; err == nil {
		t.Fatal("kernel inbound duplicate must still be rejected after upgrade")
	}
}

// Existing database where v1 never applied because its precheck counted
// imported-node duplicates: the precheck for node_inbound must only count
// kernel-managed rows, otherwise one imported duplicate blocks all four
// indexes forever.
func TestNodeInboundPrecheckIgnoresImportedDuplicates(t *testing.T) {
	db := openB4FileDB(t)
	if err := db.AutoMigrate(&model.ForwardPort{}, &model.ChainTunnel{}, &model.NodeInbound{}, &model.User{}, &schemaMigration{}); err != nil {
		t.Fatalf("craft tables: %v", err)
	}
	// Two imported nodes sharing a port, no index yet, v1 not recorded.
	if err := db.Create(b4Inbound(0, 0, "a.example.com", 443)).Error; err != nil {
		t.Fatalf("seed imported A: %v", err)
	}
	if err := db.Create(b4Inbound(0, 0, "b.example.com", 443)).Error; err != nil {
		t.Fatalf("seed imported B: %v", err)
	}

	if err := autoMigrateAll(db); err != nil {
		t.Fatalf("autoMigrateAll: %v", err)
	}
	def := nodeInboundIndexDef(t, db)
	if def == "" {
		t.Fatal("idx_node_inbound_node_port must be created even when imported duplicates exist")
	}
	if !indexIsPartial(def) {
		t.Fatalf("index must be partial, got: %s", def)
	}
	var n int64
	if err := db.Model(&model.NodeInbound{}).Count(&n).Error; err != nil || n != 2 {
		t.Fatalf("imported rows must survive migration, count=%d err=%v", n, err)
	}
}
