package kernel

import (
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// D44-1（手动填写节点）：手动表单在前端把填好的字段拼成标准分享链接，
// 再走与粘贴导入完全相同的 ParseImport 链路。本文件钉死这条契约：
// 前端 manualNodeLink() 产出的 8 种协议链接必须被后端原样解析，
// 必填缺失 / 非法端口 / 非法地址必须在同一链路被拒绝，生成的 UUID
// 必须是后端接受的 v4 形态，且解析出的节点经 externalRow 落库后的
// 形态与粘贴导入一致（ProxyProtocol + Extra 往返），从而可进订阅/中转。
//
// F-13（测试保真度）：用例链接不再是本文件内手写的字面量，而是
// testdata/manual-links.json——由 vite-frontend/scripts/
// gen-manual-links.mjs 对真实的 manualNodeLink() 跑出来的 golden
// 快照（含每个用例的输入字段与产出链接，以及 JS randomUUID() /
// http fallback 产出的真实 UUID）。前端改了不重跑生成器，
// TestManualNodeGoldenMatchesFrontend 即红（源码哈希校验）。
// 再生成命令（工作目录 vite-frontend）：
//
//	node scripts/gen-manual-links.mjs
//
//go:embed testdata/manual-links.json
var manualLinksGoldenJSON []byte

type manualLinksGolden struct {
	GeneratedBy  string `json:"generatedBy"`
	Source       string `json:"source"`
	SourceSha256 string `json:"sourceSha256"`
	Cases        []struct {
		Name     string         `json:"name"`
		Protocol string         `json:"protocol"`
		Input    map[string]any `json:"input"`
		Link     string         `json:"link"`
	} `json:"cases"`
	UUIDs struct {
		RandomUUID []string `json:"randomUUID"`
		Fallback   []string `json:"fallback"`
	} `json:"uuids"`
}

func loadManualGolden(t *testing.T) *manualLinksGolden {
	t.Helper()
	var g manualLinksGolden
	if err := json.Unmarshal(manualLinksGoldenJSON, &g); err != nil {
		t.Fatalf("unmarshal testdata/manual-links.json: %v", err)
	}
	if g.GeneratedBy != "vite-frontend/scripts/gen-manual-links.mjs" {
		t.Fatalf("golden generatedBy = %q, want the gen-manual-links.mjs snapshot", g.GeneratedBy)
	}
	if len(g.Cases) == 0 {
		t.Fatalf("golden has no cases")
	}
	// 源码哈希校验：golden 必须与当前 manual-node.tsx 同源。前端改动
	// 后不重跑生成器，这里即红——测试不静默接受过期的前端快照。
	_, thisFile, _, _ := runtime.Caller(0)
	tsxPath := filepath.Join(filepath.Dir(thisFile), "..", "..", "..",
		"vite-frontend", "src", "pages", "inbound", "manual-node.tsx")
	raw, err := os.ReadFile(tsxPath)
	if err != nil {
		t.Fatalf("read manual-node.tsx: %v", err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != g.SourceSha256 {
		t.Fatalf("manual-node.tsx sha256 = %s, golden = %s：前端已改动，"+
			"请在 vite-frontend 下重跑 node scripts/gen-manual-links.mjs 再提交",
			got, g.SourceSha256)
	}
	return &g
}

const manualUUID = "11111111-2222-4333-8444-555555555555"

// manualBackendProtocol：golden 里的前端协议名 -> 后端 ProxyProtocol
// 的规范名。两者仅 ss 一格不同（前端 "ss"，后端 "shadowsocks"）。
func manualBackendProtocol(frontend string) string {
	if frontend == "ss" {
		return ProtocolShadowsocks
	}
	return frontend
}

// manualNodeCaseChecks：golden 用例名 -> 解析后关键字段断言。
// 链接本身来自 golden（JS 产物快照），这里只钉死后端解析出的字段。
func manualNodeCaseChecks() map[string]func(t *testing.T, n ImportedNode) {
	// SS2022 密钥：16 个零字节的 base64（与生成器输入同值）。
	ssKey := base64.StdEncoding.EncodeToString(make([]byte, 16))
	// Reality pbk：32 字节的 RawURL base64（43 个 A 即 32 个零字节）。
	realityPBK := strings.Repeat("A", 43)

	return map[string]func(t *testing.T, n ImportedNode){
		"ss": func(t *testing.T, n ImportedNode) {
			if n.Proxy.str("cipher") != "2022-blake3-aes-128-gcm" {
				t.Errorf("ss cipher = %q", n.Proxy.str("cipher"))
			}
			if n.Proxy.str("password") != ssKey {
				t.Errorf("ss password = %q, want the 16-byte key", n.Proxy.str("password"))
			}
		},
		"vmess": func(t *testing.T, n ImportedNode) {
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
		"vmess-ws-tls": func(t *testing.T, n ImportedNode) {
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
		"vless-tls": func(t *testing.T, n ImportedNode) {
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
		"vless-reality": func(t *testing.T, n ImportedNode) {
			ro := n.Proxy.sub("reality-opts")
			if ro.str("public-key") != realityPBK {
				t.Errorf("vless reality pbk = %q", ro.str("public-key"))
			}
			if ro.str("short-id") != "abcd1234" {
				t.Errorf("vless reality sid = %q", ro.str("short-id"))
			}
		},
		"vless-ws": func(t *testing.T, n ImportedNode) {
			if n.Proxy.str("network") != "ws" {
				t.Errorf("vless network = %q", n.Proxy.str("network"))
			}
			if n.Proxy.sub("ws-opts").str("path") != "/ws" {
				t.Errorf("vless ws path = %q", n.Proxy.sub("ws-opts").str("path"))
			}
		},
		"trojan": func(t *testing.T, n ImportedNode) {
			if n.Proxy.str("password") != "secretpass" {
				t.Errorf("trojan password = %q", n.Proxy.str("password"))
			}
			if n.Proxy.str("sni") != "example.com" {
				t.Errorf("trojan sni = %q", n.Proxy.str("sni"))
			}
		},
		"hysteria2": func(t *testing.T, n ImportedNode) {
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
		"tuic": func(t *testing.T, n ImportedNode) {
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
		"anytls": func(t *testing.T, n ImportedNode) {
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
		"socks5-auth": func(t *testing.T, n ImportedNode) {
			if n.Proxy.str("username") != "user1" || n.Proxy.str("password") != "pass1" {
				t.Errorf("socks5 auth = %q/%q", n.Proxy.str("username"), n.Proxy.str("password"))
			}
		},
		"socks5-noauth": func(t *testing.T, n ImportedNode) {
			if n.Proxy.str("username") != "" || n.Proxy.str("password") != "" {
				t.Errorf("socks5 noauth must not carry credentials: %q/%q", n.Proxy.str("username"), n.Proxy.str("password"))
			}
		},
	}
}

// TestManualNodeLinksParse：golden 里的 manualNodeLink() 真实产物
// （8 种协议）必须被 ParseImport 原样接受，且落库字段正确。
func TestManualNodeLinksParse(t *testing.T) {
	g := loadManualGolden(t)
	checks := manualNodeCaseChecks()
	for _, c := range g.Cases {
		kind := c.Name
		nodes, errs := ParseImport(c.Link)
		if len(nodes) != 1 || len(errs) != 0 {
			t.Errorf("%s: ParseImport = %d nodes, errs=%v, want 1 node", kind, len(nodes), errs)
			continue
		}
		n := nodes[0]
		if n.Host != "203.0.113.10" {
			t.Errorf("%s: host = %q", kind, n.Host)
		}
		if got := ProxyProtocol(n.Proxy); got != manualBackendProtocol(c.Protocol) {
			t.Errorf("%s: ProxyProtocol = %q, want %q", kind, got, manualBackendProtocol(c.Protocol))
		}
		// F-10 显式验证：anytls 分支从不写 security 参数。旧手写用例
		// 含 ?security=tls 是失真——golden 里的 anytls 链接不得含
		// security 参数；前端若日后加上，该断言即红。
		if kind == "anytls" {
			u, err := url.Parse(c.Link)
			if err != nil {
				t.Errorf("anytls: url.Parse: %v", err)
			} else if u.Query().Has("security") {
				t.Errorf("anytls golden link must not carry a security param, got %q", c.Link)
			}
		}
		if check := checks[kind]; check != nil {
			check(t, n)
		} else {
			t.Errorf("%s: no field check registered for golden case", kind)
		}
	}
}

// TestManualNodeLinksRejectBadInput：非法端口/地址、必填缺失、
// 格式错误的 UUID/密钥/公钥，必须在导入解析层被逐条拒绝。
// （拒绝探针本就是“坏输入”本身，手写字面量无保真度问题，维持原样。）
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

// TestManualNodeUUIDGeneration：前端「生成」按钮产出的 v4 UUID 必须与
// 后端 uuidPattern 同形、且能进 vless/tuic/vmess 链接解析。
// UUID 来自 golden——生成器对真实的 JS randomUUID() 跑了两条路径：
// crypto.randomUUID（安全上下文）与 crypto.getRandomValues 手拼 v4
// （http fallback）。不再用 Go 的 uuid.NewString() 冒充 JS 产物。
func TestManualNodeUUIDGeneration(t *testing.T) {
	g := loadManualGolden(t)
	if len(g.UUIDs.RandomUUID) == 0 || len(g.UUIDs.Fallback) == 0 {
		t.Fatalf("golden uuids missing: randomUUID=%d fallback=%d",
			len(g.UUIDs.RandomUUID), len(g.UUIDs.Fallback))
	}
	seen := map[string]bool{}
	checkOne := func(source, id string) {
		t.Helper()
		if seen[id] {
			t.Fatalf("generated duplicate UUID %q", id)
		}
		seen[id] = true
		// v4 形态：version nibble 必须为 4，variant nibble 为 8/9/a/b。
		if len(id) != 36 || id[14] != '4' || !strings.ContainsRune("89ab", rune(id[19])) {
			t.Fatalf("%s UUID %q is not v4 (version/variant bits)", source, id)
		}
		if !uuidPattern.MatchString(id) {
			t.Fatalf("%s UUID %q does not match uuidPattern", source, id)
		}
		link := "vless://" + id + "@203.0.113.10:443?encryption=none&security=none#gen"
		nodes, errs := ParseImport(link)
		if len(nodes) != 1 || len(errs) != 0 {
			t.Fatalf("%s UUID %q rejected: nodes=%d errs=%v", source, id, len(nodes), errs)
		}
		if nodes[0].Proxy.str("uuid") != id {
			t.Fatalf("parsed uuid = %q, want %q", nodes[0].Proxy.str("uuid"), id)
		}
	}
	for _, id := range g.UUIDs.RandomUUID {
		checkOne("crypto.randomUUID", id)
	}
	for _, id := range g.UUIDs.Fallback {
		checkOne("getRandomValues-fallback", id)
	}
}

// TestManualNodeRowShape 的落库比对已移至 handler 包
// （go-backend/internal/http/handler/manual_node_row_shape_test.go）：
// externalRow 在 handler 包，kernel 包因 import cycle 调不到它；
// 那边真调 externalRow 对 golden 链接与粘贴导入的同协议链接分别
// 落库成行，再逐字段比对（ProxyProtocol + Extra JSON + 关键字段）。
// 这里只保留 JSON 往返这一 kernel 包内可做的部分：ParseImport 产物
// 的 Proxy  marshal/unmarshal 后协议与关键字段不变。
func TestManualNodeRowShape(t *testing.T) {
	g := loadManualGolden(t)
	for _, c := range g.Cases {
		nodes, errs := ParseImport(c.Link)
		if len(nodes) != 1 || len(errs) != 0 {
			t.Errorf("%s: ParseImport = %d nodes, errs=%v", c.Name, len(nodes), errs)
			continue
		}
		raw, err := json.Marshal(nodes[0].Proxy)
		if err != nil {
			t.Errorf("%s: marshal proxy: %v", c.Name, err)
			continue
		}
		var back Proxy
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Errorf("%s: unmarshal proxy: %v", c.Name, err)
			continue
		}
		if got := ProxyProtocol(back); got != manualBackendProtocol(c.Protocol) {
			t.Errorf("%s: round-tripped ProxyProtocol = %q, want %q", c.Name, got, manualBackendProtocol(c.Protocol))
		}
		if back.str("server") != "203.0.113.10" {
			t.Errorf("%s: round-tripped server = %q", c.Name, back.str("server"))
		}
		if back.num("port") != nodes[0].Port {
			t.Errorf("%s: round-tripped port = %d, want %d", c.Name, back.num("port"), nodes[0].Port)
		}
		// 订阅/中转摘要面：解析出的节点必须能被 DescribeProxy 正常描述。
		if d := DescribeProxy(back); d == "" || strings.Contains(d, "unknown") {
			t.Errorf("%s: DescribeProxy = %q", c.Name, d)
		}
	}
}
