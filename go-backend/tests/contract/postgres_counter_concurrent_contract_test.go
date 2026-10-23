package contract_test

import (
	"database/sql"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R4 发版门项 11（F-S01，R3-F-S01 疑似升 P3 立案）：PostgreSQL
// Read Committed 下入站/订阅计数管线「读基线 → 算 delta → Expr
// 累加」无串行化时，两路并发同一样本各按同一旧基线算一份 delta，
// 流量被双计（门上实证：订阅三轮全双计 3600=2×1800、入站后两轮
// 双计 3100）。SQLite 的 MaxOpenConns(1) 会掩盖此并发模型，故本
// 组用例以 FLMX_POSTGRES_TEST_DSN 门控，无 DSN 时按既有 contract
// 口径 skip；方法与发版门探针同形（基线 1000 后三轮双并发提交同
// 一样本 1500/2100/2800，期望总增量 1800），对象逐字为
// ApplyInboundCounterSamples / ApplySubscriptionCounterSamples。
// 修复口径：repo 层基线行 SELECT … FOR UPDATE（见 repo 包注释），
// 本组在真 PG 上复跑即发版门项 11 的复验面。

func openCounterContractRepo(t *testing.T) *repo.Repository {
	t.Helper()
	baseDSN := strings.TrimSpace(os.Getenv("FLMX_POSTGRES_TEST_DSN"))
	if baseDSN == "" {
		t.Skip("set FLMX_POSTGRES_TEST_DSN to run postgres contract tests")
	}
	schemaName := "contract_counter_conc_" + strconv.FormatInt(time.Now().UnixNano(), 36)
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
	return r
}

// applyConcurrent fires n goroutines that release simultaneously and
// each invoke apply once with the identical sample batch; all calls
// must succeed (a serialization fix must not turn the losing callers
// into errors — the poll loop treats an apply error as a lost round).
func applyConcurrent(t *testing.T, n int, apply func() error) {
	t.Helper()
	start := make(chan struct{})
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = apply()
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent apply %d: %v", i, err)
		}
	}
}

func TestPostgresApplySubscriptionCounterSamplesConcurrentDoubleApplyContract(t *testing.T) {
	r := openCounterContractRepo(t)

	sub := model.Subscription{Name: "pgcounter-sub", Token: "pgcounter-token", Kind: "managed", Enabled: 1}
	if err := r.DB().Create(&sub).Error; err != nil {
		t.Fatalf("seed subscription: %v", err)
	}
	const nodeID = int64(7)
	apply := func(bytes int64) func() error {
		return func() error {
			return r.ApplySubscriptionCounterSamples(nodeID, "e1", []repo.SubscriptionCounterSample{
				{SubscriptionID: sub.ID, Direction: "up", Bytes: bytes},
			}, time.Now().UnixMilli())
		}
	}
	// 基线 1000：只建基线、不计增量（既有口径）。
	if err := apply(1000)(); err != nil {
		t.Fatalf("apply baseline: %v", err)
	}
	// 三轮双并发提交同一样本：增量 500/600/700，每轮只许入账一次。
	for _, bytes := range []int64{1500, 2100, 2800} {
		applyConcurrent(t, 4, apply(bytes))
	}
	var got model.Subscription
	if err := r.DB().First(&got, sub.ID).Error; err != nil {
		t.Fatalf("reload subscription: %v", err)
	}
	if got.UploadBytes != 1800 {
		t.Fatalf("subscription double-counted on PG: upload_bytes=%d, want 1800 (500+600+700, each round counted once)", got.UploadBytes)
	}
	var counter model.SubscriptionCounter
	if err := r.DB().Where("node_id = ? AND subscription_id = ? AND direction = ?", nodeID, sub.ID, "up").First(&counter).Error; err != nil {
		t.Fatalf("reload subscription counter: %v", err)
	}
	if counter.Bytes != 2800 {
		t.Fatalf("subscription baseline=%d, want 2800", counter.Bytes)
	}
}

func TestPostgresApplyInboundCounterSamplesConcurrentDoubleApplyContract(t *testing.T) {
	r := openCounterContractRepo(t)

	inbound := model.NodeInbound{Name: "pgcounter-inbound", Protocol: "vless", Port: 443}
	if err := r.DB().Create(&inbound).Error; err != nil {
		t.Fatalf("seed inbound: %v", err)
	}
	apply := func(bytes int64) func() error {
		return func() error {
			return r.ApplyInboundCounterSamples([]repo.InboundCounterSample{
				{InboundID: inbound.ID, Protocol: "vless", Direction: "in", Epoch: "e1", Bytes: bytes},
			}, time.Now().UnixMilli())
		}
	}
	// 基线 1000：只建基线、不计增量（R2X-033 口径）。
	if err := apply(1000)(); err != nil {
		t.Fatalf("apply baseline: %v", err)
	}
	// 三轮双并发提交同一样本：增量 500/600/700，每轮只许入账一次。
	for _, bytes := range []int64{1500, 2100, 2800} {
		applyConcurrent(t, 4, apply(bytes))
	}
	var got model.NodeInbound
	if err := r.DB().First(&got, inbound.ID).Error; err != nil {
		t.Fatalf("reload inbound: %v", err)
	}
	if got.UploadBytes != 1800 {
		t.Fatalf("inbound double-counted on PG: upload_bytes=%d, want 1800 (500+600+700, each round counted once)", got.UploadBytes)
	}
	var counter model.NodeInboundCounter
	if err := r.DB().Where("inbound_id = ? AND protocol = ? AND direction = ?", inbound.ID, "vless", "in").First(&counter).Error; err != nil {
		t.Fatalf("reload inbound counter: %v", err)
	}
	if counter.Bytes != 2800 {
		t.Fatalf("inbound baseline=%d, want 2800", counter.Bytes)
	}
}
