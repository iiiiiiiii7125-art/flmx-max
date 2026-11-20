package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"go-backend/internal/auth"
	"go-backend/internal/http/handler"
	"go-backend/internal/store/repo"
)

// R18-B10-01：未注册的 /api/v1/* 路径必须回 {code,msg,data,ts} 信封
// 404（HTTP 404 + JSON 体），而不是 Go 默认的裸文本 "404 page not
// found"。非 /api/ 路径保持标准裸文本 404；未登录调用方仍先被
// JWT 中间件拦为信封 401。
func TestR18B1001UnregisteredAPIPathReturnsEnvelope404(t *testing.T) {
	const secret = "router-b1001-secret"
	r, err := repo.Open(filepath.Join(t.TempDir(), "router-b1001.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer func() { _ = r.Close() }()

	h := handler.New(r, secret)
	ts := httptest.NewServer(NewRouter(h, secret))
	defer ts.Close()

	adminToken, err := auth.GenerateToken(1, "admin", 0, secret)
	if err != nil {
		t.Fatalf("generate admin token: %v", err)
	}

	doReq := func(method, path, token string) (int, string, http.Header) {
		t.Helper()
		req, err := http.NewRequest(method, ts.URL+path, nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		if token != "" {
			req.Header.Set("Authorization", token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do request: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		var sb strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			sb.Write(buf[:n])
			if err != nil {
				break
			}
		}
		return resp.StatusCode, sb.String(), resp.Header
	}

	// 已登录：未注册 /api/v1/* 路径 → HTTP 404 + 信封 JSON。
	status, body, header := doReq(http.MethodPost, "/api/v1/does-not-exist", adminToken)
	if status != http.StatusNotFound {
		t.Fatalf("unregistered /api/v1 path must be HTTP 404, got %d body=%q", status, body)
	}
	if ct := header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("unregistered /api/v1 path must be JSON, got Content-Type %q body=%q", ct, body)
	}
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("unregistered /api/v1 path body must decode as envelope JSON: %v body=%q", err, body)
	}
	if env.Code == 0 || env.Msg == "" {
		t.Fatalf("envelope 404 must carry a non-zero code and a message, got %+v body=%q", env, body)
	}
	if !strings.Contains(env.Msg, "接口不存在") {
		t.Fatalf("envelope 404 message must say 接口不存在, got %q", env.Msg)
	}

	// 未登录：同一路径仍先被 JWT 中间件拦为信封 401（兜底不得绕过鉴权）。
	status, body, _ = doReq(http.MethodPost, "/api/v1/does-not-exist", "")
	if status != http.StatusOK {
		t.Fatalf("unauthenticated request must still reach the envelope layer, got HTTP %d body=%q", status, body)
	}
	var env401 struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal([]byte(body), &env401); err != nil {
		t.Fatalf("unauthenticated body must decode as envelope JSON: %v body=%q", err, body)
	}
	if env401.Code != 401 {
		t.Fatalf("unauthenticated unregistered /api/v1 path must be envelope 401, got code=%d body=%q", env401.Code, body)
	}

	// 非 /api/ 路径保持标准裸文本 404（/error 静态页等不受影响）。
	status, body, _ = doReq(http.MethodGet, "/no-such-page", adminToken)
	if status != http.StatusNotFound {
		t.Fatalf("non-/api/ unknown path must stay HTTP 404, got %d body=%q", status, body)
	}
	if !strings.Contains(body, "404 page not found") {
		t.Fatalf("non-/api/ unknown path must stay bare-text 404, got body=%q", body)
	}
}
