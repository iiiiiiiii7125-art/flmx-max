package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go-backend/internal/auth"
	"go-backend/internal/http/response"
)

func nowForTest() time.Time { return time.Now() }

// H-12: /api/v1/user/quota/reset 必须仅管理员可调用。
func TestRequiresAdminCoversQuotaReset(t *testing.T) {
	if !requiresAdmin("/api/v1/user/quota/reset") {
		t.Fatalf("H-12: /api/v1/user/quota/reset must require admin")
	}
}

// M-05: 持"需改密"受限 token 时，除改密接口外所有 API 返回 403。
func TestJWTRestrictsPwdChangeRequiredToken(t *testing.T) {
	secret := "test-jwt-secret"
	restricted, err := auth.GenerateTokenAtWithPwdChangeRequired(1, "admin@user", 0, secret, nowForTest())
	if err != nil {
		t.Fatalf("generate restricted token: %v", err)
	}
	normal, err := auth.GenerateToken(1, "admin@user", 0, secret)
	if err != nil {
		t.Fatalf("generate normal token: %v", err)
	}
	newAuthState := func(userID int64) (*auth.UserAuthState, error) {
		return &auth.UserAuthState{ID: userID, RoleID: 0, Status: 1, PasswordChangedAt: 1}, nil
	}
	wrapped := JWT(AuthOptions{JWTSecret: secret, GetUserAuthState: newAuthState})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			response.WriteJSON(w, response.OK("pass"))
		}))

	serve := func(token, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Header.Set("Authorization", token)
		res := httptest.NewRecorder()
		wrapped.ServeHTTP(res, req)
		return res
	}

	// 受限 token：普通业务接口 403
	assertCodeMsg(t, serve(restricted, "/api/v1/tunnel/list"), 403, "请先修改默认管理员密码")
	// 受限 token：改密接口放行
	assertCodeMsg(t, serve(restricted, "/api/v1/user/updatePassword"), 0, "操作成功")
	// 普通 token：不受影响
	assertCodeMsg(t, serve(normal, "/api/v1/tunnel/list"), 0, "操作成功")
}

// M-05: 受限 token 的 claim 确实被签名保护，伪造会被拒。
func TestPwdChangeRequiredClaimIsSigned(t *testing.T) {
	secret := "test-jwt-secret"
	token, err := auth.GenerateTokenAtWithPwdChangeRequired(1, "admin@user", 0, secret, nowForTest())
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	claims, ok := auth.ValidateToken(token, secret)
	if !ok || !claims.PwdChangeRequired {
		t.Fatalf("restricted claim missing after round-trip")
	}
	if _, ok := auth.ValidateToken(token, "wrong-secret"); ok {
		t.Fatalf("token validated with wrong secret")
	}
	normal, err := auth.GenerateToken(1, "admin@user", 0, secret)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	if claims, ok := auth.ValidateToken(normal, secret); !ok || claims.PwdChangeRequired {
		t.Fatalf("normal token should not carry the restricted claim")
	}
}
