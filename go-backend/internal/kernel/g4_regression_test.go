package kernel

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// G4 (1.3.1) 回归组：
//   - B1（A1 复审）：自建 Snell 入站在 Clash/Mihomo 输出被静默丢弃；
//   - 同类口径：导入 Snell 节点在 proxyClash 的 v4/v5 门控、proxySingbox
//     的缺失分支与 shadow-tls 键的静默丢弃；
//   - BUG-A4-2：导入对 vless/vmess/tuic 的 UUID 只查非空不查格式。
// 每个用例在修复前都应 FAIL（Snell 渲染类）或收下坏节点（UUID 类）。

func g4SnellEndpoint(t *testing.T, version int, shadowTLS bool) Endpoint {
	t.Helper()
	in := Inbound{ID: 91, Protocol: ProtocolSnell, Port: 6160,
		Settings: Settings{Password: "psk-g4-test-123456", SnellVersion: version}}
	if shadowTLS {
		in.Security = Security{Type: SecurityShadowTLS, SNI: "www.microsoft.com",
			ShadowTLSPassword: "stls-pass-g4", Fingerprint: "chrome"}
	}
	if err := Normalize(TypeSingbox, &in); err != nil {
		t.Fatalf("normalize snell v%d: %v", version, err)
	}
	return Endpoint{Name: "G4 Snell", Host: "snell.example.com", Port: 6160, Inbound: in}
}

// B1 黄金断言：Snell v5 入站（面板默认版本）必须出现在 Clash 输出中，
// 参数与 sing-box 渲染同口径（version 4、psk 一致、v5 带 UDP）。
func TestG4ClashProxyRendersSnell(t *testing.T) {
	node, ok := ClashProxy(g4SnellEndpoint(t, 5, false))
	if !ok {
		t.Fatal("B1: snell v5 inbound must render for clash/mihomo")
	}
	m := yamlToMap(node)
	if m["type"] != "snell" || m["psk"] != "psk-g4-test-123456" || m["version"] != 4 || m["udp"] != true {
		t.Fatalf("snell clash node fields: %v", m)
	}
	out := marshalYAML(node)
	for _, want := range []string{`type: "snell"`, "psk-g4-test-123456", "version: 4", "udp: true"} {
		if !strings.Contains(out, want) {
			t.Fatalf("clash snell yaml missing %q:\n%s", want, out)
		}
	}

	// v4：同样渲染为 version 4，但 v4 无 UDP（与入站 Network() 口径一致）。
	node, ok = ClashProxy(g4SnellEndpoint(t, 4, false))
	if !ok {
		t.Fatal("snell v4 inbound must render for clash/mihomo")
	}
	if m = yamlToMap(node); m["version"] != 4 || m["udp"] != false {
		t.Fatalf("snell v4 clash node: %v", m)
	}

	// v6：Mihomo 的 NewSnell 直接报 "snell version error"、且无 mode
	// 字段可放——有据不支持，保持不渲染（而非渲染出坏节点）。
	if _, ok = ClashProxy(g4SnellEndpoint(t, 6, false)); ok {
		t.Fatal("snell v6 has no mihomo client support and must stay out")
	}

	// ShadowTLS 前置：obfs-opts 承载 shadowtls 模式，UDP 关闭。
	node, ok = ClashProxy(g4SnellEndpoint(t, 5, true))
	if !ok {
		t.Fatal("snell over shadowtls must render for clash/mihomo")
	}
	m = yamlToMap(node)
	if m["udp"] != false || m["client-fingerprint"] != "chrome" {
		t.Fatalf("snell+shadowtls clash node: %v", m)
	}
	oo, _ := m["obfs-opts"].(map[string]interface{})
	if oo["mode"] != "shadowtls" || oo["host"] != "www.microsoft.com" || oo["password"] != "stls-pass-g4" || oo["version"] != 3 {
		t.Fatalf("snell+shadowtls obfs-opts: %v", m)
	}
}

// B1 端到端：整份 Clash 订阅里 Snell 节点在 proxies 与分组中同时出现；
// Shadowrocket（消费 Clash 结构）同样拿得到。
func TestG4SubscriptionClashCarriesSnell(t *testing.T) {
	eps := []Endpoint{g4SnellEndpoint(t, 5, false)}
	body, _, err := Subscription(FormatClash, eps, SubOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, `type: "snell"`) || !strings.Contains(body, "G4 Snell") {
		t.Fatalf("clash subscription must carry the snell node:\n%s", body)
	}
	sr, _, err := Subscription(FormatShadowrocket, eps, SubOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sr, "snell") {
		t.Fatalf("shadowrocket profile must carry the snell node:\n%s", sr)
	}
}

// 导入 Snell 节点的 Clash 口径：v5 归一到 4、v3 原样、v6 不渲染；
// "shadow-tls" 面板键须转成 obfs-opts 而非静默丢弃。
func TestG4ProxyClashSnell(t *testing.T) {
	mk := func(p Proxy) Endpoint {
		return Endpoint{Name: "imp", Host: "imp.example.com", Port: 8388, Proxy: p}
	}
	node, ok := ClashProxy(mk(Proxy{"type": "snell", "psk": "abc", "version": 5}))
	if !ok {
		t.Fatal("imported snell v5 must render for clash")
	}
	if m := yamlToMap(node); m["version"] != 4 || m["psk"] != "abc" {
		t.Fatalf("imported snell v5 must normalize to version 4: %v", m)
	}
	node, ok = ClashProxy(mk(Proxy{"type": "snell", "psk": "abc", "version": 3}))
	if !ok {
		t.Fatal("imported snell v3 must render for clash")
	}
	if m := yamlToMap(node); Proxy(m).num("version") != 3 {
		t.Fatalf("imported snell v3 keeps its version: %v", m)
	}
	if _, ok = ClashProxy(mk(Proxy{"type": "snell", "psk": "abc", "version": 6})); ok {
		t.Fatal("imported snell v6 must stay out of clash output")
	}
	node, ok = ClashProxy(mk(Proxy{"type": "snell", "psk": "abc", "version": 4,
		"shadow-tls": map[string]interface{}{"host": "sni.example.com", "password": "stls-pw", "version": 3}}))
	if !ok {
		t.Fatal("imported snell over shadowtls must render for clash")
	}
	m := yamlToMap(node)
	oo, _ := m["obfs-opts"].(map[string]interface{})
	if oo["mode"] != "shadowtls" || oo["host"] != "sni.example.com" || oo["password"] != "stls-pw" || oo["version"] != 3 {
		t.Fatalf("shadow-tls key must convert to obfs-opts: %v", m)
	}
	if _, leaked := m["shadow-tls"]; leaked {
		t.Fatalf("panel-only shadow-tls key must not leak into clash output: %v", m)
	}
}

// 导入 Snell 节点的 sing-box 口径：受与自建入站相同的 UA 门控
// （<1.14 不下发）；渲染参数与 singboxOutbound 同口径；带 obfs 的
// 节点 sing-box 无法表达，保持不渲染；ShadowTLS 走 detour 双出站。
func TestG4ProxySingboxSnell(t *testing.T) {
	mk := func(p Proxy) Endpoint {
		return Endpoint{Name: "imp", Host: "imp.example.com", Port: 8388, Proxy: p}
	}
	e := mk(Proxy{"type": "snell", "psk": "abc", "version": 5})
	if _, ok := SingboxOutbounds(e); ok {
		t.Fatal("imported snell must stay gated for sing-box < 1.14")
	}
	outs, ok := singboxOutbounds(e, true)
	if !ok || len(outs) != 1 {
		t.Fatalf("imported snell must render for sing-box 1.14+: %v %v", outs, ok)
	}
	if outs[0]["type"] != "snell" || outs[0]["version"] != 4 || outs[0]["psk"] != "abc" {
		t.Fatalf("imported snell sing-box outbound: %v", outs[0])
	}
	obfs := mk(Proxy{"type": "snell", "psk": "abc", "version": 3,
		"obfs-opts": map[string]interface{}{"mode": "http", "host": "obfs.example.com"}})
	if _, ok := singboxOutbounds(obfs, true); ok {
		t.Fatal("sing-box cannot express snell obfs; node must stay out")
	}
	stls := mk(Proxy{"type": "snell", "psk": "abc", "version": 4,
		"shadow-tls": map[string]interface{}{"host": "sni.example.com", "password": "stls-pw", "version": 3}})
	outs, ok = singboxOutbounds(stls, true)
	if !ok || len(outs) != 2 {
		t.Fatalf("snell over shadowtls needs the detour pair: %v %v", outs, ok)
	}
	if outs[0]["detour"] != "imp (shadowtls)" || outs[1]["type"] != "shadowtls" || outs[1]["password"] != "stls-pw" {
		t.Fatalf("snell shadowtls pair: %v", outs)
	}
	if _, has := outs[0]["server"]; has {
		t.Fatalf("inner snell outbound must not carry the server: %v", outs[0])
	}
}

// BUG-A4-2：非法 UUID 走逐条错误通道（带行号、不整批拒绝），
// 合法 UUID（含大写）照常导入；Clash 档案路径同口径。
func TestG4ParseImportRejectsBadUUID(t *testing.T) {
	vmessBad, _ := json.Marshal(map[string]string{
		"v": "2", "ps": "bad", "add": "203.0.113.10", "port": "443", "id": "not-a-uuid",
		"aid": "0", "scy": "auto", "net": "tcp", "type": "none", "host": "", "path": "", "tls": "",
	})
	bads := map[string]string{
		"vless": "vless://not-a-uuid@203.0.113.10:443?security=none#bad",
		"tuic":  "tuic://not-a-uuid:somepass@203.0.113.10:443#bad",
		"vmess": "vmess://" + base64.StdEncoding.EncodeToString(vmessBad),
	}
	for kind, line := range bads {
		nodes, errs := ParseImport(line)
		if len(nodes) != 0 || len(errs) != 1 {
			t.Fatalf("%s: bad uuid must be rejected per-line, got nodes=%v errs=%v", kind, nodes, errs)
		}
		if !strings.Contains(errs[0], "UUID 格式错误") || !strings.Contains(errs[0], "第 1 行") {
			t.Fatalf("%s: error must name the line and the cause: %v", kind, errs)
		}
	}

	good := "vless://11111111-2222-3333-4444-555555555555@203.0.113.10:443?security=none#good"
	nodes, errs := ParseImport(bads["vless"] + "\n" + good)
	if len(nodes) != 1 || len(errs) != 1 {
		t.Fatalf("a bad line must not sink the batch: nodes=%v errs=%v", nodes, errs)
	}
	nodes, errs = ParseImport("vless://AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE@203.0.113.10:443?security=none#upper")
	if len(nodes) != 1 || len(errs) != 0 {
		t.Fatalf("uppercase uuid must import: nodes=%v errs=%v", nodes, errs)
	}

	clash := "proxies:\n  - {name: bad, type: vless, server: 203.0.113.10, port: 443, uuid: not-a-uuid}\n"
	nodes, errs = ParseImport(clash)
	if len(nodes) != 0 || len(errs) == 0 || !strings.Contains(strings.Join(errs, " "), "UUID 格式错误") {
		t.Fatalf("clash profile bad uuid must be rejected: nodes=%v errs=%v", nodes, errs)
	}
}
