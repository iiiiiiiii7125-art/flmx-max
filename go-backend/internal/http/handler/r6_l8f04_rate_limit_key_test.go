package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"go-backend/internal/clientip"
)

// R6-L8-F-04：限流键第三级回退不得再是含端口的原始 RemoteAddr
// （端口轮换即换桶绕过），须收口到 clientip.UnresolvedKey 共享桶
// （fail-closed 记账），与 gh 镜像面 / WS 面同口径。四处原自有
// 回退链（登录限流、login 用户名信号 IP、sub_store、captcha
// verify）统一改走 rateLimitKeyForRequest，本文件钉死其形态与
// 两条消费路径的落桶键。

func TestR6L8F04RateLimitKeyForRequest(t *testing.T) {
	cases := []struct {
		name       string
		remoteAddr string
		xff        string
		hopsEnv    string
		want       string
	}{
		{name: "public direct strips port", remoteAddr: "203.0.113.7:5555", want: "203.0.113.7"},
		{name: "private peer without xff", remoteAddr: "10.0.0.9:8080", want: "10.0.0.9"},
		{name: "ipv6 direct strips port", remoteAddr: "[2001:db8::5]:443", want: "2001:db8::5"},
		{name: "trusted proxy anchors xff", remoteAddr: "172.30.0.1:8080", xff: "203.0.113.7", hopsEnv: "1", want: "203.0.113.7"},
		{name: "unparseable falls to shared bucket", remoteAddr: "bogus-remote-addr", want: clientip.UnresolvedKey},
		{name: "empty falls to shared bucket", remoteAddr: "", want: clientip.UnresolvedKey},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.hopsEnv != "" {
				t.Setenv("FLMX_TRUSTED_PROXY_HOPS", tc.hopsEnv)
			}
			r := &http.Request{RemoteAddr: tc.remoteAddr, Header: make(http.Header)}
			if tc.xff != "" {
				r.Header.Set("X-Forwarded-For", tc.xff)
			}
			if got := rateLimitKeyForRequest(r, "test surface"); got != tc.want {
				t.Fatalf("rateLimitKeyForRequest(remoteAddr=%q) = %q, want %q", tc.remoteAddr, got, tc.want)
			}
			if got := rateLimitKeyForRequest(r, "test surface"); got == tc.remoteAddr && tc.remoteAddr != tc.want {
				t.Fatalf("rate limit key must never be the raw RemoteAddr %q", tc.remoteAddr)
			}
		})
	}
}

func TestR6L8F04LoginRateLimitUnresolvedSharedBucket(t *testing.T) {
	old := globalLoginLimiter
	globalLoginLimiter = &loginRateLimiter{buckets: make(map[string]*loginAttemptBucket)}
	defer func() { globalLoginLimiter = old }()

	h := &Handler{}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/user/login", nil)
	r.RemoteAddr = "bogus-remote-addr"
	for i := 0; i < loginRateLimitMaxAttempts; i++ {
		if !h.checkLoginRateLimit(httptest.NewRecorder(), r) {
			t.Fatalf("attempt %d within the shared-bucket cap must pass", i+1)
		}
	}
	if h.checkLoginRateLimit(httptest.NewRecorder(), r) {
		t.Fatal("attempt beyond the shared-bucket cap must be rejected")
	}
	if _, ok := globalLoginLimiter.buckets[clientip.UnresolvedKey]; !ok {
		t.Fatalf("unresolvable requests must land in the shared bucket %q, buckets: %v",
			clientip.UnresolvedKey, globalLoginLimiter.buckets)
	}
	if len(globalLoginLimiter.buckets) != 1 {
		t.Fatalf("expected exactly the shared bucket, got %v", globalLoginLimiter.buckets)
	}
}

func TestR6L8F04CaptchaVerifyUnresolvedSharedBucket(t *testing.T) {
	h := &Handler{}
	r := &http.Request{RemoteAddr: "bogus-remote-addr", Header: make(http.Header)}
	for i := 0; i < captchaVerifyRateLimitMaxAttempts; i++ {
		if !h.allowCaptchaVerify(r) {
			t.Fatalf("attempt %d within the shared-bucket cap must pass", i+1)
		}
	}
	if h.allowCaptchaVerify(r) {
		t.Fatal("attempt beyond the shared-bucket cap must be rejected")
	}
	h.captchaVerifyMu.Lock()
	defer h.captchaVerifyMu.Unlock()
	if len(h.captchaVerifyBuckets) != 1 {
		t.Fatalf("expected exactly one bucket, got %v", h.captchaVerifyBuckets)
	}
	if _, ok := h.captchaVerifyBuckets[clientip.UnresolvedKey]; !ok {
		t.Fatalf("bucket key must be the shared key %q, got %v", clientip.UnresolvedKey, h.captchaVerifyBuckets)
	}
}
