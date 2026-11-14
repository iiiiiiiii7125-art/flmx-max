package handler

import (
	"testing"

	"go-backend/internal/kernel"
)

// F2-DBL-01: per-agent socket-mark base 的版本门——agent >= 2.1.13
// 才渲染/下发 per-agent base，旧 agent 与未知版本一律 legacy。
func TestUserMarkBaseForNode(t *testing.T) {
	legacy := kernel.LegacyUserMarkBase
	perAgent := kernel.UserMarkBaseForAgent(75)
	if legacy != 0x46560000 || perAgent != 0x474B0000 {
		t.Fatalf("sanity: legacy 0x%x per-agent 0x%x", legacy, perAgent)
	}
	cases := []struct {
		version string
		want    int64
	}{
		{"2.1.13", perAgent},
		{"2.1.13-cand", perAgent},
		{"2.1.13-cand (debian/amd64)", perAgent},
		{"2.1.14", perAgent},
		{"2.2.0", perAgent},
		{"v2.1.13", perAgent},
		{"2.1.12", legacy},
		{"2.1.12-cand", legacy},
		{"2.1.8-cand (debian/amd64)", legacy},
		{"2.1.5", legacy},
		{"", legacy},
		{"dev", legacy},
		{"garbage", legacy},
	}
	for _, c := range cases {
		if got := userMarkBaseForNode(75, c.version); got != c.want {
			t.Fatalf("version %q: got 0x%x, want 0x%x", c.version, got, c.want)
		}
	}
	// 不同节点、同一版本：base 随节点变。
	if got := userMarkBaseForNode(76, "2.1.13"); got != kernel.UserMarkBaseForAgent(76) {
		t.Fatalf("node 76: got 0x%x", got)
	}
	if userMarkBaseForNode(76, "2.1.13") == userMarkBaseForNode(75, "2.1.13") {
		t.Fatal("two nodes must get different bases")
	}
}
