package handler

import (
	"net"
	"testing"
)

// R1 X-P3-32 (X-CS1, V-04): 元数据硬黑名单覆盖不全——阿里云元数据
// 100.100.100.200 在 ALLOW_PRIVATE=true 时可达；NAT64 公共前缀
// 64:ff9b::/96 翻译出的元数据/私网地址被当公网放行。

func TestR1B5AlibabaMetadataAlwaysRejected(t *testing.T) {
	t.Setenv("FLMX_FETCH_ALLOW_PRIVATE_NETWORKS", "true")
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "100.64.0.0/10")
	ip := net.ParseIP("100.100.100.200")
	if !isMetadataAddress(ip) {
		t.Fatal("100.100.100.200 must be in metadataBlacklist")
	}
	if err := validateFetchIP(ip, "https://example.com", "https"); err == nil {
		t.Fatal("validateFetchIP(100.100.100.200) must reject even with ALLOW_PRIVATE=true")
	}
}

func TestR1B5NAT64EmbeddedMetadataRejected(t *testing.T) {
	t.Setenv("FLMX_FETCH_ALLOW_PRIVATE_NETWORKS", "true")
	// 64:ff9b::a9fe:a9fe 内嵌 169.254.169.254（云元数据）。
	ip := net.ParseIP("64:ff9b::a9fe:a9fe")
	if ip == nil {
		t.Fatal("parse NAT64 address")
	}
	if !isMetadataAddress(ip) {
		t.Fatal("NAT64-embedded metadata address must be detected as metadata")
	}
	if err := validateFetchIP(ip, "https://example.com", "https"); err == nil {
		t.Fatal("validateFetchIP(64:ff9b::a9fe:a9fe) must reject even with ALLOW_PRIVATE=true")
	}
	// 阿里元数据的 NAT64 内嵌形态：100.100.100.200 = 6464:64c8。
	ip2 := net.ParseIP("64:ff9b::6464:64c8")
	if err := validateFetchIP(ip2, "https://example.com", "https"); err == nil {
		t.Fatal("validateFetchIP(64:ff9b::6464:64c8 = 100.100.100.200) must reject")
	}
}

func TestR1B5NAT64EmbeddedPrivateRejected(t *testing.T) {
	// 未放行私网时，NAT64 内嵌私网地址必须走同一私网判定被拒。
	t.Setenv("FLMX_FETCH_ALLOW_PRIVATE_NETWORKS", "")
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "")
	// 64:ff9b::0a00:0001 内嵌 10.0.0.1。
	ip := net.ParseIP("64:ff9b::0a00:0001")
	if err := validateFetchIP(ip, "https://example.com", "https"); err == nil {
		t.Fatal("validateFetchIP(NAT64-embedded 10.0.0.1) must reject when private not allowlisted")
	}
}

func TestR1B5NAT64PublicUnaffected(t *testing.T) {
	t.Setenv("FLMX_FETCH_ALLOW_PRIVATE_NETWORKS", "")
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "")
	// 64:ff9b::0808:0808 内嵌公网 8.8.8.8：https 应放行（防过修正）。
	ip := net.ParseIP("64:ff9b::0808:0808")
	if err := validateFetchIP(ip, "https://example.com", "https"); err != nil {
		t.Fatalf("validateFetchIP(NAT64-embedded 8.8.8.8, https) = %v, want nil", err)
	}
	// 普通公网对照。
	if err := validateFetchIP(net.ParseIP("8.8.8.8"), "https://example.com", "https"); err != nil {
		t.Fatalf("validateFetchIP(8.8.8.8, https) = %v, want nil", err)
	}
}
