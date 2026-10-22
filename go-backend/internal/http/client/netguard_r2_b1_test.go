package client

import (
	"net"
	"testing"
)

// R2 批1（R2X-006 联邦传输面）：NAT64 全形态 + 阿里云元数据硬拒前置。
// 红态（修复前）：NAT64 地址被当作公网 IPv6、100.100.100.200 可被
// CGNAT allowlist 放行。

func TestR2B1FederationNAT64AndMetadataHardDeny(t *testing.T) {
	t.Setenv("FLMX_FETCH_ALLOW_PRIVATE_NETWORKS", "")
	t.Setenv("FLVX_FETCH_ALLOW_PRIVATE_NETWORKS", "")
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "")
	t.Setenv("FLVX_FETCH_PRIVATE_ALLOWLIST", "")
	deny := []string{
		"64:ff9b::a9fe:a9fe",   // NAT64 内嵌 169.254.169.254
		"64:ff9b::a00:1",       // NAT64 内嵌 10.0.0.1
		"64:ff9b::6464:64c8",   // NAT64 内嵌 100.100.100.200
		"64:ff9b:1::a9fe:a9fe", // 本地 NAT64
		"2002:a9fe:a9fe::1",    // 6to4 内嵌元数据
		"2002:0a00:0001::1",    // 6to4 内嵌 10.0.0.1
	}
	for _, s := range deny {
		if err := validateFederationIPs("t.test", []net.IP{net.ParseIP(s)}); err == nil {
			t.Fatalf("validateFederationIPs(%s) must be rejected by default", s)
		}
	}
	// 内嵌公网按公网放行（防过修）。
	for _, s := range []string{"64:ff9b::808:808", "2002:0808:0808::1"} {
		if err := validateFederationIPs("t.test", []net.IP{net.ParseIP(s)}); err != nil {
			t.Fatalf("validateFederationIPs(%s) embedded public must pass: %v", s, err)
		}
	}
}

func TestR2B1FederationAliyunMetadataHardBeforeAllowlist(t *testing.T) {
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "100.64.0.0/10")
	t.Setenv("FLVX_FETCH_PRIVATE_ALLOWLIST", "")
	if err := validateFederationIPs("t.test", []net.IP{net.ParseIP("100.100.100.200")}); err == nil {
		t.Fatal("100.100.100.200 must be hard-denied even when CGNAT is allowlisted")
	}
	if err := validateFederationIPs("t.test", []net.IP{net.ParseIP("169.254.0.23")}); err == nil {
		t.Fatal("169.254.0.23 must be hard-denied (whole /16)")
	}
	// allowlist 内的普通 CGNAT 维持放行。
	if err := validateFederationIPs("t.test", []net.IP{net.ParseIP("100.64.1.2")}); err != nil {
		t.Fatalf("allowlisted CGNAT must stay allowed: %v", err)
	}
}
