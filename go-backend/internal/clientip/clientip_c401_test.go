package clientip

import "testing"

// C4-01: 私网对端不再默认可信——未显式配置 FLMX_TRUSTED_PROXIES
// 时, 内网主机伪造的 XFF 必须被忽略 (限流键回 RemoteAddr);
// 配置命中后同型于联邦面恢复采信。

func TestC401PrivatePeerNotTrustedByDefault(t *testing.T) {
	t.Setenv("FLMX_TRUSTED_PROXIES", "")
	for _, peer := range []string{"192.168.1.50:6365", "172.30.0.5:6365", "10.0.0.9:6365", "169.254.1.1:6365", "100.64.0.1:6365"} {
		got := FromRequest(reqWith(peer, "1.2.3.4"))
		want := peer[:len(peer)-5] // strip :port
		if got == nil || got.String() != want {
			t.Fatalf("peer %s: unconfigured private peer must not be trusted, got %v want %s", peer, got, want)
		}
	}
}

func TestC401ConfiguredCIDRPeerTrusted(t *testing.T) {
	t.Setenv("FLMX_TRUSTED_PROXIES", "172.30.0.0/16")
	got := FromRequest(reqWith("172.30.0.5:6365", "203.0.113.7"))
	if got == nil || got.String() != "203.0.113.7" {
		t.Fatalf("configured proxy peer must resolve the XFF client, got %v", got)
	}
	// 配置外私网对端仍不可信。
	got = FromRequest(reqWith("192.168.1.50:6365", "203.0.113.7"))
	if got == nil || got.String() != "192.168.1.50" {
		t.Fatalf("peer outside the configured CIDRs must stay untrusted, got %v", got)
	}
	// 裸 IP 条目同型可用。
	t.Setenv("FLMX_TRUSTED_PROXIES", "192.168.1.50")
	got = FromRequest(reqWith("192.168.1.50:6365", "203.0.113.7"))
	if got == nil || got.String() != "203.0.113.7" {
		t.Fatalf("bare-IP entry must trust that peer, got %v", got)
	}
}

func TestC401InvalidEntriesGrantNoTrust(t *testing.T) {
	t.Setenv("FLMX_TRUSTED_PROXIES", "not-a-cidr, 999.1.1.1/8")
	got := FromRequest(reqWith("192.168.1.50:6365", "1.2.3.4"))
	if got == nil || got.String() != "192.168.1.50" {
		t.Fatalf("invalid config entries must grant no trust, got %v", got)
	}
}
