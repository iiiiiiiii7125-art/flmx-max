package metrics

import (
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R18-C4-02 回归：WS 指标摄入无逐节点速率限制，被攻陷节点可以
// 线速洪泛 metric 帧打满 node_metric 表并争抢 SQLite 单连接。
// 修复：RecordNodeMetric 入口加逐节点最小接受间隔（1 秒；正常
// agent 每 5 秒一帧，无损）+ nodeBuffer 长度帽（5000，超限丢最旧）。
func TestRecordNodeMetricPerNodeRateLimit(t *testing.T) {
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()
	svc := NewIngestionService(r)
	info := SystemInfo{CPUUsage: 50.0, MemoryUsage: 60.0, DiskUsage: 30.0}

	// 同一节点 1 秒内连报 100 帧：只接受首帧（或跨秒边界时 2 帧），
	// 其余全部丢弃计数，不落库、不推进 NIC 基线。
	for i := 0; i < 100; i++ {
		svc.RecordNodeMetric(1, info)
	}
	// 异节点不受影响：同一时刻的首帧必须被接受。
	svc.RecordNodeMetric(2, info)
	svc.flushNodeMetrics()

	metrics, err := r.GetNodeMetrics(1, time.Now().UnixMilli()-60000, time.Now().UnixMilli()+1000)
	if err != nil {
		t.Fatalf("get metrics node 1: %v", err)
	}
	if len(metrics) > 2 {
		t.Fatalf("node 1 rows = %d, want <= 2 (one frame per second max)", len(metrics))
	}
	svc.nodeBufferMu.Lock()
	dropped := svc.metricDropped[1]
	svc.nodeBufferMu.Unlock()
	if len(metrics)+int(dropped) != 100 {
		t.Fatalf("node 1 rows + dropped = %d + %d, want 100", len(metrics), dropped)
	}
	if dropped < 98 {
		t.Fatalf("node 1 dropped = %d, want >= 98 of the 100-frame flood", dropped)
	}
	other, err := r.GetNodeMetrics(2, time.Now().UnixMilli()-60000, time.Now().UnixMilli()+1000)
	if err != nil {
		t.Fatalf("get metrics node 2: %v", err)
	}
	if len(other) != 1 {
		t.Fatalf("node 2 rows = %d, want 1 (per-node limit must not cross nodes)", len(other))
	}
}

// 缓冲帽的确定性验证：自动 flush 是异步的（len>=200 即 go
// flush），经 RecordNodeMetric 端到端计数会被并发 flush 冲掉，
// 故直接对 trimNodeBufferLocked 断言不变式——缓冲永不超帽，
// 超限丢最旧。
func TestNodeBufferCapDropsOldest(t *testing.T) {
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()
	svc := NewIngestionService(r)
	svc.nodeBufferMu.Lock()
	svc.nodeBuffer = make([]*model.NodeMetric, 0, maxNodeBufferMetrics+10)
	for i := 0; i < maxNodeBufferMetrics+10; i++ {
		svc.nodeBuffer = append(svc.nodeBuffer, &model.NodeMetric{NodeID: 99, Timestamp: int64(i)})
	}
	dropped := svc.trimNodeBufferLocked()
	n := len(svc.nodeBuffer)
	first := svc.nodeBuffer[0].Timestamp
	svc.nodeBufferMu.Unlock()
	if dropped != 10 {
		t.Fatalf("trimmed = %d, want 10", dropped)
	}
	if n != maxNodeBufferMetrics {
		t.Fatalf("buffer len = %d, want cap %d", n, maxNodeBufferMetrics)
	}
	if first != 10 {
		t.Fatalf("oldest remaining timestamp = %d, want 10 (oldest 10 must be dropped)", first)
	}
}
