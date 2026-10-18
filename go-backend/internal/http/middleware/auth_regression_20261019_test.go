package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"go-backend/internal/auth"
	"go-backend/internal/http/response"
)

// H-12: /api/v1/user/quota/reset 必须仅管理员可调用。
func TestRequiresAdminCoversQuotaReset(t *testing.T) {
	if !requiresAdmin("/api/v1/user/quota/reset") {
		t.Fatalf("H-12: /api/v1/user/quota/reset must require admin")
	}
}

// M-05: isDefaultSeedAdminPassword 判定逻辑。
func TestIsDefaultSeedAdminPassword(t *testing.T) {
	seed := &auth.UserAuthState{ID: 1, Username: "admin@user", RoleID: 0, Status: 1, PasswordChangedAt: seedAdminPasswordChangedAt}
	if !isDefaultSeedAdminPassword(seed) {
		t.Fatalf("seed admin with default password not detected")
	}
	legacy := &auth.UserAuthState{ID: 1, Username: "admin_user", RoleID: 0, Status: 1, PasswordChangedAt: seedAdminPasswordChangedAt}
	if !isDefaultSeedAdminPassword(legacy) {
		t.Fatalf("legacy seed admin name not detected")
	}
	changed := &auth.UserAuthState{ID: 1, Username: "admin@user", RoleID: 0, Status: 1, PasswordChangedAt: seedAdminPasswordChangedAt + 1}
	if isDefaultSeedAdminPassword(changed) {
		t.Fatalf("admin who changed password still flagged")
	}
	renamed := &auth.UserAuthState{ID: 1, Username: "root", RoleID: 0, Status: 1, PasswordChangedAt: seedAdminPasswordChangedAt}
	if isDefaultSeedAdminPassword(renamed) {
		t.Fatalf("renamed admin flagged")
	}
	nonAdmin := &auth.UserAuthState{ID: 2, Username: "admin@user", RoleID: 1, Status: 1, PasswordChangedAt: seedAdminPasswordChangedAt}
	if isDefaultSeedAdminPassword(nonAdmin) {
		t.Fatalf("non-admin flagged")
	}
	if isDefaultSeedAdminPassword(nil) {
		t.Fatalf("nil state flagged")
	}
}

// M-05: 种子默认口令下，除改密接口外所有 API 返回 403。
func TestJWTForcesPasswordChangeOnDefaultSeed(t *testing.T) {
	secret := "test-jwt-secret"
	token, err := auth.GenerateToken(1, "admin@user", 0, secret)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	wrapped := JWT(AuthOptions{
		JWTSecret: secret,
		GetUserAuthState: func(userID int64) (*auth.UserAuthState, error) {
			return &auth.UserAuthState{ID: 1, Username: "admin@user", RoleID: 0, Status: 1, PasswordChangedAt: seedAdminPasswordChangedAt}, nil
		},
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response.WriteJSON(w, response.OK("pass"))
	}))

	// 普通业务接口应被 403
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tunnel/list", nil)
	req.Header.Set("Authorization", token)
	res := httptest.NewRecorder()
	wrapped.ServeHTTP(res, req)
	assertCodeMsg(t, res, 403, "请先修改默认管理员密码")

	// 改密接口放行
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/user/updatePassword", nil)
	req2.Header.Set("Authorization", token)
	res2 := httptest.NewRecorder()
	wrapped.ServeHTTP(res2, req2)
	assertCodeMsg(t, res2, 0, "操作成功")
}
