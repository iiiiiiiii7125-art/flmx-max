package service

import (
	"testing"

	"github.com/go-gost/x/config"
)

// A02-001 回归（解析层）：flmx-no-private 的识别必须精确——只有
// 引用了该内置 bypass 的服务才装拨号落点 guard，名字近似或未引用
// 的不得误装（guard 会对解析结果做私网复核，误装会误伤普通服务）。
func TestServiceHasNoPrivateBypass(t *testing.T) {
	cases := []struct {
		name string
		cfg  *config.ServiceConfig
		want bool
	}{
		{"nil cfg", nil, false},
		{"no bypass", &config.ServiceConfig{}, false},
		{"primary bypass hit", &config.ServiceConfig{Bypass: "flmx-no-private"}, true},
		{"secondary bypass hit", &config.ServiceConfig{Bypasses: []string{"other", "flmx-no-private"}}, true},
		{"similar name is not a hit", &config.ServiceConfig{Bypass: "flmx-no-private-2"}, false},
		{"other bypass only", &config.ServiceConfig{Bypass: "wl", Bypasses: []string{"a", "b"}}, false},
	}
	for _, tc := range cases {
		if got := serviceHasNoPrivateBypass(tc.cfg); got != tc.want {
			t.Fatalf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
