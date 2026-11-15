package handler

import (
	"testing"

	"go-backend/internal/kernel"
)

// F2-DBL-01 / E-01: per-agent socket-mark base 的版本门——agent >= 2.1.13
// 才渲染/下发 per-agent base，旧 agent 与未知版本一律 legacy。
// E-01：base 按节点 secret 派生——同 id 不同 secret（不同面板）
// 的节点得不同 base。
func TestUserMarkBaseForNode(t *testing.T) {
	legacy := kernel.LegacyUserMarkBase
	secretA := "0123456789abcdef0123456789abcdef"
	secretB := "fedcba9876543210fedcba9876543210"
	perAgent := kernel.UserMarkBaseForAgent(1, secretA)
	if legacy != 0x46560000 || perAgent != 0x47170000 {
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
		if got := userMarkBaseForNode(1, secretA, c.version); got != c.want {
			t.Fatalf("version %q: got 0x%x, want 0x%x", c.version, got, c.want)
		}
	}
	// 同 id、不同 secret：base 随 secret 变（不同面板的同 id 节点）。
	if got := userMarkBaseForNode(1, secretB, "2.1.13"); got != kernel.UserMarkBaseForAgent(1, secretB) {
		t.Fatalf("secret B: got 0x%x", got)
	}
	if userMarkBaseForNode(1, secretB, "2.1.13") == userMarkBaseForNode(1, secretA, "2.1.13") {
		t.Fatal("same node id with different secrets must get different bases")
	}
	// 同 secret、不同 id：base 不变（secret 才是身份）。
	if userMarkBaseForNode(76, secretA, "2.1.13") != perAgent {
		t.Fatal("same secret must mint the same base regardless of node id")
	}
	// 空 secret 回落 v1（节点 id 低字节）——仅防御，生产行恒有 secret。
	if got := userMarkBaseForNode(75, "", "2.1.13"); got != kernel.UserMarkBaseForAgent(75, "") {
		t.Fatalf("empty secret: got 0x%x", got)
	}
}
