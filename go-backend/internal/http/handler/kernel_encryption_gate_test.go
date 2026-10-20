package handler

import "testing"

// VLESS 加密的 Xray 版本门以 25.6.8 为下限精确到修订号；"latest" 与
// 不可解析版本不拦截（与 versionBelow 的既有语义一致）。
func TestVersionBelow3(t *testing.T) {
	cases := []struct {
		version string
		want    bool
	}{
		{"25.6.8", false},
		{"25.6.7", true},
		{"25.6.11", false},
		{"25.9.3", false},
		{"24.12.31", true},
		{"26.1.0", false},
		{"v25.6.8", false},
		{"latest", false},
		{"", false},
	}
	for _, c := range cases {
		if got := versionBelow3(c.version, 25, 6, 8); got != c.want {
			t.Errorf("versionBelow3(%q) = %v, want %v", c.version, got, c.want)
		}
	}
}
