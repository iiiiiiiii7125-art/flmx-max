package kernel

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// A5-01: sing-box 配置导入缺 Snell / NaïveProxy 类型映射——面板自身为
// sing-box 1.14+ 客户端生成的订阅（含 snell v6、naive）无法回导。
func TestImportSingboxSnellNaive(t *testing.T) {
	cfg := `{"outbounds":[
	  {"type":"selector","tag":"select","outbounds":["s4"]},
	  {"type":"snell","tag":"s4","server":"1.2.3.4","server_port":6160,"version":4,"psk":"psk-aaaa"},
	  {"type":"snell","tag":"s6","server":"1.2.3.5","server_port":6161,"version":6,"psk":"psk-bbbb"},
	  {"type":"snell","tag":"s6s","server_port":0,"version":6,"psk":"psk-cccc","detour":"s6s-stls"},
	  {"type":"shadowtls","tag":"s6s-stls","server":"9.9.9.9","server_port":443,"version":3,"password":"stls-pw","tls":{"enabled":true,"server_name":"www.bing.com"}},
	  {"type":"naive","tag":"nv","server":"5.6.7.8","server_port":443,"username":"u1","password":"p1","tls":{"enabled":true,"server_name":"nv.example.com"}}
	]}`
	nodes, errs := ParseImport(cfg)
	if len(errs) != 0 {
		t.Fatalf("errs: %v", errs)
	}
	if len(nodes) != 4 {
		t.Fatalf("want 4 nodes, got %d: %+v", len(nodes), nodes)
	}
	byName := map[string]ImportedNode{}
	for _, n := range nodes {
		byName[n.Name] = n
	}
	s4 := byName["s4"]
	if s4.Proxy.Type() != "snell" || s4.Proxy.str("psk") != "psk-aaaa" || s4.Proxy.num("version") != 4 {
		t.Fatalf("snell v4: %+v", s4.Proxy)
	}
	s6 := byName["s6"]
	if s6.Proxy.Type() != "snell" || s6.Proxy.num("version") != 6 {
		t.Fatalf("snell v6: %+v", s6.Proxy)
	}
	s6s := byName["s6s"]
	if s6s.Host != "9.9.9.9" || s6s.Port != 443 || s6s.Proxy.sub("shadow-tls").str("password") != "stls-pw" ||
		s6s.Proxy.sub("shadow-tls").str("host") != "www.bing.com" {
		t.Fatalf("snell over shadowtls must take the detour address: %+v", s6s)
	}
	nv := byName["nv"]
	if nv.Proxy.Type() != "naive" || nv.Proxy.str("username") != "u1" || nv.Proxy.str("password") != "p1" {
		t.Fatalf("naive: %+v", nv.Proxy)
	}
	// 回导后再渲染：snell v6 在 sing-box 口径保留，naive 能回到 sing-box，
	// 且 naive 绝不进 Clash（Mihomo 无 Naïve 客户端，会毒化整份配置）。
	eps := []Endpoint{}
	for _, n := range nodes {
		eps = append(eps, Endpoint{Name: n.Name, Host: n.Host, Port: n.Port, Proxy: n.Proxy, Direct: true})
	}
	sb, _, err := Subscription(FormatSingbox, eps, SubOptions{SingboxSnell: true})
	if err != nil {
		t.Fatal(err)
	}
	var rendered struct {
		Outbounds []map[string]interface{} `json:"outbounds"`
	}
	if err := json.Unmarshal([]byte(sb), &rendered); err != nil {
		t.Fatal(err)
	}
	types := map[string]bool{}
	for _, o := range rendered.Outbounds {
		types[Proxy(o).str("type")+":"+Proxy(o).str("tag")] = true
	}
	if !types["snell:s6"] || !types["naive:nv"] {
		t.Fatalf("round trip lost nodes: %v", types)
	}
	clash := clashProfile(eps, false, SubOptions{}, false)
	if strings.Contains(clash, "type: naive") || strings.Contains(clash, "type: \"naive\"") {
		t.Fatalf("naive must not render into Clash profiles:\n%s", clash)
	}
}

// A5-01 round-trip：面板自产的 sing-box 订阅（自建 Snell v6 + Naïve 入站）
// 经 ParseImport 回导，节点数不减。
func TestImportSingboxOwnProfileRoundTrip(t *testing.T) {
	ins := []Inbound{
		{ID: 1, Protocol: ProtocolSnell, Port: 6161, Settings: Settings{Password: "psk-bbbbbbbbbbbb", SnellVersion: 6}},
		{ID: 2, Protocol: ProtocolNaive, Port: 443, Settings: Settings{Username: "nu", Password: "naive-pass-1"},
			Security: Security{Type: SecurityTLS, CertMode: CertModeACME, SNI: "nv.example.com", ACMEEmail: "me@example.com"}},
	}
	eps := []Endpoint{}
	for _, in := range ins {
		if err := Normalize(TypeSingbox, &in); err != nil {
			t.Fatal(err)
		}
		eps = append(eps, Endpoint{Name: "n" + in.Protocol, Host: "203.0.113.9", Port: in.Port, Inbound: in})
	}
	body, _, err := Subscription(FormatSingbox, eps, SubOptions{SingboxSnell: true})
	if err != nil {
		t.Fatal(err)
	}
	nodes, errs := ParseImport(body)
	if len(errs) != 0 || len(nodes) != 2 {
		t.Fatalf("own sing-box profile must re-import fully: nodes=%v errs=%v\n%s", nodes, errs, body)
	}
}

// D4-07：Clash 条目级容错——单个条目内的重复键 / 标量条目只丢该条目，
// 不再整单 0 节点；错误进回执并指名条目序号。
func TestImportClashEntryLevelTolerance(t *testing.T) {
	dup := `
proxies:
  - name: GOOD
    type: ss
    server: 1.2.3.4
    port: 8388
    cipher: aes-128-gcm
    password: p1
  - name: DUPKEY
    type: ss
    server: 1.2.3.5
    port: 8388
    port: 8389
    cipher: aes-128-gcm
    password: p2
`
	nodes, errs := ParseImport(dup)
	if len(nodes) != 1 || nodes[0].Name != "GOOD" {
		t.Fatalf("duplicate key in one entry must not sink the whole profile: nodes=%v errs=%v", nodes, errs)
	}
	if len(errs) != 1 || !strings.Contains(errs[0], "第 2 个节点") {
		t.Fatalf("errs must name the bad entry: %v", errs)
	}

	scalar := `
proxies:
  - "just-a-string"
  - {name: GOOD2, type: trojan, server: 5.6.7.8, port: 443, password: pw}
`
	nodes, errs = ParseImport(scalar)
	if len(nodes) != 1 || nodes[0].Name != "GOOD2" {
		t.Fatalf("scalar entry must be skipped, not sink the profile: nodes=%v errs=%v", nodes, errs)
	}
	if len(errs) != 1 || !strings.Contains(errs[0], "第 1 个节点") {
		t.Fatalf("errs must name the bad entry: %v", errs)
	}
}

// D4-08：解析器数值/类型静默降级族——不确定即拒绝该条目并回执。
func TestImportRejectsInexactNumbersAndBadBlocks(t *testing.T) {
	// ① sing-box 小数端口被截断成 8388 静默导入 → 必须拒绝该条目。
	cfg := `{"outbounds":[
	  {"type":"shadowsocks","tag":"frac","server":"1.2.3.4","server_port":8388.9,"method":"aes-128-gcm","password":"p"},
	  {"type":"shadowsocks","tag":"good","server":"1.2.3.5","server_port":8388,"method":"aes-128-gcm","password":"p"}
	]}`
	nodes, errs := ParseImport(cfg)
	if len(nodes) != 1 || nodes[0].Name != "good" || len(errs) != 1 {
		t.Fatalf("fractional port must reject only that entry: nodes=%v errs=%v", nodes, errs)
	}

	// ② vmess aid 溢出（1e20）→ 垃圾 alterId 入库 → 必须拒绝。
	v := map[string]interface{}{"v": "2", "ps": "big-aid", "add": "1.2.3.4", "port": "443",
		"id": "11111111-2222-3333-4444-555555555555", "aid": json.Number("1e20"), "net": "tcp"}
	raw, _ := json.Marshal(v)
	link := "vmess://" + base64.StdEncoding.EncodeToString(raw)
	if nodes, errs := ParseImport(link); len(nodes) != 0 || len(errs) != 1 {
		t.Fatalf("overflowing aid must be rejected: nodes=%v errs=%v", nodes, errs)
	}
	// sing-box 侧同型：alter_id 溢出同样拒绝。
	cfg = `{"outbounds":[
	  {"type":"vmess","tag":"big","server":"1.2.3.4","server_port":443,"uuid":"11111111-2222-3333-4444-555555555555","alter_id":1e20}
	]}`
	if nodes, errs := ParseImport(cfg); len(nodes) != 0 || len(errs) != 1 {
		t.Fatalf("overflowing alter_id must be rejected: nodes=%v errs=%v", nodes, errs)
	}

	// ③ sing-box tls 块类型错位（字符串而非对象）被静默吞掉 → 必须报错。
	cfg = `{"outbounds":[
	  {"type":"trojan","tag":"badtls","server":"1.2.3.4","server_port":443,"password":"pw","tls":"yes"}
	]}`
	if nodes, errs := ParseImport(cfg); len(nodes) != 0 || len(errs) != 1 {
		t.Fatalf("non-object tls block must be an error: nodes=%v errs=%v", nodes, errs)
	}
}

// A5-07：Clash YAML 导入的 Snell 缺 version 时补默认 4（与行格式同口径），
// 否则再导出时整键缺失、客户端缺省语义不明。
func TestImportClashSnellDefaultVersion(t *testing.T) {
	clash := `
proxies:
  - {name: SN, type: snell, server: 1.2.3.4, port: 6160, psk: abc}
`
	nodes, errs := ParseImport(clash)
	if len(errs) != 0 || len(nodes) != 1 {
		t.Fatalf("nodes=%v errs=%v", nodes, errs)
	}
	if got := nodes[0].Proxy.num("version"); got != 4 {
		t.Fatalf("clash snell without version must default to 4, got %d (%v)", got, nodes[0].Proxy)
	}
}
