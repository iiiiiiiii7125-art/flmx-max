package health

import (
	"net"
	"testing"
)

// PORT-CLOSURE Lane D — PC-09（P3）：ValidateNodeMonitorTarget icmp 分支
// 对 Target "host:port" 校验容忍（monitor_ssrf.go:97-102）。探针按
// ledger §4（B-NEW-07 实例 "203.0.113.7:80"）重建：icmp 含端口即拒；
// 合法域名/裸主机/icmp 目标仍通过（R4X-008「解析失败不拒存」的域名
// Lookup 容忍原由不受影响）；tcp 面口径不变。
func TestPortClosureDPC09ICMPTargetWithPortRejected(t *testing.T) {
	// 密封解析：example.com 恒解析为公网地址、其余域名恒解析失败，
	// 放行断言走哪条分支不依赖环境 DNS。
	sealMonitorDNSForTest(t, map[string][]net.IP{
		"example.com": {net.ParseIP("93.184.216.34")},
	})
	reject := []string{
		"203.0.113.7:80",
		"10.0.0.1:8080",
		"example.com:80",
		"[2001:db8::1]:80",
	}
	for _, target := range reject {
		if err := ValidateNodeMonitorTarget("icmp", target); err == nil {
			t.Errorf("ValidateNodeMonitorTarget(icmp, %q) = nil, want format error (icmp target must be a bare host)", target)
		}
	}
	allow := []struct{ typ, target string }{
		{"icmp", "203.0.113.7"},
		{"icmp", "192.168.1.1"},
		{"icmp", "example.com"},
		{"icmp", "nonexistent.invalid"},
		{"icmp", "2001:db8::1"},
		{"icmp", "[2001:db8::1]"},
		{"tcp", "203.0.113.7:80"},
	}
	for _, c := range allow {
		if err := ValidateNodeMonitorTarget(c.typ, c.target); err != nil {
			t.Errorf("ValidateNodeMonitorTarget(%s, %q) = %v, want nil", c.typ, c.target, err)
		}
	}
}
