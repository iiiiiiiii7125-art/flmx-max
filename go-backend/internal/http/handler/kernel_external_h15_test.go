package handler

import (
	"net"
	"testing"
)

// H-15/R-005: SSRF 防护
// 修复前：fetchSubscriptionInfo 直接请求用户输入的 URL，无内网检查
// 修复后：validateSubscriptionURL 拦截回环/内网/链路本地/云元数据；
//        重定向后重新校验；DNS 解析后检查 IP（防重绑定）
func TestH15_PrivateIPBlocked(t *testing.T) {
	// 使用 IP 字面量避免 DNS 查询
	blocked := []string{
		"http://127.0.0.1/sub",
		"http://192.168.1.1/sub",
		"http://10.0.0.1/sub",
		"http://172.16.0.1/sub",
		"http://169.254.169.254/latest/meta-data/",
		"http://[::1]/sub",
		"http://[fe80::1]/sub",
	}
	for _, u := range blocked {
		if err := validateSubscriptionURL(u); err == nil {
			t.Errorf("expected %q to be blocked", u)
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
	invalid := []string{
		"ftp://example.com/sub",
		"file:///etc/passwd",
		"gopher://example.com/",
	}
	for _, u := range invalid {
		if err := validateSubscriptionURL(u); err == nil {
			t.Errorf("expected %q to be rejected", u)
		}
	}
}
