package repo_test

import (
	"path/filepath"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// B12-001（Round 2）：GetTunnelQualityHistory 此前是同族区间接口里
// 唯一没有行数上限的——种入 20000 行全量返回。同族口径
// （GetServiceMonitorResultsByTimeRange / GetTunnelMetrics）为
// Limit(5000) 且保留最新：降序取 5000 再反转为升序返回。本测试锁定：
// 超量时恰回 5000 行、保留的是最新 5000 行、返回仍按时间升序。

func openTunnelQualityRepo(t *testing.T) *repo.Repository {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "tunnel-quality-limit.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func TestTunnelQualityHistoryCappedAt5000KeepingNewest(t *testing.T) {
	r := openTunnelQualityRepo(t)
	base := time.Now().UnixMilli() - 10000000
	const total = 5200
	const tunnelID = 9

	for i := 0; i < total; i++ {
		row := &model.TunnelQuality{TunnelID: tunnelID, Success: 1, Timestamp: base + int64(i)}
		if err := r.InsertTunnelQuality(row); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	// 另一隧道的行不得混入。
	if err := r.InsertTunnelQuality(&model.TunnelQuality{TunnelID: 10, Success: 1, Timestamp: base + total + 100}); err != nil {
		t.Fatalf("insert other tunnel: %v", err)
	}

	rows, err := r.GetTunnelQualityHistory(tunnelID, 0, base+total+1000)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(rows) != 5000 {
		t.Fatalf("B12-001: history rows = %d, want capped 5000", len(rows))
	}
	// 保留最新：最早一行应为第 total-5000 条，最晚为最后一条。
	if rows[0].Timestamp != base+int64(total-5000) {
		t.Fatalf("B12-001: oldest kept timestamp = %d, want %d (newest 5000 kept)", rows[0].Timestamp, base+int64(total-5000))
	}
	if rows[len(rows)-1].Timestamp != base+int64(total-1) {
		t.Fatalf("B12-001: newest kept timestamp = %d, want %d", rows[len(rows)-1].Timestamp, base+int64(total-1))
	}
	// 返回顺序仍为时间升序（前端图表按此消费）。
	for i := 1; i < len(rows); i++ {
		if rows[i].Timestamp < rows[i-1].Timestamp {
			t.Fatalf("history not ascending at %d: %d < %d", i, rows[i].Timestamp, rows[i-1].Timestamp)
		}
	}
}

func TestTunnelQualityHistoryUnderCapReturnsAllAscending(t *testing.T) {
	r := openTunnelQualityRepo(t)
	base := time.Now().UnixMilli() - 100000
	for i := 0; i < 3; i++ {
		row := &model.TunnelQuality{TunnelID: 11, Success: 1, Timestamp: base + int64(i)}
		if err := r.InsertTunnelQuality(row); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	rows, err := r.GetTunnelQualityHistory(11, 0, base+100)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("history rows = %d, want 3", len(rows))
	}
	for i := 1; i < len(rows); i++ {
		if rows[i].Timestamp < rows[i-1].Timestamp {
			t.Fatalf("history not ascending at %d", i)
		}
	}
}
