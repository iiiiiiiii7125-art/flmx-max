package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// R5-B2 A-001b / D5：联邦侧与浏览器侧拆开。
// 旧缺陷：authPeer 复用 resolvePeerClientIP，私网 RemoteAddr 被一律视为
// 可信代理，私网对端伪造 XFF 即可绕过 AllowedIPs 白名单。

func fedReq(remoteAddr, xff string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/federation/tunnel/create", nil)
	r.RemoteAddr = remoteAddr
	if xff != "" {
		r.Header.Set("X-Forwarded-For", xff)
	}
	return r
}

// 场景 7：默认（未配置 TRUSTED_PROXIES）私网对端伪造 XFF，仍按真实 RemoteAddr 判定。
func TestA001b_FederationIgnoresSpoofedXFFByDefault(t *testing.T) {
	t.Setenv("FLVX_FEDERATION_TRUSTED_PROXIES", "")
	t.Setenv("FLVX_TRUSTED_PROXY_HOPS", "1")
	got := federationClientIP(fedReq("192.168.1.50:1234", "10.0.0.1"))
	if got == nil || got.String() != "192.168.1.50" {
		t.Fatalf("default: got %v, want 192.168.1.50 (spoofed XFF must be ignored)", got)
	}
}

// 显式配置可信代理 CIDR 后才采信 XFF（仍从右解析）。
func TestA001b_FederationTrustedProxiesOptIn(t *testing.T) {
	t.Setenv("FLVX_FEDERATION_TRUSTED_PROXIES", "192.168.1.0/24")
	t.Setenv("FLVX_TRUSTED_PROXY_HOPS", "1")
	got := federationClientIP(fedReq("192.168.1.50:1234", "10.0.0.1"))
	if got == nil || got.String() != "10.0.0.1" {
		t.Fatalf("opt-in: got %v, want 10.0.0.1", got)
	}
	// CIDR 外的对端：即使配了可信网段也不采信
	got = federationClientIP(fedReq("192.168.2.50:1234", "10.0.0.1"))
	if got == nil || got.String() != "192.168.2.50" {
		t.Fatalf("outside CIDR: got %v, want 192.168.2.50", got)
	}
	// 裸 IP 条目视为 /32
	t.Setenv("FLVX_FEDERATION_TRUSTED_PROXIES", "192.168.1.50")
	got = federationClientIP(fedReq("192.168.1.50:1234", "10.0.0.1"))
	if got == nil || got.String() != "10.0.0.1" {
		t.Fatalf("bare IP: got %v, want 10.0.0.1", got)
	}
}
