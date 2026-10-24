package kernel

import (
	"encoding/base64"
	"strings"
	"testing"
)

// R8 批 2（R8X-003）：SS 单面集补收 5 值——aes-128-gcm-siv、
// aes-256-gcm-siv、chacha8-ietf-poly1305、aes-128-ccm、aes-256-ccm
// 并入 ssCiphersMihomoOnly，与 chacha20 同型分面处置。二进制签名
// 逐值与 chacha20 逐字同型（sing-box exit 1 / mihomo exit 0 /
// Xray exit 23，R8-D-sscand-probe.log:1-2,8-10 + 本批复跑日志
// R8-FIX-B2-binary-probe.log）。关闭断言（verdicts §1 写死）：5 值
// 逐值——进口不拒 / Clash 面保留 / sing-box 面略过 / 双链式面按
// face 条款（Xray 链式拒、sing-box 链式 SKIP→报错）；逐值用例镜像
// TestR6B1SSChacha20SingleFace、五份分别登记，不得以两值证据顶替
// 其余三值（N-35）。
//
// 四无二进制面（Surge/Loon/QX/URI）的客户端后果恒标未验证：本文件
// 对其只断言面板渲染 EMIT/SKIP 与 R8 基线逐格不变（防过修对照）——
// Surge/URI 经 ValidSSCipher 同谓词设门故 EMIT；Loon/QX 为自有白
// 名单自守、本 5 值不在名单内故 SKIP（名单未动，非本批对象）。

// r8b2CheckMihomoOnlySingleFace 是 5 值的共用断言体；每值由独立的
// 顶层 Test 函数调用、分别登记（测试名即登记行）。
func r8b2CheckMihomoOnlySingleFace(t *testing.T, cipher string) {
	t.Helper()
	// 谓词：合法集内 + 单面集成員，大小写不敏感。
	if !ValidSSCipher(cipher) || !ValidSSCipher(strings.ToUpper(cipher)) {
		t.Fatalf("cipher=%s 须在谓词合法集内（进口不拒、分面处置）", cipher)
	}
	if !ssCipherMihomoOnly(cipher) || !ssCipherMihomoOnly(strings.ToUpper(cipher)) {
		t.Fatalf("cipher=%s 须在单面集 ssCiphersMihomoOnly 内", cipher)
	}
	if ValidSSCipherForXrayChain(cipher) {
		t.Fatalf("cipher=%s 不得进入 Xray 链式子集", cipher)
	}
	// 进口不拒：Clash 文档与 URI 两路逐条入库、健康不连坐口径同
	// R6 合法集用例；大小写变体小写愈合落库。
	doc := "proxies:\n  - {name: ok, type: ss, server: 1.2.3.4, port: 8388, cipher: " + cipher + ", password: pw, udp: true}\n"
	if nodes, errs := ParseImport(doc); len(errs) != 0 || len(nodes) != 1 {
		t.Fatalf("cipher=%s Clash 进口不得误拒：nodes=%v errs=%v", cipher, nodes, errs)
	}
	uri := "ss://" + base64.RawURLEncoding.EncodeToString([]byte(cipher+":pw")) + "@1.2.3.4:8388#ok"
	if nodes, errs := ParseImport(uri); len(errs) != 0 || len(nodes) != 1 {
		t.Fatalf("cipher=%s URI 进口不得误拒：nodes=%v errs=%v", cipher, nodes, errs)
	}
	upperDoc := "proxies:\n  - {name: ok, type: ss, server: 1.2.3.4, port: 8388, cipher: " + strings.ToUpper(cipher) + ", password: pw, udp: true}\n"
	nodes, errs := ParseImport(upperDoc)
	if len(errs) != 0 || len(nodes) != 1 || nodes[0].Proxy.str("cipher") != cipher {
		t.Fatalf("cipher=%s 大小写变体须入库并小写愈合：nodes=%v errs=%v", cipher, nodes, errs)
	}
	// 存量分面：sing-box 略过、Clash 保留且 cipher 逐字不变。
	e := r6b1SSEndpoint("stock", cipher, "pw")
	if _, ok := proxySingbox(e); ok {
		t.Fatalf("cipher=%s 存量 sing-box 须整节点略过（sing-box FATAL unknown method）", cipher)
	}
	node, ok := proxyClash(e)
	if !ok {
		t.Fatalf("cipher=%s 存量 Clash 须保留（mihomo 接受）", cipher)
	}
	if got := Proxy(yamlToMap(node)).str("cipher"); got != cipher {
		t.Fatalf("cipher=%s Clash 产物 cipher 须逐字保留，got %q", cipher, got)
	}
	// 计数分面同口径（与健康节点并存）：sing-box 只计健康、Clash
	// 与 Shadowrocket（Clash 系同门）全计。
	eps := []Endpoint{e, r6b1HealthySS}
	if n := SubscriptionNodeCount(FormatSingbox, eps, SubOptions{}); n != 1 {
		t.Fatalf("cipher=%s sing-box 计数须为 1（仅健康节点），got %d", cipher, n)
	}
	if n := SubscriptionNodeCount(FormatClash, eps, SubOptions{}); n != 2 {
		t.Fatalf("cipher=%s Clash 计数须为 2，got %d", cipher, n)
	}
	if n := SubscriptionNodeCount(FormatShadowrocket, eps, SubOptions{}); n != 2 {
		t.Fatalf("cipher=%s Shadowrocket 计数须为 2，got %d", cipher, n)
	}
	// 双链式面按 face 条款：Xray 链式面板侧拒（保存期 + 下发期同
	// 点）、sing-box 链式 SKIP→报错。
	ce := Endpoint{Name: "chain", Host: "1.2.3.4", Port: 8388,
		Proxy: Proxy{"type": "ss", "server": "1.2.3.4", "port": 8388, "cipher": cipher, "password": "pw", "udp": true}}
	if _, err := chainTargetFromProxy(ce); err == nil {
		t.Fatalf("cipher=%s Xray 链式面须在面板侧被拒（Xray 本体 exit 23）", cipher)
	}
	if _, err := xrayChainOutbound(ce, "via-1", 0); err == nil {
		t.Fatalf("cipher=%s xrayChainOutbound 须报错、不产出可下发配置", cipher)
	}
	if err := CheckChainWith(TypeXray, ce, RenderOptions{}); err == nil {
		t.Fatalf("cipher=%s CheckChainWith(Xray) 须报错（保存期同点生效）", cipher)
	}
	if _, err := singboxChainOutbounds(ce, "via-1", 0, false); err == nil {
		t.Fatalf("cipher=%s sing-box 链式面须报错（SKIP→报错既有口径）", cipher)
	}
	if err := CheckChainWith(TypeSingbox, ce, RenderOptions{}); err == nil {
		t.Fatalf("cipher=%s CheckChainWith(sing-box) 须报错（保存期同点生效）", cipher)
	}
	// 无二进制四面防过修对照（仅代码级断言、客户端后果未验证）：
	// Surge/URI 同谓词设门 EMIT；Loon/QX 自有白名单不含本值 SKIP。
	if _, ok := proxySurge(e); !ok {
		t.Fatalf("cipher=%s Surge 面板渲染须 EMIT（同谓词设门、基线不变）", cipher)
	}
	if _, ok := proxyURI(e); !ok {
		t.Fatalf("cipher=%s URI 面板渲染须 EMIT（同谓词设门、基线不变）", cipher)
	}
	p := Proxy{"type": "ss", "name": "stock", "server": "1.2.3.4", "port": 8388, "cipher": cipher, "password": "pw", "udp": true}
	if _, ok := loonLine(p); ok {
		t.Fatalf("cipher=%s Loon 须维持 SKIP（自有白名单未含本值、白名单未动）", cipher)
	}
	if _, ok := qxLine(p); ok {
		t.Fatalf("cipher=%s QX 须维持 SKIP（自有白名单未含本值、白名单未动）", cipher)
	}
}

func TestR8B2SSAES128GCMSivSingleFace(t *testing.T) {
	r8b2CheckMihomoOnlySingleFace(t, "aes-128-gcm-siv")
}

func TestR8B2SSAES256GCMSivSingleFace(t *testing.T) {
	r8b2CheckMihomoOnlySingleFace(t, "aes-256-gcm-siv")
}

func TestR8B2SSChacha8IETFPoly1305SingleFace(t *testing.T) {
	r8b2CheckMihomoOnlySingleFace(t, "chacha8-ietf-poly1305")
}

func TestR8B2SSAES128CCMSingleFace(t *testing.T) {
	r8b2CheckMihomoOnlySingleFace(t, "aes-128-ccm")
}

func TestR8B2SSAES256CCMSingleFace(t *testing.T) {
	r8b2CheckMihomoOnlySingleFace(t, "aes-256-ccm")
}
