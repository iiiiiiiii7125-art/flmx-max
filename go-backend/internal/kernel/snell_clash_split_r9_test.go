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
// 范围边界（有意保留、待单独裁定）：导入 Snell 节点仍走 proxyClash
// 既有口径（R2X-002 矩阵），clash 格式对其 v4/v5 的下发不在本轮
// 改动内——末尾一格钉住现状，翻转须有台账条目。

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

// 范围边界钉格：导入 Snell v5 在 clash 格式的现状（proxyClash 归一
// 为 version 4 下发）本轮不动，见文件头注。
func TestR9ImportedSnellClashBoundaryUnchanged(t *testing.T) {
	e := Endpoint{Name: "imp", Host: "imp.example.com", Port: 8388,
		Proxy: Proxy{"type": "snell", "psk": "abc", "version": 5}}
	node, ok := ClashProxy(e)
	if !ok {
		t.Fatal("imported snell v5 must still render via ClashProxy")
	}
	if m := yamlToMap(node); m["version"] != 4 {
		t.Fatalf("imported snell v5 must still normalize to version 4: %v", m)
	}
	if got := SubscriptionNodeCount(FormatClash, []Endpoint{e}, SubOptions{}); got != 1 {
		t.Fatalf("imported snell v5 clash count = %d, want 1 (pre-R9-01 caliber, unchanged)", got)
	}
}
