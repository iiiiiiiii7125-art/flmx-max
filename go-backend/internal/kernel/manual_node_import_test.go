package kernel

import (
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
		// T-01：空标签主机名（连续点号）须拒，前后端同口径。
		"host-empty-label":       "vless://" + manualUUID + "@a..b:443#bad",
		"host-empty-label-3dots": "vless://" + manualUUID + "@a...b:443#bad",
		// sweep §3：导入路径 SNI / WS Host 空标签须拒——导入路径此前
		// 根本不校验这两字段格式（自建面 normalizeSecurity /
		// normalizeTransport 早已拒）。
		"sni-empty-label": "vless://" + manualUUID +
			"@203.0.113.10:443?sni=a..b&fp=chrome&type=tcp&encryption=none&security=tls#bad",
		"sni-empty-label-3dots": "vless://" + manualUUID +
			"@203.0.113.10:443?sni=a...b&fp=chrome&type=tcp&encryption=none&security=tls#bad",
		"ws-host-empty-label": "vless://" + manualUUID +
			"@203.0.113.10:443?sni=example.com&fp=chrome&type=ws&path=%2Fws&host=a..b&encryption=none&security=tls#bad",
		"ws-host-empty-label-3dots": "vless://" + manualUUID +
			"@203.0.113.10:443?sni=example.com&fp=chrome&type=ws&path=%2Fws&host=a...b&encryption=none&security=tls#bad",
	}
	for kind, link := range bad {
		nodes, errs := ParseImport(link)
		if len(nodes) != 0 || len(errs) == 0 {
			t.Errorf("%s: ParseImport = %d nodes, errs=%v, want 0 nodes with error", kind, len(nodes), errs)
		}
	}
}

// TestValidateImportHostEmptyLabelMatrix：T-01 空标签主机名 12 形态矩阵
// （再审计 lane-2 核查表原样）。仅标签内连续点号（a..b / a...b）是
// 本轮补的洞；其余形态早已被拒，矩阵钉死“修后仍同判”，防过修与回归。
func TestValidateImportHostEmptyLabelMatrix(t *testing.T) {
	cases := []struct {
		host    string
		wantErr bool
	}{
		{"a..b", true},
		{"a...b", true},
		{".a", true},
		{"a.", true},
		{".", true},
		{"..", true},
		{"a..", true},
		{"..b", true},
		{"a.b.", true},
		{".a.b", true},
		{"example.com", false},
		{"a.b", false},
	}
	for _, c := range cases {
		err := ValidateImportHost(c.host)
		if c.wantErr && err == nil {
			t.Errorf("ValidateImportHost(%q) = nil, want error", c.host)
			continue
		}
		if !c.wantErr && err != nil {
			t.Errorf("ValidateImportHost(%q) = %v, want nil", c.host, err)
			continue
		}
		if c.wantErr && err.Error() != fmt.Sprintf("地址 %q 不正确", c.host) {
			t.Errorf("ValidateImportHost(%q) = %q, want %q", c.host, err.Error(), fmt.Sprintf("地址 %q 不正确", c.host))
		}
	}
}

// TestValidateImportHostEmbeddedLoopbackMessage：R1-F-01 翻译内嵌特殊
// 地址（NAT64/6to4 内嵌 127/8 回环、内嵌 0.0.0.0 未指定）的拒错文案
// 须与前端同口径。内嵌地址确是回环/未指定，“本机”文案比后端原先的
// 云元数据桶文案更准确，故以后端改就前端为准；裁决（拒绝）本身不变。
// 内嵌 0.0.0.0 两例是 F-03 全族 sweep 时发现的同根残留，一并覆盖。
func TestValidateImportHostEmbeddedLoopbackMessage(t *testing.T) {
	for _, host := range []string{"64:ff9b::127.0.0.1", "2002:7f00:1::", "64:ff9b::0", "2002::"} {
		err := ValidateImportHost(host)
		if err == nil {
			t.Errorf("ValidateImportHost(%q) = nil, want rejection", host)
			continue
		}
		if err.Error() != "地址不能是本机或 0.0.0.0" {
			t.Errorf("ValidateImportHost(%q) = %q, want %q", host, err.Error(), "地址不能是本机或 0.0.0.0")
		}
	}
}

// TestT01EmptyLabelTransportHostAndSNI：T-01 同一 hostPattern 还守着
// 传输 Host（model.go:390）与 SNI（model.go:630），共用 helper 须
// 顺带覆盖这两处；文案各复用本字段原有文案，不改。
func TestT01EmptyLabelTransportHostAndSNI(t *testing.T) {
	if err := normalizeTransport(&Transport{Type: TransportWS, Host: "a..b", Path: "/ws"}); err == nil || err.Error() != "Host 格式错误：\"a..b\"" {
		t.Errorf("normalizeTransport(Host=a..b) = %v, want %q", err, "Host 格式错误：\"a..b\"")
	}
	if err := normalizeTransport(&Transport{Type: TransportWS, Host: "example.com", Path: "/ws"}); err != nil {
		t.Errorf("normalizeTransport(Host=example.com) = %v, want nil", err)
	}
	if err := normalizeSecurity(&Inbound{Security: Security{Type: SecurityTLS, SNI: "a..b"}}); err == nil || err.Error() != "SNI 格式错误：\"a..b\"" {
		t.Errorf("normalizeSecurity(SNI=a..b) = %v, want %q", err, "SNI 格式错误：\"a..b\"")
	}
	if err := normalizeSecurity(&Inbound{Security: Security{Type: SecurityNone, SNI: "example.com"}}); err != nil {
		t.Errorf("normalizeSecurity(SNI=example.com) = %v, want nil", err)
	}
}

// TestImportHostnamesEmptyLabel：sweep §3——导入路径的 TLS SNI 与传输
// Host 空标签须拒。finishProxy 是四格式共同收口，在此一处按
// HasEmptyLabel 校验 SNI（servername/sni 两键都认）与各传输 Host
// （ws-opts / http-opts / h2-opts / xhttp-opts），文案各复用自建面
// 原有文案（"SNI 格式错误" / "Host 格式错误"，F-LIVE-01 起附引坏值原文）。
func TestImportHostnamesEmptyLabel(t *testing.T) {
	// SNI：各协议的 SNI 键（vless/vmess 存 servername，trojan/hy2/
	// tuic/anytls 存 sni）都要拒空标签。
	sniBad := map[string]string{
		"vless-tls": "vless://" + manualUUID +
			"@203.0.113.10:443?sni=a..b&fp=chrome&type=tcp&encryption=none&security=tls#bad",
		"trojan-tls": "trojan://secretpass@203.0.113.10:443?sni=a..b&fp=chrome&type=tcp&security=tls#bad",
		"hy2":        "hysteria2://secretpass@203.0.113.10:443/?sni=a..b#bad",
		"tuic":       "tuic://" + manualUUID + ":secretpass@203.0.113.10:443?sni=a..b&alpn=h3#bad",
		"anytls":     "anytls://secretpass@203.0.113.10:443/?sni=a..b&fp=chrome#bad",
	}
	for kind, link := range sniBad {
		nodes, errs := ParseImport(link)
		if len(nodes) != 0 || len(errs) == 0 {
			t.Errorf("%s: ParseImport = %d nodes, errs=%v, want 0 nodes with error", kind, len(nodes), errs)
			continue
		}
		if !strings.Contains(errs[0], "SNI 格式错误") {
			t.Errorf("%s: err = %q, want %q", kind, errs[0], "SNI 格式错误")
		}
	}
	// VMess SNI 走 JSON 解析，单独构造。
	vmessJSON := `{"v":"2","ps":"bad","add":"203.0.113.10","port":"443","id":"` + manualUUID +
		`","aid":"0","scy":"auto","net":"ws","type":"none","host":"example.com","path":"/ws","tls":"tls","sni":"a..b","fp":"chrome"}`
	vmessLink := "vmess://" + base64.StdEncoding.EncodeToString([]byte(vmessJSON)) + "#bad"
	if nodes, errs := ParseImport(vmessLink); len(nodes) != 0 || len(errs) == 0 || !strings.Contains(errs[0], "SNI 格式错误") {
		t.Errorf("vmess-tls: ParseImport = %d nodes, errs=%v, want 0 nodes with %q", len(nodes), errs, "SNI 格式错误")
	}
	// 传输 Host：ws / httpupgrade / h2 / xhttp / tcp-http 五路都要拒
	// 空标签；SNI 均为合法 example.com，只测 Host 这一格。
	hostBad := map[string]string{
		"ws": "vless://" + manualUUID +
			"@203.0.113.10:443?sni=example.com&fp=chrome&type=ws&path=%2Fws&host=a..b&encryption=none&security=tls#bad",
		"httpupgrade": "vless://" + manualUUID +
			"@203.0.113.10:443?sni=example.com&fp=chrome&type=httpupgrade&path=%2Fws&host=a..b&encryption=none&security=tls#bad",
		"h2": "vless://" + manualUUID +
			"@203.0.113.10:443?sni=example.com&fp=chrome&type=h2&host=a..b&encryption=none&security=tls#bad",
		"xhttp": "vless://" + manualUUID +
			"@203.0.113.10:443?sni=example.com&fp=chrome&type=xhttp&host=a..b&encryption=none&security=tls#bad",
		"tcp-http": "vless://" + manualUUID +
			"@203.0.113.10:443?sni=example.com&fp=chrome&type=tcp&headerType=http&host=a..b&encryption=none&security=tls#bad",
	}
	for kind, link := range hostBad {
		nodes, errs := ParseImport(link)
		if len(nodes) != 0 || len(errs) == 0 {
			t.Errorf("%s: ParseImport = %d nodes, errs=%v, want 0 nodes with error", kind, len(nodes), errs)
			continue
		}
		if !strings.Contains(errs[0], "Host 格式错误") {
			t.Errorf("%s: err = %q, want %q", kind, errs[0], "Host 格式错误")
		}
	}
	// 合法对照：SNI 域名/IP、WS Host 域名均须放行。
	good := []string{
		"vless://" + manualUUID + "@203.0.113.10:443?sni=example.com&fp=chrome&type=tcp&encryption=none&security=tls#good",
		"vless://" + manualUUID + "@203.0.113.10:443?sni=203.0.113.20&fp=chrome&type=tcp&encryption=none&security=tls#good",
		"vless://" + manualUUID + "@203.0.113.10:443?sni=example.com&fp=chrome&type=ws&path=%2Fws&host=example.com&encryption=none&security=tls#good",
	}
	for _, link := range good {
		nodes, errs := ParseImport(link)
		if len(nodes) != 1 || len(errs) != 0 {
			t.Errorf("good link: ParseImport = %d nodes, errs=%v, want 1 node", len(nodes), errs)
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
