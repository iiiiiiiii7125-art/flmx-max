package metrics

import (
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// C3-06: flush 写失败不得丢窗口——指标与流量增量回填，下一轮
// 写成功时恰好落库一次。

func TestC306FlushFailureRequeuesWindow(t *testing.T) {
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()
	svc := NewIngestionService(r)

	base := SystemInfo{Uptime: 10, BytesReceived: 1000, BytesTransmitted: 2000}
	svc.RecordNodeMetric(3, base) // 基线
	grown := base
	grown.BytesReceived += 600
	grown.BytesTransmitted += 900
	svc.RecordNodeMetric(3, grown)

	// 注入写失败：两张目标表都删掉。
	if err := r.DB().Migrator().DropTable(&model.NodeMetric{}, &model.NodeTraffic{}); err != nil {
		t.Fatalf("drop tables: %v", err)
	}
	svc.flushNodeMetrics()
	if len(svc.nodeBuffer) != 2 {
		t.Fatalf("failed flush must requeue metrics, buffered %d", len(svc.nodeBuffer))
	}
	if len(svc.traffic) == 0 {
		t.Fatal("failed flush must requeue traffic deltas")
	}

	// 恢复写面后重 flush：恰好落一次。
	if err := r.DB().Migrator().CreateTable(&model.NodeMetric{}, &model.NodeTraffic{}); err != nil {
		t.Fatalf("recreate tables: %v", err)
	}
	svc.flushNodeMetrics()
	if len(svc.nodeBuffer) != 0 || len(svc.traffic) != 0 {
		t.Fatalf("successful flush must drain the requeue (buffer=%d traffic=%d)", len(svc.nodeBuffer), len(svc.traffic))
	}
	now := time.Now().UnixMilli()
	rows, err := r.GetNodeMetrics(3, now-60000, now+60000)
	if err != nil {
		t.Fatalf("read back metrics: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("requeued metrics must land exactly once, got %d rows", len(rows))
	}
	var trafficRows int64
	if err := r.DB().Table("node_traffic").Where("node_id = ?", 3).Count(&trafficRows).Error; err != nil {
		t.Fatalf("count traffic: %v", err)
	}
	if trafficRows != 1 {
		t.Fatalf("requeued traffic delta must land as one hourly bucket, got %d rows", trafficRows)
	}
}
