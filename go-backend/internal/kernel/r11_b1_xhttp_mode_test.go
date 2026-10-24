package kernel

import (
	"encoding/json"
	"strings"
	"testing"
)

// R11 批 1（R11X-001，P1）：自建入站 XHTTP Transport.Mode 收口。
// 施工口径（R11-FIX-PLAN §1 批 1）：normalizeTransport 的 XHTTP 支与
// 指纹格（normalizeSecurity / R10X-001）同形处置——Mode 先小写愈合、
// 再过 ValidXHTTPMode 合法集 {auto, packet-up, stream-up, stream-one}，
// 集外拒；空值补 auto 的既有顺序与默认不变。保存面与备份恢复面同经
// Normalize，一处收口。
// 关闭断言逐条独立登记（N-35）：
//   ① Normalize 对 AUTO/Packet-Up 愈合落库为规范小写、对 bogus 报错拒
//      —— TestR11B1XHTTPModeSelfBuiltHealAndReject
//   ② 合法四值（含大小写变体经愈合后）产物逐字不变
//      —— TestR11B1XHTTPModeLegalValuesProductsVerbatim
//   ③ 备份恢复路径同格复验拒/愈合同口径
//      —— repo 包 TestR11B1BackupRestoreXHTTPModeGate（backup_r11_b1_test.go）
//   ④ 自建 Clash 面 / ShareURI / Xray 内核配置三面产物不再含坏 mode
//      （探针格复跑）—— TestR11B1XHTTPModeProductsHaveNoBadMode

const r11b1UUID = "11111111-2222-3333-4444-555555555555"

func r11b1XHTTPInbound(mode string) Inbound {
	return Inbound{
		Protocol: ProtocolVLESS,
		Port:     443,
		Settings: Settings{UUID: r11b1UUID},
		Transport: Transport{
			Type: TransportXHTTP,
			Path: "/xh",
			Mode: mode,
		},
		Security: Security{Type: SecurityNone},
	}
}

func r11b1Products(t *testing.T, in Inbound) (clash, uri, xray string) {
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
	if len(rendered.Files) != 1 {
		t.Fatalf("Render(Xray) 须产出 1 个配置文件，got %d", len(rendered.Files))
	}
	return clash, uri, rendered.Files[0].Content
}

// xrayRenderedXHTTPMode 从 Xray 内核配置 JSON 中结构化取出
// inbounds[0].streamSettings.xhttpSettings.mode。
func xrayRenderedXHTTPMode(t *testing.T, content string) string {
	t.Helper()
	var cfg struct {
		Inbounds []struct {
			StreamSettings struct {
				XHTTPSettings struct {
					Mode string `json:"mode"`
				} `json:"xhttpSettings"`
			} `json:"streamSettings"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal([]byte(content), &cfg); err != nil {
		t.Fatalf("Xray 配置须为合法 JSON：%v", err)
	}
	if len(cfg.Inbounds) != 1 {
		t.Fatalf("Xray 配置须含 1 个入站，got %d", len(cfg.Inbounds))
	}
	return cfg.Inbounds[0].StreamSettings.XHTTPSettings.Mode
}

// ---------- 断言 ①：愈合落库 + 集外拒 ----------

func TestR11B1XHTTPModeSelfBuiltHealAndReject(t *testing.T) {
	// 大小写变体（含首尾空白，TrimSpace 为既有顺序）愈合落库为规范小写。
	for inVal, want := range map[string]string{
		"AUTO":        "auto",
		"Packet-Up":   "packet-up",
		"STREAM-ONE":  "stream-one",
		"Stream-Up":   "stream-up",
		" Packet-Up ": "packet-up",
	} {
		in := r11b1XHTTPInbound(inVal)
		if err := Normalize(TypeXray, &in); err != nil {
			t.Fatalf("Normalize(mode=%q) 须愈合而非报错：%v", inVal, err)
		}
		if in.Transport.Mode != want {
			t.Fatalf("Normalize(mode=%q) 须愈合落库为 %q，got %q", inVal, want, in.Transport.Mode)
		}
	}
	// 集外值报错拒、回执点名 mode。
	for _, bad := range []string{"bogus", "packet_up", "streamone", "auto-up"} {
		in := r11b1XHTTPInbound(bad)
		err := Normalize(TypeXray, &in)
		if err == nil {
			t.Fatalf("Normalize(mode=%q) 须被拒，got err=nil 落库 Mode=%q", bad, in.Transport.Mode)
		}
		if !strings.Contains(err.Error(), "mode") {
			t.Fatalf("Normalize(mode=%q) 拒绝回执须点名 mode，got %q", bad, err.Error())
		}
	}
	// 空值补 auto 的既有顺序与默认不变。
	in := r11b1XHTTPInbound("")
	if err := Normalize(TypeXray, &in); err != nil {
		t.Fatalf("Normalize(mode=\"\") 须补默认而非报错：%v", err)
	}
	if in.Transport.Mode != "auto" {
		t.Fatalf("空 Mode 须补默认 auto，got %q", in.Transport.Mode)
	}
	// 非 XHTTP 支 Mode 既有清除口径不变（防过修）：WS 支清空、TCP 支清空。
	ws := Inbound{Protocol: ProtocolVLESS, Port: 443,
		Settings:  Settings{UUID: r11b1UUID},
		Transport: Transport{Type: TransportWS, Path: "/ws", Mode: "bogus"},
		Security:  Security{Type: SecurityNone}}
	if err := Normalize(TypeXray, &ws); err != nil {
		t.Fatalf("WS 支 Normalize 须通过（Mode 恒清）：%v", err)
	}
	if ws.Transport.Mode != "" {
		t.Fatalf("WS 支 Mode 须被清空，got %q", ws.Transport.Mode)
	}
}

// ---------- 断言 ②：合法四值产物逐字不变 ----------

func TestR11B1XHTTPModeLegalValuesProductsVerbatim(t *testing.T) {
	variants := map[string]string{
		"AUTO": "auto", "Packet-Up": "packet-up",
		"STREAM-UP": "stream-up", "Stream-One": "stream-one",
	}
	for _, mode := range []string{"auto", "packet-up", "stream-up", "stream-one"} {
		canon := r11b1XHTTPInbound(mode)
		if err := Normalize(TypeXray, &canon); err != nil {
			t.Fatalf("合法 mode %q Normalize 须通过：%v", mode, err)
		}
		if canon.Transport.Mode != mode {
			t.Fatalf("合法 mode %q 须逐字落库，got %q", mode, canon.Transport.Mode)
		}
		cClash, cURI, cXray := r11b1Products(t, canon)
		// 金标：三面产物逐字含规范值（规范小写输入的产物形态不变）。
		if !strings.Contains(cClash, `mode: "`+mode+`"`) {
			t.Fatalf("合法 mode %q 的 Clash 产物须含 %q，got:\n%s", mode, `mode: "`+mode+`"`, cClash)
		}
		if !strings.Contains(cURI, "mode="+mode) {
			t.Fatalf("合法 mode %q 的 ShareURI 产物须含 %q，got %q", mode, "mode="+mode, cURI)
		}
		if got := xrayRenderedXHTTPMode(t, cXray); got != mode {
			t.Fatalf("合法 mode %q 的 Xray 配置 mode 须逐字为 %q，got %q", mode, mode, got)
		}
		// 大小写变体经愈合后，三面产物与规范输入逐字一致。
		for variant, healed := range variants {
			if healed != mode {
				continue
			}
			vIn := r11b1XHTTPInbound(variant)
			if err := Normalize(TypeXray, &vIn); err != nil {
				t.Fatalf("变体 mode %q Normalize 须愈合通过：%v", variant, err)
			}
			vClash, vURI, vXray := r11b1Products(t, vIn)
			if vClash != cClash || vURI != cURI || vXray != cXray {
				t.Fatalf("变体 %q 愈合后产物须与规范 %q 逐字一致：\nclash %q vs %q\nuri %q vs %q\nxray %q vs %q",
					variant, mode, vClash, cClash, vURI, cURI, vXray, cXray)
			}
		}
	}
}

// ---------- 断言 ④：探针格复跑，三面不再含坏 mode ----------

func TestR11B1XHTTPModeProductsHaveNoBadMode(t *testing.T) {
	// M2/M6 探针原格：AUTO / Packet-Up / bogus 经 Normalize 落库后
	// 逐字进自建 Clash 面、ShareURI 与 Xray 内核配置。修复后变体格
	// 落库即规范值、三面产物只见规范值；bogus 格在 Normalize 即被
	// 拒、无从落库产出（断言 ① 已坐实其拒，此处只复跑产物面）。
	for inVal, want := range map[string]string{"AUTO": "auto", "Packet-Up": "packet-up"} {
		in := r11b1XHTTPInbound(inVal)
		if err := Normalize(TypeXray, &in); err != nil {
			t.Fatalf("探针格 mode=%q Normalize 须愈合通过：%v", inVal, err)
		}
		clash, uri, xray := r11b1Products(t, in)
		if strings.Contains(clash, inVal) || strings.Contains(uri, inVal) || strings.Contains(xray, inVal) {
			t.Fatalf("探针格 mode=%q 三面产物不得再含坏值：\nclash:\n%s\nuri:%s\nxray:%s", inVal, clash, uri, xray)
		}
		if !strings.Contains(clash, `mode: "`+want+`"`) {
			t.Fatalf("探针格 mode=%q Clash 产物须含规范 mode %q，got:\n%s", inVal, want, clash)
		}
		if !strings.Contains(uri, "mode="+want) {
			t.Fatalf("探针格 mode=%q ShareURI 产物须含规范 mode %q，got %q", inVal, want, uri)
		}
		if got := xrayRenderedXHTTPMode(t, xray); got != want {
			t.Fatalf("探针格 mode=%q Xray 配置 mode 须为 %q，got %q", inVal, want, got)
		}
		// 同源第四面（M6 登记）：该入站作链式目标时 Xray 链式出站
		// 的 xhttpSettings.mode 同为规范值。
		e := Endpoint{Name: "sb", Host: "203.0.113.10", Port: 443, Direct: true, Inbound: in}
		ob, err := xrayChainOutbound(e, "chain-out", 0)
		if err != nil {
			t.Fatalf("探针格 mode=%q 链式出站须可达：%v", inVal, err)
		}
		stream, _ := ob["streamSettings"].(obj)
		xs, _ := stream["xhttpSettings"].(obj)
		if xs["mode"] != want {
			t.Fatalf("探针格 mode=%q 链式 xhttpSettings.mode 须为 %q，got %v", inVal, want, xs["mode"])
		}
	}
}
