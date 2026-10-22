package repo

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// D3-05: peer_share_runtime.resource_key 的单列全局唯一索引与
// (share_id, resource_key) 的查询/幂等语义矛盾——两个消费方面板的首条
// 多跳隧道必然生成相同的键（tunnel:1:node:1:type:3:hop:0），第二个消费方
// reserve-port 恒撞键失败（R5 真机坐实）。v3 迁移把唯一性收敛为复合
// (share_id, resource_key)。

const legacyPeerShareRuntimeDDL = `
CREATE TABLE peer_share_runtime (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	share_id INTEGER NOT NULL,
	node_id INTEGER NOT NULL,
	reservation_id TEXT NOT NULL,
	resource_key TEXT NOT NULL,
	binding_id TEXT NOT NULL DEFAULT '',
	role TEXT NOT NULL DEFAULT '',
	chain_name TEXT NOT NULL DEFAULT '',
	service_name TEXT NOT NULL DEFAULT '',
	protocol TEXT NOT NULL DEFAULT 'tls',
	strategy TEXT NOT NULL DEFAULT 'round',
	port INTEGER NOT NULL DEFAULT 0,
	target TEXT NOT NULL DEFAULT '',
	applied INTEGER NOT NULL DEFAULT 0,
	release_pending INTEGER NOT NULL DEFAULT 0,
	status INTEGER NOT NULL DEFAULT 1,
	created_time INTEGER NOT NULL,
	updated_time INTEGER NOT NULL
);
CREATE UNIQUE INDEX idx_peer_share_runtime_resource_key ON peer_share_runtime(resource_key);
CREATE UNIQUE INDEX idx_peer_share_runtime_reservation_id ON peer_share_runtime(reservation_id);
`

func sqliteIndexSQL(t *testing.T, r *Repository, name string) (string, bool) {
	t.Helper()
	var sqlText interface{}
	row := r.db.Raw("SELECT sql FROM sqlite_master WHERE type='index' AND name=?", name).Row()
	if err := row.Scan(&sqlText); err != nil {
		return "", false
	}
	if s, ok := sqlText.(string); ok {
		return s, true
	}
	return "", true
}

func TestMigrateV3PeerShareRuntimeCompositeUnique(t *testing.T) {
	db := openB4FileDB(t)
	if err := db.Exec(legacyPeerShareRuntimeDDL).Error; err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}

	insert := func(shareID int64, reservation, key string) error {
		return db.Exec(`INSERT INTO peer_share_runtime(share_id, node_id, reservation_id, resource_key, created_time, updated_time) VALUES(?, 1, ?, ?, 1, 1)`, shareID, reservation, key).Error
	}
	const sharedKey = "tunnel:1:node:1:type:3:hop:0"
	if err := insert(1, "res-a", sharedKey); err != nil {
		t.Fatalf("first insert under legacy index: %v", err)
	}
	if err := insert(2, "res-b", sharedKey); err == nil {
		t.Fatal("legacy single-column unique index unexpectedly allowed cross-share duplicate key")
	}

	if err := migrateV3PeerShareRuntimeCompositeUnique(db); err != nil {
		t.Fatalf("run v3 migration: %v", err)
	}

	// 旧单列索引必须被删除，复合索引必须存在且覆盖 (share_id, resource_key)。
	var legacyCount int
	if err := db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_peer_share_runtime_resource_key'`).Scan(&legacyCount).Error; err != nil {
		t.Fatalf("count legacy index: %v", err)
	}
	if legacyCount != 0 {
		t.Fatal("legacy single-column unique index still present after v3 migration")
	}
	var compositeSQL string
	if err := db.Raw(`SELECT sql FROM sqlite_master WHERE type='index' AND name='idx_peer_share_runtime_share_resource'`).Scan(&compositeSQL).Error; err != nil {
		t.Fatalf("read composite index: %v", err)
	}
	lower := strings.ToLower(compositeSQL)
	if !strings.Contains(lower, "share_id") || !strings.Contains(lower, "resource_key") || !strings.Contains(lower, "unique") {
		t.Fatalf("composite index definition unexpected: %q", compositeSQL)
	}

	// R5 场景：两个 share 同键都应成功；同 share 同键仍被拒。
	if err := insert(2, "res-b", sharedKey); err != nil {
		t.Fatalf("cross-share same key insert after migration: %v", err)
	}
	if err := insert(1, "res-c", sharedKey); err == nil {
		t.Fatal("same-share duplicate resource_key must still be rejected")
	}

	// 幂等：重复执行不报错、不动索引。
	if err := migrateV3PeerShareRuntimeCompositeUnique(db); err != nil {
		t.Fatalf("v3 migration is not idempotent: %v", err)
	}
}

// D3-04: 存量 share 的 relay 凭据启动回填——空凭据被补发、已有凭据
// 不被覆盖轮换、二次回填幂等。
func TestBackfillPeerShareRelayCredentials(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "backfill.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = r.Close() }()

	now := time.Now().UnixMilli()
	legacy := &model.PeerShare{Name: "legacy", NodeID: 1, Token: "tok-legacy", IsActive: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.CreatePeerShare(legacy); err != nil {
		t.Fatalf("create legacy share: %v", err)
	}
	modern := &model.PeerShare{Name: "modern", NodeID: 1, Token: "tok-modern", IsActive: 1,
		RelayUsername: "share_preset", RelayPassword: "preset-password", CreatedTime: now, UpdatedTime: now}
	if err := r.CreatePeerShare(modern); err != nil {
		t.Fatalf("create modern share: %v", err)
	}

	backfillPeerShareRelayCredentials(r.DB())

	got, err := r.GetPeerShare(legacy.ID)
	if err != nil || got == nil {
		t.Fatalf("reload legacy: %v", err)
	}
	if got.RelayPassword == "" || got.RelayUsername == "" {
		t.Fatal("legacy share must receive relay credentials from backfill")
	}
	firstPassword := got.RelayPassword

	gotModern, err := r.GetPeerShare(modern.ID)
	if err != nil || gotModern == nil {
		t.Fatalf("reload modern: %v", err)
	}
	if gotModern.RelayPassword != "preset-password" || gotModern.RelayUsername != "share_preset" {
		t.Fatal("existing credentials must not be overwritten")
	}

	backfillPeerShareRelayCredentials(r.DB())
	again, err := r.GetPeerShare(legacy.ID)
	if err != nil || again == nil {
		t.Fatalf("reload legacy again: %v", err)
	}
	if again.RelayPassword != firstPassword {
		t.Fatal("backfill must be idempotent (no rotation on second run)")
	}
}

func TestOpenCreatesCompositePeerShareRuntimeIndex(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	if _, exists := sqliteIndexSQL(t, r, "idx_peer_share_runtime_resource_key"); exists {
		t.Fatal("fresh install must not create the legacy single-column resource_key unique index")
	}
	def, exists := sqliteIndexSQL(t, r, "idx_peer_share_runtime_share_resource")
	if !exists {
		t.Fatal("fresh install is missing the composite (share_id, resource_key) unique index")
	}
	if lower := strings.ToLower(def); !strings.Contains(lower, "share_id") || !strings.Contains(lower, "resource_key") {
		t.Fatalf("composite index definition unexpected: %q", def)
	}

	now := int64(1)
	mk := func(shareID int64, reservation string) *model.PeerShareRuntime {
		return &model.PeerShareRuntime{
			ShareID: shareID, NodeID: 1, ReservationID: reservation,
			ResourceKey: "tunnel:1:node:1:type:3:hop:0", Status: 1,
			CreatedTime: now, UpdatedTime: now,
		}
	}
	if err := r.CreatePeerShareRuntime(mk(11, "res-11")); err != nil {
		t.Fatalf("create runtime share 11: %v", err)
	}
	if err := r.CreatePeerShareRuntime(mk(12, "res-12")); err != nil {
		t.Fatalf("second consumer with identical resource_key must succeed after D3-05 fix: %v", err)
	}
	if err := r.CreatePeerShareRuntime(mk(11, "res-13")); err == nil {
		t.Fatal("same-share duplicate resource_key must be rejected")
	}
}
