package kernel

import (
	"strings"
	"testing"
)

// R10 批 1（R10X-001/002/003，进口枚举收口）：有限枚举字段在进口零
// 校验、谓词零覆盖、渲染零愈合，坏值原样落库并毒化法定客户端整表/
// 整单（R10-D-01/02/03，X-verdicts §0 升 P1）。口径（FIX-PLAN 批 1）：
//   - R10X-001 client-fingerprint：进口在 finishProxy 一处按 sing-box
//     uTLS 合法集校验 + 小写愈合落库；forEndpoint 存量克隆同口径
//     小写；合法集外整节点略过（并入 validImportedProxyFields）；
//     自建面 normalizeSecurity 同根格同批收口。
//   - R10X-002 xhttp-opts.mode：进口一处校验 + 小写愈合，合法集
//     {auto, packet-up, stream-up, stream-one}、集外拒；forEndpoint
//     存量小写愈合。
//   - R10X-003 SS plugin-opts.mode：plugin=obfs 时 mode 小写愈合 +
//     合法集 {http, tls} 校验（进口与 ssPluginString 消费前同口径）；
//     附带格（断言独立，见 TestR10B1SSPluginNameCaseInsensitive）：
//     ssPluginString 对 plugin 名大小写不敏感匹配。
// 关闭断言三条分开登记（N-35）：各自以本条二进制格为据——毒化形态
// 修复后落库规范值/被拒、产物面不再含毒化值、合法小写样本逐字不变。

const r10b1UUID = "11111111-2222-3333-4444-555555555555"

// ---------- R10X-001 client-fingerprint ----------

func TestR10B1FingerprintImportHealAndReject(t *testing.T) {
	// Clash YAML 通道：Chrome 大写变体愈合落库。
	clash := "proxies:\n  - {name: FP1, type: vless, server: 203.0.113.1, port: 443, uuid: " + r10b1UUID + ", tls: true, servername: a.example.com, client-fingerprint: Chrome}\n"
	n := mustImportOne(t, clash)
	if got := n.Proxy.str("client-fingerprint"); got != "chrome" {
		t.Fatalf("Clash Chrome 须愈合落库为 chrome，got %q", got)
	}
	// sing-box JSON 通道：utls fingerprint FIREFOX 愈合落库。
	sb := `{"outbounds":[{"type":"trojan","tag":"FP2","server":"203.0.113.2","server_port":443,"password":"pw","tls":{"enabled":true,"server_name":"a.example.com","utls":{"enabled":true,"fingerprint":"FIREFOX"}}}]}`
	n = mustImportOne(t, sb)
	if got := n.Proxy.str("client-fingerprint"); got != "firefox" {
		t.Fatalf("sing-box FIREFOX 须愈合落库为 firefox，got %q", got)
	}
	// URI 通道：fp=Safari 愈合落库。
	n = mustImportOne(t, "vless://"+r10b1UUID+"@203.0.113.3:443?encryption=none&security=tls&sni=a.example.com&fp=Safari&type=tcp#FP3")
	if got := n.Proxy.str("client-fingerprint"); got != "safari" {
		t.Fatalf("URI Safari 须愈合落库为 safari，got %q", got)
	}
	// 合法集逐值小写原样落库（防过修）。
	for _, fp := range []string{"chrome", "firefox", "edge", "safari", "360", "qq", "ios", "android", "random", "randomized", "chrome_psk"} {
		node := mustImportOne(t, "proxies:\n  - {name: FP, type: vless, server: 203.0.113.1, port: 443, uuid: "+r10b1UUID+", tls: true, client-fingerprint: "+fp+"}\n")
		if got := node.Proxy.str("client-fingerprint"); got != fp {
			t.Fatalf("合法 fingerprint %q 须逐字落库，got %q", fp, got)
		}
	}
	// 合法集大小写变体逐值愈合（集内成员的大小写形态）。
	for in, want := range map[string]string{"CHROME": "chrome", "FireFox": "firefox", "EDGE": "edge", "IOS": "ios", "Random": "random"} {
		node := mustImportOne(t, "proxies:\n  - {name: FP, type: vless, server: 203.0.113.1, port: 443, uuid: "+r10b1UUID+", tls: true, client-fingerprint: "+in+"}\n")
		if got := node.Proxy.str("client-fingerprint"); got != want {
			t.Fatalf("fingerprint %q 须愈合为 %q，got %q", in, want, got)
		}
	}
	// 垃圾值逐条拒（不连坐同批其余节点由 ParseImport 既有口径保证，
	// 此处单节点断言被拒且回执点名 fingerprint）。
	for _, fp := range []string{"bogus123", "chrome-100"} {
		_, errs := ParseImport("proxies:\n  - {name: FP, type: vless, server: 203.0.113.1, port: 443, uuid: " + r10b1UUID + ", tls: true, client-fingerprint: " + fp + "}\n")
		if len(errs) != 1 || !strings.Contains(errs[0], "fingerprint") {
			t.Fatalf("垃圾 fingerprint %q 须逐条被拒且回执点名，errs=%v", fp, errs)
		}
	}
}

func TestR10B1FingerprintStockHealAndSkip(t *testing.T) {
	// 存量大写：forEndpoint 克隆小写愈合、存储不动。
	e := Endpoint{Name: "fp-stock", Host: "203.0.113.1", Port: 443, Direct: true,
		Proxy: Proxy{"type": "vless", "server": "203.0.113.1", "port": 443,
			"uuid": r10b1UUID, "tls": true, "servername": "a.example.com", "client-fingerprint": "Chrome"}}
	healed := e.Proxy.forEndpoint(e)
	if got := healed.str("client-fingerprint"); got != "chrome" {
		t.Fatalf("存量 Chrome 经 forEndpoint 须愈合为 chrome，got %q", got)
	}
	if got := e.Proxy.str("client-fingerprint"); got != "Chrome" {
		t.Fatalf("存储不动：原值须仍为 Chrome，got %q", got)
	}
	// 订阅 sing-box 面产物不再含毒化值。
	obs, ok := proxySingbox(e)
	if !ok {
		t.Fatal("存量 Chrome 节点 sing-box 面须愈合下发而非略过")
	}
	tlsMap, _ := obs[0]["tls"].(map[string]interface{})
	utls, _ := tlsMap["utls"].(map[string]interface{})
	if utls["fingerprint"] != "chrome" {
		t.Fatalf("sing-box 产物 utls.fingerprint 须为 chrome，got %v", utls["fingerprint"])
	}
	// 存量垃圾：谓词全集整节点略过（sing-box / Clash 两法定面）。
	g := Endpoint{Name: "fp-bad", Host: "203.0.113.2", Port: 443, Direct: true,
		Proxy: Proxy{"type": "vless", "server": "203.0.113.2", "port": 443,
			"uuid": r10b1UUID, "tls": true, "client-fingerprint": "bogus123"}}
	for _, face := range []string{ssFaceSingbox, ssFaceClash, ssFaceOther, ssFaceXrayChain} {
		if validImportedProxyFields(g.Proxy, face) {
			t.Fatalf("垃圾 fingerprint 存量在 %s 面谓词须不过", face)
		}
	}
	if _, ok := proxySingbox(g); ok {
		t.Fatal("垃圾 fingerprint 存量 sing-box 面须整节点略过")
	}
	if _, ok := proxyClash(g); ok {
		t.Fatal("垃圾 fingerprint 存量 Clash 面须整节点略过")
	}
	// 合法小写存量逐字不变（防过修）。
	low := Endpoint{Name: "fp-low", Host: "203.0.113.3", Port: 443, Direct: true,
		Proxy: Proxy{"type": "trojan", "server": "203.0.113.3", "port": 443,
			"password": "pw", "client-fingerprint": "firefox"}}
	if got := low.Proxy.forEndpoint(low).str("client-fingerprint"); got != "firefox" {
		t.Fatalf("合法小写 fingerprint 须逐字不变，got %q", got)
	}
}

func TestR10B1FingerprintSelfBuiltNormalize(t *testing.T) {
	// 自建面同根格：API 直写大写 fingerprint 经 Normalize 愈合落库。
	in := Inbound{Protocol: ProtocolVLESS, Port: 443,
		Security: Security{Type: SecurityReality, Fingerprint: "Chrome"}}
	if err := Normalize(TypeXray, &in); err != nil {
		t.Fatalf("Normalize 大写 fingerprint 须愈合而非报错：%v", err)
	}
	if in.Security.Fingerprint != "chrome" {
		t.Fatalf("自建 fingerprint 须愈合为 chrome，got %q", in.Security.Fingerprint)
	}
	// 垃圾值 Normalize 拒（不得落库后毒化自建端点 sing-box 面）。
	bad := Inbound{Protocol: ProtocolVLESS, Port: 443,
		Security: Security{Type: SecurityReality, Fingerprint: "bogus123"}}
	if err := Normalize(TypeXray, &bad); err == nil {
		t.Fatal("自建垃圾 fingerprint 须被 Normalize 拒绝")
	}
	// 合法小写逐字不变。
	okIn := Inbound{Protocol: ProtocolVLESS, Port: 443,
		Security: Security{Type: SecurityReality, Fingerprint: "firefox"}}
	if err := Normalize(TypeXray, &okIn); err != nil {
		t.Fatalf("Normalize 合法 fingerprint 须通过：%v", err)
	}
	if okIn.Security.Fingerprint != "firefox" {
		t.Fatalf("合法小写 fingerprint 须逐字不变，got %q", okIn.Security.Fingerprint)
	}
}

// ---------- R10X-002 xhttp-opts.mode ----------

func TestR10B1XHTTPModeImportHealAndReject(t *testing.T) {
	mk := func(mode string) string {
		return "proxies:\n  - {name: XH, type: vless, server: 203.0.113.1, port: 443, uuid: " + r10b1UUID + ", network: xhttp, xhttp-opts: {path: /xh, mode: " + mode + "}}\n"
	}
	// 大小写变体愈合落库。
	for in, want := range map[string]string{"AUTO": "auto", "Packet-Up": "packet-up", "STREAM-ONE": "stream-one", "Stream-Up": "stream-up"} {
		n := mustImportOne(t, mk(in))
		if got := n.Proxy.sub("xhttp-opts").str("mode"); got != want {
			t.Fatalf("xhttp mode %q 须愈合落库为 %q，got %q", in, want, got)
		}
	}
	// 合法集逐值小写原样落库（防过修）。
	for _, mode := range []string{"auto", "packet-up", "stream-up", "stream-one"} {
		n := mustImportOne(t, mk(mode))
		if got := n.Proxy.sub("xhttp-opts").str("mode"); got != mode {
			t.Fatalf("合法 xhttp mode %q 须逐字落库，got %q", mode, got)
		}
	}
	// URI 通道 mode 变体同愈合。
	n := mustImportOne(t, "vless://"+r10b1UUID+"@203.0.113.1:443?encryption=none&security=none&type=xhttp&path=%2Fxh&mode=Packet-Up#XH")
	if got := n.Proxy.sub("xhttp-opts").str("mode"); got != "packet-up" {
		t.Fatalf("URI xhttp mode 须愈合为 packet-up，got %q", got)
	}
	// 垃圾值逐条拒、回执点名 mode。
	_, errs := ParseImport(mk("bogus"))
	if len(errs) != 1 || !strings.Contains(errs[0], "mode") {
		t.Fatalf("垃圾 xhttp mode 须逐条被拒且回执点名，errs=%v", errs)
	}
}

func TestR10B1XHTTPModeStockHealAndSkip(t *testing.T) {
	e := Endpoint{Name: "xh-stock", Host: "203.0.113.1", Port: 443, Direct: true,
		Proxy: Proxy{"type": "vless", "server": "203.0.113.1", "port": 443, "uuid": r10b1UUID,
			"network": "xhttp", "xhttp-opts": map[string]interface{}{"path": "/xh", "mode": "AUTO"}}}
	healed := e.Proxy.forEndpoint(e)
	if got := healed.sub("xhttp-opts").str("mode"); got != "auto" {
		t.Fatalf("存量 AUTO 经 forEndpoint 须愈合为 auto，got %q", got)
	}
	if got := e.Proxy.sub("xhttp-opts").str("mode"); got != "AUTO" {
		t.Fatalf("存储不动：原值须仍为 AUTO，got %q", got)
	}
	// URI 面产物含愈合值。
	uri, ok := proxyURI(e)
	if !ok || !strings.Contains(uri, "mode=auto") {
		t.Fatalf("存量 xhttp URI 须含 mode=auto：ok=%v uri=%q", ok, uri)
	}
	// Xray 链式面：愈合后可达（修复前 AUTO 原样下发 exit 23 面）。
	if _, err := chainTargetFromProxy(e); err != nil {
		t.Fatalf("存量 AUTO 经愈合后 Xray 链式须可达：%v", err)
	}
	// 存量垃圾：谓词不过、Clash 面略过、链式面面板侧报错。
	g := Endpoint{Name: "xh-bad", Host: "203.0.113.2", Port: 443, Direct: true,
		Proxy: Proxy{"type": "vless", "server": "203.0.113.2", "port": 443, "uuid": r10b1UUID,
			"network": "xhttp", "xhttp-opts": map[string]interface{}{"path": "/xh", "mode": "bogus"}}}
	if validImportedProxyFields(g.Proxy, ssFaceClash) {
		t.Fatal("垃圾 xhttp mode 存量谓词须不过")
	}
	if _, ok := proxyClash(g); ok {
		t.Fatal("垃圾 xhttp mode 存量 Clash 面须整节点略过")
	}
	if _, err := chainTargetFromProxy(g); err == nil {
		t.Fatal("垃圾 xhttp mode 存量 Xray 链式面须面板侧报错")
	}
	// 合法小写存量逐字不变（防过修）。
	low := Endpoint{Name: "xh-low", Host: "203.0.113.3", Port: 443, Direct: true,
		Proxy: Proxy{"type": "vless", "server": "203.0.113.3", "port": 443, "uuid": r10b1UUID,
			"network": "xhttp", "xhttp-opts": map[string]interface{}{"path": "/xh", "mode": "stream-one"}}}
	if got := low.Proxy.forEndpoint(low).sub("xhttp-opts").str("mode"); got != "stream-one" {
		t.Fatalf("合法小写 xhttp mode 须逐字不变，got %q", got)
	}
}

// ---------- R10X-003 SS plugin-opts.mode ----------

func TestR10B1SSPluginObfsModeImportHealAndReject(t *testing.T) {
	mk := func(mode string) string {
		return "proxies:\n  - {name: SS, type: ss, server: 203.0.113.1, port: 8388, cipher: aes-128-gcm, password: pw, plugin: obfs, plugin-opts: {mode: " + mode + ", host: bing.com}}\n"
	}
	// 大小写变体愈合落库。
	for in, want := range map[string]string{"HTTP": "http", "TLS": "tls", "Http": "http"} {
		n := mustImportOne(t, mk(in))
		if got := n.Proxy.sub("plugin-opts").str("mode"); got != want {
			t.Fatalf("plugin-opts mode %q 须愈合落库为 %q，got %q", in, want, got)
		}
		if got := n.Proxy.str("plugin"); got != "obfs" {
			t.Fatalf("plugin 名须逐字保留 obfs，got %q", got)
		}
	}
	// 合法集逐值小写原样落库（防过修）。
	for _, mode := range []string{"http", "tls"} {
		n := mustImportOne(t, mk(mode))
		if got := n.Proxy.sub("plugin-opts").str("mode"); got != mode {
			t.Fatalf("合法 plugin-opts mode %q 须逐字落库，got %q", mode, got)
		}
	}
	// 垃圾值逐条拒、回执点名 mode。
	_, errs := ParseImport(mk("bogus"))
	if len(errs) != 1 || !strings.Contains(errs[0], "mode") {
		t.Fatalf("垃圾 plugin-opts mode 须逐条被拒且回执点名，errs=%v", errs)
	}
	// 防过修：v2ray-plugin 的 mode=websocket 不受 obfs 合法集约束。
	n := mustImportOne(t, "proxies:\n  - {name: SS2, type: ss, server: 203.0.113.1, port: 8388, cipher: aes-128-gcm, password: pw, plugin: v2ray-plugin, plugin-opts: {mode: websocket, host: bing.com}}\n")
	if got := n.Proxy.sub("plugin-opts").str("mode"); got != "websocket" {
		t.Fatalf("v2ray-plugin mode 须逐字不变，got %q", got)
	}
	// ss URI 通道（SIP003 obfs=http 大写值）：经 applySSPlugin 落
	// plugin-opts.mode，finishProxy 同口径愈合。
	n = mustImportOne(t, "ss://"+r10b1SSUserinfo()+"@203.0.113.1:8388/?plugin=obfs-local%3Bobfs%3DHTTP%3Bobfs-host%3Dbing.com#SSURI")
	if got := n.Proxy.sub("plugin-opts").str("mode"); got != "http" {
		t.Fatalf("URI 通道 plugin-opts mode HTTP 须愈合为 http，got %q", got)
	}
}

func r10b1SSUserinfo() string {
	// aes-128-gcm:pw 的 RawURLEncoding base64（与既有导入用例同形态）。
	return "YWVzLTEyOC1nY206cHc"
}

func TestR10B1SSPluginObfsModeStockHealAndSkip(t *testing.T) {
	e := Endpoint{Name: "ss-stock", Host: "203.0.113.1", Port: 8388, Direct: true,
		Proxy: Proxy{"type": "ss", "server": "203.0.113.1", "port": 8388,
			"cipher": "aes-128-gcm", "password": "pw", "plugin": "obfs",
			"plugin-opts": map[string]interface{}{"mode": "HTTP", "host": "bing.com"}}}
	// ssPluginString 消费前同口径：产物 mode 已小写。
	if got := ssPluginString(e.Proxy.forEndpoint(e)); got != "obfs-local;obfs=http;obfs-host=bing.com" {
		t.Fatalf("ssPluginString 须产出愈合 mode，got %q", got)
	}
	// sing-box 面 plugin_opts 不再含毒化值。
	obs, ok := proxySingbox(e)
	if !ok {
		t.Fatal("存量 HTTP 节点 sing-box 面须愈合下发而非略过")
	}
	if got, _ := obs[0]["plugin_opts"].(string); got != "obfs=http;obfs-host=bing.com" {
		t.Fatalf("sing-box plugin_opts 须为愈合值，got %q", got)
	}
	// Clash 面（forEndpoint 愈合 + 通用透传）产物 mode 小写。
	node, ok := proxyClash(e)
	if !ok {
		t.Fatal("存量 HTTP 节点 Clash 面须愈合下发而非略过")
	}
	cm := yamlToMap(node)
	po, _ := cm["plugin-opts"].(map[string]interface{})
	if po["mode"] != "http" {
		t.Fatalf("Clash plugin-opts mode 须愈合为 http，got %v", po["mode"])
	}
	if got := e.Proxy.sub("plugin-opts").str("mode"); got != "HTTP" {
		t.Fatalf("存储不动：原值须仍为 HTTP，got %q", got)
	}
	// 存量垃圾 mode：谓词不过、双法定面略过。
	g := Endpoint{Name: "ss-bad", Host: "203.0.113.2", Port: 8388, Direct: true,
		Proxy: Proxy{"type": "ss", "server": "203.0.113.2", "port": 8388,
			"cipher": "aes-128-gcm", "password": "pw", "plugin": "obfs",
			"plugin-opts": map[string]interface{}{"mode": "bogus"}}}
	for _, face := range []string{ssFaceSingbox, ssFaceClash} {
		if validImportedProxyFields(g.Proxy, face) {
			t.Fatalf("垃圾 plugin-opts mode 存量在 %s 面谓词须不过", face)
		}
	}
	if _, ok := proxySingbox(g); ok {
		t.Fatal("垃圾 plugin-opts mode 存量 sing-box 面须整节点略过")
	}
	if _, ok := proxyClash(g); ok {
		t.Fatal("垃圾 plugin-opts mode 存量 Clash 面须整节点略过")
	}
	// 合法小写存量产物逐字不变（防过修）。
	low := Endpoint{Name: "ss-low", Host: "203.0.113.3", Port: 8388, Direct: true,
		Proxy: Proxy{"type": "ss", "server": "203.0.113.3", "port": 8388,
			"cipher": "aes-128-gcm", "password": "pw", "plugin": "obfs",
			"plugin-opts": map[string]interface{}{"mode": "tls", "host": "bing.com"}}}
	if got := ssPluginString(low.Proxy.forEndpoint(low)); got != "obfs-local;obfs=tls;obfs-host=bing.com" {
		t.Fatalf("合法小写 plugin 产物须逐字不变，got %q", got)
	}
}

// TestR10B1SSPluginNameCaseInsensitive 是 R10X-003 附带格的独立断言
// （与本体 mode 格分开登记，禁互替）：URI 面对 plugin 名变体此前静
// 默丢 plugin（节点降级），ssPluginString 大小写不敏感匹配后产物含
// plugin 参数；规范小写名产物逐字不变。
func TestR10B1SSPluginNameCaseInsensitive(t *testing.T) {
	mk := func(plugin string, po Proxy) Endpoint {
		return Endpoint{Name: "ss-name", Host: "203.0.113.1", Port: 8388, Direct: true,
			Proxy: Proxy{"type": "ss", "server": "203.0.113.1", "port": 8388,
				"cipher": "aes-128-gcm", "password": "pw", "plugin": plugin,
				"plugin-opts": map[string]interface{}(po)}}
	}
	obfsPO := Proxy{"mode": "http", "host": "bing.com"}
	if got := ssPluginString(mk("OBFS", obfsPO).Proxy); got != "obfs-local;obfs=http;obfs-host=bing.com" {
		t.Fatalf("plugin=OBFS 须命中 obfs 支，got %q", got)
	}
	if got := ssPluginString(mk("Shadow-TLS", Proxy{"host": "bing.com", "password": "sp", "version": 3}).Proxy); got != "shadow-tls;host=bing.com;password=sp;version=3" {
		t.Fatalf("plugin=Shadow-TLS 须命中 shadow-tls 支，got %q", got)
	}
	if got := ssPluginString(mk("V2ray-Plugin", Proxy{"mode": "websocket", "host": "bing.com"}).Proxy); got != "v2ray-plugin;mode=websocket;host=bing.com" {
		t.Fatalf("plugin=V2ray-Plugin 须命中 v2ray-plugin 支，got %q", got)
	}
	// URI 面：变体名节点产物含 plugin 参数（修复前为空、静默丢弃）。
	uri, ok := proxyURI(mk("OBFS", obfsPO))
	if !ok || !strings.Contains(uri, "plugin=") || !strings.Contains(uri, "obfs") {
		t.Fatalf("URI 面 plugin=OBFS 产物须含 plugin 参数：ok=%v uri=%q", ok, uri)
	}
	// 规范小写名产物逐字不变（防过修）。
	if got := ssPluginString(mk("obfs", obfsPO).Proxy); got != "obfs-local;obfs=http;obfs-host=bing.com" {
		t.Fatalf("规范 plugin 名产物须逐字不变，got %q", got)
	}
}
