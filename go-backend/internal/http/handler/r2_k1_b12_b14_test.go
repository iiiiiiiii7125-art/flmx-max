package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// R2 B12-002: 服务监控 update 对非法 type / enabled 域外值与 create 同
// 口径点名报错——此前 type 仅在等于 tcp/icmp 时赋值、enabled 仅在
// {0,1} 时赋值，其余静默吞没且回成功，管理员误以为已改。
// R2 B14-001: config 写入口（update / update-single）键名归一——大写
// 变体不再生成读不到的影子行，写入后按规范键可读回。

func k1AdminFixture(t *testing.T) (*repo.Repository, *Handler, int64) {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now().UnixMilli()
	adminID, err := r.CreateUser("k1-admin", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now)
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	return r, h, adminID
}

func TestR2K1MonitorUpdateRejectsInvalidTypeAndEnabled(t *testing.T) {
	r, h, adminID := k1AdminFixture(t)
	call := func(fn func(http.ResponseWriter, *http.Request), path, body string) envelope {
		w := httptest.NewRecorder()
		fn(w, claimsCtxReq(http.MethodPost, path, "", body, adminID, 0))
		return decodeEnvelope(t, w)
	}

	if env := call(h.monitorServiceCreate, "/api/v1/monitor/services/create",
		`{"name":"k1-mon","type":"tcp","target":"8.8.8.8:53","intervalSec":60,"timeoutSec":5}`); env.Code != 0 {
		t.Fatalf("create monitor: code=%d msg=%q", env.Code, env.Msg)
	}
	monitors, err := r.ListServiceMonitors()
	if err != nil || len(monitors) != 1 {
		t.Fatalf("list monitors: %v / %v", monitors, err)
	}
	id := monitors[0].ID

	if env := call(h.monitorServiceUpdate, "/api/v1/monitor/services/update",
		fmt.Sprintf(`{"id":%d,"type":"http"}`, id)); env.Code == 0 {
		t.Fatal("update with type=http accepted, want rejection")
	}
	if env := call(h.monitorServiceUpdate, "/api/v1/monitor/services/update",
		fmt.Sprintf(`{"id":%d,"enabled":5}`, id)); env.Code == 0 {
		t.Fatal("update with enabled=5 accepted, want rejection")
	}
	got, err := r.GetServiceMonitor(id)
	if err != nil || got == nil {
		t.Fatalf("get monitor: %v / %v", got, err)
	}
	if got.Type != "tcp" || got.Enabled != 1 {
		t.Fatalf("monitor mutated by rejected updates: type=%q enabled=%d, want tcp/1", got.Type, got.Enabled)
	}

	// 对照：合法更新照常（改名 + enabled=0）。
	if env := call(h.monitorServiceUpdate, "/api/v1/monitor/services/update",
		fmt.Sprintf(`{"id":%d,"name":"k1-mon2","enabled":0}`, id)); env.Code != 0 {
		t.Fatalf("valid update rejected: code=%d msg=%q", env.Code, env.Msg)
	}
	got, _ = r.GetServiceMonitor(id)
	if got.Name != "k1-mon2" || got.Enabled != 0 {
		t.Fatalf("valid update not applied: name=%q enabled=%d", got.Name, got.Enabled)
	}
}

func TestR2K1ConfigWritesNormalizeKeys(t *testing.T) {
	r, h, adminID := k1AdminFixture(t)
	call := func(fn func(http.ResponseWriter, *http.Request), path, body string) envelope {
		w := httptest.NewRecorder()
		fn(w, claimsCtxReq(http.MethodPost, path, "", body, adminID, 0))
		return decodeEnvelope(t, w)
	}

	// update-single 大写变体：落库为规范键、规范键可读回、无影子行。
	if env := call(h.updateSingleConfig, "/api/v1/config/update-single",
		`{"name":"NOTIFY_WEBHOOK_URL","value":"https://hook.example/abc"}`); env.Code != 0 {
		t.Fatalf("update-single uppercase key: code=%d msg=%q", env.Code, env.Msg)
	}
	cfg, err := r.GetConfigByName("notify_webhook_url")
	if err != nil || cfg == nil || cfg.Value != "https://hook.example/abc" {
		t.Fatalf("normalized key not readable: %v / %v", cfg, err)
	}
	if shadow, _ := r.GetConfigByName("NOTIFY_WEBHOOK_URL"); shadow != nil {
		t.Fatalf("shadow row persisted for uppercase key: %+v", shadow)
	}

	// update（批量）同口径。
	if env := call(h.updateConfigs, "/api/v1/config/update",
		`{"APP_NAME":"Panel X"}`); env.Code != 0 {
		t.Fatalf("update batch uppercase key: code=%d msg=%q", env.Code, env.Msg)
	}
	cfg, err = r.GetConfigByName("app_name")
	if err != nil || cfg == nil || cfg.Value != "Panel X" {
		t.Fatalf("batch normalized key not readable: %v / %v", cfg, err)
	}
	if shadow, _ := r.GetConfigByName("APP_NAME"); shadow != nil {
		t.Fatalf("shadow row persisted for batch uppercase key: %+v", shadow)
	}
}
