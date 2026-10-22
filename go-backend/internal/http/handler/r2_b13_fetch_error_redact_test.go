package handler

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// R2X-028（R2 补批 13）：抓取失败的错误原文含订阅 token 明文——
// client.Do 的 *url.Error 内嵌完整请求 URL（含 ?token=），该 err 被
// 原样传播到 remote_source 的定时同步日志与 last_error 落库、以及
// kernel_external 的手动导入失败回显。期望：错误离开 secure_fetch
// 层前剥离 URL 查询串（logredact 口径），错误分类信息（连接拒绝等）
// 保留以便排障。

const (
	r2b13TokenConnRefused = "SECRET_TOKEN_028_CONNREFUSED"
	r2b13TokenPublicHTTP  = "SECRET_TOKEN_028_PUBLICHTTP"
	r2b13TokenRedirect    = "SECRET_TOKEN_028_REDIRECT"
)

// r2b13AllowPrivate 打开抓取私网放行（本机回环拨号用），测试后还原。
func r2b13AllowPrivate(t *testing.T) {
	t.Helper()
	for _, k := range []string{"FLMX_FETCH_ALLOW_PRIVATE_NETWORKS", "FLVX_FETCH_ALLOW_PRIVATE_NETWORKS"} {
		os.Setenv(k, "true")
		t.Cleanup(func() { os.Unsetenv(k) })
	}
	for _, k := range []string{"FLMX_FETCH_PRIVATE_ALLOWLIST", "FLVX_FETCH_PRIVATE_ALLOWLIST"} {
		os.Unsetenv(k)
	}
}

// r2b13ClosedPort 返回一个刚释放的本机端口：拨号必然连接拒绝，
// 不依赖任何约定端口的占用状态。
func r2b13ClosedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return port
}

func r2b13FetchCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 15*time.Second)
}

// redactFetchError 单元面：%w 包装层的前缀文案与内层分类保留，
// 只剥 URL；不含 URL 的错误原样透传（同一 error 值）。
func TestR2B13_RedactFetchErrorUnit(t *testing.T) {
	inner := &url.Error{
		Op:  "Get",
		URL: "https://provider.example/sub?token=" + r2b13TokenConnRefused,
		Err: errors.New("dial tcp 1.2.3.4:443: connect: connection refused"),
	}
	wrapped := fmt.Errorf("redirect blocked: %w", inner)
	out := redactFetchError(wrapped)
	if strings.Contains(out.Error(), r2b13TokenConnRefused) || strings.Contains(out.Error(), "token=") {
		t.Fatalf("redacted error still leaks token: %v", out)
	}
	if !strings.Contains(out.Error(), "redirect blocked") || !strings.Contains(out.Error(), "connection refused") {
		t.Fatalf("redacted error lost classification text: %v", out)
	}
	if !strings.Contains(out.Error(), "https://provider.example/sub") {
		t.Fatalf("redacted error should keep host+path for troubleshooting: %v", out)
	}

	plain := errors.New("unexpected status 503")
	if got := redactFetchError(plain); got != plain {
		t.Fatalf("error without URL must pass through unchanged, got %v", got)
	}
	if got := redactFetchError(nil); got != nil {
		t.Fatalf("nil must stay nil, got %v", got)
	}
}

// 连接拒绝（订阅源不可达的最常见形态）：fetchSubscriptionInfo 返回
// 的 err 不得含 token 原文，且须保留连接拒绝分类。
func TestR2B13_FetchErrorStripsTokenConnRefused(t *testing.T) {
	r2b13AllowPrivate(t)
	port := r2b13ClosedPort(t)
	rawURL := fmt.Sprintf("https://127.0.0.1:%d/sub?token=%s", port, r2b13TokenConnRefused)

	ctx, cancel := r2b13FetchCtx()
	defer cancel()
	_, _, err := fetchSubscriptionInfo(ctx, rawURL)
	if err == nil {
		t.Fatal("expected fetch to fail against a closed port")
	}
	if strings.Contains(err.Error(), r2b13TokenConnRefused) {
		t.Fatalf("fetch error leaks subscription token: %v", err)
	}
	if strings.Contains(err.Error(), "token=") {
		t.Fatalf("fetch error still carries query string: %v", err)
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("fetch error lost its classification (want connection refused): %v", err)
	}
}

// 公网 http 拒绝：该错误在 validateFetchIP 内以 %s 拼入完整 rawURL
// （含 token），不经 client.Do 也会泄漏。脱敏后仍须保留
// "public http not allowed" 分类文案（IMP-007 的中文翻译靠它匹配）。
func TestR2B13_FetchErrorStripsTokenPublicHTTP(t *testing.T) {
	for _, k := range []string{
		"FLMX_FETCH_ALLOW_PRIVATE_NETWORKS", "FLVX_FETCH_ALLOW_PRIVATE_NETWORKS",
		"FLMX_FETCH_PRIVATE_ALLOWLIST", "FLVX_FETCH_PRIVATE_ALLOWLIST",
	} {
		os.Unsetenv(k)
	}
	rawURL := "http://8.8.8.8/sub?token=" + r2b13TokenPublicHTTP

	ctx, cancel := r2b13FetchCtx()
	defer cancel()
	_, err := SecureFetch(ctx, rawURL)
	if err == nil {
		t.Fatal("expected public http fetch to be rejected")
	}
	if strings.Contains(err.Error(), r2b13TokenPublicHTTP) {
		t.Fatalf("fetch error leaks subscription token: %v", err)
	}
	if !strings.Contains(err.Error(), "public http not allowed") {
		t.Fatalf("fetch error lost its classification (want public http not allowed): %v", err)
	}
}

// 重定向跳：token 只出现在重定向目标 URL 中（不在原始 rawURL 里），
// client.Do 的 *url.Error 内嵌的是目标 URL。收口必须覆盖这条通道，
// 不能只替换原始 URL。
func TestR2B13_FetchErrorStripsTokenRedirectTarget(t *testing.T) {
	r2b13AllowPrivate(t)
	closedPort := r2b13ClosedPort(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := fmt.Sprintf("https://127.0.0.1:%d/redir?token=%s", closedPort, r2b13TokenRedirect)
		http.Redirect(w, r, target, http.StatusFound)
	}))
	defer srv.Close()

	ctx, cancel := r2b13FetchCtx()
	defer cancel()
	_, err := SecureFetch(ctx, srv.URL+"/start")
	if err == nil {
		t.Fatal("expected redirected fetch to fail against a closed port")
	}
	if strings.Contains(err.Error(), r2b13TokenRedirect) {
		t.Fatalf("fetch error leaks redirect-target token: %v", err)
	}
	if strings.Contains(err.Error(), "token=") {
		t.Fatalf("fetch error still carries query string: %v", err)
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("fetch error lost its classification (want connection refused): %v", err)
	}
}
