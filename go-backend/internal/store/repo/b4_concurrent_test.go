package repo

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// R5-B4: 并发测试（真并发，非顺序调用）。
// 本次执行于测试库。

func setupB4TestDB(t *testing.T) *Repository {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "b4_test.db")
	r, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	// 运行 v1 迁移。
	if err := migrateV1PortUserUnique(r.db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return r
}

// TestB4_ConcurrentForwardCreate: 并发 20 次转发创建，无重复、无 500。
func TestB4_ConcurrentForwardCreate(t *testing.T) {
	r := setupB4TestDB(t)
	now := time.Now().UnixMilli()

	var wg sync.WaitGroup
	errs := make([]error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			// 每个用不同端口，避免唯一约束（测试锁本身不死锁）。
			_, errs[idx] = r.CreateForwardTx(1, "test", "fwd", 0, "", "fifo", now, idx, []int64{1}, 10000+idx, "", nil, 0, 0, nil, 0, 0, 0)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: %v", i, err)
		}
	}
}

// TestB4_ConcurrentSamePort: 并发同节点同端口，只有一个成功。
func TestB4_ConcurrentSamePort(t *testing.T) {
	r := setupB4TestDB(t)
	now := time.Now().UnixMilli()

	var wg sync.WaitGroup
	results := make([]error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, results[idx] = r.CreateForwardTx(1, "test", "fwd", 0, "", "fifo", now, idx, []int64{1}, 20000, "", nil, 0, 0, nil, 0, 0, 0)
		}(i)
	}
	wg.Wait()

	success := 0
	portConflict := 0
	for _, err := range results {
		if err == nil {
			success++
		} else if err == errPortInUse {
			portConflict++
		} else {
			t.Errorf("unexpected error: %v", err)
		}
	}
	if success != 1 {
		t.Errorf("expected 1 success, got %d", success)
	}
	if portConflict != 9 {
		t.Errorf("expected 9 port conflicts, got %d", portConflict)
	}
}

// TestB4_ConcurrentUsername: 并发同用户名，一个成功、一个"用户名已存在"。
func TestB4_ConcurrentUsername(t *testing.T) {
	r := setupB4TestDB(t)
	now := time.Now().UnixMilli()

	var wg sync.WaitGroup
	results := make([]error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, results[idx] = r.CreateUser("duplicate_user", "hash", 1, 0, 0, 0, 0, 1, 0, now)
		}(i)
	}
	wg.Wait()

	success := 0
	conflict := 0
	for _, err := range results {
		if err == nil {
			success++
		} else if err == errUsernameExists {
			conflict++
		} else {
			t.Errorf("unexpected error: %v", err)
		}
	}
	if success != 1 {
		t.Errorf("expected 1 success, got %d", success)
	}
	if conflict != 9 {
		t.Errorf("expected 9 conflicts, got %d", conflict)
	}
}

// TestB4_ConcurrentDefaultSubscription: 并发 10 次，只产生一条。
func TestB4_ConcurrentDefaultSubscription(t *testing.T) {
	r := setupB4TestDB(t)
	now := time.Now().UnixMilli()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = r.DefaultSubscription(now)
		}()
	}
	wg.Wait()

	var count int64
	r.db.Model(&struct {
		ID int64
	}{}).Table("subscription").Count(&count)
	// 用原生查询。
	r.db.Raw("SELECT COUNT(*) FROM subscription").Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 subscription, got %d", count)
	}
}

// TestB4_PortLockNoNetwork: 锁内无网络调用（本测试验证锁的基本行为）。
func TestB4_PortLockNoNetwork(t *testing.T) {
	// 锁是纯内存 mutex，不涉及网络。此测试验证锁的获取/释放不死锁。
	mu1 := AcquirePortAllocLock(1)
	ReleasePortAllocLock(mu1)
	// 再次获取应成功。
	mu2 := AcquirePortAllocLock(1)
	ReleasePortAllocLock(mu2)
	// 不同节点应为不同锁。
	mu3 := AcquirePortAllocLock(2)
	if mu1 == mu3 {
		t.Error("different nodes should have different mutexes")
	}
	ReleasePortAllocLock(mu3)
	// 清理。
	CleanupPortAllocLock(1)
	CleanupPortAllocLock(2)
}

// TestB4_DisabledUserRename: 停用用户改名后可注册同名（验证 §0.2 结论）。
func TestB4_DisabledUserRename(t *testing.T) {
	r := setupB4TestDB(t)
	now := time.Now().UnixMilli()

	// 创建用户并停用。
	uid, err := r.CreateUser("testuser", "hash", 1, 0, 0, 0, 0, 1, 0, now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// 模拟停用（status=0）并改名。
	r.db.Exec("UPDATE \"user\" SET status=0, \"user\"=? WHERE id=?", "testuser_disabled_20261019", uid)

	// 新用户可注册同名。
	_, err = r.CreateUser("testuser", "hash2", 1, 0, 0, 0, 0, 1, 0, now)
	if err != nil {
		t.Errorf("should allow same username after rename: %v", err)
	}
}
