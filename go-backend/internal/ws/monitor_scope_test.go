package ws

import (
	"path/filepath"
	"testing"
	"time"

	"go-backend/internal/auth"
	"go-backend/internal/store/repo"
)

// 监控权限范围收窄回归（2026-11-01 用户拍板）：节点实时流
// （/system-info?type=0 的节点状态与系统信息广播）只对管理员开放，
// 持有监控权限的普通用户不再准入——validateMonitorSession 必须
// 拒绝（修复前凭 HasMonitorPermission 返回 true）。管理员准入不变。
func TestMonitorScopeWSAdmissionClosed(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "ws_scope.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer func() { _ = r.Close() }()
	if err := r.InsertMonitorPermission(2, 123); err != nil {
		t.Fatalf("insert monitor permission: %v", err)
	}

	s := NewServer(r, "scope-secret")
	s.SetUserAuthStateLookup(func(userID int64) (*auth.UserAuthState, error) {
		roleID := 1
		if userID == 1 {
			roleID = 0
		}
		return &auth.UserAuthState{ID: userID, RoleID: roleID, Status: 1, PasswordChangedAt: 0}, nil
	})

	viewerClaims := auth.Claims{
		Sub: "2", RoleID: 1,
		Exp:   time.Now().Add(time.Hour).Unix(),
		IatMs: time.Now().UnixMilli(),
	}
	if s.validateMonitorSession(2, viewerClaims) {
		t.Fatal("monitor-permission user must no longer be admitted to the node realtime stream")
	}

	adminClaims := auth.Claims{
		Sub: "1", RoleID: 0,
		Exp:   time.Now().Add(time.Hour).Unix(),
		IatMs: time.Now().UnixMilli(),
	}
	if !s.validateAdminSession(1, adminClaims) {
		t.Fatal("admin session validation must stay admitted")
	}
}
