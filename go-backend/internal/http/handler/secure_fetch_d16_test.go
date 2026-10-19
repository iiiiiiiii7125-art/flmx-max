package handler

import (
	"context"
	"net"
	"testing"
	"time"
)

// R5-B5 D16: 下载/SSRF 统一硬化。
// 可达性证据：T-D TD-IMP-001/002（0.0.0.0 绕过、DNS rebinding）；
// T-F J-001/J-002（下载无校验 5 处中 3 处未修）。

func TestIsPrivateIPCoverage(t *testing.T) {
	cases := []struct {
		ip      string
		private bool
	}{
		{"127.0.0.1", true},
		{"0.0.0.0", true},           // TD-IMP-001
		{"::", true},               // TD-IMP-001
		{"10.0.0.1", true},
		{"172.16.0.1", true},
		{"192.168.1.1", true},
		{"169.254.169.254", true},  // 元数据服务
		{"100.64.0.1", true},       // CGNAT
		{"::ffff:127.0.0.1", true}, // IPv4-mapped
		{"::ffff:10.0.0.1", true},  // IPv4-mapped 私网
		{"8.8.8.8", false},
		{"1.1.1.1", false},
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if got := isPrivateIP(ip); got != c.private {
			t.Errorf("isPrivateIP(%s) = %v, want %v", c.ip, got, c.private)
		}
	}
}

func TestSecureFetchRejectsPrivate(t *testing.T) {
	SecureFetchAllowHTTP = true
	defer func() { SecureFetchAllowHTTP = false }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 内网 IP 直接拒绝。
	for _, url := range []string{
		"http://127.0.0.1/test",
		"http://0.0.0.0/test",
		"http://10.0.0.1/test",
		"http://169.254.169.254/latest/meta-data/",
	} {
		if _, err := SecureFetch(ctx, url); err == nil {
			t.Errorf("SecureFetch(%s) should be blocked", url)
		}
	}
}

func TestSecureFetchRejectsHTTP(t *testing.T) {
	// 默认不允许 http。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := SecureFetch(ctx, "http://example.com/test"); err == nil {
		t.Error("http should be blocked by default")
	}
}

func TestSecureFetchURLValidation(t *testing.T) {
	// validateFetchURL 单元测试。
	SecureFetchAllowHTTP = true
	defer func() { SecureFetchAllowHTTP = false }()

	// 非法 URL。
	if _, err := validateFetchURL("not-a-url"); err == nil {
		t.Error("invalid URL should fail")
	}
	// 空 host。
	if _, err := validateFetchURL("https://"); err == nil {
		t.Error("empty host should fail")
	}
}
