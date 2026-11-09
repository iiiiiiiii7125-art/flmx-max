package kernel

import (
	"encoding/base64"
	"strings"
	"testing"
)

// W3 修复波（2.1.2 审计路 3）契约测试：导入解析全集的静默吞没/误拒/
// 字段丢弃逐条钉住。修前行为的复现探针见审计 F3-IMP 报告，本文件是
// 修复后的回归门——任一用例转红即对应条目复发。

// I-002：base64 包裹的 Clash / sing-box 订阅必须被识别（旧实现解码后
// 只认含 "://" 的文本，两格式整单 0 节点且逐行误报）。
func TestW3Base64WrappedProfilesParse(t *testing.T) {
	clash := "proxies:\n  - {name: C1, type: ss, server: 1.2.3.4, port: 8388, cipher: aes-256-gcm, password: pw}\n"
	nodes, errs := ParseImport(base64.StdEncoding.EncodeToString([]byte(clash)))
	if len(nodes) != 1 || len(errs) != 0 {
		t.Fatalf("base64 clash: nodes=%v errs=%v", nodes, errs)
	}
	singbox := `{"outbounds":[{"type":"trojan","tag":"S1","server":"5.6.7.8","server_port":443,"password":"pw"}]}`
	nodes, errs = ParseImport(base64.StdEncoding.EncodeToString([]byte(singbox)))
	if len(nodes) != 1 || len(errs) != 0 {
		t.Fatalf("base64 sing-box: nodes=%v errs=%v", nodes, errs)
	}
}

// I-001：sing-box endpoints 不再静默吞没——逐条点名回执，outbounds
// 照常导入；只有 endpoints 的配置不误报「没有 outbounds」。
func TestW3SingboxEndpointsNamedReceipt(t *testing.T) {
	body := `{"outbounds":[{"type":"trojan","tag":"S1","server":"5.6.7.8","server_port":443,"password":"pw"}],
		"endpoints":[{"type":"wireguard","tag":"wg-home","address":["10.0.0.2/32"],"private_key":"abc"}]}`
	nodes, errs := ParseImport(body)
	if len(nodes) != 1 {
		t.Fatalf("outbound must still import: %v", nodes)
	}
	if len(errs) != 1 || !strings.Contains(errs[0], "wg-home") || !strings.Contains(errs[0], "wireguard") {
		t.Fatalf("endpoint must be named in the receipt: %v", errs)
	}
	_, errs = ParseImport(`{"endpoints":[{"type":"wireguard","address":["10.0.0.2/32"]}]}`)
	if len(errs) != 1 || !strings.Contains(errs[0], "第 1 个端点") || strings.Contains(errs[0], "没有 outbounds") {
		t.Fatalf("endpoints-only profile: %v", errs)
	}
}

// I-011：sing-box 无 tag 条目失败时回执回落「第 N 个节点」，不留空名。
func TestW3SingboxUntaggedErrorNamesPosition(t *testing.T) {
	_, errs := ParseImport(`{"outbounds":[{"type":"vless","server":"5.6.7.8","server_port":443}]}`)
	if len(errs) != 1 || !strings.HasPrefix(errs[0], "第 1 个节点：") {
		t.Fatalf("want positional fallback name, got %v", errs)
	}
}

// I-006：尾点 FQDN 导入面规范化（剥一个根点、落库规范形）；双尾点
// 仍拒；链接面同口径。
func TestW3TrailingDotHostCanonicalized(t *testing.T) {
	n := mustImportOne(t, "trojan://pw@node.example.com.:443#T")
	if n.Host != "node.example.com" || n.Proxy.str("server") != "node.example.com" {
		t.Fatalf("link face: host=%q server=%q", n.Host, n.Proxy.str("server"))
	}
	nodes, errs := ParseImport("proxies:\n  - {name: C1, type: ss, server: cdn.example.com., port: 8388, cipher: aes-256-gcm, password: pw}\n")
	if len(nodes) != 1 || len(errs) != 0 || nodes[0].Host != "cdn.example.com" {
		t.Fatalf("clash face: nodes=%v errs=%v", nodes, errs)
	}
	if _, errs := ParseImport("trojan://pw@node.example.com..:443#T"); len(errs) == 0 {
		t.Fatal("double trailing dot must stay rejected")
	}
}

// I-007：Hysteria2 带宽/hop 三进口（sing-box 字段、分享链接）存入且
// sing-box 面回写 up_mbps/down_mbps/hop_interval。
func TestW3Hy2BandwidthAndHopRoundTrip(t *testing.T) {
	body := `{"outbounds":[{"type":"hysteria2","tag":"H","server":"5.6.7.8","server_port":443,"password":"pw","up_mbps":50,"down_mbps":200,"hop_interval":"30s"}]}`
	n := mustImportProfile(t, body)
	if n.Proxy.str("up") != "50" || n.Proxy.str("down") != "200" || n.Proxy.num("hop-interval") != 30 {
		t.Fatalf("stored: %v", n.Proxy)
	}
	obs, ok := proxySingbox(Endpoint{Name: "H", Host: "5.6.7.8", Port: 443, Proxy: n.Proxy, Direct: true})
	if !ok || obs[0]["up_mbps"] != 50 || obs[0]["down_mbps"] != 200 || obs[0]["hop_interval"] != "30s" {
		t.Fatalf("sing-box render: %v", obs)
	}
	n = mustImportOne(t, "hysteria2://pw@5.6.7.8:443/?up=80Mbps&down=120#H")
	if n.Proxy.str("up") != "80Mbps" || n.Proxy.str("down") != "120" {
		t.Fatalf("link face stored: %v", n.Proxy)
	}
	obs, ok = proxySingbox(Endpoint{Name: "H", Host: "5.6.7.8", Port: 443, Proxy: n.Proxy, Direct: true})
	if !ok || obs[0]["up_mbps"] != 80 || obs[0]["down_mbps"] != 120 {
		t.Fatalf("link->sing-box render: %v", obs)
	}
	uri, ok := proxyURI(Endpoint{Name: "H", Host: "5.6.7.8", Port: 443, Proxy: n.Proxy, Direct: true})
	if !ok || !strings.Contains(uri, "up=80") || !strings.Contains(uri, "down=120") {
		t.Fatalf("uri replay: %s", uri)
	}
}

// I-008：TUIC heartbeat/udp_over_stream、VLESS packet_encoding、
// sing-box socks network 的进口与回写。
func TestW3TuicAndVlessFieldsRoundTrip(t *testing.T) {
	body := `{"outbounds":[{"type":"tuic","tag":"T","server":"5.6.7.8","server_port":443,"uuid":"9d3b0c6a-0000-4000-8000-000000000001","password":"pw","heartbeat":"10s","udp_over_stream":true,"tls":{"enabled":true}}]}`
	n := mustImportProfile(t, body)
	if n.Proxy.num("heartbeat-interval") != 10000 || !n.Proxy.flag("udp_over_stream") {
		t.Fatalf("tuic stored: %v", n.Proxy)
	}
	obs, ok := proxySingbox(Endpoint{Name: "T", Host: "5.6.7.8", Port: 443, Proxy: n.Proxy, Direct: true})
	if !ok || obs[0]["heartbeat"] != "10s" || obs[0]["udp_over_stream"] != true {
		t.Fatalf("tuic sing-box render: %v", obs)
	}
	n = mustImportOne(t, "tuic://9d3b0c6a-0000-4000-8000-000000000001:pw@5.6.7.8:443?heartbeat_interval=5000&udp_over_stream=true#T")
	if n.Proxy.num("heartbeat-interval") != 5000 || !n.Proxy.flag("udp_over_stream") {
		t.Fatalf("tuic link stored: %v", n.Proxy)
	}
	if _, errs := ParseImport("tuic://9d3b0c6a-0000-4000-8000-000000000001:pw@5.6.7.8:443?heartbeat_interval=abc#T"); len(errs) == 0 || !strings.Contains(errs[0], "heartbeat_interval") {
		t.Fatalf("bad heartbeat must be named, got %v", errs)
	}

	body = `{"outbounds":[{"type":"vless","tag":"V","server":"5.6.7.8","server_port":443,"uuid":"9d3b0c6a-0000-4000-8000-000000000001","packet_encoding":"xudp"}]}`
	n = mustImportProfile(t, body)
	if n.Proxy.str("packet-encoding") != "xudp" {
		t.Fatalf("vless stored: %v", n.Proxy)
	}
	obs, ok = proxySingbox(Endpoint{Name: "V", Host: "5.6.7.8", Port: 443, Proxy: n.Proxy, Direct: true})
	if !ok || obs[0]["packet_encoding"] != "xudp" {
		t.Fatalf("vless sing-box render: %v", obs)
	}
	n = mustImportOne(t, "vless://9d3b0c6a-0000-4000-8000-000000000001@5.6.7.8:443?packetEncoding=packetaddr#V")
	if n.Proxy.str("packet-encoding") != "packetaddr" {
		t.Fatalf("vless link stored: %v", n.Proxy)
	}
	uri, ok := proxyURI(Endpoint{Name: "V", Host: "5.6.7.8", Port: 443, Proxy: n.Proxy, Direct: true})
	if !ok || !strings.Contains(uri, "packetEncoding=packetaddr") {
		t.Fatalf("vless uri replay: %s", uri)
	}

	body = `{"outbounds":[{"type":"socks","tag":"S","server":"5.6.7.8","server_port":1080,"network":"tcp"}]}`
	n = mustImportProfile(t, body)
	if n.Proxy.flag("udp") {
		t.Fatalf("tcp-only socks must store udp=false: %v", n.Proxy)
	}
}

// I-009：Surge 行名含 "=" 时按类型集定位真分隔符，名字完整保留。
func TestW3SurgeNameContainingEquals(t *testing.T) {
	n := mustImportOne(t, "A=B 港线 = ss, 1.2.3.4, 8388, encrypt-method=aes-256-gcm, password=pw")
	if n.Name != "A=B 港线" || n.Proxy.Type() != "ss" || n.Port != 8388 {
		t.Fatalf("name=%q proxy=%v", n.Name, n.Proxy)
	}
}

// I-010：SOCKS5/HTTP 凭据必须成对；双空与双全都合法。
func TestW3SocksHTTPCredentialPairing(t *testing.T) {
	if _, errs := ParseImport("socks5://useronly@1.2.3.4:1080#S"); len(errs) == 0 || !strings.Contains(errs[0], "成对") {
		t.Fatalf("username-only socks must be rejected naming the pairing rule, got %v", errs)
	}
	if _, errs := ParseImport("proxies:\n  - {name: H, type: http, server: 1.2.3.4, port: 8080, password: pw}\n"); len(errs) == 0 || !strings.Contains(errs[0], "成对") {
		t.Fatalf("password-only http must be rejected, got %v", errs)
	}
	mustImportOne(t, "socks5://1.2.3.4:1080#S")
	mustImportOne(t, "socks5://user:pass@1.2.3.4:1080#S")
}

// I-012：http(s) 行的错误文案区分订阅地址与 HTTP 代理节点两种读法。
func TestW3HTTPLinkMessage(t *testing.T) {
	_, errs := ParseImport("ss://YWVzLTI1Ni1nY206cHc@1.2.3.4:8388#a\nhttp://1.2.3.4:8080")
	if len(errs) != 1 || !strings.Contains(errs[0], "不是单个节点链接") || !strings.Contains(errs[0], "手动填写") {
		t.Fatalf("want the disambiguating message, got %v", errs)
	}
}

// P-001：quic-only 协议缺省传输补 quic（不再恒填 tcp 后报误导错误）；
// 有 tcp 的协议缺省仍为 tcp。ComboSupported 同口径。
func TestW3NormalizeDefaultTransportQuicOnly(t *testing.T) {
	in := Inbound{Protocol: ProtocolHysteria2, Port: 443, Settings: Settings{Password: "pw"}}
	if err := Normalize(TypeSingbox, &in); err != nil {
		t.Fatalf("hy2 with omitted transport must normalize, got %v", err)
	}
	if in.Transport.Type != TransportQUIC {
		t.Fatalf("hy2 default transport = %q, want quic", in.Transport.Type)
	}
	in = Inbound{Protocol: ProtocolShadowsocks, Port: 8388, Settings: Settings{Method: "aes-256-gcm", Password: "pw"}}
	if err := Normalize(TypeXray, &in); err != nil {
		t.Fatalf("ss normalize: %v", err)
	}
	if in.Transport.Type != TransportTCP {
		t.Fatalf("ss default transport = %q, want tcp", in.Transport.Type)
	}
	if !ComboSupported(TypeSingbox, ProtocolHysteria2, "", "tls") {
		t.Fatal("ComboSupported must apply the same quic default for an empty transport")
	}
}

// P-002：下发复检面空安全层不豁免（trojan 空安全层会被渲染成无 TLS）。
func TestW3ComboSupportedEmptySecurityRejected(t *testing.T) {
	if ComboSupported(TypeXray, ProtocolTrojan, "tcp", "") {
		t.Fatal("empty security must not pass the apply-time recheck")
	}
	if !ComboSupported(TypeXray, ProtocolTrojan, "tcp", "tls") {
		t.Fatal("trojan+tcp+tls must stay supported")
	}
	if !ComboSupported(TypeXray, ProtocolVLESS, "tcp", "none") {
		t.Fatal("an explicit 'none' security in the catalog must stay supported")
	}
}

// T-003：AcceptProxyProtocol×非 Xray 进下发复检（与创建面同谓词）。
func TestW3CheckSettingsConditionsAcceptProxy(t *testing.T) {
	in := &Inbound{Protocol: ProtocolVLESS, AcceptProxyProtocol: true}
	if err := CheckSettingsConditions(TypeSingbox, in); err == nil || !strings.Contains(err.Error(), "只支持 Xray") {
		t.Fatalf("sing-box + AcceptProxy must be rejected at recheck, got %v", err)
	}
	if err := CheckSettingsConditions(TypeXray, in); err != nil {
		t.Fatalf("xray + AcceptProxy must pass, got %v", err)
	}
}

// P-003：SubscriptionFormatSkipped 与 SubscriptionNodeCount 同源同口径
// ——逐格式 skipped 数 + count 数 = 管线端点总数；且确定格点名
// （naive 无 Clash 表达，必在 clash 的 skipped 名单内）。
func TestW3FormatSkippedMatchesCount(t *testing.T) {
	naive := mustImportOne(t, "naive://user:pass@1.2.3.4:443#N")
	ss := mustImportOne(t, "ss://YWVzLTI1Ni1nY206cHc@1.2.3.4:8388#a")
	eps := []Endpoint{
		{Name: "naive-node", Host: naive.Host, Port: naive.Port, Proxy: naive.Proxy, Direct: true},
		{Name: "ss-node", Host: ss.Host, Port: ss.Port, Proxy: ss.Proxy, Direct: true},
	}
	for _, format := range SubscriptionFormats() {
		skipped := SubscriptionFormatSkipped(format, eps, SubOptions{})
		count := SubscriptionNodeCount(format, eps, SubOptions{})
		if len(skipped)+count != len(eps) {
			t.Fatalf("%s: skipped %v + count %d != %d endpoints", format, skipped, count, len(eps))
		}
	}
	skipped := SubscriptionFormatSkipped(FormatClash, eps, SubOptions{})
	if len(skipped) != 1 || skipped[0] != "naive-node" {
		t.Fatalf("clash skipped = %v, want [naive-node]", skipped)
	}
}

// mustImportProfile imports a whole profile body that must yield
// exactly one node.
func mustImportProfile(t *testing.T, body string) ImportedNode {
	t.Helper()
	nodes, errs := ParseImport(body)
	if len(nodes) != 1 || len(errs) != 0 {
		t.Fatalf("import profile: nodes=%v errs=%v", nodes, errs)
	}
	return nodes[0]
}
