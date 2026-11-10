package handler

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// AP-08 回归：StatisticsFlow.CreatedTime 此前 json:"-"，流量图
// 只能按 HH:00 字符串拼日期；现透出 UnixMilli 毫秒时间戳，前端
// 以它定日。
func TestStatisticsFlowCreatedTimeSerialized(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "jobs-stats-created.db")
	r, err := repo.Open(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	h := New(r, "secret")
	now := time.Date(2026, 2, 7, 12, 0, 0, 0, time.UTC)
	if err := r.DB().Exec(`UPDATE user SET in_flow = 100, out_flow = 200 WHERE id = 1`).Error; err != nil {
		t.Fatalf("seed user flow: %v", err)
	}
	h.runStatisticsFlowJob(now)

	flows, err := r.GetStatisticsFlows(1, 24)
	if err != nil {
		t.Fatalf("get statistics flows: %v", err)
	}
	if len(flows) == 0 {
		t.Fatalf("expected at least one statistics row")
	}
	raw, err := json.Marshal(flows[0])
	if err != nil {
		t.Fatalf("marshal statistics flow: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode statistics flow: %v", err)
	}
	created, ok := decoded["createdTime"].(float64)
	if !ok || created == 0 {
		t.Fatalf("createdTime missing from serialized StatisticsFlow: %s", string(raw))
	}
	if int64(created) != now.Truncate(time.Hour).UnixMilli() {
		t.Fatalf("createdTime = %v, want hour-mark millis %d", created, now.Truncate(time.Hour).UnixMilli())
	}
}
