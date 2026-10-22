package kernel

import "testing"

// R3 批1（R3X-010）导入面回归：PolicyHardDenyOrLoopback 补拒后，
// ValidateImportHost 对保留/组播/fe80 字面量必须报错，Private/CGNAT
// 仍放行（防过修）。红态（修复前）：0.1.2.3 / 224.0.0.1 / fe80::1 均 err=nil。
func TestR3B1ValidateImportHostRejectsReservedMulticastLinkLocal(t *testing.T) {
	for _, host := range []string{"0.1.2.3", "224.0.0.1", "fe80::1", "240.0.0.1", "198.18.0.1"} {
		if err := ValidateImportHost(host); err == nil {
			t.Fatalf("ValidateImportHost(%q) must be rejected (R3X-010)", host)
		}
	}
}

func TestR3B1ValidateImportHostStillAllowsPrivateCGNAT(t *testing.T) {
	for _, host := range []string{"10.0.0.5", "100.64.0.1"} {
		if err := ValidateImportHost(host); err != nil {
			t.Fatalf("ValidateImportHost(%q) must stay allowed: %v", host, err)
		}
	}
}
