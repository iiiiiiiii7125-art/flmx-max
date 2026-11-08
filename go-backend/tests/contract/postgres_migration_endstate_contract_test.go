package contract_test

import (
	"database/sql"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// PG 迁移终态契约组（R2-E3 的 F05–F09 ＋ LIVE-ALL 的 M-0228 腿）：五条
// PG-only 迁移分支此前从未在真 PostgreSQL 上验过终态（既存覆盖只有
// SQLite 实跑或 stub 计数）。本组逐条播种旧形态、经 repo.OpenPostgres
// 全启动序列跑迁移、再以 information_schema 断言终态，并复开一次验
// 幂等。全部用例以 FLMX_POSTGRES_TEST_DSN 门控，无 DSN 时 SKIP。

func pgEndstateTestDSN(t *testing.T) string {
	t.Helper()
	baseDSN := strings.TrimSpace(os.Getenv("FLMX_POSTGRES_TEST_DSN"))
	if baseDSN == "" {
		t.Skip("set FLMX_POSTGRES_TEST_DSN to run postgres contract tests")
	}
	schemaName := "contract_pgmig_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	adminDB, err := sql.Open("pgx", baseDSN)
	if err != nil {
		t.Fatalf("open postgres admin connection: %v", err)
	}
	t.Cleanup(func() {
		_, _ = adminDB.Exec(`DROP SCHEMA IF EXISTS "` + schemaName + `" CASCADE`)
		_ = adminDB.Close()
	})
	if _, err := adminDB.Exec(`CREATE SCHEMA "` + schemaName + `"`); err != nil {
		t.Fatalf("create schema %s: %v", schemaName, err)
	}
	testDSN, err := withSearchPath(baseDSN, schemaName)
	if err != nil {
		t.Fatalf("build schema dsn: %v", err)
	}
	return testDSN
}

func openPGRepo(t *testing.T, dsn string) *repo.Repository {
	t.Helper()
	r, err := repo.OpenPostgres(dsn)
	if err != nil {
		t.Fatalf("open postgres repository: %v", err)
	}
	return r
}

func pgColumnDataType(t *testing.T, r *repo.Repository, table, column string) string {
	t.Helper()
	ns := mustQueryNullString(t, r, `
		SELECT data_type FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = ? AND column_name = ?
		LIMIT 1`, table, column)
	if !ns.Valid {
		t.Fatalf("column %s.%s not found in information_schema", table, column)
	}
	return ns.String
}

func pgSchemaVersion(t *testing.T, r *repo.Repository) int64 {
	t.Helper()
	return mustQueryInt64(t, r, `SELECT version FROM schema_version LIMIT 1`)
}

// F05（最高优先，与 glebarez DropColumn 静默空转同机制谓词）：rmshare
// v6 在 PG 分支用 Migrator().DropColumn 删 node 远端四列，此前只在
// SQLite（原生 ALTER 分支）验过。本用例在已完成首次启动的库上回装
// 2.1.0 前形态（四列＋五张联邦表＋一台远端节点＋删版本行重新布防），
// 复开后断言四列/五表在 information_schema 中消失、远端行被删、
// 本地行保留、版本行恰一条；三开幂等。
func TestPostgresFederationRemovalMigrationEndState(t *testing.T) {
	testDSN := pgEndstateTestDSN(t)

	r := openPGRepo(t, testDSN)
	retrofit := []string{
		`ALTER TABLE node ADD COLUMN is_remote integer NOT NULL DEFAULT 0`,
		`ALTER TABLE node ADD COLUMN remote_url text`,
		`ALTER TABLE node ADD COLUMN remote_token text`,
		`ALTER TABLE node ADD COLUMN remote_config text`,
		`CREATE TABLE peer_share (id bigint PRIMARY KEY)`,
		`CREATE TABLE peer_share_runtime (id bigint PRIMARY KEY)`,
		`CREATE TABLE peer_share_resource (id bigint PRIMARY KEY)`,
		`CREATE TABLE federation_tunnel_binding (id bigint PRIMARY KEY)`,
		`CREATE TABLE federation_pending_release (id bigint PRIMARY KEY)`,
	}
	for _, ddl := range retrofit {
		if err := r.DB().Exec(ddl).Error; err != nil {
			_ = r.Close()
			t.Fatalf("retrofit legacy federation state (%s): %v", ddl, err)
		}
	}
	remote := model.Node{Name: "pg-legacy-remote", Secret: "remote-secret", ServerIP: "10.60.0.1", Port: "443", CreatedTime: 1}
	local := model.Node{Name: "pg-local-node", Secret: "local-secret", ServerIP: "10.60.0.2", Port: "443", CreatedTime: 1}
	if err := r.DB().Create(&remote).Error; err != nil {
		_ = r.Close()
		t.Fatalf("create remote node row: %v", err)
	}
	if err := r.DB().Create(&local).Error; err != nil {
		_ = r.Close()
		t.Fatalf("create local node row: %v", err)
	}
	if err := r.DB().Exec(`UPDATE node SET is_remote = 1, remote_url = 'https://peer.example', remote_token = 'tok', remote_config = '{}' WHERE id = ?`, remote.ID).Error; err != nil {
		_ = r.Close()
		t.Fatalf("mark node remote: %v", err)
	}
	if err := r.DB().Exec(`DELETE FROM schema_migration WHERE version = ?`, "rmshare_v6_federation_removal").Error; err != nil {
		_ = r.Close()
		t.Fatalf("re-arm v6 migration: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close repository before migration run: %v", err)
	}

	assertEndState := func(r *repo.Repository) {
		t.Helper()
		if n := mustQueryInt64(t, r, `
			SELECT COUNT(*) FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = 'node'
			  AND column_name IN ('is_remote', 'remote_url', 'remote_token', 'remote_config')`); n != 0 {
			t.Fatalf("expected node remote columns dropped on postgres, %d remain", n)
		}
		if n := mustQueryInt64(t, r, `
			SELECT COUNT(*) FROM information_schema.tables
			WHERE table_schema = current_schema()
			  AND table_name IN ('peer_share', 'peer_share_runtime', 'peer_share_resource', 'federation_tunnel_binding', 'federation_pending_release')`); n != 0 {
			t.Fatalf("expected federation tables dropped on postgres, %d remain", n)
		}
		if n := mustQueryInt64(t, r, `SELECT COUNT(*) FROM node WHERE id = ?`, remote.ID); n != 0 {
			t.Fatalf("expected remote node row deleted, found %d", n)
		}
		if n := mustQueryInt64(t, r, `SELECT COUNT(*) FROM node WHERE id = ?`, local.ID); n != 1 {
			t.Fatalf("expected local node row preserved, found %d", n)
		}
		if n := mustQueryInt64(t, r, `SELECT COUNT(*) FROM schema_migration WHERE version = ?`, "rmshare_v6_federation_removal"); n != 1 {
			t.Fatalf("expected exactly one rmshare v6 version row, found %d", n)
		}
	}

	r = openPGRepo(t, testDSN)
	assertEndState(r)
	if err := r.Close(); err != nil {
		t.Fatalf("close repository after migration run: %v", err)
	}

	r = openPGRepo(t, testDSN)
	assertEndState(r)
	_ = r.Close()
}

// F06：preparePostgresLegacySchema 把旧 schema.sql 内联 UNIQUE 留下的
// 约束名 vite_config_name_key 改名为 GORM 命名策略的
// uni_vite_config_name。此前全树无测试、无 PG 播种。本用例回装旧名
// 约束后复开：启动必须成功（改名不到位时 AutoMigrate 的
// DropConstraint 会打错名字）、旧名必须消失、name 唯一性必须仍被
// 强制（改名后的约束或同名唯一索引任一在位）、存量行保留；三开幂等。
func TestPostgresLegacyConstraintRenameEndState(t *testing.T) {
	testDSN := pgEndstateTestDSN(t)

	r := openPGRepo(t, testDSN)
	if err := r.DB().Exec(`ALTER TABLE vite_config ADD CONSTRAINT vite_config_name_key UNIQUE (name)`).Error; err != nil {
		_ = r.Close()
		t.Fatalf("retrofit legacy constraint name: %v", err)
	}
	// 植入自有标记行验存量保留（不用总行数：seedData 的逐键懒播种会让
	// 总行数在后续启动里合法增长，总数断言会误伤）。
	if err := r.DB().Exec(`INSERT INTO vite_config (name, value, time) VALUES ('pg_constraint_probe', 'keep-me', 0)`).Error; err != nil {
		_ = r.Close()
		t.Fatalf("insert marker config row: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close repository before rename run: %v", err)
	}

	assertEndState := func(r *repo.Repository) {
		t.Helper()
		if n := mustQueryInt64(t, r, `
			SELECT COUNT(*) FROM information_schema.table_constraints
			WHERE constraint_schema = current_schema() AND table_name = 'vite_config'
			  AND constraint_name = 'vite_config_name_key' AND constraint_type = 'UNIQUE'`); n != 0 {
			t.Fatalf("expected legacy constraint name gone on postgres, found %d", n)
		}
		marker := mustQueryNullString(t, r, `SELECT value FROM vite_config WHERE name = 'pg_constraint_probe' LIMIT 1`)
		if !marker.Valid || marker.String != "keep-me" {
			t.Fatalf("expected marker config row preserved, got %q", marker.String)
		}
		// name 唯一性仍须被强制：改名约束或唯一索引任一在位都算数，
		// 以重复插入实探为准（二者皆无则插入成功、测试在此变红）。
		dupErr := r.DB().Exec(`INSERT INTO vite_config (name, value, time) VALUES ('pg_constraint_probe', 'dup', 0)`).Error
		if dupErr == nil {
			t.Fatalf("expected duplicate vite_config name insert to be rejected")
		}
	}

	r = openPGRepo(t, testDSN)
	assertEndState(r)
	if err := r.Close(); err != nil {
		t.Fatalf("close repository after rename run: %v", err)
	}

	r = openPGRepo(t, testDSN)
	assertEndState(r)
	_ = r.Close()
}

// F07：schema ver<3 的 PG ALTER（vite_config.value → TEXT）此前只被
// stub 计数测试覆盖，真实执行零断言。本用例把 value 回装为
// varchar 并把 schema_version 压回 2，复开后断言列类型归 text 且
// 版本链完整推进到现行版本；三开幂等。
func TestPostgresViteConfigValueMigrationEndState(t *testing.T) {
	testDSN := pgEndstateTestDSN(t)

	r := openPGRepo(t, testDSN)
	if err := r.DB().Exec(`ALTER TABLE vite_config ALTER COLUMN value TYPE varchar(1024)`).Error; err != nil {
		_ = r.Close()
		t.Fatalf("retrofit varchar value column: %v", err)
	}
	if got := pgColumnDataType(t, r, "vite_config", "value"); got != "character varying" {
		_ = r.Close()
		t.Fatalf("retrofit failed: vite_config.value type = %q, want character varying", got)
	}
	if err := r.DB().Exec(`UPDATE schema_version SET version = 2`).Error; err != nil {
		_ = r.Close()
		t.Fatalf("rewind schema version to 2: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close repository before migration run: %v", err)
	}

	r = openPGRepo(t, testDSN)
	if got := pgColumnDataType(t, r, "vite_config", "value"); got != "text" {
		_ = r.Close()
		t.Fatalf("expected vite_config.value to be text after migration, got %q", got)
	}
	if v := pgSchemaVersion(t, r); v != 8 {
		_ = r.Close()
		t.Fatalf("expected schema_version 8 after migration chain, got %d", v)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close repository after migration run: %v", err)
	}

	r = openPGRepo(t, testDSN)
	if got := pgColumnDataType(t, r, "vite_config", "value"); got != "text" {
		_ = r.Close()
		t.Fatalf("expected vite_config.value to stay text on reopen, got %q", got)
	}
	_ = r.Close()
}

// F08：schema ver<5 的 PG ALTER（流量列 ×11 → BIGINT）与 F07 同形，
// 此前仅 stub 计数。本用例把 11 列回装为 integer、版本压回 4，复开
// 后逐列断言 information_schema 中均为 bigint，存量管理员行保留、
// 版本推进到现行版本。
func TestPostgresTrafficBigIntMigrationEndState(t *testing.T) {
	testDSN := pgEndstateTestDSN(t)

	columns := [][2]string{
		{"user", "flow"}, {"user", "in_flow"}, {"user", "out_flow"},
		{"forward", "in_flow"}, {"forward", "out_flow"},
		{"statistics_flow", "flow"}, {"statistics_flow", "total_flow"},
		{"tunnel", "flow"},
		{"user_tunnel", "flow"}, {"user_tunnel", "in_flow"}, {"user_tunnel", "out_flow"},
	}

	r := openPGRepo(t, testDSN)
	for _, c := range columns {
		ddl := `ALTER TABLE "` + c[0] + `" ALTER COLUMN "` + c[1] + `" TYPE integer`
		if err := r.DB().Exec(ddl).Error; err != nil {
			_ = r.Close()
			t.Fatalf("retrofit integer column %s.%s: %v", c[0], c[1], err)
		}
		if got := pgColumnDataType(t, r, c[0], c[1]); got != "integer" {
			_ = r.Close()
			t.Fatalf("retrofit failed: %s.%s type = %q, want integer", c[0], c[1], got)
		}
	}
	if err := r.DB().Exec(`UPDATE schema_version SET version = 4`).Error; err != nil {
		_ = r.Close()
		t.Fatalf("rewind schema version to 4: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close repository before migration run: %v", err)
	}

	r = openPGRepo(t, testDSN)
	for _, c := range columns {
		if got := pgColumnDataType(t, r, c[0], c[1]); got != "bigint" {
			_ = r.Close()
			t.Fatalf("expected %s.%s to be bigint after migration, got %q", c[0], c[1], got)
		}
	}
	if v := pgSchemaVersion(t, r); v != 8 {
		_ = r.Close()
		t.Fatalf("expected schema_version 8 after migration chain, got %d", v)
	}
	if n := mustQueryInt64(t, r, `SELECT COUNT(*) FROM "user"`); n < 1 {
		_ = r.Close()
		t.Fatalf("expected seeded user rows preserved after migration, found %d", n)
	}
	_ = r.Close()
}

// F09：ensurePostgresIDDefaults 在 migrate 测试中被整体 stub。
// node 表的单点修复已有
// TestPostgresNodeCreateRepairsMissingIDDefaultContract 覆盖；本用例
// 补其未覆盖面：对非模型旧表的全集发现扫描（建 <表>_id_seq 序列、
// SET DEFAULT nextval、按存量最大 id 同步序列）以及对既存自定义
// nextval 默认的保留。播种在首次 Open 之前走裸连接完成。
func TestPostgresLegacyTableIDDefaultEndState(t *testing.T) {
	testDSN := pgEndstateTestDSN(t)

	seed, err := sql.Open("pgx", testDSN)
	if err != nil {
		t.Fatalf("open postgres seed connection: %v", err)
	}
	seedStmts := []string{
		`CREATE TABLE legacy_id_probe (id bigint PRIMARY KEY, note text NOT NULL)`,
		`INSERT INTO legacy_id_probe (id, note) VALUES (41, 'a'), (42, 'b')`,
		`CREATE SEQUENCE legacy_id_custom_seq`,
		`CREATE TABLE legacy_id_custom (id bigint PRIMARY KEY DEFAULT nextval('legacy_id_custom_seq'), note text)`,
	}
	for _, stmt := range seedStmts {
		if _, err := seed.Exec(stmt); err != nil {
			_ = seed.Close()
			t.Fatalf("seed legacy table (%s): %v", stmt, err)
		}
	}
	_ = seed.Close()

	assertProbe := func(r *repo.Repository, wantNext int64) {
		t.Helper()
		def := mustQueryNullString(t, r, `
			SELECT column_default FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = 'legacy_id_probe' AND column_name = 'id'
			LIMIT 1`)
		if !def.Valid || !strings.Contains(strings.ToLower(def.String), "nextval(") ||
			!strings.Contains(def.String, "legacy_id_probe_id_seq") {
			t.Fatalf("expected legacy_id_probe.id default to be nextval on its own sequence, got %q", def.String)
		}
		if n := mustQueryInt64(t, r, `
			SELECT COUNT(*) FROM information_schema.sequences
			WHERE sequence_schema = current_schema() AND sequence_name = 'legacy_id_probe_id_seq'`); n != 1 {
			t.Fatalf("expected sequence legacy_id_probe_id_seq to exist, found %d", n)
		}
		var newID int64
		if err := r.DB().Raw(`INSERT INTO legacy_id_probe (note) VALUES ('auto') RETURNING id`).Scan(&newID).Error; err != nil {
			t.Fatalf("insert into legacy_id_probe without id: %v", err)
		}
		if newID != wantNext {
			t.Fatalf("expected sequence synced past seeded max id (next %d), got %d", wantNext, newID)
		}
		customDef := mustQueryNullString(t, r, `
			SELECT column_default FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = 'legacy_id_custom' AND column_name = 'id'
			LIMIT 1`)
		if !customDef.Valid || !strings.Contains(customDef.String, "legacy_id_custom_seq") {
			t.Fatalf("expected pre-existing custom default to be preserved, got %q", customDef.String)
		}
	}

	r := openPGRepo(t, testDSN)
	assertProbe(r, 43)
	if err := r.Close(); err != nil {
		t.Fatalf("close repository after repair run: %v", err)
	}

	r = openPGRepo(t, testDSN)
	assertProbe(r, 44)
	_ = r.Close()
}

// M-0228 腿：存储概要接口（handler storageSummary 背后的
// repo.DatabaseStorageSummary）的 PG 分支从未在真 PG 上回读过——
// LIVE-ALL 的实读因 PG 面板腿未实际运行而只读到 sqlite。本用例在
// 真 PG 打开的仓库上断言 dbType 回读为 postgres、库大小为正、
// 文本形态在位，JSON 键名与线上回读一致。
func TestPostgresStorageSummaryEndState(t *testing.T) {
	testDSN := pgEndstateTestDSN(t)

	r := openPGRepo(t, testDSN)
	defer func() { _ = r.Close() }()

	summary, err := r.DatabaseStorageSummary()
	if err != nil {
		t.Fatalf("database storage summary on postgres: %v", err)
	}
	if summary.DBType != "postgres" {
		t.Fatalf("expected dbType postgres, got %q", summary.DBType)
	}
	if summary.DatabaseSizeBytes <= 0 {
		t.Fatalf("expected positive database size on postgres, got %d", summary.DatabaseSizeBytes)
	}
	if summary.DatabaseSizeText == "" {
		t.Fatalf("expected non-empty database size text")
	}
	raw, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("marshal storage summary: %v", err)
	}
	if !strings.Contains(string(raw), `"dbType":"postgres"`) {
		t.Fatalf("expected storage summary JSON to carry dbType postgres, got %s", raw)
	}
}
