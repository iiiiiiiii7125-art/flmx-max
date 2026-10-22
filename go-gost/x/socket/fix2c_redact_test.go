package socket

import (
	"encoding/json"
	"strings"
	"testing"
)

// FIX2-C #4（V1-N3）：命令详情日志的脱敏渲染旧实现用正则
// "[^"]*" 匹配 JSON 字符串值，值中含转义引号 \" 时在转义处提前
// 收尾、引号后的剩余值明文落日志。改为真 JSON 解析后按键掩码：
// 构造一个 password 值内嵌引号的命令 JSON（即 json.Marshal 的
// 实际产物形态），渲染结果不得包含密码的任何片段。
func TestFix2CRedactCommandForLogEscapedQuote(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"type":      "AddService",
		"requestId": "req-1",
		"data": map[string]any{
			"name":     "svc-1",
			"password": `pa"ss-WORD-tail`,
			"auth":     map[string]any{"username": "u1", "password": "second-secret"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, ok := redactCommandForLog(payload)
	if !ok {
		t.Fatal("valid command JSON must render a redacted detail line")
	}
	for _, leaked := range []string{`pa"ss-WORD-tail`, "ss-WORD-tail", "second-secret"} {
		if strings.Contains(out, leaked) {
			t.Fatalf("redacted rendering leaked %q: %s", leaked, out)
		}
	}
	if !strings.Contains(out, "AddService") || !strings.Contains(out, "svc-1") {
		t.Fatalf("redacted rendering must keep structure identifiable: %s", out)
	}
}

// FIX2-C #4：无法解析的输入不得整行渲染（调用方据 ok=false 只打
// type+requestId 第一行），更不得把原文透传进日志。
func TestFix2CRedactCommandForLogUnparseable(t *testing.T) {
	out, ok := redactCommandForLog([]byte(`{"type":"AddService","password":"BROKEN`))
	if ok {
		t.Fatalf("unparseable payload must not render, got: %s", out)
	}
	if strings.Contains(out, "BROKEN") {
		t.Fatalf("unparseable payload leaked raw text: %s", out)
	}
}

// FIX2-C #6（V1-N2）：sanitizeWebSocketURL 的变体缺口——参数名
// 大小写变体不掩码、解析失败回退原文（fail-open）。现口径：
// 大小写不敏感、全部同名参数都掩、解析失败返回占位符。
func TestFix2CSanitizeWebSocketURLVariants(t *testing.T) {
	out := sanitizeWebSocketURL("wss://panel.example.com/system-info?type=1&Secret=CASESECRET&secret=FIRST&secret=SECOND")
	for _, leaked := range []string{"CASESECRET", "FIRST", "SECOND"} {
		if strings.Contains(out, leaked) {
			t.Fatalf("sanitized URL leaked %q: %s", leaked, out)
		}
	}
	if !strings.Contains(out, "panel.example.com/system-info") {
		t.Fatalf("sanitized URL must stay identifiable: %s", out)
	}

	bad := sanitizeWebSocketURL("wss://panel.example.com/\x7f?secret=TOPSECRET")
	if strings.Contains(bad, "TOPSECRET") {
		t.Fatalf("unparseable URL must fail closed, got: %s", bad)
	}
}
