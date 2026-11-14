package repo

import (
	"testing"
)

// RM-ANN v1 迁移回归：把新装库手工改造成带公告存量的旧库
// （announcement 表 + F 路 rig 同款 1 行空记录），跑迁移后断言
// 表消失、版本已记、二跑幂等、其余表完好；新装库直接记版本跳过。
func TestMigrateV1AnnouncementTableRemoved(t *testing.T) {
	r, err := Open(t.TempDir() + "/rmann-v1.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	db := r.DB()

	// 新装流程已在 Open 时记过 v1 版本；删记录以便重跑迁移本体。
	if err := db.Exec("DELETE FROM schema_migration WHERE version = ?", migrationRMAnnV1Version).Error; err != nil {
		t.Fatalf("clear v1 version record: %v", err)
	}
	// 伪造存量：announcement 表 + 1 行空记录（U1 rig 同款
	// id=1 content='' enabled=0）。
	if err := db.Exec(`CREATE TABLE announcement (
		id INTEGER PRIMARY KEY, content TEXT, enabled INTEGER,
		created_time INTEGER, updated_time INTEGER)`).Error; err != nil {
		t.Fatalf("create legacy announcement table: %v", err)
	}
	if err := db.Exec(`INSERT INTO announcement(id, content, enabled, created_time, updated_time)
		VALUES (1, '', 0, 1, 1)`).Error; err != nil {
		t.Fatalf("seed announcement row: %v", err)
	}
	// 伪造对照：node 表留一行，迁移不得波及（只用当前 schema
	// 已有的列；is_remote 等联邦列早已随 v6 迁移下线，不在表里）。
	if err := db.Exec(`INSERT INTO node (name, secret, server_ip, port, created_time, updated_time, status)
		VALUES ('v1-local', 's-local', '10.0.0.1', '1000-2000', 1, 1, 1)`).Error; err != nil {
		t.Fatalf("seed local node: %v", err)
	}

	if err := migrateV1AnnouncementTableRemoved(db); err != nil {
		t.Fatalf("migrateV1AnnouncementTableRemoved: %v", err)
	}

	if db.Migrator().HasTable("announcement") {
		t.Errorf("legacy announcement table still present after v1 migration")
	}
	var versionCount int64
	if err := db.Table("schema_migration").Where("version = ?", migrationRMAnnV1Version).Count(&versionCount).Error; err != nil {
		t.Fatalf("count migration version: %v", err)
	}
	if versionCount != 1 {
		t.Fatalf("migration version rows = %d, want 1", versionCount)
	}
	// 二跑幂等：无表可删仍成功、版本不重复记。
	if err := migrateV1AnnouncementTableRemoved(db); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if db.Migrator().HasTable("announcement") {
		t.Errorf("announcement table reappeared after second run")
	}
	if err := db.Table("schema_migration").Where("version = ?", migrationRMAnnV1Version).Count(&versionCount).Error; err != nil {
		t.Fatalf("count migration version after second run: %v", err)
	}
	if versionCount != 1 {
		t.Fatalf("migration version rows after second run = %d, want 1", versionCount)
	}
	var nodeCount int64
	if err := db.Table("node").Count(&nodeCount).Error; err != nil {
		t.Fatalf("count nodes: %v", err)
	}
	if nodeCount != 1 {
		t.Fatalf("node rows after migration = %d, want 1 (untouched)", nodeCount)
	}
}

// 新装库：Open 即记 v1 版本、announcement 表从不存在、二次 Open
// 同样干净（AutoMigrate 不会把已删模型的表建回来）。
func TestMigrateV1AnnouncementFreshDB(t *testing.T) {
	path := t.TempDir() + "/rmann-v1-fresh.db"
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if r.DB().Migrator().HasTable("announcement") {
		t.Fatalf("fresh DB must not have an announcement table")
	}
	var versionCount int64
	if err := r.DB().Table("schema_migration").Where("version = ?", migrationRMAnnV1Version).Count(&versionCount).Error; err != nil {
		t.Fatalf("count migration version: %v", err)
	}
	if versionCount != 1 {
		t.Fatalf("fresh DB migration version rows = %d, want 1", versionCount)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	r2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = r2.Close() })
	if r2.DB().Migrator().HasTable("announcement") {
		t.Fatalf("announcement table reappeared after reopen")
	}
}
