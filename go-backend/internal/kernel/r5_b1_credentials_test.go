package kernel

import (
	"fmt"
	"strings"
	"testing"
)

// R5 批 1（R5X-001 / R5X-002 / R5X-003）：凭据族系统收口回归。
// 口径（R5-X-verdicts §3 硬性验收法）：
//   - 枚举格在导入期 validateProxyCredentials 逐条拒绝入 errs、其余
//     节点不连坐；合法集逐值以 sing-box 1.14.3 / mihomo v1.19.33 双
//     二进制核对后写死（见批报告逐格矩阵）。
//   - 存量面 proxySingbox / proxyClash 复用导入期同一谓词整节点略过
//     （Reality 复用 validateRealityOpts），SubscriptionNodeCount 同口径。
//   - tuic udp-relay-mode 与 hy2 ports 两格先行探针定生死：ports 经面
//     板渲染产物 + 双二进制坐实整表毒化（abc / 越界 / 单端口形态在
//     sing-box server_ports 下均 FATAL）→ 入校验集；udp-relay-mode 双
//     二进制 check 均不校验、拨号探针在本环境不可得（UDP 受限）→
//     未验证，不入校验集（批报告登记带入 R6）。

const r5b1UUID = "11111111-2222-3333-4444-555555555555"

const r5b1HealthyClash = "  - {name: healthy, type: ss, server: 203.0.113.99, port: 8388, cipher: aes-128-gcm, password: pw, udp: true}\n"

// r5b1BadDocs returns, per garbage cell, documents for every import path
// that can carry the field. Each document pairs the bad node with a
// healthy SS node where the format allows multiple nodes.
func r5b1BadDocs() map[string][]string {
	vmessClash := "proxies:\n  - {name: bad, type: vmess, server: 1.2.3.4, port: 443, uuid: " + r5b1UUID + ", alterId: 0, cipher: garbage-cipher, udp: true}\n" + r5b1HealthyClash
	vmessURI := "vmess://eyJ2IjoiMiIsInBzIjoiYmFkIiwgImFkZCI6IjEuMi4zLjQiLCAicG9ydCI6IjQ0MyIsICJpZCI6IjExMTExMTExLTIyMjItMzMzMy00NDQ0LTU1NTU1NTU1NTU1IiwgImFpZCI6IjAiLCAic2N5IjoiZ2FyYmFnZS1jaXBoZXIiLCAibmV0IjoidGNwIiwgInR5cGUiOiJub25lIiwgImhvc3QiOiIiLCAicGF0aCI6IiIsICJ0bHMiOiIifQ==\nss://aes-128-gcm:pw@203.0.113.99:8388#healthy"
	vmessSB := `{"outbounds":[{"type":"vmess","tag":"bad","server":"1.2.3.4","server_port":443,"uuid":"` + r5b1UUID + `","security":"garbage-cipher","alter_id":0},{"type":"shadowsocks","tag":"healthy","server":"203.0.113.99","server_port":8388,"method":"aes-128-gcm","password":"pw"}]}`
	hy2Clash := "proxies:\n  - {name: bad, type: hysteria2, server: 1.2.3.4, port: 443, password: pw, obfs: garbage-obfs, obfs-password: x, udp: true}\n" + r5b1HealthyClash
	hy2URI := "hysteria2://pw@1.2.3.4:443?obfs=garbage-obfs&obfs-password=x#bad\nss://aes-128-gcm:pw@203.0.113.99:8388#healthy"
	hy2SB := `{"outbounds":[{"type":"hysteria2","tag":"bad","server":"1.2.3.4","server_port":443,"password":"pw","obfs":{"type":"garbage-obfs","password":"x"}},{"type":"shadowsocks","tag":"healthy","server":"203.0.113.99","server_port":8388,"method":"aes-128-gcm","password":"pw"}]}`
	flowClash := "proxies:\n  - {name: bad, type: vless, server: 1.2.3.4, port: 443, uuid: " + r5b1UUID + ", flow: garbage-flow, udp: true}\n" + r5b1HealthyClash
	flowURI := "vless://" + r5b1UUID + "@1.2.3.4:443?encryption=none&flow=garbage-flow&type=tcp#bad\nss://aes-128-gcm:pw@203.0.113.99:8388#healthy"
	ccClash := "proxies:\n  - {name: bad, type: tuic, server: 1.2.3.4, port: 443, uuid: " + r5b1UUID + ", password: pw, congestion-controller: garbage-cc, udp: true}\n" + r5b1HealthyClash
	ccURI := "tuic://" + r5b1UUID + ":pw@1.2.3.4:443?congestion_control=garbage-cc#bad\nss://aes-128-gcm:pw@203.0.113.99:8388#healthy"
	encClash := "proxies:\n  - {name: bad, type: vless, server: 1.2.3.4, port: 443, uuid: " + r5b1UUID + ", encryption: garbage-enc, udp: true}\n" + r5b1HealthyClash
	encURI := "vless://" + r5b1UUID + "@1.2.3.4:443?encryption=garbage-enc&type=tcp#bad\nss://aes-128-gcm:pw@203.0.113.99:8388#healthy"
	snellClash := "proxies:\n  - {name: bad, type: snell, server: 1.2.3.4, port: 443, psk: pw, version: 4, obfs-opts: {mode: garbage-obfs, host: example.com}, udp: true}\n" + r5b1HealthyClash
	portsClash := "proxies:\n  - {name: bad, type: hysteria2, server: 1.2.3.4, port: 443, password: pw, ports: 'abc', udp: true}\n" + r5b1HealthyClash
	portsOOB := "proxies:\n  - {name: bad, type: hysteria2, server: 1.2.3.4, port: 443, password: pw, ports: '80000-90000', udp: true}\n" + r5b1HealthyClash
	portsURI := "hysteria2://pw@1.2.3.4:443?mport=abc#bad\nss://aes-128-gcm:pw@203.0.113.99:8388#healthy"
	return map[string][]string{
		"vmess-cipher": {vmessClash, vmessURI, vmessSB},
		"hy2-obfs":     {hy2Clash, hy2URI, hy2SB},
		"vless-flow":   {flowClash, flowURI},
		"tuic-cc":      {ccClash, ccURI},
		"vless-enc":    {encClash, encURI},
		"snell-obfs":   {snellClash},
		"hy2-ports":    {portsClash, portsOOB, portsURI},
	}
}

func TestR5B1EnumGarbageRejectedAllPaths(t *testing.T) {
	for cell, docs := range r5b1BadDocs() {
		for i, doc := range docs {
			nodes, errs := ParseImport(doc)
			if len(errs) == 0 || len(nodes) != 1 || nodes[0].Name != "healthy" {
				t.Fatalf("%s doc%d：坏节点须逐条拒绝、健康节点不连坐，got nodes=%v errs=%v", cell, i, nodes, errs)
			}
		}
	}
}

func TestR5B1EnumLegalValuesAccepted(t *testing.T) {
	clash := func(node string) string { return "proxies:\n  - {name: ok, " + node + "}\n" }
	docs := map[string]string{}
	for _, c := range []string{"auto", "aes-128-gcm", "chacha20-poly1305", "none", "zero"} {
		docs["vmess-cipher-"+c] = clash(fmt.Sprintf("type: vmess, server: 1.2.3.4, port: 443, uuid: %s, alterId: 0, cipher: %s, udp: true", r5b1UUID, c))
	}
	for _, o := range []string{"salamander", "gecko"} {
		docs["hy2-obfs-"+o] = clash(fmt.Sprintf("type: hysteria2, server: 1.2.3.4, port: 443, password: pw, obfs: %s, obfs-password: x, udp: true", o))
	}
	for _, p := range []string{"443", "20000-30000", "443,9000-10000", "1-65535"} {
		docs["hy2-ports-"+p] = clash(fmt.Sprintf("type: hysteria2, server: 1.2.3.4, port: 443, password: pw, ports: '%s', udp: true", p))
	}
	// F-01 修复后 flow 单独出现仍须有 TLS 或 ENC 才能入库：本格原为
	// 无 TLS/无 ENC 的 vision（即 F-01 缺陷组合本身），现补 tls +
	// servername 使其仍为“vision 是合法 flow 枚举值”的对照格。
	docs["vless-flow-vision"] = clash(fmt.Sprintf("type: vless, server: 1.2.3.4, port: 443, uuid: %s, flow: xtls-rprx-vision, tls: true, servername: a.example.com, udp: true", r5b1UUID))
	docs["vless-flow-empty"] = clash(fmt.Sprintf("type: vless, server: 1.2.3.4, port: 443, uuid: %s, udp: true", r5b1UUID))
	for _, cc := range []string{"bbr", "cubic", "new_reno"} {
		docs["tuic-cc-"+cc] = clash(fmt.Sprintf("type: tuic, server: 1.2.3.4, port: 443, uuid: %s, password: pw, congestion-controller: %s, udp: true", r5b1UUID, cc))
	}
	for _, m := range []string{"http", "tls", "shadow-tls", "restls", "jls"} {
		extra := ""
		switch m {
		case "shadow-tls":
			extra = ", password: pw2, version: 3"
		case "restls":
			extra = ", password: pw2, version-hint: tls13"
		case "jls":
			extra = ", username: u, password: pw2"
		}
		docs["snell-obfs-"+m] = clash(fmt.Sprintf("type: snell, server: 1.2.3.4, port: 443, psk: pw, version: 4, obfs-opts: {mode: %s, host: example.com%s}, udp: true", m, extra))
	}
	for name, doc := range docs {
		nodes, errs := ParseImport(doc)
		if len(errs) != 0 || len(nodes) != 1 {
			t.Fatalf("%s：合法值不得误拒，got nodes=%v errs=%v", name, nodes, errs)
		}
	}
	// vmess URI 合法对照（scy=zero）。
	nodes, errs := ParseImport("vmess://eyJ2IjoiMiIsInBzIjoib2siLCAiYWRkIjoiMS4yLjMuNCIsICJwb3J0IjoiNDQzIiwgImlkIjoiMTExMTExMTEtMjIyMi0zMzMzLTQ0NDQtNTU1NTU1NTU1NTU1IiwgImFpZCI6IjAiLCAic2N5IjoiemVybyIsICJuZXQiOiJ0Y3AiLCAidHlwZSI6Im5vbmUiLCAiaG9zdCI6IiIsICJwYXRoIjoiIiwgInRscyI6IiJ9")
	if len(errs) != 0 || len(nodes) != 1 {
		t.Fatalf("vmess URI cipher=zero 须入库：nodes=%v errs=%v", nodes, errs)
	}
}

// TestR5B1EncryptionGrammarAntiOverfit is the hard gate from verdicts
// §3.5: real EncryptionString products (both auths, all modes) must be
// accepted, while grammar garbage is rejected.
func TestR5B1EncryptionGrammarAntiOverfit(t *testing.T) {
	for _, auth := range []string{EncryptionAuthX25519, EncryptionAuthMLKEM} {
		for _, mode := range []string{EncryptionModeNative, EncryptionModeXorpub, EncryptionModeRandom} {
			secret, err := GenerateEncryptionSecret(auth)
			if err != nil {
				t.Fatal(err)
			}
			enc, err := Settings{EncryptionAuth: auth, EncryptionMode: mode, EncryptionSecret: secret}.EncryptionString()
			if err != nil {
				t.Fatal(err)
			}
			doc := "proxies:\n  - {name: ok, type: vless, server: 1.2.3.4, port: 443, uuid: " + r5b1UUID + ", encryption: '" + enc + "', udp: true}\n"
			if nodes, errs := ParseImport(doc); len(errs) != 0 || len(nodes) != 1 {
				t.Fatalf("真实 ENC 产物（%s/%s）不得误拒：nodes=%v errs=%v", auth, mode, nodes, errs)
			}
			uri := "vless://" + r5b1UUID + "@1.2.3.4:443?encryption=" + enc + "&type=tcp#ok"
			if nodes, errs := ParseImport(uri); len(errs) != 0 || len(nodes) != 1 {
				t.Fatalf("真实 ENC 产物 URI（%s/%s）不得误拒：nodes=%v errs=%v", auth, mode, nodes, errs)
			}
		}
	}
	pub32 := "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"
	pad := "100-111-1111.75-0-111.50-0-3333"
	bad := []string{
		"garbage-enc",
		"mlkem768x25519plus.garbage.0rtt." + pad + "." + pub32,                                             // bad mode
		"mlkem768x25519plus.native.2rtt." + pad + "." + pub32,                                              // bad rtt
		"mlkem768x25519plus.native.600s." + pad + "." + pub32,                                              // decryption-form ticket segment
		"mlkem768x25519plus.native.0rtt.garbage." + pub32,                                                  // bad padding
		"mlkem768x25519plus.native.0rtt." + pad + ".AAAA",                                                  // auth segment wrong length
		"mlkem768x25519plus.native.0rtt." + pad + ".!!!",                                                   // auth segment not base64
		"mlkem768.native.0rtt." + pad + "." + pub32,                                                        // wrong prefix
		Settings{EncryptionAuth: EncryptionAuthX25519, EncryptionSecret: mustSecret(t)}.DecryptionString(), // server-form string
	}
	for _, enc := range bad {
		doc := "proxies:\n  - {name: bad, type: vless, server: 1.2.3.4, port: 443, uuid: " + r5b1UUID + ", encryption: '" + enc + "', udp: true}\n"
		if nodes, errs := ParseImport(doc); len(nodes) != 0 || len(errs) == 0 {
			t.Fatalf("encryption=%q 须拒绝：nodes=%v errs=%v", enc, nodes, errs)
		}
		if ValidVLESSEncryption(enc) {
			t.Fatalf("谓词层：encryption=%q 不得报合法", enc)
		}
	}
	if !ValidVLESSEncryption("") || !ValidVLESSEncryption("none") {
		t.Fatal("谓词层：空 / none 须合法")
	}
}

func mustSecret(t *testing.T) string {
	t.Helper()
	s, err := GenerateEncryptionSecret(EncryptionAuthX25519)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestR5B1StockRenderSkip: direct stock rows (bypassing import) with
// garbage enum / Reality values must be skipped whole-node by both
// renderers, with SubscriptionNodeCount on the same口径.
func TestR5B1StockRenderSkip(t *testing.T) {
	goodPBK := "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"
	mk := func(p Proxy) Endpoint {
		return Endpoint{Name: "stock", Host: "1.2.3.4", Port: p.num("port"), Direct: true, Proxy: p}
	}
	healthy := Endpoint{Name: "healthy", Host: "203.0.113.99", Port: 8388, Direct: true,
		Proxy: Proxy{"type": "ss", "server": "203.0.113.99", "port": 8388, "cipher": "aes-128-gcm", "password": "pw", "udp": true}}
	bad := map[string]Proxy{
		"vmess-cipher":       {"type": "vmess", "server": "1.2.3.4", "port": 443, "uuid": r5b1UUID, "cipher": "garbage-cipher", "udp": true},
		"hy2-obfs":           {"type": "hysteria2", "server": "1.2.3.4", "port": 443, "password": "pw", "obfs": "garbage-obfs", "obfs-password": "x", "udp": true},
		"hy2-ports":          {"type": "hysteria2", "server": "1.2.3.4", "port": 443, "password": "pw", "ports": "abc", "udp": true},
		"vless-flow":         {"type": "vless", "server": "1.2.3.4", "port": 443, "uuid": r5b1UUID, "flow": "garbage-flow", "udp": true},
		"tuic-cc":            {"type": "tuic", "server": "1.2.3.4", "port": 443, "uuid": r5b1UUID, "password": "pw", "congestion-controller": "garbage-cc", "udp": true},
		"vless-enc":          {"type": "vless", "server": "1.2.3.4", "port": 443, "uuid": r5b1UUID, "encryption": "garbage-enc", "udp": true},
		"snell-obfs":         {"type": "snell", "server": "1.2.3.4", "port": 443, "psk": "pw", "version": 4, "obfs-opts": map[string]interface{}{"mode": "garbage-obfs", "host": "example.com"}, "udp": true},
		"reality-pbk":        {"type": "vless", "server": "1.2.3.4", "port": 443, "uuid": r5b1UUID, "tls": true, "reality-opts": map[string]interface{}{"public-key": "GARBAGE!!", "short-id": "ab12"}, "udp": true},
		"reality-pbk-trojan": {"type": "trojan", "server": "1.2.3.4", "port": 443, "password": "pw", "reality-opts": map[string]interface{}{"public-key": "", "short-id": ""}, "udp": true},
		"reality-sid":        {"type": "vless", "server": "1.2.3.4", "port": 443, "uuid": r5b1UUID, "tls": true, "reality-opts": map[string]interface{}{"public-key": goodPBK, "short-id": "zz"}, "udp": true},
	}
	for name, p := range bad {
		e := mk(p)
		if _, ok := proxySingbox(e); ok {
			t.Fatalf("%s：存量坏值 sing-box 须整节点略过", name)
		}
		if _, ok := proxyClash(e); ok {
			t.Fatalf("%s：存量坏值 Clash 须整节点略过", name)
		}
		eps := []Endpoint{e, healthy}
		if n := SubscriptionNodeCount(FormatSingbox, eps, SubOptions{}); n != 1 {
			t.Fatalf("%s：sing-box 计数须同口径为 1（仅健康节点），got %d", name, n)
		}
		if n := SubscriptionNodeCount(FormatClash, eps, SubOptions{}); n != 1 {
			t.Fatalf("%s：Clash 计数须同口径为 1（仅健康节点），got %d", name, n)
		}
	}
	// 合法存量对照：不得误略。
	good := map[string]Proxy{
		"vmess-zero":   {"type": "vmess", "server": "1.2.3.4", "port": 443, "uuid": r5b1UUID, "cipher": "zero", "udp": true},
		"hy2-gecko":    {"type": "hysteria2", "server": "1.2.3.4", "port": 443, "password": "pw", "obfs": "gecko", "obfs-password": "x", "udp": true},
		"hy2-ports":    {"type": "hysteria2", "server": "1.2.3.4", "port": 443, "password": "pw", "ports": "20000-30000", "udp": true},
		"vless-vision": {"type": "vless", "server": "1.2.3.4", "port": 443, "uuid": r5b1UUID, "flow": "xtls-rprx-vision", "udp": true},
		"tuic-newreno": {"type": "tuic", "server": "1.2.3.4", "port": 443, "uuid": r5b1UUID, "password": "pw", "congestion-controller": "new_reno", "udp": true},
		"snell-tls":    {"type": "snell", "server": "1.2.3.4", "port": 443, "psk": "pw", "version": 4, "obfs-opts": map[string]interface{}{"mode": "tls", "host": "example.com"}, "udp": true},
		// F-02 修复后合法 Reality 存量须带 SNI：本格原无 servername
		//（即 F-02 缺陷形态），现补 servername 使其仍为“合法 Reality
		// 存量两面不得误略”的对照格。
		"reality-good": {"type": "vless", "server": "1.2.3.4", "port": 443, "uuid": r5b1UUID, "tls": true, "servername": "a.example.com", "reality-opts": map[string]interface{}{"public-key": goodPBK, "short-id": "ab12"}, "udp": true},
	}
	for name, p := range good {
		e := mk(p)
		if _, ok := proxyClash(e); !ok {
			t.Fatalf("%s：合法存量 Clash 不得误略", name)
		}
		if name == "snell-tls" {
			continue // sing-box 对任何 snell obfs 均既有略过口径
		}
		if _, ok := proxySingbox(e); !ok {
			t.Fatalf("%s：合法存量 sing-box 不得误略", name)
		}
	}
}

// TestR5B1Hy2PortsSingboxRenderShape: stored ports must become valid
// sing-box server_ports entries (single port → p:p; comma items split).
func TestR5B1Hy2PortsSingboxRenderShape(t *testing.T) {
	for ports, want := range map[string][]string{
		"443":            {"443:443"},
		"20000-30000":    {"20000:30000"},
		"443,9000-10000": {"443:443", "9000:10000"},
	} {
		e := Endpoint{Name: "h", Host: "1.2.3.4", Port: 443, Direct: true,
			Proxy: Proxy{"type": "hysteria2", "server": "1.2.3.4", "port": 443, "password": "pw", "ports": ports, "udp": true}}
		obs, ok := proxySingbox(e)
		if !ok || len(obs) != 1 {
			t.Fatalf("ports=%q sing-box 须渲染：ok=%v obs=%v", ports, ok, obs)
		}
		got, _ := obs[0]["server_ports"].([]string)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("ports=%q server_ports=%v, want %v", ports, got, want)
		}
	}
}

// TestR5B1SnellShadowTLSModeSpelling: mihomo's canonical snell obfs mode
// is "shadow-tls" (v1.19.33 -t + official docs); the panel's historical
// "shadowtls" spelling is rejected by mihomo itself. Import heals the
// alias, and both Clash renderers emit the canonical spelling.
func TestR5B1SnellShadowTLSModeSpelling(t *testing.T) {
	doc := "proxies:\n  - {name: s, type: snell, server: 1.2.3.4, port: 443, psk: pw, version: 4, obfs-opts: {mode: shadowtls, host: example.com, password: pw2, version: 3}, udp: true}\n"
	nodes, errs := ParseImport(doc)
	if len(errs) != 0 || len(nodes) != 1 {
		t.Fatalf("shadowtls 别名须愈合入库：nodes=%v errs=%v", nodes, errs)
	}
	if got := nodes[0].Proxy.sub("obfs-opts").str("mode"); got != "shadow-tls" {
		t.Fatalf("落库 mode 须规范为 shadow-tls，got %q", got)
	}
	e := Endpoint{Name: "s", Host: "1.2.3.4", Port: 443, Direct: true, Proxy: nodes[0].Proxy}
	node, ok := proxyClash(e)
	if !ok {
		t.Fatal("愈合后的 snell+shadowtls 须可渲染 Clash")
	}
	if m := yamlToMap(node); Proxy(m).sub("obfs-opts").str("mode") != "shadow-tls" {
		t.Fatalf("Clash 渲染 mode 须为 shadow-tls：%v", m)
	}
	// 存量 shadow-tls 子对象（sing-box detour / Surge 导入形态）在
	// proxyClash 合成 obfs-opts 时同样须用规范拼写。
	stock := Endpoint{Name: "s2", Host: "1.2.3.4", Port: 443, Direct: true,
		Proxy: Proxy{"type": "snell", "server": "1.2.3.4", "port": 443, "psk": "pw", "version": 4,
			"shadow-tls": map[string]interface{}{"host": "example.com", "password": "pw2", "version": 3}, "udp": true}}
	node, ok = proxyClash(stock)
	if !ok {
		t.Fatal("存量 shadow-tls 子对象 snell 须可渲染 Clash")
	}
	if m := yamlToMap(node); Proxy(m).sub("obfs-opts").str("mode") != "shadow-tls" {
		t.Fatalf("合成 mode 须为 shadow-tls：%v", m)
	}
}
