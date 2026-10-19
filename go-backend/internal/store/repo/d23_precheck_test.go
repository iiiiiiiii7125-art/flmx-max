package repo

import (
	"path/filepath"
	"testing"
)

// R5-B4C D23: 迁移预检测试。

func TestD23_PrecheckWithDups(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "d23_test.db")
	r, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// 重置迁移状态（Open 已自动执行，需回滚以测试预检）。
	r.db.Exec("DELETE FROM schema_migration WHERE version = ?", migrationV1Version)
	for _, idx := range []string{"idx_forward_port_node_port", "idx_chain_tunnel_node_port", "idx_node_inbound_node_port", "idx_user_username_unique"} {
		r.db.Exec("DROP INDEX IF EXISTS " + idx)
	}

	// 构造重复数据。
	r.db.Exec(`INSERT INTO forward_port (forward_id, node_id, port) VALUES (1, 1, 8080), (2, 1, 8080)`)
	r.db.Exec(`INSERT INTO "user" ("user", pwd, role_id, exp_time, flow, num, status, max_conn, created_time) VALUES ('dup', 'h', 1, 0, 0, 0, 1, 0, 0), ('dup', 'h', 1, 0, 0, 0, 1, 0, 0)`)

	// 运行迁移：应跳过建索引，但不报错（面板正常启动）。
	if err := migrateV1PortUserUnique(r.db); err != nil {
		t.Fatalf("migration should not fail: %v", err)
	}

	// 索引未建。
	var count int64
	r.db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_forward_port_node_port'").Scan(&count)
	if count != 0 {
		t.Error("index should NOT be created when dups exist")
	}

	// 版本未记录（下次启动重试）。
	var m schemaMigration
	err = r.db.Where("version = ?", migrationV1Version).First(&m).Error
	if err == nil {
		t.Error("version should NOT be recorded when precheck fails")
	}
}

func TestD23_PrecheckClean(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "d23_clean.db")
	r, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// 正常库：直接建成。
	if err := migrateV1PortUserUnique(r.db); err != nil {
		t.Fatalf("migration: %v", err)
	}

	var count int64
	r.db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_forward_port_node_port'").Scan(&count)
	if count != 1 {
		t.Error("index should be created for clean DB")
	}
}
