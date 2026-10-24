package kernel

import (
	"strings"
	"testing"
)

// R12 批 X（R12X-002，P3）：自建渲染侧归一。
// 备份逐字恢复变体（AUTO / Packet-Up，R5X-024 受保护行为：落库逐字，
// 本批不动）绕过 Normalize，直达三处原文消费点；渲染侧须小写愈合，
// 愈合后仍域外回退默认 auto（安全方向，与 chain.go 空值回退同向）。
// 探针对象为备份恢复变体对象本身——本测试全程不调 Normalize，禁以
// 愈合后对象顶替（N-35）。落库值本身不得被渲染改动。

func r12bxFaces(t *testing.T, in Inbound) (xrayMode, chainMode, uri, clash string) {
	t.Helper()
	e := Endpoint{Name: "sb", Host: "203.0.113.10", Port: 443, Direct: true, Inbound: in}
	node, ok := ClashProxy(e)
	if !ok {
		t.Fatal("ClashProxy 须产出自建 XHTTP 节点")
	}
	clash = marshalYAML(node)
	uri, ok = ShareURI(e)
	if !ok {
		t.Fatal("ShareURI 须产出自建 XHTTP 链接")
	}
	rendered, err := Render(TypeXray, []Inbound{in})
	if err != nil {
		t.Fatalf("Render(Xray) 须成功：%v", err)
	}
	xrayMode = xrayRenderedXHTTPMode(t, rendered.Files[0].Content)
	ob, err := xrayChainOutbound(e, "chain-out", 0)
	if err != nil {
		t.Fatalf("链式出站须可达：%v", err)
	}
	stream, _ := ob["streamSettings"].(obj)
	xs, _ := stream["xhttpSettings"].(obj)
	chainMode, _ = xs["mode"].(string)
	return xrayMode, chainMode, uri, clash
}

func TestR12BXSelfBuiltRenderHealsBackupVariants(t *testing.T) {
	// 变体格：四面产物均须为小写规范值，且渲染不得改动原对象落库值。
	for inVal, want := range map[string]string{
		"AUTO": "auto", "Packet-Up": "packet-up",
		"STREAM-ONE": "stream-one", "Stream-Up": "stream-up",
	} {
		in := r11b1XHTTPInbound(inVal)
		xrayMode, chainMode, uri, clash := r12bxFaces(t, in)
		if in.Transport.Mode != inVal {
			t.Fatalf("渲染不得改动原对象 Mode：输入 %q 渲染后 %q", inVal, in.Transport.Mode)
		}
		if xrayMode != want {
			t.Fatalf("变体 %q 的 Xray 入站 mode 须为 %q，got %q", inVal, want, xrayMode)
		}
		if chainMode != want {
			t.Fatalf("变体 %q 的 Xray 链式 mode 须为 %q，got %q", inVal, want, chainMode)
		}
		if !strings.Contains(uri, "mode="+want) {
			t.Fatalf("变体 %q 的 ShareURI 须含 mode=%q，got %q", inVal, want, uri)
		}
		if !strings.Contains(clash, `mode: "`+want+`"`) {
			t.Fatalf("变体 %q 的 Clash 产物须含 mode %q，got:\n%s", inVal, want, clash)
		}
		if strings.Contains(clash, inVal) || strings.Contains(uri, inVal) {
			t.Fatalf("变体 %q 的产物不得再含原值：\nclash:\n%s\nuri:%s", inVal, clash, uri)
		}
	}
	// 垃圾格：存量域外值（如有）四面回退 auto，不毒化、不拒单。
	for _, bad := range []string{"bogus", "packet_up", "auto-up"} {
		in := r11b1XHTTPInbound(bad)
		xrayMode, chainMode, uri, clash := r12bxFaces(t, in)
		if xrayMode != "auto" || chainMode != "auto" {
			t.Fatalf("垃圾 mode %q 须回退 auto：xray=%q chain=%q", bad, xrayMode, chainMode)
		}
		if !strings.Contains(uri, "mode=auto") {
			t.Fatalf("垃圾 mode %q 的 ShareURI 须含 mode=auto，got %q", bad, uri)
		}
		if !strings.Contains(clash, `mode: "auto"`) {
			t.Fatalf("垃圾 mode %q 的 Clash 产物须含 mode auto，got:\n%s", bad, clash)
		}
	}
	// 空值格：Xray/链式/URI 沿既有默认补 auto；Clash 沿既有口径省略该键。
	in := r11b1XHTTPInbound("")
	xrayMode, chainMode, uri, clash := r12bxFaces(t, in)
	if xrayMode != "auto" || chainMode != "auto" {
		t.Fatalf("空 mode 须补默认 auto：xray=%q chain=%q", xrayMode, chainMode)
	}
	if !strings.Contains(uri, "mode=auto") {
		t.Fatalf("空 mode 的 ShareURI 须含 mode=auto，got %q", uri)
	}
	if strings.Contains(clash, "mode:") {
		t.Fatalf("空 mode 的 Clash 产物须沿既有口径省略 mode 键，got:\n%s", clash)
	}
}
