package contract_test

import (
	"database/sql"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"go-backend/internal/store/repo"
)

// PG-2 回归契约：无 seq 的旧 agent（legacy 兼容路径）在 PostgreSQL 上必须
// 能完成计数。修复前 node_seq.go 的 legacy UPSERT 用裸列名自引用
// （legacy_count = legacy_count + 1），PG 在 ON CONFLICT DO UPDATE 的
// 右值中判定其在目标行与 EXCLUDED 间歧义，解析期即报 SQLSTATE 42702，
// 空表首插亦失败——PG 面板上所有 legacy 上报必失败。SQLite 接受裸列名
// 故该缺陷只在真 PG 上可见，本测试以 FLMX_POSTGRES_TEST_DSN 门控。
func TestPostgresLegacySeqContract(t *testing.T) {
	baseDSN := strings.TrimSpace(os.Getenv("FLMX_POSTGRES_TEST_DSN"))
	if baseDSN == "" {
		t.Skip("set FLMX_POSTGRES_TEST_DSN to run postgres contract tests")
	}

	schemaName := "contract_legacy_seq_" + strconv.FormatInt(time.Now().UnixNano(), 36)
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

	// 空 schema 打开仓库本身即 PG-1 的回归守卫：迁移 v1+v2 必须无手工
	// 绕行全程成功，两条版本行都落。
	r, err := repo.OpenPostgres(testDSN)
	if err != nil {
		t.Fatalf("open postgres repository: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	for _, version := range []string{"r5b4_v1_port_user_unique", "r5b4_v2_node_inbound_partial_unique"} {
		var n int64
		if err := r.DB().Raw(`SELECT COUNT(*) FROM schema_migration WHERE version = ?`, version).Scan(&n).Error; err != nil {
			t.Fatalf("query schema_migration: %v", err)
		}
		if n != 1 {
			t.Fatalf("expected schema_migration row for %s, got %d", version, n)
		}
	}
	var idxCount int64
	if err := r.DB().Raw(`SELECT COUNT(*) FROM pg_indexes WHERE indexname = 'idx_user_username_unique'`).Scan(&idxCount).Error; err != nil {
		t.Fatalf("query pg_indexes: %v", err)
	}
	if idxCount != 1 {
		t.Fatalf("expected idx_user_username_unique to exist on postgres, got %d", idxCount)
	}

	now := time.Now().Unix()

	// legacy 首报（空表插入路径）。
	accepted, isReplay, isLegacy, err := r.CheckAndUpdateSeq(424242, nil, now)
	if err != nil {
		t.Fatalf("legacy first report failed: %v", err)
	}
	if !accepted || isReplay || !isLegacy {
		t.Fatalf("legacy first report: accepted=%v replay=%v legacy=%v, want true/false/true", accepted, isReplay, isLegacy)
	}

	// legacy 再报（冲突更新路径，legacy_count 应累加到 2）。
	accepted, _, isLegacy, err = r.CheckAndUpdateSeq(424242, nil, now+1)
	if err != nil {
		t.Fatalf("legacy second report failed: %v", err)
	}
	if !accepted || !isLegacy {
		t.Fatalf("legacy second report: accepted=%v legacy=%v, want true/true", accepted, isLegacy)
	}
	var legacyCount int64
	if err := r.DB().Raw(`SELECT legacy_count FROM node_seq WHERE node_id = ?`, 424242).Scan(&legacyCount).Error; err != nil {
		t.Fatalf("query node_seq: %v", err)
	}
	if legacyCount != 2 {
		t.Fatalf("expected legacy_count=2 after two legacy reports, got %d", legacyCount)
	}

	// 带 seq 的正常路径同测（与 F-071 的 PG 结论对齐）：首报接受、同 seq 重放拒绝。
	seq := int64(7)
	accepted, isReplay, _, err = r.CheckAndUpdateSeq(424243, &seq, now)
	if err != nil || !accepted || isReplay {
		t.Fatalf("seq first report: accepted=%v replay=%v err=%v, want true/false/nil", accepted, isReplay, err)
	}
	accepted, isReplay, _, err = r.CheckAndUpdateSeq(424243, &seq, now+1)
	if err != nil || accepted || !isReplay {
		t.Fatalf("seq replay: accepted=%v replay=%v err=%v, want false/true/nil", accepted, isReplay, err)
	}
}
