package kernel

import (
	"strings"
	"testing"
)

// R10 批 2（ENC 文法对齐，断言四条分开登记、不得互替，N-35）：
//
//   - R10X-011（W-1，P3）：元素数 ∈ {3,4,5}——4 元素组转放行。
//   - R10X-012（W-2，P3）：首组 0 / 首数 <100 只查前三数——第 4 数起
//     含 0 转放行；前三数内的 0 与 <100 维持拒（不得过放）。
//   - R10X-004（W-3，P2）：位数判定改按 token 原始字符串长度 ≤11
//     （计符号）——`+`11 位转拒；`+`10 位、plain 11 位等证伪格仍放行。
//   - R10X-010（D-05，P3）：proxyURI 的 vless 支加
//     validImportedProxyFields(p, ssFaceOther) 谓词门——坏存量两格
//     转不发链、合法 ENC 的 URI 逐字不变。
//
// 口径唯一依据 = R10-X-verdicts §1.3（按 Xray 25.9.5 / mihomo
// v1.19.33 双二进制实测集一次对齐）。

// ---- R10X-011（W-1）：元素数 {3,4,5} ----

func TestR10B2W1Count4Accepted(t *testing.T) {
	for name, pad := range map[string]string{
		"count4-first": "100-200-300-400",
		"count4-later": "100-200-300.100-200-300-400",
		"count3":       "100-200-300",
		"count5":       "100-200-300-400-500",
	} {
		enc := r9b2EncWithPad(pad)
		if !ValidVLESSEncryption(enc) {
			t.Fatalf("W-1 %s：谓词须放行 pad=%q", name, pad)
		}
		if nodes, errs := ParseImport(r9b2ClashVLESS(enc)); len(errs) != 0 || len(nodes) != 1 {
			t.Fatalf("W-1 %s：进口须放行 pad=%q：nodes=%v errs=%v", name, pad, nodes, errs)
		}
		p := Proxy{"type": "vless", "server": "1.2.3.4", "port": 443, "uuid": r8b1UUID, "encryption": enc}
		if !validImportedProxyFields(p, ssFaceClash) {
			t.Fatalf("W-1 %s：存量/渲染谓词须放行 pad=%q", name, pad)
		}
	}
	// 原拒格仍拒：元素数 1/2/6（及 7）。
	for name, pad := range map[string]string{
		"count1": "100",
		"count2": "100-200",
		"count6": "100-200-300-400-500-600",
		"count7": "100-200-300-400-500-600-700",
	} {
		if ValidVLESSEncryption(r9b2EncWithPad(pad)) {
			t.Fatalf("W-1 %s：元素数原拒格须仍拒 pad=%q", name, pad)
		}
	}
}

// ---- R10X-012（W-2）：首组只查前三数 ----

func TestR10B2W2FirstGroupTailZeroAccepted(t *testing.T) {
	for name, pad := range map[string]string{
		"pos4zero-5grp":    "100-200-300-0-500",
		"pos5zero-5grp":    "100-200-300-400-0",
		"count4-zerotail":  "100-200-300-0",
		"pos4zero-4grp-lt": "100-200-300-50",
	} {
		enc := r9b2EncWithPad(pad)
		if !ValidVLESSEncryption(enc) {
			t.Fatalf("W-2 %s：谓词须放行 pad=%q", name, pad)
		}
		if nodes, errs := ParseImport(r9b2ClashVLESS(enc)); len(errs) != 0 || len(nodes) != 1 {
			t.Fatalf("W-2 %s：进口须放行 pad=%q：nodes=%v errs=%v", name, pad, nodes, errs)
		}
	}
	// 原拒格仍拒：前三数内的 0（pos1/pos2/pos3，含 4/5 元素组）与
	// 首数 <100（plain / `+` 前缀 / 4/5 元素组）。
	for name, pad := range map[string]string{
		"pos1zero":     "0-200-300",
		"pos2zero":     "100-0-300",
		"pos3zero":     "100-200-0",
		"pos2zero-5":   "100-0-300-400-500",
		"pos3zero-4":   "100-200-0-400",
		"first99":      "99-200-300",
		"first99-5":    "99-200-300-400-500",
		"first-plus99": "+99-200-300",
		"first75-zero": "75-0-111",
	} {
		enc := r9b2EncWithPad(pad)
		if ValidVLESSEncryption(enc) {
			t.Fatalf("W-2 %s：前三数原拒格须仍拒 pad=%q", name, pad)
		}
		if nodes, errs := ParseImport(r9b2ClashVLESS(enc)); len(nodes) != 0 || len(errs) == 0 {
			t.Fatalf("W-2 %s：进口须仍拒 pad=%q：nodes=%v errs=%v", name, pad, nodes, errs)
		}
	}
}

// ---- R10X-004（W-3）：token 原始长度 ≤11（计符号）----

func TestR10B2W3SignedTokenLength(t *testing.T) {
	// 转拒格：`+` × 11 位数字 = 12 字符；零填充同格（与数值量级无关）；
	// 后组同形；`+` × 12 位维持拒。
	for name, pad := range map[string]string{
		"plus11":        "+99999999999-200-300",
		"plus11-zeropd": "+00000000100-200-300",
		"plus11-later":  "100-200-300.+99999999999-200-300",
		"plus12":        "+999999999999-200-300",
		"plain12":       "999999999999-200-300",
	} {
		enc := r9b2EncWithPad(pad)
		if ValidVLESSEncryption(enc) {
			t.Fatalf("W-3 %s：谓词须拒 pad=%q", name, pad)
		}
		if nodes, errs := ParseImport(r9b2ClashVLESS(enc)); len(nodes) != 0 || len(errs) == 0 {
			t.Fatalf("W-3 %s：进口须拒 pad=%q：nodes=%v errs=%v", name, pad, nodes, errs)
		}
		p := Proxy{"type": "vless", "server": "1.2.3.4", "port": 443, "uuid": r8b1UUID, "encryption": enc}
		if validImportedProxyFields(p, ssFaceClash) {
			t.Fatalf("W-3 %s：存量/渲染谓词须拒 pad=%q", name, pad)
		}
	}
	// 证伪格仍放行：`+`10 位 = 11 字符、plain 11 位、`+` 小数、前导零、
	// 无前缀超 uint32 值。
	for name, pad := range map[string]string{
		"plus10":       "+9999999999-200-300",
		"plus10-u32p1": "+4294967296-200-300",
		"plain11":      "99999999999-200-300",
		"plain-u32p1":  "4294967296-200-300",
		"plus-small":   "+100-200-300",
		"leading-zero": "0100-200-300",
		"uint32max":    "4294967295-200-300",
		"plus10-later": "100-200-300.+9999999999-200-300",
	} {
		enc := r9b2EncWithPad(pad)
		if !ValidVLESSEncryption(enc) {
			t.Fatalf("W-3 %s：证伪格须保持放行 pad=%q", name, pad)
		}
		if nodes, errs := ParseImport(r9b2ClashVLESS(enc)); len(errs) != 0 || len(nodes) != 1 {
			t.Fatalf("W-3 %s：证伪格进口须放行 pad=%q：nodes=%v errs=%v", name, pad, nodes, errs)
		}
	}
}

// ---- 对齐全表（谓词 × 进口 × 存量谓词三面逐格同结论）----

func TestR10B2AlignmentFullTable(t *testing.T) {
	cases := []struct {
		name string
		pad  string
		want bool
	}{
		// 元素数 1/2/3/4/5/6
		{"count1", "100", false},
		{"count2", "100-200", false},
		{"count3", "100-200-300", true},
		{"count4", "100-200-300-400", true},
		{"count5", "100-200-300-400-500", true},
		{"count6", "100-200-300-400-500-600", false},
		{"count4-later", "100-200-300.100-200-300-400", true},
		{"count2-later", "100-200-300.100-200", false},
		// 位数 plain/`+` × 10/11/12
		{"plain10", "9999999999-200-300", true},
		{"plain11", "99999999999-200-300", true},
		{"plain12", "999999999999-200-300", false},
		{"plus10", "+9999999999-200-300", true},
		{"plus11", "+99999999999-200-300", false},
		{"plus12", "+999999999999-200-300", false},
		{"digits12-later", "100-200-300.999999999999-200-300", false},
		// 首组 0 位置全格
		{"zero-pos1", "0-200-300", false},
		{"zero-pos2", "100-0-300", false},
		{"zero-pos3", "100-200-0", false},
		{"zero-pos4-5grp", "100-200-300-0-500", true},
		{"zero-pos5-5grp", "100-200-300-400-0", true},
		{"zero-pos4-4grp", "100-200-300-0", true},
		{"zero-later", "100-200-300.75-0-111", true},
		// 首数 <100 位置全格（只首组首数；中位/后组 <100 放行）
		{"lt100-first", "99-200-300", false},
		{"lt100-first-5grp", "99-200-300-400-500", false},
		{"lt100-mid", "100-50-300", true},
		{"lt100-later-first", "100-200-300.50-200-300", true},
		// 证伪格全集
		{"plus-small", "+100-200-300", true},
		{"leading-zero", "0100-200-300", true},
		{"uint32max", "4294967295-200-300", true},
		{"panel-chain", "100-111-1111.75-0-111.50-0-3333", true},
		{"two-groups", "100-200-300.400-500-600", true},
	}
	for _, c := range cases {
		enc := r9b2EncWithPad(c.pad)
		if got := ValidVLESSEncryption(enc); got != c.want {
			t.Fatalf("全表 %s：谓词=%v want %v pad=%q", c.name, got, c.want, c.pad)
		}
		nodes, errs := ParseImport(r9b2ClashVLESS(enc))
		if c.want && (len(errs) != 0 || len(nodes) != 1) {
			t.Fatalf("全表 %s：进口须放行 pad=%q：nodes=%v errs=%v", c.name, c.pad, nodes, errs)
		}
		if !c.want && (len(nodes) != 0 || len(errs) == 0) {
			t.Fatalf("全表 %s：进口须拒 pad=%q：nodes=%v errs=%v", c.name, c.pad, nodes, errs)
		}
		p := Proxy{"type": "vless", "server": "1.2.3.4", "port": 443, "uuid": r8b1UUID, "encryption": enc}
		if got := validImportedProxyFields(p, ssFaceClash); got != c.want {
			t.Fatalf("全表 %s：存量谓词=%v want %v pad=%q", c.name, got, c.want, c.pad)
		}
	}
	// 无填充官方形 / none / 空不回归。
	if !ValidVLESSEncryption(r8b1Enc4X25519) || !ValidVLESSEncryption(r8b1Enc4MLKEM) ||
		!ValidVLESSEncryption("") || !ValidVLESSEncryption("none") {
		t.Fatal("全表：官方 4 段 / 空 / none 须保持合法")
	}
}

// ---- R10X-010（D-05）：proxyURI vless 支谓词门 ----

func TestR10B2D05ProxyURIVlessGate(t *testing.T) {
	// 坏存量格 1：ENC 大写段形态（进口已拒、仅存量可达；R10-D FACE
	// vless-enc-UPPERseg 原格）。
	badEnc := Endpoint{Name: "vless-enc-UPPERseg", Host: "203.0.113.7", Port: 443, Direct: true, Proxy: Proxy{
		"type": "vless", "server": "203.0.113.7", "port": 443, "uuid": r8b1UUID,
		"encryption": "MLKEM768X25519PLUS.NATIVE.0RTT." + r9b2Pub32,
	}}
	if uri, ok := proxyURI(badEnc); ok {
		t.Fatalf("D-05 坏 ENC 存量 URI 面须不发链，got %q", uri)
	}
	// 坏存量格 2：Reality short-id 大写 hex（R10-D FACE
	// vless-reality-sid-UPPER 原格）。
	badSID := Endpoint{Name: "vless-reality-sid-UPPER", Host: "203.0.113.7", Port: 443, Direct: true, Proxy: Proxy{
		"type": "vless", "server": "203.0.113.7", "port": 443, "uuid": r8b1UUID, "tls": true,
		"servername":   "s.example.com",
		"reality-opts": map[string]interface{}{"public-key": strings.Repeat("A", 43), "short-id": "0123ABCD"},
	}}
	if uri, ok := proxyURI(badSID); ok {
		t.Fatalf("D-05 坏 Reality 存量 URI 面须不发链，got %q", uri)
	}
	// 合法 ENC：URI 逐字不变（防过修，期望串逐字写死）。
	encStr := "mlkem768x25519plus.native.0rtt.100-111-1111.75-0-111.50-0-3333." + r9b2Pub32
	good := Endpoint{Name: "d05-good", Host: "203.0.113.7", Port: 443, Direct: true, Proxy: Proxy{
		"type": "vless", "server": "203.0.113.7", "port": 443, "uuid": r8b1UUID,
		"network": "tcp", "encryption": encStr,
	}}
	want := "vless://" + r8b1UUID + "@203.0.113.7:443?encryption=" + encStr + "&security=none&type=tcp#d05-good"
	if uri, ok := proxyURI(good); !ok || uri != want {
		t.Fatalf("D-05 合法 ENC 的 URI 须逐字不变：ok=%v\ngot  %q\nwant %q", ok, uri, want)
	}
	// 合法 Reality：URI 照常发链且逐字含规范 sid/pbk。
	goodR := Endpoint{Name: "d05-reality", Host: "203.0.113.7", Port: 443, Direct: true, Proxy: Proxy{
		"type": "vless", "server": "203.0.113.7", "port": 443, "uuid": r8b1UUID, "tls": true,
		"servername":   "s.example.com",
		"reality-opts": map[string]interface{}{"public-key": strings.Repeat("A", 43), "short-id": "0123abcd"},
	}}
	uri, ok := proxyURI(goodR)
	if !ok || !strings.Contains(uri, "sid=0123abcd") || !strings.Contains(uri, "security=reality") {
		t.Fatalf("D-05 合法 Reality 的 URI 须照常发链：ok=%v uri=%q", ok, uri)
	}
	// 普通合法 vless（无 ENC/Reality）不误伤。
	plain := Endpoint{Name: "d05-plain", Host: "203.0.113.7", Port: 443, Direct: true, Proxy: Proxy{
		"type": "vless", "server": "203.0.113.7", "port": 443, "uuid": r8b1UUID, "network": "tcp",
	}}
	if _, ok := proxyURI(plain); !ok {
		t.Fatal("D-05 普通合法 vless URI 须照常发链")
	}
}
