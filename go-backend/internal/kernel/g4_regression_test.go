package kernel

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// G4 (1.3.1) 回归组：
//   - 导入 Snell 节点在 proxyClash 的 v4/v5 门控、proxySingbox
//     的缺失分支与 shadow-tls 键的静默丢弃；
//   - BUG-A4-2：导入对 vless/vmess/tuic 的 UUID 只查非空不查格式。
// 每个用例在修复前都应 FAIL（Snell 渲染类）或收下坏节点（UUID 类）。

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
	if oo["mode"] != "shadow-tls" || oo["host"] != "sni.example.com" || oo["password"] != "stls-pw" || oo["version"] != 3 {
		t.Fatalf("shadow-tls key must convert to obfs-opts: %v", m)
	}
	if _, leaked := m["shadow-tls"]; leaked {
		t.Fatalf("panel-only shadow-tls key must not leak into clash output: %v", m)
	}
}

// 导入 Snell 节点的 sing-box 口径：受 UA 门控
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
