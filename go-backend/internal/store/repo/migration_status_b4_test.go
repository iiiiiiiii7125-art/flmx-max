package repo

import (
	"testing"
)

// F-009 (B4): 迁移状态接口必须对 v2 也说实话。v1 已应用但
// node_inbound 索引仍是全量唯一（v2 未落地）时，旧实现被 v1 的
// 「已应用」短路，恒报无待迁移，健康检查永远显示正常。
func TestPendingMigrationStatusSeesV2(t *testing.T) {
	r := openCascadeRepo(t, "status.db")

	// Open 已跑完 v1+v2：索引为部分唯一，无待迁移。
	if s := r.GetPendingMigrationStatus(); s.Required {
		t.Fatalf("fresh migrated DB must report no pending migration, got %+v", s)
	}

	// 模拟存量卡住态：索引退回全量定义 + 删除 v2 版本记录。
	if err := r.DB().Exec(`DROP INDEX idx_node_inbound_node_port`).Error; err != nil {
		t.Fatalf("drop index: %v", err)
	}
	if err := r.DB().Exec(`CREATE UNIQUE INDEX idx_node_inbound_node_port ON node_inbound (node_id, port)`).Error; err != nil {
		t.Fatalf("recreate full index: %v", err)
	}
	if err := r.DB().Exec(`DELETE FROM schema_migration WHERE version = ?`, migrationV2Version).Error; err != nil {
		t.Fatalf("delete v2 record: %v", err)
	}
	s := r.GetPendingMigrationStatus()
	if !s.Required {
		t.Fatal("F-009: full (non-partial) node_inbound index with v2 unapplied must be reported as pending")
	}
	found := false
	for _, tbl := range s.Tables {
		if tbl.Table == "node_inbound" {
			found = true
		}
	}
	if !found {
		t.Fatalf("pending detail must name node_inbound, got %+v", s.Tables)
	}
}
