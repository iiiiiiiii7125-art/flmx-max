package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go-backend/internal/auth"
	"go-backend/internal/health"
	"go-backend/internal/http/middleware"
	"go-backend/internal/store/model"
)

// RR4-F5-01 钉死用例：复刻审计探针场景——短间隔监控 A 的最新结果
// 在内存缓存里、长间隔监控 B 的最新结果只在 DB 里（面板重启后 B
// 尚未在本进程跑过）。修复前总览只要缓存非空就整体短路、B 缺席；
// 修复后按 monitorId 合并（缓存优先、同项取更新），且停用项在
// 两侧都不得出现。
func TestRR4F5LatestResultsMergesCacheAndDB(t *testing.T) {
	h, r := newPanelsecHandler(t)
	now := time.Now().UnixMilli()

	seedMonitor := func(name string, intervalSec, enabled int) model.ServiceMonitor {
		t.Helper()
		m := model.ServiceMonitor{
			Name: name, Type: "tcp", Target: "127.0.0.1:80",
			IntervalSec: intervalSec, TimeoutSec: 5, Enabled: enabled,
			CreatedTime: now, UpdatedTime: now,
		}
		if err := r.DB().Create(&m).Error; err != nil {
			t.Fatalf("seed monitor %s: %v", name, err)
		}
		if enabled == 0 {
			// Enabled 带 default:1，零值创建会被默认值覆盖，显式落库。
			if err := r.DB().Model(&model.ServiceMonitor{}).Where("id = ?", m.ID).Update("enabled", 0).Error; err != nil {
				t.Fatalf("disable monitor %s: %v", name, err)
			}
			m.Enabled = 0
		}
		return m
	}
	monA := seedMonitor("rr4-f5-a", 1, 1)
	monB := seedMonitor("rr4-f5-b", 3600, 1)
	monC := seedMonitor("rr4-f5-c-disabled", 60, 0)

	// B 的最新结果只在 DB；A 在 DB 里只有一条更旧的结果（延迟标记
	// 999)，合并后必须以缓存的新结果为准；C 已停用、两侧都不得回。
	if err := r.InsertServiceMonitorResult(&model.ServiceMonitorResult{
		MonitorID: monB.ID, Timestamp: now - 1000, Success: 1, LatencyMs: 222,
	}); err != nil {
		t.Fatalf("insert B result: %v", err)
	}
	if err := r.InsertServiceMonitorResult(&model.ServiceMonitorResult{
		MonitorID: monA.ID, Timestamp: now - 60000, Success: 1, LatencyMs: 999,
	}); err != nil {
		t.Fatalf("insert A stale result: %v", err)
	}
	if err := r.InsertServiceMonitorResult(&model.ServiceMonitorResult{
		MonitorID: monC.ID, Timestamp: now - 500, Success: 1, LatencyMs: 333,
	}); err != nil {
		t.Fatalf("insert C result: %v", err)
	}

	checker := health.NewChecker(r, nil)
	checker.SeedLatestCachedForTesting(&model.ServiceMonitorResult{
		MonitorID: monA.ID, Timestamp: now, Success: 1, LatencyMs: 111,
	})
	checker.SeedLatestCachedForTesting(&model.ServiceMonitorResult{
		MonitorID: monC.ID, Timestamp: now, Success: 1, LatencyMs: 333,
	})
	h.healthCheck = checker

	token, err := auth.GenerateToken(1, "admin", 0, "test-secret")
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	claims, ok := auth.ValidateToken(token, "test-secret")
	if !ok {
		t.Fatalf("validate token failed")
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/monitor/services/latest-results", nil)
	req.Header.Set("Authorization", token)
	req = req.WithContext(context.WithValue(req.Context(), middleware.ClaimsContextKey, claims))
	rec := httptest.NewRecorder()
	h.monitorServiceLatestResultsHandler(rec, req)

	var out struct {
		Code int `json:"code"`
		Data []struct {
			MonitorID int64   `json:"monitorId"`
			LatencyMs float64 `json:"latencyMs"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rec.Body.String())
	}
	if out.Code != 0 {
		t.Fatalf("expected code 0, got %d body=%s", out.Code, rec.Body.String())
	}
	byID := make(map[int64]float64, len(out.Data))
	for _, row := range out.Data {
		byID[row.MonitorID] = row.LatencyMs
	}
	if len(out.Data) != 2 {
		t.Fatalf("expected exactly monitors A+B, got %v body=%s", byID, rec.Body.String())
	}
	if lat, ok := byID[monA.ID]; !ok || lat != 111 {
		t.Fatalf("monitor A must come from cache (latency 111), got %v present=%v", lat, ok)
	}
	if lat, ok := byID[monB.ID]; !ok || lat != 222 {
		t.Fatalf("monitor B must be merged from DB (latency 222), got %v present=%v", lat, ok)
	}
	if _, ok := byID[monC.ID]; ok {
		t.Fatalf("disabled monitor C must not appear, got %v", byID)
	}
}
