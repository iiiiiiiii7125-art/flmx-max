package kernel

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// R11 批 2（未设门渲染面族，断言分面、不得互替，N-35）：
//
//   - R11X-002（P3，URI 面）：proxyURI 其余六支此前无谓词门，存量
//     垃圾枚举值原样发链（M2-01 ≡ M6-02，verdicts §1 终裁 P3）。
//     修复：vmess/trojan/anytls/hysteria2/tuic 支入口统一过
//     validImportedProxyFields(p, ssFaceOther)；ss 支改经统一谓词
//     （ssFaceClash——本面保留 SS 单面集，见 proxy_produce.go 注），
//     plugin-opts.mode 格并入谓词。关闭断言 = A2-01/02/03/05/07/08/11
//     存量垃圾格全转 SKIP + 合法/变体格产物逐字不变
//     （TestR11B2URIFaceGate）。
//   - R11X-003（P2，Surge/Loon/QX 面）：三面无统一谓词门，存量垃圾
//     原样下发（M2-02）。修复：proxySurge 入口与 loonLine/qxLine
//     入口补谓词门，面参数逐面取 ssFaceClash（SS 单面集 chacha20
//     在三面合法，不得以 sing-box/other 面参数误杀；其余单面集值
//     在 Loon/QX 由既有白名单继续略过）。关闭断言 = A2-03/04/05/11
//     存量垃圾格全转 SKIP + 合法/变体/单面集格逐字不变 + 三面既有
//     手工门（Hy2 obfs 略过、SS 白名单、VLESS flow、ENC 略过）
//     不回归（TestR11B2SurgeLoonQXFaceGate）。
//
// 存量格以直构 Proxy 模拟修复前落库行（与 M2 探针同口径，绕过进口）；
// Loon/QX 经 endpointProxy→loonLine/qxLine 的生产路径渲染。

const r11b2UUID = "11111111-2222-3333-4444-555555555555"

var r11b2PBK = strings.Repeat("A", 43)

func r11b2EP(p Proxy) Endpoint {
	return Endpoint{Name: "m2", Host: "203.0.113.10", Port: 443, Direct: true, Proxy: p}
}

func r11b2Loon(t *testing.T, e Endpoint) (string, bool) {
	t.Helper()
	p, ok := endpointProxy(e)
	if !ok {
		return "", false
	}
	return loonLine(p)
}

func r11b2QX(t *testing.T, e Endpoint) (string, bool) {
	t.Helper()
	p, ok := endpointProxy(e)
	if !ok {
		return "", false
	}
	return qxLine(p)
}

// ---- 存量垃圾格构造（与 R11-M2 探针同格） ----

func r11b2Garbage() map[string]Endpoint {
	return map[string]Endpoint{
		"A2-01 vmess fp bogus": r11b2EP(Proxy{"type": "vmess", "server": "203.0.113.10", "port": 443, "uuid": r11b2UUID, "cipher": "auto", "tls": true, "servername": "a.example.com", "client-fingerprint": "bogus123"}),
		"A2-01 trojan fp bogus": r11b2EP(Proxy{"type": "trojan", "server": "203.0.113.10", "port": 443, "password": "pw", "tls": true, "sni": "a.example.com", "client-fingerprint": "bogus123"}),
		"A2-01 anytls fp bogus": r11b2EP(Proxy{"type": "anytls", "server": "203.0.113.10", "port": 443, "password": "pw", "sni": "a.example.com", "client-fingerprint": "bogus123"}),
		"A2-02 trojan xhttp bogus": r11b2EP(Proxy{"type": "trojan", "server": "203.0.113.10", "port": 443, "password": "pw", "tls": true, "sni": "a.example.com", "network": "xhttp", "xhttp-opts": map[string]interface{}{"path": "/xh", "mode": "bogus"}}),
		"A2-03 ss plugin bogus": r11b2EP(Proxy{"type": "ss", "server": "203.0.113.10", "port": 443, "cipher": "aes-128-gcm", "password": "pw", "plugin": "obfs", "plugin-opts": map[string]interface{}{"mode": "bogus", "host": "bing.com"}}),
		"A2-04 snell obfs bogus": r11b2EP(Proxy{"type": "snell", "server": "203.0.113.10", "port": 443, "psk": "psk12345678", "version": 4, "obfs-opts": map[string]interface{}{"mode": "bogus", "host": "bing.com"}}),
		"A2-05 vmess cipher bogus": r11b2EP(Proxy{"type": "vmess", "server": "203.0.113.10", "port": 443, "uuid": r11b2UUID, "cipher": "bogus"}),
		"A2-07 hy2 obfs bogus": r11b2EP(Proxy{"type": "hysteria2", "server": "203.0.113.10", "port": 443, "password": "pw", "obfs": "bogus", "obfs-password": "opw"}),
		"A2-08 tuic cc bogus": r11b2EP(Proxy{"type": "tuic", "server": "203.0.113.10", "port": 443, "uuid": r11b2UUID, "password": "pw", "congestion-controller": "bogus"}),
		"A2-11 vless reality badsid": r11b2EP(Proxy{"type": "vless", "server": "203.0.113.10", "port": 443, "uuid": r11b2UUID, "tls": true, "servername": "a.example.com", "reality-opts": map[string]interface{}{"public-key": r11b2PBK, "short-id": "0123ABCD"}}),
		"A2-11 vless reality badpbk": r11b2EP(Proxy{"type": "vless", "server": "203.0.113.10", "port": 443, "uuid": r11b2UUID, "tls": true, "servername": "a.example.com", "reality-opts": map[string]interface{}{"public-key": "not-base64!!!", "short-id": "0123abcd"}}),
		"A2-11 trojan reality badsid": r11b2EP(Proxy{"type": "trojan", "server": "203.0.113.10", "port": 443, "password": "pw", "tls": true, "sni": "a.example.com", "reality-opts": map[string]interface{}{"public-key": r11b2PBK, "short-id": "0123ABCD"}}),
	}
}

// ---- R11X-002：URI 面 ----

func TestR11B2URIFaceGate(t *testing.T) {
	// 垃圾格全转 SKIP：A2-01（vmess/trojan/anytls fp）、A2-02
	// （trojan xhttp mode）、A2-03（ss plugin mode）、A2-05（vmess
	// cipher）、A2-07（hy2 obfs）、A2-08（tuic cc）、A2-11
	// （trojan Reality）——逐格点名，任一格发链即失败。
	uriGarbage := []string{
		"A2-01 vmess fp bogus", "A2-01 trojan fp bogus", "A2-01 anytls fp bogus",
		"A2-02 trojan xhttp bogus", "A2-03 ss plugin bogus", "A2-05 vmess cipher bogus",
		"A2-07 hy2 obfs bogus", "A2-08 tuic cc bogus", "A2-11 trojan reality badsid",
	}
	g := r11b2Garbage()
	for _, name := range uriGarbage {
		if uri, ok := proxyURI(g[name]); ok {
			t.Fatalf("R11X-002 %s：URI 面存量垃圾须不发链，got %q", name, uri)
		}
	}
	// 合法/变体格产物逐字不变（期望串 = 修复前基线逐字，M2 探针
	// 与本批修复前探针双据）。
	legal := []struct {
		name string
		e    Endpoint
		want string
	}{
		{"vmess auto+chrome", r11b2EP(Proxy{"type": "vmess", "server": "203.0.113.10", "port": 443, "uuid": r11b2UUID, "cipher": "auto", "tls": true, "servername": "a.example.com", "client-fingerprint": "chrome"}),
			"vmess://eyJhZGQiOiIyMDMuMC4xMTMuMTAiLCJhaWQiOiIwIiwiZnAiOiJjaHJvbWUiLCJob3N0IjoiIiwiaWQiOiIxMTExMTExMS0yMjIyLTMzMzMtNDQ0NC01NTU1NTU1NTU1NTUiLCJuZXQiOiJ0Y3AiLCJwYXRoIjoiIiwicG9ydCI6IjQ0MyIsInBzIjoibTIiLCJzY3kiOiJhdXRvIiwic25pIjoiYS5leGFtcGxlLmNvbSIsInRscyI6InRscyIsInR5cGUiOiJub25lIiwidiI6IjIifQ=="},
		{"trojan legal", r11b2EP(Proxy{"type": "trojan", "server": "203.0.113.10", "port": 443, "password": "pw", "tls": true, "sni": "a.example.com", "client-fingerprint": "chrome"}),
			"trojan://pw@203.0.113.10:443?fp=chrome&security=tls&sni=a.example.com&type=tcp#m2"},
		{"anytls legal", r11b2EP(Proxy{"type": "anytls", "server": "203.0.113.10", "port": 443, "password": "pw", "sni": "a.example.com", "client-fingerprint": "chrome"}),
			"anytls://pw@203.0.113.10:443/?fp=chrome&sni=a.example.com#m2"},
		{"ss plugin http", r11b2EP(Proxy{"type": "ss", "server": "203.0.113.10", "port": 443, "cipher": "aes-128-gcm", "password": "pw", "plugin": "obfs", "plugin-opts": map[string]interface{}{"mode": "http", "host": "bing.com"}}),
			"ss://YWVzLTEyOC1nY206cHc@203.0.113.10:443/?plugin=obfs-local%3Bobfs%3Dhttp%3Bobfs-host%3Dbing.com#m2"},
		{"ss plugin HTTP variant", r11b2EP(Proxy{"type": "ss", "server": "203.0.113.10", "port": 443, "cipher": "aes-128-gcm", "password": "pw", "plugin": "obfs", "plugin-opts": map[string]interface{}{"mode": "HTTP", "host": "bing.com"}}),
			"ss://YWVzLTEyOC1nY206cHc@203.0.113.10:443/?plugin=obfs-local%3Bobfs%3Dhttp%3Bobfs-host%3Dbing.com#m2"},
		{"ss chacha20 singleface", r11b2EP(Proxy{"type": "ss", "server": "203.0.113.10", "port": 443, "cipher": "chacha20", "password": "pw"}),
			"ss://Y2hhY2hhMjA6cHc@203.0.113.10:443#m2"},
		{"ss aes-128-gcm-siv singleface", r11b2EP(Proxy{"type": "ss", "server": "203.0.113.10", "port": 443, "cipher": "aes-128-gcm-siv", "password": "pw"}),
			"ss://YWVzLTEyOC1nY20tc2l2OnB3@203.0.113.10:443#m2"},
		{"hy2 salamander", r11b2EP(Proxy{"type": "hysteria2", "server": "203.0.113.10", "port": 443, "password": "pw", "obfs": "salamander", "obfs-password": "opw"}),
			"hysteria2://pw@203.0.113.10:443/?obfs=salamander&obfs-password=opw#m2"},
		{"hy2 gecko", r11b2EP(Proxy{"type": "hysteria2", "server": "203.0.113.10", "port": 443, "password": "pw", "obfs": "gecko", "obfs-password": "opw"}),
			"hysteria2://pw@203.0.113.10:443/?obfs=gecko&obfs-password=opw#m2"},
		{"tuic bbr", r11b2EP(Proxy{"type": "tuic", "server": "203.0.113.10", "port": 443, "uuid": r11b2UUID, "password": "pw", "congestion-controller": "bbr"}),
			"tuic://" + r11b2UUID + ":pw@203.0.113.10:443?congestion_control=bbr#m2"},
		{"trojan xhttp auto", r11b2EP(Proxy{"type": "trojan", "server": "203.0.113.10", "port": 443, "password": "pw", "tls": true, "sni": "a.example.com", "network": "xhttp", "xhttp-opts": map[string]interface{}{"path": "/xh", "mode": "auto"}}),
			"trojan://pw@203.0.113.10:443?mode=auto&path=%2Fxh&security=tls&sni=a.example.com&type=xhttp#m2"},
		{"trojan reality good", r11b2EP(Proxy{"type": "trojan", "server": "203.0.113.10", "port": 443, "password": "pw", "tls": true, "sni": "a.example.com", "reality-opts": map[string]interface{}{"public-key": r11b2PBK, "short-id": "0123abcd"}}),
			"trojan://pw@203.0.113.10:443?pbk=" + r11b2PBK + "&security=reality&sid=0123abcd&sni=a.example.com&type=tcp#m2"},
		{"vmess aes-128-gcm", r11b2EP(Proxy{"type": "vmess", "server": "203.0.113.10", "port": 443, "uuid": r11b2UUID, "cipher": "aes-128-gcm", "tls": true, "servername": "a.example.com", "client-fingerprint": "Chrome"}),
			""}, // 逐字串见下方 contains 断言（base64 载荷含愈合 cipher/fp）
	}
	for _, c := range legal {
		uri, ok := proxyURI(c.e)
		if !ok {
			t.Fatalf("R11X-002 合法格 %s：URI 须照常发链", c.name)
		}
		if c.want != "" && uri != c.want {
			t.Fatalf("R11X-002 合法格 %s：URI 产物须逐字不变\ngot  %q\nwant %q", c.name, uri, c.want)
		}
	}
	// vmess 变体格（Chrome fp + AES-128-GCM cipher）：forEndpoint
	// 愈合后发链、载荷含愈合值（与修复前探针逐字一致的解码断言）。
	uri, _ := proxyURI(legal[len(legal)-1].e)
	raw, err := decodeVMessURI(uri)
	if err != nil || raw["fp"] != "chrome" || raw["scy"] != "aes-128-gcm" {
		t.Fatalf("R11X-002 vmess 变体格：载荷须含愈合 fp/scy，got %v err=%v", raw, err)
	}
}

// ---- R11X-003：Surge / Loon / QX 面 ----

func TestR11B2SurgeLoonQXFaceGate(t *testing.T) {
	g := r11b2Garbage()
	// A2-03（ss plugin mode bogus）：三面全转 SKIP。
	e := g["A2-03 ss plugin bogus"]
	if line, ok := proxySurge(e); ok {
		t.Fatalf("R11X-003 A2-03：Surge 须略过，got %q", line)
	}
	if line, ok := r11b2Loon(t, e); ok {
		t.Fatalf("R11X-003 A2-03：Loon 须略过，got %q", line)
	}
	if line, ok := r11b2QX(t, e); ok {
		t.Fatalf("R11X-003 A2-03：QX 须略过，got %q", line)
	}
	// A2-04（snell obfs mode bogus）：Surge 转 SKIP（Loon/QX 无
	// snell、本已略过，维持）。
	e = g["A2-04 snell obfs bogus"]
	if line, ok := proxySurge(e); ok {
		t.Fatalf("R11X-003 A2-04：Surge 须略过，got %q", line)
	}
	if _, ok := r11b2Loon(t, e); ok {
		t.Fatal("R11X-003 A2-04：Loon 无 snell 须维持略过")
	}
	if _, ok := r11b2QX(t, e); ok {
		t.Fatal("R11X-003 A2-04：QX 无 snell 须维持略过")
	}
	// A2-05（vmess cipher bogus）：Loon/QX 转 SKIP；Surge 经入口
	// 门同转 SKIP（Surge 行本不载 cipher，入口门按整节点口径）。
	e = g["A2-05 vmess cipher bogus"]
	if line, ok := r11b2Loon(t, e); ok {
		t.Fatalf("R11X-003 A2-05：Loon 须略过，got %q", line)
	}
	if line, ok := r11b2QX(t, e); ok {
		t.Fatalf("R11X-003 A2-05：QX 须略过，got %q", line)
	}
	if line, ok := proxySurge(e); ok {
		t.Fatalf("R11X-003 A2-05：Surge 入口门须略过，got %q", line)
	}
	// A2-11（Reality 坏格，vless badsid/badpbk + trojan badsid）：
	// Loon/QX 转 SKIP；Surge 对 trojan 坏格经入口门转 SKIP
	// （vless 本不在 Surge 面、维持略过）。
	for _, name := range []string{"A2-11 vless reality badsid", "A2-11 vless reality badpbk", "A2-11 trojan reality badsid"} {
		e = g[name]
		if line, ok := r11b2Loon(t, e); ok {
			t.Fatalf("R11X-003 %s：Loon 须略过，got %q", name, line)
		}
		if line, ok := r11b2QX(t, e); ok {
			t.Fatalf("R11X-003 %s：QX 须略过，got %q", name, line)
		}
		if line, ok := proxySurge(e); ok {
			t.Fatalf("R11X-003 %s：Surge 须略过，got %q", name, line)
		}
	}

	// 合法/变体/单面集格逐字不变（期望串 = 修复前基线逐字）。
	ssHTTP := r11b2EP(Proxy{"type": "ss", "server": "203.0.113.10", "port": 443, "cipher": "aes-128-gcm", "password": "pw", "plugin": "obfs", "plugin-opts": map[string]interface{}{"mode": "http", "host": "bing.com"}})
	if line, ok := proxySurge(ssHTTP); !ok || line != "m2 = ss, 203.0.113.10, 443, encrypt-method=aes-128-gcm, password=pw, obfs=http, obfs-host=bing.com" {
		t.Fatalf("R11X-003 合法 ss plugin http Surge 须逐字不变：ok=%v line=%q", ok, line)
	}
	if line, ok := r11b2Loon(t, ssHTTP); !ok || line != `m2=shadowsocks,203.0.113.10,443,aes-128-gcm,"pw",obfs-name=http,obfs-host=bing.com` {
		t.Fatalf("R11X-003 合法 ss plugin http Loon 须逐字不变：ok=%v line=%q", ok, line)
	}
	if line, ok := r11b2QX(t, ssHTTP); !ok || line != "shadowsocks=203.0.113.10:443, method=aes-128-gcm, password=pw, obfs=http, obfs-host=bing.com, tag=m2" {
		t.Fatalf("R11X-003 合法 ss plugin http QX 须逐字不变：ok=%v line=%q", ok, line)
	}
	// 变体格（plugin mode HTTP 大写）：三面经 forEndpoint 愈合后
	// 产物与规范格逐字一致（防过修：不得因入口门误杀变体）。
	ssHTTPUp := r11b2EP(Proxy{"type": "ss", "server": "203.0.113.10", "port": 443, "cipher": "aes-128-gcm", "password": "pw", "plugin": "obfs", "plugin-opts": map[string]interface{}{"mode": "HTTP", "host": "bing.com"}})
	if line, ok := proxySurge(ssHTTPUp); !ok || !strings.Contains(line, "obfs=http") {
		t.Fatalf("R11X-003 变体 ss plugin HTTP Surge 须愈合下发：ok=%v line=%q", ok, line)
	}
	if line, ok := r11b2Loon(t, ssHTTPUp); !ok || !strings.Contains(line, "obfs-name=http") {
		t.Fatalf("R11X-003 变体 ss plugin HTTP Loon 须愈合下发：ok=%v line=%q", ok, line)
	}
	if line, ok := r11b2QX(t, ssHTTPUp); !ok || !strings.Contains(line, "obfs=http") {
		t.Fatalf("R11X-003 变体 ss plugin HTTP QX 须愈合下发：ok=%v line=%q", ok, line)
	}
	// 单面集格（ss chacha20，三面合法）：逐字不变、不得误杀。
	cha := r11b2EP(Proxy{"type": "ss", "server": "203.0.113.10", "port": 443, "cipher": "chacha20", "password": "pw"})
	if line, ok := proxySurge(cha); !ok || line != "m2 = ss, 203.0.113.10, 443, encrypt-method=chacha20, password=pw, udp-relay=true" {
		t.Fatalf("R11X-003 单面集 chacha20 Surge 须逐字不变：ok=%v line=%q", ok, line)
	}
	if line, ok := r11b2Loon(t, cha); !ok || line != `m2=shadowsocks,203.0.113.10,443,chacha20,"pw"` {
		t.Fatalf("R11X-003 单面集 chacha20 Loon 须逐字不变（不得以 other/singbox 面误杀）：ok=%v line=%q", ok, line)
	}
	if line, ok := r11b2QX(t, cha); !ok || line != "shadowsocks=203.0.113.10:443, method=chacha20, password=pw, tag=m2" {
		t.Fatalf("R11X-003 单面集 chacha20 QX 须逐字不变（不得以 other/singbox 面误杀）：ok=%v line=%q", ok, line)
	}
	// 单面集其余值（aes-128-gcm-siv）：Surge 保留（同谓词口径）、
	// Loon/QX 白名单外维持略过（白名单未动、非误杀回归）。
	siv := r11b2EP(Proxy{"type": "ss", "server": "203.0.113.10", "port": 443, "cipher": "aes-128-gcm-siv", "password": "pw"})
	if line, ok := proxySurge(siv); !ok || !strings.Contains(line, "encrypt-method=aes-128-gcm-siv") {
		t.Fatalf("R11X-003 单面集 aes-128-gcm-siv Surge 须保留：ok=%v line=%q", ok, line)
	}
	if _, ok := r11b2Loon(t, siv); ok {
		t.Fatal("R11X-003 aes-128-gcm-siv Loon 须维持白名单外略过")
	}
	if _, ok := r11b2QX(t, siv); ok {
		t.Fatal("R11X-003 aes-128-gcm-siv QX 须维持白名单外略过")
	}
	// Snell 合法/变体格（Surge）：http 与 HTTP 变体、shadow-tls
	// 别名愈合产物逐字不变。
	for _, mode := range []string{"http", "HTTP"} {
		e := r11b2EP(Proxy{"type": "snell", "server": "203.0.113.10", "port": 443, "psk": "psk12345678", "version": 4, "obfs-opts": map[string]interface{}{"mode": mode, "host": "bing.com"}})
		if line, ok := proxySurge(e); !ok || line != "m2 = snell, 203.0.113.10, 443, psk=psk12345678, version=4, obfs=http, obfs-host=bing.com" {
			t.Fatalf("R11X-003 Snell obfs %s Surge 须逐字不变：ok=%v line=%q", mode, ok, line)
		}
	}
	e = r11b2EP(Proxy{"type": "snell", "server": "203.0.113.10", "port": 443, "psk": "psk12345678", "version": 4, "obfs-opts": map[string]interface{}{"mode": "shadow-tls", "host": "bing.com"}})
	if line, ok := proxySurge(e); !ok || !strings.Contains(line, "obfs=shadow-tls") {
		t.Fatalf("R11X-003 Snell obfs shadow-tls Surge 须保留：ok=%v line=%q", ok, line)
	}
	// Reality 合法格（Loon/QX 逐字不变）。
	goodR := r11b2EP(Proxy{"type": "vless", "server": "203.0.113.10", "port": 443, "uuid": r11b2UUID, "tls": true, "servername": "a.example.com", "reality-opts": map[string]interface{}{"public-key": r11b2PBK, "short-id": "0123abcd"}})
	if line, ok := r11b2Loon(t, goodR); !ok || line != `m2=vless,203.0.113.10,443,"`+r11b2UUID+`",transport=tcp,over-tls=true,sni=a.example.com,public-key="`+r11b2PBK+`",short-id=0123abcd` {
		t.Fatalf("R11X-003 合法 Reality Loon 须逐字不变：ok=%v line=%q", ok, line)
	}
	if line, ok := r11b2QX(t, goodR); !ok || line != "vless=203.0.113.10:443, method=none, password="+r11b2UUID+", obfs=over-tls, obfs-host=a.example.com, tls-verification=true, reality-base64-pubkey="+r11b2PBK+", reality-hex-shortid=0123abcd, tag=m2" {
		t.Fatalf("R11X-003 合法 Reality QX 须逐字不变：ok=%v line=%q", ok, line)
	}
	// VMess 合法格（Loon 逐字不变）。
	vm := r11b2EP(Proxy{"type": "vmess", "server": "203.0.113.10", "port": 443, "uuid": r11b2UUID, "cipher": "aes-128-gcm", "tls": true, "servername": "a.example.com", "client-fingerprint": "Chrome"})
	if line, ok := r11b2Loon(t, vm); !ok || line != `m2=vmess,203.0.113.10,443,aes-128-gcm,"`+r11b2UUID+`",transport=tcp,over-tls=true,tls-name=a.example.com,alterId=0` {
		t.Fatalf("R11X-003 合法 vmess Loon 须逐字不变：ok=%v line=%q", ok, line)
	}
	if line, ok := r11b2QX(t, vm); !ok || !strings.Contains(line, "method=aes-128-gcm") {
		t.Fatalf("R11X-003 合法 vmess QX 须保留：ok=%v line=%q", ok, line)
	}

	// ---- 既有手工门不回归（逐门点名） ----
	// Hy2 obfs 手工门：Surge/Loon 对非 salamander（gecko）略过、
	// salamander 保留（谓词放行 gecko，略过须仍由手工门执行）。
	gecko := r11b2EP(Proxy{"type": "hysteria2", "server": "203.0.113.10", "port": 443, "password": "pw", "obfs": "gecko", "obfs-password": "opw"})
	if _, ok := proxySurge(gecko); ok {
		t.Fatal("手工门回归：Hy2 gecko Surge 须维持略过")
	}
	if _, ok := r11b2Loon(t, gecko); ok {
		t.Fatal("手工门回归：Hy2 gecko Loon 须维持略过")
	}
	sala := r11b2EP(Proxy{"type": "hysteria2", "server": "203.0.113.10", "port": 443, "password": "pw", "obfs": "salamander", "obfs-password": "opw"})
	if _, ok := proxySurge(sala); !ok {
		t.Fatal("手工门回归：Hy2 salamander Surge 须保留")
	}
	if _, ok := r11b2Loon(t, sala); !ok {
		t.Fatal("手工门回归：Hy2 salamander Loon 须保留")
	}
	// SS 白名单手工门：白名单外 cipher（aes-128-ctr，共同集内、
	// 谓词放行）在 Loon/QX 仍须由白名单略过。
	ctr := r11b2EP(Proxy{"type": "ss", "server": "203.0.113.10", "port": 443, "cipher": "aes-128-ctr", "password": "pw"})
	if _, ok := r11b2Loon(t, ctr); ok {
		t.Fatal("手工门回归：aes-128-ctr Loon 须维持白名单略过")
	}
	if _, ok := r11b2QX(t, ctr); ok {
		t.Fatal("手工门回归：aes-128-ctr QX 须维持白名单略过")
	}
	// VLESS flow 手工门：非 vision flow 在 Loon/QX 略过、vision 保留。
	flowBad := r11b2EP(Proxy{"type": "vless", "server": "203.0.113.10", "port": 443, "uuid": r11b2UUID, "tls": true, "servername": "a.example.com", "flow": "xtls-rprx-direct"})
	if _, ok := r11b2Loon(t, flowBad); ok {
		t.Fatal("手工门回归：非 vision flow Loon 须维持略过")
	}
	if _, ok := r11b2QX(t, flowBad); ok {
		t.Fatal("手工门回归：非 vision flow QX 须维持略过")
	}
	flowGood := r11b2EP(Proxy{"type": "vless", "server": "203.0.113.10", "port": 443, "uuid": r11b2UUID, "tls": true, "servername": "a.example.com", "flow": "xtls-rprx-vision"})
	if line, ok := r11b2Loon(t, flowGood); !ok || !strings.Contains(line, "flow=xtls-rprx-vision") {
		t.Fatalf("手工门回归：vision flow Loon 须保留：ok=%v line=%q", ok, line)
	}
	if line, ok := r11b2QX(t, flowGood); !ok || !strings.Contains(line, "vless-flow=xtls-rprx-vision") {
		t.Fatalf("手工门回归：vision flow QX 须保留：ok=%v line=%q", ok, line)
	}
	// ENC 手工门：合法 ENC 节点（谓词放行）在 Loon/QX 仍整节点
	// 略过（无 ENC 表达），不得因入口门改变处置。
	encStr := "mlkem768x25519plus.native.0rtt.100-111-1111.75-0-111.50-0-3333." + r11b2PBK
	encNode := r11b2EP(Proxy{"type": "vless", "server": "203.0.113.10", "port": 443, "uuid": r11b2UUID, "encryption": encStr})
	if _, ok := r11b2Loon(t, encNode); ok {
		t.Fatal("手工门回归：合法 ENC Loon 须维持略过")
	}
	if _, ok := r11b2QX(t, encNode); ok {
		t.Fatal("手工门回归：合法 ENC QX 须维持略过")
	}
}

func decodeVMessURI(uri string) (map[string]string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, "vmess://"))
	if err != nil {
		return nil, err
	}
	var v map[string]string
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return v, nil
}
