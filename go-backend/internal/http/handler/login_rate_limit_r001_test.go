package handler

import (
	"testing"
)

// R-001: 限流键去掉端口
// 修复前：resolvePeerClientIP 返回 nil 时用 r.RemoteAddr（含端口）做键，
//
//	攻击者轮换源端口可绕过 10次/分钟限制。
//
// 修复后：降级时用 parseIPLiteral 提取纯 IP。
func TestR001_RateLimitKeyHasNoPort(t *testing.T) {
	// parseIPLiteral 应正确提取 IP
	testCases := []struct {
		remoteAddr string
		expectedIP string
	}{
		{"192.168.1.100:54321", "192.168.1.100"},
		{"10.0.0.1:12345", "10.0.0.1"},
		{"[::1]:8080", "::1"},
	}
	for _, tc := range testCases {
		ip := parseIPLiteral(tc.remoteAddr)
		if ip == nil {
			t.Errorf("parseIPLiteral(%q) returned nil", tc.remoteAddr)
			continue
		}
		if ip.String() != tc.expectedIP {
			t.Errorf("parseIPLiteral(%q) = %q, expected %q", tc.remoteAddr, ip.String(), tc.expectedIP)
		}
		// 关键断言：键中不含端口
		if contains(ip.String(), ":") && tc.expectedIP != "::1" {
			// IPv4 不应含冒号（端口分隔符）
			t.Errorf("key %q should not contain port", ip.String())
		}
	}
}
