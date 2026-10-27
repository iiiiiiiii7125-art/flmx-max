package repo

import "testing"

// FINAL-AUDIT-170 L1-F-04（P3）：恢复面 repo 镜像 validImportNodeAddress /
// validImportListenAddr 与 handler IsValidNodeAddress 同口径补空标签判。
func TestFinalL1F04ValidImportNodeAddressRejectsEmptyLabel(t *testing.T) {
	for _, addr := range []string{
		"a..b",
		"a...b",
		"a..b:443",
		".example.com",
		"example.com.",
		"[a..b]:443",
	} {
		if validImportNodeAddress(addr) {
			t.Errorf("validImportNodeAddress(%q) = true, want false (empty DNS label)", addr)
		}
	}
	for _, addr := range []string{
		"",
		"203.0.113.10",
		"node.example.com",
		"node.example.com:6365",
		"[2001:db8::1]:6365",
		"2001:db8::1",
		"::1",
		"127.0.0.1",
		"192.168.1.1",
		"localhost",
	} {
		if !validImportNodeAddress(addr) {
			t.Errorf("validImportNodeAddress(%q) = false, want true (防过修)", addr)
		}
	}
}

func TestFinalL1F04ValidImportListenAddrRejectsEmptyLabel(t *testing.T) {
	for _, addr := range []string{
		"a..b",
		"a...b",
		".example.com",
		"example.com.",
	} {
		if validImportListenAddr(addr) {
			t.Errorf("validImportListenAddr(%q) = true, want false (empty DNS label)", addr)
		}
	}
	for _, addr := range []string{
		"",
		"192.0.2.1",
		"node.example.com",
		"2001:db8::1",
		"[::]",
		"[2001:db8::1]",
		"127.0.0.1",
	} {
		if !validImportListenAddr(addr) {
			t.Errorf("validImportListenAddr(%q) = false, want true (防过修)", addr)
		}
	}
}

// FINAL-AUDIT-170 L1-F-05（P3）：恢复面 validImportMonitorTarget
// 只做格式层，与 ValidateNodeMonitorTarget 同口径补空标签判。
// 恢复面不做 DNS 解析的口径不变。
func TestFinalL1F05ValidImportMonitorTargetRejectsEmptyLabel(t *testing.T) {
	reject := []struct{ typ, target string }{
		{"tcp", "a..b:80"},
		{"tcp", "a...b:443"},
		{"tcp", "example.com.:80"},
		{"icmp", "a..b"},
		{"icmp", "a...b"},
		{"icmp", "example.com."},
	}
	for _, c := range reject {
		if validImportMonitorTarget(c.typ, c.target) {
			t.Errorf("validImportMonitorTarget(%s, %q) = true, want false (empty DNS label)", c.typ, c.target)
		}
	}
	allow := []struct{ typ, target string }{
		{"tcp", "8.8.8.8:53"},
		{"tcp", "nonexistent.invalid:80"},
		{"tcp", "127.0.0.1:8080"},
		{"tcp", "[::1]:80"},
		{"icmp", "nonexistent.invalid"},
		{"icmp", "192.168.1.1"},
		{"icmp", "2001:db8::1"},
		{"icmp", "[2001:db8::1]"},
	}
	for _, c := range allow {
		if !validImportMonitorTarget(c.typ, c.target) {
			t.Errorf("validImportMonitorTarget(%s, %q) = false, want true (防过修)", c.typ, c.target)
		}
	}
}
