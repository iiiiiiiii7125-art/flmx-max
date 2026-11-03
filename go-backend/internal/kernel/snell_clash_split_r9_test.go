package kernel

import (
	"strings"
	"testing"
)

// R9-01（LEDGER9 §5.1）：渲染按格式分流——clash 格式停止下发自建
// Snell v4/v5（与 v6 同口径：官方 Mihomo 1.19.16/1.19.17 实测 v4 条目
// 整份拒绝、仅 v3 可过、shadow-tls obfs 同拒）；stash/shadowrocket
// 保留现渲染。本文件钉住分流矩阵：内置渲染、计数与模板三条路径同口径。
//
// 范围边界的后续（R10-01，LEDGER10 §2，本文件的 R10 钉格）：导入
// Snell 节点自修复轮 10 起同接接受域门——clash 格式按 Mihomo 实测
// 接受集分流、v1/v2 改出 udp:false 保节点；stash/shadowrocket 仍走
// proxyClash 既有口径（R2X-002 矩阵）不动。sing-box 面 obfs-opts
// 形态的 Snell+ShadowTLS 同批转换对齐（I-F4-02）。

func r9SnellMixedEndpoints(t *testing.T) []Endpoint {
	t.Helper()
	vless := Inbound{ID: 92, Protocol: ProtocolVLESS, Port: 8443,
		Settings: Settings{UUID: testUUID}, Security: Security{Type: SecurityNone}}
	if err := Normalize(TypeSingbox, &vless); err != nil {
		t.Fatalf("normalize vless: %v", err)
	}
	return []Endpoint{
		g4SnellEndpoint(t, 5, false),
		{Name: "R9 VLESS", Host: "vless.example.com", Port: 8443, Inbound: vless},
	}
}

func TestR9SnellClashFormatSplit(t *testing.T) {
	for _, tc := range []struct {
		name     string
		endpoint Endpoint
		wantKept bool // stash / shadowrocket 是否保留该条目
	}{
		{"v4", g4SnellEndpoint(t, 4, false), true},
		{"v5", g4SnellEndpoint(t, 5, false), true},
		{"v5-shadowtls", g4SnellEndpoint(t, 5, true), true},
		{"v6", g4SnellEndpoint(t, 6, false), false},
	} {
		eps := []Endpoint{tc.endpoint}

		clash, _, err := Subscription(FormatClash, eps, SubOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(clash, "snell") || strings.Contains(clash, "G4 Snell") {
			t.Fatalf("%s: clash format must not carry built snell:\n%s", tc.name, clash)
		}
		if got := SubscriptionNodeCount(FormatClash, eps, SubOptions{}); got != 0 {
			t.Fatalf("%s: clash count = %d, want 0", tc.name, got)
		}

		for _, format := range []string{FormatStash, FormatShadowrocket} {
			body, _, err := Subscription(format, eps, SubOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if kept := strings.Contains(body, "snell"); kept != tc.wantKept {
				t.Fatalf("%s: %s snell kept = %v, want %v:\n%s", tc.name, format, kept, tc.wantKept, body)
			}
			want := 0
			if tc.wantKept {
				want = 1
			}
			if got := SubscriptionNodeCount(format, eps, SubOptions{}); got != want {
				t.Fatalf("%s: %s count = %d, want %d", tc.name, format, got, want)
			}
		}
	}
}

// 混合订阅：clash 只少 Snell 一行，其余节点与分组照常；stash 全在。
func TestR9SnellClashSplitKeepsOtherNodes(t *testing.T) {
	eps := r9SnellMixedEndpoints(t)
	clash, _, err := Subscription(FormatClash, eps, SubOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(clash, "snell") || !strings.Contains(clash, "R9 VLESS") {
		t.Fatalf("clash must keep the vless node and only drop snell:\n%s", clash)
	}
	if got := SubscriptionNodeCount(FormatClash, eps, SubOptions{}); got != 1 {
		t.Fatalf("clash count = %d, want 1", got)
	}
	stash, _, err := Subscription(FormatStash, eps, SubOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stash, "snell") || !strings.Contains(stash, "R9 VLESS") {
		t.Fatalf("stash must carry both nodes:\n%s", stash)
	}
	if got := SubscriptionNodeCount(FormatStash, eps, SubOptions{}); got != 2 {
		t.Fatalf("stash count = %d, want 2", got)
	}
}

// 模板路径同口径：同一模板，clash 槽位无 Snell、stash 槽位有 Snell。
func TestR9SnellClashSplitTemplates(t *testing.T) {
	tpl := "proxies: []\nproxy-groups:\n  - {name: 选择, type: select, include-all: true}\nrules: [MATCH,选择]\n"
	eps := []Endpoint{g4SnellEndpoint(t, 5, false)}

	clash, _, err := Subscription(FormatClash, eps, SubOptions{Templates: map[string]string{TemplateClash: tpl}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(clash, "snell") {
		t.Fatalf("clash template output must not carry built snell:\n%s", clash)
	}
	stash, _, err := Subscription(FormatStash, eps, SubOptions{Templates: map[string]string{TemplateStash: tpl}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stash, "snell") {
		t.Fatalf("stash template output must carry the snell node:\n%s", stash)
	}
}

// R10-01（LEDGER10 §2，翻转原 TestR9ImportedSnellClashBoundaryUnchanged
// 的现状钉格——该格按其头注「翻转须有台账条目」，台账条目即 R10-01）：
// 导入 Snell 在 clash（Mihomo）格式接入与自建面同一接受域门。可下发
// 集＝{v0 无版本键、v1、v2（强制 udp:false）、v3、v3＋http/tls obfs}；
// v4/v5 与 shadow-tls/restls/jls 混淆形态在 clash 格式略过（官方
// Mihomo 1.19.16/1.19.17 实测整份拒绝，修复轮 10 红/绿证在案）。
// stash/shadowrocket 不动：全形态仍走 proxyClash 既有口径（v6 除外）。
func r10ImpSnell(extra Proxy) Endpoint {
	p := Proxy{"type": "snell", "server": "imp.example.com", "port": 8388, "psk": "imp-psk-123456"}
	for k, v := range extra {
		p[k] = v
	}
	return Endpoint{Name: "imp", Host: "imp.example.com", Port: 8388, Proxy: p}
}

func TestR10ImportedSnellMihomoGateMatrix(t *testing.T) {
	stlsKey := Proxy{"shadow-tls": map[string]interface{}{"password": "stls-pass-123", "host": "www.microsoft.com", "version": 3}}
	stlsObfs := Proxy{"obfs-opts": map[string]interface{}{"mode": "shadow-tls", "host": "www.microsoft.com", "password": "stls-pass-123", "version": 3}}
	merge := func(base Proxy, extra Proxy) Proxy {
		for k, v := range extra {
			base[k] = v
		}
		return base
	}
	for _, tc := range []struct {
		name      string
		extra     Proxy
		wantClash bool
		wantStash bool
	}{
		{"v0", Proxy{}, true, true},
		{"v1", Proxy{"version": 1}, true, true},
		{"v2", Proxy{"version": 2}, true, true},
		{"v3", Proxy{"version": 3}, true, true},
		{"v4", Proxy{"version": 4}, false, true},
		{"v5", Proxy{"version": 5}, false, true},
		{"v6", Proxy{"version": 6}, false, false},
		{"v3-http", Proxy{"version": 3, "obfs-opts": map[string]interface{}{"mode": "http", "host": "www.microsoft.com"}}, true, true},
		{"v3-tls", Proxy{"version": 3, "obfs-opts": map[string]interface{}{"mode": "tls", "host": "www.microsoft.com"}}, true, true},
		{"v4-stls-key", merge(Proxy{"version": 4}, stlsKey), false, true},
		{"v5-stls-key", merge(Proxy{"version": 5}, stlsKey), false, true},
		{"v4-stls-obfs", merge(Proxy{"version": 4}, stlsObfs), false, true},
		{"v5-stls-obfs", merge(Proxy{"version": 5}, stlsObfs), false, true},
		{"v3-restls", Proxy{"version": 3, "obfs-opts": map[string]interface{}{"mode": "restls", "host": "www.microsoft.com", "password": "stls-pass-123"}}, false, true},
		{"v3-jls", Proxy{"version": 3, "obfs-opts": map[string]interface{}{"mode": "jls", "host": "www.microsoft.com", "password": "stls-pass-123"}}, false, true},
	} {
		e := r10ImpSnell(tc.extra)
		eps := []Endpoint{e}

		if _, ok := clashProxyMihomo(e); ok != tc.wantClash {
			t.Fatalf("%s: clashProxyMihomo ok = %v, want %v", tc.name, ok, tc.wantClash)
		}
		clash, _, err := Subscription(FormatClash, eps, SubOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if kept := strings.Contains(clash, "snell"); kept != tc.wantClash {
			t.Fatalf("%s: clash snell kept = %v, want %v:\n%s", tc.name, kept, tc.wantClash, clash)
		}
		wantClashCount := 0
		if tc.wantClash {
			wantClashCount = 1
		}
		if got := SubscriptionNodeCount(FormatClash, eps, SubOptions{}); got != wantClashCount {
			t.Fatalf("%s: clash count = %d, want %d", tc.name, got, wantClashCount)
		}

		if _, ok := ClashProxy(e); ok != tc.wantStash {
			t.Fatalf("%s: ClashProxy ok = %v, want %v (stash/SR caliber unchanged)", tc.name, ok, tc.wantStash)
		}
		for _, format := range []string{FormatStash, FormatShadowrocket} {
			body, _, err := Subscription(format, eps, SubOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if kept := strings.Contains(body, "snell"); kept != tc.wantStash {
				t.Fatalf("%s: %s snell kept = %v, want %v:\n%s", tc.name, format, kept, tc.wantStash, body)
			}
		}
		wantStashCount := 0
		if tc.wantStash {
			wantStashCount = 1
		}
		if got := SubscriptionNodeCount(FormatStash, eps, SubOptions{}); got != wantStashCount {
			t.Fatalf("%s: stash count = %d, want %d", tc.name, got, wantStashCount)
		}
	}

	// 字段级：v1/v2 强制 udp:false 且版本原样；v0 无版本键、udp:false；
	// v3 保持 udp:true。
	for _, tc := range []struct {
		name        string
		extra       Proxy
		wantVersion interface{} // nil = 版本键必须缺席
		wantUDP     bool
	}{
		{"v0", Proxy{}, nil, false},
		{"v1", Proxy{"version": 1}, 1, false},
		{"v2", Proxy{"version": 2}, 2, false},
		{"v3", Proxy{"version": 3}, 3, true},
	} {
		node, ok := clashProxyMihomo(r10ImpSnell(tc.extra))
		if !ok {
			t.Fatalf("%s: clashProxyMihomo must keep the node", tc.name)
		}
		m := yamlToMap(node)
		if tc.wantVersion == nil {
			if _, present := m["version"]; present {
				t.Fatalf("%s: version key must stay absent: %v", tc.name, m)
			}
		} else if got := Proxy(m).num("version"); got != tc.wantVersion.(int) {
			t.Fatalf("%s: version = %d, want %d", tc.name, got, tc.wantVersion)
		}
		if got, _ := m["udp"].(bool); got != tc.wantUDP {
			t.Fatalf("%s: udp = %v, want %v (%v)", tc.name, m["udp"], tc.wantUDP, m)
		}
	}
	// v3+http 在场时 obfs-opts 原样带出。
	node, ok := clashProxyMihomo(r10ImpSnell(Proxy{"version": 3,
		"obfs-opts": map[string]interface{}{"mode": "http", "host": "www.microsoft.com"}}))
	if !ok {
		t.Fatal("v3+http must render via clashProxyMihomo")
	}
	if oo := Proxy(yamlToMap(node)).sub("obfs-opts"); oo.str("mode") != "http" {
		t.Fatalf("v3+http obfs-opts lost: %v", yamlToMap(node))
	}

	// 模板路径同门：导入 v5 在 clash 模板槽位消失、stash 槽位仍在。
	tpl := "proxies: []\nproxy-groups:\n  - {name: 选择, type: select, include-all: true}\nrules: [MATCH,选择]\n"
	eps := []Endpoint{r10ImpSnell(Proxy{"version": 5})}
	clashTpl, _, err := Subscription(FormatClash, eps, SubOptions{Templates: map[string]string{TemplateClash: tpl}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(clashTpl, "snell") {
		t.Fatalf("clash template output must not carry imported snell v5:\n%s", clashTpl)
	}
	stashTpl, _, err := Subscription(FormatStash, eps, SubOptions{Templates: map[string]string{TemplateStash: tpl}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stashTpl, "snell") {
		t.Fatalf("stash template output must carry imported snell v5:\n%s", stashTpl)
	}
}

// R10-01 同批（I-F4-02）：sing-box 面 obfs-opts 形态的 Snell+ShadowTLS
// 与 shadow-tls 键形态同走 detour 转换（此前整节点静默略过）；其余
// obfs 形态维持略过；版本白名单门对两形态同判（v3 同略）。
func TestR10ImportedSnellSingboxShadowTLSAlign(t *testing.T) {
	stlsKey := Proxy{"shadow-tls": map[string]interface{}{"password": "stls-pass-123", "host": "www.microsoft.com", "version": 3}}
	stlsObfs := Proxy{"obfs-opts": map[string]interface{}{"mode": "shadow-tls", "host": "www.microsoft.com", "password": "stls-pass-123", "version": 3}}
	merge := func(base Proxy, extra Proxy) Proxy {
		for k, v := range extra {
			base[k] = v
		}
		return base
	}
	assertDetour := func(t *testing.T, name string, e Endpoint, wantSnellVersion int) {
		t.Helper()
		outs, ok := singboxOutbounds(e, true)
		if !ok || len(outs) != 2 {
			t.Fatalf("%s: singbox outbounds = %v, ok = %v; want detour pair", name, outs, ok)
		}
		snell, stls := outs[0], outs[1]
		if snell["type"] != "snell" || snell["detour"] != "imp (shadowtls)" || snell["version"] != wantSnellVersion {
			t.Fatalf("%s: snell outbound = %v", name, snell)
		}
		if stls["type"] != "shadowtls" || stls["version"] != 3 || stls["password"] != "stls-pass-123" {
			t.Fatalf("%s: shadowtls outbound = %v", name, stls)
		}
		if tlsCfg, _ := stls["tls"].(map[string]interface{}); tlsCfg["server_name"] != "www.microsoft.com" {
			t.Fatalf("%s: shadowtls server_name = %v", name, stls["tls"])
		}
		if got := SubscriptionNodeCount(FormatSingbox, []Endpoint{e}, SubOptions{SingboxSnell: true}); got != 1 {
			t.Fatalf("%s: sing-box count = %d, want 1", name, got)
		}
	}
	assertDetour(t, "v4-stls-obfs", r10ImpSnell(merge(Proxy{"version": 4}, stlsObfs)), 4)
	assertDetour(t, "v5-stls-obfs", r10ImpSnell(merge(Proxy{"version": 5}, stlsObfs)), 4)
	assertDetour(t, "v5-stls-key", r10ImpSnell(merge(Proxy{"version": 5}, stlsKey)), 4)

	for _, tc := range []struct {
		name  string
		extra Proxy
	}{
		{"v4-restls-obfs", Proxy{"version": 4, "obfs-opts": map[string]interface{}{"mode": "restls", "host": "www.microsoft.com", "password": "stls-pass-123"}}},
		{"v3-stls-obfs", merge(Proxy{"version": 3}, stlsObfs)},
		{"v3-stls-key", merge(Proxy{"version": 3}, stlsKey)},
	} {
		e := r10ImpSnell(tc.extra)
		if outs, ok := singboxOutbounds(e, true); ok {
			t.Fatalf("%s: sing-box must skip, got %v", tc.name, outs)
		}
		if got := SubscriptionNodeCount(FormatSingbox, []Endpoint{e}, SubOptions{SingboxSnell: true}); got != 0 {
			t.Fatalf("%s: sing-box count = %d, want 0", tc.name, got)
		}
	}
}
