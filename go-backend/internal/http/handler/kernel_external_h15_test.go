package handler

import (
	"net"
	"testing"
)

// H-15/R-005: SSRF 防护
// 修复前：fetchSubscriptionInfo 直接请求用户输入的 URL，无内网检查
// 修复后：SecureFetch 统一硬化（拨号时按目标 IP 判定、元数据硬黑名单、
//
//	公网强制 https、逐跳重定向校验）；URL 级行为测试见 secure_fetch_d17_test.go。
//
// IMP-005：validateSubscriptionURL 已删除（被 SecureFetch 取代的死代码）。
// 本文件保留 IP 分类单元测试；URL 抓取的 SSRF 行为由 SecureFetch 测试覆盖。
func TestH15_PrivateIPBlocked(t *testing.T) {
	// 使用 IP 字面量直接测试 isPrivateIP（避免 DNS 查询）。
	// 对应原来 validateSubscriptionURL 会拒绝的内网 URL。
	blocked := []string{
		"127.0.0.1",   // http://127.0.0.1/sub
		"192.168.1.1", // http://192.168.1.1/sub
		"10.0.0.1",    // http://10.0.0.1/sub
		"172.16.0.1",  // http://172.16.0.1/sub
		"169.254.169.254",
		"::1",     // http://[::1]/sub
		"fe80::1", // http://[fe80::1]/sub
	}
	for _, ipStr := range blocked {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			t.Fatalf("failed to parse %s", ipStr)
		}
		if !isPrivateIP(ip) {
			t.Errorf("expected %q to be classified private", ipStr)
		}
	}
}

func TestH15_PublicIPAllowed(t *testing.T) {
	// 直接测试 isPrivateIP，不依赖 DNS
	publicIPs := []string{"8.8.8.8", "1.1.1.1", "8.8.4.4"}
	for _, ipStr := range publicIPs {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			t.Fatalf("failed to parse %s", ipStr)
		}
		if isPrivateIP(ip) {
			t.Errorf("expected %s to be public", ipStr)
		}
	}
	// 私有 IP 应被识别
	privateIPs := []string{"127.0.0.1", "192.168.1.1", "10.0.0.1", "172.16.0.1", "169.254.169.254"}
	for _, ipStr := range privateIPs {
		ip := net.ParseIP(ipStr)
		if !isPrivateIP(ip) {
			t.Errorf("expected %s to be private", ipStr)
		}
	}
}

func TestH15_InvalidSchemeRejected(t *testing.T) {
	// IMP-005：scheme 白名单由 SecureFetch 的 validateFetchURL 执行，
	// 这里直接测试当前实际执法点。
	invalid := []string{
		"ftp://example.com/sub",
		"file:///etc/passwd",
		"gopher://example.com/",
	}
	for _, u := range invalid {
		if _, err := validateFetchURL(u); err == nil {
			t.Errorf("expected %q to be rejected", u)
		}
	}
}
