package health

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R18-B11-03：runChecks 的 checking 轮次守卫名不副实——defer 在
// runChecks 返回（派生完 worker 即返回）时就把标志清零，而 worker
// 仍在执行检查；守卫只串行化了微秒级的分发循环，并未守卫轮次执行。
// 默认配置（扫描 1s、单检最长 60s）下轮次重叠是常态，并发拨号数
// 可达 2 × WorkerLimit。
// 修法 (a)：轮次在全部 worker 完成前不算结束——runChecks 等本轮
// worker 全部完成后再返回（defer 随之清标志）；关机取消时不等
// （ctx.Done 即提前返回，关机时延与修前一致，见 R18-B11-04 纪要）。
// 回归钉：慢检查（3s 拨号）在途时启动第二轮；旧代码下第二轮照常
// 分发新到期的监控，并发拨号数达到 2——必红；修后第二轮被 CAS
// 挡回，并发拨号数恒为 1——变绿。
func TestR18B1103OverlappingRoundsDoNotExceedWorkerLimit(t *testing.T) {
	// AUDIT-R2 (F1X-001)：本测试验证拨号机制，需实际连接 127.0.0.1。
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	slowListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen slow: %v", err)
	}
	defer slowListener.Close()
	slowAddr := slowListener.Addr().String()
	fastListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen fast: %v", err)
	}
	defer fastListener.Close()
	fastAddr := fastListener.Addr().String()
	acceptLoop := func(l net.Listener) {
		go func() {
			for {
				conn, err := l.Accept()
				if err != nil {
					return
				}
				conn.Close()
			}
		}()
	}
	acceptLoop(slowListener)
	acceptLoop(fastListener)

	// 并发拨号计数：慢目标拨号耗时 3s，快目标立即拨通。
	var cur, maxConc atomic.Int32
	savedDial := tcpDialContext
	tcpDialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		n := cur.Add(1)
		for {
			m := maxConc.Load()
			if n <= m || maxConc.CompareAndSwap(m, n) {
				break
			}
		}
		defer cur.Add(-1)
		if address == slowAddr {
			time.Sleep(3 * time.Second)
		}
		return savedDial(ctx, network, address)
	}
	t.Cleanup(func() { tcpDialContext = savedDial })

	now := time.Now().UnixMilli()
	mkMonitor := func(name, target string) *model.ServiceMonitor {
		return &model.ServiceMonitor{
			Name:        name,
			Type:        "tcp",
			Target:      target,
			IntervalSec: 1,
			TimeoutSec:  5,
			NodeID:      0,
			Enabled:     1,
			CreatedTime: now,
			UpdatedTime: now,
		}
	}
	slowMonitor := mkMonitor("Slow Monitor", slowAddr)
	lateMonitor := mkMonitor("Late Monitor", fastAddr)
	if err := r.CreateServiceMonitor(slowMonitor); err != nil {
		t.Fatalf("create slow monitor: %v", err)
	}
	if err := r.CreateServiceMonitor(lateMonitor); err != nil {
		t.Fatalf("create late monitor: %v", err)
	}

	checker := NewChecker(r, nil)
	// late 监控在第一轮分发时未到期（500ms < 1s 间隔），在第二轮
	// 分发时已到期（约 1500ms ≥ 1s 间隔）——第二轮在旧代码下必定
	// 分发它，与在途的慢检查并发。
	checker.mu.Lock()
	checker.lastRun[lateMonitor.ID] = time.Now().UnixMilli() - 500
	checker.mu.Unlock()

	ctx := context.Background()
	round1Done := make(chan struct{})
	go func() {
		defer close(round1Done)
		checker.runChecks(ctx)
	}()

	// 确认第一轮已分发出慢检查（在途），再等 1s 让 late 到期。
	deadline := time.Now().Add(5 * time.Second)
	for {
		checker.mu.RLock()
		_, inFlight := checker.inFlight[slowMonitor.ID]
		checker.mu.RUnlock()
		if inFlight {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the first round to dispatch the slow check")
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(time.Second)

	// 第二轮：旧代码照常分发 late（与慢检查并发，maxConc=2）；
	// 修后 checking 标志仍为 1，第二轮被 CAS 挡回（maxConc 恒为 1）。
	checker.runChecks(ctx)

	// 等第一轮跑完（慢检查 DB 落行），再断言并发拨号峰值。
	deadline = time.Now().Add(10 * time.Second)
	for {
		results, err := r.GetServiceMonitorResults(slowMonitor.ID, 10)
		if err != nil {
			t.Fatalf("get slow results: %v", err)
		}
		if len(results) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the slow check to finish")
		}
		time.Sleep(50 * time.Millisecond)
	}
	<-round1Done

	if got := maxConc.Load(); got > 1 {
		t.Fatalf("R18-B11-03: overlapping rounds must not dispatch while the previous round's workers still run: peak concurrent dials=%d, want <=1", got)
	}
}
