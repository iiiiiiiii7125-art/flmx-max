package contract_test

import (
	"database/sql"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// U-1 direct probe (PostgreSQL side of repo.TestCompanionPartialUniqueIndex):
// the partial unique index on node_inbound.companion_of must exist on real
// PostgreSQL after the product migration path (OpenPostgres → autoMigrateAll)
// and must behave like the SQLite one: any number of plain inbounds
// (companion_of = 0 — the column is NOT NULL, so 0 is the "empty" value),
// at most one companion per origin, different origins coexist.
func TestPostgresCompanionPartialUniqueIndex(t *testing.T) {
	baseDSN := strings.TrimSpace(os.Getenv("FLMX_POSTGRES_TEST_DSN"))
	if baseDSN == "" {
		t.Skip("set FLMX_POSTGRES_TEST_DSN to run postgres contract tests")
	}

	schemaName := "contract_companion_" + strconv.FormatInt(time.Now().UnixNano(), 36)
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

	r, err := repo.OpenPostgres(testDSN)
	if err != nil {
		t.Fatalf("open postgres repository: %v", err)
	}
	t.Cleanup(func() {
		_ = r.Close()
	})

	// Probe the index object itself before exercising it: it must be a
	// UNIQUE index over companion_of carrying the partial predicate.
	indexdef := mustQueryNullString(t, r, `
		SELECT indexdef
		FROM pg_indexes
		WHERE schemaname = current_schema()
		  AND tablename = 'node_inbound'
		  AND indexname = 'idx_node_inbound_companion'
		LIMIT 1
	`)
	if !indexdef.Valid {
		t.Fatal("partial unique index idx_node_inbound_companion missing on node_inbound after OpenPostgres migration")
	}
	if !strings.Contains(indexdef.String, "UNIQUE") ||
		!strings.Contains(indexdef.String, "companion_of") ||
		!strings.Contains(indexdef.String, "> 0") {
		t.Fatalf("idx_node_inbound_companion must be UNIQUE over companion_of with a > 0 partial predicate, got: %s", indexdef.String)
	}

	now := time.Now().UnixMilli()
	mk := func(name string, companionOf int64, port int) error {
		return r.CreateNodeInbound(&model.NodeInbound{
			KernelID: 1, NodeID: 1, Name: name, Protocol: "shadowsocks",
			Port: port, CompanionOf: companionOf, CreatedTime: now, UpdatedTime: now,
		})
	}
	if err := mk("a", 0, 10001); err != nil {
		t.Fatalf("plain inbound a: %v", err)
	}
	if err := mk("b", 0, 10002); err != nil {
		t.Fatalf("plain inbound b (second companion_of=0 row) must be allowed: %v", err)
	}
	if err := mk("a-companion", 1, 10003); err != nil {
		t.Fatalf("first companion of origin 1: %v", err)
	}
	if err := mk("a-companion-dup", 1, 10004); err == nil {
		t.Fatal("a second companion for the same origin must be rejected by the partial unique index")
	} else if !strings.Contains(err.Error(), "duplicate key") &&
		!strings.Contains(err.Error(), "23505") &&
		!strings.Contains(err.Error(), "idx_node_inbound_companion") {
		t.Fatalf("duplicate companion must fail on the unique index, got: %v", err)
	}
	if err := mk("b-companion", 2, 10005); err != nil {
		t.Fatalf("companion of a different origin (2) must coexist: %v", err)
	}
	comp, err := r.GetCompanionInbound(1)
	if err != nil || comp == nil || comp.Name != "a-companion" {
		t.Fatalf("GetCompanionInbound: %v %+v", err, comp)
	}
	all, err := r.ListCompanionInbounds()
	if err != nil || len(all) != 2 {
		t.Fatalf("ListCompanionInbounds: %v (%d rows)", err, len(all))
	}
}
