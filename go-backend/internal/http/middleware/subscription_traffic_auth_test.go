package middleware

import "testing"

// #88（3.3.29）：管理端流量统计在 /api/v1/subscription/ 前缀下，须仅
// 管理员可调用；用户端 /api/v1/user/subscription/traffic 对登录用户开放
// （handler 内再按订阅分配表校验），不得被管理员前缀误伤。
func TestRequiresAdminCoversSubscriptionTraffic(t *testing.T) {
	if !requiresAdmin("/api/v1/subscription/traffic") {
		t.Fatalf("/api/v1/subscription/traffic must require admin")
	}
	if requiresAdmin("/api/v1/user/subscription/traffic") {
		t.Fatalf("/api/v1/user/subscription/traffic must stay open to logged-in users")
	}
}
