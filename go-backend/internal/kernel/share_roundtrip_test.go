package kernel

import (
	"encoding/base64"
	"strings"
	"testing"
)

// TestShareURIRoundTripAllForms 是常设往返回归（B11-001，沿 Round 2
// B11-002 处方落地）：面板 ShareURI 生成的全部 15 种形态必须能被面板
// 自身 ParseImport 回解析出恰 1 个节点——生成面与导入面再分叉时本
// 测试先红。历史上 naive 的 NekoBox/v2rayN 形态（naive+https://）只
// 生成不可导入，Round 2 处方在分诊环节遗失、1.7.5 原样复现，本表
// 即为防再遗失的永久门。
func TestShareURIRoundTripAllForms(t *testing.T) {
	uuid := "0b53cf5d-dfab-4ea3-98d7-f2d75400ed68"
	ss2022Key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef"))
	cases := []struct {
		name   string
		kernel string
		in     Inbound
	}{
		{"vless-reality-tcp", TypeXray, Inbound{Protocol: ProtocolVLESS, Port: 443,
			Settings:  Settings{UUID: uuid, Flow: FlowVision},
			Transport: Transport{Type: TransportTCP},
			Security:  Security{Type: SecurityReality, SNI: "www.example.com", RealityDest: "www.example.com:443", RealityPublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", RealityShortIDs: []string{"01ab"}}}},
		{"vless-ws-tls", TypeXray, Inbound{Protocol: ProtocolVLESS, Port: 443,
			Settings:  Settings{UUID: uuid},
			Transport: Transport{Type: TransportWS, Path: "/ws"},
			Security:  Security{Type: SecurityTLS, SNI: "cdn.example.com"}}},
		{"vless-grpc-reality", TypeXray, Inbound{Protocol: ProtocolVLESS, Port: 443,
			Settings:  Settings{UUID: uuid},
			Transport: Transport{Type: TransportGRPC, ServiceName: "gsvc"},
			Security:  Security{Type: SecurityReality, SNI: "www.example.com", RealityDest: "www.example.com:443", RealityPublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", RealityShortIDs: []string{"01ab"}}}},
		{"vless-xhttp-tls", TypeXray, Inbound{Protocol: ProtocolVLESS, Port: 443,
			Settings:  Settings{UUID: uuid},
			Transport: Transport{Type: TransportXHTTP, Path: "/xh"},
			Security:  Security{Type: SecurityTLS, SNI: "cdn.example.com"}}},
		{"vless-none-tcp", TypeXray, Inbound{Protocol: ProtocolVLESS, Port: 8080,
			Settings:  Settings{UUID: uuid},
			Transport: Transport{Type: TransportTCP},
			Security:  Security{Type: SecurityNone}}},
		{"vmess-tcp-none", TypeXray, Inbound{Protocol: ProtocolVMess, Port: 8080,
			Settings:  Settings{UUID: uuid},
			Transport: Transport{Type: TransportTCP},
			Security:  Security{Type: SecurityNone}}},
		{"vmess-ws-tls", TypeXray, Inbound{Protocol: ProtocolVMess, Port: 443,
			Settings:  Settings{UUID: uuid},
			Transport: Transport{Type: TransportWS, Path: "/vm"},
			Security:  Security{Type: SecurityTLS, SNI: "cdn.example.com"}}},
		{"trojan-tcp-tls", TypeXray, Inbound{Protocol: ProtocolTrojan, Port: 443,
			Settings:  Settings{Password: "tr-pass"},
			Transport: Transport{Type: TransportTCP},
			Security:  Security{Type: SecurityTLS, SNI: "tj.example.com"}}},
		{"ss-aes", TypeXray, Inbound{Protocol: ProtocolShadowsocks, Port: 8388,
			Settings: Settings{Method: "aes-256-gcm", Password: "ss-pass"},
			Security: Security{Type: SecurityNone}}},
		{"ss2022", TypeSingbox, Inbound{Protocol: ProtocolShadowsocks, Port: 8388,
			Settings: Settings{Method: "2022-blake3-aes-128-gcm", Password: ss2022Key},
			Security: Security{Type: SecurityNone}}},
		{"hysteria2", TypeSingbox, Inbound{Protocol: ProtocolHysteria2, Port: 443,
			Settings:  Settings{Password: "hy-pass"},
			Transport: Transport{Type: TransportQUIC},
			Security:  Security{Type: SecurityTLS, SNI: "hy.example.com", CertMode: "self"}}},
		{"tuic", TypeSingbox, Inbound{Protocol: ProtocolTUIC, Port: 443,
			Settings:  Settings{UUID: uuid, Password: "tu-pass"},
			Transport: Transport{Type: TransportQUIC},
			Security:  Security{Type: SecurityTLS, SNI: "tu.example.com", CertMode: "self"}}},
		{"anytls", TypeSingbox, Inbound{Protocol: ProtocolAnyTLS, Port: 443,
			Settings:  Settings{Password: "any-pass"},
			Transport: Transport{Type: TransportTCP},
			Security:  Security{Type: SecurityTLS, SNI: "at.example.com", CertMode: "self"}}},
		{"naive-direct", TypeSingbox, Inbound{Protocol: ProtocolNaive, Port: 443,
			Settings:  Settings{Username: "nv-user", Password: "nv-pass1"},
			Transport: Transport{Type: TransportTCP},
			Security:  Security{Type: SecurityTLS, SNI: "nv.example.com", CertMode: CertModeACME, ACMEEmail: "me@example.com"}}},
	}
	for _, c := range cases {
		in := c.in
		if err := Normalize(c.kernel, &in); err != nil {
			t.Errorf("%s: Normalize: %v", c.name, err)
			continue
		}
		e := Endpoint{Name: c.name, Host: "203.0.113.10", Port: in.Port, Inbound: in, Direct: true}
		uri, ok := ShareURI(e)
		if !ok {
			t.Errorf("%s: ShareURI not ok", c.name)
			continue
		}
		nodes, errs := ParseImport(uri)
		if len(nodes) != 1 {
			t.Errorf("%s: roundtrip FAILED uri=%q nodes=%d errs=%v", c.name, uri, len(nodes), errs)
			continue
		}
		t.Logf("%s: OK %s -> type=%s", c.name, strings.SplitN(uri, "://", 2)[0]+"://", nodes[0].Proxy.Type())
	}
	// 第 15 形态：naive 中转形（host != SNI，链接带 ?sni=），生成的
	// naive+https:// 链接必须回解析为 naive 节点（B11-001 本体）。
	in := Inbound{Protocol: ProtocolNaive, Port: 443,
		Settings:  Settings{Username: "nv-user", Password: "nv-pass1"},
		Transport: Transport{Type: TransportTCP},
		Security:  Security{Type: SecurityTLS, SNI: "nv.example.com", CertMode: CertModeACME, ACMEEmail: "me@example.com"}}
	if err := Normalize(TypeSingbox, &in); err != nil {
		t.Fatalf("naive relay Normalize: %v", err)
	}
	uri, ok := ShareURI(Endpoint{Name: "naive-relay", Host: "198.51.100.7", Port: 31000, Inbound: in})
	if !ok {
		t.Fatalf("naive-relay: ShareURI not ok")
	}
	if !strings.HasPrefix(uri, "naive+https://") {
		t.Fatalf("naive-relay: expected naive+https:// share URI, got %q", uri)
	}
	nodes, errs := ParseImport(uri)
	if len(nodes) != 1 {
		t.Fatalf("naive-relay: roundtrip FAILED uri=%q nodes=%d errs=%v", uri, len(nodes), errs)
	}
	if got := nodes[0].Proxy.Type(); got != "naive" {
		t.Errorf("naive-relay: imported proxy type = %q, want naive", got)
	}
}
