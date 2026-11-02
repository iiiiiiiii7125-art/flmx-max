package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/http/response"
	"go-backend/internal/security"
	"go-backend/internal/store/repo"
)

// R2-021（B-sec-01）钉死用例：未鉴权可达面在仓储/内部错误时只回
// 通用文案（code -2 + "Internal error"），原始错误只进服务端日志
// ——与联邦面 O12-001 同口径。故障注入用 DROP TABLE 把目标查询
// 打成 DB 错误（原文含 "no such table"），断言响应不含原文。
// 修复前各用例的 msg 即为 DB 错误原文 → 红。

func newPanelsecHandler(t *testing.T) (*Handler, *repo.Repository) {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return New(r, "test-secret"), r
}

func dropTableForTest(t *testing.T, r *repo.Repository, table string) {
	t.Helper()
	if err := r.DB().Exec("DROP TABLE " + table).Error; err != nil {
		t.Fatalf("drop table %s: %v", table, err)
	}
}

func assertGenericInternalError(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	body := rec.Body.String()
	var out response.R
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v body=%s", err, body)
	}
	if out.Code != -2 || out.Msg != "Internal error" {
		t.Fatalf("expected generic internal error (-2, \"Internal error\"), got (%d, %q)", out.Code, out.Msg)
	}
	if strings.Contains(body, "no such table") || strings.Contains(body, "sql:") {
		t.Fatalf("response leaks raw storage error: %s", body)
	}
}

func postJSON(t *testing.T, h *Handler, fn func(http.ResponseWriter, *http.Request), path, payload string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(payload))
	rec := httptest.NewRecorder()
	fn(rec, req)
	return rec
}

func TestLoginCaptchaConfigErrorIsGeneric(t *testing.T) {
	h, r := newPanelsecHandler(t)
	dropTableForTest(t, r, "vite_config")
	rec := postJSON(t, h, h.login, "/api/v1/user/login", `{"username":"someone","password":"whatever"}`)
	assertGenericInternalError(t, rec)
}

func TestLoginUserLookupErrorIsGeneric(t *testing.T) {
	h, r := newPanelsecHandler(t)
	dropTableForTest(t, r, "user")
	rec := postJSON(t, h, h.login, "/api/v1/user/login", `{"username":"someone","password":"whatever"}`)
	assertGenericInternalError(t, rec)
}

func TestCheckCaptchaErrorIsGeneric(t *testing.T) {
	h, r := newPanelsecHandler(t)
	dropTableForTest(t, r, "vite_config")
	rec := postJSON(t, h, h.checkCaptcha, "/api/v1/captcha/check", `{}`)
	assertGenericInternalError(t, rec)
}

func TestCaptchaVerifyErrorIsGeneric(t *testing.T) {
	h, r := newPanelsecHandler(t)
	dropTableForTest(t, r, "vite_config")
	rec := postJSON(t, h, h.captchaVerify, "/api/v1/captcha/verify", `{"id":"x","data":"y"}`)
	assertGenericInternalError(t, rec)
}

func TestPublicConfigGetErrorIsGeneric(t *testing.T) {
	h, r := newPanelsecHandler(t)
	dropTableForTest(t, r, "vite_config")
	rec := postJSON(t, h, h.getPublicConfigByName, "/api/v1/public/config/get", `{"name":"app_name"}`)
	assertGenericInternalError(t, rec)
}

func TestConfigGetAnonymousPublicKeyErrorIsGeneric(t *testing.T) {
	h, r := newPanelsecHandler(t)
	dropTableForTest(t, r, "vite_config")
	rec := postJSON(t, h, h.getConfigByName, "/api/v1/config/get", `{"name":"app_name"}`)
	assertGenericInternalError(t, rec)
}

func TestSubStoreUserLookupErrorIsGeneric(t *testing.T) {
	resetSubStoreLimiterForTest()
	h, r := newPanelsecHandler(t)
	dropTableForTest(t, r, "user")
	rec := postJSON(t, h, h.openAPISubStore, "/api/v1/open_api/sub_store", `{"user":"abc","password":"pw","tunnel":"-1"}`)
	assertGenericInternalError(t, rec)
}

func TestSubStoreTunnelLookupErrorIsGeneric(t *testing.T) {
	resetSubStoreLimiterForTest()
	h, r := newPanelsecHandler(t)
	admin, err := r.GetUserByUsername("admin@user")
	if err != nil || admin == nil {
		t.Fatalf("get seed admin: %v", err)
	}
	newHash, err := security.HashPassword("changed-strong-password")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if err := r.UpdateUserPassword(admin.ID, newHash, time.Now().UnixMilli()); err != nil {
		t.Fatalf("update password: %v", err)
	}
	dropTableForTest(t, r, "user_tunnel")
	rec := postJSON(t, h, h.openAPISubStore, "/api/v1/open_api/sub_store", `{"user":"admin@user","password":"changed-strong-password","tunnel":"5"}`)
	assertGenericInternalError(t, rec)
}
