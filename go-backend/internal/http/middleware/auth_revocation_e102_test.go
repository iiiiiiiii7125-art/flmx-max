package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go-backend/internal/auth"
	"go-backend/internal/http/response"
)

// E1-02：服务端登出推进 TokensRevokedBefore 后，此前签发的 token
// 必须被中间件拒绝；水位之后重新登录签发的 token 必须放行（登出
// 不得逼改密、不得影响重新登录）。改密水位口径保持不变。
func TestJWTRejectsTokenAfterLogoutRevocation(t *testing.T) {
	secret := "middleware-revocation-secret"
	// 签发时刻取一小时前：token 本身未过期（exp = 签发 +7d），
	// 拒绝只能来自吊销水位，而非过期。
	issuedAt := time.Now().Add(-time.Hour)
	token, err := auth.GenerateTokenAt(1, "admin_user", 0, secret, issuedAt)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	claims, err := auth.ParseClaims(token, secret)
	if err != nil {
		t.Fatalf("parse claims: %v", err)
	}

	wrapped := JWT(AuthOptions{
		JWTSecret: secret,
		GetUserAuthState: func(userID int64) (*auth.UserAuthState, error) {
			return &auth.UserAuthState{
				ID: userID, RoleID: 0, Status: 1,
				PasswordChangedAt:   0,
				TokensRevokedBefore: claims.IatMs + 1,
			}, nil
		},
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response.WriteJSON(w, response.OK("pass"))
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tunnel/list", nil)
	req.Header.Set("Authorization", token)
	res := httptest.NewRecorder()
	wrapped.ServeHTTP(res, req)
	assertAuthDenied(t, res)
}

func TestJWTAcceptsTokenIssuedAfterLogoutRevocation(t *testing.T) {
	secret := "middleware-revocation-secret"
	revokedBefore := time.Now().Add(-time.Hour).UnixMilli()
	token, err := auth.GenerateTokenAt(1, "admin_user", 0, secret, time.UnixMilli(revokedBefore+1))
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	wrapped := JWT(AuthOptions{
		JWTSecret: secret,
		GetUserAuthState: func(userID int64) (*auth.UserAuthState, error) {
			return &auth.UserAuthState{
				ID: userID, RoleID: 0, Status: 1,
				PasswordChangedAt:   0,
				TokensRevokedBefore: revokedBefore,
			}, nil
		},
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response.WriteJSON(w, response.OK("pass"))
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tunnel/list", nil)
	req.Header.Set("Authorization", token)
	res := httptest.NewRecorder()
	wrapped.ServeHTTP(res, req)
	assertCode(t, res, 0)
}
