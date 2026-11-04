package health

import (
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R1-09-L1 回归：手动 RunOnce 的结果必须回写内存最新缓存，总览读面
// （纯缓存快径）立即可见；且时间戳守卫保证并发的更新调度结果不被
// 较旧的手动结果覆盖。
func TestCheckerRunOnceUpdatesLatestCache(t *testing.T) {
	t.Setenv("FLVX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	checker := NewChecker(nil, nil)
	monitor := &model.ServiceMonitor{
		ID: 4242, Type: "tcp", Target: "127.0.0.1:1", TimeoutSec: 1,
	}

	res, err := checker.RunOnce(monitor)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res == nil {
		t.Fatal("RunOnce returned nil result")
	}
	cached := checker.GetLatestCached()
	if len(cached) != 1 || cached[0].MonitorID != monitor.ID || cached[0].Timestamp != res.Timestamp {
		t.Fatalf("latest cache after RunOnce = %+v, want the manual result", cached)
	}

	// 守卫：缓存中已有更新时间戳的结果时，手动结果不得把它顶掉。
	future := &model.ServiceMonitorResult{
		MonitorID: monitor.ID,
		Timestamp: time.Now().UnixMilli() + 60_000,
	}
	checker.SeedLatestCachedForTesting(future)
	res2, err := checker.RunOnce(monitor)
	if err != nil {
		t.Fatalf("RunOnce (guard): %v", err)
	}
	cached = checker.GetLatestCached()
	if len(cached) != 1 || cached[0].Timestamp != future.Timestamp {
		t.Fatalf("newer cached result must survive older RunOnce result: got %+v, manual ts=%d", cached, res2.Timestamp)
	}
}
