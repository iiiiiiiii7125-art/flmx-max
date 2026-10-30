package repo

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// B02-003（K10）回归：用户名判重大小写不敏感的整族口径。
//
// 覆盖：全部判重面折叠语义、CreateUserTx 并发变体竞态（D23 降级
// 态）、改名撞变体的唯一冲突友好映射、v5 迁移在干净库建索引 /
// 脏库点名降级 / 清理后补建、seedData 折叠占用、登录精确匹配哨兵
// （GetUserByUsername 本次未动，折叠查名必须查不到）。

func k10OpenRepo(t *testing.T) *Repository {
	t.Helper()
	r, err := Open(filepath.Join(t.TempDir(), "k10.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func k10CreateUser(t *testing.T, r *Repository, name string) int64 {
	t.Helper()
	id, err := r.CreateUser(name, "hash", 1, 0, 0, 0, 0, 1, 0, time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("CreateUser(%q): %v", name, err)
	}
	return id
}

// 全部判重面（UserExists / UserExistsExcluding / UsernameExists /
// UsernameExistsExceptID / ensureUsernameAvailable）对大小写变体
// 必须同口径命中；排除自身时变体自查必须放行。
func TestK10_FoldSemanticsAcrossCheckFaces(t *testing.T) {
	r := k10OpenRepo(t)
	adminID := k10CreateUser(t, r, "Admin")
	otherID := k10CreateUser(t, r, "someone")

	for _, variant := range []string{"admin", "ADMIN", "aDmIn"} {
		if ok, err := r.UserExists(variant); err != nil || !ok {
			t.Fatalf("UserExists(%q) = %v, %v; want true, nil", variant, ok, err)
		}
		if ok, err := r.UsernameExists(variant); err != nil || !ok {
			t.Fatalf("UsernameExists(%q) = %v, %v; want true, nil", variant, ok, err)
		}
		if ok, err := r.UserExistsExcluding(variant, otherID); err != nil || !ok {
			t.Fatalf("UserExistsExcluding(%q, other) = %v, %v; want true, nil", variant, ok, err)
		}
		if ok, err := r.UsernameExistsExceptID(variant, otherID); err != nil || !ok {
			t.Fatalf("UsernameExistsExceptID(%q, other) = %v, %v; want true, nil", variant, ok, err)
		}
	}
	// 排除自身：变体指向自己必须放行（改名只改大小写合法）。
	if ok, err := r.UserExistsExcluding("ADMIN", adminID); err != nil || ok {
		t.Fatalf("UserExistsExcluding(self variant) = %v, %v; want false, nil", ok, err)
	}
	if ok, err := r.UsernameExistsExceptID("ADMIN", adminID); err != nil || ok {
		t.Fatalf("UsernameExistsExceptID(self variant) = %v, %v; want false, nil", ok, err)
	}
	// 事务内检查（备份导入腿）同口径。
	tx := r.BeginTx()
	if tx == nil || tx.Error != nil {
		t.Fatalf("begin tx: %v", tx.Error)
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureUsernameAvailable(tx, "ADMIN", 0); !errors.Is(err, errUsernameExists) {
		t.Fatalf("ensureUsernameAvailable(variant) = %v; want errUsernameExists", err)
	}
	if err := ensureUsernameAvailable(tx, "ADMIN", adminID); err != nil {
		t.Fatalf("ensureUsernameAvailable(self variant) = %v; want nil", err)
	}
	if err := ensureUsernameAvailable(tx, "brand-new", 0); err != nil {
		t.Fatalf("ensureUsernameAvailable(new) = %v; want nil", err)
	}
}

// 登录面哨兵：GetUserByUsername 保持精确匹配（B02-003 未动登录），
// 折叠变体查不到、存储形态查得到。判重改动不得把登录一并折叠。
func TestK10_LoginLookupStaysExactMatch(t *testing.T) {
	r := k10OpenRepo(t)
	k10CreateUser(t, r, "Admin")
	if u, err := r.GetUserByUsername("admin"); err != nil || u != nil {
		t.Fatalf("GetUserByUsername(%q) = %v, %v; want nil, nil (login stays exact)", "admin", u, err)
	}
	if u, err := r.GetUserByUsername("Admin"); err != nil || u == nil {
		t.Fatalf("GetUserByUsername(%q) = %v, %v; want user, nil", "Admin", u, err)
	}
}

// CreateUserTx 串行路径：先建 Bob，再以变体建号必须在事务内复检
// 被拒（不依赖唯一索引）。
func TestK10_CreateUserTxFoldVariantRejected(t *testing.T) {
	r := k10OpenRepo(t)
	now := time.Now().UnixMilli()
	create := func(name string) error {
		tx := r.BeginTx()
		if tx == nil || tx.Error != nil {
			return tx.Error
		}
		if _, err := r.CreateUserTx(tx, name, "hash", 1, 0, 0, 0, 0, 1, 0, now); err != nil {
			_ = tx.Rollback()
			return err
		}
		return tx.Commit().Error
	}
	if err := create("Bob"); err != nil {
		t.Fatalf("create Bob: %v", err)
	}
	if err := create("bOB"); !errors.Is(err, errUsernameExists) {
		t.Fatalf("create bOB = %v; want errUsernameExists", err)
	}
}

// D23 降级态（两个唯一索引都缺席）下 8 路并发以同一折叠名的不同
// 变体走 CreateUserTx：只许 1 个成功、其余全部 errUsernameExists、
// 最终折叠计数恰 1。SQLite 靠串行连接、PG 靠折叠键咨询锁（PG 面
// 由 contract 测试 TestPostgresUsernameFoldConcurrentVariants 覆盖）。
func TestK10_ConcurrentCreateUserTxFoldVariantsWithoutUniqueIndexes(t *testing.T) {
	r := k10OpenRepo(t)
	for _, idx := range []string{"idx_user_username_unique", usernameFoldIndexName} {
		if err := r.db.Exec(`DROP INDEX IF EXISTS ` + idx).Error; err != nil {
			t.Fatalf("drop %s to simulate D23 skip state: %v", idx, err)
		}
	}
	now := time.Now().UnixMilli()
	variants := []string{"RaceUser", "raceuser", "RACEUSER", "Raceuser", "rAceUser", "raCeUser", "racEUser", "raceUseR"}
	start := make(chan struct{})
	errs := make([]error, len(variants))
	var wg sync.WaitGroup
	for i, name := range variants {
		wg.Add(1)
		go func(idx int, uname string) {
			defer wg.Done()
			<-start
			tx := r.BeginTx()
			if tx == nil || tx.Error != nil {
				errs[idx] = tx.Error
				return
			}
			if _, err := r.CreateUserTx(tx, uname, "hash", 1, 0, 0, 0, 0, 1, 0, now); err != nil {
				_ = tx.Rollback()
				errs[idx] = err
				return
			}
			errs[idx] = tx.Commit().Error
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
		if !errors.Is(err, errUsernameExists) {
			t.Fatalf("goroutine %d (%q): want errUsernameExists, got %v", i, variants[i], err)
		}
	}
	if success != 1 {
		t.Fatalf("concurrent fold-variant CreateUserTx successes = %d, want exactly 1", success)
	}
	cnt, err := usernameFoldCount(r.db, "raceuser", 0)
	if err != nil {
		t.Fatalf("fold count: %v", err)
	}
	if cnt != 1 {
		t.Fatalf("fold-variant rows = %d, want 1", cnt)
	}
}

// 改名撞变体：折叠唯一索引在场时，UpdateUserNameAndPassword 与
// UpdateUserWithPassword 的唯一冲突必须映射为 errUsernameExists，
// 不得把 DB 原文漏出；只改自己名字的大小写必须成功。
func TestK10_RenameOntoFoldVariantMapsToUsernameExists(t *testing.T) {
	r := k10OpenRepo(t)
	aliceID := k10CreateUser(t, r, "alice")
	bobID := k10CreateUser(t, r, "bob")
	now := time.Now().UnixMilli()

	if err := r.UpdateUserNameAndPassword(bobID, "ALICE", "hash2", now); !errors.Is(err, ErrUsernameExists) {
		t.Fatalf("UpdateUserNameAndPassword onto variant = %v; want ErrUsernameExists", err)
	}
	if err := r.UpdateUserWithPassword(bobID, "Alice", "hash2", 0, 0, 0, 0, 0, 0, now); !errors.Is(err, ErrUsernameExists) {
		t.Fatalf("UpdateUserWithPassword onto variant = %v; want ErrUsernameExists", err)
	}
	if err := r.UpdateUserWithoutPassword(bobID, "aLiCe", 0, 0, 0, 0, 0, 0, now); !errors.Is(err, ErrUsernameExists) {
		t.Fatalf("UpdateUserWithoutPassword onto variant = %v; want ErrUsernameExists", err)
	}
	// 撞名失败后 bob 原名不动。
	u, err := r.GetUserByID(bobID)
	if err != nil || u == nil || u.User != "bob" {
		t.Fatalf("bob after failed renames = %+v, %v; want user bob intact", u, err)
	}
	// 只改自己大小写：合法，放行且落库为新形态。
	if err := r.UpdateUserNameAndPassword(aliceID, "Alice", "hash3", now); err != nil {
		t.Fatalf("self case-only rename: %v", err)
	}
	u, err = r.GetUserByID(aliceID)
	if err != nil || u == nil || u.User != "Alice" {
		t.Fatalf("alice after self rename = %+v, %v; want stored form Alice", u, err)
	}
}

// v5 迁移：干净库 Open 后折叠唯一索引已建、版本已记录。
func TestK10_MigrationV5AppliedOnCleanDB(t *testing.T) {
	r := k10OpenRepo(t)
	exists, err := c12IndexExists(r.db, usernameFoldIndexName)
	if err != nil {
		t.Fatalf("index exists check: %v", err)
	}
	if !exists {
		t.Fatalf("index %s missing on clean DB", usernameFoldIndexName)
	}
	var m schemaMigration
	if err := r.db.Where("version = ?", migrationK10V5Version).First(&m).Error; err != nil {
		t.Fatalf("migration %s not recorded: %v", migrationK10V5Version, err)
	}
}

// v5 迁移脏数据降级：模拟存量旧库（无 v5 版本记录、大小写双生子
// 在库）——迁移必须不报错、不建索引、不记版本、不动存量行；
// 待处理状态必须如实上报；清理掉一个后重跑必须补建索引并记版本。
func TestK10_MigrationV5DirtyDataDegradesThenRecovers(t *testing.T) {
	r := k10OpenRepo(t)
	// 退回「升级前」形态：删 v5 版本记录 + 拆折叠索引（精确索引
	// 不拦变体双生子，保留以模拟真实存量库）。
	if err := r.db.Exec(`DELETE FROM schema_migration WHERE version = ?`, migrationK10V5Version).Error; err != nil {
		t.Fatalf("unrecord v5: %v", err)
	}
	if err := r.db.Exec(`DROP INDEX IF EXISTS ` + usernameFoldIndexName).Error; err != nil {
		t.Fatalf("drop fold index: %v", err)
	}
	legacyA := k10CreateUser(t, r, "LegacyAdmin")
	legacyB := k10CreateUser(t, r, "legacyadmin")

	if err := migrateV5UsernameCasefoldUnique(r.db); err != nil {
		t.Fatalf("dirty migration must not error (panel must start): %v", err)
	}
	exists, err := c12IndexExists(r.db, usernameFoldIndexName)
	if err != nil {
		t.Fatalf("index exists check: %v", err)
	}
	if exists {
		t.Fatal("fold index must NOT be created over case-variant duplicates")
	}
	var rec int64
	if err := r.db.Model(&schemaMigration{}).Where("version = ?", migrationK10V5Version).Count(&rec).Error; err != nil {
		t.Fatalf("count version rows: %v", err)
	}
	if rec != 0 {
		t.Fatal("v5 must stay unrecorded while duplicates exist (retry next startup)")
	}
	// 存量双生子原样保留：不合并、不改名、不删行。
	cnt, err := usernameFoldCount(r.db, "LEGACYADMIN", 0)
	if err != nil || cnt != 2 {
		t.Fatalf("twins after dirty migration: count=%d err=%v; want 2, nil", cnt, err)
	}
	ua, err := r.GetUserByID(legacyA)
	if err != nil || ua == nil || ua.User != "LegacyAdmin" {
		t.Fatalf("twin A mutated: %+v, %v", ua, err)
	}
	// 检测口径：分组列举必须点名这一组且含两个 id。
	groups, err := listUsernameFoldDupGroups(r.db)
	if err != nil {
		t.Fatalf("list dup groups: %v", err)
	}
	if len(groups) != 1 || groups[0].Fold != "legacyadmin" {
		t.Fatalf("dup groups = %+v; want exactly the legacyadmin group", groups)
	}
	// 待迁移状态如实上报（健康检查面）。
	st := r.GetPendingMigrationStatus()
	if !st.Required {
		t.Fatal("GetPendingMigrationStatus must report Required while fold twins block v5")
	}
	foundUser := false
	for _, tbl := range st.Tables {
		if tbl.Table == "user" && tbl.DuplicateGroups == 1 {
			foundUser = true
		}
	}
	if !foundUser {
		t.Fatalf("pending status tables = %+v; want user entry with 1 group", st.Tables)
	}
	// 降级期间应用层判重仍执法：第三个变体必须被拒。
	if ok, err := r.UserExists("LEGACYADMIN"); err != nil || !ok {
		t.Fatalf("UserExists in degraded state = %v, %v; want true, nil", ok, err)
	}

	// 人工清理掉一个双生子后，重跑迁移补建索引并记版本。
	if err := r.db.Exec(`DELETE FROM "user" WHERE id = ?`, legacyB).Error; err != nil {
		t.Fatalf("cleanup twin B: %v", err)
	}
	if err := migrateV5UsernameCasefoldUnique(r.db); err != nil {
		t.Fatalf("migration after cleanup: %v", err)
	}
	exists, err = c12IndexExists(r.db, usernameFoldIndexName)
	if err != nil || !exists {
		t.Fatalf("fold index after cleanup: exists=%v err=%v; want true, nil", exists, err)
	}
	if err := r.db.Where("version = ?", migrationK10V5Version).First(&schemaMigration{}).Error; err != nil {
		t.Fatalf("v5 not recorded after cleanup: %v", err)
	}
}

// seedData 折叠占用：id=1 缺失且 admin@user 的大小写变体已被占用
// 时，不得补建（会撞唯一索引）、必须走人工处理标记，且不落新行。
func TestK10_SeedSkipsWhenFoldVariantTaken(t *testing.T) {
	r := k10OpenRepo(t)
	defer func() { adminBootstrapRequired = false }()
	k10CreateUser(t, r, "filler-user")
	if err := r.db.Exec(`DELETE FROM "user" WHERE id = 1`).Error; err != nil {
		t.Fatalf("delete seeded admin: %v", err)
	}
	variantID := k10CreateUser(t, r, "Admin@User")
	if variantID == 1 {
		t.Fatal("test setup broken: variant took id=1, seed scenario unreachable")
	}
	seedData(r.db)
	if !IsAdminBootstrapRequired() {
		t.Fatal("seedData must flag manual handling when a case variant of admin@user exists")
	}
	var id1 int64
	if err := r.db.Model(&model.User{}).Where("id = ?", 1).Count(&id1).Error; err != nil {
		t.Fatalf("count id=1: %v", err)
	}
	if id1 != 0 {
		t.Fatal("seedData must not create id=1 while the fold variant is taken")
	}
}
