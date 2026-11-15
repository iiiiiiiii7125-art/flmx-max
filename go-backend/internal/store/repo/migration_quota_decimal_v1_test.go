package repo

import (
	"path/filepath"
	"testing"
)

// C-(6)③：配额小数化迁移 quota_decimal_v1 —— SQLite 仅记版本
// （列类型亲和灵活无需改列），PostgreSQL 两列改 double precision。
// 本测试只覆盖 SQLite 面（无 PG DSN 时的默认路径）：版本行在
// Open 时落库、重复执行幂等、版本行不重复。
func TestMigrationQuotaDecimalV1Idempotent(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "quota-decimal-v1.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	var count int64
	if err := r.DB().Model(&schemaMigration{}).Where("version = ?", migrationQuotaDecimalV1Version).Count(&count).Error; err != nil {
		t.Fatalf("count migration rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("migration %s must be recorded exactly once on open, got %d rows", migrationQuotaDecimalV1Version, count)
	}
	if err := migrateV1QuotaDecimal(r.DB()); err != nil {
		t.Fatalf("second run of %s must be a no-op, got %v", migrationQuotaDecimalV1Version, err)
	}
	if err := r.DB().Model(&schemaMigration{}).Where("version = ?", migrationQuotaDecimalV1Version).Count(&count).Error; err != nil {
		t.Fatalf("count migration rows after second run: %v", err)
	}
	if count != 1 {
		t.Fatalf("migration %s must stay at exactly one row after a second run, got %d", migrationQuotaDecimalV1Version, count)
	}
}
