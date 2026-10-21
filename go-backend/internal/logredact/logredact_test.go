package logredact

import (
	"strings"
	"testing"
)

// FIX2-C #5（V1-N1）：logredact.URL 此前只掩面板自身前缀
// （/api/v1/open_api/sub/），第三方订阅源 URL 的 path 型 token
// （机场订阅链接的常见形态 https://provider/link/{token}）经
// secure_fetch/fetch_audit 日志原样落盘。现通用规则：日志中的
// URL 一律掩码疑似 token 的长随机 path 段（≥16 字符、字母数字
// 混合、仅 token 字符集），普通路径段保留。
func TestURLMasksThirdPartyPathToken(t *testing.T) {
	token := "9f8e7d6c5b4a39281706f5e4d3c2b1a0"
	out := URL("https://provider.example.com/link/" + token)
	if strings.Contains(out, token) {
		t.Fatalf("third-party path token leaked: %s", out)
	}
	if !strings.HasPrefix(out, "https://provider.example.com/link/") {
		t.Fatalf("masked URL must keep scheme/host/readable prefix: %s", out)
	}
}

func TestURLMasksUUIDPathSegment(t *testing.T) {
	out := URL("https://api.example.com/v1/sessions/3f6f1c2e-9a4b-4cde-8f01-23456789abcd/detail")
	if strings.Contains(out, "3f6f1c2e") {
		t.Fatalf("UUID path segment leaked: %s", out)
	}
	if !strings.Contains(out, "/v1/sessions/") || !strings.HasSuffix(out, "/detail") {
		t.Fatalf("non-token segments must be preserved: %s", out)
	}
}

func TestURLKeepsOrdinaryPaths(t *testing.T) {
	in := "https://panel.example.com/api/v1/subscriptions/getting-started-guide"
	if out := URL(in); out != in {
		t.Fatalf("ordinary path must pass through unchanged, got %s", out)
	}
}

func TestURLExemptPrefixSkipsSegmentMasking(t *testing.T) {
	t.Setenv("FLMX_LOG_REDACT_EXEMPT_PREFIXES", "/public/files/")
	token := "9f8e7d6c5b4a39281706f5e4d3c2b1a0"
	in := "https://cdn.example.com/public/files/" + token
	if out := URL(in); out != in {
		t.Fatalf("exempt prefix must skip segment masking, got %s", out)
	}
	// 豁免只作用于命中前缀的路径，其余照旧掩码。
	out := URL("https://cdn.example.com/link/" + token)
	if strings.Contains(out, token) {
		t.Fatalf("non-exempt path token leaked: %s", out)
	}
}
