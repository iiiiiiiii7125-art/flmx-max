package kernel

import "testing"

// R25-X-01 守卫行为锚点：ValidateImportHost 本身不动——带端口的值
// 仍被拒，裸 host 仍放行。修复只在分享地址派生侧剥离端口，
// 守卫标准一字不变。
func TestR25L3BShareHostRejectsHostPort(t *testing.T) {
	if err := ValidateImportHost("16.146.199.68:6366"); err == nil {
		t.Fatal(`ValidateImportHost("16.146.199.68:6366") = nil, want error`)
	}
	if err := ValidateImportHost("[2001:db8::1]:6366"); err == nil {
		t.Fatal(`ValidateImportHost("[2001:db8::1]:6366") = nil, want error`)
	}
	if err := ValidateImportHost("16.146.199.68"); err != nil {
		t.Fatalf(`ValidateImportHost("16.146.199.68") = %v, want nil`, err)
	}
	if err := ValidateImportHost("2001:db8::1"); err != nil {
		t.Fatalf(`ValidateImportHost("2001:db8::1") = %v, want nil`, err)
	}
}
