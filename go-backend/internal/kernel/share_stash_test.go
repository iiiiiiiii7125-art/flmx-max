package kernel

import (
	"strings"
	"testing"
)

// DA-7（1.4.0 移植上游 3.3.31）：Stash 从「别名 Clash」升为一等格式。
// 断言面：
//   - UA 分发：Stash UA → FormatStash（不再落 Clash）；
//   - 规则开启时 Stash 的规则本体与 Mihomo 逐行一致，但规则集序列化
//     为 YAML（Stash 读不了 .mrs），且不带 Mihomo 专属 DNS/嗅探段；
//   - 节点携带面与 Clash 恒等（同一 ClashProxy 门控）：含 ENC VLESS、
//     Snell v4/v5 进、v6 不进；计数 SubscriptionNodeCount 两格式恒等
//     （X-P3-2 计数/实得错位的反向断言）；
//   - 规则关闭时 Stash 与 Clash 输出逐字节一致；
//   - 链式：Stash 输出含落地/中转组与 dialer-proxy（组闭合）。

func TestFormatForUserAgentStash(t *testing.T) {
	for _, ua := range []string{"Stash/3.1.0", "stash", "STASH", "Mozilla/5.0 Stash"} {
		if got := FormatForUserAgent(ua); got != FormatStash {
			t.Fatalf("UA %q -> %q, want %q", ua, got, FormatStash)
		}
	}
	if got := FormatForUserAgent("ClashMeta/1.0"); got != FormatClash {
		t.Fatalf("clash UA -> %q, want %q", got, FormatClash)
	}
}

// rulesSection 取出渲染结果的 rules 段（profile 最后一节）做跨格式对齐。
func rulesSection(t *testing.T, body string) string {
	t.Helper()
	i := strings.LastIndex(body, "\nrules:")
	if i < 0 {
		t.Fatalf("no rules section in:\n%s", body)
	}
	return body[i:]
}

func TestStashRulesYAMLAndBodyParity(t *testing.T) {
	eps := shareFixtures(t)
	opts := SubOptions{Rules: true, RuleGroups: []string{"adblock", "youtube"}}

	clash, _, err := Subscription(FormatClash, eps, opts)
	if err != nil {
		t.Fatal(err)
	}
	stash, _, err := Subscription(FormatStash, eps, opts)
	if err != nil {
		t.Fatal(err)
	}

	// 对照组：Mihomo 侧必须是 .mrs + 规则设置段，证明夹具确实开了规则。
	if !strings.Contains(clash, ".mrs") || !strings.Contains(clash, "fake-ip-range") {
		t.Fatalf("clash control must carry .mrs sets and rule settings:\n%s", clash)
	}
	// Stash：规则集 YAML 化，全文无 mrs 字样。
	if strings.Contains(stash, "mrs") {
		t.Fatalf("stash output must not mention mrs:\n%s", stash)
	}
	if !strings.Contains(stash, `format: "yaml"`) || !strings.Contains(stash, ".yaml") {
		t.Fatalf("stash rule providers must be yaml:\n%s", stash)
	}
	// Stash：不带 Mihomo 专属的 DNS/嗅探设置段。
	for _, bad := range []string{"fake-ip", "sniffer", "enhanced-mode"} {
		if strings.Contains(stash, bad) {
			t.Fatalf("stash output must leave out Mihomo-only setting %q:\n%s", bad, stash)
		}
	}
	// 规则本体与 Mihomo 逐行一致（providers 段不同，rules 段必须相同）。
	if got, want := rulesSection(t, stash), rulesSection(t, clash); got != want {
		t.Fatalf("stash rules body differs from clash:\nstash:\n%s\nclash:\n%s", got, want)
	}
	// 组闭合：节点选择组在 Stash 输出中存在（规则落点不悬空）。
	if !strings.Contains(stash, "节点选择") {
		t.Fatalf("stash output lacks the node-select group:\n%s", stash)
	}
}

func TestStashRulesOffByteIdenticalToClash(t *testing.T) {
	eps := shareFixtures(t)
	clash, _, err := Subscription(FormatClash, eps, SubOptions{})
	if err != nil {
		t.Fatal(err)
	}
	stash, _, err := Subscription(FormatStash, eps, SubOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if stash != clash {
		t.Fatalf("rules off: stash must equal clash byte for byte\nstash:\n%s\nclash:\n%s", stash, clash)
	}
}

func stashMatrixEndpoints(t *testing.T) []Endpoint {
	t.Helper()
	secret, err := GenerateEncryptionSecret(EncryptionAuthX25519)
	if err != nil {
		t.Fatal(err)
	}
	enc := Inbound{ID: 77, Protocol: ProtocolVLESS, Port: 443,
		Settings: Settings{UUID: testUUID, EncryptionAuth: EncryptionAuthX25519, EncryptionMode: EncryptionModeNative, EncryptionSecret: secret},
		Security: Security{Type: SecurityNone}}
	enc.Transport.Type = TransportTCP
	return []Endpoint{
		{Name: "普通 Trojan", Host: "203.0.113.30", Port: 8443, Direct: true, Inbound: Inbound{ID: 71, Protocol: ProtocolTrojan, Port: 8443,
			Settings: Settings{Password: "tpw"}, Security: Security{Type: SecurityTLS, SNI: "t.example.com"}}},
		{Name: "ENC 节点", Host: "203.0.113.20", Port: 443, Direct: true, Inbound: enc},
		g4SnellEndpoint(t, 5, false),
		g4SnellEndpoint(t, 6, false),
	}
}

func TestStashNodeSetAndCountParity(t *testing.T) {
	eps := stashMatrixEndpoints(t)
	// g4SnellEndpoint 的 Name 固定为 "G4 Snell"，v5/v6 同名会被
	// uniqueNames 去重——改名以便逐个断言携带面。
	eps[2].Name = "Snell v5 节点"
	eps[3].Name = "Snell v6 节点"

	countClash := SubscriptionNodeCount(FormatClash, eps, SubOptions{})
	countStash := SubscriptionNodeCount(FormatStash, eps, SubOptions{})
	if countStash != countClash {
		t.Fatalf("node count stash=%d, want clash=%d (X-P3-2 parity)", countStash, countClash)
	}
	if countClash != 3 {
		t.Fatalf("fixture must render 3 nodes for clash family (trojan+enc+snell v5), got %d", countClash)
	}

	stash, _, err := Subscription(FormatStash, eps, SubOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"普通 Trojan", "ENC 节点", "Snell v5 节点", "encryption"} {
		if !strings.Contains(stash, want) {
			t.Fatalf("stash output missing %q:\n%s", want, stash)
		}
	}
	if strings.Contains(stash, "Snell v6 节点") {
		t.Fatalf("snell v6 must stay out of stash (no client support):\n%s", stash)
	}
}

func TestStashChainGroups(t *testing.T) {
	eps := []Endpoint{
		{Name: "落地A", Host: "203.0.113.40", Port: 8443, Direct: true, Inbound: Inbound{ID: 81, Protocol: ProtocolTrojan, Port: 8443,
			Settings: Settings{Password: "lpw"}, Security: Security{Type: SecurityTLS, SNI: "l.example.com"}}},
		{Name: "中转B", Host: "203.0.113.41", Port: 8443, Direct: true, Inbound: Inbound{ID: 82, Protocol: ProtocolTrojan, Port: 8443,
			Settings: Settings{Password: "rpw"}, Security: Security{Type: SecurityTLS, SNI: "r.example.com"}}},
	}
	opts := SubOptions{ChainLandingNames: []string{"落地A"}, ChainRelayNames: []string{"中转B"}}
	stash, _, err := Subscription(FormatStash, eps, opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{ChainLandingGroup, ChainRelayGroup, "dialer-proxy"} {
		if !strings.Contains(stash, want) {
			t.Fatalf("stash chain output missing %q:\n%s", want, stash)
		}
	}
	if got, want := SubscriptionNodeCount(FormatStash, eps, opts), SubscriptionNodeCount(FormatClash, eps, opts); got != want {
		t.Fatalf("chain node count stash=%d, want clash=%d", got, want)
	}
}
