package repo

import (
	"path/filepath"
	"testing"

	gsqlite "github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// X01-002 回归：schema_version 读写失败不得再被吞成「版本 0」或
// 「已推进」。旧实现下：getSchemaVersion 对任何 First 错误都建行
// 回 0（读失败伪装成空表、触发全量重跑），setSchemaVersion 的
// Update 错误无声消失，migrateSchema 无条件 return nil。本文件
// 以注入故障（表缺失 / 列损坏 / 空表写）逐面锁定错误必须上抛，
// 且正常路径（空表建行、版本推进、migrateSchema 全程）不回归。

func x01BareDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(gsqlite.Open(":memory:"), &gorm.Config{
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

func x01CreateVersionTable(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec(`CREATE TABLE schema_version (version INTEGER NOT NULL DEFAULT 0)`).Error; err != nil {
		t.Fatalf("create schema_version: %v", err)
	}
}

// 正常路径：空表读回 0 并建行；推进后可读回新值；重复推进幂等。
func TestX01_SchemaVersionRoundTrip(t *testing.T) {
	db := x01BareDB(t)
	x01CreateVersionTable(t, db)

	ver, err := getSchemaVersion(db)
	if err != nil {
		t.Fatalf("getSchemaVersion on empty table: %v", err)
	}
	if ver != 0 {
		t.Fatalf("empty table version = %d, want 0", ver)
	}
	if err := setSchemaVersion(db, 3); err != nil {
		t.Fatalf("setSchemaVersion: %v", err)
	}
	ver, err = getSchemaVersion(db)
	if err != nil {
		t.Fatalf("getSchemaVersion after set: %v", err)
	}
	if ver != 3 {
		t.Fatalf("version after set = %d, want 3", ver)
	}
}

// 读故障注入（一）：表缺失。旧实现把 "no such table" 吞成版本 0。
func TestX01_GetSchemaVersionMissingTableErrors(t *testing.T) {
	db := x01BareDB(t)
	if _, err := getSchemaVersion(db); err == nil {
		t.Fatal("getSchemaVersion with missing table must return an error, got nil (masked as version 0)")
	}
}

// 读故障注入（二）：表在但列损坏（无 version 列）。First 的错误
// 不是 ErrRecordNotFound，必须上抛、不得走建行回 0 分支。
func TestX01_GetSchemaVersionCorruptTableErrors(t *testing.T) {
	db := x01BareDB(t)
	if err := db.Exec(`CREATE TABLE schema_version (bogus TEXT)`).Error; err != nil {
		t.Fatalf("create corrupt schema_version: %v", err)
	}
	if _, err := getSchemaVersion(db); err == nil {
		t.Fatal("getSchemaVersion with corrupt table must return an error, got nil")
	}
}

// 写故障注入（一）：表缺失时 Update 必然失败，必须上抛。
func TestX01_SetSchemaVersionMissingTableErrors(t *testing.T) {
	db := x01BareDB(t)
	if err := setSchemaVersion(db, 6); err == nil {
		t.Fatal("setSchemaVersion with missing table must return an error, got nil (silently no-oped)")
	}
}

// 写故障注入（二）：表在但无行——0 行更新等于没推进，必须报错，
// 不得伪装成已落库。
func TestX01_SetSchemaVersionNoRowErrors(t *testing.T) {
	db := x01BareDB(t)
	x01CreateVersionTable(t, db)
	if err := setSchemaVersion(db, 6); err == nil {
		t.Fatal("setSchemaVersion with no row to update must return an error, got nil")
	}
}

// 链路级：schema_version 表损坏时 migrateSchema 必须报错（旧实现
// 无条件报完成）；完好库上 migrateSchema 正常跑完并把版本推进到
// currentSchemaVersion。
func TestX01_MigrateSchemaPropagatesSchemaVersionFailure(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "x01-migrate.db")

	good, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	ver, err := getSchemaVersion(good.db)
	if err != nil {
		t.Fatalf("getSchemaVersion on healthy repo: %v", err)
	}
	if ver != currentSchemaVersion {
		t.Fatalf("healthy repo schema version = %d, want %d", ver, currentSchemaVersion)
	}
	if err := good.Close(); err != nil {
		t.Fatalf("close repo: %v", err)
	}

	broken, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen repo: %v", err)
	}
	defer func() { _ = broken.Close() }()
	if err := broken.db.Migrator().DropTable("schema_version"); err != nil {
		t.Fatalf("drop schema_version: %v", err)
	}
	if err := migrateSchema(broken.db); err == nil {
		t.Fatal("migrateSchema with dropped schema_version table must return an error, got nil")
	}
}
