package service

import (
	"errors"
	"strings"
	"testing"
)

// FIX2-C #3（H §9 残余）：HTTP 上报与 WS 命令面同口径禁降级——候选
// 只允许单一 scheme：显式 scheme 优先（http/ws → 明文、https/wss →
// 加密），裸地址默认 https。旧实现恒产出 https+http 双候选并在失败
// 时自动回退明文，等于 TLS 被阻断即可把上报面降级到 http。
func TestFix2CReportCandidatesSingleScheme(t *testing.T) {
	cases := []struct {
		name string
		addr string
		want string // 期望的唯一 upload URL 前缀
	}{
		{"bare defaults https", "panel.example.com:443", "https://panel.example.com:443/flow/upload?secret=abc"},
		{"explicit http is plaintext opt-in", "http://panel.example.com:8080", "http://panel.example.com:8080/flow/upload?secret=abc"},
		{"explicit https", "https://panel.example.com:8443", "https://panel.example.com:8443/flow/upload?secret=abc"},
		{"ws maps to http", "ws://panel.example.com:8080", "http://panel.example.com:8080/flow/upload?secret=abc"},
		{"wss maps to https", "wss://panel.example.com:8443", "https://panel.example.com:8443/flow/upload?secret=abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upload, config := buildReportURLCandidates(tc.addr, "abc")
			if len(upload) != 1 || len(config) != 1 {
				t.Fatalf("expected exactly one candidate per endpoint, got upload=%v config=%v", upload, config)
			}
			if upload[0] != tc.want {
				t.Fatalf("unexpected upload candidate: %s (want %s)", upload[0], tc.want)
			}
			wantConfig := strings.Replace(tc.want, "/flow/upload?", "/flow/config?", 1)
			if config[0] != wantConfig {
				t.Fatalf("unexpected config candidate: %s (want %s)", config[0], wantConfig)
			}
		})
	}
}

// FIX2-C #6（V1-N2）：sanitizeReportURL 必须大小写不敏感且掩码全部
// 同名参数；旧实现只处理首个小写 secret=，Secret= 与第二个 secret=
// 都原样漏出。
func TestFix2CSanitizeReportURLVariants(t *testing.T) {
	in := "https://panel.example.com/flow/upload?Secret=CASESECRET&secret=FIRSTSECRET&secret=SECONDSECRET&seq=9"
	out := sanitizeReportURL(in)
	for _, leaked := range []string{"CASESECRET", "FIRSTSECRET", "SECONDSECRET"} {
		if strings.Contains(out, leaked) {
			t.Fatalf("sanitized URL leaked %q: %s", leaked, out)
		}
	}
	if !strings.Contains(out, "panel.example.com/flow/upload") || !strings.Contains(out, "seq=9") {
		t.Fatalf("sanitized URL must keep non-sensitive context: %s", out)
	}
}

// FIX2-C #6（V1-N2）：redactReportError 的逐字替换在 URL 被规范化
// （如错误文本里多一个尾随 /）时落空，残余文本必须再过一遍敏感
// 参数掩码，任何形态下都不得漏出 secret 值。
func TestFix2CRedactReportErrorNormalizedURL(t *testing.T) {
	raw := "https://panel.example.com/flow/upload?secret=TOPSECRET"
	err := errors.New("Post \"https://panel.example.com/flow/upload/?secret=TOPSECRET\": dial failed")
	out := redactReportError(err, raw, sanitizeReportURL(raw))
	if strings.Contains(out, "TOPSECRET") {
		t.Fatalf("redacted error leaked the secret via normalized URL: %s", out)
	}
}
