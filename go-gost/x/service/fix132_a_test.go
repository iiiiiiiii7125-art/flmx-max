package service

import (
	"context"
	"strings"
	"testing"
	"time"
)

// FIX-A #1（D1-01）：HTTP 上报失败/回退日志与聚合 error 不得包含
// 完整 URL 中的明文节点 secret（WS 侧已有 sanitizeWebSocketURL，
// HTTP 侧漏网）。修复前 error 原文含 secret=明文 → FAIL。
func TestPostJSONWithFallbackErrorRedactsSecret(t *testing.T) {
	const secret = "NODESECRET-abc123456789"
	urls := []string{
		"http://127.0.0.1:1/flow/upload?secret=" + secret,
		"http://127.0.0.1:2/flow/upload?secret=" + secret,
	}
	ok, err := postJSONWithFallback(context.Background(), urls, []byte(`{}`), "fix132-test", 2*time.Second, nil)
	if ok {
		t.Fatal("unreachable endpoints must not report success")
	}
	if err == nil {
		t.Fatal("expected aggregated error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("aggregated error leaked node secret: %v", err)
	}
	// 掩码形态二选一：我方 sanitizeReportURL 产出 secret=***；
	// http.Client 的 *url.Error 自身则产出 secret=<redacted>。
	if !strings.Contains(err.Error(), "secret=***") && !strings.Contains(err.Error(), "secret=<redacted>") {
		t.Fatalf("error should keep redacted URL context, got: %v", err)
	}
}

func TestSanitizeReportURL(t *testing.T) {
	got := sanitizeReportURL("https://panel.example.com/flow/upload?secret=TOPSECRET&seq=9")
	if strings.Contains(got, "TOPSECRET") {
		t.Fatalf("secret leaked in sanitized URL: %s", got)
	}
	if !strings.Contains(got, "panel.example.com/flow/upload") {
		t.Fatalf("sanitized URL lost host/path context: %s", got)
	}
	plain := sanitizeReportURL("https://panel.example.com/flow/config")
	if plain != "https://panel.example.com/flow/config" {
		t.Fatalf("credential-free URL must pass through unchanged: %s", plain)
	}
}
