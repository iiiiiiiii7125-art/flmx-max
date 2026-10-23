package kernel

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

// R3 发版门 GATE-F1（R3X-008 渲染侧规范化）：批 2 在导入面接受四种
// base64 形态的 SS2022 密钥（Std 带填充 / RawStd / URL 带填充 /
// RawURL），但渲染侧原样输出，发版门真机实证非规范形态被原样渲染
// 进 sing-box 与 mihomo 订阅、两家客户端均解码失败
// （sing-box 1.14.3 check FATAL、mihomo 整表 -t 失败）。
//
// 本批口径：
//   - 单段合法 key 在全部渲染出口规范化为带填充 StdEncoding 后输出
//     （解码字节不变，仅线格式归一）；规范形态自身恒等。
//   - 存量坏 key 维持各面既有口径：sing-box 面（导入节点）略过，
//     其余格式原样输出（B2 裁定），不新造语义。
//   - SIP023 冒号多段：本批时点 sing-box 保守略过；R4-SIP023-R 已
//     解除该略过（逐段归一后下发，见 r4_b2_sip023_test.go 与本文件
//     BadAndMulti 用例的随裁定更新）；Clash 面逐段
//     规范化（双客户端二进制实测按段以带填充 Std 解码、逐段归一不
//     改变解码字节，证据见批报告）；Surge / Loon / QX / ss 链接等
//     段解码未经二进制证实的面维持原样输出。
//   - 自建入站的 SS2022 密码经 Normalize 只可能是 Std 形态；此处
//     同 helper 兜底存量直写行（规范化或原样，绝不略过）。

var gateF1Forms = []struct {
	name string
	enc  *base64.Encoding
}{
	{"Std", base64.StdEncoding},
	{"RawStd", base64.RawStdEncoding},
	{"URL", base64.URLEncoding},
	{"RawURL", base64.RawURLEncoding},
}

func gateF1Endpoint(cipher, pw string) Endpoint {
	return Endpoint{Name: "g1", Host: "203.0.113.10", Port: 8388, Direct: true,
		Proxy: Proxy{"type": "ss", "server": "203.0.113.10", "port": 8388,
			"cipher": cipher, "password": pw}}
}

// TestR3GateF1NormalizedHelper：规范化 helper 直测——四形态归一到
// 带填充 Std、规范形态恒等、legacy/多段/坏 key 原样不动。
func TestR3GateF1NormalizedHelper(t *testing.T) {
	raw32 := r3b2RawKey(32)
	want := base64.StdEncoding.EncodeToString(raw32)
	for _, f := range gateF1Forms {
		got := NormalizedSS2022Password(r3b2C256, f.enc.EncodeToString(raw32))
		if got != want {
			t.Fatalf("%s 形态须规范化为带填充 Std：got %q want %q", f.name, got, want)
		}
	}
	if got := NormalizedSS2022Password(r3b2C256, want); got != want {
		t.Fatalf("规范形态须恒等：got %q", got)
	}
	if got := NormalizedSS2022Password("aes-256-gcm", "plainpassword"); got != "plainpassword" {
		t.Fatalf("legacy 口令不得改动：got %q", got)
	}
	multi := want + ":" + base64.RawURLEncoding.EncodeToString(raw32)
	if got := NormalizedSS2022Password(r3b2C256, multi); got != multi {
		t.Fatalf("通用 helper 对多段维持原样：got %q want %q", got, multi)
	}
	// Clash 面专用逐段 helper：双客户端二进制实测按段 Std 解码，
	// 逐段归一不改变解码字节；一段坏则整体原样（该面既有口径）。
	if got := NormalizedSS2022PasswordSegments(r3b2C256, multi); got != want+":"+want {
		t.Fatalf("多段逐段归一：got %q want %q", got, want+":"+want)
	}
	badMulti := want + ":plainpassword"
	if got := NormalizedSS2022PasswordSegments(r3b2C256, badMulti); got != badMulti {
		t.Fatalf("多段含坏段须整体原样：got %q", got)
	}
	if got := NormalizedSS2022PasswordSegments(r3b2C256, base64.RawURLEncoding.EncodeToString(raw32)); got != want {
		t.Fatalf("逐段 helper 对单段与通用 helper 一致：got %q", got)
	}
	if got := NormalizedSS2022Password(r3b2C256, "plainpassword"); got != "plainpassword" {
		t.Fatalf("坏 key 在 helper 层原样返回（略过/原样由各渲染面既有口径决定）：got %q", got)
	}
	if got := NormalizedSS2022Password(r3b2C128, base64.RawURLEncoding.EncodeToString(r3b2RawKey(16))); got != base64.StdEncoding.EncodeToString(r3b2RawKey(16)) {
		t.Fatalf("16B cipher 同样规范化：got %q", got)
	}
}

// TestR3GateF1ProxyRenderAllFaces：导入节点四形态 × 全部渲染出口，
// 输出的密码必须是带填充 Std 规范形态（且解码字节与源 key 相同）。
func TestR3GateF1ProxyRenderAllFaces(t *testing.T) {
	raw32 := r3b2RawKey(32)
	want := base64.StdEncoding.EncodeToString(raw32)
	for _, f := range gateF1Forms {
		pw := f.enc.EncodeToString(raw32)
		e := gateF1Endpoint(r3b2C256, pw)
		t.Run(f.name, func(t *testing.T) {
			// sing-box JSON（导入节点面）
			outs, ok := proxySingbox(e)
			if !ok || len(outs) != 1 || outs[0]["password"] != want {
				t.Fatalf("sing-box 渲染须规范化：ok=%v outs=%v", ok, outs)
			}
			// Clash / mihomo YAML
			node, ok := proxyClash(e)
			if !ok || Proxy(yamlToMap(node)).str("password") != want {
				t.Fatalf("clash 渲染须规范化：ok=%v node=%v", ok, node)
			}
			// Surge
			line, ok := proxySurge(e)
			if !ok || !strings.Contains(line, "password="+want) {
				t.Fatalf("surge 渲染须规范化：ok=%v line=%q", ok, line)
			}
			// 分享链接 ss://（SIP022 明文 userinfo，百分号编码规范形态）
			uri, ok := proxyURI(e)
			if !ok || !strings.Contains(uri, ":"+url.QueryEscape(want)+"@") {
				t.Fatalf("ss 链接须规范化：ok=%v uri=%q", ok, uri)
			}
			// Loon（经 endpointProxy 的 Clash 模式直通路径）
			p, ok := endpointProxy(e)
			if !ok {
				t.Fatal("endpointProxy 失败")
			}
			if ll, ok := loonLine(p); !ok || !strings.Contains(ll, "%q") && !strings.Contains(ll, want) {
				t.Fatalf("loon 渲染须规范化：ok=%v line=%q", ok, ll)
			}
			// Quantumult X
			if ql, ok := qxLine(p); !ok || !strings.Contains(ql, "password="+want) {
				t.Fatalf("qx 渲染须规范化：ok=%v line=%q", ok, ql)
			}
			// Shadowrocket（Clash 列表面）
			if body := shadowrocketProfile([]Endpoint{e}); !strings.Contains(body, want) {
				t.Fatalf("shadowrocket 渲染须规范化：%q", body)
			}
			// Xray 链式出站（导入节点作 Via 时的客户端渲染）
			ob, err := xrayChainOutbound(e, "via")
			if err != nil {
				t.Fatalf("xray chain: %v", err)
			}
			servers, _ := ob["settings"].(obj)["servers"].([]interface{})
			if len(servers) != 1 || servers[0].(obj)["password"] != want {
				t.Fatalf("xray chain 渲染须规范化：%v", ob["settings"])
			}
			// sing-box 链式出站
			obs, err := singboxChainOutbounds(e, "via", 0, false)
			if err != nil || len(obs) != 1 || obs[0].(obj)["password"] != want {
				t.Fatalf("sing-box chain 渲染须规范化：err=%v obs=%v", err, obs)
			}
		})
	}
}

// TestR3GateF1SubscriptionPipeline：整份订阅四格式端到端——存量非
// 规范 key（直接构造 Proxy 行，等价 DB 直写行）渲染后规范可用。
func TestR3GateF1SubscriptionPipeline(t *testing.T) {
	raw32 := r3b2RawKey(32)
	want := base64.StdEncoding.EncodeToString(raw32)
	eps := []Endpoint{gateF1Endpoint(r3b2C256, base64.RawURLEncoding.EncodeToString(raw32))}

	body, _, err := Subscription(FormatSingbox, eps, SubOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Outbounds []map[string]interface{} `json:"outbounds"`
	}
	if err := json.Unmarshal([]byte(body), &cfg); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, o := range cfg.Outbounds {
		if o["type"] == "shadowsocks" {
			found = true
			if o["password"] != want {
				t.Fatalf("sing-box 订阅密码须规范化：got %v want %q", o["password"], want)
			}
		}
	}
	if !found {
		t.Fatalf("sing-box 订阅缺 ss 出站：%s", body)
	}

	body, _, err = Subscription(FormatClash, eps, SubOptions{})
	if err != nil || !strings.Contains(body, want) {
		t.Fatalf("clash 订阅须含规范化密码：err=%v body=%q", err, body)
	}

	body, _, err = Subscription(FormatSurge, eps, SubOptions{})
	if err != nil || !strings.Contains(body, "password="+want) {
		t.Fatalf("surge 订阅须含规范化密码：err=%v body=%q", err, body)
	}

	body, _, err = Subscription(FormatBase64, eps, SubOptions{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), url.QueryEscape(want)) {
		t.Fatalf("base64 通用格式解码后须含规范化 ss 链接：%q", raw)
	}
}

// TestR3GateF1BadAndMultiKeepExistingShape：存量坏 key 口径——
// sing-box 面略过；Clash 面 R6 批 1 起同口径略过（R6X-002，原「原样
// 输出」断言 supersede，见函数内注）；Surge / ss 链接对坏 key 亦同
// 批设门略过（未验证面）。多段：R4-SIP023-R 起 sing-box 面与 Clash
// 面同口径逐段归一后下发（本断言随该裁定更新，原「保守略过」断言
// supersede）；合法多段在 Surge / ss 链接维持原样。
func TestR3GateF1BadAndMultiKeepExistingShape(t *testing.T) {
	raw32 := r3b2RawKey(32)
	want := base64.StdEncoding.EncodeToString(raw32)
	multi := want + ":" + base64.RawURLEncoding.EncodeToString(raw32)

	bad := gateF1Endpoint(r3b2C256, "plainpassword")
	if _, ok := proxySingbox(bad); ok {
		t.Fatal("存量坏 key 在 sing-box 面必须略过（B2 既有口径）")
	}
	// R6 批 1 口径更新（R6X-002 / BC-R6-02）：Clash 面坏 key 改为与
	// sing-box 面同口径整节点略过（原 B2「clash 面原样输出」口径
	// supersede——该口径下坏存量毒化 mihomo 整份订阅，R6-B/D/L 三
	// 域实证立案）。本断言随裁定更新。
	if _, ok := proxyClash(bad); ok {
		t.Fatal("存量坏 key 在 clash 面必须略过（R6X-002：与 sing-box 面同口径）")
	}

	me := gateF1Endpoint(r3b2C256, multi)
	if outs, ok := proxySingbox(me); !ok || len(outs) != 1 || outs[0]["password"] != want+":"+want {
		t.Fatalf("多段在 sing-box 面须逐段归一后下发（R4-SIP023-R）：ok=%v outs=%v", ok, outs)
	}
	if node, ok := proxyClash(me); !ok || Proxy(yamlToMap(node)).str("password") != want+":"+want {
		t.Fatalf("多段在 clash 面须逐段归一：ok=%v node=%v", ok, node)
	}
	if line, ok := proxySurge(me); !ok || !strings.Contains(line, "password="+multi) {
		t.Fatalf("多段在 surge 面原样输出（裁定口径）：%q ok=%v", line, ok)
	}
	uri, ok := proxyURI(me)
	if !ok || !strings.Contains(uri, ":"+url.QueryEscape(multi)+"@") {
		t.Fatalf("多段在 ss 链接原样输出（裁定口径）：%q ok=%v", uri, ok)
	}
}

// TestR3GateF1BuiltinInboundFaces：自建入站存量非规范 key（直接构造
// Inbound 行，绕 Normalize 的直写存量）在四订阅面渲染后规范化；
// sing-box 面规范化或原样、绝不略过（与导入节点面的略过口径不同，
// 自建面既有行为是恒渲染）。
func TestR3GateF1BuiltinInboundFaces(t *testing.T) {
	raw32 := r3b2RawKey(32)
	want := base64.StdEncoding.EncodeToString(raw32)
	e := Endpoint{Name: "b1", Host: "203.0.113.10", Port: 8388, Direct: true,
		Inbound: Inbound{ID: 9, Protocol: ProtocolShadowsocks, Port: 8388,
			Settings: Settings{Method: r3b2C256, Password: base64.RawURLEncoding.EncodeToString(raw32)}}}

	uri, ok := ShareURI(e)
	if !ok || !strings.Contains(uri, ":"+url.QueryEscape(want)+"@") {
		t.Fatalf("自建 ss 链接须规范化：%q ok=%v", uri, ok)
	}
	node, ok := ClashProxy(e)
	if !ok {
		t.Fatal("自建 clash 渲染失败")
	}
	m := Proxy(yamlToMap(node))
	if m.str("password") != want {
		t.Fatalf("自建 clash 须规范化：got %q want %q", m.str("password"), want)
	}
	o, ok := singboxOutbound(e)
	if !ok || o["password"] != want {
		t.Fatalf("自建 sing-box 须规范化且不略过：ok=%v o=%v", ok, o)
	}
	line, ok := SurgeLine(e)
	if !ok || !strings.Contains(line, "password="+want) {
		t.Fatalf("自建 surge 须规范化：%q ok=%v", line, ok)
	}
	// Loon/QX 自建路径经 ClashProxy 中转，同一规范化结果。
	p, ok := endpointProxy(e)
	if !ok {
		t.Fatal("endpointProxy 自建失败")
	}
	if ll, ok := loonLine(p); !ok || !strings.Contains(ll, want) {
		t.Fatalf("自建 loon 须规范化：%q ok=%v", ll, ok)
	}
	if ql, ok := qxLine(p); !ok || !strings.Contains(ql, "password="+want) {
		t.Fatalf("自建 qx 须规范化：%q ok=%v", ql, ok)
	}
}
