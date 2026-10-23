package kernel

import (
	"strings"
	"testing"
)

// 协议×格式矩阵审计（2026-10-20）回归：导入的 XHTTP 节点在 Clash 路径
// 不得再被整体跳过（与原生 ClashProxy 在 1.0.17 的修复对齐）。
func TestProxyClashRendersImportedXHTTP(t *testing.T) {
	e := Endpoint{Name: "导入 xhttp", Host: "203.0.113.9", Port: 443, Direct: true, Proxy: Proxy{
		"type": "vless", "server": "203.0.113.9", "port": 443,
		"uuid":    "11111111-2222-3333-4444-555555555555",
		"network": "xhttp", "tls": true, "servername": "cdn.example.com",
		"xhttp-opts": map[string]interface{}{"path": "/xh", "mode": "auto"},
	}}
	node, ok := proxyClash(e)
	if !ok {
		t.Fatal("imported xhttp proxy must render for clash")
	}
	found := false
	for _, f := range node {
		if f.Key == "network" && f.Value == "xhttp" {
			found = true
		}
	}
	if !found {
		t.Fatalf("rendered node lacks network: xhttp: %v", node)
	}
}

// SS 密码 2022-blake3-chacha20-poly1305 是面板可选的标准 SS2022 方法，
// Loon/QX 白名单不得遗漏（曾导致整节点在两家订阅中静默消失）。
// R6 批 1 注：loonLine/qxLine 同批补 SS2022 key 逐段谓词门后，夹具
// 密码须为该 cipher 的合法 32B key（原夹具 16B key 对 32B cipher 本
// 不合法、旧面无 key 门才得以渲染）；本测试的断言对象（cipher 白
// 名单覆盖）不变。
func TestLoonQXRenderSS2022ChachaCipher(t *testing.T) {
	p := Proxy{"type": "ss", "server": "203.0.113.10", "port": 8388,
		"cipher": "2022-blake3-chacha20-poly1305", "password": "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="}
	if line, ok := loonLine(p); !ok || !strings.Contains(line, "2022-blake3-chacha20-poly1305") {
		t.Fatalf("loon line = %q ok=%v", line, ok)
	}
	if line, ok := qxLine(p); !ok || !strings.Contains(line, "2022-blake3-chacha20-poly1305") {
		t.Fatalf("qx line = %q ok=%v", line, ok)
	}
}

// Surfboard 与 Egern 消费 Surge 格式，UA 识别应分发 Surge 而非通用格式。
func TestFormatForUserAgentSurgeFamily(t *testing.T) {
	for _, ua := range []string{"Surfboard/2.3.0", "Egern/1.20.0"} {
		if got := FormatForUserAgent(ua); got != FormatSurge {
			t.Fatalf("UA %q -> %q, want %q", ua, got, FormatSurge)
		}
	}
}

// 导入的 http / quic 传输节点在 sing-box 有对应表达，不得被 transport 分支丢弃。
func TestProxySingboxRendersHTTPAndQUICTransports(t *testing.T) {
	httpE := Endpoint{Name: "vmess http", Host: "203.0.113.11", Port: 443, Direct: true, Proxy: Proxy{
		"type": "vmess", "server": "203.0.113.11", "port": 443,
		"uuid": "11111111-2222-3333-4444-555555555555", "alterId": 0,
		"network": "http", "tls": true,
		"http-opts": map[string]interface{}{"path": []interface{}{"/h2"}, "headers": map[string]interface{}{"Host": []interface{}{"cdn.example.com"}}},
	}}
	outs, ok := proxySingbox(httpE)
	if !ok {
		t.Fatal("imported vmess+http must render for sing-box")
	}
	if tr, _ := outs[0]["transport"].(map[string]interface{}); tr == nil || tr["type"] != "http" {
		t.Fatalf("transport = %v, want type http", outs[0]["transport"])
	}

	quicE := Endpoint{Name: "vless quic", Host: "203.0.113.12", Port: 443, Direct: true, Proxy: Proxy{
		"type": "vless", "server": "203.0.113.12", "port": 443,
		"uuid":    "11111111-2222-3333-4444-555555555555",
		"network": "quic", "tls": true,
	}}
	outs, ok = proxySingbox(quicE)
	if !ok {
		t.Fatal("imported vless+quic must render for sing-box")
	}
	if tr, _ := outs[0]["transport"].(map[string]interface{}); tr == nil || tr["type"] != "quic" {
		t.Fatalf("transport = %v, want type quic", outs[0]["transport"])
	}
}

// ComboSupported 是下发前复检与 catalog 的同一口径：sing-box 的 gRPC+Reality
// 已补齐（与 Xray 侧一致），ws+Reality 与 sing-box+xhttp 仍必须拒绝。
func TestComboSupportedMatrix(t *testing.T) {
	cases := []struct {
		kernel, protocol, transport, security string
		want                                  bool
	}{
		{TypeSingbox, ProtocolVLESS, TransportGRPC, SecurityReality, true},
		{TypeSingbox, ProtocolTrojan, TransportGRPC, SecurityReality, true},
		{TypeSingbox, ProtocolVLESS, TransportWS, SecurityReality, false},
		{TypeSingbox, ProtocolVLESS, TransportXHTTP, SecurityTLS, false},
		{TypeXray, ProtocolVLESS, TransportXHTTP, SecurityReality, true},
		{TypeSingbox, ProtocolAnyTLS, TransportTCP, SecurityTLS, true},
		{TypeSnell, ProtocolSnell, TransportTCP, SecurityShadowTLS, true},
		{TypeXray, ProtocolHysteria2, TransportQUIC, SecurityTLS, false},
		{"nosuchkernel", ProtocolVLESS, TransportTCP, SecurityTLS, false},
	}
	for _, c := range cases {
		if got := ComboSupported(c.kernel, c.protocol, c.transport, c.security); got != c.want {
			t.Errorf("ComboSupported(%s,%s,%s,%s) = %v, want %v", c.kernel, c.protocol, c.transport, c.security, got, c.want)
		}
	}
}
