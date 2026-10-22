package kernel

import (
	"testing"
)

// X-P3-11（L-02-1）：导入 Snell 的 udp 此前透传源值——sing-box 来源恒
// udp:true（proxy_parse 恒置）、Clash 来源缺键时整键缺失；自建入站
// clashSnell 恒按 Network() 语义显式输出（v4/ShadowTLS 为 tcp-only →
// false，v5 非 ShadowTLS → true）。proxyClash 须同口径合成、不透传。

func clashOfImported(t *testing.T, src string) map[string]interface{} {
	t.Helper()
	nodes, errs := ParseImport(src)
	if len(errs) != 0 || len(nodes) != 1 {
		t.Fatalf("parse: nodes=%v errs=%v", nodes, errs)
	}
	node, ok := ClashProxy(Endpoint{Name: nodes[0].Name, Host: nodes[0].Host, Port: nodes[0].Port, Proxy: nodes[0].Proxy})
	if !ok {
		t.Fatal("imported snell must render for clash")
	}
	return yamlToMap(node)
}

func TestR1B7ImportedSnellUDPSynthesized(t *testing.T) {
	// sing-box 来源 v4 + ShadowTLS：udp 必须为 false（源恒置 true）。
	m := clashOfImported(t, `{"outbounds":[
		{"type":"snell","tag":"stls4","server_port":0,"version":4,"psk":"psk-aaaa","detour":"stls4-d"},
		{"type":"shadowtls","tag":"stls4-d","server":"1.2.3.4","server_port":6160,"version":3,"password":"stls-pw","tls":{"enabled":true,"server_name":"www.bing.com"}}
	]}`)
	if got, present := m["udp"]; !present || got != false {
		t.Fatalf("v4+shadowtls snell udp = %v (present=%v), want explicit false: %v", got, present, m)
	}
	if oo, _ := m["obfs-opts"].(map[string]interface{}); oo["mode"] != "shadowtls" {
		t.Fatalf("shadowtls must still convert to obfs-opts: %v", m)
	}

	// 普通 v5（sing-box 来源）：udp=true（渲染版本归一为 4，UDP 语义按源版本）。
	m = clashOfImported(t, `{"outbounds":[
		{"type":"snell","tag":"plain5","server":"1.2.3.4","server_port":6160,"version":5,"psk":"psk-bbbb"}
	]}`)
	if got := m["udp"]; got != true {
		t.Fatalf("plain v5 snell udp = %v, want true: %v", got, m)
	}
	if got := m["version"]; got != 4 {
		t.Fatalf("plain v5 snell version = %v, want normalized 4: %v", got, m)
	}

	// 普通 v4（sing-box 来源，源恒置 udp:true）：与自建 v4 同口径 udp=false。
	m = clashOfImported(t, `{"outbounds":[
		{"type":"snell","tag":"plain4","server":"1.2.3.4","server_port":6160,"version":4,"psk":"psk-cccc"}
	]}`)
	if got, present := m["udp"]; !present || got != false {
		t.Fatalf("plain v4 snell udp = %v (present=%v), want explicit false: %v", got, present, m)
	}

	// Clash 来源缺 udp 键：显式输出布尔（v4 → false），不再整键缺失。
	m = clashOfImported(t, "proxies:\n  - {name: SN, type: snell, server: 1.2.3.4, port: 6160, psk: abc, version: 4}\n")
	if got, present := m["udp"]; !present || got != false {
		t.Fatalf("clash-source snell without udp key: udp = %v (present=%v), want explicit false: %v", got, present, m)
	}
	// Clash 来源即使带 udp:true 也不透传（v4 语义优先于源值）。
	m = clashOfImported(t, "proxies:\n  - {name: SN, type: snell, server: 1.2.3.4, port: 6160, psk: abc, version: 4, udp: true}\n")
	if got := m["udp"]; got != false {
		t.Fatalf("clash-source v4 snell udp:true must not pass through, got %v: %v", got, m)
	}
}
