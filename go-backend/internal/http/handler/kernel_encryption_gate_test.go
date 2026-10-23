package handler

import "testing"

// VLESS 加密的 Xray 版本门以 25.9.5 为下限精确到修订号（R8X-001 更正：
// 25.6.8–25.8.29 对面板 ENC 产物实测 exit 23，真下限 25.9.5，逐版二分
// 日志 audit-out-132/r8/R8-FIX-B1-version-bisect.log）；"latest" 与
// 不可解析版本不拦截（与 versionBelow 的既有语义一致）。
func TestVersionBelow3(t *testing.T) {
	cases := []struct {
		version string
		want    bool
	}{
		{"25.9.5", false},
		{"25.9.4", true},
		{"25.8.29", true},
		{"25.6.8", true},
		{"25.9.10", false},
		{"25.10.0", false},
		{"24.12.31", true},
		{"26.10.21", false},
		{"v25.9.5", false},
		{"latest", false},
		{"", false},
	}
	for _, c := range cases {
		if got := versionBelow3(c.version, 25, 9, 5); got != c.want {
			t.Errorf("versionBelow3(%q, 25, 9, 5) = %v, want %v", c.version, got, c.want)
		}
	}
}
