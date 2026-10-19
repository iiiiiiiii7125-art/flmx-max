package ws

import (
	"testing"
	"time"

	"go-backend/internal/auth"
)

// R5-B2 A-005 回归测试：WS 会话校验必须与 HTTP 侧一致，
// 受限 token（PwdChangeRequired，M-05）不得建立 WS 会话。

func testWSServer() *Server {
	s := &Server{}
	s.getUserAuthState = func(userID int64) (*auth.UserAuthState, error) {
		return &auth.UserAuthState{
			ID:                userID,
			RoleID:            0,
			Status:            1,
			PasswordChangedAt: time.Now().Add(-time.Hour).UnixMilli(),
		}, nil
	}
	return s
}

func wsClaims(pwdChangeRequired bool) auth.Claims {
	return auth.Claims{
		Sub:               "1",
		RoleID:            0,
		Exp:               time.Now().Add(time.Hour).Unix(),
		IatMs:             time.Now().UnixMilli(),
		PwdChangeRequired: pwdChangeRequired,
	}
}

// 受限 token 的 WS 握手（admin/monitor）必须被拒；正常 token 可过。
func TestA005_RestrictedTokenRejectedByWSValidators(t *testing.T) {
	s := testWSServer()

	if s.validateAdminSession(1, wsClaims(true)) {
		t.Error("validateAdminSession accepted a PwdChangeRequired token")
	}
	if s.validateMonitorSession(1, wsClaims(true)) {
		t.Error("validateMonitorSession accepted a PwdChangeRequired token")
	}
	if !s.validateAdminSession(1, wsClaims(false)) {
		t.Error("validateAdminSession rejected a normal token")
	}
	// monitor 校验需要 repo.HasMonitorPermission；无 repo 时拒绝与本改动无关，
	// 此处不断言 monitor 正常路径（由集成测试覆盖）。
}

// 改密后（新签发的不带受限标记的 token）恢复正常。
func TestA005_NormalTokenStillAccepted(t *testing.T) {
	s := testWSServer()
	if !s.validateAdminSession(1, wsClaims(false)) {
		t.Fatal("normal token rejected after fix")
	}
}
