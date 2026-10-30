package contract_test

import (
	"database/sql"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"go-backend/internal/store/repo"
)

// B02-003（K10）PostgreSQL 面：判重大小写不敏感在真 PG 上成立。
//
// 沙箱无 PG，本组以 FLMX_POSTGRES_TEST_DSN 门控（与
// postgres_counter_concurrent_contract_test 同口径，CI 的
// TestPostgres* 组/T2 执行）。SQLite 面由 repo 包
// r2_k10_username_fold_test.go 覆盖。
//
// PG 与 SQLite 的关键差异点逐一钉死：
//   - lower() 折叠比较在 READ COMMITTED 下的应用层判重；
//   - CreateUserTx 并发变体：折叠键咨询锁串行化（旧实现按原文
//     取锁 + 精确复检，变体并发可双双落库）；
//   - v5 函数唯一索引 (LOWER("user")) 的 DB 兜底与冲突转译；
//   - 存量双生子在库时 OpenPostgres 不报错（D23 降级）、不合并，
//     应用层判重仍执法。

func openUsernameFoldContractRepo(t *testing.T) (*repo.Repository, string) {
	t.Helper()
	baseDSN := strings.TrimSpace(os.Getenv("FLMX_POSTGRES_TEST_DSN"))
	if baseDSN == "" {
		t.Skip("set FLMX_POSTGRES_TEST_DSN to run postgres contract tests")
	}
	schemaName := "contract_username_fold_" + strconv.FormatInt(time.Now().UnixNano(), 36)
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
	return r, testDSN
}

func createUserTx(t *testing.T, r *repo.Repository, name string) error {
	t.Helper()
	tx := r.BeginTx()
	if tx == nil || tx.Error != nil {
		return tx.Error
	}
	if _, err := r.CreateUserTx(tx, name, "hash", 1, 0, 0, 0, 0, 1, 0, time.Now().UnixMilli()); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit().Error
}

// 顺序面：建 Admin 后，全部判重面与 CreateUserTx 对变体同口径拒绝；
// 改名撞变体经唯一冲突映射回 ErrUsernameExists（不漏 PG 原文）。
func TestPostgresUsernameFoldChecksAndRename(t *testing.T) {
	r, _ := openUsernameFoldContractRepo(t)
	if err := createUserTx(t, r, "Admin"); err != nil {
		t.Fatalf("create Admin: %v", err)
	}
	for _, variant := range []string{"admin", "ADMIN", "aDmIn"} {
		if ok, err := r.UserExists(variant); err != nil || !ok {
			t.Fatalf("UserExists(%q) = %v, %v; want true, nil", variant, ok, err)
		}
		if ok, err := r.UsernameExists(variant); err != nil || !ok {
			t.Fatalf("UsernameExists(%q) = %v, %v; want true, nil", variant, ok, err)
		}
		if err := createUserTx(t, r, variant); !errors.Is(err, repo.ErrUsernameExists) {
			t.Fatalf("CreateUserTx(%q) = %v; want ErrUsernameExists", variant, err)
		}
	}
	bobID, err := r.CreateUser("bob", "hash", 1, 0, 0, 0, 0, 1, 0, time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	if _, err := r.CreateUser("alice", "hash", 1, 0, 0, 0, 0, 1, 0, time.Now().UnixMilli()); err != nil {
		t.Fatalf("create alice: %v", err)
	}
	if err := r.UpdateUserNameAndPassword(bobID, "Alice", "hash3", time.Now().UnixMilli()); !errors.Is(err, repo.ErrUsernameExists) {
		t.Fatalf("rename onto fold variant = %v; want ErrUsernameExists (mapped, no raw PG error)", err)
	}
	if err := r.UpdateUserNameAndPassword(bobID, "Bobby", "hash3", time.Now().UnixMilli()); err != nil {
		t.Fatalf("legit rename bob->Bobby must succeed: %v", err)
	}
}

// 并发面：8 路并发以同一折叠名的不同变体走 CreateUserTx，只许
// 1 个成功。PG 无 SQLite 的串行连接兜底，全靠折叠键咨询锁 +
// 折叠复检（索引在场时另有 DB 兜底，本用例先 DROP 折叠索引，
// 把防线压到锁+复检本身，与 X01-001 的降级口径对齐）。
func TestPostgresUsernameFoldConcurrentVariants(t *testing.T) {
	r, _ := openUsernameFoldContractRepo(t)
	if err := r.DB().Exec(`DROP INDEX IF EXISTS idx_user_username_fold_unique`).Error; err != nil {
		t.Fatalf("drop fold index to test lock+recheck line: %v", err)
	}
	variants := []string{"RaceUser", "raceuser", "RACEUSER", "Raceuser", "rAceUser", "raCeUser", "racEUser", "raceUseR"}
	start := make(chan struct{})
	errs := make([]error, len(variants))
	var wg sync.WaitGroup
	for i, name := range variants {
		wg.Add(1)
		go func(idx int, uname string) {
			defer wg.Done()
			<-start
			errs[idx] = createUserTx(t, r, uname)
		}(i, name)
	}
	close(start)
	wg.Wait()

	success := 0
	for i, err := range errs {
		if err == nil {
			success++
			continue
		}
		if !errors.Is(err, repo.ErrUsernameExists) {
			t.Fatalf("goroutine %d (%q): want ErrUsernameExists, got %v", i, variants[i], err)
		}
	}
	if success != 1 {
		t.Fatalf("concurrent fold-variant successes = %d, want exactly 1", success)
	}
	if ok, err := r.UserExists("RACEUSER"); err != nil || !ok {
		t.Fatalf("UserExists after race = %v, %v; want true, nil", ok, err)
	}
}

// LB-K10-02 钉死：咨询锁键的折叠必须与判重同源（SQL lower()）。
// 见证对 (ΟΣ, ος) 与 (İ, i̇)：PG lower() 把每对折为同值，而 Go
// strings.ToLower 对每对给出不同结果——旧实现以 Go 折叠派生锁键
// 时，两变体各持一把锁、事务内复检互不可见，在 D23 降级态
// （折叠索引缺席）下并发 CreateUserTx 可双双落库。本用例 DROP
// 折叠索引把防线压到咨询锁+复检本身，每对并发只许 1 个成功；
// 锁键若回退 Go 折叠，本用例将以双成功转红。每对先经 SQL 探针
// 确认本库 lower() 确实同折（C locale 等不做 Unicode 折叠的库
// 里两名在判重口径下本就不同名、无分歧，探针不成立即跳过该对）。
func TestPostgresUsernameFoldLockFoldFromSQL(t *testing.T) {
	r, _ := openUsernameFoldContractRepo(t)
	sqlFold := func(name string) string {
		var folded string
		if err := r.DB().Raw(`SELECT LOWER(?)`, name).Scan(&folded).Error; err != nil {
			t.Fatalf("probe LOWER(%q): %v", name, err)
		}
		return folded
	}
	pairs := [][2]string{
		{"ΟΣ", "ος"}, // ΟΣ / ος（词尾 sigma）
		{"İ", "i̇"},  // İ / i + combining dot above
	}
	probed := make([][2]string, 0, len(pairs))
	for _, pair := range pairs {
		if sqlFold(pair[0]) == sqlFold(pair[1]) {
			probed = append(probed, pair)
		}
	}
	if len(probed) == 0 {
		t.Skip("test database lower() folds neither witness pair (locale without Unicode case folding); no lock/check fold divergence exists there")
	}
	if err := r.DB().Exec(`DROP INDEX IF EXISTS idx_user_username_fold_unique`).Error; err != nil {
		t.Fatalf("drop fold index to test lock+recheck line: %v", err)
	}
	for _, pair := range probed {
		start := make(chan struct{})
		errs := make([]error, 2)
		var wg sync.WaitGroup
		for i, name := range pair {
			wg.Add(1)
			go func(idx int, uname string) {
				defer wg.Done()
				<-start
				errs[idx] = createUserTx(t, r, uname)
			}(i, name)
		}
		close(start)
		wg.Wait()
		success := 0
		for i, err := range errs {
			if err == nil {
				success++
				continue
			}
			if !errors.Is(err, repo.ErrUsernameExists) {
				t.Fatalf("pair %q/%q goroutine %d: want ErrUsernameExists, got %v", pair[0], pair[1], i, err)
			}
		}
		if success != 1 {
			t.Fatalf("pair %q/%q: concurrent successes = %d, want exactly 1 (lock key must fold via SQL lower(), not Go strings.ToLower)", pair[0], pair[1], success)
		}
	}
}

// 脏数据面：存量双生子在库（模拟旧版精确口径建出的 Admin+admin）
// 时重新 OpenPostgres 必须成功（v5 按 D23 跳过、不报错、不合并），
// 双生子原样保留，应用层判重对第三个变体仍拒绝。
func TestPostgresUsernameFoldDirtyDataReopenDegrades(t *testing.T) {
	r, dsn := openUsernameFoldContractRepo(t)
	// 退回升级前形态：拆折叠索引 + 删 v5 版本记录，再用精确口径
	// 的裸写入口造双生子（CreateUser 无应用层预检、只靠索引兜底，
	// 索引拆除后可落库，与旧版行为同形）。
	if err := r.DB().Exec(`DROP INDEX IF EXISTS idx_user_username_fold_unique`).Error; err != nil {
		t.Fatalf("drop fold index: %v", err)
	}
	if err := r.DB().Exec(`DELETE FROM schema_migration WHERE version = 'k10_v5_username_casefold_unique'`).Error; err != nil {
		t.Fatalf("unrecord v5: %v", err)
	}
	if _, err := r.CreateUser("LegacyAdmin", "hash", 1, 0, 0, 0, 0, 1, 0, time.Now().UnixMilli()); err != nil {
		t.Fatalf("create LegacyAdmin: %v", err)
	}
	if _, err := r.CreateUser("legacyadmin", "hash", 1, 0, 0, 0, 0, 1, 0, time.Now().UnixMilli()); err != nil {
		t.Fatalf("create legacyadmin twin: %v", err)
	}
	_ = r.Close()

	r2, err := repo.OpenPostgres(dsn)
	if err != nil {
		t.Fatalf("reopen over case-variant twins must not fail (D23 degrade): %v", err)
	}
	defer func() { _ = r2.Close() }()
	// 双生子不被静默合并/改名：两个存储形态都还在。
	for _, form := range []string{"LegacyAdmin", "legacyadmin"} {
		u, err := r2.GetUserByUsername(form)
		if err != nil || u == nil {
			t.Fatalf("twin %q missing after reopen: u=%v err=%v", form, u, err)
		}
	}
	// 降级期应用层判重仍执法。
	if ok, err := r2.UserExists("LEGACYADMIN"); err != nil || !ok {
		t.Fatalf("UserExists in degraded state = %v, %v; want true, nil", ok, err)
	}
	if err := createUserTx(t, r2, "LEGACYADMIN"); !errors.Is(err, repo.ErrUsernameExists) {
		t.Fatalf("CreateUserTx third variant in degraded state = %v; want ErrUsernameExists", err)
	}
}
