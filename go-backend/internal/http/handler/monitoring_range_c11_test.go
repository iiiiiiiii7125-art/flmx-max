package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// C11（B12-002 / B12-003）回归：监控四面（节点指标 / 隧道指标 / 隧道质量 /
// 服务监控结果）的时间范围参数统一由 parseMetricsRange 解析：
//   - 单界必须生效，不得整段忽略后回落全量或 limit 查询（B12-002）；
//   - 不可解析的 start/end 四面一致返回 -1「无效的时间范围」，
//     不得静默回退默认 1h 窗口（B12-003）。

func setupC11(t *testing.T) (*Handler, *repo.Repository, int64) {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now().UnixMilli()
	adminID, err := r.CreateUser("c11-admin", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now)
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	return h, r, adminID
}

func c11Get(t *testing.T, h *Handler, adminID int64, fn func(http.ResponseWriter, *http.Request), path string) envelope {
	t.Helper()
	w := httptest.NewRecorder()
	fn(w, claimsCtxReq(http.MethodGet, path, "", "", adminID, 0))
	return decodeEnvelope(t, w)
}

func c11Rows(t *testing.T, env envelope) int {
	t.Helper()
	if env.Code != 0 {
		t.Fatalf("expected code 0, got %d msg=%q", env.Code, env.Msg)
	}
	if string(env.Data) == "null" {
		return 0
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(env.Data, &rows); err != nil {
		t.Fatalf("decode rows: %v (data=%s)", err, env.Data)
	}
	return len(rows)
}

// B12-002：services results 只传一界时该界必须生效。
// 修复前：单界被整段忽略、落回 limit 查询返回全部行。
func TestMonitorServiceResultsSingleBoundC11(t *testing.T) {
	h, r, adminID := setupC11(t)
	now := time.Now().UnixMilli()

	mon := &model.ServiceMonitor{
		Name: "c11-mon", Type: "tcp", Target: "8.8.8.8:53",
		IntervalSec: 60, TimeoutSec: 5, Enabled: 1,
		CreatedTime: now, UpdatedTime: now,
	}
	if err := r.CreateServiceMonitor(mon); err != nil {
		t.Fatalf("create monitor: %v", err)
	}
	if mon.ID == 0 {
		t.Fatalf("monitor ID not assigned")
	}
	for _, ts := range []int64{now - 1000, now - 2*60*60*1000} {
		if err := r.InsertServiceMonitorResult(&model.ServiceMonitorResult{
			MonitorID: mon.ID, Timestamp: ts, Success: 1, LatencyMs: 12.5,
		}); err != nil {
			t.Fatalf("insert result: %v", err)
		}
	}
	base := fmt.Sprintf("/api/v1/monitor/services/%d/results", mon.ID)

	// 无参数：保持 limit 回退（向后兼容），返回全部 2 行。
	if n := c11Rows(t, c11Get(t, h, adminID, h.monitorServiceResultsHandler, base)); n != 2 {
		t.Fatalf("no params must keep limit fallback with 2 rows, got %d", n)
	}

	// 只传 start（30 分钟前）：该界生效，只剩最近 1 行（修复前为 2 行）。
	p := fmt.Sprintf("%s?start=%d", base, now-30*60*1000)
	if n := c11Rows(t, c11Get(t, h, adminID, h.monitorServiceResultsHandler, p)); n != 1 {
		t.Fatalf("single start bound must filter to 1 row, got %d", n)
	}

	// 只传 end（30 分钟前）：该界生效，最近一行被界排除、窗口内 0 行（修复前为 2 行）。
	// （不用恰好在默认窗口边缘的 end 值：缺省 start=服务端 now-1h 随时间移动，边缘值会竞态。）
	p = fmt.Sprintf("%s?end=%d", base, now-30*60*1000)
	if n := c11Rows(t, c11Get(t, h, adminID, h.monitorServiceResultsHandler, p)); n != 0 {
		t.Fatalf("single end bound must filter to 0 rows, got %d", n)
	}

	// 只传 end=now：窗口 [now-1h, now] 内只剩最近 1 行。
	p = fmt.Sprintf("%s?end=%d", base, now)
	if n := c11Rows(t, c11Get(t, h, adminID, h.monitorServiceResultsHandler, p)); n != 1 {
		t.Fatalf("single end=now must return 1 row, got %d", n)
	}

	// 只传未来 start：与默认 end=now 构成倒置范围，必须报错而非返回全部。
	env := c11Get(t, h, adminID, h.monitorServiceResultsHandler, base+"?start=9999999999999")
	if env.Code != -1 || env.Msg != "无效的时间范围" {
		t.Fatalf("future single start must be -1 无效的时间范围, got code=%d msg=%q", env.Code, env.Msg)
	}

	// 只传过去 end=1：与默认 start 构成倒置范围，必须报错而非返回全部。
	env = c11Get(t, h, adminID, h.monitorServiceResultsHandler, base+"?end=1")
	if env.Code != -1 || env.Msg != "无效的时间范围" {
		t.Fatalf("past single end must be -1 无效的时间范围, got code=%d msg=%q", env.Code, env.Msg)
	}

	// 成对未来范围：过滤本身工作，返回 0 行（既有行为保持）。
	p = fmt.Sprintf("%s?start=%d&end=%d", base, now+60000, now+120000)
	if n := c11Rows(t, c11Get(t, h, adminID, h.monitorServiceResultsHandler, p)); n != 0 {
		t.Fatalf("paired future range must return 0 rows, got %d", n)
	}
}

// B12-003：不可解析的 start/end 在四个面一致报错。
// 修复前：节点/隧道三面 code=0 静默用默认窗口，仅 services results 报错。
func TestMonitorBadRangeConsistentC11(t *testing.T) {
	h, _, adminID := setupC11(t)

	cases := []struct {
		name string
		fn   func(http.ResponseWriter, *http.Request)
		path string
	}{
		{"node metrics paired bad", h.monitorNodeMetricsHandler, "/api/v1/monitor/nodes/1/metrics?start=abc&end=xyz"},
		{"node metrics single bad start", h.monitorNodeMetricsHandler, "/api/v1/monitor/nodes/1/metrics?start=abc"},
		{"tunnel metrics single bad start", h.monitorTunnelMetrics, "/api/v1/monitor/tunnels/1/metrics?start=abc"},
		{"tunnel metrics single bad end", h.monitorTunnelMetrics, "/api/v1/monitor/tunnels/1/metrics?end=xyz"},
		{"tunnel quality single bad end", h.monitorTunnelMetrics, "/api/v1/monitor/tunnels/1/quality?end=xyz"},
		{"tunnel quality paired bad", h.monitorTunnelMetrics, "/api/v1/monitor/tunnels/1/quality?start=abc&end=xyz"},
		{"service results single bad start", h.monitorServiceResultsHandler, "/api/v1/monitor/services/1/results?start=abc"},
		{"service results single bad end", h.monitorServiceResultsHandler, "/api/v1/monitor/services/1/results?end=xyz"},
		{"service results paired bad", h.monitorServiceResultsHandler, "/api/v1/monitor/services/1/results?start=abc&end=xyz"},
	}
	for _, tc := range cases {
		env := c11Get(t, h, adminID, tc.fn, tc.path)
		if env.Code != -1 || env.Msg != "无效的时间范围" {
			t.Fatalf("%s: must be -1 无效的时间范围, got code=%d msg=%q", tc.name, env.Code, env.Msg)
		}
	}
}

// 单界/双界在数据面确实覆盖默认窗口（节点指标与隧道质量各验一面，
// 与 services results 的 TestMonitorServiceResultsSingleBoundC11 互补）。
func TestMonitorMetricsBoundsEffectiveC11(t *testing.T) {
	h, r, adminID := setupC11(t)
	now := time.Now().UnixMilli()

	for _, ts := range []int64{now - 1000, now - 2*60*60*1000} {
		if err := r.InsertNodeMetric(&model.NodeMetric{NodeID: 1, Timestamp: ts, CPUUsage: 10}); err != nil {
			t.Fatalf("insert node metric: %v", err)
		}
		if err := r.InsertTunnelQuality(&model.TunnelQuality{TunnelID: 1, Timestamp: ts, Success: 1}); err != nil {
			t.Fatalf("insert tunnel quality: %v", err)
		}
	}

	// 默认窗口（1h）：两个面都只见最近 1 行。
	if n := c11Rows(t, c11Get(t, h, adminID, h.monitorNodeMetricsHandler, "/api/v1/monitor/nodes/1/metrics")); n != 1 {
		t.Fatalf("node metrics default window must return 1 row, got %d", n)
	}
	if n := c11Rows(t, c11Get(t, h, adminID, h.monitorTunnelMetrics, "/api/v1/monitor/tunnels/1/quality")); n != 1 {
		t.Fatalf("tunnel quality default window must return 1 row, got %d", n)
	}

	// 显式 3h 窗口：两个面都见 2 行（提供值覆盖默认窗口）。
	p := fmt.Sprintf("/api/v1/monitor/nodes/1/metrics?start=%d&end=%d", now-3*60*60*1000, now)
	if n := c11Rows(t, c11Get(t, h, adminID, h.monitorNodeMetricsHandler, p)); n != 2 {
		t.Fatalf("node metrics explicit 3h window must return 2 rows, got %d", n)
	}
	p = fmt.Sprintf("/api/v1/monitor/tunnels/1/quality?start=%d&end=%d", now-3*60*60*1000, now)
	if n := c11Rows(t, c11Get(t, h, adminID, h.monitorTunnelMetrics, p)); n != 2 {
		t.Fatalf("tunnel quality explicit 3h window must return 2 rows, got %d", n)
	}

	// 只传 start（30 分钟前）：两个面都只剩最近 1 行。
	p = fmt.Sprintf("/api/v1/monitor/nodes/1/metrics?start=%d", now-30*60*1000)
	if n := c11Rows(t, c11Get(t, h, adminID, h.monitorNodeMetricsHandler, p)); n != 1 {
		t.Fatalf("node metrics single start must return 1 row, got %d", n)
	}
	p = fmt.Sprintf("/api/v1/monitor/tunnels/1/quality?start=%d", now-30*60*1000)
	if n := c11Rows(t, c11Get(t, h, adminID, h.monitorTunnelMetrics, p)); n != 1 {
		t.Fatalf("tunnel quality single start must return 1 row, got %d", n)
	}

	// 超 24h 窗口四面统一「时间范围过大」（口径保持）。
	p = fmt.Sprintf("/api/v1/monitor/nodes/1/metrics?start=%d&end=%d", now-48*60*60*1000, now)
	env := c11Get(t, h, adminID, h.monitorNodeMetricsHandler, p)
	if env.Code != -1 || env.Msg != "时间范围过大" {
		t.Fatalf("oversized range must be -1 时间范围过大, got code=%d msg=%q", env.Code, env.Msg)
	}
}
