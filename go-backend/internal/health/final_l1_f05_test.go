package health

import "testing"

// FINAL-AUDIT-170 L1-F-05（P3）：节点执行监控目标 `a..b`——
// ValidateNodeMonitorTarget「解析失败不拒存」把空标签也放行。
// 空标签是纯语法判定、在任何 DNS 下都不可解析，语法层即拒，
// 不触碰「面板与节点 DNS 分视图，解析失败不拒存」的设计。
func TestFinalL1F05NodeMonitorTargetRejectsEmptyLabel(t *testing.T) {
	reject := []struct{ typ, target string }{
		{"tcp", "a..b:80"},
		{"tcp", "a...b:443"},
		{"tcp", ".example.com:80"},
		{"tcp", "example.com.:80"},
		{"icmp", "a..b"},
		{"icmp", "a...b"},
		{"icmp", ".example.com"},
		{"icmp", "example.com."},
	}
	for _, c := range reject {
		if err := ValidateNodeMonitorTarget(c.typ, c.target); err == nil {
			t.Errorf("ValidateNodeMonitorTarget(%s, %q) = nil, want empty-label rejection", c.typ, c.target)
		}
	}
	allow := []struct{ typ, target string }{
		{"tcp", "nonexistent.invalid:80"}, // 解析失败仍不拒存（节点侧 DNS 可能可解析）
		{"icmp", "nonexistent.invalid"},
		{"tcp", "8.8.8.8:53"},
		{"tcp", "127.0.0.1:8080"}, // 回环放行（节点监控本机服务）
		{"tcp", "192.168.1.1:443"}, // LAN 放行
		{"tcp", "[::1]:8080"},
		{"tcp", "[2001:db8::1]:80"},
		{"icmp", "192.168.1.1"},
		{"icmp", "127.0.0.1"},
		{"icmp", "2001:db8::1"},
		{"icmp", "[2001:db8::1]"},
	}
	for _, c := range allow {
		if err := ValidateNodeMonitorTarget(c.typ, c.target); err != nil {
			t.Errorf("ValidateNodeMonitorTarget(%s, %q) = %v, want nil (防过修)", c.typ, c.target, err)
		}
	}
}
