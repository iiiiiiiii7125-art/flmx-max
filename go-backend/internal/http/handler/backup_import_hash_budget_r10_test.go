package handler

// 修复轮 10 路 caps：R10-10（I-F2-03）handler 入口钉格——本地
// 导入在取 backupImportMu 单飞锁之前先行校验 bcrypt 预算，超限
// 整单以 -2 点名拒绝（repo 层计数/Import 收口钉格见 repo 包
// backup_import_hash_budget_r10_test.go；Gist 恢复入口同判、
// 与本地共用 repo.ValidateBackupImportUserHashBudget）。

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"net/http/httptest"

	"go-backend/internal/store/repo"
)

func TestR1010BackupImportRejectsOverBudgetBeforeLock(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r1010-handler.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	users := make([]map[string]any, 0, 1001)
	for i := 0; i < 1001; i++ {
		users = append(users, map[string]any{
			"id": 5000 + i, "user": fmt.Sprintf("r1010-h-user-%d", i), "pwd": "PlaintextPass123",
			"roleId": 1, "expTime": 2727251700000, "flowResetTime": 1, "num": 10, "status": 1,
		})
	}
	req := newAuthenticatedJSONRequest(t, map[string]any{
		"types":   []string{"users"},
		"version": "1.0",
		"users":   users,
	})
	res := httptest.NewRecorder()
	start := time.Now()
	h.backupImport(res, req)
	elapsed := time.Since(start)

	code, msg := r1008Envelope(t, res.Body.String())
	if code != -2 || !strings.Contains(msg, "超过单次上限 1000") {
		t.Fatalf("over-budget backupImport: got code=%d msg=%q, want -2 naming the cap", code, msg)
	}
	// 锁前拒绝：不得等到任何哈希发生（基线单行 300–900ms，整单
	// 1001 行以分钟计）；宽松界 5s 只排除「已开始哈希」。
	if elapsed > 5*time.Second {
		t.Fatalf("pre-lock rejection must be immediate, took %v", elapsed)
	}
	if u, _ := r.GetUserByUsername("r1010-h-user-0"); u != nil {
		t.Fatal("rejected import must not persist any user row")
	}
}
