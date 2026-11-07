package handler

import (
	"reflect"
	"testing"
)

// FIX-K (K1-01/K1-02/K1-05) 回归门：KCP 段密钥按作用域派生、两端
// 同源、非公开默认常量；参数单一真源；段协议回落两面一致。

const fixkTestSecret = "fixk-test-jwt-secret-0123456789abcdef"

func fixkKCPMetadata(t *testing.T, cfg map[string]interface{}) map[string]interface{} {
	t.Helper()
	md, ok := cfg["metadata"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected metadata map, got %v", cfg)
	}
	return md
}

func TestFixKKCPKeyForDerivation(t *testing.T) {
	k1 := KCPKeyFor(fixkTestSecret, 42)
	if k1 == "" {
		t.Fatal("derived key must not be empty")
	}
	// 同作用域重复派生恒定（同隧道重下发不换钥）。
	if again := KCPKeyFor(fixkTestSecret, 42); again != k1 {
		t.Fatalf("same scope must derive the same key: %q vs %q", k1, again)
	}
	// 不同作用域不同钥。
	if other := KCPKeyFor(fixkTestSecret, 43); other == k1 {
		t.Fatal("different scopes must derive different keys")
	}
	// 不同面板密钥不同钥。
	if other := KCPKeyFor("another-secret", 42); other == k1 {
		t.Fatal("different panel secrets must derive different keys")
	}
	// 绝不是 kcp-go 的公开默认常量，也不是其盐。
	if k1 == "it's a secrect" || k1 == "kcp-go" {
		t.Fatal("derived key must not be the public kcp-go default")
	}
	// 缺失输入不派生（调用方据此拒绝生成，不回落默认钥）。
	if KCPKeyFor("", 42) != "" || KCPKeyFor(fixkTestSecret, 0) != "" || KCPKeyFor(fixkTestSecret, -1) != "" {
		t.Fatal("empty secret / non-positive scope must not derive a key")
	}
}

func TestFixKKCPBuildersCarryDerivedKey(t *testing.T) {
	h := &Handler{jwtSecret: fixkTestSecret}
	key, err := h.tunnelKCPKey(42, "tls", "kcp")
	if err != nil || key == "" {
		t.Fatalf("tunnelKCPKey: key=%q err=%v", key, err)
	}
	dialerMD := fixkKCPMetadata(t, buildTunnelDialerConfig("kcp", key, kcpTierSmall, false))
	listenerMD := fixkKCPMetadata(t, buildTunnelListenerConfig("kcp", key, kcpTierSmall, false))
	// 两端逐字同源（整份 metadata 相等，含 key/crypt）。
	if !reflect.DeepEqual(dialerMD, listenerMD) {
		t.Fatalf("dialer/listener metadata drift:\n dialer=%v\n listener=%v", dialerMD, listenerMD)
	}
	if dialerMD["kcp.key"] != key {
		t.Fatalf("kcp.key not delivered: %v", dialerMD["kcp.key"])
	}
	if dialerMD["kcp.key"] == "it's a secrect" {
		t.Fatal("kcp.key must not be the public default constant")
	}
	if dialerMD["kcp.crypt"] != "aes-256" {
		t.Fatalf("kcp.crypt must be aes-256, got %v", dialerMD["kcp.crypt"])
	}
	// K1-02 参数面单一真源；窗口/缓冲值口径已由 FIX-KCPMEM
	// （P1-04）接管：无内存声明的 Handler 恒走 small 保守缺省
	// 档（512/1MiB/1MiB/512KiB），large 档与旧恒定值逐字相同
	// 的断言在 fixkcpmem 测试中；FEC 缺省仍 10/3 不变（P1-05）。
	for k, want := range map[string]interface{}{
		"kcp.mode": "fast3", "kcp.sndwnd": 512, "kcp.rcvwnd": 512,
		"kcp.mtu": 1350, "kcp.datashard": 10, "kcp.parityshard": 3,
	} {
		if dialerMD[k] != want {
			t.Fatalf("param %s changed: got %v want %v", k, dialerMD[k], want)
		}
	}
	// 不同隧道不同钥。
	key43, err := h.tunnelKCPKey(43, "tls", "kcp")
	if err != nil || key43 == "" || key43 == key {
		t.Fatalf("tunnel 43 key must differ: %q err=%v", key43, err)
	}
	// 非 KCP 隧道不需要钥、不报错。
	if k, err := h.tunnelKCPKey(44, "tls", "tls", "ws"); err != nil || k != "" {
		t.Fatalf("non-kcp tunnel must not need a key: %q err=%v", k, err)
	}
	// 隧道自身协议为 kcp（段行未逐段标注）同样需要钥。
	if k, err := h.tunnelKCPKey(45, "kcp"); err != nil || k == "" {
		t.Fatalf("tunnel-level kcp must need a key: %q err=%v", k, err)
	}
}

func TestFixKKCPKeyDerivationFailureRejects(t *testing.T) {
	h := &Handler{} // jwtSecret 缺失
	if _, err := h.tunnelKCPKey(42, "tls", "kcp"); err == nil {
		t.Fatal("KCP tunnel without panel secret must fail, not fall back to the default key")
	}
	if _, err := federationKCPKey("", 7, "kcp"); err == nil {
		t.Fatal("federation KCP without secret must fail")
	}
	if _, err := federationKCPKey(fixkTestSecret, 0, "kcp"); err == nil {
		t.Fatal("federation KCP without share scope must fail")
	}
	// 非 KCP 联邦腿不派生、不报错。
	if k, err := federationKCPKey("", 0, "tls"); err != nil || k != "" {
		t.Fatalf("non-kcp federation leg must not need a key: %q err=%v", k, err)
	}
}

func TestFixKFederationKCPSameSource(t *testing.T) {
	// 消费方中继链 builder 与提供方角色服务 builder 用同一
	// (secret, shareID) 派生——两面 metadata 必须逐字同源。
	chain, err := buildFederationMiddleChainConfig("fed_chain_1", 7, "tls", "round",
		[]federationRuntimeTarget{{Host: "10.0.0.2", Port: 24001, Protocol: "kcp"}}, "", fixkTestSecret, 7, false)
	if err != nil {
		t.Fatalf("build federation middle chain: %v", err)
	}
	hops := chain["hops"].([]map[string]interface{})
	nodes := hops[0]["nodes"].([]map[string]interface{})
	dialerMD := fixkKCPMetadata(t, nodes[0]["dialer"].(map[string]interface{}))

	svcKey, err := federationKCPKey(fixkTestSecret, 7, "kcp")
	if err != nil {
		t.Fatalf("federationKCPKey: %v", err)
	}
	svc := buildFederationServiceConfig("fed_svc_1", "192.0.2.2:24002", "kcp", "exit", "", 1, "", svcKey)
	listenerMD := fixkKCPMetadata(t, svc["listener"].(map[string]interface{}))
	if !reflect.DeepEqual(dialerMD, listenerMD) {
		t.Fatalf("federation dialer/listener metadata drift:\n dialer=%v\n listener=%v", dialerMD, listenerMD)
	}
	if dialerMD["kcp.key"] != KCPKeyFor(fixkTestSecret, 7) {
		t.Fatalf("federation key must be the share-scoped derivation, got %v", dialerMD["kcp.key"])
	}
	// 中继链含 KCP 目标而密钥作用域缺失 → 整条链拒绝生成。
	if _, err := buildFederationMiddleChainConfig("fed_chain_1", 7, "tls", "round",
		[]federationRuntimeTarget{{Host: "10.0.0.2", Port: 24001, Protocol: "kcp"}}, "", "", 0, false); err == nil {
		t.Fatal("KCP federation chain without key scope must be rejected")
	}
}

func TestFixKSegmentProtocolFallbackAligned(t *testing.T) {
	// K1-05：生成面回落（effectiveSegmentProtocol）与质量探针面
	// 回落（chainSegmentProtocol）对全部输入组合逐字一致——尤
	// 其是空段行 + kcp 隧道这一历史分歧形态。
	cases := [][2]string{
		{"", "kcp"}, {"", "tls"}, {"", ""}, {"kcp", "tls"},
		{"tls", "kcp"}, {" KCP ", "tls"}, {"udp", ""},
	}
	for _, c := range cases {
		gen := effectiveSegmentProtocol(c[0], c[1])
		probe := chainSegmentProtocol(c[0], c[1])
		if gen != probe {
			t.Fatalf("fallback drift for segment=%q tunnel=%q: generation=%q probe=%q", c[0], c[1], gen, probe)
		}
	}
	if got := effectiveSegmentProtocol("", "kcp"); got != "kcp" {
		t.Fatalf("empty segment on kcp tunnel must generate a kcp leg, got %q", got)
	}
}
