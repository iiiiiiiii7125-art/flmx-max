package kernel

import (
	"encoding/base64"
	"strings"
	"testing"
)

// R6 批 1（R6X-001 / R6X-002 / R6X-003 + R6X-024 前置）：凭据族 SS 格总收口。
// 合法集口径 = 本批施工前重跑的 30 方法名双二进制逐值探针（面板渲染产物 ×
// sing-box 1.14.3 check / mihomo v1.19.33 -t），全表 + 原始日志存档于
// audit-out-132/r6/R6-FIX-B1-ss-method-probe.log：共同接受 18、单面
// {chacha20: mihomo-only} 1、双拒 11。关闭断言三条分开登记（本文件
// 测试分组与之一一对应，链式面 SS cipher 格归 R6X-001 组）。

var r6b1CommonCiphers = []string{
	"2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm", "2022-blake3-chacha20-poly1305",
	"aes-128-gcm", "aes-192-gcm", "aes-256-gcm", "chacha20-ietf-poly1305", "xchacha20-ietf-poly1305",
	"aes-128-cfb", "aes-192-cfb", "aes-256-cfb", "aes-128-ctr", "aes-192-ctr", "aes-256-ctr",
	"chacha20-ietf", "rc4-md5", "none", "xchacha20",
}

var r6b1RejectedCiphers = []string{
	"garbage-cipher", "aead_chacha20_poly1305", "bf-cfb", "cast5-cfb", "des-cfb", "idea-cfb",
	"rc2-cfb", "seed-cfb", "salsa20", "xsalsa20", "plain",
}

const r6b1Key16 = "AAECAwQFBgcICQoLDA0ODw=="
const r6b1Key16b = "EBESExQVFhcYGRobHB0eHw=="
const r6b1Key32 = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="

func r6b1SSPassword(cipher string) string {
	switch SS2022KeyLen(cipher) {
	case 16:
		return r6b1Key16
	case 32:
		return r6b1Key32
	}
	return "pw"
}

func r6b1SSEndpoint(name, cipher, password string) Endpoint {
	return Endpoint{Name: name, Host: "1.2.3.4", Port: 8388, Direct: true,
		Proxy: Proxy{"type": "ss", "server": "1.2.3.4", "port": 8388, "cipher": cipher, "password": password, "udp": true}}
}

var r6b1HealthySS = Endpoint{Name: "healthy", Host: "203.0.113.99", Port: 8388, Direct: true,
	Proxy: Proxy{"type": "ss", "server": "203.0.113.99", "port": 8388, "cipher": "aes-128-gcm", "password": "pw", "udp": true}}

// ---- 谓词逐值 ----

func TestR6B1ValidSSCipherPerValue(t *testing.T) {
	for _, c := range r6b1CommonCiphers {
		if !ValidSSCipher(c) {
			t.Fatalf("共同集 %q 须合法", c)
		}
		if !ValidSSCipher(strings.ToUpper(c)) {
			t.Fatalf("共同集大小写变体 %q 须合法（大小写不敏感）", strings.ToUpper(c))
		}
	}
	if !ValidSSCipher("chacha20") || !ValidSSCipher("CHACHA20") {
		t.Fatal("单面集 chacha20 须在谓词合法集内（进口不拒、分面处置）")
	}
	for _, c := range r6b1RejectedCiphers {
		if ValidSSCipher(c) {
			t.Fatalf("双拒格 %q 不得合法", c)
		}
		if ValidSSCipher(strings.ToUpper(c)) {
			t.Fatalf("双拒格大小写变体 %q 不得合法", strings.ToUpper(c))
		}
	}
	if ValidSSCipher("") || ValidSSCipher("chacha20-poly1305") || ValidSSCipher("aes-128-gcm2") {
		t.Fatal("集外值（空 / chacha20-poly1305 / 近名）不得合法")
	}
}

// ---- R6X-001 关闭断言组 1：进口四路径双拒格逐条拒绝、健康不连坐 ----

func TestR6B1SSCipherGarbageRejectedAllPaths(t *testing.T) {
	healthyClash := "  - {name: healthy, type: ss, server: 203.0.113.99, port: 8388, cipher: aes-128-gcm, password: pw, udp: true}\n"
	for _, m := range r6b1RejectedCiphers {
		uri := "ss://" + base64.RawURLEncoding.EncodeToString([]byte(m+":pw")) + "@1.2.3.4:8388#bad\nss://aes-128-gcm:pw@203.0.113.99:8388#healthy"
		docs := map[string]string{
			"clash":   "proxies:\n  - {name: bad, type: ss, server: 1.2.3.4, port: 8388, cipher: " + m + ", password: pw, udp: true}\n" + healthyClash,
			"uri":     uri,
			"singbox": `{"outbounds":[{"type":"shadowsocks","tag":"bad","server":"1.2.3.4","server_port":8388,"method":"` + m + `","password":"pw"},{"type":"shadowsocks","tag":"healthy","server":"203.0.113.99","server_port":8388,"method":"aes-128-gcm","password":"pw"}]}`,
			"surge":   "bad = ss, 1.2.3.4, 8388, encrypt-method=" + m + ", password=pw\nss://aes-128-gcm:pw@203.0.113.99:8388#healthy",
		}
		for path, doc := range docs {
			nodes, errs := ParseImport(doc)
			if len(errs) == 0 || len(nodes) != 1 || nodes[0].Name != "healthy" {
				t.Fatalf("cipher=%s path=%s：双拒格须逐条拒绝、健康不连坐，got nodes=%v errs=%v", m, path, nodes, errs)
			}
		}
	}
}

// TestR6B1SSUnknownCipherRejected：30 值候选集之外的未知/未来名
// （future-cipher-2099，R3 期望为演进兼容放行）自 R6 起按 BC-R6-01
// 「未知/双拒 method 不可导入」逐条拒绝（key 谓词本身不查未知名的
// 性质不变，由 TestR3B2ValidateSS2022PasswordHelper 直测维持）。
func TestR6B1SSUnknownCipherRejected(t *testing.T) {
	docs := map[string]string{
		"clash":   "proxies:\n  - {name: bad, type: ss, server: 1.2.3.4, port: 8388, cipher: future-cipher-2099, password: whatever, udp: true}\n",
		"uri":     "ss://" + base64.RawURLEncoding.EncodeToString([]byte("future-cipher-2099:whatever")) + "@1.2.3.4:8388#bad",
		"singbox": `{"outbounds":[{"type":"shadowsocks","tag":"bad","server":"1.2.3.4","server_port":8388,"method":"future-cipher-2099","password":"whatever"}]}`,
		"surge":   "bad = ss, 1.2.3.4, 8388, encrypt-method=future-cipher-2099, password=whatever",
	}
	for path, doc := range docs {
		nodes, errs := ParseImport(doc)
		if len(nodes) != 0 || len(errs) == 0 {
			t.Fatalf("path=%s：未知 cipher 须逐条拒绝，got nodes=%v errs=%v", path, nodes, errs)
		}
	}
}

// TestR6B1SSCipherLegalAcceptedAllPaths：合法集 19 值（18 共同 + chacha20）
// 进口不误拒；大小写变体愈合小写落库。
func TestR6B1SSCipherLegalAccepted(t *testing.T) {
	legal := append(append([]string{}, r6b1CommonCiphers...), "chacha20")
	for _, m := range legal {
		doc := "proxies:\n  - {name: ok, type: ss, server: 1.2.3.4, port: 8388, cipher: " + m + ", password: " + r6b1SSPassword(m) + ", udp: true}\n"
		nodes, errs := ParseImport(doc)
		if len(errs) != 0 || len(nodes) != 1 {
			t.Fatalf("cipher=%s 合法值不得误拒：nodes=%v errs=%v", m, nodes, errs)
		}
		uri := "ss://" + base64.RawURLEncoding.EncodeToString([]byte(m+":"+r6b1SSPassword(m))) + "@1.2.3.4:8388#ok"
		if nodes, errs := ParseImport(uri); len(errs) != 0 || len(nodes) != 1 {
			t.Fatalf("cipher=%s URI 合法值不得误拒：nodes=%v errs=%v", m, nodes, errs)
		}
	}
	for _, variant := range []string{"AES-128-GCM", "ChaCha20-IETF-Poly1305", "CHACHA20", "2022-BLAKE3-AES-128-GCM", "XCHACHA20", "NONE", "RC4-MD5"} {
		m := strings.ToLower(variant)
		doc := "proxies:\n  - {name: ok, type: ss, server: 1.2.3.4, port: 8388, cipher: " + variant + ", password: " + r6b1SSPassword(m) + ", udp: true}\n"
		nodes, errs := ParseImport(doc)
		if len(errs) != 0 || len(nodes) != 1 {
			t.Fatalf("大小写变体 %s 不得误拒：nodes=%v errs=%v", variant, nodes, errs)
		}
		if got := nodes[0].Proxy.str("cipher"); got != m {
			t.Fatalf("变体 %s 落库须小写愈合为 %s，got %s", variant, m, got)
		}
	}
}

// ---- R6X-001 关闭断言组 2：存量双面略过 + 单面分面 + 计数同口径 ----

func TestR6B1SSStockRenderSkip(t *testing.T) {
	for _, m := range r6b1RejectedCiphers {
		e := r6b1SSEndpoint("stock", m, "pw")
		if _, ok := proxySingbox(e); ok {
			t.Fatalf("cipher=%s 存量 sing-box 须整节点略过", m)
		}
		if _, ok := proxyClash(e); ok {
			t.Fatalf("cipher=%s 存量 Clash 须整节点略过", m)
		}
		eps := []Endpoint{e, r6b1HealthySS}
		if n := SubscriptionNodeCount(FormatSingbox, eps, SubOptions{}); n != 1 {
			t.Fatalf("cipher=%s sing-box 计数须同口径为 1，got %d", m, n)
		}
		if n := SubscriptionNodeCount(FormatClash, eps, SubOptions{}); n != 1 {
			t.Fatalf("cipher=%s Clash 计数须同口径为 1，got %d", m, n)
		}
		for _, format := range []string{FormatSingbox, FormatClash} {
			body, _, err := Subscription(format, eps, SubOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(body, m) {
				t.Fatalf("cipher=%s 不得出现在 %s 产物中", m, format)
			}
		}
	}
	// 大小写变体双拒格存量同样略过（谓词大小写不敏感）。
	e := r6b1SSEndpoint("stock", "GARBAGE-CIPHER", "pw")
	if _, ok := proxySingbox(e); ok {
		t.Fatal("GARBAGE-CIPHER 变体存量 sing-box 须略过")
	}
	if _, ok := proxyClash(e); ok {
		t.Fatal("GARBAGE-CIPHER 变体存量 Clash 须略过")
	}
	// 合法集逐值存量不误略（双面）。
	for _, m := range r6b1CommonCiphers {
		e := r6b1SSEndpoint("stock", m, r6b1SSPassword(m))
		if _, ok := proxySingbox(e); !ok {
			t.Fatalf("cipher=%s 合法存量 sing-box 不得误略", m)
		}
		if _, ok := proxyClash(e); !ok {
			t.Fatalf("cipher=%s 合法存量 Clash 不得误略", m)
		}
		eps := []Endpoint{e, r6b1HealthySS}
		if n := SubscriptionNodeCount(FormatSingbox, eps, SubOptions{}); n != 2 {
			t.Fatalf("cipher=%s sing-box 计数须为 2，got %d", m, n)
		}
		if n := SubscriptionNodeCount(FormatClash, eps, SubOptions{}); n != 2 {
			t.Fatalf("cipher=%s Clash 计数须为 2，got %d", m, n)
		}
	}
	// 合法集大小写变体存量不误略（forEndpoint 小写愈合 + 谓词不敏感）。
	e = r6b1SSEndpoint("stock", "AES-256-GCM", "pw")
	if _, ok := proxySingbox(e); !ok {
		t.Fatal("AES-256-GCM 变体存量 sing-box 不得误略")
	}
	if _, ok := proxyClash(e); !ok {
		t.Fatal("AES-256-GCM 变体存量 Clash 不得误略")
	}
}

// TestR6B1SSChacha20SingleFace：单面集 chacha20——进口不拒（见合法集
// 用例）、sing-box 面整节点略过、Clash 面保留，计数分面同口径。
func TestR6B1SSChacha20SingleFace(t *testing.T) {
	e := r6b1SSEndpoint("stock", "chacha20", "pw")
	if _, ok := proxySingbox(e); ok {
		t.Fatal("chacha20 存量 sing-box 须整节点略过（sing-box FATAL unknown method）")
	}
	node, ok := proxyClash(e)
	if !ok {
		t.Fatal("chacha20 存量 Clash 须保留（mihomo 接受）")
	}
	if got := Proxy(yamlToMap(node)).str("cipher"); got != "chacha20" {
		t.Fatalf("Clash 产物 cipher 须为 chacha20，got %q", got)
	}
	eps := []Endpoint{e, r6b1HealthySS}
	if n := SubscriptionNodeCount(FormatSingbox, eps, SubOptions{}); n != 1 {
		t.Fatalf("chacha20 sing-box 计数须为 1（仅健康节点），got %d", n)
	}
	if n := SubscriptionNodeCount(FormatClash, eps, SubOptions{}); n != 2 {
		t.Fatalf("chacha20 Clash 计数须为 2，got %d", n)
	}
}

// ---- R6X-002 关闭断言组：Clash 面 SS2022 坏 key 略过 + SIP023 防过修 ----

func TestR6B1SS2022BadKeyClashSkip(t *testing.T) {
	bad := r6b1SSEndpoint("stock", "2022-blake3-aes-128-gcm", "shared")
	if _, ok := proxyClash(bad); ok {
		t.Fatal("SS2022 坏 key 存量 Clash 须整节点略过（与 sing-box 面同口径）")
	}
	if _, ok := proxySingbox(bad); ok {
		t.Fatal("SS2022 坏 key 存量 sing-box 须整节点略过（既有守卫回归）")
	}
	eps := []Endpoint{bad, r6b1HealthySS}
	if n := SubscriptionNodeCount(FormatClash, eps, SubOptions{}); n != 1 {
		t.Fatalf("坏 key Clash 计数须同口径为 1，got %d", n)
	}
	body, _, err := Subscription(FormatClash, eps, SubOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "shared") {
		t.Fatal("坏 key 不得出现在 Clash 产物中")
	}
	// SIP023 多段中任一段坏 → 整节点略过（逐段谓词）。
	mixed := r6b1SSEndpoint("stock", "2022-blake3-aes-128-gcm", r6b1Key16+":shared")
	if _, ok := proxyClash(mixed); ok {
		t.Fatal("SIP023 含坏段存量 Clash 须整节点略过")
	}
}

func TestR6B1SS2022SIP023LegalClashVerbatim(t *testing.T) {
	// SIP023 合法多段（逐段均为规范 Std 形态）：Clash 产物逐字不变（防过修硬门）。
	pw := r6b1Key16 + ":" + r6b1Key16b
	e := r6b1SSEndpoint("stock", "2022-blake3-aes-128-gcm", pw)
	node, ok := proxyClash(e)
	if !ok {
		t.Fatal("SIP023 合法多段 Clash 不得误略")
	}
	if got := Proxy(yamlToMap(node)).str("password"); got != pw {
		t.Fatalf("SIP023 合法多段 Clash 产物须逐字不变：got %q want %q", got, pw)
	}
	if _, ok := proxySingbox(e); !ok {
		t.Fatal("SIP023 合法多段 sing-box 不得误略")
	}
	// 合法单段两面不误略。
	e2 := r6b1SSEndpoint("stock", "2022-blake3-aes-256-gcm", r6b1Key32)
	if _, ok := proxyClash(e2); !ok {
		t.Fatal("SS2022 合法单段 Clash 不得误略")
	}
	if _, ok := proxySingbox(e2); !ok {
		t.Fatal("SS2022 合法单段 sing-box 不得误略")
	}
}

// ---- R6X-003 关闭断言组：Xray 链式面五格面板侧被拒 + 健康三格 ----
// （链式面 SS cipher 双拒格归 R6X-001 证据集，单列一组不用例。）

func TestR6B1ChainTargetGuards(t *testing.T) {
	goodPBK := "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"
	mk := func(p Proxy) Endpoint {
		p["server"] = "1.2.3.4"
		p["port"] = 443
		return Endpoint{Name: "chain", Host: "1.2.3.4", Port: 443, Proxy: p}
	}
	bad := map[string]Proxy{
		"reality-pbk": {"type": "vless", "uuid": r5b1UUID, "tls": true, "reality-opts": map[string]interface{}{"public-key": "GARBAGE!!", "short-id": "ab12"}},
		"reality-sid": {"type": "vless", "uuid": r5b1UUID, "tls": true, "reality-opts": map[string]interface{}{"public-key": goodPBK, "short-id": "zz"}},
		"vless-flow":  {"type": "vless", "uuid": r5b1UUID, "flow": "garbage-flow"},
		"vless-enc":   {"type": "vless", "uuid": r5b1UUID, "encryption": "garbage-enc"},
		"ss2022-key":  {"type": "ss", "cipher": "2022-blake3-aes-128-gcm", "password": "shared"},
	}
	for name, p := range bad {
		e := mk(p)
		if _, err := chainTargetFromProxy(e); err == nil {
			t.Fatalf("%s：链式目标坏存量须在面板侧被拒", name)
		}
		if _, err := xrayChainOutbound(e, "via-1"); err == nil {
			t.Fatalf("%s：xrayChainOutbound 须报错、不产出可下发配置", name)
		}
		if err := CheckChainWith(TypeXray, e, RenderOptions{}); err == nil {
			t.Fatalf("%s：CheckChainWith(Xray) 须报错（保存期同点生效）", name)
		}
	}
	good := map[string]Proxy{
		"vmess-auto":    {"type": "vmess", "uuid": r5b1UUID, "cipher": "auto"},
		"vless-reality": {"type": "vless", "uuid": r5b1UUID, "flow": "xtls-rprx-vision", "tls": true, "reality-opts": map[string]interface{}{"public-key": goodPBK, "short-id": "ab12"}},
		"ss-aes128gcm":  {"type": "ss", "cipher": "aes-128-gcm", "password": "pw"},
	}
	for name, p := range good {
		e := mk(p)
		if _, err := chainTargetFromProxy(e); err != nil {
			t.Fatalf("%s：健康链式目标不得误拒：%v", name, err)
		}
		if _, err := xrayChainOutbound(e, "via-1"); err != nil {
			t.Fatalf("%s：健康链式目标须可构建出站：%v", name, err)
		}
	}
}

// TestR6B1ChainSSCipherGarbageRejected 归 R6X-001 证据集：SS cipher
// 双拒格与单面 chacha20 在 Xray 链式面（非 mihomo 面）面板侧被拒。
func TestR6B1ChainSSCipherGarbageRejected(t *testing.T) {
	for _, m := range append(append([]string{}, r6b1RejectedCiphers...), "chacha20") {
		e := Endpoint{Name: "chain", Host: "1.2.3.4", Port: 8388,
			Proxy: Proxy{"type": "ss", "server": "1.2.3.4", "port": 8388, "cipher": m, "password": "pw"}}
		if _, err := chainTargetFromProxy(e); err == nil {
			t.Fatalf("cipher=%s 链式目标须在面板侧被拒（Xray 非 mihomo 面）", m)
		}
	}
}

// ---- 无二进制四面同批设门（恒标未验证：仅代码级断言，不宣称已验关闭） ----

func TestR6B1SSOtherFacesGate(t *testing.T) {
	// Surge（proxySurge）与 URI（proxyURI）：双拒 cipher 与 SS2022 坏 key 略过。
	for _, m := range r6b1RejectedCiphers {
		e := r6b1SSEndpoint("stock", m, "pw")
		if _, ok := proxySurge(e); ok {
			t.Fatalf("cipher=%s Surge 须略过（未验证面同批设门）", m)
		}
		if _, ok := proxyURI(e); ok {
			t.Fatalf("cipher=%s URI 须略过（未验证面同批设门）", m)
		}
	}
	badKey := r6b1SSEndpoint("stock", "2022-blake3-aes-128-gcm", "shared")
	if _, ok := proxySurge(badKey); ok {
		t.Fatal("SS2022 坏 key Surge 须略过")
	}
	if _, ok := proxyURI(badKey); ok {
		t.Fatal("SS2022 坏 key URI 须略过")
	}
	healthy := r6b1SSEndpoint("stock", "aes-128-gcm", "pw")
	if _, ok := proxySurge(healthy); !ok {
		t.Fatal("健康 SS Surge 不得误略")
	}
	if _, ok := proxyURI(healthy); !ok {
		t.Fatal("健康 SS URI 不得误略")
	}
	// Loon / QX：cipher 白名单自守维持（双拒格本已略过）；新增 SS2022
	// 坏 key 门——坏 key 略过、合法 2022 节点保留。
	for _, m := range r6b1RejectedCiphers {
		p := Proxy{"type": "ss", "name": "stock", "server": "1.2.3.4", "port": 8388, "cipher": m, "password": "pw", "udp": true}
		if _, ok := loonLine(p); ok {
			t.Fatalf("cipher=%s Loon 须略过（白名单自守）", m)
		}
		if _, ok := qxLine(p); ok {
			t.Fatalf("cipher=%s QX 须略过（白名单自守）", m)
		}
	}
	pBad := Proxy{"type": "ss", "name": "stock", "server": "1.2.3.4", "port": 8388, "cipher": "2022-blake3-aes-128-gcm", "password": "shared", "udp": true}
	if _, ok := loonLine(pBad); ok {
		t.Fatal("SS2022 坏 key Loon 须略过")
	}
	if _, ok := qxLine(pBad); ok {
		t.Fatal("SS2022 坏 key QX 须略过")
	}
	pGood := Proxy{"type": "ss", "name": "stock", "server": "1.2.3.4", "port": 8388, "cipher": "2022-blake3-aes-128-gcm", "password": r6b1Key16, "udp": true}
	if _, ok := loonLine(pGood); !ok {
		t.Fatal("SS2022 合法节点 Loon 不得误略")
	}
	if _, ok := qxLine(pGood); !ok {
		t.Fatal("SS2022 合法节点 QX 不得误略")
	}
	// 计数同口径（Loon/QX 经 loonLine/qxLine 同门）。
	eps := []Endpoint{badKey, r6b1HealthySS}
	if n := SubscriptionNodeCount(FormatLoon, eps, SubOptions{}); n != 1 {
		t.Fatalf("坏 key Loon 计数须为 1，got %d", n)
	}
	if n := SubscriptionNodeCount(FormatQX, eps, SubOptions{}); n != 1 {
		t.Fatalf("坏 key QX 计数须为 1，got %d", n)
	}
	if n := SubscriptionNodeCount(FormatSurge, eps, SubOptions{}); n != 1 {
		t.Fatalf("坏 key Surge 计数须为 1，got %d", n)
	}
	if n := SubscriptionNodeCount(FormatBase64, eps, SubOptions{}); n != 1 {
		t.Fatalf("坏 key base64/URI 计数须为 1，got %d", n)
	}
}
