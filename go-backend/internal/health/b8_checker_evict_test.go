package health

import (
	"context"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// F-084 变体 (audit FINAL §5-B8 / A7-011 同型): health.Checker 的
// latestResults/lastRun/lastDBWrite 只写不淘汰，监控删除/停用后其实测
// 结果仍被 monitorServiceLatestResultsHandler 的 cache 优先路径返回。
// 修复前本测试失败：runChecks 后幽灵 monitor 999 的缓存仍在。
func TestCheckerEvictsStaleResultsF084(t *testing.T) {
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	live := &model.ServiceMonitor{
		Name: "f084-live", Type: "tcp", Target: "127.0.0.1:1",
		IntervalSec: 60, TimeoutSec: 2, Enabled: 1,
		CreatedTime: now, UpdatedTime: now,
	}
	if err := r.CreateServiceMonitor(live); err != nil {
		t.Fatal(err)
	}
	if live.ID <= 0 {
		t.Fatal("create must assign the monitor ID")
	}

	checker := NewChecker(r, nil)
	checker.mu.Lock()
	checker.latestResults[999] = &model.ServiceMonitorResult{MonitorID: 999, Timestamp: now}
	checker.lastRun[999] = now
	checker.lastDBWrite[999] = now
	checker.mu.Unlock()

	checker.runChecks(context.Background())

	checker.mu.RLock()
	_, staleResult := checker.latestResults[999]
	_, staleRun := checker.lastRun[999]
	_, staleWrite := checker.lastDBWrite[999]
	checker.mu.RUnlock()
	if staleResult || staleRun || staleWrite {
		t.Fatalf("stale monitor caches must be evicted (result=%v run=%v write=%v)",
			staleResult, staleRun, staleWrite)
	}

	// 等本轮 worker 把 live 结果写入后，停用该 monitor 再跑一轮：
	// enabled 集合为空时也必须淘汰（含 len==0 的早退路径之前）。
	deadline := time.Now().Add(3 * time.Second)
	for {
		found := false
		for _, res := range checker.GetLatestCached() {
			if res.MonitorID == live.ID {
				found = true
			}
		}
		if found || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	live.Enabled = 0
	if err := r.UpdateServiceMonitor(live); err != nil {
		t.Fatal(err)
	}
	checker.runChecks(context.Background())
	if got := checker.GetLatestCached(); len(got) != 0 {
		t.Fatalf("all cached results must be evicted once no monitor is enabled, got %+v", got)
	}
}
