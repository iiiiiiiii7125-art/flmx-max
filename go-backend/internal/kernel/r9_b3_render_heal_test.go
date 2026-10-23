package kernel

import (
	"encoding/json"
	"strings"
	"testing"
)

// R9 批 3（R9X-004）：链式逐段归一同型残余——大小写变体四字段
// （VMess cipher / VLESS flow / Hysteria2 obfs / TUIC
// congestion-controller）无渲染愈合，存量/旧备份来源原样透传。
// 口径（R9-X-verdicts §2 写死）：forEndpoint 克隆一处收口小写愈合
// （同 SS cipher 的 R4X-001 口径），存储不动、非 BC；一处同时覆盖
// 链式双面（Xray 经 chainTargetFromProxy、sing-box 经
// singboxOutbounds→proxySingbox）与订阅面（singboxProfile /
// clashProfile 经 SingboxOutbounds / ClashProxy 共用同一
// proxySingbox / proxyClash → forEndpoint 构造点，实证见批报告）。
// 关闭断言分面登记（不得互替）：
//   - 链式面：TestR9B3ChainFourFieldVariantRenderHeal
//   - 订阅面：TestR9B3SubscriptionFourFieldVariantRenderHeal
// 防过修：合法小写存量逐字不变（TestR9B3LegalLowercaseVerbatim），
// 且原始 Proxy 存储值在渲染后仍为原大写（渲染克隆不回写）。

const r9b3UUID = "11111111-2222-3333-4444-555555555555"

func r9b3StockEndpoints() []Endpoint {
	return []Endpoint{
		{Name: "vm-up", Host: "203.0.113.10", Port: 443, Direct: true,
			Proxy: Proxy{"type": "vmess", "server": "203.0.113.10", "port": 443,
				"uuid": r9b3UUID, "alterId": 0, "cipher": "AES-128-GCM"}},
		{Name: "vl-up", Host: "203.0.113.11", Port: 443, Direct: true,
			Proxy: Proxy{"type": "vless", "server": "203.0.113.11", "port": 443,
				"uuid": r9b3UUID, "flow": "XTLS-RPRX-VISION"}},
		{Name: "hy2-up", Host: "203.0.113.12", Port: 443, Direct: true,
			Proxy: Proxy{"type": "hysteria2", "server": "203.0.113.12", "port": 443,
				"password": "pw", "obfs": "Salamander", "obfs-password": "obpw"}},
		{Name: "tuic-up", Host: "203.0.113.13", Port: 443, Direct: true,
			Proxy: Proxy{"type": "tuic", "server": "203.0.113.13", "port": 443,
				"uuid": r9b3UUID, "password": "pw", "congestion-controller": "BBR"}},
	}
}

func r9b3LegalEndpoints() []Endpoint {
	return []Endpoint{
		{Name: "vm-low", Host: "203.0.113.10", Port: 443, Direct: true,
			Proxy: Proxy{"type": "vmess", "server": "203.0.113.10", "port": 443,
				"uuid": r9b3UUID, "alterId": 0, "cipher": "aes-128-gcm"}},
		{Name: "vl-low", Host: "203.0.113.11", Port: 443, Direct: true,
			Proxy: Proxy{"type": "vless", "server": "203.0.113.11", "port": 443,
				"uuid": r9b3UUID, "flow": "xtls-rprx-vision"}},
		{Name: "hy2-low", Host: "203.0.113.12", Port: 443, Direct: true,
			Proxy: Proxy{"type": "hysteria2", "server": "203.0.113.12", "port": 443,
				"password": "pw", "obfs": "salamander", "obfs-password": "obpw"}},
		{Name: "tuic-low", Host: "203.0.113.13", Port: 443, Direct: true,
			Proxy: Proxy{"type": "tuic", "server": "203.0.113.13", "port": 443,
				"uuid": r9b3UUID, "password": "pw", "congestion-controller": "bbr"}},
	}
}

// xrayChainField 从 Xray 链式出站的 users[0] 取字段。
func r9b3XrayUserField(t *testing.T, e Endpoint, key string) string {
	t.Helper()
	ob, err := xrayChainOutbound(e, "probe")
	if err != nil {
		t.Fatalf("xrayChainOutbound(%s) 须成功：%v", e.Name, err)
	}
	settings, _ := ob["settings"].(obj)
	vnext, _ := settings["vnext"].([]interface{})
	if len(vnext) != 1 {
		t.Fatalf("xrayChainOutbound(%s) vnext 形态异常：%v", e.Name, ob)
	}
	users, _ := vnext[0].(obj)["users"].([]interface{})
	if len(users) != 1 {
		t.Fatalf("xrayChainOutbound(%s) users 形态异常：%v", e.Name, ob)
	}
	s, _ := users[0].(obj)[key].(string)
	return s
}

func TestR9B3ChainFourFieldVariantRenderHeal(t *testing.T) {
	eps := r9b3StockEndpoints()

	// sing-box 链式面（singboxChainOutbounds → singboxOutbounds →
	// proxySingbox → forEndpoint）：四格逐字小写。
	wantSB := []struct {
		idx   int
		check func(t *testing.T, o map[string]interface{})
	}{
		{0, func(t *testing.T, o map[string]interface{}) {
			if o["security"] != "aes-128-gcm" {
				t.Fatalf("vmess sing-box 链式 security 须愈合为 aes-128-gcm，got %v", o["security"])
			}
		}},
		{1, func(t *testing.T, o map[string]interface{}) {
			if o["flow"] != "xtls-rprx-vision" {
				t.Fatalf("vless sing-box 链式 flow 须愈合为 xtls-rprx-vision，got %v", o["flow"])
			}
		}},
		{2, func(t *testing.T, o map[string]interface{}) {
			obfs, _ := o["obfs"].(map[string]interface{})
			if obfs["type"] != "salamander" {
				t.Fatalf("hy2 sing-box 链式 obfs.type 须愈合为 salamander，got %v", o["obfs"])
			}
		}},
		{3, func(t *testing.T, o map[string]interface{}) {
			if o["congestion_control"] != "bbr" {
				t.Fatalf("tuic sing-box 链式 congestion_control 须愈合为 bbr，got %v", o["congestion_control"])
			}
		}},
	}
	for _, w := range wantSB {
		obs, err := singboxChainOutbounds(eps[w.idx], "probe", 0, false)
		if err != nil {
			t.Fatalf("singboxChainOutbounds(%s) 须成功：%v", eps[w.idx].Name, err)
		}
		if len(obs) != 1 {
			t.Fatalf("singboxChainOutbounds(%s) 须 1 出站，got %v", eps[w.idx].Name, obs)
		}
		o, _ := obs[0].(obj)
		w.check(t, map[string]interface{}(o))
	}

	// Xray 链式面：仅 vmess / vless 两格有 Xray 链式产物（hy2 / tuic
	// 经 Xray 链式恒为既有显式拒绝「请改用 sing-box 内核」，非毒化
	// 产物面——愈合在其共用 forEndpoint 克隆上游完成，由 sing-box
	// 链式格与订阅格代证该两字段）。
	if got := r9b3XrayUserField(t, eps[0], "security"); got != "aes-128-gcm" {
		t.Fatalf("vmess Xray 链式 security 须愈合为 aes-128-gcm，got %q", got)
	}
	if got := r9b3XrayUserField(t, eps[1], "flow"); got != "xtls-rprx-vision" {
		t.Fatalf("vless Xray 链式 flow 须愈合为 xtls-rprx-vision，got %q", got)
	}
	for _, idx := range []int{2, 3} {
		if _, err := xrayChainOutbound(eps[idx], "probe"); err == nil {
			t.Fatalf("xrayChainOutbounds(%s) 须维持既有显式拒绝", eps[idx].Name)
		}
	}

	// 存储不动：渲染后原始 Proxy 四字段仍为原大写存量值。
	if got := eps[0].Proxy.str("cipher"); got != "AES-128-GCM" {
		t.Fatalf("存储不动：vmess cipher 原值须仍为 AES-128-GCM，got %q", got)
	}
	if got := eps[1].Proxy.str("flow"); got != "XTLS-RPRX-VISION" {
		t.Fatalf("存储不动：vless flow 原值须仍为 XTLS-RPRX-VISION，got %q", got)
	}
	if got := eps[2].Proxy.str("obfs"); got != "Salamander" {
		t.Fatalf("存储不动：hy2 obfs 原值须仍为 Salamander，got %q", got)
	}
	if got := eps[3].Proxy.str("congestion-controller"); got != "BBR" {
		t.Fatalf("存储不动：tuic congestion-controller 原值须仍为 BBR，got %q", got)
	}
}

func TestR9B3SubscriptionFourFieldVariantRenderHeal(t *testing.T) {
	eps := r9b3StockEndpoints()

	// sing-box 订阅面整份产物：四格同愈合、整表 JSON 可解析且四节点齐。
	sb, _, err := Subscription(FormatSingbox, eps, SubOptions{})
	if err != nil {
		t.Fatalf("Subscription(singbox) 须成功：%v", err)
	}
	var parsed struct {
		Outbounds []map[string]interface{} `json:"outbounds"`
	}
	if err := json.Unmarshal([]byte(sb), &parsed); err != nil {
		t.Fatalf("sing-box 订阅整表须可解析：%v", err)
	}
	byTag := map[string]map[string]interface{}{}
	for _, o := range parsed.Outbounds {
		if tag, _ := o["tag"].(string); tag != "" {
			byTag[tag] = o
		}
	}
	for _, name := range []string{"vm-up", "vl-up", "hy2-up", "tuic-up"} {
		if _, ok := byTag[name]; !ok {
			t.Fatalf("sing-box 订阅整表须含节点 %s，tags=%v", name, byTag)
		}
	}
	if got := byTag["vm-up"]["security"]; got != "aes-128-gcm" {
		t.Fatalf("订阅 sing-box vmess security 须愈合，got %v", got)
	}
	if got := byTag["vl-up"]["flow"]; got != "xtls-rprx-vision" {
		t.Fatalf("订阅 sing-box vless flow 须愈合，got %v", got)
	}
	if obfs, _ := byTag["hy2-up"]["obfs"].(map[string]interface{}); obfs["type"] != "salamander" {
		t.Fatalf("订阅 sing-box hy2 obfs.type 须愈合，got %v", byTag["hy2-up"]["obfs"])
	}
	if got := byTag["tuic-up"]["congestion_control"]; got != "bbr" {
		t.Fatalf("订阅 sing-box tuic congestion_control 须愈合，got %v", got)
	}
	for _, up := range []string{"AES-128-GCM", "XTLS-RPRX-VISION", "Salamander", "\"BBR\""} {
		if strings.Contains(sb, up) {
			t.Fatalf("订阅 sing-box 整表不得残留大写变体 %s：%s", up, sb)
		}
	}

	// Clash 订阅面整份产物：四格同愈合（proxyClash 通用键直出，
	// 字段名以 Clash 形态为准）。
	clash, _, err := Subscription(FormatClash, eps, SubOptions{})
	if err != nil {
		t.Fatalf("Subscription(clash) 须成功：%v", err)
	}
	for _, want := range []string{`cipher: "aes-128-gcm"`, `flow: "xtls-rprx-vision"`,
		`obfs: "salamander"`, `congestion-controller: "bbr"`} {
		if !strings.Contains(clash, want) {
			t.Fatalf("订阅 Clash 整表须含愈合值 %q：\n%s", want, clash)
		}
	}
	for _, up := range []string{"AES-128-GCM", "XTLS-RPRX-VISION", "Salamander", "BBR"} {
		if strings.Contains(clash, up) {
			t.Fatalf("订阅 Clash 整表不得残留大写变体 %s：\n%s", up, clash)
		}
	}

	// URI 面（同经 forEndpoint 的 proxyURI）抽查两格。
	if uri, ok := proxyURI(eps[1]); !ok || !strings.Contains(uri, "flow=xtls-rprx-vision") {
		t.Fatalf("vless URI 须含愈合 flow：ok=%v uri=%q", ok, uri)
	}
	if uri, ok := proxyURI(eps[3]); !ok || !strings.Contains(uri, "congestion_control=bbr") {
		t.Fatalf("tuic URI 须含愈合 congestion_control：ok=%v uri=%q", ok, uri)
	}
}

func TestR9B3LegalLowercaseVerbatim(t *testing.T) {
	// 防过修：合法小写存量经链式与订阅两面渲染逐字不变（与变体格
	// 愈合后的产物逐字段相同形态，且不被改写为其他值）。
	eps := r9b3LegalEndpoints()
	for i, e := range eps {
		obs, err := singboxChainOutbounds(e, "probe", 0, false)
		if err != nil || len(obs) != 1 {
			t.Fatalf("合法小写 %s sing-box 链式须成功：err=%v obs=%v", e.Name, err, obs)
		}
		_ = i
	}
	if got := eps[0].Proxy.forEndpoint(eps[0]).str("cipher"); got != "aes-128-gcm" {
		t.Fatalf("合法 vmess cipher 须逐字不变，got %q", got)
	}
	if got := eps[1].Proxy.forEndpoint(eps[1]).str("flow"); got != "xtls-rprx-vision" {
		t.Fatalf("合法 vless flow 须逐字不变，got %q", got)
	}
	if got := eps[2].Proxy.forEndpoint(eps[2]).str("obfs"); got != "salamander" {
		t.Fatalf("合法 hy2 obfs 须逐字不变，got %q", got)
	}
	if got := eps[3].Proxy.forEndpoint(eps[3]).str("congestion-controller"); got != "bbr" {
		t.Fatalf("合法 tuic congestion-controller 须逐字不变，got %q", got)
	}
	// 空值不造值：缺字段节点经 forEndpoint 不得凭空生出四字段。
	bare := Endpoint{Name: "bare", Host: "203.0.113.20", Port: 443, Direct: true,
		Proxy: Proxy{"type": "tuic", "server": "203.0.113.20", "port": 443,
			"uuid": r9b3UUID, "password": "pw"}}
	if got := bare.Proxy.forEndpoint(bare).str("congestion-controller"); got != "" {
		t.Fatalf("空 congestion-controller 不得造值，got %q", got)
	}
	// 垃圾值小写化后仍是垃圾值、仍被谓词拒（愈合不扩大接受集）。
	garbage := Endpoint{Name: "g", Host: "203.0.113.21", Port: 443, Direct: true,
		Proxy: Proxy{"type": "vmess", "server": "203.0.113.21", "port": 443,
			"uuid": r9b3UUID, "cipher": "AES-128-CTR"}}
	healed := garbage.Proxy.forEndpoint(garbage)
	if healed.str("cipher") != "aes-128-ctr" || ValidVMessCipher(healed.str("cipher")) {
		t.Fatalf("垃圾 cipher 小写化后仍须被谓词拒：healed=%q valid=%v",
			healed.str("cipher"), ValidVMessCipher(healed.str("cipher")))
	}
	if _, ok := proxySingbox(garbage); ok {
		t.Fatal("垃圾 cipher 存量 sing-box 面须整节点略过（愈合不放行垃圾）")
	}
}
