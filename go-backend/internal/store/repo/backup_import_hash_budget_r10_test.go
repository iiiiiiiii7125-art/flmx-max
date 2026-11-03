package repo

// 修复轮 10 路 caps：R10-10（I-F2-03）备份导入 bcrypt 预算钉格。
//
// 基线（0cb4a7ba）红证：Import 40 行明文口令用户耗时 35.7s
// （893ms/行，沙箱争用下量级）；1001 行明文导入 15s 后仍在跑、
// 无任何预算拒绝——非哈希行逐行 cost-10 哈希且全程占单飞锁＋
// 30 分钟 detached 事务，约 2MB 构造备份即可烧满整单预算。
//
// 修复后：需哈希行数（口令非 $2 前缀）在校验相位以 1000 为帽，
// 超限整单点名拒绝（Import 进事务前自查、handler 两入口在取
// 单飞锁前先行同判）；合法备份恒携 $2 哈希、永不触发。

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

func r1010Users(n int, pwd string) []model.UserBackup {
	return r1010UsersFrom(5000, n, pwd)
}

func r1010UsersFrom(base int, n int, pwd string) []model.UserBackup {
	now := time.Now().UnixMilli()
	users := make([]model.UserBackup, 0, n)
	for i := 0; i < n; i++ {
		users = append(users, model.UserBackup{
			ID:            int64(base + i),
			User:          fmt.Sprintf("r1010-user-%d", base+i),
			Pwd:           pwd,
			RoleID:        1,
			ExpTime:       2727251700000,
			FlowResetTime: 1,
			Num:           10,
			CreatedTime:   now,
			UpdatedTime:   now,
			Status:        1,
		})
	}
	return users
}

const r1010Prehashed = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"

func TestR1010HashBudgetValidator(t *testing.T) {
	// 恰 1000 行需哈希：放行（只做计数、不真哈希）。
	atCap := &model.BackupData{Version: "1.0", Users: r1010Users(1000, "PlaintextPass123")}
	if err := ValidateBackupImportUserHashBudget(atCap, []string{"users"}); err != nil {
		t.Fatalf("1000 plaintext rows must pass the budget, got %v", err)
	}

	// 1001 行需哈希：拒绝且点名双方数字。
	over := &model.BackupData{Version: "1.0", Users: r1010Users(1001, "PlaintextPass123")}
	err := ValidateBackupImportUserHashBudget(over, []string{"users"})
	if err == nil || !strings.Contains(err.Error(), "1001") || !strings.Contains(err.Error(), "上限 1000") {
		t.Fatalf("1001 plaintext rows must be rejected naming both counts, got %v", err)
	}

	// legacy MD5 与空口令同样计入需哈希集合计。
	mixed := &model.BackupData{Version: "1.0", Users: append(
		r1010UsersFrom(5000, 600, "3c85cdebade1c51cf64ca9f3c09d182d"),
		r1010UsersFrom(20000, 401, "")...)}
	if err := ValidateBackupImportUserHashBudget(mixed, []string{"users"}); err == nil {
		t.Fatal("600 legacy + 401 empty = 1001 hash-needing rows must be rejected")
	}

	// 预哈希行不计：5000 行 $2 口令放行。
	pre := &model.BackupData{Version: "1.0", Users: r1010Users(5000, r1010Prehashed)}
	if err := ValidateBackupImportUserHashBudget(pre, []string{"users"}); err != nil {
		t.Fatalf("pre-hashed rows must never consume budget, got %v", err)
	}

	// 未选 users 段：不判。
	if err := ValidateBackupImportUserHashBudget(over, []string{"nodes"}); err != nil {
		t.Fatalf("budget must not apply when users segment is not selected, got %v", err)
	}
	// nil 备份：不判（版本判在调用方另行处理）。
	if err := ValidateBackupImportUserHashBudget(nil, []string{"users"}); err != nil {
		t.Fatalf("nil backup must pass, got %v", err)
	}
}

func TestR1010ImportRejectsOverBudgetBeforeTransaction(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "r1010-reject.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	backup := &model.BackupData{Version: "1.0", Users: r1010Users(1001, "PlaintextPass123")}
	start := time.Now()
	_, err = r.Import(backup, []string{"users"})
	elapsed := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "超过单次上限 1000") {
		t.Fatalf("over-budget import must be rejected naming the cap, got %v", err)
	}
	// 拒绝发生在任何哈希之前：整单耗时应远低于一行哈希的量级
	// 上限（基线单行 300–900ms）；给争用留宽松界 5s。
	if elapsed > 5*time.Second {
		t.Fatalf("rejection must precede hashing, took %v", elapsed)
	}
	// 未进事务：一行未落库。
	if u, _ := r.GetUserByUsername("r1010-user-0"); u != nil {
		t.Fatal("rejected import must not persist any user row")
	}
}

func TestR1010ImportPrehashedAtScaleAccepted(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "r1010-prehashed.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	// 1000 行预哈希（合法备份形态）：不触发预算、正常导入。
	backup := &model.BackupData{Version: "1.0", Users: r1010Users(1000, r1010Prehashed)}
	result, err := r.Import(backup, []string{"users"})
	if err != nil {
		t.Fatalf("pre-hashed import must succeed, got %v", err)
	}
	if result.UsersImported != 1000 {
		t.Fatalf("UsersImported = %d, want 1000", result.UsersImported)
	}
}
