package kernel

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// D44-1（手动填写节点）：手动表单在前端把填好的字段拼成标准分享链接，
// 再走与粘贴导入完全相同的 ParseImport 链路。本文件钉死这条契约：
// 前端 manualNodeLink() 产出的 8 种协议链接必须被后端原样解析，
// 必填缺失 / 非法端口 / 非法地址必须在同一链路被拒绝，生成的 UUID
// 必须是后端接受的 v4 形态，且解析出的节点经 externalRow 落库后的
// 形态与粘贴导入一致（ProxyProtocol + Extra 往返），从而可进订阅/中转。

const manualUUID = "11111111-2222-4333-8444-555555555555"

func manualNodeLinkCases() map[string]struct {
	link     string
	protocol string
	check    func(t *testing.T, n ImportedNode)
} {
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
	// Reality pbk：32 字节的 RawURL base64（43 个 A 即 32 个零字节）。
	realityPBK := strings.Repeat("A", 43)

	return map[string]struct {
		link     string
		protocol string
		check    func(t *testing.T, n ImportedNode)
	}{
		"ss": {
			link:     "ss://" + ssUser + "@203.0.113.10:8388#manual-ss",
			protocol: ProtocolShadowsocks,
			check: func(t *testing.T, n ImportedNode) {
				if n.Proxy.str("cipher") != "2022-blake3-aes-128-gcm" {
					t.Errorf("ss cipher = %q", n.Proxy.str("cipher"))
				}
				if n.Proxy.str("password") != ssKey {
					t.Errorf("ss password = %q, want the 16-byte key", n.Proxy.str("password"))
				}
			},
		},
		"vmess": {
			link:     "vmess://" + base64.StdEncoding.EncodeToString(vmessJSON),
			protocol: ProtocolVMess,
			check: func(t *testing.T, n ImportedNode) {
				if n.Proxy.str("uuid") != manualUUID {
					t.Errorf("vmess uuid = %q", n.Proxy.str("uuid"))
				}
				if n.Proxy.str("cipher") != "auto" {
					t.Errorf("vmess cipher = %q", n.Proxy.str("cipher"))
				}
				if n.Proxy.num("alterId") != 0 {
					t.Errorf("vmess alterId = %d", n.Proxy.num("alterId"))
				}
			},
		},
		"vmess-ws-tls": {
			link:     "vmess://" + base64.StdEncoding.EncodeToString(vmessWSJSON),
			protocol: ProtocolVMess,
			check: func(t *testing.T, n ImportedNode) {
				if !n.Proxy.flag("tls") {
					t.Errorf("vmess ws-tls must carry tls")
				}
				if n.Proxy.str("servername") != "example.com" {
					t.Errorf("vmess servername = %q", n.Proxy.str("servername"))
				}
				if n.Proxy.str("network") != "ws" {
					t.Errorf("vmess network = %q", n.Proxy.str("network"))
				}
				if n.Proxy.sub("ws-opts").str("path") != "/ws" {
					t.Errorf("vmess ws path = %q", n.Proxy.sub("ws-opts").str("path"))
				}
			},
		},
		"vless-tls": {
			link: "vless://" + manualUUID +
				"@203.0.113.10:443?encryption=none&security=tls&sni=example.com&fp=chrome&type=tcp#manual-vless",
			protocol: ProtocolVLESS,
			check: func(t *testing.T, n ImportedNode) {
				if n.Proxy.str("uuid") != manualUUID {
					t.Errorf("vless uuid = %q", n.Proxy.str("uuid"))
				}
				if n.Proxy.str("encryption") != "none" {
					t.Errorf("vless encryption = %q", n.Proxy.str("encryption"))
				}
				if !n.Proxy.flag("tls") || n.Proxy.str("servername") != "example.com" {
					t.Errorf("vless tls/servername = %v/%q", n.Proxy.flag("tls"), n.Proxy.str("servername"))
				}
				if n.Proxy.str("client-fingerprint") != "chrome" {
					t.Errorf("vless fp = %q", n.Proxy.str("client-fingerprint"))
				}
			},
		},
		"vless-reality": {
			link: "vless://" + manualUUID +
				"@203.0.113.10:443?encryption=none&security=reality&sni=www.microsoft.com&fp=chrome&pbk=" +
				realityPBK + "&sid=abcd1234&type=tcp#manual-vless-reality",
			protocol: ProtocolVLESS,
			check: func(t *testing.T, n ImportedNode) {
				ro := n.Proxy.sub("reality-opts")
				if ro.str("public-key") != realityPBK {
					t.Errorf("vless reality pbk = %q", ro.str("public-key"))
				}
				if ro.str("short-id") != "abcd1234" {
					t.Errorf("vless reality sid = %q", ro.str("short-id"))
				}
			},
		},
		"vless-ws": {
			link: "vless://" + manualUUID +
				"@203.0.113.10:443?encryption=none&security=tls&sni=example.com&fp=chrome&type=ws&path=%2Fws&host=example.com#manual-vless-ws",
			protocol: ProtocolVLESS,
			check: func(t *testing.T, n ImportedNode) {
				if n.Proxy.str("network") != "ws" {
					t.Errorf("vless network = %q", n.Proxy.str("network"))
				}
				if n.Proxy.sub("ws-opts").str("path") != "/ws" {
					t.Errorf("vless ws path = %q", n.Proxy.sub("ws-opts").str("path"))
				}
			},
		},
		"trojan": {
			link:     "trojan://secretpass@203.0.113.10:443?security=tls&sni=example.com&fp=chrome&type=tcp#manual-trojan",
			protocol: ProtocolTrojan,
			check: func(t *testing.T, n ImportedNode) {
				if n.Proxy.str("password") != "secretpass" {
					t.Errorf("trojan password = %q", n.Proxy.str("password"))
				}
				if n.Proxy.str("sni") != "example.com" {
					t.Errorf("trojan sni = %q", n.Proxy.str("sni"))
				}
			},
		},
		"hysteria2": {
			link:     "hysteria2://secretpass@203.0.113.10:443/?obfs=salamander&obfs-password=obfspass&mport=20000-30000#manual-hy2",
			protocol: ProtocolHysteria2,
			check: func(t *testing.T, n ImportedNode) {
				if n.Proxy.str("password") != "secretpass" {
					t.Errorf("hysteria2 password = %q", n.Proxy.str("password"))
				}
				if n.Proxy.str("obfs") != "salamander" {
					t.Errorf("hysteria2 obfs = %q", n.Proxy.str("obfs"))
				}
				if n.Proxy.str("obfs-password") != "obfspass" {
					t.Errorf("hysteria2 obfs-password = %q", n.Proxy.str("obfs-password"))
				}
				if n.Proxy.str("ports") != "20000-30000" {
					t.Errorf("hysteria2 ports = %q", n.Proxy.str("ports"))
				}
			},
		},
		"tuic": {
			link: "tuic://" + manualUUID +
				":secretpass@203.0.113.10:443?alpn=h3&congestion_control=bbr&udp_relay_mode=native#manual-tuic",
			protocol: ProtocolTUIC,
			check: func(t *testing.T, n ImportedNode) {
				if n.Proxy.str("uuid") != manualUUID {
					t.Errorf("tuic uuid = %q", n.Proxy.str("uuid"))
				}
				if n.Proxy.str("password") != "secretpass" {
					t.Errorf("tuic password = %q", n.Proxy.str("password"))
				}
				if n.Proxy.str("congestion-controller") != "bbr" {
					t.Errorf("tuic congestion-controller = %q", n.Proxy.str("congestion-controller"))
				}
				if n.Proxy.str("udp-relay-mode") != "native" {
					t.Errorf("tuic udp-relay-mode = %q", n.Proxy.str("udp-relay-mode"))
				}
			},
		},
		"anytls": {
			link:     "anytls://secretpass@203.0.113.10:443/?security=tls&sni=example.com&fp=chrome#manual-anytls",
			protocol: ProtocolAnyTLS,
			check: func(t *testing.T, n ImportedNode) {
				if n.Proxy.str("password") != "secretpass" {
					t.Errorf("anytls password = %q", n.Proxy.str("password"))
				}
				if n.Proxy.str("sni") != "example.com" {
					t.Errorf("anytls sni = %q", n.Proxy.str("sni"))
				}
				if n.Proxy.str("client-fingerprint") != "chrome" {
					t.Errorf("anytls fp = %q", n.Proxy.str("client-fingerprint"))
				}
			},
		},
		"socks5-auth": {
			link:     "socks5://user1:pass1@203.0.113.10:1080#manual-socks5",
			protocol: "socks5",
			check: func(t *testing.T, n ImportedNode) {
				if n.Proxy.str("username") != "user1" || n.Proxy.str("password") != "pass1" {
					t.Errorf("socks5 auth = %q/%q", n.Proxy.str("username"), n.Proxy.str("password"))
				}
			},
		},
		"socks5-noauth": {
			link:     "socks5://203.0.113.10:1080#manual-socks5-noauth",
			protocol: "socks5",
			check: func(t *testing.T, n ImportedNode) {
				if n.Proxy.str("username") != "" || n.Proxy.str("password") != "" {
					t.Errorf("socks5 noauth must not carry credentials: %q/%q", n.Proxy.str("username"), n.Proxy.str("password"))
				}
			},
		},
	}
}

// TestManualNodeLinksParse：手动表单 8 种协议的拼链接产物必须被
// ParseImport 原样接受，且落库字段（协议/地址/端口/凭据）正确。
func TestManualNodeLinksParse(t *testing.T) {
	for kind, c := range manualNodeLinkCases() {
		nodes, errs := ParseImport(c.link)
		if len(nodes) != 1 || len(errs) != 0 {
			t.Errorf("%s: ParseImport = %d nodes, errs=%v, want 1 node", kind, len(nodes), errs)
			continue
		}
		n := nodes[0]
		if n.Host != "203.0.113.10" {
			t.Errorf("%s: host = %q", kind, n.Host)
		}
		if got := ProxyProtocol(n.Proxy); got != c.protocol {
			t.Errorf("%s: ProxyProtocol = %q, want %q", kind, got, c.protocol)
		}
		if c.check != nil {
			c.check(t, n)
		}
	}
}

// TestManualNodeLinksRejectBadInput：非法端口/地址、必填缺失、
// 格式错误的 UUID/密钥/公钥，必须在导入解析层被逐条拒绝。
func TestManualNodeLinksRejectBadInput(t *testing.T) {
	bad := map[string]string{
		"port-0":                  "vless://" + manualUUID + "@203.0.113.10:0#bad",
		"port-65536":              "vless://" + manualUUID + "@203.0.113.10:65536#bad",
		"port-nonnumeric":         "vless://" + manualUUID + "@203.0.113.10:abc#bad",
		"host-loopback":           "vless://" + manualUUID + "@127.0.0.1:443#bad",
		"host-localhost":          "vless://" + manualUUID + "@localhost:443#bad",
		"host-with-port":          "vless://" + manualUUID + "@203.0.113.10:443:8443#bad",
		"missing-uuid":            "vless://@203.0.113.10:443#bad",
		"bad-uuid":                "vless://not-a-uuid@203.0.113.10:443#bad",
		"missing-password-trojan": "trojan://@203.0.113.10:443#bad",
		"missing-password-anytls": "anytls://@203.0.113.10:443/#bad",
		"reality-no-pbk": "vless://" + manualUUID +
			"@203.0.113.10:443?encryption=none&security=reality&sni=www.microsoft.com#bad",
		"reality-bad-pbk": "vless://" + manualUUID +
			"@203.0.113.10:443?encryption=none&security=reality&sni=www.microsoft.com&pbk=short#bad",
		"ss2022-short-key": "ss://" +
			base64.RawURLEncoding.EncodeToString([]byte("2022-blake3-aes-128-gcm:AAAA")) +
			"@203.0.113.10:8388#bad",
		"ss-no-password": "ss://" +
			base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:")) +
			"@203.0.113.10:8388#bad",
		"tuic-no-password": "tuic://" + manualUUID +
			":@203.0.113.10:443#bad",
		"hy2-bad-ports": "hysteria2://secretpass@203.0.113.10:443/?mport=abc#bad",
	}
	for kind, link := range bad {
		nodes, errs := ParseImport(link)
		if len(nodes) != 0 || len(errs) == 0 {
			t.Errorf("%s: ParseImport = %d nodes, errs=%v, want 0 nodes with error", kind, len(nodes), errs)
		}
	}
}

// TestManualNodeUUIDGeneration：前端「生成」按钮产出的 v4 UUID
// （crypto.randomUUID，http 下走 crypto.getRandomValues 手拼 v4）
// 必须与后端 uuidPattern 同形、且能进 vless/tuic/vmess 链接解析；
// 每次生成的值必须不同（不重复）。
func TestManualNodeUUIDGeneration(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		id := uuid.NewString()
		if seen[id] {
			t.Fatalf("generated duplicate UUID %q", id)
		}
		seen[id] = true
		if !uuidPattern.MatchString(strings.ToLower(id)) {
			t.Fatalf("generated UUID %q does not match uuidPattern", id)
		}
		link := "vless://" + id + "@203.0.113.10:443?security=none#gen"
		nodes, errs := ParseImport(link)
		if len(nodes) != 1 || len(errs) != 0 {
			t.Fatalf("generated UUID %q rejected: nodes=%d errs=%v", id, len(nodes), errs)
		}
		if nodes[0].Proxy.str("uuid") != id {
			t.Fatalf("parsed uuid = %q, want %q", nodes[0].Proxy.str("uuid"), id)
		}
	}
}

// TestManualNodeRowShape：手动链接解析出的节点，经 externalRow 的
// 落库形态（ProxyProtocol + Extra JSON 往返）必须与粘贴导入一致，
// 这样下游的订阅装配与中转读取才能把它当作同一个导入节点。
// 这里直接验证 ParseImport 产物在 JSON 往返后协议与关键字段不变
// （externalRow 本身只做 Marshal + 字段搬运）。
func TestManualNodeRowShape(t *testing.T) {
	for kind, c := range manualNodeLinkCases() {
		nodes, errs := ParseImport(c.link)
		if len(nodes) != 1 || len(errs) != 0 {
			t.Errorf("%s: ParseImport = %d nodes, errs=%v", kind, len(nodes), errs)
			continue
		}
		raw, err := json.Marshal(nodes[0].Proxy)
		if err != nil {
			t.Errorf("%s: marshal proxy: %v", kind, err)
			continue
		}
		var back Proxy
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Errorf("%s: unmarshal proxy: %v", kind, err)
			continue
		}
		if got := ProxyProtocol(back); got != c.protocol {
			t.Errorf("%s: round-tripped ProxyProtocol = %q, want %q", kind, got, c.protocol)
		}
		if back.str("server") != "203.0.113.10" {
			t.Errorf("%s: round-tripped server = %q", kind, back.str("server"))
		}
		if back.num("port") != nodes[0].Port {
			t.Errorf("%s: round-tripped port = %d, want %d", kind, back.num("port"), nodes[0].Port)
		}
		// 订阅/中转摘要面：解析出的节点必须能被 DescribeProxy 正常描述。
		if d := DescribeProxy(back); d == "" || strings.Contains(d, "unknown") {
			t.Errorf("%s: DescribeProxy = %q", kind, d)
		}
	}
}
