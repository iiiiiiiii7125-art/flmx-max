package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/auth"
	"go-backend/internal/security"
	"go-backend/internal/store/repo"
)

// R1-12-01：新播种的初始口令（本例经 FLMX_ADMIN_PASSWORD 指定）
// 登录必须与旧字面量默认口令同口径——只发受限 token、
// requirePasswordChange=true；管理员改密后初始口令失效、
// 再登录为完整 token。旧实现下新播种用公开字面量 admin@user，
// 本用例对旧播种路径的关键差异是：口令值来自环境而非字面量，
// 且 isStoredDefaultPassword 必须经 repo 登记的候选命中。
func TestLoginWithInitialAdminPasswordRequiresChange(t *testing.T) {
	t.Setenv(repo.InitialAdminPasswordEnv, "initial-from-env-42")
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	login := func(password, ip string) (bool, auth.Claims, int) {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/login",
			strings.NewReader(`{"username":"admin@user","password":`+jsonQuote(password)+`}`))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = ip
		h.login(w, req)
		env := decodeEnvelope(t, w)
		if env.Code != 0 {
			return false, auth.Claims{}, env.Code
		}
		var data struct {
			Token                 string `json:"token"`
			RequirePasswordChange bool   `json:"requirePasswordChange"`
		}
		if err := json.Unmarshal(env.Data, &data); err != nil || data.Token == "" {
			t.Fatalf("login data: err=%v data=%s", err, env.Data)
		}
		claims, err := auth.ParseClaims(data.Token, "test-secret")
		if err != nil {
			t.Fatalf("parse token: %v", err)
		}
		return data.RequirePasswordChange, claims, 0
	}

	// 旧公开字面量对新播种必须登录失败。
	if _, _, code := login("admin@user", "198.51.100.251:4000"); code == 0 {
		t.Fatal("login with the old public literal must fail against a fresh seed")
	}

	// 初始口令登录成功，但只发受限 token。
	flag, claims, _ := login("initial-from-env-42", "198.51.100.252:4000")
	if !flag {
		t.Fatal("initial password login: requirePasswordChange must be true")
	}
	if !claims.PwdChangeRequired {
		t.Fatal("initial password login: JWT must carry pwd_change_required")
	}
	if !isStoredDefaultPassword(0, mustStoredHash(t, r, "admin@user")) {
		t.Fatal("stored-hash basis must agree (true) while the initial password is unchanged")
	}

	// 管理员改密后：初始口令失效、新口令登录为完整 token。
	newHash, err := security.HashPassword("operator-chosen-pw")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if err := r.UpdateUserPassword(1, newHash, time.Now().UnixMilli()); err != nil {
		t.Fatalf("update password: %v", err)
	}
	if _, _, code := login("initial-from-env-42", "198.51.100.253:4000"); code == 0 {
		t.Fatal("initial password must stop working after the admin password change")
	}
	flag, claims, _ = login("operator-chosen-pw", "198.51.100.254:4000")
	if flag || claims.PwdChangeRequired {
		t.Fatal("changed-password admin: requirePasswordChange must be false")
	}
	if isStoredDefaultPassword(0, mustStoredHash(t, r, "admin@user")) {
		t.Fatal("stored-hash basis must agree (false) after the password change")
	}
}
