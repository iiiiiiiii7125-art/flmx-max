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
