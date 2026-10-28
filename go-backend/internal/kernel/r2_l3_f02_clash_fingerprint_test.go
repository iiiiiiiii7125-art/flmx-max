// R2-L3-F-02：Clash 入口 `fingerprint`（uTLS）未归一为 `client-fingerprint`。
// parseClashProfile 整条透传、无键映射，finishProxy 的 R10X-001 校验只读
// client-fingerprint：合法值 sing-box 面回落 chrome、分享链接丢 fp 参数、
// Xray 链式面回落默认指纹，仅 Clash 面靠原 map 透传存活；垃圾值 0 错误
// 建成、绕过 R10X-001 校验。本文件钉死归一语义：Clash 原生键 fingerprint
// （与非标准别名 fp）按类型归一进 client-fingerprint，原键删除；垃圾值
// 走 R10X-001 拒收；hysteria2 的 fingerprint 是证书 pin，须原样保留。
package kernel

import (
	"strings"
	"testing"
)

// r2l3f02Clash 拼一条单节点 Clash profile，extra 为追加的 flow-map 字段。
func r2l3f02Clash(typ, cred, extra string) string {
	return "proxies:\n  - {name: CF, type: " + typ + ", server: 203.0.113.1, port: 443, " + cred + extra + "}\n"
}

func TestR2L3F02ClashFingerprintNormalized(t *testing.T) {
	// uTLS fingerprint 的四类 Clash 类型：vless / vmess / trojan / anytls。
	cases := []struct {
		name string
		text string
	}{
		{"vless", r2l3f02Clash("vless", "uuid: "+manualUUID, ", tls: true, servername: a.example.com, fingerprint: firefox")},
		{"vmess", r2l3f02Clash("vmess", "uuid: "+manualUUID, ", tls: true, servername: a.example.com, fingerprint: firefox")},
		{"trojan", r2l3f02Clash("trojan", "password: pw", ", sni: a.example.com, fingerprint: firefox")},
		{"anytls", r2l3f02Clash("anytls", "password: pw", ", sni: a.example.com, fingerprint: firefox")},
	}
	for _, c := range cases {
		n := mustImportOne(t, c.text)
		if got := n.Proxy.str("client-fingerprint"); got != "firefox" {
			t.Errorf("%s: client-fingerprint = %q, want %q", c.name, got, "firefox")
		}
		if _, ok := n.Proxy["fingerprint"]; ok {
			t.Errorf("%s: 原键 fingerprint 须删除不再残留，got %v", c.name, n.Proxy["fingerprint"])
		}
		if _, ok := n.Proxy["fp"]; ok {
			t.Errorf("%s: 原键 fp 须删除不再残留，got %v", c.name, n.Proxy["fp"])
		}
	}
	// 大小写变体愈合落库（与 client-fingerprint 直给的 R10 口径一致）。
	n := mustImportOne(t, r2l3f02Clash("vless", "uuid: "+manualUUID, ", tls: true, servername: a.example.com, fingerprint: FireFox"))
	if got := n.Proxy.str("client-fingerprint"); got != "firefox" {
		t.Errorf("FireFox must heal to firefox, got %q", got)
	}
}

func TestR2L3F02ClashFingerprintGarbageRejected(t *testing.T) {
	for _, fp := range []string{"garbage-not-a-fp", "bogus123", "chrome-100"} {
		nodes, errs := ParseImport(r2l3f02Clash("vless", "uuid: "+manualUUID, ", tls: true, servername: a.example.com, fingerprint: "+fp))
		if len(nodes) != 0 || len(errs) != 1 || !strings.Contains(errs[0], "fingerprint") {
			t.Errorf("fingerprint %q: nodes=%d errs=%v, want 0 nodes + 1 err mentioning fingerprint", fp, len(nodes), errs)
		}
	}
	// 坏行不连坐同批其余行：同一 proxies 序列内坏行被拒、合法行照常建成。
	mixed := "proxies:\n" +
		"  - {name: BAD, type: vless, server: 203.0.113.1, port: 443, uuid: " + manualUUID + ", tls: true, servername: a.example.com, fingerprint: garbage-not-a-fp}\n" +
		"  - {name: GOOD, type: vless, server: 203.0.113.2, port: 443, uuid: " + manualUUID + ", tls: true, servername: a.example.com, fingerprint: firefox}\n"
	nodes, errs := ParseImport(mixed)
	if len(nodes) != 1 || len(errs) != 1 {
		t.Fatalf("mixed batch: nodes=%d errs=%v, want 1 node + 1 err", len(nodes), errs)
	}
	if got := nodes[0].Proxy.str("client-fingerprint"); got != "firefox" {
		t.Fatalf("mixed batch survivor client-fingerprint = %q, want firefox", got)
	}
}

func TestR2L3F02ClashFingerprintPriority(t *testing.T) {
	// client-fingerprint（面板规范键）已在位者为准，不被 fingerprint 覆盖。
	n := mustImportOne(t, r2l3f02Clash("vless", "uuid: "+manualUUID, ", tls: true, servername: a.example.com, client-fingerprint: safari, fingerprint: firefox"))
	if got := n.Proxy.str("client-fingerprint"); got != "safari" {
		t.Errorf("canonical client-fingerprint must win, got %q", got)
	}
	// fingerprint（Clash 原生）优先于 fp（非标准别名）。
	n = mustImportOne(t, r2l3f02Clash("vless", "uuid: "+manualUUID, ", tls: true, servername: a.example.com, fingerprint: firefox, fp: chrome"))
	if got := n.Proxy.str("client-fingerprint"); got != "firefox" {
		t.Errorf("native fingerprint must win over fp alias, got %q", got)
	}
	// 只有 fp 在位时也归一（末位兜底）。
	n = mustImportOne(t, r2l3f02Clash("vless", "uuid: "+manualUUID, ", tls: true, servername: a.example.com, fp: firefox"))
	if got := n.Proxy.str("client-fingerprint"); got != "firefox" {
		t.Errorf("fp alias must normalize to firefox, got %q", got)
	}
	if _, ok := n.Proxy["fp"]; ok {
		t.Errorf("fp key must not remain, got %v", n.Proxy["fp"])
	}
}

func TestR2L3F02ClashFingerprintPinKept(t *testing.T) {
	// 双义键的值形态区分：64 位十六进制是证书 pin（SHA-256），与 11 值
	// uTLS 集无交集——原样保留 fingerprint 键，不映射、不拒收。
	// 链式面 TestCheckChainRejections 的 trojan+pin 用例即此语义。
	pin := strings.Repeat("ab", 32)
	n := mustImportOne(t, r2l3f02Clash("trojan", "password: pw", ", sni: a.example.com, skip-cert-verify: true, fingerprint: "+pin))
	if got := n.Proxy.str("fingerprint"); got != pin {
		t.Fatalf("pin-shaped fingerprint must stay, got %q", got)
	}
	if got := n.Proxy.str("client-fingerprint"); got != "" {
		t.Fatalf("pin must not become client-fingerprint, got %q", got)
	}
	// pin 仍进链式面（Xray pinnedPeerCertSha256 的来源），跳过证书
	// 验证的 trojan 节点凭 pin 可进链。
	e := Endpoint{Name: n.Name, Host: n.Host, Port: n.Port, Direct: true, Proxy: n.Proxy}
	if err := CheckChain(TypeXray, e); err != nil {
		t.Fatalf("trojan+pin must chain via Xray: %v", err)
	}
	// 无 skip-cert-verify 时 pin 同样保留（仅暂无消费面，不构成拒收理由）。
	n = mustImportOne(t, r2l3f02Clash("trojan", "password: pw", ", sni: a.example.com, fingerprint: "+pin))
	if got := n.Proxy.str("fingerprint"); got != pin {
		t.Fatalf("pin-shaped fingerprint must stay without skip-cert-verify, got %q", got)
	}
}

func TestR2L3F02ClashFingerprintHy2PinPreserved(t *testing.T) {
	// hysteria2 的 fingerprint 是证书 pin（SHA-256），与内部 fingerprint
	// 键同义：须原样保留，不得映射为 client-fingerprint。
	pin := strings.Repeat("ab", 32)
	n := mustImportOne(t, r2l3f02Clash("hysteria2", "password: pw", ", sni: a.example.com, skip-cert-verify: true, fingerprint: "+pin))
	if got := n.Proxy.str("fingerprint"); got != pin {
		t.Fatalf("hy2 fingerprint pin must stay, got %q", got)
	}
	if got := n.Proxy.str("client-fingerprint"); got != "" {
		t.Fatalf("hy2 must not gain client-fingerprint, got %q", got)
	}
	// pin 的实际消费面是分享链接（proxy_produce.go:160 读 fingerprint
	// 出 pinSHA256）；Xray 链式面本身拒接 hy2（chainTargetFromProxy
	// default 分支），pin 分支（chain.go:443）只对 vless/vmess/trojan
	// 等可进链式的 TLS 类型可达，故此处不断言链式。
	e := Endpoint{Name: n.Name, Host: n.Host, Port: n.Port, Direct: true, Proxy: n.Proxy}
	uri, ok := proxyURI(e)
	if !ok {
		t.Fatal("proxyURI must succeed for the hy2 node")
	}
	if !strings.Contains(uri, "pinSHA256="+pin) {
		t.Fatalf("hy2 share link must carry pinSHA256=%s: %q", pin, uri)
	}
}

func TestR2L3F02ClashFingerprintConsumers(t *testing.T) {
	// 归一后三面消费须正确（审计原案的三面丢失逐面核对）。
	n := mustImportOne(t, r2l3f02Clash("vless", "uuid: "+manualUUID, ", tls: true, servername: a.example.com, fingerprint: firefox"))
	e := Endpoint{Name: n.Name, Host: n.Host, Port: n.Port, Direct: true, Proxy: n.Proxy}
	// sing-box 面：utls.fingerprint 不再回落 chrome。
	obs, ok := proxySingbox(e)
	if !ok {
		t.Fatal("proxySingbox must succeed for the normalized node")
	}
	tlsMap, _ := obs[0]["tls"].(map[string]interface{})
	utls, _ := tlsMap["utls"].(map[string]interface{})
	if utls["fingerprint"] != "firefox" {
		t.Fatalf("sing-box utls.fingerprint = %v, want firefox", utls["fingerprint"])
	}
	// 分享链接面：fp 参数须在位，再导入一轮仍在。
	uri, ok := proxyURI(e)
	if !ok {
		t.Fatal("proxyURI must succeed for the normalized node")
	}
	if !strings.Contains(uri, "fp=firefox") {
		t.Fatalf("share link must carry fp=firefox: %q", uri)
	}
	back := mustImportOne(t, uri)
	if got := back.Proxy.str("client-fingerprint"); got != "firefox" {
		t.Fatalf("round-trip client-fingerprint = %q, want firefox", got)
	}
	// Xray 链式面：Fingerprint 不再回落默认 chrome。
	ct, err := chainTargetFromProxy(e)
	if err != nil {
		t.Fatalf("chainTargetFromProxy: %v", err)
	}
	if ct.Fingerprint != "firefox" {
		t.Fatalf("chain Fingerprint = %q, want firefox", ct.Fingerprint)
	}
}

func TestR2L3F02ClashFingerprintRegression(t *testing.T) {
	// client-fingerprint 直给的既有行为不变（R10 口径：大写愈合、垃圾拒）。
	n := mustImportOne(t, r2l3f02Clash("vless", "uuid: "+manualUUID, ", tls: true, servername: a.example.com, client-fingerprint: Chrome"))
	if got := n.Proxy.str("client-fingerprint"); got != "chrome" {
		t.Fatalf("direct client-fingerprint Chrome must heal to chrome, got %q", got)
	}
	_, errs := ParseImport(r2l3f02Clash("vless", "uuid: "+manualUUID, ", tls: true, client-fingerprint: bogus123"))
	if len(errs) != 1 || !strings.Contains(errs[0], "fingerprint") {
		t.Fatalf("direct garbage client-fingerprint must be rejected, errs=%v", errs)
	}
	// 链接面 fp 既有行为不变。
	n = mustImportOne(t, "vless://"+manualUUID+"@203.0.113.3:443?encryption=none&security=tls&sni=a.example.com&fp=Safari&type=tcp#FP")
	if got := n.Proxy.str("client-fingerprint"); got != "safari" {
		t.Fatalf("link fp=Safari must heal to safari, got %q", got)
	}
	// 无 fingerprint 的旧 Clash 行行为逐字不变：不新增 client-fingerprint 键。
	n = mustImportOne(t, r2l3f02Clash("vless", "uuid: "+manualUUID, ", tls: true, servername: a.example.com"))
	if _, ok := n.Proxy["client-fingerprint"]; ok {
		t.Errorf("legacy Clash row must not gain client-fingerprint: %v", n.Proxy)
	}
}
