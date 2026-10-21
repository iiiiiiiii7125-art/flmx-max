package repo

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	gsqlite "github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"go-backend/internal/store/model"
)

// FIX132-C 第 4/5/6 项回归：R4-P1 默认值渲染、A3-S1/S2 双言迁移
// 顺序、A3-06 创建原子性、A3-09 唯一冲突识别精度。

// R4-P1：release_legacy_family 的 SQLite 默认渲染不得再是
// `DEFAULT false`——pgloader 把 numeric DEFAULT 'false' 翻译到 PG
// 时以 22P02 拒绝，整条官方迁移路径在建表期 FATAL（R4 在 PG 16
// 实测，全库 49 表仅此 1 列）。
func TestFix132PeerShareResourceDefaultRendersNumeric(t *testing.T) {
	r := openBackupRepo(t, "psr-ddl.db")
	var ddl string
	if err := r.DB().Raw(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'peer_share_resource'`).Scan(&ddl).Error; err != nil {
		t.Fatalf("read ddl: %v", err)
	}
	if ddl == "" {
		t.Fatal("peer_share_resource table must exist")
	}
	if strings.Contains(strings.ToLower(ddl), "default false") {
		t.Fatalf("R4-P1: release_legacy_family must not render DEFAULT false (pgloader 22P02), ddl: %s", ddl)
	}
	// 应用层 bool 语义不变：缺省落 false、显式 true 读回 true。
	plain := model.PeerShareResource{ShareID: 1, NodeID: 1, Kind: "service", OriginalName: "a", RuntimeName: "a", UpdatedTime: 1}
	createRow(t, r, &plain)
	if plain.ReleaseLegacyFamily {
		t.Fatal("default ReleaseLegacyFamily must be false")
	}
	flagged := model.PeerShareResource{ShareID: 1, NodeID: 1, Kind: "relay", OriginalName: "b", RuntimeName: "b", UpdatedTime: 1, ReleaseLegacyFamily: true}
	createRow(t, r, &flagged)
	var back model.PeerShareResource
	if err := r.DB().Where("id = ?", flagged.ID).First(&back).Error; err != nil {
		t.Fatalf("reload flagged row: %v", err)
	}
	if !back.ReleaseLegacyFamily {
		t.Fatal("explicit true must round-trip")
	}
}

// R4-P1 存量兼容：旧 DDL（numeric NOT NULL DEFAULT false）建出的
// 存量表，GORM 读 bool 不受影响（1→true、0→false）。
func TestFix132PeerShareResourceLegacyNumericColumnReadable(t *testing.T) {
	db, err := gorm.Open(gsqlite.Open(filepath.Join(t.TempDir(), "legacy-psr.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open scratch db: %v", err)
	}
	// 旧渲染逐字复刻（R4 在存量库 sqlite_master 中实测的形态）。
	if err := db.Exec(`CREATE TABLE peer_share_resource (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		share_id INTEGER NOT NULL, node_id INTEGER NOT NULL,
		kind TEXT NOT NULL, original_name TEXT NOT NULL, runtime_name TEXT NOT NULL,
		legacy_names TEXT NOT NULL DEFAULT '', legacy_service_base TEXT NOT NULL DEFAULT '',
		release_legacy_family numeric NOT NULL DEFAULT false,
		config TEXT NOT NULL DEFAULT '', desired_state TEXT NOT NULL DEFAULT 'active',
		applied INTEGER NOT NULL DEFAULT 0, updated_time INTEGER NOT NULL
	)`).Error; err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if err := db.Exec(`INSERT INTO peer_share_resource(share_id, node_id, kind, original_name, runtime_name, updated_time, release_legacy_family)
		VALUES(1, 1, 'service', 'a', 'a', 1, 1), (2, 1, 'service', 'b', 'b', 1, 0)`).Error; err != nil {
		t.Fatalf("seed legacy rows: %v", err)
	}
	var rows []model.PeerShareResource
	if err := db.Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatalf("read legacy rows: %v", err)
	}
	if len(rows) != 2 || !rows[0].ReleaseLegacyFamily || rows[1].ReleaseLegacyFamily {
		t.Fatalf("legacy numeric column must scan into bool, got %+v", rows)
	}
}

// A3-S1（双言同病，SQLite 探针）：chain_tunnel 两行同节点 port=0
// （有效值 0、非 NULL）时，v1 预检（port > 0 口径）放行、原全量
// 唯一索引建索引必 23505/UNIQUE 失败——迁移顺序口径必须一致：
// 索引改为部分索引 WHERE port > 0，与预检同口径。
// PG 路径同文件同 SQL 构造（部分索引两方言均支持），逻辑同构。
func TestFix132ChainTunnelPortZeroDuplicatesDoNotBlockV1Migration(t *testing.T) {
	r := openBackupRepo(t, "v1-port0.db")
	db := r.DB()
	// 还原「v1 未应用、无唯一索引」的存量形态。
	if err := db.Exec(`DROP INDEX IF EXISTS idx_chain_tunnel_node_port`).Error; err != nil {
		t.Fatalf("drop index: %v", err)
	}
	if err := db.Exec(`DELETE FROM schema_migration WHERE version = ?`, migrationV1Version).Error; err != nil {
		t.Fatalf("reset migration record: %v", err)
	}
	if err := db.Exec(`INSERT INTO chain_tunnel(tunnel_id, chain_type, node_id, port)
		VALUES(11, '1', 1, 0), (12, '1', 1, 0), (13, '1', 1, 12000)`).Error; err != nil {
		t.Fatalf("seed chain rows: %v", err)
	}
	if err := migrateV1PortUserUnique(db); err != nil {
		t.Fatalf("A3-S1: port=0 duplicates must not fail the v1 migration: %v", err)
	}
	var indexSQL string
	if err := db.Raw(`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'idx_chain_tunnel_node_port'`).Scan(&indexSQL).Error; err != nil {
		t.Fatalf("read index ddl: %v", err)
	}
	if !strings.Contains(indexSQL, "port > 0") {
		t.Fatalf("A3-S1: chain index must be partial (WHERE port > 0) to match the precheck, got: %q", indexSQL)
	}
	// 部分索引仍须拦截真实端口重复（port > 0 的唯一性不放松）。
	if err := db.Exec(`INSERT INTO chain_tunnel(tunnel_id, chain_type, node_id, port) VALUES(14, '1', 1, 12000)`).Error; err == nil {
		t.Fatal("duplicate real port must still violate the unique index")
	}
}

// A3-S2（双言同病，SQLite 探针）：存量 tunnel_metric 含重复桶行且
// 无唯一索引时，启动序列必须先由去重迁移收口、再建唯一索引；
// AutoMigrate 不得抢先按模型标签建唯一索引把升级堵死。
// PG 路径同一启动序列（OpenPostgres 同序），逻辑同构。
func TestFix132TunnelMetricDuplicatesSurviveStartupSequence(t *testing.T) {
	db, err := gorm.Open(gsqlite.Open(filepath.Join(t.TempDir(), "tm-legacy.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open scratch db: %v", err)
	}
	// 存量形态：表在、无唯一索引、含重复 (tunnel, node, ts) 桶行。
	if err := db.Exec(`CREATE TABLE tunnel_metric (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		tunnel_id INTEGER NOT NULL, node_id INTEGER NOT NULL, timestamp INTEGER NOT NULL,
		bytes_in INTEGER, bytes_out INTEGER, connections INTEGER, errors INTEGER, avg_latency_ms REAL
	)`).Error; err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if err := db.Exec(`INSERT INTO tunnel_metric(tunnel_id, node_id, timestamp, bytes_in, bytes_out, connections, errors, avg_latency_ms)
		VALUES(7, 9, 1700000000, 100, 200, 3, 1, 10.0), (7, 9, 1700000000, 50, 60, 2, 0, 20.0)`).Error; err != nil {
		t.Fatalf("seed duplicate buckets: %v", err)
	}
	if err := autoMigrateAll(db); err != nil {
		t.Fatalf("A3-S2: autoMigrateAll must not fail on duplicate metric buckets: %v", err)
	}
	if err := migrateSchema(db); err != nil {
		t.Fatalf("A3-S2: migrateSchema must dedupe then index: %v", err)
	}
	var buckets int64
	if err := db.Raw(`SELECT COUNT(*) FROM tunnel_metric WHERE tunnel_id = 7 AND node_id = 9 AND timestamp = 1700000000`).Scan(&buckets).Error; err != nil {
		t.Fatalf("count buckets: %v", err)
	}
	if buckets != 1 {
		t.Fatalf("duplicate buckets must be merged to one row, got %d", buckets)
	}
	var bytesIn int64
	if err := db.Raw(`SELECT bytes_in FROM tunnel_metric WHERE tunnel_id = 7 AND node_id = 9 AND timestamp = 1700000000`).Scan(&bytesIn).Error; err != nil {
		t.Fatalf("read merged bucket: %v", err)
	}
	if bytesIn != 150 {
		t.Fatalf("dedupe must sum bytes_in (100+50), got %d", bytesIn)
	}
	var uidxCount int64
	if err := db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'uidx_tunnel_metric_bucket'`).Scan(&uidxCount).Error; err != nil {
		t.Fatalf("check unique index: %v", err)
	}
	if uidxCount != 1 {
		t.Fatal("the dedupe migration's unique index must exist after startup")
	}
	// 模型侧不得再声明同列唯一索引（与 uidx 双唯一冗余且抢跑）。
	var legacyIdx int64
	if err := db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_tunnel_metric_tunnel_time'`).Scan(&legacyIdx).Error; err != nil {
		t.Fatalf("check legacy index: %v", err)
	}
	if legacyIdx != 0 {
		t.Fatal("model-declared idx_tunnel_metric_tunnel_time must be gone; uidx_tunnel_metric_bucket is the single owner")
	}
}

// A3-06：CreateNodeInbound 的「建行 + enabled 写回」必须原子——
// 写回失败不得留下 enabled=1 的反意图行。用触发器强制 UPDATE 失败。
func TestFix132CreateNodeInboundRollsBackWhenWritebackFails(t *testing.T) {
	r := openBackupRepo(t, "inbound-atomic.db")
	if err := r.DB().Exec(`CREATE TRIGGER fix132_fail_inbound_update
		BEFORE UPDATE ON node_inbound
		BEGIN SELECT RAISE(ABORT, 'fix132 forced update failure'); END`).Error; err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	in := &model.NodeInbound{Name: "atomic-probe", Protocol: "vless", Port: 24443, Enabled: 0}
	if err := r.CreateNodeInbound(in); err == nil {
		t.Fatal("writeback failure must surface as an error")
	}
	if n := countWhere(t, r, "node_inbound", "name = ?", "atomic-probe"); n != 0 {
		t.Fatalf("A3-06: failed create must leave no row behind, got %d", n)
	}
}

// CreateNodeInbound 正常路径语义保持（BUG-A4-1 回归不破）：显式
// Enabled=0 落 0、Enabled=1 落 1。
func TestFix132CreateNodeInboundKeepsEnabledIntent(t *testing.T) {
	r := openBackupRepo(t, "inbound-enabled.db")
	off := &model.NodeInbound{Name: "want-off", Protocol: "vless", Port: 24444, Enabled: 0}
	if err := r.CreateNodeInbound(off); err != nil {
		t.Fatalf("create disabled inbound: %v", err)
	}
	on := &model.NodeInbound{Name: "want-on", Protocol: "vless", Port: 24445, Enabled: 1}
	if err := r.CreateNodeInbound(on); err != nil {
		t.Fatalf("create enabled inbound: %v", err)
	}
	if n := countWhere(t, r, "node_inbound", "name = ? AND enabled = 0", "want-off"); n != 1 {
		t.Fatal("explicit Enabled=0 must persist as 0")
	}
	if n := countWhere(t, r, "node_inbound", "name = ? AND enabled = 1", "want-on"); n != 1 {
		t.Fatal("explicit Enabled=1 must persist as 1")
	}
}

// A3-09：唯一冲突识别不得用 "already exists" 宽子串——PG 的 DDL
// 类错误（relation/index already exists）会被误判成业务唯一冲突，
// 掩盖真实错误。只认两方言的精确短语。
func TestFix132UniqueViolationDetectionIsPrecise(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"UNIQUE constraint failed: user.user", true},
		{`duplicate key value violates unique constraint "idx_user_username_unique"`, true},
		{`relation "idx_foo" already exists`, false},
		{`pq: relation "user" already exists`, false},
		{"database table is locked", false},
	}
	for _, c := range cases {
		if got := isUniqueConstraintViolation(errors.New(c.msg)); got != c.want {
			t.Fatalf("isUniqueConstraintViolation(%q) = %v, want %v", c.msg, got, c.want)
		}
	}
	if isUniqueConstraintViolation(nil) {
		t.Fatal("nil error must not be a violation")
	}
}
