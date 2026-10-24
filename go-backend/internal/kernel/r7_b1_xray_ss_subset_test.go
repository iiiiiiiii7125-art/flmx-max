package kernel

import (
	"strings"
	"testing"
)

// R7 批 1（R7X-001）：Xray 链式面 SS 子集门。
// 子集口径 = 本批施工前置硬门重测（Xray 25.6.8 fbae89d 对双客户端共同集
// 18 值逐值 `run -test`，原始日志 audit-out-132/r7/R7-FIX-B1-xray-ss-reprobe.log）：
// 接受 8 / 拒绝 10，与审计轮 R7-D/B/L 三域探针的 8/10 分界逐值一致、无漂移。
// 关闭断言三组分开登记（N-35，禁互相顶替）：
//   组 ① 链式面格（本文件 TestR7B1XrayChainSSSubsetGate）；
//   组 ② 订阅面防过修格（TestR7B1SubscriptionFacesUnchanged，基准 =
//        R7-B-report §3.1 逐面 EMIT/SKIP 表 / R7-B-faces-probe.log faces.tsv）；
//   组 ③ sing-box 链式面格（TestR7B1SingboxChainSSUnchanged）。
// 四无二进制面（Surge/Loon/QX/URI）的客户端后果恒标未验证：组 ② 只断言
// 面板渲染 EMIT/SKIP 集与 1.4.3 逐字不变，不宣称客户端已验关闭。

var r7b1XrayAccept = []string{
	"2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm", "2022-blake3-chacha20-poly1305",
	"aes-128-gcm", "aes-256-gcm", "chacha20-ietf-poly1305", "xchacha20-ietf-poly1305", "none",
}

var r7b1XrayReject = []string{
	"aes-192-gcm",
	"aes-128-cfb", "aes-192-cfb", "aes-256-cfb",
	"aes-128-ctr", "aes-192-ctr", "aes-256-ctr",
	"chacha20-ietf", "rc4-md5", "xchacha20",
}

func r7b1ChainSSEndpoint(cipher string) Endpoint {
	return Endpoint{Name: "chain", Host: "1.2.3.4", Port: 8388,
		Proxy: Proxy{"type": "ss", "server": "1.2.3.4", "port": 8388,
			"cipher": cipher, "password": r6b1SSPassword(cipher), "udp": true}}
}

// ---- 组 ①：Xray 链式面子集门（保存期 + 下发期同经 chainTargetFromProxy 一点） ----

func TestR7B1XrayChainSSSubsetGate(t *testing.T) {
	// 子集外 10 值：保存期（CheckChainWith）与下发期（chainTargetFromProxy /
	// xrayChainOutbound）均被拒，且错误明确点名 cipher。
	for _, m := range r7b1XrayReject {
		e := r7b1ChainSSEndpoint(m)
		_, err := chainTargetFromProxy(e)
		if err == nil {
			t.Fatalf("cipher=%s：Xray 链式面须在面板侧被拒（Xray 本体 exit 23）", m)
		}
		if !strings.Contains(err.Error(), m) {
			t.Fatalf("cipher=%s：拒绝错误须点名 cipher，got %q", m, err.Error())
		}
		if _, err := xrayChainOutbound(e, "via-1", 0); err == nil {
			t.Fatalf("cipher=%s：xrayChainOutbound 须报错、不产出可下发配置", m)
		}
		if err := CheckChainWith(TypeXray, e, RenderOptions{}); err == nil {
			t.Fatalf("cipher=%s：CheckChainWith(Xray) 须报错（保存期同点生效）", m)
		} else if !strings.Contains(err.Error(), m) {
			t.Fatalf("cipher=%s：保存期拒绝错误须点名 cipher，got %q", m, err.Error())
		}
	}
	// 子集 8 值：三点全放行，产物 method 逐字为该 cipher（产物经 Xray
	// run -test exit 0 的二进制证据见批探针日志 R7-FIX-B1-panel-product-xray.log）。
	for _, m := range r7b1XrayAccept {
		e := r7b1ChainSSEndpoint(m)
		ct, err := chainTargetFromProxy(e)
		if err != nil {
			t.Fatalf("cipher=%s：Xray 子集内须放行，got %v", m, err)
		}
		if ct.Cipher != m {
			t.Fatalf("cipher=%s：链式目标 cipher 须逐字保留，got %q", m, ct.Cipher)
		}
		if _, err := xrayChainOutbound(e, "via-1", 0); err != nil {
			t.Fatalf("cipher=%s：xrayChainOutbound 须可构建出站，got %v", m, err)
		}
		if err := CheckChainWith(TypeXray, e, RenderOptions{}); err != nil {
			t.Fatalf("cipher=%s：CheckChainWith(Xray) 保存期须放行，got %v", m, err)
		}
	}
	// 大小写变体：forEndpoint 小写愈合后同口径——子集外变体被拒且点名
	// 愈合后的 cipher 名，子集内变体放行。
	e := r7b1ChainSSEndpoint("AES-128-CFB")
	if _, err := chainTargetFromProxy(e); err == nil || !strings.Contains(err.Error(), "aes-128-cfb") {
		t.Fatalf("AES-128-CFB 变体须被拒且点名 aes-128-cfb，got %v", err)
	}
	e = r7b1ChainSSEndpoint("AES-128-GCM")
	if _, err := chainTargetFromProxy(e); err != nil {
		t.Fatalf("AES-128-GCM 变体须放行（小写愈合），got %v", err)
	}
	// chacha20 既有处置不变：Xray 链式面仍被拒（经统一谓词单面集格，
	// 与修复前同一点位、同形态）。
	e = r7b1ChainSSEndpoint("chacha20")
	if _, err := chainTargetFromProxy(e); err == nil {
		t.Fatal("chacha20 在 Xray 链式面的既有拒绝处置不得改变")
	}
	if err := CheckChainWith(TypeXray, e, RenderOptions{}); err == nil {
		t.Fatal("chacha20 CheckChainWith(Xray) 须维持被拒")
	}
	// 谓词逐值（新面档）：子集谓词与逐值表一致、大小写不敏感。
	for _, m := range r7b1XrayAccept {
		if !ValidSSCipherForXrayChain(m) || !ValidSSCipherForXrayChain(strings.ToUpper(m)) {
			t.Fatalf("cipher=%s 须在 Xray 链式子集内", m)
		}
	}
	for _, m := range append(append([]string{}, r7b1XrayReject...), "chacha20", "garbage-cipher", "") {
		if ValidSSCipherForXrayChain(m) {
			t.Fatalf("cipher=%q 不得在 Xray 链式子集内", m)
		}
	}
}

// ---- 组 ②：订阅面防过修——18 值六面 EMIT/SKIP 集与 1.4.3 逐字不变 ----
// 基准 = R7-B-report §3.1 表（faces.tsv）：sing-box / Clash / Surge / URI
// 全 EMIT；Loon 自有白名单 SKIP 6 值、QX SKIP 5 值（能力降级、非本批对象）。

var r7b1LoonSkip = map[string]bool{
	"aes-192-cfb": true, "aes-128-ctr": true, "aes-192-ctr": true,
	"aes-256-ctr": true, "none": true, "xchacha20": true,
}

var r7b1QXSkip = map[string]bool{
	"aes-192-cfb": true, "aes-128-ctr": true, "aes-192-ctr": true,
	"aes-256-ctr": true, "xchacha20": true,
}

func TestR7B1SubscriptionFacesUnchanged(t *testing.T) {
	for _, m := range r6b1CommonCiphers {
		e := r6b1SSEndpoint("stock", m, r6b1SSPassword(m))
		if _, ok := proxySingbox(e); !ok {
			t.Fatalf("cipher=%s sing-box 订阅面须 EMIT（与 1.4.3 不变）", m)
		}
		if _, ok := proxyClash(e); !ok {
			t.Fatalf("cipher=%s Clash 订阅面须 EMIT（与 1.4.3 不变）", m)
		}
		if _, ok := proxySurge(e); !ok {
			t.Fatalf("cipher=%s Surge 订阅面须 EMIT（与 1.4.3 不变，未验证面仅代码断言）", m)
		}
		if _, ok := proxyURI(e); !ok {
			t.Fatalf("cipher=%s URI 订阅面须 EMIT（与 1.4.3 不变，未验证面仅代码断言）", m)
		}
		p := Proxy{"type": "ss", "name": "stock", "server": "1.2.3.4", "port": 8388,
			"cipher": m, "password": r6b1SSPassword(m), "udp": true}
		if _, ok := loonLine(p); ok == r7b1LoonSkip[m] {
			t.Fatalf("cipher=%s Loon 面 EMIT/SKIP 须与 1.4.3 逐字不变（skip=%v），got emit=%v", m, r7b1LoonSkip[m], ok)
		}
		if _, ok := qxLine(p); ok == r7b1QXSkip[m] {
			t.Fatalf("cipher=%s QX 面 EMIT/SKIP 须与 1.4.3 逐字不变（skip=%v），got emit=%v", m, r7b1QXSkip[m], ok)
		}
		// 计数同口径：sing-box / Clash 两面 18 值全计。
		eps := []Endpoint{e}
		if n := SubscriptionNodeCount(FormatSingbox, eps, SubOptions{}); n != 1 {
			t.Fatalf("cipher=%s sing-box 计数须为 1，got %d", m, n)
		}
		if n := SubscriptionNodeCount(FormatClash, eps, SubOptions{}); n != 1 {
			t.Fatalf("cipher=%s Clash 计数须为 1，got %d", m, n)
		}
	}
}

// ---- 组 ③：sing-box 链式面——全 18 值处置不变（sing-box 接受全集） ----

func TestR7B1SingboxChainSSUnchanged(t *testing.T) {
	for _, m := range r6b1CommonCiphers {
		e := r7b1ChainSSEndpoint(m)
		obs, err := singboxChainOutbounds(e, "via-1", 0, false)
		if err != nil || len(obs) == 0 {
			t.Fatalf("cipher=%s sing-box 链式面须放行（与 1.4.3 不变），got obs=%d err=%v", m, len(obs), err)
		}
		if err := CheckChainWith(TypeSingbox, e, RenderOptions{}); err != nil {
			t.Fatalf("cipher=%s CheckChainWith(sing-box) 保存期须放行，got %v", m, err)
		}
	}
	// chacha20 在 sing-box 链式面既有处置不变：SKIP→报错。
	e := r7b1ChainSSEndpoint("chacha20")
	if _, err := singboxChainOutbounds(e, "via-1", 0, false); err == nil {
		t.Fatal("chacha20 sing-box 链式面须维持报错（SKIP→报错既有口径）")
	}
}
