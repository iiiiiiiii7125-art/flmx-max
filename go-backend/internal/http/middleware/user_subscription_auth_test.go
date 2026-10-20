package middleware

import "testing"

// 1.0.12: /user-subscription/list 与 /set 读写任意用户的订阅分配，须仅管理员可调用；
// /user-subscription/mine 为普通用户自己的订阅，保持开放。
func TestRequiresAdminCoversUserSubscriptionAssignment(t *testing.T) {
	if !requiresAdmin("/api/v1/user-subscription/list") {
		t.Fatalf("/api/v1/user-subscription/list must require admin")
	}
	if !requiresAdmin("/api/v1/user-subscription/set") {
		t.Fatalf("/api/v1/user-subscription/set must require admin")
	}
	if requiresAdmin("/api/v1/user-subscription/mine") {
		t.Fatalf("/api/v1/user-subscription/mine must stay open to normal users")
	}
}
