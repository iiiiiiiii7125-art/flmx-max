package kernel

import (
	"strings"
	"testing"
)

// R8 批 2 同族定级（B8-S1 → 立案 R8X-003-S1，P2）：SIP023 非规范
// 多段产物在 Xray 链式面的处置。事实（本批双口径探针落盘
// R8-FIX-B2-s1-binary-probe.log）：进口 kept verbatim、Xray 链式面
// 原样透传，存档产物（.r7b-out/sip023/noncanon-xray.json）与面板
// 新产物在 Xray 25.6.8 同为 exit 23（decode key: illegal base64
// data at input byte 20）；R7 登记「非规范 exit 0」半句已由
// verdicts §2 裁定作废、正值 exit 23。sing-box / Clash 订阅面与
// sing-box 链式面均逐段归一（NormalizedSS2022PasswordSegments），
// Xray 链式面是唯一未归一的已验面：单段非规范形经 forEndpoint
// 单段归一覆盖、多段形漏网——同根（渲染收敛点未覆盖多段）。
//
// 修复（只限链式面非规范段处置，不动进口/订阅面既有归一行为）：
// chainTargetFromProxy 的 SS 分支改经
// NormalizedSS2022PasswordSegments 逐段归一（与 sing-box/Clash 面
// 同 helper、解码字节不变、规范形为不动点）。

const (
	r8b2SegStd   = "AAECAwQFBgcICQoLDA0ODw==" // 16B Std 填充段
	r8b2SegRaw   = "EBESExQVFhcYGRobHB0eHw"   // 16B RawURL 无填充段（非规范）
	r8b2SegStd2  = "EBESExQVFhcYGRobHB0eHw==" // 同字节 Std 填充形（规范）
	r8b2Noncanon = r8b2SegStd + ":" + r8b2SegRaw
	r8b2Canon    = r8b2SegStd + ":" + r8b2SegStd2
	r8b2Cipher   = "2022-blake3-aes-128-gcm"
)

func r8b2SIP023Endpoint(pw string) Endpoint {
	return Endpoint{Name: "multi", Host: "1.2.3.4", Port: 8388, Direct: true,
		Proxy: Proxy{"type": "ss", "server": "1.2.3.4", "port": 8388,
			"cipher": r8b2Cipher, "password": pw, "udp": true}}
}

// TestR8B2SIP023NoncanonXrayChainNormalized：非规范多段经 Xray
// 链式面逐段归一（产物密码 = 规范多段逐字形，Xray exit 0 的二进
// 制证据见批探针日志）；规范形不动点；存储值不被渲染克隆改写。
func TestR8B2SIP023NoncanonXrayChainNormalized(t *testing.T) {
	e := r8b2SIP023Endpoint(r8b2Noncanon)
	ct, err := chainTargetFromProxy(e)
	if err != nil {
		t.Fatalf("非规范多段 Xray 链式须放行（逐段归一后可下发），got %v", err)
	}
	if ct.Password != r8b2Canon {
		t.Fatalf("Xray 链式产物密码须逐段归一为规范形：got %q want %q", ct.Password, r8b2Canon)
	}
	if _, err := xrayChainOutbound(e, "via-1"); err != nil {
		t.Fatalf("非规范多段 xrayChainOutbound 须可构建出站：%v", err)
	}
	if err := CheckChainWith(TypeXray, e, RenderOptions{}); err != nil {
		t.Fatalf("非规范多段 CheckChainWith(Xray) 保存期须放行：%v", err)
	}
	// 渲染克隆归一、存储不动（进口 kept verbatim 口径不变）。
	if got := e.Proxy.str("password"); got != r8b2Noncanon {
		t.Fatalf("存储密码不得被链式渲染改写：got %q want %q", got, r8b2Noncanon)
	}
	// 规范多段为不动点（逐字不变，防过修）。
	ct2, err := chainTargetFromProxy(r8b2SIP023Endpoint(r8b2Canon))
	if err != nil || ct2.Password != r8b2Canon {
		t.Fatalf("规范多段 Xray 链式须逐字不变：ct=%+v err=%v", ct2, err)
	}
	// 单段非规范形既有归一不回归（forEndpoint 单段口径）。
	ct3, err := chainTargetFromProxy(r8b2SIP023Endpoint(r8b2SegRaw))
	if err != nil || ct3.Password != r8b2SegStd2 {
		t.Fatalf("单段非规范形 Xray 链式须维持归一：ct=%+v err=%v", ct3, err)
	}
	// 坏段仍被统一谓词拒（门不放宽）。
	if _, err := chainTargetFromProxy(r8b2SIP023Endpoint(r8b2SegStd + ":plainpassword")); err == nil {
		t.Fatal("含坏段多段 Xray 链式须维持面板侧被拒")
	}
}

// TestR8B2SIP023NoncanonOtherFacesUnchanged：修复面只限 Xray 链
// 式面——进口 kept verbatim、订阅各面与 sing-box 链式面的既有归
// 一/原样口径逐面不变（防扩散硬门）。
func TestR8B2SIP023NoncanonOtherFacesUnchanged(t *testing.T) {
	// 进口 kept verbatim：非规范多段逐字落库。
	doc := "proxies:\n  - {name: multi, type: ss, server: 1.2.3.4, port: 8388, cipher: " + r8b2Cipher + ", password: " + r8b2Noncanon + ", udp: true}\n"
	nodes, errs := ParseImport(doc)
	if len(errs) != 0 || len(nodes) != 1 || nodes[0].Proxy.str("password") != r8b2Noncanon {
		t.Fatalf("非规范多段进口须 kept verbatim：nodes=%v errs=%v", nodes, errs)
	}
	e := r8b2SIP023Endpoint(r8b2Noncanon)
	// sing-box / Clash 订阅面：逐段归一（既有口径）。
	if outs, ok := proxySingbox(e); !ok || outs[0]["password"] != r8b2Canon {
		t.Fatalf("sing-box 订阅面须维持逐段归一：ok=%v outs=%v", ok, outs)
	}
	if node, ok := proxyClash(e); !ok || Proxy(yamlToMap(node)).str("password") != r8b2Canon {
		t.Fatalf("Clash 订阅面须维持逐段归一：ok=%v node=%v", ok, node)
	}
	// sing-box 链式面：逐段归一（既有口径、随 proxySingbox 同函数）。
	obs, err := singboxChainOutbounds(e, "via-1", 0, false)
	if err != nil || len(obs) != 1 || obs[0].(obj)["password"] != r8b2Canon {
		t.Fatalf("sing-box 链式面须维持逐段归一：err=%v obs=%v", err, obs)
	}
	// Surge / URI 残余面：原样下发逐字不变（未验证残余、不在本批
	// 修复面内）。
	if line, ok := proxySurge(e); !ok || !strings.Contains(line, r8b2Noncanon) {
		t.Fatalf("Surge 残余面须维持原样下发：ok=%v line=%q", ok, line)
	}
	if uri, ok := proxyURI(e); !ok || !strings.Contains(uri, "EBESExQVFhcYGRobHB0eHw") {
		t.Fatalf("URI 残余面须维持原样下发：ok=%v uri=%q", ok, uri)
	}
}
