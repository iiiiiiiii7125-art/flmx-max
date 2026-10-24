package kernel

import (
	"strings"
	"testing"
)

// R11X-006（批 5）：Snell v6 顶层 mode 大小写变体在 sing-box JSON / Surge
// 行两进口面被精确匹配静默丢弃。修复口径（FIX-PLAN 批 5）：两转换器只
// 搬运原始 mode，执法一处收口于 validateProxyCredentials 的 snell 支——
// version==6 且 EqualFold(unshaped) 时愈合落库规范值 SnellModeUnshaped，
// 其余（bogus / 非 v6）不落 mode（与既有转换器丢弃口径及
// TestR2B5SnellV6ModeRoundTrip 的 v4/bogus 格一致，节点本身不误杀）。

func r11b5SingboxSnell(version int, mode string) string {
	m := ""
	if mode != "" {
		m = `,"mode":"` + mode + `"`
	}
	return `{"outbounds":[{"type":"snell","tag":"sb","server":"203.0.113.10","server_port":8388,"psk":"psk-r11b5-test","version":` +
		map[int]string{4: "4", 5: "5", 6: "6"}[version] + m + `}]}`
}

func r11b5SurgeSnell(version int, mode string) string {
	m := ""
	if mode != "" {
		m = ", mode=" + mode
	}
	v := map[int]string{4: "4", 5: "5", 6: "6"}[version]
	return "Sg = snell, 203.0.113.11, 8389, psk=psk-r11b5-test, version=" + v + m
}

func TestR11B5SnellTopModeVariantHealing(t *testing.T) {
	// 两通道 × 变体：进口后存储均为规范值 unshaped、回执无误杀。
	for _, mode := range []string{"UNSHAPED", "Unshaped", "uNsHaPeD"} {
		for _, tc := range []struct {
			name string
			in   string
		}{
			{"singbox", r11b5SingboxSnell(6, mode)},
			{"surge", r11b5SurgeSnell(6, mode)},
		} {
			nodes, errs := ParseImport(tc.in)
			if len(errs) != 0 || len(nodes) != 1 {
				t.Fatalf("%s mode=%s: nodes=%d errs=%v（不得误杀）", tc.name, mode, len(nodes), errs)
			}
			if got := nodes[0].Proxy.str("mode"); got != SnellModeUnshaped {
				t.Fatalf("%s mode=%s: stored mode=%q, want %q", tc.name, mode, got, SnellModeUnshaped)
			}
		}
	}
	// 规范值逐字不变。
	for _, tc := range []struct {
		name string
		in   string
	}{
		{"singbox", r11b5SingboxSnell(6, "unshaped")},
		{"surge", r11b5SurgeSnell(6, "unshaped")},
	} {
		nodes, errs := ParseImport(tc.in)
		if len(errs) != 0 || len(nodes) != 1 || nodes[0].Proxy.str("mode") != "unshaped" {
			t.Fatalf("%s canonical: nodes=%v errs=%v", tc.name, nodes, errs)
		}
		// 渲染带整形语义（存储格为据的渲染抽核）。
		e := Endpoint{Name: "x", Host: nodes[0].Host, Port: nodes[0].Port, Proxy: nodes[0].Proxy, Direct: true}
		outs, ok := singboxOutbounds(e, true)
		if !ok || len(outs) != 1 || outs[0]["mode"] != "unshaped" {
			t.Fatalf("%s canonical sing-box render: %v ok=%v", tc.name, outs, ok)
		}
		line, ok := proxySurge(e)
		if !ok || !strings.Contains(line, "mode=unshaped") {
			t.Fatalf("%s canonical surge render: %q ok=%v", tc.name, line, ok)
		}
	}
	// 变体愈合后渲染同样带 mode=unshaped（两通道各一格）。
	for _, in := range []string{r11b5SingboxSnell(6, "UNSHAPED"), r11b5SurgeSnell(6, "Unshaped")} {
		nodes, _ := ParseImport(in)
		if len(nodes) != 1 {
			t.Fatalf("variant render setup: %v", nodes)
		}
		e := Endpoint{Name: "x", Host: nodes[0].Host, Port: nodes[0].Port, Proxy: nodes[0].Proxy, Direct: true}
		outs, ok := singboxOutbounds(e, true)
		if !ok || len(outs) != 1 || outs[0]["mode"] != "unshaped" {
			t.Fatalf("variant sing-box render must carry mode=unshaped: %v ok=%v", outs, ok)
		}
		line, ok := proxySurge(e)
		if !ok || !strings.Contains(line, "mode=unshaped") {
			t.Fatalf("variant surge render must carry mode=unshaped: %q ok=%v", line, ok)
		}
	}
}

func TestR11B5SnellTopModeBogusAndNonV6Dropped(t *testing.T) {
	// mode=bogus 仍不落 mode、节点不误杀（两通道）。
	for _, tc := range []struct {
		name string
		in   string
	}{
		{"singbox-v6-bogus", r11b5SingboxSnell(6, "bogus")},
		{"surge-v6-bogus", r11b5SurgeSnell(6, "bogus")},
		{"singbox-v5-unshaped", r11b5SingboxSnell(5, "unshaped")},
		{"surge-v5-unshaped", r11b5SurgeSnell(5, "unshaped")},
		{"singbox-v5-variant", r11b5SingboxSnell(5, "UNSHAPED")},
		{"surge-v5-variant", r11b5SurgeSnell(5, "UNSHAPED")},
		{"singbox-v4-unshaped", r11b5SingboxSnell(4, "unshaped")},
		{"surge-v4-unshaped", r11b5SurgeSnell(4, "unshaped")},
	} {
		nodes, errs := ParseImport(tc.in)
		if len(errs) != 0 || len(nodes) != 1 {
			t.Fatalf("%s: nodes=%d errs=%v（不得误杀）", tc.name, len(nodes), errs)
		}
		if _, has := nodes[0].Proxy["mode"]; has {
			t.Fatalf("%s: mode must not be stored, proxy=%v", tc.name, nodes[0].Proxy)
		}
	}
}

func TestR11B5SnellTopModeClashControl(t *testing.T) {
	// Clash 对照（存储格为据）：规范值逐字落库不变。
	doc := "proxies:\n  - {name: c, type: snell, server: 203.0.113.12, port: 8390, psk: psk-r11b5-test, version: 6, mode: unshaped, udp: true}\n"
	nodes, errs := ParseImport(doc)
	if len(errs) != 0 || len(nodes) != 1 {
		t.Fatalf("clash canonical: nodes=%v errs=%v", nodes, errs)
	}
	if got := nodes[0].Proxy.str("mode"); got != "unshaped" {
		t.Fatalf("clash canonical stored mode=%q, want unshaped", got)
	}
}
