package handler

import (
	"net"
	"testing"
)

// F-023 (audit FINAL §5-B8 / A6-002): isPrivateIP 声称拦截 0.0.0.0/8
// （注释引 TD-IMP-001），实际只有 IsUnspecified 命中 0.0.0.0 单地址，
// 0.1.2.3 等被判公网放行；Linux 下 0/8 拨号直达本机。
// 修复前本测试失败：0.1.2.3 / 0.255.255.255 均返回 false。
func TestIsPrivateIPCoversZeroSlash8F023(t *testing.T) {
	for _, s := range []string{"0.0.0.0", "0.1.2.3", "0.255.255.255", "0.0.0.1"} {
		if !isPrivateIP(net.ParseIP(s)) {
			t.Fatalf("isPrivateIP(%s) must be true (0.0.0.0/8 整段)", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "1.1.1.1", "203.0.113.9"} {
		if isPrivateIP(net.ParseIP(s)) {
			t.Fatalf("isPrivateIP(%s) must stay false (公网)", s)
		}
	}
	// validateFetchIP 是 SecureFetch 的实际门：0/8 必须在"公网 https 放行"
	// 之前就被私网口径拦下（需 allowlist 或直接拒绝，与其他私网段一致）。
	if err := validateFetchIP(net.ParseIP("0.1.2.3"), "https://example.com", "https"); err == nil {
		t.Fatal("validateFetchIP(0.1.2.3) must not be treated as public")
	}
}
