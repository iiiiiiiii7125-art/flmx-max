package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"go-backend/internal/store/repo"
)

// R5-B2 D1 / A-004 回归测试：sub_store 独立限流 + POST + 弃用窗口。
//
// 旧缺陷：GET 公开、无任何限流、凭据在 URL query（进 access_log）。

func newSubStoreTestHandler(t *testing.T) *Handler {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return &Handler{repo: r}
}

func resetSubStoreLimiterForTest() {
	subStoreLimiter = &loginRateLimiter{buckets: make(map[string]*loginAttemptBucket)}
}

func decodeEnvelopeCode(t *testing.T, body []byte) int {
	t.Helper()
	var env struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	return env.Code
}

// 无认证连续请求被限流（11 次后 429），且限流桶独立于 login。
func TestA004_SubStoreRateLimitedIndependently(t *testing.T) {
	t.Setenv("FLVX_TRUSTED_PROXY_HOPS", "1")
	resetSubStoreLimiterForTest()
	resetLoginLimitersForTest()
	h := newSubStoreTestHandler(t)

	limited := false
	for i := 0; i < 20; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/open_api/sub_store?user=victim&pwd=wrong", nil)
		req.RemoteAddr = "127.0.0.1:5000"
		w := httptest.NewRecorder()
		h.openAPISubStore(w, req)
		if decodeEnvelopeCode(t, w.Body.Bytes()) == 429 {
			limited = true
			if i < 10 {
				t.Fatalf("blocked too early at attempt %d", i+1)
			}
			break
		}
	}
	if !limited {
		t.Fatal("sub_store not rate limited after 20 rapid unauthenticated requests")
	}

	// 关键：打爆 sub_store 桶后，同一 IP 的 login 限流不受影响（桶隔离）
	lw := httptest.NewRecorder()
	if !h.checkLoginRateLimit(lw, loginReqWithXFF("127.0.0.1:5000", "")) {
		t.Fatal("sub_store flood leaked into login rate limiter buckets")
	}
}

// GET 弃用头生效；POST body 为正式用法。
func TestA004_SubStoreDeprecationAndPOST(t *testing.T) {
	resetSubStoreLimiterForTest()
	h := newSubStoreTestHandler(t)

	// GET：Deprecation + Sunset 头
	greq := httptest.NewRequest(http.MethodGet, "/api/v1/open_api/sub_store?user=u&pwd=p", nil)
	greq.RemoteAddr = "127.0.0.1:5001"
	gw := httptest.NewRecorder()
	h.openAPISubStore(gw, greq)
	if gw.Header().Get("Deprecation") != "true" {
		t.Error("GET sub_store missing Deprecation: true header")
	}
	if gw.Header().Get("Sunset") != subStoreSunsetHTTPDate {
		t.Errorf("GET sub_store Sunset = %q, want %q", gw.Header().Get("Sunset"), subStoreSunsetHTTPDate)
	}

	// POST：凭据走 body；错误密码应返回鉴权失败而非“请求失败”
	preq := httptest.NewRequest(http.MethodPost, "/api/v1/open_api/sub_store",
		strings.NewReader(`{"user":"nosuchuser","password":"x","tunnel":"-1"}`))
	preq.RemoteAddr = "127.0.0.1:5002"
	pw := httptest.NewRecorder()
	h.openAPISubStore(pw, preq)
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(pw.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode POST response: %v", err)
	}
	if env.Msg != "鉴权失败" {
		t.Fatalf("POST sub_store with bad creds: msg=%q (want 鉴权失败)", env.Msg)
	}
	if pw.Header().Get("Deprecation") != "" {
		t.Error("POST sub_store should not carry Deprecation header")
	}
}
