package handler

import (
	"net"
	"testing"
)

// FLMX 改名过渡退场（1.9.10）：FLVX_* 旧名双读已经摘除——只设旧名
// 时各消费面必须回落缺省行为，新名 FLMX_* 是唯一生效来源。存量部署
// 须在升级前把 FLVX_* 键改名为 FLMX_*（见 doc/guide/upgrade.md）。
func TestRenameCompatLegacyEnvRetired(t *testing.T) {
	t.Setenv("FLVX_TRUSTED_PROXY_HOPS", "3")
	if got := TrustedProxyHops(); got == 3 {
		t.Fatalf("retired FLVX_TRUSTED_PROXY_HOPS must be ignored: got %d", got)
	}
	t.Setenv("FLMX_TRUSTED_PROXY_HOPS", "2")
	if got := TrustedProxyHops(); got != 2 {
		t.Fatalf("FLMX_TRUSTED_PROXY_HOPS must be honored: got %d", got)
	}

	t.Setenv("FLVX_FETCH_PRIVATE_ALLOWLIST", "10.0.0.0/8")
	if isPrivateAllowed(net.ParseIP("10.1.2.3")) {
		t.Fatal("retired FLVX_FETCH_PRIVATE_ALLOWLIST must be ignored")
	}
	t.Setenv("FLVX_FETCH_ALLOW_PRIVATE_NETWORKS", "true")
	if isPrivateAllowed(net.ParseIP("192.168.1.1")) {
		t.Fatal("retired FLVX_FETCH_ALLOW_PRIVATE_NETWORKS must be ignored")
	}
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "10.0.0.0/8")
	if !isPrivateAllowed(net.ParseIP("10.1.2.3")) {
		t.Fatal("FLMX_FETCH_PRIVATE_ALLOWLIST must be honored")
	}

	// Passkey Origin：只设旧名 FLVX_WEBAUTHN_ORIGIN 时必须读不到，
	// 只设新名时必须读到。
	t.Setenv("FLVX_WEBAUTHN_ORIGIN", "https://panel.example.test")
	if _, origin := passkeyConfig(); origin == "https://panel.example.test" {
		t.Fatalf("retired FLVX_WEBAUTHN_ORIGIN must be ignored: origin=%q", origin)
	}
	t.Setenv("FLMX_WEBAUTHN_ORIGIN", "https://panel.example.test")
	if _, origin := passkeyConfig(); origin != "https://panel.example.test" {
		t.Fatalf("FLMX_WEBAUTHN_ORIGIN must be honored: origin=%q", origin)
	}
}

// FLMX 改名兼容：面板版本键新名 FLMX_VERSION 优先、旧名
// FLUX_VERSION 回退（存量 .env 未迁移前继续可读）。
func TestRenameCompatPanelVersionKeyPriority(t *testing.T) {
	t.Setenv("FLUX_VERSION", "1.9.7")
	if got := currentPanelVersion(); got != "1.9.7" {
		t.Fatalf("legacy FLUX_VERSION ignored: got %q", got)
	}
	t.Setenv("FLMX_VERSION", "2.0.0")
	if got := currentPanelVersion(); got != "2.0.0" {
		t.Fatalf("FLMX_VERSION must win over legacy FLUX_VERSION: got %q", got)
	}
}

// FLMX 改名兼容：改名前发出的 v2/v1 个人订阅链接签名必须继续有效，
// 新链接使用 v3（flmx-subsig）签名。
func TestRenameCompatSigVersions(t *testing.T) {
	h := &Handler{jwtSecret: "rename-test-secret"}
	const token = "sub-token-abc"
	const uid = int64(42)

	v3 := h.subURLSigForToken(token, uid)
	if !h.verifySubURLSigForToken(token, uid, v3) {
		t.Fatal("v3 sig must verify")
	}
	v2 := h.subURLSigV2ForToken(token, uid)
	if v2 == v3 {
		t.Fatal("v2 and v3 sigs must differ (different prefixes)")
	}
	if !h.verifySubURLSigForToken(token, uid, v2) {
		t.Fatal("legacy v2 sig must still verify after rename")
	}
	v1 := h.subURLSig(uid)
	if !h.verifySubURLSigForToken(token, uid, v1) {
		t.Fatal("legacy v1 sig must still verify after rename")
	}
}
