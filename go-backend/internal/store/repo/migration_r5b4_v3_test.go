package repo

import (
	"database/sql"
	"testing"

	"go-backend/internal/store/model"
	"gorm.io/gorm"
)

// R18-C1-01: v1 初版（f1d94f48）建的是全量唯一索引
// idx_chain_tunnel_node_port (node_id, port)；A3-S1（2455ccd7）才
// 改为 WHERE port > 0 的部分索引。但 v1 的幂等是按索引名 EXISTS
// 跳过，永不核对定义——窗口期跑过 v1 的升级库永远留着全量索引，
// port=0（“未定端口”）的链路行在备份恢复时撞唯一约束、整单回滚。
// v3 迁移仿照 v2：定义已是 partial → 记版本跳过；是全量 → 预检
// port>0 无重复后显式 DROP + CREATE 转换。

func chainTunnelIndexDef(t *testing.T, db *gorm.DB) string {
	t.Helper()
	var def string
	if err := db.Raw(`SELECT sql FROM sqlite_master WHERE type='index' AND name='idx_chain_tunnel_node_port'`).Scan(&def).Error; err != nil {
		t.Fatalf("read index definition: %v", err)
	}
	return def
}

func b3ChainTunnel(nodeID, port int64, portValid bool) *model.ChainTunnel {
	return &model.ChainTunnel{
		TunnelID:  1,
		ChainType: "tcp",
		NodeID:    nodeID,
		Port:      sql.NullInt64{Int64: port, Valid: portValid},
	}
}

func migrationVersionRecorded(t *testing.T, db *gorm.DB, version string) bool {
	t.Helper()
	var n int64
	if err := db.Model(&schemaMigration{}).Where("version = ?", version).Count(&n).Error; err != nil {
		t.Fatalf("count migration %s: %v", version, err)
	}
	return n > 0
}

// Fresh database: after the full migration chain the chain_tunnel
// unique index must be partial, two same-node port=0 rows may coexist
// (both mean "port undecided"), and two same-node port>0 rows must
// still clash.
func TestChainTunnelPartialUniqueFreshDB(t *testing.T) {
	db := openB4FileDB(t)
	if err := autoMigrateAll(db); err != nil {
		t.Fatalf("autoMigrateAll: %v", err)
	}
	def := chainTunnelIndexDef(t, db)
	if def == "" {
		t.Fatal("idx_chain_tunnel_node_port missing on fresh DB")
	}
	if !chainTunnelIndexIsPartial(def) {
		t.Fatalf("fresh DB index must be partial (WHERE port > 0), got: %s", def)
	}
	if err := db.Create(b3ChainTunnel(7, 0, true)).Error; err != nil {
		t.Fatalf("first port=0 row: %v", err)
	}
	if err := db.Create(b3ChainTunnel(7, 0, true)).Error; err != nil {
		t.Fatalf("second same-node port=0 row must succeed (undecided port): %v", err)
	}
	if err := db.Create(b3ChainTunnel(7, 0, false)).Error; err != nil {
		t.Fatalf("same-node NULL port row must succeed: %v", err)
	}
	if err := db.Create(b3ChainTunnel(7, 5, true)).Error; err != nil {
		t.Fatalf("first port=5 row: %v", err)
	}
	if err := db.Create(b3ChainTunnel(7, 5, true)).Error; err == nil {
		t.Fatal("second same-node port=5 row must still violate uniqueness")
	}
	if !migrationVersionRecorded(t, db, migrationV3Version) {
		t.Fatalf("migration %s must be recorded on fresh DB", migrationV3Version)
	}
}

// Existing database where v1 already created the FULL unique index:
// the upgrade must convert it to the partial definition in place
// (migration v3: explicit DROP + CREATE; the v1 name-exists idempotency
// check must not leave the old definition behind).
func TestChainTunnelPartialUniqueUpgradesExistingFullIndex(t *testing.T) {
	db := openB4FileDB(t)
	// Hand-craft the pre-fix state: tables, old full index, v1 recorded.
	if err := db.AutoMigrate(&model.ForwardPort{}, &model.ChainTunnel{}, &model.NodeInbound{}, &model.User{}, &schemaMigration{}); err != nil {
		t.Fatalf("craft tables: %v", err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX idx_chain_tunnel_node_port ON chain_tunnel (node_id, port)`).Error; err != nil {
		t.Fatalf("craft old index: %v", err)
	}
	if err := db.Create(&schemaMigration{Version: migrationV1Version, AppliedAt: 1}).Error; err != nil {
		t.Fatalf("craft v1 record: %v", err)
	}
	if err := db.Create(b3ChainTunnel(7, 0, true)).Error; err != nil {
		t.Fatalf("seed port=0 row: %v", err)
	}
	if err := db.Create(b3ChainTunnel(7, 5, true)).Error; err != nil {
		t.Fatalf("seed port=5 row: %v", err)
	}

	if err := autoMigrateAll(db); err != nil {
		t.Fatalf("autoMigrateAll (upgrade path): %v", err)
	}
	def := chainTunnelIndexDef(t, db)
	if !chainTunnelIndexIsPartial(def) {
		t.Fatalf("existing full index must be converted to partial on upgrade, got: %s", def)
	}
	if err := db.Create(b3ChainTunnel(7, 0, true)).Error; err != nil {
		t.Fatalf("second same-node port=0 row after upgrade must succeed (R18-C1-01): %v", err)
	}
	if err := db.Create(b3ChainTunnel(7, 5, true)).Error; err == nil {
		t.Fatal("port=5 duplicate must still be rejected after upgrade")
	}
	var n int64
	if err := db.Model(&model.ChainTunnel{}).Count(&n).Error; err != nil || n != 3 {
		t.Fatalf("chain rows = %d, want 3 surviving (2 seeded + 2nd port=0; port=5 duplicate rejected)", n)
	}
	if !migrationVersionRecorded(t, db, migrationV3Version) {
		t.Fatalf("migration %s must be recorded after conversion", migrationV3Version)
	}
}

// Existing database where port>0 duplicates already exist: v3 must
// skip the conversion (D23), record no version, and never block
// startup — the next boot retries after cleanup.
func TestChainTunnelPartialUniqueSkipsOnPortDuplicates(t *testing.T) {
	db := openB4FileDB(t)
	if err := db.AutoMigrate(&model.ForwardPort{}, &model.ChainTunnel{}, &model.NodeInbound{}, &model.User{}, &schemaMigration{}); err != nil {
		t.Fatalf("craft tables: %v", err)
	}
	if err := db.Create(b3ChainTunnel(7, 5, true)).Error; err != nil {
		t.Fatalf("seed port=5 A: %v", err)
	}
	if err := db.Create(b3ChainTunnel(7, 5, true)).Error; err != nil {
		t.Fatalf("seed port=5 B: %v", err)
	}

	if err := autoMigrateAll(db); err != nil {
		t.Fatalf("autoMigrateAll must not fail on duplicate data: %v", err)
	}
	if def := chainTunnelIndexDef(t, db); def != "" {
		t.Fatalf("index must not be created while port>0 duplicates exist, got: %s", def)
	}
	if migrationVersionRecorded(t, db, migrationV3Version) {
		t.Fatalf("migration %s must not be recorded while duplicates exist", migrationV3Version)
	}
	// Retry path: a second boot still starts cleanly.
	if err := autoMigrateAll(db); err != nil {
		t.Fatalf("second autoMigrateAll must not fail: %v", err)
	}
}
