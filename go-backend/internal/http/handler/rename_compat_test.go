package handler

import (
	"net"
	"testing"
)

// FLMX 改名兼容：存量部署的 FLVX_* 环境变量必须继续生效（新名优先）。
func TestRenameCompatLegacyEnvFallback(t *testing.T) {
	t.Setenv("FLVX_TRUSTED_PROXY_HOPS", "3")
	if got := TrustedProxyHops(); got != 3 {
		t.Fatalf("legacy FLVX_TRUSTED_PROXY_HOPS ignored: got %d", got)
	}
	t.Setenv("FLMX_TRUSTED_PROXY_HOPS", "2")
	if got := TrustedProxyHops(); got != 2 {
		t.Fatalf("FLMX_TRUSTED_PROXY_HOPS must win over legacy: got %d", got)
	}

	t.Setenv("FLVX_FETCH_PRIVATE_ALLOWLIST", "10.0.0.0/8")
	if !isPrivateAllowed(net.ParseIP("10.1.2.3")) {
		t.Fatal("legacy FLVX_FETCH_PRIVATE_ALLOWLIST ignored")
	}
	t.Setenv("FLVX_FETCH_ALLOW_PRIVATE_NETWORKS", "true")
	if !isPrivateAllowed(net.ParseIP("192.168.1.1")) {
		t.Fatal("legacy FLVX_FETCH_ALLOW_PRIVATE_NETWORKS ignored")
	}

	// Passkey Origin：存量 .env 里的 FLVX_WEBAUTHN_ORIGIN 必须继续被读到，
	// 否则升级后 Passkey 入口整体消失（1.0.9 踩过的同类坑）。
	t.Setenv("FLVX_WEBAUTHN_ORIGIN", "https://panel.example.test")
	if _, origin := passkeyConfig(); origin != "https://panel.example.test" {
		t.Fatalf("legacy FLVX_WEBAUTHN_ORIGIN ignored: origin=%q", origin)
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
