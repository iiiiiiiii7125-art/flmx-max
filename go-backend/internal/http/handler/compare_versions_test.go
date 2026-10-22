package handler

import "testing"

// A11-132-06 回归：compareVersions 旧实现吞 Atoi 错误（失败段按 0 计），
// 构造版本号可误判防降级与 HasUpdate。新实现严格语义：可比时给出
// 正确的语义大小关系，不可比时返回错误、由调用方 fail-closed。
func TestCompareVersionsStrict(t *testing.T) {
	cases := []struct {
		name    string
		v1, v2  string
		want    int
		wantErr bool
	}{
		{"equal", "1.3.2", "1.3.2", 0, false},
		{"less", "1.3.2", "1.3.10", -1, false},
		{"greater", "1.10.0", "1.9.9", 1, false},
		{"shorter padded with zero", "1.3", "1.3.0", 0, false},
		{"v prefix tolerated", "v1.3.3", "1.3.2", 1, false},
		{"dev is lowest", "dev", "0.0.1", -1, false},
		{"dev both", "dev", "dev", 0, false},
		{"suffix segment incomparable", "1.3.2", "1.3.2-foo", 0, true},
		{"beta suffix incomparable", "1.3.2-beta1", "1.3.0", 0, true},
		{"overflow segment incomparable", "1.3.99999999999999999999", "1.3.2", 0, true},
		{"empty segment incomparable", "1..2", "1.0.2", 0, true},
		{"empty version incomparable", "", "1.3.2", 0, true},
		{"negative segment incomparable", "1.-1.0", "1.0.0", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := compareVersions(tc.v1, tc.v2)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("compareVersions(%q, %q) = %d, want error", tc.v1, tc.v2, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("compareVersions(%q, %q) unexpected error: %v", tc.v1, tc.v2, err)
			}
			if got != tc.want {
				t.Fatalf("compareVersions(%q, %q) = %d, want %d", tc.v1, tc.v2, got, tc.want)
			}
		})
	}
}
