package kernel

import (
	"encoding/json"
	"strings"
	"testing"
)

// F-022 (audit FINAL §5-B8 / A5-002): Extra 不得覆盖面板不变量。
// 修复前这些用例全部放行（Normalize 返回 nil、Render 输出被覆盖的配置）。

func TestNormalizeRejectsProtectedExtraKeysF022(t *testing.T) {
	xrayVLESS := func(extra string) Inbound {
		return Inbound{Protocol: ProtocolVLESS, Port: 443,
			Settings: Settings{UUID: testUUID},
			Security: Security{Type: SecurityNone},
			Extra:    json.RawMessage(extra)}
	}
	singboxSS := func(extra string) Inbound {
		return Inbound{Protocol: ProtocolShadowsocks, Port: 8388,
			Settings: Settings{Method: DefaultSSMethod, Password: "AAAAAAAAAAAAAAAAAAAAAA=="},
			Extra:    json.RawMessage(extra)}
	}
	cases := []struct {
		name       string
		kernelType string
		in         Inbound
	}{
		{"xray port override", TypeXray, xrayVLESS(`{"port": 9999}`)},
		{"xray listen override", TypeXray, xrayVLESS(`{"listen": "127.0.0.1"}`)},
		{"xray tag override", TypeXray, xrayVLESS(`{"tag": "direct"}`)},
		{"xray protocol override", TypeXray, xrayVLESS(`{"protocol": "trojan"}`)},
		{"xray settings clients wiped", TypeXray, xrayVLESS(`{"settings": {"clients": []}}`)},
		{"xray stream security downgrade", TypeXray, xrayVLESS(`{"streamSettings": {"security": "none"}}`)},
		{"xray stream reality wiped", TypeXray, xrayVLESS(`{"streamSettings": {"realitySettings": {}}}`)},
		{"xray stream network swap", TypeXray, xrayVLESS(`{"streamSettings": {"network": "ws"}}`)},
		{"singbox listen_port override", TypeSingbox, singboxSS(`{"listen_port": 9999}`)},
		{"singbox password override", TypeSingbox, singboxSS(`{"password": "pwned"}`)},
		{"singbox tls override", TypeSingbox, singboxSS(`{"tls": {"enabled": false}}`)},
		{"singbox users override", TypeSingbox, singboxSS(`{"users": []}`)},
	}
	for _, tc := range cases {
		in := tc.in
		err := Normalize(tc.kernelType, &in)
		if err == nil {
			t.Fatalf("%s: protected Extra must be rejected, got nil error", tc.name)
		}
		if !strings.Contains(err.Error(), "受保护") {
			t.Fatalf("%s: error must name the protected field, got %q", tc.name, err.Error())
		}
	}
}

func TestNormalizeAllowsBenignExtraF022(t *testing.T) {
	// 预期用途（kernel_test.go 金快照同款）：sniffing 与 streamSettings.sockopt
	// 必须继续可用，防误伤。
	in := Inbound{Protocol: ProtocolVLESS, Port: 9443, Listen: "127.0.0.1",
		Settings:  Settings{UUID: testUUID},
		Transport: Transport{Type: TransportXHTTP, Path: "/x"},
		Security:  Security{Type: SecurityNone},
		Extra:     json.RawMessage(`{"sniffing":{"enabled":false},"streamSettings":{"sockopt":{"tcpFastOpen":true}}}`)}
	if err := Normalize(TypeXray, &in); err != nil {
		t.Fatalf("benign Extra must stay allowed: %v", err)
	}
	out, err := Render(TypeXray, []Inbound{in})
	if err != nil {
		t.Fatalf("render benign Extra: %v", err)
	}
	if !strings.Contains(out.Files[0].Content, "tcpFastOpen") {
		t.Fatalf("sockopt must be merged into rendered config:\n%s", out.Files[0].Content)
	}
}

func TestRenderRejectsProtectedExtraBypassingNormalizeF022(t *testing.T) {
	// 存量/导入行可能绕过 Normalize，渲染层必须兜底拒绝而不是静默覆盖。
	in := Inbound{ID: 7, Protocol: ProtocolVLESS, Port: 443, Listen: "::",
		Settings: Settings{UUID: testUUID},
		Security: Security{Type: SecurityNone},
		Extra:    json.RawMessage(`{"port": 9999}`)}
	if _, err := Render(TypeXray, []Inbound{in}); err == nil {
		t.Fatal("render must reject protected Extra keys even without Normalize")
	} else if !strings.Contains(err.Error(), "受保护") {
		t.Fatalf("error must name the protected field, got %q", err.Error())
	}
}

func TestNormalizeSnellExtraRejectedF022(t *testing.T) {
	// Snell 渲染（INI）根本不消费 Extra；保存期必须明确报错，不得静默忽略。
	in := Inbound{Protocol: ProtocolSnell, Port: 8388,
		Settings: Settings{Password: "snell-psk"},
		Extra:    json.RawMessage(`{"sniffing":{"enabled":false}}`)}
	err := Normalize(TypeSnell, &in)
	if err == nil {
		t.Fatal("Snell inbound with Extra must be rejected at save time")
	}
	if !strings.Contains(err.Error(), "Snell") || !strings.Contains(err.Error(), "高级配置") {
		t.Fatalf("error must explain Snell ignores高级配置, got %q", err.Error())
	}
}
