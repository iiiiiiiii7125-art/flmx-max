package repo

import (
	"testing"

	"go-backend/internal/store/model"
)

// DEV-RM-SHARE v6 迁移回归：把新装库手工改造成带联邦存量的旧库
// （五张联邦表 + node 远端四列 + 一台远端节点），跑迁移后断言
// 表/列清净、远端节点被删、本地节点完好、版本已记、二跑幂等。
func TestMigrateV6FederationRemoval(t *testing.T) {
	r, err := Open(t.TempDir() + "/rmshare-v6.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	db := r.DB()

	// 伪造存量：node 远端四列。
	for _, col := range []string{
		"is_remote INTEGER DEFAULT 0",
		"remote_url TEXT DEFAULT ''",
		"remote_token TEXT DEFAULT ''",
		"remote_config TEXT DEFAULT ''",
	} {
		if err := db.Exec("ALTER TABLE node ADD COLUMN " + col).Error; err != nil {
			t.Fatalf("add legacy column %s: %v", col, err)
		}
	}
	// 伪造存量：五张联邦表。
	for _, table := range federationRemovalTables {
		if err := db.Exec("CREATE TABLE " + table + " (id INTEGER PRIMARY KEY)").Error; err != nil {
			t.Fatalf("create legacy table %s: %v", table, err)
		}
	}
	if err := db.Exec("INSERT INTO peer_share (id) VALUES (1)").Error; err != nil {
		t.Fatalf("seed peer_share: %v", err)
	}
	// 伪造存量：一台本地节点 + 一台远端节点。
	if err := db.Exec("INSERT INTO node (name, secret, server_ip, port, created_time, updated_time, status, is_remote) VALUES ('v6-local', 's-local', '10.0.0.1', '1000-2000', 1, 1, 1, 0)").Error; err != nil {
		t.Fatalf("seed local node: %v", err)
	}
	if err := db.Exec("INSERT INTO node (name, secret, server_ip, port, created_time, updated_time, status, is_remote, remote_url, remote_token) VALUES ('v6-remote', 's-remote', '10.0.0.2', '1000-2000', 1, 1, 1, 1, 'http://peer.example', 'tok')").Error; err != nil {
		t.Fatalf("seed remote node: %v", err)
	}
	// 新装流程已在 Open 时记过 v6 版本；删记录以便重跑迁移本体。
	if err := db.Exec("DELETE FROM schema_migration WHERE version = ?", migrationRMShareV6Version).Error; err != nil {
		t.Fatalf("clear v6 version record: %v", err)
	}

	if err := migrateV6FederationRemoval(db); err != nil {
		t.Fatalf("migrateV6FederationRemoval: %v", err)
	}

	for _, table := range federationRemovalTables {
		if db.Migrator().HasTable(table) {
			t.Errorf("legacy federation table %s still present after v6 migration", table)
		}
	}
	for _, col := range nodeRemoteColumns {
		if db.Migrator().HasColumn(&model.Node{}, col) {
			t.Errorf("node column %s still present after v6 migration", col)
		}
	}
	var nodeCount int64
	if err := db.Table("node").Count(&nodeCount).Error; err != nil {
		t.Fatalf("count nodes: %v", err)
	}
	if nodeCount != 1 {
		t.Fatalf("node rows after migration = %d, want 1 (local kept, remote removed)", nodeCount)
	}
	var name string
	if err := db.Table("node").Pluck("name", &name).Error; err != nil {
		t.Fatalf("read surviving node: %v", err)
	}
	if name != "v6-local" {
		t.Fatalf("surviving node = %q, want v6-local", name)
	}
	var versionCount int64
	if err := db.Table("schema_migration").Where("version = ?", migrationRMShareV6Version).Count(&versionCount).Error; err != nil {
		t.Fatalf("count version records: %v", err)
	}
	if versionCount != 1 {
		t.Fatalf("v6 version records = %d, want 1", versionCount)
	}

	// 幂等：二跑直接按版本记录跳过、无错误。
	if err := migrateV6FederationRemoval(db); err != nil {
		t.Fatalf("second migrateV6FederationRemoval run: %v", err)
	}
}
