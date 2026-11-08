package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"go-backend/internal/store/repo"
)

// DEV-RM-SHARE 回归钉格：跨面板共享/联邦功能整体删除后，其 14 条
// 路由（7 条管理员面 + 7 条对端面）不得在 Register 的全量注册结果
// 中复活——任一路径不再返回 404 即视为功能残留、本格变红。
func TestFederationRoutesRemoved404(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/rmshare-routes.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })

	h := New(r, "test-jwt-secret")
	mux := http.NewServeMux()
	h.Register(mux)

	paths := []string{
		"/api/v1/federation/share/list",
		"/api/v1/federation/share/create",
		"/api/v1/federation/share/update",
		"/api/v1/federation/share/delete",
		"/api/v1/federation/share/reset-flow",
		"/api/v1/federation/share/remote-usage/list",
		"/api/v1/federation/node/import",
		"/api/v1/federation/connect",
		"/api/v1/federation/tunnel/create",
		"/api/v1/federation/runtime/reserve-port",
		"/api/v1/federation/runtime/apply-role",
		"/api/v1/federation/runtime/release-role",
		"/api/v1/federation/runtime/diagnose",
		"/api/v1/federation/runtime/command",
	}
	for _, p := range paths {
		req := httptest.NewRequest(http.MethodPost, p, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("removed federation route %s: got status %d, want 404", p, w.Code)
		}
	}
}
