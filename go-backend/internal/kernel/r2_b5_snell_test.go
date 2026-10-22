package kernel

import (
	"fmt"
	"strings"
	"testing"
)

// R2X-002（批 5）：导入 Snell 版本域分客户端口径（verdicts §2.4）：
//   - finishProxy：v<0 || v>6 拒绝导入；0→4 缺省、1–6 落库（不得拒 v1–v3）；
//   - proxySingbox：白名单 {4,5→4, 6→6}，其余（含 1/2/3、缺失）整节点略过；
//   - proxyClash：1–4 直出、5→4、6+ 略过（既有），补负数略过；
//   - proxySurge：1–6 原生直出，>6（及 <0）略过。
// 计数（SubscriptionNodeCount）必须与各渲染器去留一致；作链式目标时
// RenderWith 的 sing-box 产物同口径（非法版本整单失败、合法版本在位）。

func r2SnellProxy(version *int) Proxy {
	p := Proxy{"type": "snell", "name": "s", "server": "203.0.113.10", "port": 8388, "psk": "psk-r2-test-1234"}
	if version != nil {
		p["version"] = *version
	}
	return p
}

func r2Int(v int) *int { return &v }

func TestR2B5SnellFinishProxyVersionDomain(t *testing.T) {
	cases := []struct {
		v       *int
		stored  int
		wantErr bool
	}{
		{r2Int(-1), 0, true},
		{r2Int(0), 4, false},
		{r2Int(1), 1, false},
		{r2Int(2), 2, false},
		{r2Int(3), 3, false},
		{r2Int(4), 4, false},
		{r2Int(5), 5, false},
		{r2Int(6), 6, false},
		{r2Int(7), 0, true},
		{r2Int(99), 0, true},
		{nil, 4, false}, // 缺失同 0：补默认 4
	}
	for _, tc := range cases {
		label := "missing"
		if tc.v != nil {
			label = fmt.Sprint(*tc.v)
		}
		node, err := finishProxy(r2SnellProxy(tc.v))
		if tc.wantErr {
			if err == nil {
				t.Fatalf("v=%s: finishProxy must reject out-of-domain snell version, stored %v", label, node.Proxy.num("version"))
			}
			continue
		}
		if err != nil {
			t.Fatalf("v=%s: finishProxy: %v", label, err)
		}
		if got := node.Proxy.num("version"); got != tc.stored {
			t.Fatalf("v=%s: stored version = %d, want %d", label, got, tc.stored)
		}
	}
}

// 三渲染器对脏数据（直接构造 Proxy，绕过 finishProxy）的去留与 version 值。
func TestR2B5SnellRenderersVersionDomain(t *testing.T) {
	type want struct {
		ok      bool
		version int // ok 时渲染出的 version；sing-box 的 5 已归一为 4
	}
	versions := []int{-1, 0, 1, 2, 3, 4, 5, 6, 7, 99}
	singboxWant := map[int]want{
		-1: {false, 0}, 0: {false, 0}, 1: {false, 0}, 2: {false, 0}, 3: {false, 0},
		4: {true, 4}, 5: {true, 4}, 6: {true, 6}, 7: {false, 0}, 99: {false, 0},
	}
	clashWant := map[int]want{
		-1: {false, 0}, 1: {true, 1}, 2: {true, 2}, 3: {true, 3}, 4: {true, 4},
		5: {true, 4}, 6: {false, 0}, 7: {false, 0}, 99: {false, 0},
	}
	surgeWant := map[int]want{
		-1: {false, 0}, 1: {true, 1}, 2: {true, 2}, 3: {true, 3}, 4: {true, 4},
		5: {true, 5}, 6: {true, 6}, 7: {false, 0}, 99: {false, 0},
	}
	for _, v := range versions {
		e := Endpoint{Name: "imp", Host: "203.0.113.10", Port: 8388, Proxy: r2SnellProxy(r2Int(v))}

		if w := singboxWant[v]; true {
			outs, ok := singboxOutbounds(e, true)
			if ok != w.ok {
				t.Fatalf("v=%d sing-box: ok=%v want %v (outs=%v)", v, ok, w.ok, outs)
			}
			if ok {
				if len(outs) != 1 || outs[0]["version"] != w.version {
					t.Fatalf("v=%d sing-box: outs=%v want version %d", v, outs, w.version)
				}
			}
			// 计数一致：sing-box 计数与渲染去留恒等。
			if got := SubscriptionNodeCount(FormatSingbox, []Endpoint{e}, SubOptions{SingboxSnell: true}); (got == 1) != w.ok {
				t.Fatalf("v=%d sing-box count=%d, renderer ok=%v", v, got, w.ok)
			}
		}
		if w, covered := clashWant[v]; covered {
			node, ok := proxyClash(e)
			if ok != w.ok {
				t.Fatalf("v=%d clash: ok=%v want %v", v, ok, w.ok)
			}
			if ok {
				if got := Proxy(yamlToMap(node)).num("version"); got != w.version {
					t.Fatalf("v=%d clash: version=%d want %d", v, got, w.version)
				}
			}
			if got := SubscriptionNodeCount(FormatClash, []Endpoint{e}, SubOptions{}); (got == 1) != w.ok {
				t.Fatalf("v=%d clash count=%d, renderer ok=%v", v, got, w.ok)
			}
		}
		if w, covered := surgeWant[v]; covered {
			line, ok := proxySurge(e)
			if ok != w.ok {
				t.Fatalf("v=%d surge: ok=%v want %v (line=%q)", v, ok, w.ok, line)
			}
			if ok && !strings.Contains(line, fmt.Sprintf("version=%d", w.version)) {
				t.Fatalf("v=%d surge: line %q want version=%d", v, line, w.version)
			}
			if got := SubscriptionNodeCount(FormatSurge, []Endpoint{e}, SubOptions{}); (got == 1) != w.ok {
				t.Fatalf("v=%d surge count=%d, renderer ok=%v", v, got, w.ok)
			}
		}
	}
	// 缺失版本（无 version 键）：sing-box 整节点略过。
	e := Endpoint{Name: "imp", Host: "203.0.113.10", Port: 8388, Proxy: r2SnellProxy(nil)}
	if _, ok := singboxOutbounds(e, true); ok {
		t.Fatal("sing-box must skip snell node with missing version")
	}
	if got := SubscriptionNodeCount(FormatSingbox, []Endpoint{e}, SubOptions{SingboxSnell: true}); got != 0 {
		t.Fatalf("missing-version sing-box count=%d, want 0", got)
	}
}

// 作链式目标：非法版本经 RenderWith（sing-box）整单失败，合法 v6 在位且 version=6。
func TestR2B5SnellChainTargetRenderWith(t *testing.T) {
	mkInbound := func(via Endpoint) Inbound {
		in := Inbound{ID: 5, Protocol: ProtocolVLESS, Port: 8443, Settings: Settings{UUID: testUUID}, Security: Security{Type: SecurityNone}}
		if err := Normalize(TypeSingbox, &in); err != nil {
			t.Fatal(err)
		}
		in.Via = &via
		return in
	}
	bad := Endpoint{Name: "imp", Host: "203.0.113.10", Port: 8388, Proxy: r2SnellProxy(r2Int(3))}
	if _, err := RenderWith(TypeSingbox, []Inbound{mkInbound(bad)}, RenderOptions{SingboxSnell: true}); err == nil {
		t.Fatal("chain through snell v3 must fail sing-box render (v3 is outside the sing-box domain)")
	}
	good := Endpoint{Name: "imp", Host: "203.0.113.10", Port: 8388, Proxy: r2SnellProxy(r2Int(6))}
	out, err := RenderWith(TypeSingbox, []Inbound{mkInbound(good)}, RenderOptions{SingboxSnell: true})
	if err != nil {
		t.Fatalf("chain through snell v6: %v", err)
	}
	if !strings.Contains(out.Files[0].Content, `"snell"`) || !strings.Contains(out.Files[0].Content, `"version": 6`) {
		t.Fatalf("chain render must carry snell v6 outbound:\n%s", out.Files[0].Content)
	}
}

// R2X-023：Snell v6 的 mode=unshaped 在 sing-box / Surge 导入→再下发往返保留。
func TestR2B5SnellV6ModeRoundTrip(t *testing.T) {
	// sing-box 来源。
	nodes, errs := ParseImport(`{"outbounds":[{"type":"snell","tag":"sb-v6","server":"203.0.113.10","server_port":8388,"psk":"psk-r2-test-1234","version":6,"mode":"unshaped"}]}`)
	if len(errs) != 0 || len(nodes) != 1 {
		t.Fatalf("sing-box import: nodes=%v errs=%v", nodes, errs)
	}
	if got := nodes[0].Proxy.str("mode"); got != "unshaped" {
		t.Fatalf("sing-box v6 mode lost on import: proxy=%v", nodes[0].Proxy)
	}
	e := Endpoint{Name: "sb-v6", Host: nodes[0].Host, Port: nodes[0].Port, Proxy: nodes[0].Proxy, Direct: true}
	outs, ok := singboxOutbounds(e, true)
	if !ok || len(outs) != 1 || outs[0]["mode"] != "unshaped" {
		t.Fatalf("sing-box re-render must carry mode=unshaped: %v ok=%v", outs, ok)
	}
	line, ok := proxySurge(e)
	if !ok || !strings.Contains(line, "mode=unshaped") {
		t.Fatalf("surge re-render must carry mode=unshaped: %q ok=%v", line, ok)
	}

	// Surge 来源。
	nodes, errs = ParseImport("Sg-v6 = snell, 203.0.113.11, 8389, psk=psk-r2-test-5678, version=6, mode=unshaped")
	if len(errs) != 0 || len(nodes) != 1 {
		t.Fatalf("surge import: nodes=%v errs=%v", nodes, errs)
	}
	if got := nodes[0].Proxy.str("mode"); got != "unshaped" {
		t.Fatalf("surge v6 mode lost on import: proxy=%v", nodes[0].Proxy)
	}
	e2 := Endpoint{Name: "Sg-v6", Host: nodes[0].Host, Port: nodes[0].Port, Proxy: nodes[0].Proxy, Direct: true}
	line, ok = proxySurge(e2)
	if !ok || !strings.Contains(line, "mode=unshaped") || !strings.Contains(line, "version=6") {
		t.Fatalf("surge round trip must carry mode=unshaped: %q ok=%v", line, ok)
	}
	outs, ok = singboxOutbounds(e2, true)
	if !ok || len(outs) != 1 || outs[0]["mode"] != "unshaped" {
		t.Fatalf("sing-box render of surge-imported v6 must carry mode: %v ok=%v", outs, ok)
	}

	// 默认 mode 节点往返不变：无 mode 键、无 mode 输出。
	nodes, errs = ParseImport(`{"outbounds":[{"type":"snell","tag":"sb-v6d","server":"203.0.113.12","server_port":8390,"psk":"psk-r2-test-9012","version":6}]}`)
	if len(errs) != 0 || len(nodes) != 1 {
		t.Fatalf("sing-box default-mode import: nodes=%v errs=%v", nodes, errs)
	}
	if _, has := nodes[0].Proxy["mode"]; has {
		t.Fatalf("default-mode node must not gain a mode key: %v", nodes[0].Proxy)
	}
	e3 := Endpoint{Name: "sb-v6d", Host: nodes[0].Host, Port: nodes[0].Port, Proxy: nodes[0].Proxy, Direct: true}
	outs, ok = singboxOutbounds(e3, true)
	if !ok || len(outs) != 1 {
		t.Fatalf("default-mode v6 render: %v ok=%v", outs, ok)
	}
	if _, has := outs[0]["mode"]; has {
		t.Fatalf("default-mode v6 sing-box output must not carry mode: %v", outs[0])
	}
	if line, ok := proxySurge(e3); !ok || strings.Contains(line, "mode=") {
		t.Fatalf("default-mode v6 surge output must not carry mode: %q ok=%v", line, ok)
	}

	// mode 仅 v6 且值为 unshaped 时保留（与 model 校验同域）：v4 的 mode、
	// v6 的未知 mode 值均丢弃。
	nodes, _ = ParseImport(`{"outbounds":[{"type":"snell","tag":"sb-v4m","server":"203.0.113.13","server_port":8391,"psk":"psk-r2-test-3456","version":4,"mode":"unshaped"}]}`)
	if len(nodes) != 1 {
		t.Fatalf("v4 import: %v", nodes)
	}
	if _, has := nodes[0].Proxy["mode"]; has {
		t.Fatalf("mode on non-v6 snell must be dropped: %v", nodes[0].Proxy)
	}
	nodes, _ = ParseImport("Sg-v6b = snell, 203.0.113.14, 8392, psk=psk-r2-test-7890, version=6, mode=bogus")
	if len(nodes) != 1 {
		t.Fatalf("surge v6 bogus-mode import: %v", nodes)
	}
	if _, has := nodes[0].Proxy["mode"]; has {
		t.Fatalf("unknown mode value must be dropped: %v", nodes[0].Proxy)
	}
}
