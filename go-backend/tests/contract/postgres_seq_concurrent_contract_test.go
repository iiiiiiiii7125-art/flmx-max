package contract_test

import (
	"database/sql"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R2X-011 PG 契约：并发同 seq 双入账在 PostgreSQL 上只入账一次。
// SQLite 的 MaxOpenConns(1) 串行化会掩盖方言差异（advanceSeqTx 的
// 条件更新 RowsAffected 语义须在真 PG 上同样成立），故本例以
// FLMX_POSTGRES_TEST_DSN 门控，无 DSN 时按既有 contract 口径 skip。
func TestPostgresSeqConcurrentDoubleApplyContract(t *testing.T) {
	baseDSN := strings.TrimSpace(os.Getenv("FLMX_POSTGRES_TEST_DSN"))
	if baseDSN == "" {
		t.Skip("set FLMX_POSTGRES_TEST_DSN to run postgres contract tests")
	}

	schemaName := "contract_seq_conc_" + strconv.FormatInt(time.Now().UnixNano(), 36)
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
	t.Cleanup(func() { _ = r.Close() })

	now := time.Now()
	nowMs := now.UnixMilli()
	if err := r.DB().Create(&model.User{ID: 2, User: "pgseq-user", Pwd: "pwd", RoleID: 1, ExpTime: 2727251700000, Flow: 99999, Num: 99999, CreatedTime: nowMs, Status: 1}).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := r.DB().Create(&model.Tunnel{ID: 1, Name: "tunnel-1", TrafficRatio: 1, Type: 1, Protocol: "tls", Flow: 1, CreatedTime: nowMs, UpdatedTime: nowMs, Status: 1}).Error; err != nil {
		t.Fatalf("seed tunnel: %v", err)
	}
	if err := r.DB().Create(&model.UserTunnel{ID: 10, UserID: 2, TunnelID: 1, Num: 99999, Flow: 0, ExpTime: 2727251700000, Status: 1}).Error; err != nil {
		t.Fatalf("seed user tunnel: %v", err)
	}
	if err := r.DB().Create(&model.Forward{ID: 20, UserID: 2, UserName: "pgseq-user", Name: "forward-20", TunnelID: 1, RemoteAddr: "1.1.1.1:80", Strategy: "fifo", CreatedTime: nowMs, UpdatedTime: nowMs, Status: 1}).Error; err != nil {
		t.Fatalf("seed forward: %v", err)
	}

	seq := int64(7)
	for i := 0; i < 2; i++ {
		isReplay, _, err := r.CheckSeq(9, &seq)
		if err != nil || isReplay {
			t.Fatalf("check %d: replay=%v err=%v, want pass", i, isReplay, err)
		}
	}
	deltas := []repo.FlowUploadCounterDelta{{ForwardID: 20, UserID: 2, UserTunnelID: 10, InFlow: 50, OutFlow: 100}}
	usages := map[int64]int64{2: 150}
	adv := &repo.FlowSeqAdvance{NodeID: 9, Seq: &seq}
	if _, err := r.ApplyFlowUploadAccounting(deltas, usages, now, adv); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if _, err := r.ApplyFlowUploadAccounting(deltas, usages, now, adv); !errors.Is(err, repo.ErrSeqReplay) {
		t.Fatalf("second apply: err=%v, want ErrSeqReplay", err)
	}
	var fwd model.Forward
	if err := r.DB().First(&fwd, 20).Error; err != nil {
		t.Fatal(err)
	}
	if fwd.InFlow != 50 || fwd.OutFlow != 100 {
		t.Fatalf("forward double-accounted on PG: in=%d out=%d, want 50/100", fwd.InFlow, fwd.OutFlow)
	}
}
