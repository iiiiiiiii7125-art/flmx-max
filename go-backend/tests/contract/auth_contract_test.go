package contract_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go-backend/internal/auth"
	"go-backend/internal/http/middleware"
	"go-backend/internal/http/response"
	"go-backend/internal/security"
)

func TestJWTMiddlewareContracts(t *testing.T) {
	secret := "unit-test-secret"

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response.WriteJSON(w, response.OK("pass"))
	})

	wrapped := middleware.JWT(middleware.AuthOptions{JWTSecret: secret, GetUserAuthState: func(userID int64) (*auth.UserAuthState, error) {
		return &auth.UserAuthState{ID: userID, RoleID: 0, Status: 1, PasswordChangedAt: 0}, nil
	}})(next)

	t.Run("login path is excluded", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/user/login", nil)
		res := httptest.NewRecorder()
		wrapped.ServeHTTP(res, req)
		assertCode(t, res, 0)
	})

	t.Run("missing token returns 401 contract message", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/tunnel/list", nil)
		res := httptest.NewRecorder()
		wrapped.ServeHTTP(res, req)
		assertCodeMsg(t, res, 401, "未登录或token已过期")
	})

	t.Run("invalid token returns 401 contract message", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/tunnel/list", nil)
		req.Header.Set("Authorization", "invalid.token.value")
		res := httptest.NewRecorder()
		wrapped.ServeHTTP(res, req)
		assertCodeMsg(t, res, 401, "无效的token或token已过期")
	})

	t.Run("valid token reaches next", func(t *testing.T) {
		token, err := auth.GenerateToken(1, "admin@user", 0, secret)
		if err != nil {
			t.Fatalf("generate token: %v", err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/tunnel/list", nil)
		req.Header.Set("Authorization", token)
		res := httptest.NewRecorder()
		wrapped.ServeHTTP(res, req)
		assertCode(t, res, 0)
	})

	t.Run("non-admin blocked on admin path", func(t *testing.T) {
		token, err := auth.GenerateToken(2, "normal_user", 1, secret)
		if err != nil {
			t.Fatalf("generate token: %v", err)
		}
		wrapped := middleware.JWT(middleware.AuthOptions{JWTSecret: secret, GetUserAuthState: func(userID int64) (*auth.UserAuthState, error) {
			return &auth.UserAuthState{ID: userID, RoleID: 1, Status: 1, PasswordChangedAt: 0}, nil
		}})(next)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/config/update", nil)
		req.Header.Set("Authorization", token)
		res := httptest.NewRecorder()
		wrapped.ServeHTTP(res, req)
		assertCodeMsg(t, res, 403, "权限不足，仅管理员可操作")
	})
}

func TestLoginTokenValidatesThroughRouter(t *testing.T) {
	router, r := setupContractRouter(t, "contract-jwt-secret")
	// 播种改 bcrypt（R-MD5 sunset）：本例主题是路由级登录，与
	// legacy 形态无关；legacy 登录行为由下方 sunset 合同专钉。
	seedBcryptUser(t, r, 9110, "router-login-user", "router-login-pass")

	body := bytes.NewBufferString(`{"username":"router-login-user","password":"router-login-pass","captchaId":""}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/login", body)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	var out response.R
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	if out.Code != 0 {
		t.Fatalf("expected login code 0, got %d (%s)", out.Code, out.Msg)
	}
	data, ok := out.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("expected login data map, got %T", out.Data)
	}
	token, _ := data["token"].(string)
	if token == "" {
		t.Fatal("expected login token")
	}

	checkReq := httptest.NewRequest(http.MethodPost, "/api/v1/tunnel/user/tunnel", nil)
	checkReq.Header.Set("Authorization", token)
	checkResp := httptest.NewRecorder()
	router.ServeHTTP(checkResp, checkReq)
	assertCode(t, checkResp, 0)
}

// R-MD5 sunset 取代原「login 透明迁移」合同：legacy 存储携正确
// 口令登录返回与错密相同的通用错误，账户收口为导入面终态；随后
// 经既有管理员通道（user/update 设新口令＋恢复启用）重置后，新
// 口令可正常登录——重置腿即最终用户实际走的路。
func TestLegacyPasswordSunsetOnLogin(t *testing.T) {
	router, r := setupContractRouter(t, "contract-jwt-secret")
	legacyChangedAt := seedLegacyUser(t, r, 9101, "legacy-login-user", "legacy-login-pass")

	login := func(password string) *httptest.ResponseRecorder {
		body := bytes.NewBufferString(`{"username":"legacy-login-user","password":"` + password + `","captchaId":""}`)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/user/login", body)
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		return resp
	}

	assertCodeMsg(t, login("legacy-login-pass"), -1, "账号或密码错误")

	user, err := r.GetUserByUsername("legacy-login-user")
	if err != nil || user == nil {
		t.Fatalf("get user: %v", err)
	}
	if security.IsLegacyPasswordHash(user.Pwd) {
		t.Fatalf("sunset must replace the legacy hash, got %q", user.Pwd)
	}
	if ok, legacy := security.VerifyPassword(user.Pwd, "legacy-login-pass"); ok || legacy {
		t.Fatalf("sunset replacement must not verify the old password, got (%v,%v)", ok, legacy)
	}
	if user.Status != 0 {
		t.Fatalf("sunset must disable the account pending admin reset, status=%d", user.Status)
	}
	if changedAt := mustQueryPasswordChangedAtByUsername(t, r, "legacy-login-user"); changedAt <= legacyChangedAt {
		t.Fatalf("expected password_changed_at to advance on sunset, got %d want > %d", changedAt, legacyChangedAt)
	}

	// 管理员重置腿：设新口令并恢复启用（与导入 legacy 账户同一通道）。
	adminToken, err := auth.GenerateToken(1, "admin@user", 0, "contract-jwt-secret")
	if err != nil {
		t.Fatalf("generate admin token: %v", err)
	}
	updateBody := bytes.NewBufferString(`{"id":9101,"user":"legacy-login-user","pwd":"reset-pass-1","flow":99999,"num":99999,"expTime":2727251700000,"flowResetTime":1,"status":1,"maxConn":0}`)
	updateReq := httptest.NewRequest(http.MethodPost, "/api/v1/user/update", updateBody)
	updateReq.Header.Set("Authorization", adminToken)
	updateReq.Header.Set("Content-Type", "application/json")
	updateResp := httptest.NewRecorder()
	router.ServeHTTP(updateResp, updateReq)
	assertCode(t, updateResp, 0)
	assertUserPasswordIsBcrypt(t, r, "legacy-login-user", "reset-pass-1")

	// 重置后：新口令可登、旧口令仍不可登。
	assertCode(t, login("reset-pass-1"), 0)
	assertCodeMsg(t, login("legacy-login-pass"), -1, "账号或密码错误")
}

func TestDisabledLegacyPasswordIsRejectedWithoutMigration(t *testing.T) {
	router, r := setupContractRouter(t, "contract-jwt-secret")
	legacyChangedAt := seedLegacyUserWithStatus(t, r, 9105, "disabled-legacy-user", "disabled-legacy-pass", 0)

	body := bytes.NewBufferString(`{"username":"disabled-legacy-user","password":"disabled-legacy-pass","captchaId":""}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/login", body)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	router.ServeHTTP(resp, req)
	assertCodeMsg(t, resp, -1, "账号被停用")

	user, err := r.GetUserByUsername("disabled-legacy-user")
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if user == nil {
		t.Fatal("expected disabled user to exist")
	}
	// 停用判定先于 sunset 处置：原 legacy 哈希须逐字保留（原探针
	// 经 VerifyPassword 验行，sunset 后该原语已不存在，改逐字比对）。
	if user.Pwd != legacyMD5HexForTest("disabled-legacy-pass") {
		t.Fatalf("expected disabled user to keep the original legacy MD5 hash, got %q", user.Pwd)
	}
	if changedAt := mustQueryPasswordChangedAtByUsername(t, r, "disabled-legacy-user"); changedAt != legacyChangedAt {
		t.Fatalf("expected password_changed_at to remain unchanged, got %d want %d", changedAt, legacyChangedAt)
	}
}

func assertCode(t *testing.T, rec *httptest.ResponseRecorder, expected int) {
	t.Helper()
	var out response.R
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.Code != expected {
		t.Fatalf("expected code %d, got %d", expected, out.Code)
	}
}

func assertCodeMsg(t *testing.T, rec *httptest.ResponseRecorder, expectedCode int, expectedMsg string) {
	t.Helper()
	var out response.R
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.Code != expectedCode || out.Msg != expectedMsg {
		t.Fatalf("expected (%d,%q), got (%d,%q)", expectedCode, expectedMsg, out.Code, out.Msg)
	}
}
