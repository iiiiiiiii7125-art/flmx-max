package kernel

import (
	"strings"
	"testing"
)

// 复扫（fix/final-resweep）：从头机械重推发现的新增同族残留。
// 先红后绿：每项修复前应失败，修复后须通过；含合法对照（防过修）。

// R-01：自建入站 Hysteria2 伪装地址 Masquerade `https://a..b/` 被接受并落库。
// normalizeSettings 只做 url.Parse + scheme + host 非空，hostname 不走
// HasEmptyLabel；原值渲染进 sing-box masquerade（render_singbox.go:159）。
func TestResweepMasqueradeEmptyLabel(t *testing.T) {
	bad := []string{"https://a..b/", "https://a...b/masq", "http://a..b:8080/"}
	for _, m := range bad {
		in := &Inbound{Protocol: ProtocolHysteria2, Port: 443,
			Transport: Transport{Type: TransportQUIC},
			Security:  Security{Type: SecurityTLS, SNI: "example.com"},
			Settings:  Settings{Masquerade: m}}
		if err := Normalize(TypeSingbox, in); err == nil {
			t.Errorf("Normalize(Masquerade=%q) = nil, want error (empty DNS label)", m)
		} else if !strings.Contains(err.Error(), "空标签") && !strings.Contains(err.Error(), "格式") {
			t.Errorf("Normalize(Masquerade=%q) = %v, want empty-label/format error", m, err)
		}
	}
	for _, m := range []string{"", "https://example.com/", "https://203.0.113.9/masq"} {
		in := &Inbound{Protocol: ProtocolHysteria2, Port: 443,
			Transport: Transport{Type: TransportQUIC},
			Security:  Security{Type: SecurityTLS, SNI: "example.com"},
			Settings:  Settings{Masquerade: m}}
		if err := Normalize(TypeSingbox, in); err != nil {
			t.Errorf("Normalize(Masquerade=%q) = %v, want nil", m, err)
		}
	}
}

// X-05 写面锚点：NormalizeSplitRules 仍拒收空标签域名。本修复只补
// 恢复面、写面一字不动；此测试钉死写面口径，防止日后“恢复面放宽”
// 反向扩散到写面。
func TestResweepSplitRulesWriteSurfaceStillRejectsEmptyLabel(t *testing.T) {
	for _, d := range []string{"a..b", "a...b"} {
		if _, err := NormalizeSplitRules([]SplitRule{{Domains: []string{d}, Action: SplitDirect}}); err == nil {
			t.Fatalf("NormalizeSplitRules(domains=[%q]) = nil, want error (empty DNS label)", d)
		}
	}
	if _, err := NormalizeSplitRules([]SplitRule{{Domains: []string{"a..b", "example.com"}, Action: SplitDirect}}); err == nil {
		t.Fatalf("NormalizeSplitRules(domains=[a..b example.com]) = nil, want error (one bad domain rejects the rule)")
	}
	out, err := NormalizeSplitRules([]SplitRule{{Domains: []string{"example.com"}, Action: SplitDirect}})
	if err != nil {
		t.Fatalf("NormalizeSplitRules(domains=[example.com]) = %v, want nil", err)
	}
	if len(out) != 1 || len(out[0].Domains) != 1 || out[0].Domains[0] != "example.com" {
		t.Fatalf("NormalizeSplitRules(domains=[example.com]) = %v, want [{domains:[example.com]}]", out)
	}
}
