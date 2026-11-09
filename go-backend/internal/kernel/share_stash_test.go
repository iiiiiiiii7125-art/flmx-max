package kernel

import (
	"strings"
	"testing"
)

// DA-7（1.4.0 移植上游 3.3.31）：Stash 从「别名 Clash」升为一等格式。
// 断言面：
//   - UA 分发：Stash UA → FormatStash（不再落 Clash）；
//   - 节点携带面与 Clash 的门控差异（同一 ClashProxy 门 + Stash 例外）：
//     含 ENC VLESS、Snell v4/v5 进、v6 不进；计数
//     SubscriptionNodeCount 两格式按各自携带面计
//     （X-P3-2 计数/实得错位的反向断言）；
//   - 零选项时 Stash 与 Clash 输出逐字节一致。
// （2.1.2：原规则开启格与链式格随订阅规则/链式删除同去。）

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

func TestStashPlainByteIdenticalToClash(t *testing.T) {
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
		t.Fatalf("plain: stash must equal clash byte for byte\nstash:\n%s\nclash:\n%s", stash, clash)
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
		{Name: "G4 Snell", Host: "snell.example.com", Port: 6160, Direct: true, Proxy: Proxy{"type": "snell", "server": "snell.example.com", "port": 6160, "psk": "psk-g4-test-123456", "version": 5}},
		{Name: "G4 Snell", Host: "snell.example.com", Port: 6160, Direct: true, Proxy: Proxy{"type": "snell", "server": "snell.example.com", "port": 6160, "psk": "psk-g4-test-123456", "version": 6}},
	}
}

func TestStashNodeSetAndCountParity(t *testing.T) {
	eps := stashMatrixEndpoints(t)
	// 两个导入 Snell 端点同名 "G4 Snell"，v5/v6 同名会被
	// uniqueNames 去重——改名以便逐个断言携带面。
	eps[2].Name = "Snell v5 节点"
	eps[3].Name = "Snell v6 节点"

	// X-P3-2 的 stash=clash 计数平价在导入 Snell 格分叉：
	// clash 不下发导入 Snell v5/v6 得 2，stash 保留 v5 得 3。
	countClash := SubscriptionNodeCount(FormatClash, eps, SubOptions{})
	countStash := SubscriptionNodeCount(FormatStash, eps, SubOptions{})
	if countStash != 3 {
		t.Fatalf("node count stash=%d, want 3 (trojan+enc+snell v5)", countStash)
	}
	if countClash != 2 {
		t.Fatalf("node count clash=%d, want 2 (imported snell out)", countClash)
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

	clash, _, err := Subscription(FormatClash, eps, SubOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"普通 Trojan", "ENC 节点", "encryption"} {
		if !strings.Contains(clash, want) {
			t.Fatalf("clash output missing %q:\n%s", want, clash)
		}
	}
	if strings.Contains(clash, "Snell v5 节点") || strings.Contains(clash, "Snell v6 节点") {
		t.Fatalf("clash output must not carry imported snell v5/v6 nodes:\n%s", clash)
	}
}
