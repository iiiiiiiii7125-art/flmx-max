package kernel

import (
	"strings"
	"testing"
)

// R9X-005（批 4 · 链式面）：Naive 出站需要 sing-box 1.13+（官方二进
// 制二分：1.12.17 exit 1 `unknown outbound type: naive` / 1.13.14
// exit 0）。CheckChainWith 的 sing-box 分支按目标协议点名拒绝，
// RenderOptions.SingboxNaive 与门同批落地。

func r9b4NaiveInbound(t *testing.T) Inbound {
	t.Helper()
	in := Inbound{ID: 71, Protocol: ProtocolNaive, Port: 443,
		Settings: Settings{Username: "nu", Password: "naive-pass-1"},
		Security: Security{Type: SecurityTLS, CertMode: CertModeACME, SNI: "nv.example.com", ACMEEmail: "me@example.com"}}
	if err := Normalize(TypeSingbox, &in); err != nil {
		t.Fatal(err)
	}
	return in
}

func r9b4NaiveEndpoint(t *testing.T) Endpoint {
	t.Helper()
	return Endpoint{Name: "nv", Host: "203.0.113.9", Port: 443, Inbound: r9b4NaiveInbound(t), Direct: true}
}

func r9b4ImportedNaiveEndpoint(t *testing.T) Endpoint {
	t.Helper()
	nodes, errs := ParseImport(`{"outbounds":[{"type":"naive","tag":"nv","server":"5.6.7.8","server_port":443,"username":"u1","password":"p1","tls":{"enabled":true,"server_name":"nv.example.com"}}]}`)
	if len(errs) != 0 || len(nodes) != 1 {
		t.Fatalf("import naive: nodes=%v errs=%v", nodes, errs)
	}
	n := nodes[0]
	return Endpoint{Name: n.Name, Host: n.Host, Port: n.Port, Proxy: n.Proxy, Direct: true}
}

func TestR9B4CheckChainNaiveGate(t *testing.T) {
	for name, via := range map[string]Endpoint{
		"self-built": r9b4NaiveEndpoint(t),
		"imported":   r9b4ImportedNaiveEndpoint(t),
	} {
		err := CheckChainWith(TypeSingbox, via, RenderOptions{})
		if err == nil || !strings.Contains(err.Error(), "1.13") || !strings.Contains(err.Error(), "Naive") {
			t.Fatalf("%s: sing-box without 1.13 must be named-rejected, got %v", name, err)
		}
		if err := CheckChainWith(TypeSingbox, via, RenderOptions{SingboxNaive: true}); err != nil {
			t.Fatalf("%s: sing-box 1.13+ must pass, got %v", name, err)
		}
		// Xray 面不受本门影响（naive 本就不可达、走既有不支持报错）。
		if err := CheckChainWith(TypeXray, via, RenderOptions{}); err == nil {
			t.Fatalf("%s: xray must keep rejecting naive", name)
		}
	}
}

func TestR9B4RenderSingboxNaiveChainGate(t *testing.T) {
	via := r9b4NaiveEndpoint(t)
	in := Inbound{ID: 72, Protocol: ProtocolVLESS, Port: 8443, Settings: Settings{UUID: testUUID}, Security: Security{Type: SecurityNone}}
	if err := Normalize(TypeSingbox, &in); err != nil {
		t.Fatal(err)
	}
	in.Via = &via
	if _, err := Render(TypeSingbox, []Inbound{in}); err == nil {
		t.Fatal("rendering a naive chain without the 1.13 flag must fail")
	}
	out, err := RenderWith(TypeSingbox, []Inbound{in}, RenderOptions{SingboxNaive: true})
	if err != nil {
		t.Fatal(err)
	}
	cfg := out.Files[0].Content
	if ob := outboundByTag(renderJSONString(t, cfg), "via-72"); ob == nil || ob["type"] != "naive" {
		t.Fatalf("chain outbound must be naive: %v\n%s", ob, cfg)
	}
	// 放行面逐字稳定：同标志二次渲染逐字一致。
	out2, err := RenderWith(TypeSingbox, []Inbound{in}, RenderOptions{SingboxNaive: true})
	if err != nil || out2.Files[0].Content != cfg {
		t.Fatalf("1.13+ render must be deterministic/unchanged: %v", err)
	}
}

// 对照格：自建 Naive 入站（服务端面）在 1.11 已存在，不受出站门
// 影响——默认 RenderOptions 下照常渲染入站。
func TestR9B4NaiveInboundUnaffected(t *testing.T) {
	in := r9b4NaiveInbound(t)
	out, err := Render(TypeSingbox, []Inbound{in})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Files[0].Content, `"type": "naive"`) {
		t.Fatalf("naive inbound must still render:\n%s", out.Files[0].Content)
	}
}

// ---- 连带格（订阅 sing-box 客户端 UA 面，断言与链式面分开） ----

// 处置选择「UA 略过 naive」：SingboxClientHasNaive 按 UA 判定
// 1.13+；低于 1.13 或 UA 不可解析时 sing-box 订阅略过 Naive 节点
// （其余节点照常下发），与 Snell 的 UA 门同型。
func TestR9B4SingboxClientHasNaive(t *testing.T) {
	for ua, want := range map[string]bool{
		"SFA (sing-box 1.13.14; language zh_CN)": true,
		"SFI/1.14.3 (sing-box 1.14.3)":           true,
		"sing-box/2.0.1":                         true,
		"SFA (sing-box 1.12.17; language en_US)": false,
		"SFM (sing-box 1.11.15)":                 false,
		"clash-verge/v2.0":                       false,
		"":                                       false,
	} {
		if got := SingboxClientHasNaive(ua); got != want {
			t.Errorf("%q: got %v, want %v", ua, got, want)
		}
	}
}

func TestR9B4SubscriptionSingboxNaiveUAGate(t *testing.T) {
	naive := r9b4NaiveEndpoint(t)
	vlessIn := Inbound{ID: 73, Protocol: ProtocolVLESS, Port: 443, Settings: Settings{UUID: testUUID},
		Security: Security{Type: SecurityReality, SNI: "www.microsoft.com", RealityPublicKey: "pk", RealityShortIDs: []string{"ab"}}}
	if err := Normalize(TypeSingbox, &vlessIn); err != nil {
		t.Fatal(err)
	}
	eps := []Endpoint{naive, {Name: "vl", Host: "203.0.113.10", Port: 443, Inbound: vlessIn, Direct: true}}

	old, _, err := Subscription(FormatSingbox, eps, SubOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(old, `"naive"`) || !strings.Contains(old, `"vless"`) {
		t.Fatalf("clients before sing-box 1.13 must skip naive only:\n%s", old)
	}
	if got := SubscriptionNodeCount(FormatSingbox, eps, SubOptions{}); got != 1 {
		t.Fatalf("count without naive-capable client = %d, want 1", got)
	}
	neu, _, err := Subscription(FormatSingbox, eps, SubOptions{SingboxNaive: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(neu, `"naive"`) || !strings.Contains(neu, `"vless"`) {
		t.Fatalf("sing-box 1.13+ clients must get naive:\n%s", neu)
	}
	if got := SubscriptionNodeCount(FormatSingbox, eps, SubOptions{SingboxNaive: true}); got != 2 {
		t.Fatalf("count with naive-capable client = %d, want 2", got)
	}
	// 导入 naive 节点同门。
	imp := []Endpoint{r9b4ImportedNaiveEndpoint(t)}
	oldImp, _, err := Subscription(FormatSingbox, imp, SubOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(oldImp, `"naive"`) {
		t.Fatalf("imported naive must also be skipped for old clients:\n%s", oldImp)
	}
}
