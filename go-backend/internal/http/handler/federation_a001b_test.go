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

// AUDIT-R14 (L2 变异 M1)：多条目 XFF 必须从右按 hops 解析。
// 变异将从右改为从左（entries[0]）后存活，因原测试仅用单条目 XFF。
// 本测试锁定"从右解析"语义：hops=1 时取最右条目，hops=2 时取倒数第二。
func TestA001b_FederationXFFMultiEntryFromRight(t *testing.T) {
	t.Setenv("FLVX_FEDERATION_TRUSTED_PROXIES", "192.168.1.0/24")

	// hops=1：取最右（10.0.0.1），而非最左（1.1.1.1）
	t.Setenv("FLVX_TRUSTED_PROXY_HOPS", "1")
	got := federationClientIP(fedReq("192.168.1.50:1234", "1.1.1.1, 10.0.0.1"))
	if got == nil || got.String() != "10.0.0.1" {
		t.Fatalf("hops=1 multi-entry: got %v, want 10.0.0.1 (rightmost)", got)
	}

	// hops=2：取倒数第二（1.1.1.1）
	// R24-X-01 夹具修正：代理条目须落在已配置可信 CIDR 内（本例用
	// 192.168.1.1），否则链长估计无法将其确认为代理追加（见
	// trustedXFFChainLen）；断言一字不动——正确 2 跳链仍取倒数第二。
	t.Setenv("FLVX_TRUSTED_PROXY_HOPS", "2")
	got = federationClientIP(fedReq("192.168.1.50:1234", "1.1.1.1, 192.168.1.1"))
	if got == nil || got.String() != "1.1.1.1" {
		t.Fatalf("hops=2 multi-entry: got %v, want 1.1.1.1 (second from right)", got)
	}
}
