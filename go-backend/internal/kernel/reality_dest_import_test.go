// L5-F-01：导入面 realityDest / spx 静默丢弃。
// 分享链接的 Reality 参数此前只读 pbk/sid，realityDest（目标地址）与
// spx（spiderX）直接丢弃：坏值无声消失、合法值也落不了库，与自建面
// normalizeSecurity 的 L1-F-01 校验宣称不一致。本文件钉死导入面的
// 接入语义：坏值整条拒收（与自建面同文案）、合法值落库并可回放。
package kernel

import (
	"strings"
	"testing"
)

// l5f01RealityLink 拼一条 VLESS Reality 分享链接，extra 为附加 query 参数。
func l5f01RealityLink(extra string) string {
	return "vless://" + manualUUID + "@203.0.113.10:8444?encryption=none&security=reality" +
		"&sni=example.com&fp=chrome&pbk=" + manualFixPBK + "&sid=ab12&type=tcp" + extra + "#l5f01"
}

func TestRealityDestImportBad(t *testing.T) {
	cases := []struct {
		name string
		link string
		want string
	}{
		// L5-F-01 原案：空标签 dest，须与创建面逐字同错。
		{"empty-label", l5f01RealityLink("&realityDest=a..b:443"), `Reality 目标地址不正确："a..b:443"`},
		{"empty-label-3dots", l5f01RealityLink("&realityDest=a...b:443"), `Reality 目标地址不正确："a...b:443"`},
		{"no-port", l5f01RealityLink("&realityDest=example.com"), "Reality 目标地址格式应为 host:port"},
		{"empty-host", l5f01RealityLink("&realityDest=:443"), "Reality 目标地址格式应为 host:port"},
		{"port-zero", l5f01RealityLink("&realityDest=example.com:0"), "Reality 目标端口错误"},
		{"port-too-big", l5f01RealityLink("&realityDest=example.com:70000"), "Reality 目标端口错误"},
		{"port-nonnumeric", l5f01RealityLink("&realityDest=example.com:abc"), "Reality 目标端口错误"},
		// Trojan 同走 parseUserURI 的 Reality 分支，同口径。
		{"trojan-empty-label", "trojan://secretpass@203.0.113.10:8444?security=reality&sni=example.com&fp=chrome&pbk=" + manualFixPBK + "&sid=ab12&realityDest=a..b:443#l5f01", `Reality 目标地址不正确："a..b:443"`},
		// spx 含空白字符：URL 路径段不可能含空白，拒收。
		{"spx-space", l5f01RealityLink("&realityDest=example.com:443&spx=/a%20b"), "Reality spiderX 格式错误"},
	}
	for _, c := range cases {
		nodes, errs := ParseImport(c.link)
		if len(nodes) != 0 || len(errs) == 0 {
			t.Errorf("%s: ParseImport = %d nodes, errs=%v, want 0 nodes with error", c.name, len(nodes), errs)
			continue
		}
		if !strings.Contains(errs[0], c.want) {
			t.Errorf("%s: err = %q, want contains %q", c.name, errs[0], c.want)
		}
	}
	// 坏行不连坐其余行：坏 dest 行被拒、合法行照常建成。
	nodes, errs := ParseImport(l5f01RealityLink("&realityDest=a..b:443") + "\n" + l5f01RealityLink("&realityDest=example.com:443"))
	if len(nodes) != 1 || len(errs) != 1 {
		t.Fatalf("mixed batch: nodes=%d errs=%v, want 1 node + 1 err", len(nodes), errs)
	}
	if got := nodes[0].Proxy.sub("reality-opts").str("dest"); got != "example.com:443" {
		t.Fatalf("mixed batch survivor dest = %q, want %q", got, "example.com:443")
	}
}

func TestRealityDestImportGood(t *testing.T) {
	n := mustImportOne(t, l5f01RealityLink("&realityDest=example.com:443&spx=/"))
	ro := n.Proxy.sub("reality-opts")
	if got := ro.str("dest"); got != "example.com:443" {
		t.Fatalf("reality-opts.dest = %q, want %q", got, "example.com:443")
	}
	if got := ro.str("spider-x"); got != "/" {
		t.Fatalf("reality-opts.spider-x = %q, want %q", got, "/")
	}
	// importedParams（ProxyParams 通用遍历）须可见两字段：L5-F-01 的
	// 原判据就是 importedParams 中无 realityDest 条目。
	params := ProxyParams(n.Proxy)
	wantParams := map[string]string{"reality-opts.dest": "example.com:443", "reality-opts.spider-x": "/"}
	for k, want := range wantParams {
		found := false
		for _, kv := range params {
			if kv[0] == k && kv[1] == want {
				found = true
			}
		}
		if !found {
			t.Errorf("ProxyParams missing %q=%q: %v", k, want, params)
		}
	}
	// 未带 realityDest/spx 的旧链接行为逐字不变：两键均不落库。
	old := mustImportOne(t, l5f01RealityLink(""))
	oldRO := old.Proxy.sub("reality-opts")
	if _, ok := oldRO["dest"]; ok {
		t.Errorf("legacy link must not gain reality-opts.dest: %v", oldRO)
	}
	if _, ok := oldRO["spider-x"]; ok {
		t.Errorf("legacy link must not gain reality-opts.spider-x: %v", oldRO)
	}
	// 分享链接回放：realityDest/spx 须原样回到链接里，再导入一轮仍在。
	e := Endpoint{Name: "l5f01", Host: "203.0.113.10", Port: 8444, Direct: true, Proxy: n.Proxy}
	uri, ok := proxyURI(e)
	if !ok {
		t.Fatal("proxyURI must succeed for the imported Reality node")
	}
	if !strings.Contains(uri, "realityDest=example.com%3A443") || !strings.Contains(uri, "spx=%2F") {
		t.Fatalf("round-trip URI must carry realityDest + spx: %q", uri)
	}
	back := mustImportOne(t, uri)
	if got := back.Proxy.sub("reality-opts").str("dest"); got != "example.com:443" {
		t.Fatalf("round-trip dest = %q, want %q", got, "example.com:443")
	}
	if got := back.Proxy.sub("reality-opts").str("spider-x"); got != "/" {
		t.Fatalf("round-trip spider-x = %q, want %q", got, "/")
	}
	// Xray 链式面：dest/spiderX 透传入 realitySettings（Xray 客户端
	// realitySettings 的同名字段）；pbk/sid 既有行为不变。
	ct, err := chainTargetFromProxy(e)
	if err != nil {
		t.Fatalf("chainTargetFromProxy: %v", err)
	}
	if ct.RealityDest != "example.com:443" || ct.RealitySpiderX != "/" {
		t.Fatalf("chainTarget dest/spiderX = %q/%q, want %q/%q", ct.RealityDest, ct.RealitySpiderX, "example.com:443", "/")
	}
	ob, err := xrayChainOutbound(e, "via-1", 0)
	if err != nil {
		t.Fatalf("xrayChainOutbound: %v", err)
	}
	rs := ob["streamSettings"].(map[string]interface{})["realitySettings"].(map[string]interface{})
	if rs["dest"] != "example.com:443" || rs["spiderX"] != "/" {
		t.Fatalf("xray realitySettings dest/spiderX = %v/%v, want example.com:443 /", rs["dest"], rs["spiderX"])
	}
	if rs["publicKey"] != manualFixPBK || rs["shortId"] != "ab12" {
		t.Fatalf("xray realitySettings pbk/sid changed: %v", rs)
	}
	// 自建入站的 RealityDest 是服务端伪装目标（probe 转发去向），不
	// 得泄漏进客户端 dest：chainTargetFromInbound 须保持不带 dest。
	self := Endpoint{Name: "self", Host: "203.0.113.20", Port: 443, Direct: true,
		Inbound: Inbound{Protocol: ProtocolVLESS, Port: 443,
			Settings:  Settings{UUID: manualUUID},
			Transport: Transport{Type: TransportTCP},
			Security:  Security{Type: SecurityReality, SNI: "www.example.com", RealityDest: "www.example.com:443", RealityPublicKey: manualFixPBK, RealityShortIDs: []string{"ab12"}, Fingerprint: "chrome"}}}
	selfCT, err := chainTargetFromInbound(self)
	if err != nil {
		t.Fatalf("chainTargetFromInbound: %v", err)
	}
	if selfCT.RealityDest != "" || selfCT.RealitySpiderX != "" {
		t.Fatalf("self-built chain target must not carry client dest/spiderX: %+v", selfCT)
	}
}

// TestRealityDestRestorePredicate：恢复面与导入面共用 validateRealityOpts，
// 备份里带坏 dest 的导入节点行同样须拒（ValidateImportedProxy 口径）。
func TestRealityDestRestorePredicate(t *testing.T) {
	n := mustImportOne(t, l5f01RealityLink("&realityDest=example.com:443"))
	if err := ValidateImportedProxy(n.Proxy); err != nil {
		t.Fatalf("ValidateImportedProxy(good dest) = %v, want nil", err)
	}
	bad := n.Proxy.clone()
	bad.sub("reality-opts")["dest"] = "a..b:443"
	if err := ValidateImportedProxy(bad); err == nil || !strings.Contains(err.Error(), `Reality 目标地址不正确："a..b:443"`) {
		t.Errorf("ValidateImportedProxy(bad dest) = %v, want Reality 目标地址不正确", err)
	}
	// 渲染面谓词同样共用：坏 dest 存量在各订阅面须整节点略过/不过。
	if validImportedProxyFields(bad, ssFaceOther) {
		t.Error("validImportedProxyFields(bad dest) = true, want false")
	}
}

// TestRealityLinkFieldsSweep：导入解析器 Reality/TLS 字段复扫——
// 本轮接入 realityDest/spx 后，链接上其余同族字段须逐个在位（防修
// 一漏一）。以代码为准的枚举见 lane-l5f01-report.md §复扫表。
func TestRealityLinkFieldsSweep(t *testing.T) {
	link := "vless://" + manualUUID + "@203.0.113.10:8444?encryption=none&security=reality" +
		"&sni=example.com&fp=chrome&pbk=" + manualFixPBK + "&sid=ab12" +
		"&realityDest=example.com:443&spx=/&alpn=h2%2Chttp%2F1.1&allowInsecure=1" +
		"&flow=xtls-rprx-vision&type=tcp#sweep"
	n := mustImportOne(t, link)
	p, ro := n.Proxy, n.Proxy.sub("reality-opts")
	checks := map[string]string{
		"reality-opts.public-key": manualFixPBK,
		"reality-opts.short-id":   "ab12",
		"reality-opts.dest":       "example.com:443",
		"reality-opts.spider-x":   "/",
		"servername":              "example.com",
		"client-fingerprint":      "chrome",
		"flow":                    "xtls-rprx-vision",
		"encryption":              "none",
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
	if !p.flag("tls") {
		t.Error("security=reality must set tls")
	}
}
