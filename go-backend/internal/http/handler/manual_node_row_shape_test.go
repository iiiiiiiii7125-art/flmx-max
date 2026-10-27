package handler

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"go-backend/internal/kernel"
)

// TestManualNodeRowShape（F-13）：手动链接解析出的节点，经 externalRow
// 真正落库成行后的形态，必须与粘贴导入的同协议节点行逐字段一致
// （ProxyProtocol + Extra JSON + 关键字段），下游的订阅装配与中转读取
// 才能把它当作同一个导入节点。
//
// 本测试只能落在 handler 包：externalRow 在本包，kernel 包因 import
// cycle 调不到它（旧版 kernel 测试注释自认“只做 JSON 往返、并未调用
// externalRow”，即 F-13 的第三点）。
//
// 手动侧链接来自 golden 快照（testdata/manual-links.json，生成器对真实
// 的 manualNodeLink() 跑出）；粘贴侧链接是同一节点的等价手写分享链接
// （参数顺序 / anytls 是否带 security=tls 等写法不同，解析产物应相同）。
// 两侧都走生产口径 ParseImport -> externalRow（与 inboundImport 内
// 同款调用），固定同一 now，比对除 ID 外的全部行字段。

type manualRowGolden struct {
	Cases []struct {
		Name     string         `json:"name"`
		Protocol string         `json:"protocol"`
		Input    map[string]any `json:"input"`
		Link     string         `json:"link"`
	} `json:"cases"`
}

// pastedLinkCases：与 golden 用例一一对应的粘贴导入等价链接。
// 写法取自旧版手写用例（Go map marshal 的 VMess base64、参数顺序不同、
// anytls 带 ?security=tls）——它们是“粘贴进来”的样子，不是前端产物。
func pastedLinkCases() map[string]string {
	manualUUID := "11111111-2222-4333-8444-555555555555"
	ssKey := base64.StdEncoding.EncodeToString(make([]byte, 16))
	ssUser := base64.RawURLEncoding.EncodeToString(
		[]byte("2022-blake3-aes-128-gcm:" + ssKey))

	vmessJSON, _ := json.Marshal(map[string]string{
		"v": "2", "ps": "manual-vmess", "add": "203.0.113.10",
		"port": "443", "id": manualUUID, "aid": "0", "scy": "auto",
		"net": "tcp", "type": "none", "host": "", "path": "",
		"tls": "", "sni": "", "fp": "",
	})
	vmessWSJSON, _ := json.Marshal(map[string]string{
		"v": "2", "ps": "manual-vmess-ws", "add": "203.0.113.10",
		"port": "443", "id": manualUUID, "aid": "0", "scy": "auto",
		"net": "ws", "type": "none", "host": "example.com",
		"path": "/ws", "tls": "tls", "sni": "example.com", "fp": "chrome",
	})
	realityPBK := strings.Repeat("A", 43)

	return map[string]string{
		"ss": "ss://" + ssUser + "@203.0.113.10:8388#manual-ss",
		"vmess": "vmess://" +
			base64.StdEncoding.EncodeToString(vmessJSON),
		"vmess-ws-tls": "vmess://" +
			base64.StdEncoding.EncodeToString(vmessWSJSON),
		"vless-tls": "vless://" + manualUUID +
			"@203.0.113.10:443?encryption=none&security=tls&sni=example.com&fp=chrome&type=tcp#manual-vless",
		"vless-reality": "vless://" + manualUUID +
			"@203.0.113.10:443?encryption=none&security=reality&sni=www.microsoft.com&fp=chrome&pbk=" +
			realityPBK + "&sid=abcd1234&type=tcp#manual-vless-reality",
		"vless-ws": "vless://" + manualUUID +
			"@203.0.113.10:443?encryption=none&security=tls&sni=example.com&fp=chrome&type=ws&path=%2Fws&host=example.com#manual-vless-ws",
		"trojan":        "trojan://secretpass@203.0.113.10:443?security=tls&sni=example.com&fp=chrome&type=tcp#manual-trojan",
		"hysteria2":     "hysteria2://secretpass@203.0.113.10:443/?obfs=salamander&obfs-password=obfspass&mport=20000-30000#manual-hy2",
		"tuic":          "tuic://" + manualUUID + ":secretpass@203.0.113.10:443?alpn=h3&congestion_control=bbr&udp_relay_mode=native#manual-tuic",
		"anytls":        "anytls://secretpass@203.0.113.10:443/?security=tls&sni=example.com&fp=chrome#manual-anytls",
		"socks5-auth":   "socks5://user1:pass1@203.0.113.10:1080#manual-socks5",
		"socks5-noauth": "socks5://203.0.113.10:1080#manual-socks5-noauth",
	}
}

func TestManualNodeRowShape(t *testing.T) {
	raw, err := os.ReadFile("../../kernel/testdata/manual-links.json")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var g manualRowGolden
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatalf("unmarshal golden: %v", err)
	}
	pasted := pastedLinkCases()
	// 固定 now：两侧行的时间字段才能逐字段比对。
	const now = int64(1790000000000)

	for _, c := range g.Cases {
		pastedLink, ok := pasted[c.Name]
		if !ok {
			t.Errorf("%s: no pasted counterpart registered", c.Name)
			continue
		}
		manualNodes, errs := kernel.ParseImport(c.Link)
		if len(manualNodes) != 1 || len(errs) != 0 {
			t.Errorf("%s: ParseImport(manual) = %d nodes, errs=%v", c.Name, len(manualNodes), errs)
			continue
		}
		pastedNodes, errs := kernel.ParseImport(pastedLink)
		if len(pastedNodes) != 1 || len(errs) != 0 {
			t.Errorf("%s: ParseImport(pasted) = %d nodes, errs=%v", c.Name, len(pastedNodes), errs)
			continue
		}
		// 真调 externalRow：与 inboundImport 内同一生产口径落库成行。
		manualRow, err := externalRow(manualNodes[0], false, now)
		if err != nil {
			t.Errorf("%s: externalRow(manual): %v", c.Name, err)
			continue
		}
		pastedRow, err := externalRow(pastedNodes[0], false, now)
		if err != nil {
			t.Errorf("%s: externalRow(pasted): %v", c.Name, err)
			continue
		}

		// 逐字段比对（ID 两侧均为 0，不参与；其余字段必须全等）。
		if manualRow.Name != pastedRow.Name {
			t.Errorf("%s: row Name = %q, pasted = %q", c.Name, manualRow.Name, pastedRow.Name)
		}
		if want := c.Input["name"].(string); manualRow.Name != want {
			t.Errorf("%s: row Name = %q, want golden input name %q", c.Name, manualRow.Name, want)
		}
		if manualRow.Protocol != pastedRow.Protocol {
			t.Errorf("%s: row Protocol = %q, pasted = %q", c.Name, manualRow.Protocol, pastedRow.Protocol)
		}
		if manualRow.Protocol != c.Protocol && manualRow.Protocol != kernel.ProtocolShadowsocks {
			t.Errorf("%s: row Protocol = %q, want %q", c.Name, manualRow.Protocol, c.Protocol)
		}
		if manualRow.Listen != pastedRow.Listen || manualRow.Listen != "203.0.113.10" {
			t.Errorf("%s: row Listen = %q, pasted = %q", c.Name, manualRow.Listen, pastedRow.Listen)
		}
		if manualRow.Port != pastedRow.Port {
			t.Errorf("%s: row Port = %d, pasted = %d", c.Name, manualRow.Port, pastedRow.Port)
		}
		if manualRow.ShareHost != pastedRow.ShareHost || manualRow.ShareHost != "203.0.113.10" {
			t.Errorf("%s: row ShareHost = %q, pasted = %q", c.Name, manualRow.ShareHost, pastedRow.ShareHost)
		}
		// Extra 是 Proxy 的 JSON：两侧必须逐字节相等，否则订阅/中转
		// 读到的节点形态就不一致。
		if manualRow.Extra != pastedRow.Extra {
			t.Errorf("%s: row Extra differs:\n manual: %s\n pasted: %s", c.Name, manualRow.Extra, pastedRow.Extra)
		}
		if manualRow.RelayOnly != pastedRow.RelayOnly || manualRow.RelayOnly != 0 {
			t.Errorf("%s: row RelayOnly = %d, pasted = %d", c.Name, manualRow.RelayOnly, pastedRow.RelayOnly)
		}
		if manualRow.Enabled != pastedRow.Enabled || manualRow.Enabled != 1 {
			t.Errorf("%s: row Enabled = %d, pasted = %d", c.Name, manualRow.Enabled, pastedRow.Enabled)
		}
		if manualRow.CreatedTime != now || manualRow.UpdatedTime != now {
			t.Errorf("%s: row times = %d/%d, want %d", c.Name, manualRow.CreatedTime, manualRow.UpdatedTime, now)
		}
		// Extra 回读：ProxyProtocol 与摘要面两侧一致。golden 里的前端协议名
		// 与后端规范名仅 ss 一格不同（"ss" vs "shadowsocks"）。
		var manualProxy, pastedProxy kernel.Proxy
		if err := json.Unmarshal([]byte(manualRow.Extra), &manualProxy); err != nil {
			t.Errorf("%s: unmarshal manual Extra: %v", c.Name, err)
			continue
		}
		if err := json.Unmarshal([]byte(pastedRow.Extra), &pastedProxy); err != nil {
			t.Errorf("%s: unmarshal pasted Extra: %v", c.Name, err)
			continue
		}
		wantProtocol := c.Protocol
		if wantProtocol == "ss" {
			wantProtocol = kernel.ProtocolShadowsocks
		}
		if got := kernel.ProxyProtocol(manualProxy); got != wantProtocol {
			t.Errorf("%s: Extra ProxyProtocol = %q, want %q", c.Name, got, wantProtocol)
		}
		if d := kernel.DescribeProxy(manualProxy); d == "" || strings.Contains(d, "unknown") {
			t.Errorf("%s: DescribeProxy(manual) = %q", c.Name, d)
		}
		if kernel.DescribeProxy(manualProxy) != kernel.DescribeProxy(pastedProxy) {
			t.Errorf("%s: DescribeProxy differs: %q vs %q", c.Name,
				kernel.DescribeProxy(manualProxy), kernel.DescribeProxy(pastedProxy))
		}
	}
}
