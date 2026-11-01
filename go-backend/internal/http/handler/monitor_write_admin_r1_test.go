package handler

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// X-P1-3（R1 [I]-01 / V-01 / BC-03）：监控「查看权限」回归只读。
// services 的 create/update/delete/run 四个写入口必须仅管理员可用
// （403）；管理员写入口照常成功（防过修）。
// 注：2026-11-01 监控权限范围收窄后，服务/节点读面亦仅管理员——
// 本文中原「viewer 读 list/nodes 成功」断言已按新口径改为 403，
// 隧道面的按用户过滤见 monitor_scope_test.go。
func TestR1MonitorWriteRequiresAdmin(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now().UnixMilli()

	adminID, err := r.CreateUser("r1-admin", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now)
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	viewerID, err := r.CreateUser("r1-viewer", "hash", 1, now+86400000, 1, 1, 100, 1, 0, now)
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	if err := r.InsertMonitorPermission(viewerID, now); err != nil {
		t.Fatalf("grant monitor permission: %v", err)
	}

	call := func(fn func(http.ResponseWriter, *http.Request), method, path, body string, userID int64, roleID int) envelope {
		w := httptest.NewRecorder()
		fn(w, claimsCtxReq(method, path, "", body, userID, roleID))
		return decodeEnvelope(t, w)
	}

	createBody := `{"name":"r1-mon","type":"tcp","target":"8.8.8.8:53","intervalSec":60,"timeoutSec":5}`

	// 被授权普通用户：写入口全部 403（修复前 create 返回 code=0）。
	if env := call(h.monitorServiceCreate, http.MethodPost, "/api/v1/monitor/services/create", createBody, viewerID, 1); env.Code != 403 {
		t.Fatalf("viewer create must be 403, got code=%d msg=%q", env.Code, env.Msg)
	}
	if env := call(h.monitorServiceUpdate, http.MethodPost, "/api/v1/monitor/services/update", `{"id":1,"name":"x"}`, viewerID, 1); env.Code != 403 {
		t.Fatalf("viewer update must be 403, got code=%d msg=%q", env.Code, env.Msg)
	}
	if env := call(h.monitorServiceDelete, http.MethodPost, "/api/v1/monitor/services/delete", `{"id":1}`, viewerID, 1); env.Code != 403 {
		t.Fatalf("viewer delete must be 403, got code=%d msg=%q", env.Code, env.Msg)
	}
	if env := call(h.monitorServiceRun, http.MethodPost, "/api/v1/monitor/services/run", `{"id":1}`, viewerID, 1); env.Code != 403 {
		t.Fatalf("viewer run must be 403, got code=%d msg=%q", env.Code, env.Msg)
	}
	// 且不得有任何监控被 viewer 建出。
	if monitors, err := r.ListServiceMonitors(); err != nil || len(monitors) != 0 {
		t.Fatalf("viewer must not create monitors, got %v err=%v", monitors, err)
	}

	// 范围收窄后同一 token 读服务/节点面同样 403（仅管理员）：
	// 监控权限只覆盖本人隧道的监控数据，不再含服务与节点读面。
	if env := call(h.monitorServiceListHandler, http.MethodGet, "/api/v1/monitor/services", "", viewerID, 1); env.Code != 403 {
		t.Fatalf("viewer read list must be 403 after scope narrowing, got code=%d msg=%q", env.Code, env.Msg)
	}
	if env := call(h.monitorNodeListHandler, http.MethodGet, "/api/v1/monitor/nodes", "", viewerID, 1); env.Code != 403 {
		t.Fatalf("viewer read nodes must be 403 after scope narrowing, got code=%d msg=%q", env.Code, env.Msg)
	}

	// 管理员写仍成功。
	if env := call(h.monitorServiceCreate, http.MethodPost, "/api/v1/monitor/services/create", createBody, adminID, 0); env.Code != 0 {
		t.Fatalf("admin create must succeed, got code=%d msg=%q", env.Code, env.Msg)
	}
	monitors, err := r.ListServiceMonitors()
	if err != nil || len(monitors) != 1 {
		t.Fatalf("admin create must persist one monitor, got %v err=%v", monitors, err)
	}
}
