package kernel

import (
	"strings"
	"testing"
)

// R9 批 2（R9X-002 + R9X-003，断言分开登记、不得互替）：
//
//   - R9X-002（§1）：sing-box JSON 通道 singboxToProxy 漏搬 vless
//     encryption——合法 ENC 经该通道须逐字落库、垃圾串经 finishProxy
//     既有收口逐条被拒 + 回执点名，与另三格式一致；none/缺失不变。
//   - R9X-003（§2）：ValidVLESSEncryption 填充组文法收紧至 Xray
//     25.9.5 / mihomo v1.19.33 二进制实测四类格；证伪格保持放行。

const r9b2Pub32 = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"

func r9b2SingboxVLESS(enc string, withEnc bool) string {
	encField := ""
	if withEnc {
		encField = `,"encryption":"` + enc + `"`
	}
	return `{"outbounds":[{"type":"vless","tag":"sb-vless","server":"1.2.3.4","server_port":443,"uuid":"` + r8b1UUID + `"` + encField + `}]}`
}

func r9b2ClashVLESS(enc string) string {
	return "proxies:\n  - {name: c, type: vless, server: 1.2.3.4, port: 443, uuid: " + r8b1UUID + ", encryption: '" + enc + "', udp: true}\n"
}

func r9b2PaddedMLKEM(t *testing.T) string {
	t.Helper()
	secret, err := GenerateEncryptionSecret(EncryptionAuthMLKEM)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := Settings{EncryptionAuth: EncryptionAuthMLKEM, EncryptionMode: EncryptionModeNative, EncryptionSecret: secret}.EncryptionString()
	if err != nil {
		t.Fatal(err)
	}
	return enc
}

// ---- R9X-002 ----

func TestR9B2SingboxVLESSEncryptionCarried(t *testing.T) {
	paddedX := "mlkem768x25519plus.native.0rtt.100-111-1111.75-0-111.50-0-3333." + r9b2Pub32
	cases := map[string]string{
		"x25519-4seg":   r8b1Enc4X25519,
		"mlkem-4seg":    r8b1Enc4MLKEM,
		"padded-x25519": paddedX,
		"padded-mlkem":  r9b2PaddedMLKEM(t),
	}
	for name, enc := range cases {
		nodes, errs := ParseImport(r9b2SingboxVLESS(enc, true))
		if len(errs) != 0 || len(nodes) != 1 {
			t.Fatalf("%s：sing-box 合法 ENC 须入库：nodes=%v errs=%v", name, nodes, errs)
		}
		if got := nodes[0].Proxy.str("encryption"); got != enc {
			t.Fatalf("%s：encryption 须逐字落库，got %q want %q", name, got, enc)
		}
		e := Endpoint{Name: nodes[0].Name, Host: nodes[0].Host, Port: nodes[0].Port, Direct: true, Proxy: nodes[0].Proxy}
		if _, ok := proxySingbox(e); ok {
			t.Fatalf("%s：落库 ENC 节点 sing-box 面须略过", name)
		}
		node, ok := proxyClash(e)
		if !ok {
			t.Fatalf("%s：落库 ENC 节点 Clash 面须保留", name)
		}
		found := false
		for _, f := range node {
			if f.Key == "encryption" && f.Value == enc {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s：Clash 面须含原串 encryption", name)
		}
	}
}

func TestR9B2SingboxVLESSEncryptionGarbageRejected(t *testing.T) {
	doc := `{"outbounds":[{"type":"vless","tag":"bad-enc","server":"1.2.3.4","server_port":443,"uuid":"` + r8b1UUID + `","encryption":"totally-bogus-enc"},{"type":"shadowsocks","tag":"healthy","server":"203.0.113.99","server_port":8388,"method":"aes-128-gcm","password":"pw"}]}`
	nodes, errs := ParseImport(doc)
	if len(nodes) != 1 || nodes[0].Name != "healthy" {
		t.Fatalf("sing-box 垃圾 ENC 须逐条拒、健康节点不连坐：nodes=%v errs=%v", nodes, errs)
	}
	if len(errs) != 1 || !strings.Contains(errs[0], "VLESS encryption 格式错误") {
		t.Fatalf("sing-box 回执须点名 VLESS encryption 格式错误：errs=%v", errs)
	}
	// 与另两格式回执逐字一致（同一收口文案）。
	for name, other := range map[string]string{
		"clash": r9b2ClashVLESS("totally-bogus-enc"),
		"uri":   "vless://" + r8b1UUID + "@1.2.3.4:443?encryption=totally-bogus-enc&type=tcp#bad",
	} {
		n2, e2 := ParseImport(other)
		if len(n2) != 0 || len(e2) == 0 || !strings.Contains(e2[0], "VLESS encryption 格式错误") {
			t.Fatalf("%s 通道垃圾 ENC 回执须与 sing-box 一致：nodes=%v errs=%v", name, n2, e2)
		}
	}
}

func TestR9B2SingboxVLESSEncryptionNoneMissingUnchanged(t *testing.T) {
	// 缺失：不得出现 encryption 键，节点为普通 vless。
	nodes, errs := ParseImport(r9b2SingboxVLESS("", false))
	if len(errs) != 0 || len(nodes) != 1 {
		t.Fatalf("缺失 encryption 须入库：nodes=%v errs=%v", nodes, errs)
	}
	if _, ok := nodes[0].Proxy["encryption"]; ok {
		t.Fatalf("缺失 encryption 不得造键：%v", nodes[0].Proxy)
	}
	// none：普通 vless 语义不变（sing-box 面不略过）。
	nodes, errs = ParseImport(r9b2SingboxVLESS("none", true))
	if len(errs) != 0 || len(nodes) != 1 {
		t.Fatalf("none 须入库：nodes=%v errs=%v", nodes, errs)
	}
	if got := nodes[0].Proxy.str("encryption"); got != "" && got != "none" {
		t.Fatalf("none 语义不得变异：got %q", got)
	}
	e := Endpoint{Name: nodes[0].Name, Host: nodes[0].Host, Port: nodes[0].Port, Direct: true, Proxy: nodes[0].Proxy}
	if _, ok := proxySingbox(e); !ok {
		t.Fatal("none 节点 sing-box 面须照常下发（普通 vless）")
	}
}

// ---- R9X-003 ----

func r9b2EncWithPad(pad string) string {
	return "mlkem768x25519plus.native.0rtt." + pad + "." + r9b2Pub32
}

func TestR9B2VLESSEncryptionPaddingGrammarRejected(t *testing.T) {
	// 注：count4 格按裁定 {3,5} 集合登记（verdicts §1）；本批双二进制
	// 对照实测 Xray 25.9.5 / mihomo 均 exit 0，分歧已在批报告中点名
	// 待裁定，本用例不断言随探针擅自变更。
	badPads := map[string]string{
		"count1-zero":     "0",
		"count1":          "100",
		"count2":          "100-200",
		"count2-zero":     "0-0",
		"count2-zero-mix": "0-100",
		"count4":          "100-200-300-400",
		"count6":          "100-200-300-400-500-600",
		"count7":          "100-200-300-400-500-600-700",
		"count9":          "100-200-300-400-500-600-700-800-900",
		"count12":         "100-200-300-400-500-600-700-800-900-1000-1100-1200",
		"digits12":        "999999999999-200-300",
		"digits12-later":  "100-200-300.999999999999-200-300",
		"count-later":     "100-200-300.100-200",
		"first-has-zero":  "100-0-300",
		"first-zero-tail": "100-200-0",
		"first-zero-solo": "75-0-111",
		"first-lt100":     "99-200-300",
	}
	for name, pad := range badPads {
		enc := r9b2EncWithPad(pad)
		if ValidVLESSEncryption(enc) {
			t.Fatalf("%s：谓词须拒 pad=%q", name, pad)
		}
		if nodes, errs := ParseImport(r9b2ClashVLESS(enc)); len(nodes) != 0 || len(errs) == 0 {
			t.Fatalf("%s：进口须逐条拒 pad=%q：nodes=%v errs=%v", name, pad, nodes, errs)
		}
		p := Proxy{"type": "vless", "server": "1.2.3.4", "port": 443, "uuid": r8b1UUID, "encryption": enc}
		if validImportedProxyFields(p, ssFaceClash) {
			t.Fatalf("%s：存量/渲染谓词须拒 pad=%q", name, pad)
		}
	}
}

func TestR9B2VLESSEncryptionFalsifiedGridsStillAccepted(t *testing.T) {
	manyGroups := strings.TrimSuffix(strings.Repeat("100-200-300.", 200), ".")
	goodPads := map[string]string{
		"canonical-3":     "100-200-300",
		"count5":          "100-200-300-400-500",
		"plus-prefix":     "+100-200-300",
		"leading-zero":    "0100-200-300",
		"groups200":       manyGroups,
		"digits11":        "99999999999-200-300",
		"uint32max":       "4294967295-200-300",
		"first-mid-lt100": "100-50-300",
		"panel-chain":     "100-111-1111.75-0-111.50-0-3333",
		"two-groups":      "100-200-300.400-500-600",
	}
	for name, pad := range goodPads {
		enc := r9b2EncWithPad(pad)
		if !ValidVLESSEncryption(enc) {
			t.Fatalf("%s：证伪格须保持放行 pad=%q", name, pad)
		}
		if nodes, errs := ParseImport(r9b2ClashVLESS(enc)); len(errs) != 0 || len(nodes) != 1 {
			t.Fatalf("%s：证伪格进口须放行 pad=%q：nodes=%v errs=%v", name, pad, nodes, errs)
		}
	}
	// 无填充 4 段官方形与 none/空不回归。
	if !ValidVLESSEncryption(r8b1Enc4X25519) || !ValidVLESSEncryption("") || !ValidVLESSEncryption("none") {
		t.Fatal("官方 4 段 / 空 / none 须保持合法")
	}
}
