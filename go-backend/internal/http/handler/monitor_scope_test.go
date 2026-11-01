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

// 监控权限范围收窄回归（2026-11-01 用户拍板）：监控权限的语义是
// 「只能看自己被分配隧道的监控」——
//   - 隧道三面（列表 / 质量总览 / 按 ID 指标与质量）对非管理员按
//     ListUserAccessibleTunnels 口径过滤，他人隧道按 ID 访问回 403「无权限」；
//   - 服务监控与节点面（不挂任何用户隧道）改为仅管理员，非管理员
//     回管理员专属门的 403，不得以空数据冒充成功；
//   - 管理员全集不变；无监控权限用户的探针语义（need_admin_grant）不变。
func TestMonitorScopeToUserTunnels(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now().UnixMilli()

	adminID, err := r.CreateUser("scope-admin", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now)
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	viewerID, err := r.CreateUser("scope-viewer", "hash", 1, now+86400000, 1, 1, 100, 1, 0, now)
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	strangerID, err := r.CreateUser("scope-stranger", "hash", 1, now+86400000, 1, 1, 100, 1, 0, now)
	if err != nil {
		t.Fatalf("create stranger: %v", err)
	}
	if err := r.InsertMonitorPermission(viewerID, now); err != nil {
		t.Fatalf("grant monitor permission: %v", err)
	}

	mkTunnel := func(name string, inx int) int64 {
		tn := &model.Tunnel{
			Name: name, Inx: inx, TrafficRatio: 1, Type: 1, Protocol: "tls",
			Flow: 1, CreatedTime: now, UpdatedTime: now, Status: 1,
		}
		if err := r.DB().Create(tn).Error; err != nil {
			t.Fatalf("create tunnel %s: %v", name, err)
		}
		return tn.ID
	}
	ownTunnel := mkTunnel("scope-own", 1)
	otherTunnel := mkTunnel("scope-other", 2)

	// viewer 只被分配 ownTunnel。
	if err := r.DB().Create(&model.UserTunnel{
		UserID: viewerID, TunnelID: ownTunnel, Num: 99999, Flow: 0,
		ExpTime: 2727251700000, Status: 1,
	}).Error; err != nil {
		t.Fatalf("grant tunnel: %v", err)
	}
	// 两条隧道都各有一条最新质量记录。
	for _, tid := range []int64{ownTunnel, otherTunnel} {
		if err := r.InsertTunnelQuality(&model.TunnelQuality{
			TunnelID: tid, Timestamp: now, Success: 1, EntryToExitLatency: 12,
		}); err != nil {
			t.Fatalf("insert quality for tunnel %d: %v", tid, err)
		}
	}
	mon := &model.ServiceMonitor{
		Name: "scope-mon", Type: "tcp", Target: "8.8.8.8:53",
		IntervalSec: 60, TimeoutSec: 5, NodeID: 0, Enabled: 1,
		CreatedTime: now, UpdatedTime: now,
	}
	if err := r.CreateServiceMonitor(mon); err != nil {
		t.Fatalf("create service monitor: %v", err)
	}

	call := func(fn func(http.ResponseWriter, *http.Request), path string, userID int64, roleID int) envelope {
		w := httptest.NewRecorder()
		fn(w, claimsCtxReq(http.MethodGet, path, "", "", userID, roleID))
		return decodeEnvelope(t, w)
	}
	decodeIDs := func(env envelope, key string) []int64 {
		t.Helper()
		var rows []map[string]interface{}
		if err := json.Unmarshal(env.Data, &rows); err != nil {
			t.Fatalf("decode rows: %v (data=%s)", err, env.Data)
		}
		ids := make([]int64, 0, len(rows))
		for _, row := range rows {
			if v, ok := row[key].(float64); ok {
				ids = append(ids, int64(v))
			}
		}
		return ids
	}

	// ---- 隧道列表：viewer 只见自己隧道，管理员见全量 ----
	env := call(h.monitorTunnelListHandler, "/api/v1/monitor/tunnels", viewerID, 1)
	if env.Code != 0 {
		t.Fatalf("viewer tunnel list code=%d msg=%q", env.Code, env.Msg)
	}
	if ids := decodeIDs(env, "id"); len(ids) != 1 || ids[0] != ownTunnel {
		t.Fatalf("viewer tunnel list must contain only own tunnel %d, got %v", ownTunnel, ids)
	}
	env = call(h.monitorTunnelListHandler, "/api/v1/monitor/tunnels", adminID, 0)
	if env.Code != 0 {
		t.Fatalf("admin tunnel list code=%d msg=%q", env.Code, env.Msg)
	}
	if ids := decodeIDs(env, "id"); len(ids) != 2 {
		t.Fatalf("admin tunnel list must contain both tunnels, got %v", ids)
	}

	// ---- 隧道质量总览：viewer 只见自己隧道 ----
	env = call(h.monitorTunnelQualityHandler, "/api/v1/monitor/tunnels/quality", viewerID, 1)
	if env.Code != 0 {
		t.Fatalf("viewer tunnel quality code=%d msg=%q", env.Code, env.Msg)
	}
	if ids := decodeIDs(env, "tunnelId"); len(ids) != 1 || ids[0] != ownTunnel {
		t.Fatalf("viewer tunnel quality must contain only own tunnel %d, got %v", ownTunnel, ids)
	}
	env = call(h.monitorTunnelQualityHandler, "/api/v1/monitor/tunnels/quality", adminID, 0)
	if env.Code != 0 {
		t.Fatalf("admin tunnel quality code=%d msg=%q", env.Code, env.Msg)
	}
	if ids := decodeIDs(env, "tunnelId"); len(ids) != 2 {
		t.Fatalf("admin tunnel quality must contain both tunnels, got %v", ids)
	}

	// ---- 按 ID 访问：他人隧道 403「无权限」，自己隧道与管理员放行 ----
	ownMetrics := fmt.Sprintf("/api/v1/monitor/tunnels/%d/metrics", ownTunnel)
	otherMetrics := fmt.Sprintf("/api/v1/monitor/tunnels/%d/metrics", otherTunnel)
	ownQuality := fmt.Sprintf("/api/v1/monitor/tunnels/%d/quality", ownTunnel)
	otherQuality := fmt.Sprintf("/api/v1/monitor/tunnels/%d/quality", otherTunnel)
	if env := call(h.monitorTunnelMetrics, otherMetrics, viewerID, 1); env.Code != 403 || env.Msg != "无权限" {
		t.Fatalf("viewer foreign tunnel metrics must be 403 无权限, got code=%d msg=%q", env.Code, env.Msg)
	}
	if env := call(h.monitorTunnelMetrics, otherQuality, viewerID, 1); env.Code != 403 || env.Msg != "无权限" {
		t.Fatalf("viewer foreign tunnel quality must be 403 无权限, got code=%d msg=%q", env.Code, env.Msg)
	}
	if env := call(h.monitorTunnelMetrics, ownMetrics, viewerID, 1); env.Code != 0 {
		t.Fatalf("viewer own tunnel metrics code=%d msg=%q", env.Code, env.Msg)
	}
	if env := call(h.monitorTunnelMetrics, ownQuality, viewerID, 1); env.Code != 0 {
		t.Fatalf("viewer own tunnel quality code=%d msg=%q", env.Code, env.Msg)
	}
	if env := call(h.monitorTunnelMetrics, otherMetrics, adminID, 0); env.Code != 0 {
		t.Fatalf("admin foreign tunnel metrics code=%d msg=%q", env.Code, env.Msg)
	}

	// ---- 服务监控与节点面：非管理员一律管理员门 403 ----
	adminOnly := []struct {
		name string
		fn   func(http.ResponseWriter, *http.Request)
		path string
	}{
		{"service list", h.monitorServiceListHandler, "/api/v1/monitor/services"},
		{"service latest-results", h.monitorServiceLatestResultsHandler, "/api/v1/monitor/services/latest-results"},
		{"service results", h.monitorServiceResultsHandler, fmt.Sprintf("/api/v1/monitor/services/%d/results", mon.ID)},
		{"service limits", h.monitorServiceLimitsHandler, "/api/v1/monitor/services/limits"},
		{"node list", h.monitorNodeListHandler, "/api/v1/monitor/nodes"},
		{"node metrics", h.monitorNodeMetricsHandler, "/api/v1/monitor/nodes/1/metrics"},
		{"node metrics latest", h.monitorNodeMetricsHandler, "/api/v1/monitor/nodes/1/metrics/latest"},
	}
	for _, tc := range adminOnly {
		if env := call(tc.fn, tc.path, viewerID, 1); env.Code != 403 || env.Msg != "权限不足，仅管理员可操作" {
			t.Fatalf("viewer %s must be admin-gate 403, got code=%d msg=%q", tc.name, env.Code, env.Msg)
		}
		if env := call(tc.fn, tc.path, adminID, 0); env.Code != 0 {
			t.Fatalf("admin %s code=%d msg=%q", tc.name, env.Code, env.Msg)
		}
	}

	// ---- 探针语义不变：无权限用户仍 need_admin_grant ----
	type accessData struct {
		Allowed bool   `json:"allowed"`
		Admin   bool   `json:"admin"`
		Reason  string `json:"reason"`
	}
	probe := func(userID int64, roleID int) accessData {
		env := call(h.monitorAccessHandler, "/api/v1/monitor/access", userID, roleID)
		if env.Code != 0 {
			t.Fatalf("access probe code=%d msg=%q", env.Code, env.Msg)
		}
		var data accessData
		if err := json.Unmarshal(env.Data, &data); err != nil {
			t.Fatalf("decode access data: %v", err)
		}
		return data
	}
	if d := probe(strangerID, 1); d.Allowed || d.Admin || d.Reason != "need_admin_grant" {
		t.Fatalf("stranger access = %+v, want allowed=false admin=false reason=need_admin_grant", d)
	}
	if d := probe(viewerID, 1); !d.Allowed || d.Admin {
		t.Fatalf("viewer access = %+v, want allowed=true admin=false", d)
	}
	// 无权限用户调隧道列表仍走监控门 403（与收窄前一致）。
	if env := call(h.monitorTunnelListHandler, "/api/v1/monitor/tunnels", strangerID, 1); env.Code != 403 {
		t.Fatalf("stranger tunnel list must be 403, got code=%d msg=%q", env.Code, env.Msg)
	}
}
