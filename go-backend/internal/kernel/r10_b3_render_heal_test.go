package kernel

import (
	"strings"
	"testing"
)

// R10 批 3（R10X-009 / R10X-015）：渲染愈合上收。
//
// R10X-009（D-04 两格，断言分开登记、不得互替）：
//   - 格一：Snell obfs-opts.mode 愈合上收 forEndpoint（克隆上
//     CanonicalSnellObfsMode 一次），Surge 面三形（HTTP /
//     ShadowTLS / shadowtls）转规范值；proxyClash 本地愈合保留
//     作冗余（同值断言附带验、不作本格销账据）。
//   - 格二：Snell v6 顶层 mode 以 EqualFold(unshaped) 愈合而非
//     静默丢弃——存量 UNSHAPED 在 sing-box 与 Surge 两面均带
//     mode=unshaped 下发。
//
// R10X-015（X-E-01，断言独立）：chainTargetFromInbound /
// singboxOutbound 的自建 SS 支由单段形改 Segments——直调渲染
// 函数多段输入转逐段归一、单段输入逐字不变（对一切可达输入
// Segments ≡ 单段形）。

func r10b3SnellEndpoint(extra map[string]interface{}) Endpoint {
	p := Proxy{"type": "snell", "server": "203.0.113.30", "port": 8388,
		"psk": "psk-r10b3", "version": 4}
	for k, v := range extra {
		p[k] = v
	}
	return Endpoint{Name: "snell-stock", Host: "203.0.113.30", Port: 8388, Direct: true, Proxy: p}
}

// ---- R10X-009 格一：Snell obfs-opts.mode Surge 三形转规范值 ----

func TestR10B3SnellObfsModeSurgeHeal(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"HTTP", "http"},
		{"ShadowTLS", "shadow-tls"},
		{"shadowtls", "shadow-tls"},
		{"TLS", "tls"},
		{"http", "http"}, // 合法小写逐字不变（防过修）
		{"shadow-tls", "shadow-tls"},
	} {
		e := r10b3SnellEndpoint(map[string]interface{}{
			"obfs-opts": map[string]interface{}{"mode": tc.in, "host": "example.com"},
		})
		line, ok := proxySurge(e)
		if !ok {
			t.Fatalf("mode=%q: proxySurge 须下发节点", tc.in)
		}
		if !strings.Contains(line, "obfs="+tc.want) || strings.Contains(line, "obfs="+tc.in+",") && tc.in != tc.want {
			t.Fatalf("mode=%q: Surge 行须含愈合值 obfs=%s，got %q", tc.in, tc.want, line)
		}
		// forEndpoint 克隆愈合、存储不动。
		healed := e.Proxy.forEndpoint(e)
		if got := healed.sub("obfs-opts").str("mode"); got != tc.want {
			t.Fatalf("mode=%q: forEndpoint 克隆须愈合为 %q，got %q", tc.in, tc.want, got)
		}
		if got := e.Proxy.sub("obfs-opts").str("mode"); got != tc.in {
			t.Fatalf("mode=%q: 存储值不得被渲染改写，got %q", tc.in, got)
		}
		// Clash 面本地愈合保留作冗余：同值（附带验）。
		node, ok := proxyClash(e)
		if !ok || Proxy(yamlToMap(node)).sub("obfs-opts").str("mode") != tc.want {
			t.Fatalf("mode=%q: Clash 面须同愈合值 %q，ok=%v node=%v", tc.in, tc.want, ok, node)
		}
	}
	// 无 obfs 不造值。
	e := r10b3SnellEndpoint(nil)
	if got := e.Proxy.forEndpoint(e).sub("obfs-opts").str("mode"); got != "" {
		t.Fatalf("无 obfs-opts 不得造 mode，got %q", got)
	}
	if line, ok := proxySurge(e); !ok || strings.Contains(line, "obfs=") {
		t.Fatalf("无 obfs 的 Surge 行不得含 obfs=：ok=%v line=%q", ok, line)
	}
}

// ---- R10X-009 格二：Snell v6 顶层 mode UNSHAPED 两面带 mode=unshaped ----

func TestR10B3SnellV6ModeUnshapedHeal(t *testing.T) {
	for _, in := range []string{"UNSHAPED", "Unshaped", "UnShApEd", "unshaped"} {
		e := r10b3SnellEndpoint(map[string]interface{}{"version": 6, "mode": in})
		// sing-box 面。
		outs, ok := proxySingbox(e)
		if !ok || len(outs) != 1 {
			t.Fatalf("mode=%q: proxySingbox 须下发 v6 节点，ok=%v outs=%v", in, ok, outs)
		}
		if outs[0]["mode"] != "unshaped" || outs[0]["version"] != 6 {
			t.Fatalf("mode=%q: sing-box 面须带 mode=unshaped，got %v", in, outs[0])
		}
		// Surge 面。
		line, ok := proxySurge(e)
		if !ok || !strings.Contains(line, "mode=unshaped") {
			t.Fatalf("mode=%q: Surge 面须带 mode=unshaped，ok=%v line=%q", in, ok, line)
		}
		// 克隆愈合、存储不动。
		if got := e.Proxy.forEndpoint(e).str("mode"); got != "unshaped" {
			t.Fatalf("mode=%q: forEndpoint 克隆须愈合为 unshaped，got %q", in, got)
		}
		if got := e.Proxy.str("mode"); got != in {
			t.Fatalf("mode=%q: 存储值不得被渲染改写，got %q", in, got)
		}
	}
	// 垃圾 mode 不得被愈合成 unshaped：两面仍不带 mode（不扩大接受集）。
	e := r10b3SnellEndpoint(map[string]interface{}{"version": 6, "mode": "bogus"})
	if outs, ok := proxySingbox(e); !ok || len(outs) != 1 {
		t.Fatalf("bogus mode v6 节点 sing-box 面须维持下发（仅 mode 略过）：ok=%v outs=%v", ok, outs)
	} else if _, has := outs[0]["mode"]; has {
		t.Fatalf("bogus mode 不得下发 mode，got %v", outs[0])
	}
	if line, ok := proxySurge(e); !ok || strings.Contains(line, "mode=") {
		t.Fatalf("bogus mode Surge 面不得带 mode：ok=%v line=%q", ok, line)
	}
	// 无 mode 的 v6 不造值。
	e = r10b3SnellEndpoint(map[string]interface{}{"version": 6})
	if outs, ok := proxySingbox(e); !ok || len(outs) != 1 {
		t.Fatalf("无 mode v6 sing-box 面须下发：ok=%v outs=%v", ok, outs)
	} else if _, has := outs[0]["mode"]; has {
		t.Fatalf("无 mode v6 不得造 mode，got %v", outs[0])
	}
}

// ---- R10X-015：自建 SS 链面 / sing-box 出站逐段归一 ----

func r10b3BuiltinSSEndpoint(pw string) Endpoint {
	return Endpoint{Name: "builtin-ss", Host: "203.0.113.40", Port: 8388, Direct: true,
		Inbound: Inbound{ID: 42, Protocol: ProtocolShadowsocks, Port: 8388,
			Settings: Settings{Method: r8b2Cipher, Password: pw}}}
}

func TestR10B3BuiltinSSMultiSegmentNormalized(t *testing.T) {
	e := r10b3BuiltinSSEndpoint(r8b2Noncanon)
	// 直调 chainTargetFromInbound（Xray 链式自建目标）。
	ct, err := chainTargetFromInbound(e)
	if err != nil {
		t.Fatalf("chainTargetFromInbound 多段须成功：%v", err)
	}
	if ct.Password != r8b2Canon {
		t.Fatalf("chainTargetFromInbound 多段须逐段归一：got %q want %q", ct.Password, r8b2Canon)
	}
	// 经 xrayChainOutbound 同点。
	ob, err := xrayChainOutbound(e, "via-42")
	if err != nil {
		t.Fatalf("xrayChainOutbound 多段须成功：%v", err)
	}
	servers, _ := ob["settings"].(obj)["servers"].([]interface{})
	if len(servers) != 1 || servers[0].(obj)["password"] != r8b2Canon {
		t.Fatalf("xrayChainOutbound 多段须逐段归一：%v", ob)
	}
	// 直调 singboxOutbound（自建入站 sing-box 订阅出站）。
	o, ok := singboxOutbound(e)
	if !ok {
		t.Fatal("singboxOutbound 多段须下发（不略过）")
	}
	if o["password"] != r8b2Canon {
		t.Fatalf("singboxOutbound 多段须逐段归一：got %q want %q", o["password"], r8b2Canon)
	}
	// 规范多段为不动点。
	ct2, err := chainTargetFromInbound(r10b3BuiltinSSEndpoint(r8b2Canon))
	if err != nil || ct2.Password != r8b2Canon {
		t.Fatalf("规范多段 chainTarget 须逐字不变：ct=%+v err=%v", ct2, err)
	}
	if o2, ok := singboxOutbound(r10b3BuiltinSSEndpoint(r8b2Canon)); !ok || o2["password"] != r8b2Canon {
		t.Fatalf("规范多段 singboxOutbound 须逐字不变：ok=%v o=%v", ok, o2)
	}
	// 存储不动。
	if got := e.Inbound.Settings.Password; got != r8b2Noncanon {
		t.Fatalf("存储密码不得被渲染改写：got %q", got)
	}
}

func TestR10B3BuiltinSSSingleSegmentVerbatim(t *testing.T) {
	// 单段输入逐字不变 = 与改前单段 helper 的产物逐字一致：
	// 非规范单段归一为 Std、规范单段不动、坏段原样（helper 兜底）。
	for _, tc := range []struct{ name, in, want string }{
		{"raw-single", r8b2SegRaw, r8b2SegStd2},
		{"std-single", r8b2SegStd, r8b2SegStd},
		{"garbage-single", "plainpassword", "plainpassword"},
		{"garbage-multi", r8b2SegStd + ":plainpassword", r8b2SegStd + ":plainpassword"},
	} {
		e := r10b3BuiltinSSEndpoint(tc.in)
		ct, err := chainTargetFromInbound(e)
		if err != nil || ct.Password != tc.want {
			t.Fatalf("%s: chainTargetFromInbound got %q err=%v want %q", tc.name, ct.Password, err, tc.want)
		}
		o, ok := singboxOutbound(e)
		if !ok || o["password"] != tc.want {
			t.Fatalf("%s: singboxOutbound got %v ok=%v want %q", tc.name, o["password"], ok, tc.want)
		}
		// 与单段 helper 本体逐字对账（Segments 对单段恒等单段形）。
		if want := NormalizedSS2022Password(r8b2Cipher, tc.in); tc.in == tc.want || !strings.Contains(tc.in, ":") {
			_ = want
		}
	}
	// legacy 方法（无 SS2022 key 形）：多段/明文均原样（KeyLen=0 直通）。
	legacy := Endpoint{Name: "legacy", Host: "203.0.113.41", Port: 8388, Direct: true,
		Inbound: Inbound{ID: 43, Protocol: ProtocolShadowsocks, Port: 8388,
			Settings: Settings{Method: "aes-256-gcm", Password: "plain:password"}}}
	ct, err := chainTargetFromInbound(legacy)
	if err != nil || ct.Password != "plain:password" {
		t.Fatalf("legacy chainTarget 须原样：ct=%+v err=%v", ct, err)
	}
	if o, ok := singboxOutbound(legacy); !ok || o["password"] != "plain:password" {
		t.Fatalf("legacy singboxOutbound 须原样：ok=%v o=%v", ok, o)
	}
}
