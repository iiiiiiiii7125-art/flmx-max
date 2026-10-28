// R2-L3-F-01：VMess JSON `tls=reality` 静默降级为明文 VMess。
// parseVMessURI 只认 tls∈{tls,1,true}，tls=reality 不进分支、无错误、
// sni/fp/alpn 一并丢弃，节点“导入成功”却永远连不上 Reality 服务端。
// 修复方向：接入（与 sing-box 入口同口径）——sing-box 入口实测接纳
// VMess+Reality 并透传 reality-opts，共享谓词 validateRealityOpts
// 亦接纳（manual_node_fix_test.go:98-110），sing-box 订阅渲染
// （proxySingbox 通用 tls() helper）、Clash 透传渲染、Xray 链式
// （chainTargetFromProxy 协议无关 realitySettings）三面均已支持；
// 仅链接面解析与 VMess 分享链接回放两处缺口。本文件先红后绿钉死：
// 合法 Reality 值落库且各面在位、坏值走 Reality 校验拒收、既有
// tls 取值行为不变、未识别的 tls 值明拒（不再静默降级）。
package kernel

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// r2vmessLink 拼一条 v2rayN VMess JSON 链接；overrides 为 nil 表示
// 删除该键，其余值覆盖默认值。默认即合法 Reality 节点。
func r2vmessLink(overrides map[string]interface{}) string {
	base := map[string]interface{}{
		"v": "2", "ps": "vm-reality", "add": "203.0.113.10", "port": "443",
		"id": manualUUID, "aid": "0", "scy": "auto",
		"net": "tcp", "type": "none", "host": "", "path": "",
		"tls": "reality", "sni": "example.com", "fp": "chrome", "alpn": "h2",
		"pbk": manualFixPBK, "sid": "ab12", "spx": "/",
		"realityDest": "example.com:443",
	}
	for k, v := range overrides {
		if v == nil {
			delete(base, k)
		} else {
			base[k] = v
		}
	}
	b, _ := json.Marshal(base)
	return "vmess://" + base64.StdEncoding.EncodeToString(b)
}

func TestVMessRealityImportGood(t *testing.T) {
	n := mustImportOne(t, r2vmessLink(nil))
	p, ro := n.Proxy, n.Proxy.sub("reality-opts")
	if !p.flag("tls") {
		t.Error("tls=reality must set tls")
	}
	if got := p.serverName(); got != "example.com" {
		t.Errorf("servername = %q, want %q", got, "example.com")
	}
	if got := p.str("client-fingerprint"); got != "chrome" {
		t.Errorf("client-fingerprint = %q, want %q", got, "chrome")
	}
	if got := p.strs("alpn"); len(got) != 1 || got[0] != "h2" {
		t.Errorf("alpn = %v, want [h2]", got)
	}
	if got := ro.str("public-key"); got != manualFixPBK {
		t.Errorf("reality-opts.public-key = %q, want pbk", got)
	}
	if got := ro.str("short-id"); got != "ab12" {
		t.Errorf("reality-opts.short-id = %q, want %q", got, "ab12")
	}
	if got := ro.str("spider-x"); got != "/" {
		t.Errorf("reality-opts.spider-x = %q, want %q", got, "/")
	}
	if got := ro.str("dest"); got != "example.com:443" {
		t.Errorf("reality-opts.dest = %q, want %q", got, "example.com:443")
	}
	if got := DescribeProxy(p); got != "VMess + Reality" {
		t.Errorf("DescribeProxy = %q, want %q", got, "VMess + Reality")
	}
	// importedParams 须可见 Reality 各字段（L5-F-01 同款判据）。
	params := ProxyParams(p)
	for _, want := range [][2]string{
		{"reality-opts.public-key", manualFixPBK},
		{"reality-opts.short-id", "ab12"},
		{"reality-opts.spider-x", "/"},
		{"reality-opts.dest", "example.com:443"},
		{"servername", "example.com"},
		{"client-fingerprint", "chrome"},
	} {
		found := false
		for _, kv := range params {
			if kv == want {
				found = true
			}
		}
		if !found {
			t.Errorf("ProxyParams missing %q=%q: %v", want[0], want[1], params)
		}
	}
}

func TestVMessRealityImportBad(t *testing.T) {
	cases := []struct {
		name      string
		overrides map[string]interface{}
		want      string
	}{
		// 坏 realityDest 走 validateRealityOpts（L1-F-01 同文案）。
		{"bad-dest-empty-label", map[string]interface{}{"realityDest": "a..b:443"}, `Reality 目标地址不正确："a..b:443"`},
		{"bad-dest-no-port", map[string]interface{}{"realityDest": "example.com"}, "Reality 目标地址格式应为 host:port"},
		// 缺 pbk：解析期明拒，点名 pbk（与 VLESS 链接面同款文案）。
		{"missing-pbk", map[string]interface{}{"pbk": nil}, "pbk"},
		// 坏 pbk：共享谓词拒收。
		{"bad-pbk", map[string]interface{}{"pbk": "not-base64!!!"}, "Reality 公钥 pbk 格式错误"},
		// 坏 sid：共享谓词拒收。
		{"bad-sid", map[string]interface{}{"sid": "zz"}, "Reality short-id 格式错误"},
		// 缺 SNI：共享谓词拒收（F-02 同款）。
		{"missing-sni", map[string]interface{}{"sni": nil}, "Reality 须填写 SNI"},
		// spx 含空白：共享谓词拒收。
		{"bad-spx", map[string]interface{}{"spx": "/a b"}, "Reality spiderX 格式错误"},
		// 坏指纹：横切 R10X-001 拒收，不得静默落库。
		{"bad-fp", map[string]interface{}{"fp": "garbage-not-a-fp"}, "client-fingerprint 不受支持"},
	}
	for _, c := range cases {
		nodes, errs := ParseImport(r2vmessLink(c.overrides))
		if len(nodes) != 0 || len(errs) == 0 {
			t.Errorf("%s: ParseImport = %d nodes, errs=%v, want 0 nodes with error", c.name, len(nodes), errs)
			continue
		}
		if !strings.Contains(errs[0], c.want) {
			t.Errorf("%s: err = %q, want contains %q", c.name, errs[0], c.want)
		}
	}
	// 坏行不连坐：坏 dest 行被拒、合法行照常建成。
	nodes, errs := ParseImport(r2vmessLink(map[string]interface{}{"realityDest": "a..b:443"}) + "\n" + r2vmessLink(nil))
	if len(nodes) != 1 || len(errs) != 1 {
		t.Fatalf("mixed batch: nodes=%d errs=%v, want 1 node + 1 err", len(nodes), errs)
	}
	if got := nodes[0].Proxy.sub("reality-opts").str("dest"); got != "example.com:443" {
		t.Fatalf("mixed batch survivor dest = %q, want %q", got, "example.com:443")
	}
}

// TestVMessTLSValuesRegression：既有 tls 取值行为不变。
func TestVMessTLSValuesRegression(t *testing.T) {
	// tls=tls/1/true：TLS 节点，sni/fp/alpn 落库，无 reality-opts。
	for _, v := range []string{"tls", "1", "true"} {
		n := mustImportOne(t, r2vmessLink(map[string]interface{}{
			"tls": v, "pbk": nil, "sid": nil, "spx": nil, "realityDest": nil,
		}))
		if !n.Proxy.flag("tls") {
			t.Errorf("tls=%q: must set tls", v)
		}
		if len(n.Proxy.sub("reality-opts")) != 0 {
			t.Errorf("tls=%q: must not gain reality-opts: %v", v, n.Proxy.sub("reality-opts"))
		}
		if got := n.Proxy.serverName(); got != "example.com" {
			t.Errorf("tls=%q: servername = %q, want example.com", v, got)
		}
		if got := n.Proxy.strs("alpn"); len(got) != 1 || got[0] != "h2" {
			t.Errorf("tls=%q: alpn = %v, want [h2]", v, got)
		}
	}
	// tls 缺失/空/none/0/false：明文节点，无 TLS 参数落库。
	for name, ov := range map[string]map[string]interface{}{
		"missing": {"tls": nil, "pbk": nil, "sid": nil, "spx": nil, "realityDest": nil, "sni": nil, "fp": nil, "alpn": nil},
		"empty":   {"tls": "", "pbk": nil, "sid": nil, "spx": nil, "realityDest": nil, "sni": nil, "fp": nil, "alpn": nil},
		"none":    {"tls": "none", "pbk": nil, "sid": nil, "spx": nil, "realityDest": nil, "sni": nil, "fp": nil, "alpn": nil},
		"zero":    {"tls": "0", "pbk": nil, "sid": nil, "spx": nil, "realityDest": nil, "sni": nil, "fp": nil, "alpn": nil},
		"false":   {"tls": "false", "pbk": nil, "sid": nil, "spx": nil, "realityDest": nil, "sni": nil, "fp": nil, "alpn": nil},
	} {
		n := mustImportOne(t, r2vmessLink(ov))
		if n.Proxy.flag("tls") || len(n.Proxy.sub("reality-opts")) != 0 {
			t.Errorf("tls=%s: plaintext node must not gain tls/reality-opts: %v", name, n.Proxy)
		}
		if got := n.Proxy.serverName(); got != "" {
			t.Errorf("tls=%s: plaintext node must not keep servername %q", name, got)
		}
	}
}

// TestVMessTLSUnknownRejected：未识别的 tls 值须明拒，不得静默降级
// 为明文（原案机制：白名单 if 无 else 拒，xtls 等值无声消失）。
func TestVMessTLSUnknownRejected(t *testing.T) {
	for _, v := range []string{"xtls", "bogus", "TLS2"} {
		nodes, errs := ParseImport(r2vmessLink(map[string]interface{}{"tls": v}))
		if len(nodes) != 0 || len(errs) == 0 {
			t.Errorf("tls=%q: ParseImport = %d nodes, errs=%v, want 0 nodes with error", v, len(nodes), errs)
			continue
		}
		if !strings.Contains(errs[0], v) || !strings.Contains(errs[0], "tls") {
			t.Errorf("tls=%q: err = %q, want it to name the tls value", v, errs[0])
		}
	}
}

// TestVMessRealityRoundTrip：分享链接回放须含 Reality 参数，再导入
// 一轮参数逐字不变（与 L5-F-01 的 realityDest/spx 回放同口径）。
func TestVMessRealityRoundTrip(t *testing.T) {
	n := mustImportOne(t, r2vmessLink(nil))
	e := Endpoint{Name: "vm-reality", Host: "203.0.113.10", Port: 443, Direct: true, Proxy: n.Proxy}
	uri, ok := proxyURI(e)
	if !ok {
		t.Fatal("proxyURI must succeed for the imported VMess Reality node")
	}
	if !strings.HasPrefix(uri, "vmess://") {
		t.Fatalf("round-trip URI must be vmess://, got %q", uri)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, "vmess://"))
	if err != nil {
		t.Fatalf("round-trip URI is not base64 JSON: %v", err)
	}
	var v map[string]interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("round-trip URI is not JSON: %v", err)
	}
	for k, want := range map[string]string{
		"tls": "reality", "sni": "example.com", "fp": "chrome", "alpn": "h2",
		"pbk": manualFixPBK, "sid": "ab12", "spx": "/", "realityDest": "example.com:443",
	} {
		if got, _ := v[k].(string); got != want {
			t.Errorf("round-trip JSON %s = %q, want %q (full: %v)", k, got, want, v)
		}
	}
	back := mustImportOne(t, uri)
	if !reflect.DeepEqual(normalized(back.Proxy), normalized(n.Proxy)) {
		t.Fatalf("round trip changed the node:\n%v\n%v", n.Proxy, back.Proxy)
	}
}

// TestVMessRealityRenderFaces：订阅三面须透传 Reality，不得回落或丢弃。
func TestVMessRealityRenderFaces(t *testing.T) {
	n := mustImportOne(t, r2vmessLink(nil))
	e := Endpoint{Name: "vm-reality", Host: "203.0.113.10", Port: 443, Direct: true, Proxy: n.Proxy}
	// sing-box 面：tls.reality.enabled + public_key/short_id + server_name。
	obs, ok := proxySingbox(e)
	if !ok || len(obs) == 0 {
		t.Fatalf("proxySingbox = %v, ok=%v, want one outbound", obs, ok)
	}
	tlsBlock, _ := obs[0]["tls"].(map[string]interface{})
	if tlsBlock == nil {
		t.Fatalf("sing-box outbound has no tls block: %v", obs[0])
	}
	if tlsBlock["server_name"] != "example.com" {
		t.Errorf("sing-box server_name = %v, want example.com", tlsBlock["server_name"])
	}
	reality, _ := tlsBlock["reality"].(map[string]interface{})
	if reality == nil || reality["enabled"] != true || reality["public_key"] != manualFixPBK || reality["short_id"] != "ab12" {
		t.Errorf("sing-box reality block = %v, want enabled + pbk + sid ab12", reality)
	}
	// Clash 面：reality-opts / servername / client-fingerprint 逐键透传。
	node, ok := proxyClash(e)
	if !ok {
		t.Fatal("proxyClash must succeed for the VMess Reality node")
	}
	clashFields := map[string]interface{}{}
	for _, f := range node {
		clashFields[f.Key] = f.Value
	}
	ro, _ := clashFields["reality-opts"].(map[string]interface{})
	if ro == nil || ro["public-key"] != manualFixPBK || ro["short-id"] != "ab12" {
		t.Errorf("clash reality-opts = %v, want pbk + sid ab12", clashFields["reality-opts"])
	}
	if clashFields["servername"] != "example.com" || clashFields["client-fingerprint"] != "chrome" {
		t.Errorf("clash servername/fp = %v/%v, want example.com/chrome", clashFields["servername"], clashFields["client-fingerprint"])
	}
	// Xray 链式面：realitySettings 含 pbk/sid/dest/spiderX。
	ct, err := chainTargetFromProxy(e)
	if err != nil {
		t.Fatalf("chainTargetFromProxy: %v", err)
	}
	if ct.Security != SecurityReality || ct.RealityPublic != manualFixPBK || ct.RealityShortID != "ab12" ||
		ct.RealityDest != "example.com:443" || ct.RealitySpiderX != "/" || ct.SNI != "example.com" {
		t.Fatalf("chainTarget = %+v, want SecurityReality + pbk/sid/dest/spx + SNI", ct)
	}
	ob, err := xrayChainOutbound(e, "via-1", 0)
	if err != nil {
		t.Fatalf("xrayChainOutbound: %v", err)
	}
	rs := ob["streamSettings"].(map[string]interface{})["realitySettings"].(map[string]interface{})
	if rs["publicKey"] != manualFixPBK || rs["shortId"] != "ab12" || rs["dest"] != "example.com:443" || rs["spiderX"] != "/" {
		t.Fatalf("xray realitySettings = %v, want pbk/sid/dest/spx", rs)
	}
}

// TestUnknownSecurityRejected：复扫同族残留——VLESS/Trojan 链接
// security 取未识别值时须明拒，不得静默降级为明文（与 VMess JSON
// tls 白名单 if 同机制；此前 security=bogus 建成 1 明文节点 0 错误，
// sni/fp 一并丢弃）。已知值行为不变：none/空=明文，tls/xtls/reality
// 按既有口径。
func TestUnknownSecurityRejected(t *testing.T) {
	for _, link := range []string{
		"vless://" + manualUUID + "@203.0.113.10:443?encryption=none&security=bogus&sni=example.com&fp=chrome&type=tcp#bogus",
		"trojan://pw@203.0.113.10:443?security=bogus&sni=example.com#bogus",
	} {
		nodes, errs := ParseImport(link)
		if len(nodes) != 0 || len(errs) == 0 {
			t.Errorf("%s: ParseImport = %d nodes, errs=%v, want 0 nodes with error", link, len(nodes), errs)
			continue
		}
		if !strings.Contains(errs[0], "bogus") || !strings.Contains(errs[0], "security") {
			t.Errorf("err = %q, want it to name the security value", errs[0])
		}
	}
	// 已知值回归：security=none/空仍为明文，xtls 仍按 TLS + flow 保留。
	n := mustImportOne(t, "vless://"+manualUUID+"@203.0.113.10:443?encryption=none&security=none&type=tcp#none")
	if n.Proxy.flag("tls") {
		t.Error("security=none must stay plaintext")
	}
	n = mustImportOne(t, "vless://"+manualUUID+"@203.0.113.10:443?encryption=none&security=xtls&sni=example.com&flow=xtls-rprx-vision&type=tcp#xtls")
	if !n.Proxy.flag("tls") || n.Proxy.str("flow") != "xtls-rprx-vision" {
		t.Errorf("security=xtls must keep TLS + flow: %v", n.Proxy)
	}
}

// TestVMessRealityFieldsSweep：VMess JSON 全字段复扫——pbk/sid/spx/
// realityDest/sni/fp/alpn/allowInsecure 逐格在位（防修一漏一）。
func TestVMessRealityFieldsSweep(t *testing.T) {
	n := mustImportOne(t, r2vmessLink(map[string]interface{}{"alpn": "h2,http/1.1", "allowInsecure": "1"}))
	p, ro := n.Proxy, n.Proxy.sub("reality-opts")
	checks := map[string]string{
		"reality-opts.public-key": manualFixPBK,
		"reality-opts.short-id":   "ab12",
		"reality-opts.dest":       "example.com:443",
		"reality-opts.spider-x":   "/",
		"servername":              "example.com",
		"client-fingerprint":      "chrome",
	}
	for k, want := range checks {
		var got string
		if strings.HasPrefix(k, "reality-opts.") {
			got = ro.str(strings.TrimPrefix(k, "reality-opts."))
		} else {
			got = p.str(k)
		}
		if got != want {
			t.Errorf("field %s = %q, want %q", k, got, want)
		}
	}
	if got := p.strs("alpn"); len(got) != 2 || got[0] != "h2" || got[1] != "http/1.1" {
		t.Errorf("alpn = %v, want [h2 http/1.1]", got)
	}
	if !p.flag("skip-cert-verify") {
		t.Error("allowInsecure=1 must set skip-cert-verify")
	}
}
