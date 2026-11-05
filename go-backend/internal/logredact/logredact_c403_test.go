package logredact

import (
	"strings"
	"testing"
)

// C4-03: 三类 path token 形状此前漏掩、原文落日志——纯字母 ≥16、
// 纯数字 ≥16、<16 的字母数字混合短 token。本组逐形钉住，并保
// 住普通 slug/短段不误掩的既有口径。

func TestURLMasksPureAlphaLongToken(t *testing.T) {
	in := "https://p.example.com/link/abcdefghijklmnop"
	out := URL(in)
	if strings.Contains(out, "abcdefghijklmnop") {
		t.Fatalf("pure-alpha long token leaked: %s", out)
	}
}

func TestURLMasksPureNumericLongToken(t *testing.T) {
	in := "https://p.example.com/link/1234567890123456"
	out := URL(in)
	if strings.Contains(out, "1234567890123456") {
		t.Fatalf("pure-numeric long token leaked: %s", out)
	}
}

func TestURLMasksShortMixedToken(t *testing.T) {
	in := "https://p.example.com/link/abc12345"
	out := URL(in)
	if strings.Contains(out, "abc12345") {
		t.Fatalf("short mixed token leaked: %s", out)
	}
}

func TestURLKeepsSlugsAndShortSegments(t *testing.T) {
	for _, in := range []string{
		"https://panel.example.com/api/v1/subscriptions/getting-started-guide",
		"https://panel.example.com/api/v1/user/list",
		"https://panel.example.com/download/v2.5.16/clients",
		"https://panel.example.com/link/abcdefghij", // 纯字母短段（<16、无分隔）不掩
	} {
		if out := URL(in); out != in {
			t.Fatalf("ordinary path must pass unchanged: %s -> %s", in, out)
		}
	}
}

func TestPathAppliesGenericSegmentMasking(t *testing.T) {
	out := Path("/api/v1/open_api/sub/9f8e7d6c5b4a39281706f5e4d3c2b1a0")
	if out != "/api/v1/open_api/sub/***" {
		t.Fatalf("prefix rule must still win: %s", out)
	}
	out = Path("/some/route/abc12345")
	if strings.Contains(out, "abc12345") {
		t.Fatalf("Path must mask token-shaped segments generically, got %s", out)
	}
	if out := Path("/api/v1/user/list"); out != "/api/v1/user/list" {
		t.Fatalf("ordinary API path must pass unchanged, got %s", out)
	}
}
