package repo

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// X01-001 回归：事务内同名复检在「无唯一索引」状态下必须是唯一且
// 有效的防线。PG 侧靠 pg_advisory_xact_lock 串行化（沙箱无 PG，
// 由 T2 在 test-node-1 隔离 PG 容器复验，步骤见 fix-C16.json）；
// SQLite 侧靠 MaxOpenConns(1) 串行化，本文件以真并发锁定该机理
// 不回归（X01 原探针为 overlay 注入未落盘，本文件是其落地版）。

func x01OpenRepo(t *testing.T) *Repository {
	t.Helper()
	r, err := Open(filepath.Join(t.TempDir(), "x01.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// 咨询锁在 SQLite 下必须为空操作（方言门），不得报错、不得执行
// PG 专属 SQL；nil 事务仍按既有口径拒绝。
func TestX01_LockNameForTxSQLiteNoOp(t *testing.T) {
	r := x01OpenRepo(t)
	tx := r.BeginTx()
	if tx == nil || tx.Error != nil {
		t.Fatalf("begin tx: %v", tx.Error)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockNameForTx(tx, nameLockScopeUser, "anyone"); err != nil {
		t.Fatalf("lockNameForTx on sqlite must be a no-op, got %v", err)
	}
	if err := r.LockTunnelNameTx(tx, "any-tunnel"); err != nil {
		t.Fatalf("LockTunnelNameTx on sqlite must be a no-op, got %v", err)
	}
	if err := lockNameForTx(nil, nameLockScopeUser, "anyone"); err == nil {
		t.Fatal("lockNameForTx(nil tx) must error")
	}
}

// SQLite 真并发：D23 降级状态（DROP 用户名唯一索引）下 8 个并发
// CreateUserTx 同名，只许 1 个成功、其余全部 errUsernameExists、
// 最终恰 1 行。
func TestX01_ConcurrentCreateUserTxSameNameWithoutUniqueIndex(t *testing.T) {
	r := x01OpenRepo(t)
	if err := r.db.Exec(`DROP INDEX IF EXISTS idx_user_username_unique`).Error; err != nil {
		t.Fatalf("drop unique index to simulate D23 skip state: %v", err)
	}
	now := time.Now().UnixMilli()
	const name = "x01-race-user"
	const n = 8

	start := make(chan struct{})
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			tx := r.BeginTx()
			if tx == nil || tx.Error != nil {
				errs[idx] = tx.Error
				return
			}
			if _, err := r.CreateUserTx(tx, name, "hash", 1, 0, 0, 0, 0, 1, 0, now); err != nil {
				_ = tx.Rollback()
				errs[idx] = err
				return
			}
			if err := tx.Commit().Error; err != nil {
				errs[idx] = err
				return
			}
			errs[idx] = nil
		}(i)
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
			t.Fatalf("goroutine %d: want errUsernameExists, got %v", i, err)
		}
	}
	if success != 1 {
		t.Fatalf("concurrent same-name CreateUserTx successes = %d, want exactly 1", success)
	}
	var cnt int64
	if err := r.db.Model(&model.User{}).Where(`"user" = ?`, name).Count(&cnt).Error; err != nil {
		t.Fatalf("count users: %v", err)
	}
	if cnt != 1 {
		t.Fatalf("duplicate username rows = %d, want 1", cnt)
	}
}

// SQLite 真并发：隧道表从无唯一索引，8 个并发 InsertTunnelTx 同名
// 只许 1 个成功、其余全部 ErrTunnelNameDuplicate、最终恰 1 行。
func TestX01_ConcurrentInsertTunnelTxSameName(t *testing.T) {
	r := x01OpenRepo(t)
	now := time.Now().UnixMilli()
	const name = "x01-race-tunnel"
	const n = 8

	start := make(chan struct{})
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			tx := r.BeginTx()
			if tx == nil || tx.Error != nil {
				errs[idx] = tx.Error
				return
			}
			row := &model.Tunnel{Name: name, TrafficRatio: 1, Type: 1, Protocol: "tls", Flow: 1, CreatedTime: now, UpdatedTime: now, Status: 1}
			if err := r.InsertTunnelTx(tx, row); err != nil {
				_ = tx.Rollback()
				errs[idx] = err
				return
			}
			if err := tx.Commit().Error; err != nil {
				errs[idx] = err
				return
			}
			errs[idx] = nil
		}(i)
	}
	close(start)
	wg.Wait()

	success := 0
	for i, err := range errs {
		if err == nil {
			success++
			continue
		}
		if !errors.Is(err, ErrTunnelNameDuplicate) {
			t.Fatalf("goroutine %d: want ErrTunnelNameDuplicate, got %v", i, err)
		}
	}
	if success != 1 {
		t.Fatalf("concurrent same-name InsertTunnelTx successes = %d, want exactly 1", success)
	}
	var cnt int64
	if err := r.db.Model(&model.Tunnel{}).Where("name = ?", name).Count(&cnt).Error; err != nil {
		t.Fatalf("count tunnels: %v", err)
	}
	if cnt != 1 {
		t.Fatalf("duplicate tunnel rows = %d, want 1", cnt)
	}
}

// 并发不同名不得被咨询锁/串行化误伤：4 个不同用户名并发创建应全部成功。
func TestX01_ConcurrentCreateUserTxDistinctNames(t *testing.T) {
	r := x01OpenRepo(t)
	if err := r.db.Exec(`DROP INDEX IF EXISTS idx_user_username_unique`).Error; err != nil {
		t.Fatalf("drop unique index to simulate D23 skip state: %v", err)
	}
	now := time.Now().UnixMilli()
	names := []string{"x01-user-a", "x01-user-b", "x01-user-c", "x01-user-d"}

	start := make(chan struct{})
	errs := make([]error, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
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
	for i, err := range errs {
		if err != nil {
			t.Fatalf("distinct-name create %q failed: %v", names[i], err)
		}
	}
}
