package kernel

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

// R3 批 2（R3X-008）：导入侧 SS2022 密钥格式校验收口回归。
// 口径（R3-X-verdicts §3 硬口径）：
//   - 仅三 cipher 查（2022-blake3-aes-128-gcm=16B、aes-256-gcm /
//     chacha20-poly1305=32B），复用正本 SS2022KeyLen；legacy AEAD 与
//     未知 cipher 名不查。
//   - 解码容四种 base64 形态（Std 带填充 / RawStd / URL 带填充 /
//     RawURL），任一成功且字节数恰为 N 即合法——防误拒合法机场密钥
//     是本项最大风险，下方四形态全入库为硬断言。
//   - SIP023 冒号多段按段独立校验接受导入；sing-box 渲染对不合规
//     存量保守略过，多段合法节点 R4-SIP023-R 起逐段归一后下发
//     （见 r4_b2_sip023_test.go），Clash / Surge 等格式原样输出。

// r3b2RawKey 生成含 62/63 六比特组的原始密钥，使 Std 字母表（+/）
// 与 URL 字母表（-_）的编码串必然不同，四形态互不冒充。
func r3b2RawKey(n int) []byte {
	pat := []byte{0xfb, 0xff, 0xbf}
	out := make([]byte, n)
	for i := range out {
		out[i] = pat[i%3]
	}
	return out
}

var r3b2Encodings = []struct {
	name string
	enc  *base64.Encoding
}{
	{"Std", base64.StdEncoding},
	{"RawStd", base64.RawStdEncoding},
	{"URL", base64.URLEncoding},
	{"RawURL", base64.RawURLEncoding},
}

const (
	r3b2C128 = "2022-blake3-aes-128-gcm"
	r3b2C256 = "2022-blake3-aes-256-gcm"
	r3b2CCha = "2022-blake3-chacha20-poly1305"
)

type r3b2Spec struct {
	name, host string
	port       int
	cipher     string
	password   string
}

func r3b2SSLink(s r3b2Spec) string {
	return fmt.Sprintf("ss://%s:%s@%s:%d#%s", s.cipher, url.QueryEscape(s.password),
		s.host, s.port, url.PathEscape(s.name))
}

func r3b2SSLinkB64Userinfo(s r3b2Spec) string {
	ui := base64.RawURLEncoding.EncodeToString([]byte(s.cipher + ":" + s.password))
	return fmt.Sprintf("ss://%s@%s:%d#%s", ui, s.host, s.port, url.PathEscape(s.name))
}

func r3b2ClashDoc(specs []r3b2Spec) string {
	var b strings.Builder
	b.WriteString("proxies:\n")
	for _, s := range specs {
		fmt.Fprintf(&b, "  - {name: %q, type: ss, server: %s, port: %d, cipher: %s, password: %q, udp: true}\n",
			s.name, s.host, s.port, s.cipher, s.password)
	}
	return b.String()
}

func r3b2SingboxDoc(specs []r3b2Spec) string {
	var b strings.Builder
	b.WriteString(`{"outbounds":[`)
	for i, s := range specs {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"type":"shadowsocks","tag":%q,"server":%q,"server_port":%d,"method":%q,"password":%q}`,
			s.name, s.host, s.port, s.cipher, s.password)
	}
	b.WriteString(`]}`)
	return b.String()
}

func r3b2SurgeDoc(specs []r3b2Spec) string {
	var b strings.Builder
	b.WriteString("[Proxy]\n")
	for _, s := range specs {
		fmt.Fprintf(&b, "%s = ss, %s, %d, encrypt-method=%s, password=%s\n",
			s.name, s.host, s.port, s.cipher, s.password)
	}
	return b.String()
}

func r3b2Formats() []struct {
	name string
	doc  func([]r3b2Spec) string
} {
	return []struct {
		name string
		doc  func([]r3b2Spec) string
	}{
		{"ss-link", func(specs []r3b2Spec) string {
			lines := make([]string, len(specs))
			for i, s := range specs {
				lines[i] = r3b2SSLink(s)
			}
			return strings.Join(lines, "\n")
		}},
		{"clash", r3b2ClashDoc},
		{"singbox", r3b2SingboxDoc},
		{"surge", r3b2SurgeDoc},
	}
}

type r3b2Case struct {
	name     string
	cipher   string
	password string
	wantOK   bool
}

func r3b2Cases() []r3b2Case {
	raw16, raw32 := r3b2RawKey(16), r3b2RawKey(32)
	std := base64.StdEncoding.EncodeToString
	var cases []r3b2Case
	// 防误拒硬断言：三 cipher × 四编码形态全部合法。
	for _, c := range []struct {
		cipher string
		raw    []byte
	}{
		{r3b2C128, raw16},
		{r3b2C256, raw32},
		{r3b2CCha, raw32},
	} {
		for _, e := range r3b2Encodings {
			cases = append(cases, r3b2Case{
				name:     "ok-" + c.cipher + "-" + e.name,
				cipher:   c.cipher,
				password: e.enc.EncodeToString(c.raw),
				wantOK:   true,
			})
		}
	}
	cases = append(cases,
		// 明文口令（发版门夹具 a4imp-ss 的毒化形态）。
		r3b2Case{"plaintext", r3b2C256, "plainpassword", false},
		// 错字节数：8B 喂 16B cipher、16B 喂 32B cipher、32B 喂 16B cipher。
		r3b2Case{"short8-on-128", r3b2C128, std(r3b2RawKey(8)), false},
		r3b2Case{"key16-on-256", r3b2C256, std(raw16), false},
		r3b2Case{"key32-on-128", r3b2C128, std(raw32), false},
		// 非 base64 垃圾。
		r3b2Case{"garbage", r3b2C256, "not!base64!key", false},
		// 冒号多段（SIP023）：段间可异编码形态，按段独立判定。
		r3b2Case{"multi2-mixed-enc", r3b2C256,
			std(raw32) + ":" + base64.RawURLEncoding.EncodeToString(raw32), true},
		r3b2Case{"multi3", r3b2C128,
			std(raw16) + ":" + base64.RawStdEncoding.EncodeToString(raw16) + ":" +
				base64.URLEncoding.EncodeToString(raw16), true},
		// 多段中一段坏 → 整条拒。
		r3b2Case{"multi-bad-segment", r3b2C256, std(raw32) + ":plainpassword", false},
		r3b2Case{"multi-empty-segment", r3b2C256, std(raw32) + ":", false},
		// legacy AEAD 的 key 绝不可查（无格式域）。
		r3b2Case{"legacy-aes-plaintext", "aes-256-gcm", "plainpassword", true},
		r3b2Case{"legacy-chacha-plaintext", "chacha20-ietf-poly1305", "any-password!", true},
		// R6 批 1 口径更新（BC-R6-01 / R6X-001）：未知 cipher 名的 key
		// 谓词本身仍不查（TestR3B2ValidateSS2022PasswordHelper 直测维
		// 持），但进口面自此以 ValidSSCipher 合法集收口——未知/双拒
		// method 整条拒绝，本例期望随裁定由「入库」改为「逐条报错」，
		// 报错点名 cipher（非 SS2022 密钥问题）。原「演进兼容放行」
		// 期望 supersede。
		r3b2Case{"unknown-cipher", "future-cipher-2099", "whatever", false},
	)
	return cases
}

// TestR3B2SS2022ImportMatrix：四输入格式 × 全形态表驱动。
func TestR3B2SS2022ImportMatrix(t *testing.T) {
	for _, f := range r3b2Formats() {
		for _, c := range r3b2Cases() {
			t.Run(f.name+"/"+c.name, func(t *testing.T) {
				spec := r3b2Spec{name: "N1", host: "203.0.113.10", port: 8388,
					cipher: c.cipher, password: c.password}
				nodes, errs := ParseImport(f.doc([]r3b2Spec{spec}))
				if c.wantOK {
					if len(errs) != 0 || len(nodes) != 1 {
						t.Fatalf("合法形态必须入库：nodes=%d errs=%v", len(nodes), errs)
					}
					if got := nodes[0].Proxy.str("password"); got != c.password {
						t.Fatalf("密码须原样存（不规范化重写）：got %q want %q", got, c.password)
					}
					if got := nodes[0].Proxy.str("cipher"); got != c.cipher {
						t.Fatalf("cipher: got %q want %q", got, c.cipher)
					}
					return
				}
				if len(nodes) != 0 || len(errs) != 1 {
					t.Fatalf("坏形态必须逐条报错且不入库：nodes=%d errs=%v", len(nodes), errs)
				}
				// unknown-cipher 的拒绝点是 cipher 合法集（R6X-001），
				// 报错点名 cipher；其余坏形态为 SS2022 密钥问题。
				wantMsg := "SS2022"
				if c.name == "unknown-cipher" {
					wantMsg = "cipher"
				}
				if !strings.Contains(errs[0], wantMsg) {
					t.Fatalf("报错须点名 %s 问题：%v", wantMsg, errs)
				}
			})
		}
	}
}

// TestR3B2OneBadDoesNotBlockOthers：一坏不连坐——坏 SS2022 条目逐条
// 报错，其余节点（legacy + 合法 SS2022）照常入库，四格式同口径。
func TestR3B2OneBadDoesNotBlockOthers(t *testing.T) {
	good2pw := base64.StdEncoding.EncodeToString(r3b2RawKey(16))
	specs := []r3b2Spec{
		{name: "good-legacy", host: "203.0.113.10", port: 8388, cipher: "aes-256-gcm", password: "legacy-pass"},
		{name: "bad-ss2022", host: "203.0.113.11", port: 8388, cipher: r3b2C256, password: "plainpassword"},
		{name: "good-ss2022", host: "203.0.113.12", port: 8388, cipher: r3b2C128, password: good2pw},
	}
	for _, f := range r3b2Formats() {
		t.Run(f.name, func(t *testing.T) {
			nodes, errs := ParseImport(f.doc(specs))
			if len(nodes) != 2 || len(errs) != 1 {
				t.Fatalf("一坏不连坐：nodes=%d errs=%v", len(nodes), errs)
			}
			if !strings.Contains(errs[0], "SS2022") {
				t.Fatalf("报错须点名 SS2022：%v", errs)
			}
			names := map[string]bool{}
			for _, n := range nodes {
				names[n.Name] = true
			}
			if !names["good-legacy"] || !names["good-ss2022"] || names["bad-ss2022"] {
				t.Fatalf("入库集合不对：%v", names)
			}
		})
	}
}

// TestR3B2SSLinkBase64Userinfo：SIP002 的 base64 userinfo 形态同样
// 过校验（含多段密码藏在 base64 内的形态）。
func TestR3B2SSLinkBase64Userinfo(t *testing.T) {
	raw32 := r3b2RawKey(32)
	good := base64.StdEncoding.EncodeToString(raw32)
	spec := r3b2Spec{name: "B1", host: "203.0.113.10", port: 8388, cipher: r3b2C256, password: good}
	if n := mustImportOne(t, r3b2SSLinkB64Userinfo(spec)); n.Proxy.str("password") != good {
		t.Fatalf("base64 userinfo 合法 key: %+v", n.Proxy)
	}
	multi := r3b2Spec{name: "B2", host: "203.0.113.10", port: 8388, cipher: r3b2C256,
		password: good + ":" + base64.RawURLEncoding.EncodeToString(raw32)}
	if n := mustImportOne(t, r3b2SSLinkB64Userinfo(multi)); n.Proxy.str("password") != multi.password {
		t.Fatalf("base64 userinfo 多段: %+v", n.Proxy)
	}
	bad := r3b2Spec{name: "B3", host: "203.0.113.10", port: 8388, cipher: r3b2C256, password: "plainpassword"}
	if nodes, errs := ParseImport(r3b2SSLinkB64Userinfo(bad)); len(nodes) != 0 || len(errs) != 1 {
		t.Fatalf("base64 userinfo 坏 key 必须拒：nodes=%d errs=%v", len(nodes), errs)
	}
}

// TestR3B2ValidateSS2022PasswordHelper：共享 helper 直测——四形态
// 解码不得只试 Raw 变体（带填充标准串是面板自产与 openssl 口径，
// 误拒即本项最大风险）；legacy / 未知 cipher 恒放行。
func TestR3B2ValidateSS2022PasswordHelper(t *testing.T) {
	raw32 := r3b2RawKey(32)
	for _, e := range r3b2Encodings {
		if err := ValidateSS2022Password(r3b2C256, e.enc.EncodeToString(raw32)); err != nil {
			t.Fatalf("%s 形态合法 key 被误拒：%v", e.name, err)
		}
	}
	if err := ValidateSS2022Password("aes-256-gcm", "plainpassword"); err != nil {
		t.Fatalf("legacy 不可查：%v", err)
	}
	if err := ValidateSS2022Password("future-cipher-2099", "whatever"); err != nil {
		t.Fatalf("未知 cipher 不可查：%v", err)
	}
	if err := ValidateSS2022Password(r3b2C256, "plainpassword"); err == nil ||
		!strings.Contains(err.Error(), "SS2022") || !strings.Contains(err.Error(), "32") {
		t.Fatalf("坏 key 错误须点名 SS2022 与字节数：%v", err)
	}
	// 单段判定（渲染层用）：多段合法密码在导入 helper 下合法、在
	// 单段判定下为否，两口径不得混用。
	multi := base64.StdEncoding.EncodeToString(raw32) + ":" + base64.StdEncoding.EncodeToString(raw32)
	if err := ValidateSS2022Password(r3b2C256, multi); err != nil {
		t.Fatalf("多段合法密码导入须接受：%v", err)
	}
	if ValidSS2022SingleKey(r3b2C256, multi) {
		t.Fatal("多段密码单段判定须为否（一刀切谓词不得放宽，R4-SIP023-R 硬条件；sing-box 面改用逐段谓词）")
	}
	if !ValidSS2022SingleKey("aes-256-gcm", "plainpassword") {
		t.Fatal("非 SS2022 方法单段判定恒真")
	}
}

// TestR3B2ProxySingboxSS2022Guard：渲染层纵深——存量不合规 SS2022
// 节点在 sing-box 订阅中略过（兜底存量，不迁移不清理）；多段形态
// R4-SIP023-R 起在 sing-box 逐段归一后下发（本用例断言随裁定更新，
// 全量见 r4_b2_sip023_test.go），Clash / Surge 原样输出。
func TestR3B2ProxySingboxSS2022Guard(t *testing.T) {
	mk := func(cipher, pw string) Endpoint {
		return Endpoint{Name: "x", Host: "203.0.113.10", Port: 8388, Direct: true,
			Proxy: Proxy{"type": "ss", "server": "203.0.113.10", "port": 8388,
				"cipher": cipher, "password": pw}}
	}
	raw32 := r3b2RawKey(32)
	goodStd := base64.StdEncoding.EncodeToString(raw32)
	goodRawURL := base64.RawURLEncoding.EncodeToString(raw32)
	multi := goodStd + ":" + goodRawURL

	for _, tc := range []struct {
		name   string
		cipher string
		pw     string
		wantOK bool
	}{
		{"stock-plaintext", r3b2C256, "plainpassword", false},
		{"stock-short-key", r3b2C128, base64.StdEncoding.EncodeToString(r3b2RawKey(8)), false},
		{"valid-std", r3b2C256, goodStd, true},
		{"valid-rawurl", r3b2C256, goodRawURL, true},
		{"multi-segment-released", r3b2C256, multi, true},
		{"legacy-plaintext-kept", "aes-256-gcm", "plainpassword", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outs, ok := proxySingbox(mk(tc.cipher, tc.pw))
			if ok != tc.wantOK {
				t.Fatalf("proxySingbox ok=%v want %v (outs=%v)", ok, tc.wantOK, outs)
			}
			// GATE-F1 订正（发版门项 3 裁定渲染侧规范化，supersede 本
			// 用例初版的「原样」断言）：合法单段 key 渲染为带填充 Std
			// 规范形态，非规范形态原样输出正是发版门 FAIL 点；legacy
			// 口令经 helper 原样返回，断言对其不变。R4-SIP023-R：
			// sing-box 面改与 Clash 面同 helper（逐段归一）——单段时
			// Segments 与单段 helper 等值、legacy 恒等，唯多段不同。
			if ok && outs[0]["password"] != NormalizedSS2022PasswordSegments(tc.cipher, tc.pw) {
				t.Fatalf("渲染密码须为规范化形态：%v", outs[0])
			}
		})
	}

	// 多段形态：Clash / Surge 均须输出（sing-box 面见上表已解除）。
	// GATE-F1 订正：Clash 面由「原样」改为逐段归一——双客户
	// 端二进制实测 mihomo 按段以带填充 Std 解码，本用例 multi 的第
	// 二段是 RawURL 形态、原样输出整表 -t 失败；逐段归一解码字节
	// 不变。Surge 维持原样（其段解码未经二进制证实）。
	e := mk(r3b2C256, multi)
	node, ok := proxyClash(e)
	if !ok {
		t.Fatal("多段 SS2022 必须在 Clash 输出")
	}
	wantMulti := base64.StdEncoding.EncodeToString(raw32) + ":" + base64.StdEncoding.EncodeToString(raw32)
	if got := Proxy(yamlToMap(node)).str("password"); got != wantMulti {
		t.Fatalf("clash password: got %q want %q (逐段归一)", got, wantMulti)
	}
	line, ok := proxySurge(e)
	if !ok || !strings.Contains(line, "password="+multi) {
		t.Fatalf("多段 SS2022 必须在 Surge 原样输出：%q ok=%v", line, ok)
	}
}
