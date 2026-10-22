package kernel

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

// R4 批 1（R4X-001 / R4X-002 / R4X-013）：凭据收口回归。
// 口径（R4-X-verdicts §1 写死）：
//   - R4X-001：ss cipher 大小写变体在 validateProxyCredentials 的 ss
//     分支入口一处 ToLower 收口，四格式落库即规范小写名；存量变体行
//     在渲染收敛点 forEndpoint 补小写化自愈（坏 key 变体经自愈后被
//     sing-box 守卫略过、好 key 变体以规范名渲染）。
//   - R4X-002：validate 内以「Proxy 含 reality-opts 即查」横切校验，
//     不绑协议分支（trojan 同查）；pbk 谓词 = RawURL 可解码且恰 32
//     字节；short-id 非空时须小写 hex / 偶数位 / ≤16，空放行。
//   - R4X-013：自建面（model.go Normalize）SS2022 校验改调共享四形态
//     解码核 + 单段限定（多段在自建面仍拒）。

const (
	r4b1GoodPBK = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8" // RawURL, 32 bytes (0..31)
	r4b1Key16   = "MTIzNDU2Nzg5MDEyMzQ1Ng=="                    // Std, 16 bytes
)

// ---- R4X-001 ----

func TestR4B1CipherCaseVariantImport(t *testing.T) {
	key32 := base64.StdEncoding.EncodeToString(r3b2RawKey(32))
	for _, f := range r3b2Formats() {
		// 大写 cipher + 合法 key：接受且落库为规范小写名。
		doc := f.doc([]r3b2Spec{{name: "up-good", host: "203.0.113.5", port: 8388,
			cipher: "2022-BLAKE3-AES-128-GCM", password: r4b1Key16}})
		nodes, errs := ParseImport(doc)
		if len(errs) != 0 || len(nodes) != 1 {
			t.Fatalf("%s 大写 cipher+好 key 须入库：nodes=%v errs=%v", f.name, nodes, errs)
		}
		if got := nodes[0].Proxy.str("cipher"); got != "2022-blake3-aes-128-gcm" {
			t.Fatalf("%s 落库 cipher 须为规范小写名，got %q", f.name, got)
		}
		// 混写 cipher（256）+ 合法 key。
		doc = f.doc([]r3b2Spec{{name: "mix-good", host: "203.0.113.5", port: 8388,
			cipher: "2022-Blake3-Aes-256-Gcm", password: key32}})
		nodes, errs = ParseImport(doc)
		if len(errs) != 0 || len(nodes) != 1 || nodes[0].Proxy.str("cipher") != "2022-blake3-aes-256-gcm" {
			t.Fatalf("%s 混写 cipher+好 key：nodes=%v errs=%v", f.name, nodes, errs)
		}
		// 大写 cipher + 坏 key：逐条拒绝。
		doc = f.doc([]r3b2Spec{{name: "up-bad", host: "203.0.113.5", port: 8388,
			cipher: "2022-BLAKE3-AES-128-GCM", password: "plainpassword"}})
		nodes, errs = ParseImport(doc)
		if len(nodes) != 0 || len(errs) != 1 || !strings.Contains(errs[0], "SS2022") {
			t.Fatalf("%s 大写 cipher+坏 key 须拒绝：nodes=%v errs=%v", f.name, nodes, errs)
		}
		// legacy cipher 大小写变体：任意口令合法，落库规范小写名。
		doc = f.doc([]r3b2Spec{{name: "legacy", host: "203.0.113.5", port: 8388,
			cipher: "AES-256-GCM", password: "any-password"}})
		nodes, errs = ParseImport(doc)
		if len(errs) != 0 || len(nodes) != 1 || nodes[0].Proxy.str("cipher") != "aes-256-gcm" {
			t.Fatalf("%s legacy 变体：nodes=%v errs=%v", f.name, nodes, errs)
		}
	}
}

func TestR4B1StockCipherVariantRenderHeal(t *testing.T) {
	// 存量变体行（直构 Proxy 模拟 DB 直写行，未经导入收口）。
	good := Endpoint{Name: "stock-good", Host: "203.0.113.10", Port: 8388, Direct: true,
		Proxy: Proxy{"type": "ss", "server": "203.0.113.10", "port": 8388,
			"cipher": "2022-BLAKE3-AES-128-GCM", "password": r4b1Key16}}
	obs, ok := proxySingbox(good)
	if !ok || len(obs) != 1 || obs[0]["method"] != "2022-blake3-aes-128-gcm" {
		t.Fatalf("存量好 key 变体 sing-box 渲染须自愈为规范名：ok=%v obs=%v", ok, obs)
	}
	node, ok := proxyClash(good)
	if !ok {
		t.Fatal("存量好 key 变体 Clash 渲染须成功")
	}
	if m := yamlToMap(node); Proxy(m).str("cipher") != "2022-blake3-aes-128-gcm" {
		t.Fatalf("存量好 key 变体 Clash cipher 须为规范名：%v", m)
	}
	if uri, ok := proxyURI(good); !ok || !strings.Contains(uri, "2022-blake3-aes-128-gcm:") {
		t.Fatalf("存量好 key 变体 URI 须为规范名：%q", uri)
	}
	if line, ok := proxySurge(good); !ok || !strings.Contains(line, "encrypt-method=2022-blake3-aes-128-gcm") {
		t.Fatalf("存量好 key 变体 Surge 须为规范名：%q", line)
	}
	// 存量变体 + 坏 key：自愈后 sing-box 守卫须略过（修复前守卫被
	// 大小写绕过、坏节点原样下发毒化整表）。
	bad := Endpoint{Name: "stock-bad", Host: "203.0.113.10", Port: 8388, Direct: true,
		Proxy: Proxy{"type": "ss", "server": "203.0.113.10", "port": 8388,
			"cipher": "2022-BLAKE3-AES-128-GCM", "password": "plainpassword"}}
	if _, ok := proxySingbox(bad); ok {
		t.Fatal("存量坏 key 变体 sing-box 须略过")
	}
	if ValidSS2022SingleKey("2022-BLAKE3-AES-128-GCM", "plainpassword") {
		t.Fatal("helper 层：大写 cipher + 坏 key 不得报合法")
	}
}

// ---- R4X-002 ----

func r4b1RealityDocs(pbk, sid string) map[string]string {
	uuid := "11111111-2222-3333-4444-555555555555"
	sidQ := ""
	if sid != "" {
		sidQ = "&sid=" + sid
	}
	clashRO := fmt.Sprintf("reality-opts: {public-key: %q, short-id: %q}", pbk, sid)
	if sid == "" {
		clashRO = fmt.Sprintf("reality-opts: {public-key: %q}", pbk)
	}
	sbRO := fmt.Sprintf(`"tls":{"enabled":true,"server_name":"a.com","reality":{"enabled":true,"public_key":%q,"short_id":%q}}`, pbk, sid)
	return map[string]string{
		"uri-vless":      fmt.Sprintf("vless://%s@1.2.3.4:443?encryption=none&security=reality&sni=a.com&pbk=%s%s&type=tcp#V", uuid, pbk, sidQ),
		"uri-trojan":     fmt.Sprintf("trojan://pw@1.2.3.4:443?security=reality&sni=a.com&pbk=%s%s#T", pbk, sidQ),
		"clash-vless":    fmt.Sprintf("proxies:\n  - {name: V, type: vless, server: 1.2.3.4, port: 443, uuid: %s, tls: true, servername: a.com, %s}\n", uuid, clashRO),
		"clash-trojan":   fmt.Sprintf("proxies:\n  - {name: T, type: trojan, server: 1.2.3.4, port: 443, password: pw, sni: a.com, %s}\n", clashRO),
		"singbox-vless":  fmt.Sprintf(`{"outbounds":[{"type":"vless","tag":"V","server":"1.2.3.4","server_port":443,"uuid":%q,%s}]}`, uuid, sbRO),
		"singbox-trojan": fmt.Sprintf(`{"outbounds":[{"type":"trojan","tag":"T","server":"1.2.3.4","server_port":443,"password":"pw",%s}]}`, sbRO),
	}
}

func TestR4B1RealityBadPBKRejectedAllPaths(t *testing.T) {
	for _, pbk := range []string{"GARBAGE_NOT_BASE64!!", "", "AAAA"} { // 垃圾 / 空 / 可解码但仅 3 字节
		for name, doc := range r4b1RealityDocs(pbk, "ab12") {
			if pbk == "" && strings.HasPrefix(name, "uri-") {
				continue // URI 路径空 pbk 由 parseUserURI 既有非空检查拒绝，形态不同
			}
			nodes, errs := ParseImport(doc)
			if len(nodes) != 0 || len(errs) == 0 || !strings.Contains(errs[0], "Reality") {
				t.Fatalf("%s pbk=%q 须拒绝：nodes=%v errs=%v", name, pbk, nodes, errs)
			}
		}
	}
	// URI 空 pbk 维持既有拒绝。
	if _, errs := ParseImport("vless://11111111-2222-3333-4444-555555555555@1.2.3.4:443?security=reality&sni=a.com#V"); len(errs) == 0 {
		t.Fatal("URI 空 pbk 须拒绝")
	}
}

func TestR4B1RealityGoodPBKAcceptedAllPaths(t *testing.T) {
	for name, doc := range r4b1RealityDocs(r4b1GoodPBK, "ab12") {
		nodes, errs := ParseImport(doc)
		if len(errs) != 0 || len(nodes) != 1 {
			t.Fatalf("%s 合法 pbk 须入库：nodes=%v errs=%v", name, nodes, errs)
		}
		if got := nodes[0].Proxy.sub("reality-opts").str("public-key"); got != r4b1GoodPBK {
			t.Fatalf("%s pbk 须原样落库，got %q", name, got)
		}
	}
	// short-id 空放行（三路径抽查）。
	for name, doc := range r4b1RealityDocs(r4b1GoodPBK, "") {
		if nodes, errs := ParseImport(doc); len(errs) != 0 || len(nodes) != 1 {
			t.Fatalf("%s 空 sid 须放行：nodes=%v errs=%v", name, nodes, errs)
		}
	}
}

func TestR4B1RealityShortIDPredicate(t *testing.T) {
	for _, sid := range []string{"zz", "abc", "AB12", "aabbccddeeff001122"} { // 非 hex / 奇数位 / 大写 / 超 16
		doc := r4b1RealityDocs(r4b1GoodPBK, sid)["clash-vless"]
		if nodes, errs := ParseImport(doc); len(nodes) != 0 || len(errs) == 0 || !strings.Contains(errs[0], "short-id") {
			t.Fatalf("sid=%q 须拒绝：nodes=%v errs=%v", sid, nodes, errs)
		}
	}
	for _, sid := range []string{"ab12", "00", "aabbccddeeff0011"} { // 合法：偶数位小写 hex ≤16
		doc := r4b1RealityDocs(r4b1GoodPBK, sid)["clash-vless"]
		if nodes, errs := ParseImport(doc); len(errs) != 0 || len(nodes) != 1 {
			t.Fatalf("sid=%q 须放行：nodes=%v errs=%v", sid, nodes, errs)
		}
	}
}

// ---- R4X-013 ----

func TestR4B1BuiltinSS2022FourFormsSingleSegmentOnly(t *testing.T) {
	raw := []byte("1234567890123456")
	for _, f := range gateF1Forms {
		in := Inbound{Protocol: ProtocolShadowsocks, Port: 8388,
			Settings: Settings{Method: "2022-blake3-aes-128-gcm", Password: f.enc.EncodeToString(raw)}}
		if err := Normalize(TypeSingbox, &in); err != nil {
			t.Fatalf("自建面 %s 形态须接受：%v", f.name, err)
		}
	}
	// 多段在自建面仍拒（单段限定，不得带入 SIP023 接受性）。
	seg := base64.StdEncoding.EncodeToString(raw)
	in := Inbound{Protocol: ProtocolShadowsocks, Port: 8388,
		Settings: Settings{Method: "2022-blake3-aes-128-gcm", Password: seg + ":" + seg}}
	if err := Normalize(TypeSingbox, &in); err == nil {
		t.Fatal("自建面多段密码须拒绝")
	}
	// 坏 key 仍拒。
	in = Inbound{Protocol: ProtocolShadowsocks, Port: 8388,
		Settings: Settings{Method: "2022-blake3-aes-128-gcm", Password: "plainpassword"}}
	if err := Normalize(TypeSingbox, &in); err == nil {
		t.Fatal("自建面坏 key 须拒绝")
	}
}
