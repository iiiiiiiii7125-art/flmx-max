package repo

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// C04（B04-001/B04-002）回归：inx 原子分配与节点写操作回执契约。

func setupC04TestDB(t *testing.T) *Repository {
	t.Helper()
	r, err := Open(filepath.Join(t.TempDir(), "c04_test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	return r
}

func createC04Node(t *testing.T, r *Repository, name string, inx int) int64 {
	t.Helper()
	now := time.Now().UnixMilli()
	id, err := r.CreateNode(name, "secret", "198.51.100.10", nil, nil, "1000-65535", nil, nil, nil, nil, nil, 0, 0, 0, now, 1, "", "", inx, 0, nil, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("create node %s: %v", name, err)
	}
	return id
}

// TestC04_ConcurrentCreateNodeInxUnique 复现 B04-001 的原始形态：
// 12 路并发走 handler 的「NextIndex → CreateNode」两步口径建节点，
// 落库 inx 必须互不重复。旧实现（NextIndex 纯 SELECT MIN）在此
// 必撞（实测 -7×3、-5×2 等）。
func TestC04_ConcurrentCreateNodeInxUnique(t *testing.T) {
	r := setupC04TestDB(t)
	const n = 12
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			inx := r.NextIndex("node")
			now := time.Now().UnixMilli()
			_, errs[idx] = r.CreateNode("conc-node", "secret", "198.51.100.10", nil, nil, "1000-65535", nil, nil, nil, nil, nil, 0, 0, 0, now, 1, "", "", inx, 0, nil, nil, nil, nil, "")
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d create: %v", i, err)
		}
	}
	var inxs []int
	if err := r.db.Raw("SELECT inx FROM node ORDER BY inx ASC").Scan(&inxs).Error; err != nil {
		t.Fatalf("query inx: %v", err)
	}
	if len(inxs) != n {
		t.Fatalf("expected %d nodes, got %d", n, len(inxs))
	}
	seen := map[int]int{}
	for _, v := range inxs {
		seen[v]++
		if seen[v] > 1 {
			t.Fatalf("duplicate inx %d in %v (B04-001 regression)", v, inxs)
		}
	}
}

// TestC04_NextIndexFollowsLoweredDBMin：导入/手动排序把某节点 inx
// 压到远低于发放台账时，后续发放必须跟随 DB 最小值继续向下，
// 不得回到台账水位撞上既有行。
func TestC04_NextIndexFollowsLoweredDBMin(t *testing.T) {
	r := setupC04TestDB(t)
	first := r.NextIndex("node")
	id := createC04Node(t, r, "n1", first)
	if _, err := r.UpdateNodeOrder(id, -1000, time.Now().UnixMilli()); err != nil {
		t.Fatalf("update order: %v", err)
	}
	next := r.NextIndex("node")
	if next >= -1000 {
		t.Fatalf("NextIndex = %d, want < -1000 (must follow lowered DB min)", next)
	}
}

// TestC04_UpdateNodeOrderReceipt：B04-002 的 repo 契约——更新存在
// 节点回 1 行、无错误；不存在的节点回 0 行（handler 据此逐条聚合
// 回执），不再静默吞掉。
func TestC04_UpdateNodeOrderReceipt(t *testing.T) {
	r := setupC04TestDB(t)
	id := createC04Node(t, r, "n1", 0)
	rows, err := r.UpdateNodeOrder(id, -5, time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("update existing: %v", err)
	}
	if rows != 1 {
		t.Fatalf("update existing rows = %d, want 1", rows)
	}
	var gotInx int
	if err := r.db.Raw("SELECT inx FROM node WHERE id = ?", id).Scan(&gotInx).Error; err != nil {
		t.Fatalf("query inx: %v", err)
	}
	if gotInx != -5 {
		t.Fatalf("inx = %d, want -5", gotInx)
	}
	rows, err = r.UpdateNodeOrder(99999, -5, time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("update missing: %v", err)
	}
	if rows != 0 {
		t.Fatalf("update missing rows = %d, want 0", rows)
	}
}

// TestC04_DismissExpiryReminderMissingNode：B04-002 dismiss 子项——
// 不存在的节点必须回「节点不存在」错误（handler 既有 err 分支会
// 透出），存在节点正常置位且回 nil。
func TestC04_DismissExpiryReminderMissingNode(t *testing.T) {
	r := setupC04TestDB(t)
	err := r.UpdateNodeExpiryReminderDismissed(99999, 1)
	if err == nil {
		t.Fatal("dismiss missing node: expected error, got nil (B04-002 regression)")
	}
	if !strings.Contains(err.Error(), "节点不存在") {
		t.Fatalf("dismiss missing node error = %q, want 节点不存在", err.Error())
	}
	id := createC04Node(t, r, "n1", 0)
	if err := r.UpdateNodeExpiryReminderDismissed(id, 1); err != nil {
		t.Fatalf("dismiss existing: %v", err)
	}
	var dismissed int
	if err := r.db.Raw("SELECT expiry_reminder_dismissed FROM node WHERE id = ?", id).Scan(&dismissed).Error; err != nil {
		t.Fatalf("query dismissed: %v", err)
	}
	if dismissed != 1 {
		t.Fatalf("expiry_reminder_dismissed = %d, want 1", dismissed)
	}
}
