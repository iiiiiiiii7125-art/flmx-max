package handler

import "testing"

func TestIssue515IsValidNodeAddressAcceptsBareIPv6(t *testing.T) {
	for _, addr := range []string{
		"2001:db8::1",
		"::1",
		"fe80::1%eth0",
	} {
		t.Run(addr, func(t *testing.T) {
			if err := IsValidNodeAddress(addr); err != nil {
				t.Fatalf("expected bare IPv6 address %q to be accepted: %v", addr, err)
			}
		})
	}
}

func TestIsValidNodeAddressKeepsExistingAddressForms(t *testing.T) {
	for _, addr := range []string{
		"203.0.113.10",
		"node.example.com",
		"node.example.com:6365",
		"[2001:db8::1]:6365",
	} {
		t.Run(addr, func(t *testing.T) {
			if err := IsValidNodeAddress(addr); err != nil {
				t.Fatalf("expected node address %q to be accepted: %v", addr, err)
			}
		})
	}
}

func TestIsValidNodeAddressRejectsURLComponents(t *testing.T) {
	for _, addr := range []string{
		"https://node.example.com",
		"node.example.com/path",
		"node.example.com?transport=tcp",
	} {
		t.Run(addr, func(t *testing.T) {
			if err := IsValidNodeAddress(addr); err == nil {
				t.Fatalf("expected node address %q to be rejected", addr)
			}
		})
	}
}

// FINAL-AUDIT-170 L1-F-04（P3）：节点地址 `a..b` 只做结构字符检查。
// 空标签主机名在任何 DNS 下都不可解析，写面即拒（与 T-01 同机制）。
func TestFinalL1F04IsValidNodeAddressRejectsEmptyLabel(t *testing.T) {
	for _, addr := range []string{
		"a..b",
		"a...b",
		"a..b:443",
		".example.com",
		"example.com.",
		"[a..b]:443",
	} {
		t.Run(addr, func(t *testing.T) {
			if err := IsValidNodeAddress(addr); err == nil {
				t.Fatalf("expected node address %q to be rejected (empty DNS label)", addr)
			}
		})
	}
}

// FINAL-AUDIT-170 L1-F-04 红线：RFC1918/回环的既有可存性一概不变——
// 只加空标签拒绝，不加任何 IP 政策（本地测试节点仍可存）。
func TestFinalL1F04IsValidNodeAddressKeepsLoopbackAndPrivateStorable(t *testing.T) {
	for _, addr := range []string{
		"127.0.0.1",
		"127.0.0.1:6365",
		"192.168.1.1",
		"10.0.0.1:8080",
		"[::1]:6365",
		"localhost",
		"localhost:6365",
	} {
		t.Run(addr, func(t *testing.T) {
			if err := IsValidNodeAddress(addr); err != nil {
				t.Fatalf("expected node address %q to stay storable: %v", addr, err)
			}
		})
	}
}
