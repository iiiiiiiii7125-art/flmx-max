package health

import (
	"context"
	"net"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R18-B11-02：调度 worker 写缓存无时间戳守卫，可把手动 RunOnce 的
// 更新结果覆盖回陈旧的调度结果（RunOnce 侧守卫 :131-132 是单边的）。
// 修法：worker 写缓存改为与 RunOnce 同式守卫（仅当结果不旧于缓存
// 才覆盖），lastRun 同理只向前推进。
// 回归钉：先以未来时间戳播种一条更新的缓存结果，再跑一轮调度检查
// （其结果时间戳为“现在”，旧于种子）；旧代码下 worker 无条件覆盖，
// 缓存回退到旧时间戳——必红；修后守卫留住新时间戳——变绿。
func TestR18B1102CheckerWorkerWriteDoesNotClobberNewerRunOnce(t *testing.T) {
	// AUDIT-R2 (F1X-001)：本测试验证拨号机制，需实际连接 127.0.0.1。
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	tcpAddr := listener.Addr().String()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	now := time.Now().UnixMilli()
	monitor := &model.ServiceMonitor{
		Name:        "TCP Monitor",
		Type:        "tcp",
		Target:      tcpAddr,
		IntervalSec: 60,
		TimeoutSec:  5,
		NodeID:      0,
		Enabled:     1,
		CreatedTime: now,
		UpdatedTime: now,
	}
	if err := r.CreateServiceMonitor(monitor); err != nil {
		t.Fatalf("create monitor: %v", err)
	}

	checker := NewChecker(r, nil)
	// 种子一条“更新”的手动结果（未来时间戳，复刻管理员刚点过手动执行）。
	newerTs := time.Now().UnixMilli() + 60000
	checker.SeedLatestCachedForTesting(&model.ServiceMonitorResult{
		MonitorID: monitor.ID,
		Timestamp: newerTs,
		Success:   1,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	checker.runChecks(ctx)

	// 等 worker 完成（DB 落行在缓存写之后，以 DB 行作完成信号）。
	deadline := time.Now().Add(10 * time.Second)
	for {
		results, err := r.GetServiceMonitorResults(monitor.ID, 10)
		if err != nil {
			t.Fatalf("get results: %v", err)
		}
		if len(results) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the scheduled worker result")
		}
		time.Sleep(50 * time.Millisecond)
	}

	cached := checker.GetLatestCached()
	if len(cached) != 1 {
		t.Fatalf("expected exactly one cached result, got %d", len(cached))
	}
	if cached[0].Timestamp != newerTs {
		t.Fatalf("R18-B11-02: worker write must not clobber the newer RunOnce result: cached ts=%d, want %d", cached[0].Timestamp, newerTs)
	}
}
