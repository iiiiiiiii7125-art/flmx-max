package repo

import (
	"slices"
	"testing"
)

// C1-01: 迁移状态面必须对 v3/v4 也说实话。基线只查 v1/v2/v5：
// v3 被预检挡下（不记版本）时复合唯一索引长期缺失而状态恒报
// 无待迁移；PendingMigrations() 还恒回 v1 名误标。本组用例以
// 索引手术模拟卡住态，逐项钉住状态面与兼容接口的真实回报。

func TestPendingMigrationStatusSeesV4(t *testing.T) {
	r := openCascadeRepo(t, "status-v4.db")

	if s := r.GetPendingMigrationStatus(); s.Required {
		t.Fatalf("fresh migrated DB must report no pending migration, got %+v", s)
	}

	// 模拟 v4 卡住态：删一个 v4 索引 + 删除 v4 记录。
	if err := r.DB().Exec(`DROP INDEX idx_forward_port_forward_id`).Error; err != nil {
		t.Fatalf("drop v4 index: %v", err)
	}
	if err := r.DB().Exec(`DELETE FROM schema_migration WHERE version = ?`, migrationC12V4Version).Error; err != nil {
		t.Fatalf("delete v4 record: %v", err)
	}

	s := r.GetPendingMigrationStatus()
	if !s.Required {
		t.Fatal("C1-01: missing v4 lookup index with v4 unapplied must be reported as pending")
	}
	pm := r.PendingMigrations()
	if !slices.Contains(pm, migrationC12V4Version) {
		t.Fatalf("PendingMigrations must name the v4 version, got %v", pm)
	}
	if slices.Contains(pm, migrationV1Version) {
		t.Fatalf("PendingMigrations must not mislabel v4-pending as v1, got %v", pm)
	}
}

// PendingMigrations 对 v2 卡住态也须回 v2 实名而非 v1。
func TestPendingMigrationsNamesV2(t *testing.T) {
	r := openCascadeRepo(t, "status-v2-names.db")
	if err := r.DB().Exec(`DROP INDEX idx_node_inbound_node_port`).Error; err != nil {
		t.Fatalf("drop index: %v", err)
	}
	if err := r.DB().Exec(`CREATE UNIQUE INDEX idx_node_inbound_node_port ON node_inbound (node_id, port)`).Error; err != nil {
		t.Fatalf("recreate full index: %v", err)
	}
	if err := r.DB().Exec(`DELETE FROM schema_migration WHERE version = ?`, migrationV2Version).Error; err != nil {
		t.Fatalf("delete v2 record: %v", err)
	}
	pm := r.PendingMigrations()
	if !slices.Contains(pm, migrationV2Version) {
		t.Fatalf("PendingMigrations must name the v2 version, got %v", pm)
	}
	if slices.Contains(pm, migrationV1Version) {
		t.Fatalf("PendingMigrations must not mislabel v2-pending as v1, got %v", pm)
	}
}
