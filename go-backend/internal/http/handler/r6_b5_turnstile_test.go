package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// R6 批5 · R6X-013（R6-C-02）：Turnstile 校验出站是面板出站面全集
// 唯一无超时客户端——http.PostForm 走 DefaultClient（Timeout=0、
// 默认跟随 ≤10 跳重定向），位于登录/验证码路径，上游挂死时调用
// goroutine 无限期阻塞且可累积。修复口径：专用 client（显式超时
// 10s 量级 + 受限重定向）+ 注入缝（client 与校验端点 URL 均为包级
// var，测试可替换）。正常路径行为不变（非 BC）。

func swapR6B5Turnstile(t *testing.T, client *http.Client, verifyURL string) {
	t.Helper()
	savedClient, savedURL := turnstileClient, turnstileSiteVerifyURL
	turnstileClient, turnstileSiteVerifyURL = client, verifyURL
	t.Cleanup(func() { turnstileClient, turnstileSiteVerifyURL = savedClient, savedURL })
}

func TestR6B5TurnstileDefaultClientHasTimeoutAndRestrictedRedirect(t *testing.T) {
	// 静态口径：默认专用 client 必须显式超时（10s 量级）且带
	// 重定向限制（先红：修复前无 turnstileClient 注入缝、
	// 走 DefaultClient Timeout=0）。
	if turnstileClient == nil {
		t.Fatal("turnstileClient 不得为 nil")
	}
	if turnstileClient.Timeout < 5*time.Second || turnstileClient.Timeout > 30*time.Second {
		t.Fatalf("R6X-013：专用 client 超时须为 10s 量级，got %s", turnstileClient.Timeout)
	}
	if turnstileClient.CheckRedirect == nil {
		t.Fatal("R6X-013：专用 client 必须设受限重定向策略（CheckRedirect）")
	}
	if !strings.HasPrefix(turnstileSiteVerifyURL, "https://challenges.cloudflare.com/") {
		t.Fatalf("默认校验端点须为 Cloudflare siteverify，got %q", turnstileSiteVerifyURL)
	}
}

func TestR6B5TurnstileNormalPath(t *testing.T) {
	// 正常路径防过修对照：上游 {"success":true} → 通过、
	// {"success":false} → 不通过；表单 secret/response 原样送达。
	var gotSecret, gotResponse string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err == nil {
			gotSecret = r.Form.Get("secret")
			gotResponse = r.Form.Get("response")
		}
		if r.Form.Get("response") == "good-token" {
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":false}`))
	}))
	t.Cleanup(srv.Close)
	swapR6B5Turnstile(t, turnstileClient, srv.URL)

	h := &Handler{}
	if !h.verifyCloudflareTurnstile("good-token", "the-secret") {
		t.Fatal("success=true 应通过校验")
	}
	if gotSecret != "the-secret" || gotResponse != "good-token" {
		t.Fatalf("表单字段须原样送达，got secret=%q response=%q", gotSecret, gotResponse)
	}
	if h.verifyCloudflareTurnstile("bad-token", "the-secret") {
		t.Fatal("success=false 应不通过校验")
	}
	if h.verifyCloudflareTurnstile("", "the-secret") || h.verifyCloudflareTurnstile("tok", "") {
		t.Fatal("空 token/secret 应直接不通过（既有口径）")
	}
}

func TestR6B5TurnstileHangingUpstreamTimesOut(t *testing.T) {
	// 挂死上游动态用例（常驻）：上游接受连接后不响应（等请求
	// 上下文取消才返回）。注入短超时 client 复刻专用 client 形态
	// （仅 Timeout 缩短、重定向策略沿默认），断言调用方在超时
	// 量级内返回 false、不累积挂死——修复前函数无注入缝且走
	// DefaultClient，此用例无从成立。
	unblock := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-unblock:
		case <-time.After(60 * time.Second):
		}
	}))
	t.Cleanup(srv.Close)
	// 先于 srv.Close 执行（Cleanup 后进先出）：放行仍阻塞的 handler，
	// 免 Close 等其自然超时——被测的超时行为由下方 elapsed 断言承担。
	t.Cleanup(func() { close(unblock) })
	shortClient := &http.Client{Timeout: 300 * time.Millisecond, CheckRedirect: turnstileClient.CheckRedirect}
	swapR6B5Turnstile(t, shortClient, srv.URL)

	h := &Handler{}
	start := time.Now()
	if h.verifyCloudflareTurnstile("tok", "secret") {
		t.Fatal("挂死上游不得通过校验")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("R6X-013：挂死上游下调用方须在超时量级内返回，实际阻塞 %s", elapsed.Round(time.Millisecond))
	}
}

func TestR6B5TurnstileRedirectNotFollowedToSuccess(t *testing.T) {
	// 受限重定向动态格：校验端点返回 302 跳到一个会给出
	// success=true 的地址——不得跟随并据此通过（默认跟随策略下
	// 会通过 = 先红形态）。用默认专用 client（仅替换端点 URL）。
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	t.Cleanup(target.Close)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	swapR6B5Turnstile(t, turnstileClient, srv.URL)

	h := &Handler{}
	if h.verifyCloudflareTurnstile("tok", "secret") {
		t.Fatal("R6X-013：重定向不得被跟随至 success=true 而通过校验")
	}
}
