package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go-backend/internal/auth"
	"go-backend/internal/http/middleware"
	"go-backend/internal/http/response"
	"go-backend/internal/store/repo"
)

func TestPublicConfigGetAllowsBrandKeys(t *testing.T) {
	router, r := setupConfigAccessTestRouter(t)
	seedConfigValue(t, r, "app_name", "FLVX Brand")
	seedConfigValue(t, r, "app_logo", "logo-data")
	seedConfigValue(t, r, "app_favicon", "favicon-data")
	seedConfigValue(t, r, "app_bg_image", "bg-data")
	seedConfigValue(t, r, "app_bg_image_light", "light-bg-data")
	seedConfigValue(t, r, "app_bg_image_dark", "dark-bg-data")
	seedConfigValue(t, r, "cloudflare_site_key", "site-key")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/public/config/get", bytes.NewBufferString(`{"name":"app_name"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	router.ServeHTTP(resp, req)

	assertHandlerCode(t, resp, 0)
	for name, want := range map[string]string{
		"app_bg_image_light": "light-bg-data",
		"app_bg_image_dark":  "dark-bg-data",
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/public/config/get", bytes.NewBufferString(`{"name":"`+name+`"}`))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		assertHandlerConfigValue(t, resp, name, want)
	}
}

func TestPublicBrandConfigReturnsSavedValues(t *testing.T) {
	router, r := setupConfigAccessTestRouter(t)
	seedConfigValue(t, r, "app_name", "My Panel")
	seedConfigValue(t, r, "app_logo", "logo-data")
	seedConfigValue(t, r, "app_favicon", "favicon-data")
	seedConfigValue(t, r, "hide_footer_brand", "true")
	// A leftover flag from the removed commercial license must not matter.
	seedConfigValue(t, r, "is_commercial", "false")

	for name, want := range map[string]string{
		"app_name":          "My Panel",
		"app_logo":          "logo-data",
		"app_favicon":       "favicon-data",
		"hide_footer_brand": "true",
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/public/config/get", bytes.NewBufferString(`{"name":"`+name+`"}`))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		assertHandlerConfigValue(t, resp, name, want)
	}
}

func TestPublicConfigGetRejectsSensitiveKeys(t *testing.T) {
	router, _ := setupConfigAccessTestRouter(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/public/config/get", bytes.NewBufferString(`{"name":"jwt_secret"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	router.ServeHTTP(resp, req)

	assertHandlerCodeMsg(t, resp, 403, "禁止访问敏感配置")
}

func TestConfigGetAllowsPublicCloudflareSiteKeyWithoutAuthForCachedLoginPage(t *testing.T) {
	router, r := setupConfigAccessTestRouter(t)
	seedConfigValue(t, r, "cloudflare_site_key", "site-key")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/config/get", bytes.NewBufferString(`{"name":"cloudflare_site_key"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	router.ServeHTTP(resp, req)

	assertHandlerConfigValue(t, resp, "cloudflare_site_key", "site-key")
}

func TestConfigGetRejectsSensitiveKeysWithoutAuth(t *testing.T) {
	router, _ := setupConfigAccessTestRouter(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/config/get", bytes.NewBufferString(`{"name":"jwt_secret"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	router.ServeHTTP(resp, req)

	assertHandlerCodeMsg(t, resp, 403, "禁止访问敏感配置")
}

// 未落入任何关闭集的 sensitive 键管理员可读仍是既有行为：
// license_expiry（到期日）、notify_telegram_chat_id（标识符）、
// node_sub_token（legacy 键）经 R7-L8-03 评估明示不在关闭范围，
// 本测试把这一边界钉死——关多了（误伤三键）与关少了（四凭据键
// 漏关，见 TestConfigGetNeverReturnsThirdPartyCredentials）都红。
// jwt_secret 已由 R6-L8-F-05 单独收紧，见
// TestConfigGetNeverReturnsJWTSecret；cloudflare_secret_key 原为
// 本测试的样本键，已随 R7-L8-03 关闭。
func TestConfigGetAllowsSensitiveKeysForAdmin(t *testing.T) {
	router, r := setupConfigAccessTestRouter(t)
	adminToken := mustGenerateConfigAccessToken(t, 1, "admin_user", 0)

	for name, value := range map[string]string{
		"license_expiry":          "2027-01-01",
		"notify_telegram_chat_id": "123456789",
		"node_sub_token":          "legacy-sub-token",
	} {
		seedConfigValue(t, r, name, value)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/config/get", bytes.NewBufferString(`{"name":"`+name+`"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", adminToken)
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		assertHandlerConfigValue(t, resp, name, value)
	}
}

// R7-L8-03：第三方凭据读面关闭——cloudflare_secret_key、
// notify_telegram_bot_token、notify_webhook_url、
// notify_telegram_api_base 与 jwt/gist/license 六键同口径：
// config/get 对管理员亦 403、config/list 对管理员亦删除，本体
// 绝不出现在任何响应中。非管理员/匿名维持既有 sensitive 守卫
// 403。写路径不变（管理员可写仍由
// TestConfigUpdateAllowsCloudflareSecretKeyWrite 等用例钉住）。
func TestConfigGetNeverReturnsThirdPartyCredentials(t *testing.T) {
	router, r := setupConfigAccessTestRouter(t)
	adminToken := mustGenerateConfigAccessToken(t, 1, "admin_user", 0)
	secrets := map[string]string{
		"cloudflare_secret_key":     "turnstile-secret-r7",
		"notify_telegram_bot_token": "123456:bot-token-r7",
		"notify_webhook_url":        "https://hooks.example.com/r7-secret",
		"notify_telegram_api_base":  "https://tg-proxy.example.com/r7-secret",
	}
	// 门禁登记：生产关闭集 thirdPartyCredentialConfigKeys 必须与
	// 本测试的期望集逐一相等——增删生产清单而不同步本测试即红，
	// 且下方逐键断言直接覆盖生产清单全集，不靠第二份拷贝对齐。
	if len(thirdPartyCredentialConfigKeys) != len(secrets) {
		t.Fatalf("thirdPartyCredentialConfigKeys = %v, want exactly the %d keys pinned by this test", thirdPartyCredentialConfigKeys, len(secrets))
	}
	for _, key := range thirdPartyCredentialConfigKeys {
		if _, ok := secrets[key]; !ok {
			t.Fatalf("thirdPartyCredentialConfigKeys contains unpinned key %q — extend this test's expected set", key)
		}
	}
	for name, value := range secrets {
		seedConfigValue(t, r, name, value)
	}
	// 对照组：不在关闭范围的三键须仍在管理员 list 中出现。
	seedConfigValue(t, r, "license_expiry", "2027-01-01")
	seedConfigValue(t, r, "notify_telegram_chat_id", "123456789")
	seedConfigValue(t, r, "node_sub_token", "legacy-sub-token")

	for name := range secrets {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/config/get", bytes.NewBufferString(`{"name":"`+name+`"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", adminToken)
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		assertHandlerCodeMsg(t, resp, 403, "禁止访问第三方凭据")
		if strings.Contains(resp.Body.String(), secrets[name]) {
			t.Fatalf("config/get %s response must not contain the credential: %s", name, resp.Body.String())
		}

		// 非管理员与匿名：既有 PolicyForConfig 敏感守卫 403，口径不变。
		userToken := mustGenerateConfigAccessToken(t, 2, "normal_user", 1)
		for _, token := range []string{userToken, ""} {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/config/get", bytes.NewBufferString(`{"name":"`+name+`"}`))
			req.Header.Set("Content-Type", "application/json")
			if token != "" {
				req.Header.Set("Authorization", token)
			}
			resp := httptest.NewRecorder()
			router.ServeHTTP(resp, req)
			assertHandlerCodeMsg(t, resp, 403, "禁止访问敏感配置")
		}
	}

	// config/list：管理员亦缺席四键、本体不出现在响应中；对照三键在场。
	req := httptest.NewRequest(http.MethodPost, "/api/v1/config/list", nil)
	req.Header.Set("Authorization", adminToken)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	body := resp.Body.String()
	for name, value := range secrets {
		if strings.Contains(body, name) || strings.Contains(body, value) {
			t.Fatalf("config/list must not contain %s or its value: %s", name, body)
		}
	}
	var out struct {
		Code int               `json:"code"`
		Data map[string]string `json:"data"`
	}
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&out); err != nil {
		t.Fatalf("decode config/list: %v", err)
	}
	for _, name := range []string{"license_expiry", "notify_telegram_chat_id", "node_sub_token"} {
		if _, ok := out.Data[name]; !ok {
			t.Fatalf("config/list must still contain non-closed key %s: %s", name, body)
		}
	}
}

// R6-L8-F-05：jwt_secret 与 Gist 凭据同口径——管理员经 config/get
// 亦不得读回本体（跨用户凭据：泄露后可离线伪造任意用户令牌，
// 且改密不影响该秘密），config/list 侧同步缺席。写路径后由
// R1-11-02 单独收紧为点名拒绝（DB 行零消费、假成功误导轮换），
// 见 TestConfigUpdateRejectsJWTSecretWrite。
func TestConfigGetNeverReturnsJWTSecret(t *testing.T) {
	router, r := setupConfigAccessTestRouter(t)
	adminToken := mustGenerateConfigAccessToken(t, 1, "admin_user", 0)
	seedConfigValue(t, r, "jwt_secret", "jwt-secret-body")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/config/get", bytes.NewBufferString(`{"name":"jwt_secret"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", adminToken)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	assertHandlerCodeMsg(t, resp, 403, "禁止访问 JWT 签名密钥")
	if strings.Contains(resp.Body.String(), "jwt-secret-body") {
		t.Fatalf("config/get response must not contain the jwt secret: %s", resp.Body.String())
	}

	// 对照：config/list 对管理员亦缺席该键、本体不出现在响应中。
	req = httptest.NewRequest(http.MethodPost, "/api/v1/config/list", nil)
	req.Header.Set("Authorization", adminToken)
	resp = httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if strings.Contains(resp.Body.String(), "jwt-secret-body") || strings.Contains(resp.Body.String(), "jwt_secret") {
		t.Fatalf("config/list must not contain jwt_secret: %s", resp.Body.String())
	}
}

func TestConfigGetNeverReturnsLicenseCredentials(t *testing.T) {
	router, r := setupConfigAccessTestRouter(t)
	adminToken := mustGenerateConfigAccessToken(t, 1, "admin_user", 0)
	seedConfigValue(t, r, "license_key", "license-secret")
	seedConfigValue(t, r, "machine_fingerprint", "fingerprint-secret")

	for _, name := range []string{"license_key", "license_machine_id", "machine_fingerprint"} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/config/get", bytes.NewBufferString(`{"name":"`+name+`"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", adminToken)
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		assertHandlerCodeMsg(t, resp, 403, "禁止访问系统授权凭据")
	}
}

// R14-F01（R14-X-01 / A3-01 / A8-01）：管理员经单键 config/get 亦不得
// 读回 gist_token / gist_password 明文，与 config/list 侧 delete +
// gist/status tokenHint-only 设计对齐。边界：其余 sensitive 键管理
// 员可读是既有行为，见 TestConfigGetAllowsSensitiveKeysForAdmin；
// jwt_secret 当时同列既有行为，后由 R6-L8-F-05 按同一口径单独
// 收紧，见 TestConfigGetNeverReturnsJWTSecret。
func TestConfigGetNeverReturnsGistCredentials(t *testing.T) {
	router, r := setupConfigAccessTestRouter(t)
	adminToken := mustGenerateConfigAccessToken(t, 1, "admin_user", 0)
	seedConfigValue(t, r, "gist_token", "gist-token-secret")
	seedConfigValue(t, r, "gist_password", "gist-password-secret")

	for _, name := range []string{"gist_token", "gist_password"} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/config/get", bytes.NewBufferString(`{"name":"`+name+`"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", adminToken)
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		assertHandlerCodeMsg(t, resp, 403, "禁止访问 Gist 凭据，请经 gist/status 查看")
		if strings.Contains(resp.Body.String(), "gist-token-secret") || strings.Contains(resp.Body.String(), "gist-password-secret") {
			t.Fatalf("config/get %s response must not contain gist credential: %s", name, resp.Body.String())
		}
	}

	// 对照：config/list 仍缺席两键；gist/status 仍只回 tokenHint。
	req := httptest.NewRequest(http.MethodPost, "/api/v1/config/list", nil)
	req.Header.Set("Authorization", adminToken)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if strings.Contains(resp.Body.String(), "gist-token-secret") || strings.Contains(resp.Body.String(), "gist-password-secret") {
		t.Fatalf("config/list must not contain gist credentials: %s", resp.Body.String())
	}

	// 对照：匿名读 gist 键仍 403（既有 PolicyForConfig 敏感守卫，与修前一致）。
	for _, name := range []string{"gist_token", "gist_password"} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/config/get", bytes.NewBufferString(`{"name":"`+name+`"}`))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		assertHandlerCodeMsg(t, resp, 403, "禁止访问敏感配置")
	}
}

func TestConfigListNeverReturnsLicenseCredentials(t *testing.T) {
	router, r := setupConfigAccessTestRouter(t)
	adminToken := mustGenerateConfigAccessToken(t, 1, "admin_user", 0)
	seedConfigValue(t, r, "license_key", "license-secret")
	seedConfigValue(t, r, "license_machine_id", "machine-id")
	seedConfigValue(t, r, "machine_fingerprint", "fingerprint-secret")
	seedConfigValue(t, r, "is_commercial", "true")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/config/list", nil)
	req.Header.Set("Authorization", adminToken)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	var out struct {
		Code int               `json:"code"`
		Data map[string]string `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.Code != 0 || out.Data["is_commercial"] != "true" {
		t.Fatalf("unexpected config response: %+v", out)
	}
	if _, ok := out.Data["license_key"]; ok {
		t.Fatal("license_key must not be returned")
	}
	if _, ok := out.Data["license_machine_id"]; ok {
		t.Fatal("license_machine_id must not be returned")
	}
	if _, ok := out.Data["machine_fingerprint"]; ok {
		t.Fatal("machine_fingerprint must not be returned")
	}
}

// R1-11-02 契约变更：jwt_secret 的 DB 行零消费，此前两条用例断言
// 管理员写 jwt_secret 成功——那正是审计指出的「假成功」缺陷。现
// 写面点名拒绝（真源为 env/文件），改按拒绝契约断言；其余敏感键
// （cloudflare_secret_key 等）的管理员可写由相邻用例继续覆盖。
func TestConfigUpdateRejectsJWTSecretWrite(t *testing.T) {
	router, r := setupConfigAccessTestRouter(t)
	adminToken := mustGenerateConfigAccessToken(t, 1, "admin_user", 0)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/config/update", bytes.NewBufferString(`{"jwt_secret":"rotated-secret"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", adminToken)
	resp := httptest.NewRecorder()

	router.ServeHTTP(resp, req)

	assertHandlerCodeMsgContains(t, resp, "jwt_secret")
	if cfg, err := r.GetConfigByName("jwt_secret"); err != nil || cfg != nil {
		t.Fatalf("jwt_secret should not be written, got %#v err=%v", cfg, err)
	}
}

func TestConfigUpdateSingleRejectsJWTSecretWrite(t *testing.T) {
	router, r := setupConfigAccessTestRouter(t)
	adminToken := mustGenerateConfigAccessToken(t, 1, "admin_user", 0)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/config/update-single", bytes.NewBufferString(`{"name":"jwt_secret","value":"rotated-secret"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", adminToken)
	resp := httptest.NewRecorder()

	router.ServeHTTP(resp, req)

	assertHandlerCodeMsgContains(t, resp, "jwt_secret")
	if cfg, err := r.GetConfigByName("jwt_secret"); err != nil || cfg != nil {
		t.Fatalf("jwt_secret should not be written, got %#v err=%v", cfg, err)
	}
}

func TestConfigUpdateAllowsCloudflareSecretKeyWrite(t *testing.T) {
	router, r := setupConfigAccessTestRouter(t)
	adminToken := mustGenerateConfigAccessToken(t, 1, "admin_user", 0)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/config/update", bytes.NewBufferString(`{"cloudflare_secret_key":"turnstile-secret"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", adminToken)
	resp := httptest.NewRecorder()

	router.ServeHTTP(resp, req)

	assertHandlerCode(t, resp, 0)

	cfg, err := r.GetConfigByName("cloudflare_secret_key")
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if cfg == nil || cfg.Value != "turnstile-secret" {
		t.Fatalf("expected cloudflare_secret_key to be updated, got %#v", cfg)
	}
}

func TestConfigUpdateSingleAllowsCloudflareSecretKeyWrite(t *testing.T) {
	router, r := setupConfigAccessTestRouter(t)
	adminToken := mustGenerateConfigAccessToken(t, 1, "admin_user", 0)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/config/update-single", bytes.NewBufferString(`{"name":"cloudflare_secret_key","value":"turnstile-secret"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", adminToken)
	resp := httptest.NewRecorder()

	router.ServeHTTP(resp, req)

	assertHandlerCode(t, resp, 0)

	cfg, err := r.GetConfigByName("cloudflare_secret_key")
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if cfg == nil || cfg.Value != "turnstile-secret" {
		t.Fatalf("expected cloudflare_secret_key to be updated, got %#v", cfg)
	}
}

func TestConfigUpdateRejectsLicenseKeyWrite(t *testing.T) {
	router, r := setupConfigAccessTestRouter(t)
	adminToken := mustGenerateConfigAccessToken(t, 1, "admin_user", 0)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/config/update", bytes.NewBufferString(`{"license_key":"license-secret"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", adminToken)
	resp := httptest.NewRecorder()

	router.ServeHTTP(resp, req)

	assertHandlerCodeMsg(t, resp, -1, "该配置由系统管理")
	if cfg, err := r.GetConfigByName("license_key"); err != nil || cfg != nil {
		t.Fatalf("license_key should not be written, got %#v err=%v", cfg, err)
	}
}

func TestConfigUpdateSingleRejectsLicenseKeyWrite(t *testing.T) {
	router, r := setupConfigAccessTestRouter(t)
	adminToken := mustGenerateConfigAccessToken(t, 1, "admin_user", 0)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/config/update-single", bytes.NewBufferString(`{"name":"license_key","value":"license-secret"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", adminToken)
	resp := httptest.NewRecorder()

	router.ServeHTTP(resp, req)

	assertHandlerCodeMsg(t, resp, -1, "该配置由系统管理")
	if cfg, err := r.GetConfigByName("license_key"); err != nil || cfg != nil {
		t.Fatalf("license_key should not be written, got %#v err=%v", cfg, err)
	}
}

func setupConfigAccessTestRouter(t *testing.T) (http.Handler, *repo.Repository) {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/config-access.db")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		_ = r.Close()
	})

	h := New(r, "unit-test-secret")
	mux := http.NewServeMux()
	h.Register(mux)
	wrapped := middleware.Recover(mux)
	wrapped = middleware.JWT(middleware.AuthOptions{JWTSecret: "unit-test-secret", GetUserAuthState: h.GetUserAuthState})(wrapped)
	wrapped = middleware.RequestLog(wrapped)
	wrapped = middleware.CORS(wrapped)
	return wrapped, r
}

func seedConfigValue(t *testing.T, r *repo.Repository, name, value string) {
	t.Helper()
	if err := r.DB().Exec(`INSERT INTO vite_config(name, value, time) VALUES(?, ?, 0) ON CONFLICT(name) DO UPDATE SET value = excluded.value, time = excluded.time`, name, value).Error; err != nil {
		t.Fatalf("seed config %s: %v", name, err)
	}
}

func mustGenerateConfigAccessToken(t *testing.T, userID int64, username string, roleID int) string {
	t.Helper()
	token, err := auth.GenerateToken(userID, username, roleID, "unit-test-secret")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	return token
}

func assertHandlerCode(t *testing.T, rec *httptest.ResponseRecorder, expected int) {
	t.Helper()
	var out response.R
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.Code != expected {
		t.Fatalf("expected code %d, got %d", expected, out.Code)
	}
}

func assertHandlerCodeMsg(t *testing.T, rec *httptest.ResponseRecorder, expectedCode int, expectedMsg string) {
	t.Helper()
	var out response.R
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.Code != expectedCode || out.Msg != expectedMsg {
		t.Fatalf("expected (%d,%q), got (%d,%q)", expectedCode, expectedMsg, out.Code, out.Msg)
	}
}

func assertHandlerConfigValue(t *testing.T, rec *httptest.ResponseRecorder, expectedName, expectedValue string) {
	t.Helper()
	var out struct {
		Code int `json:"code"`
		Data struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.Code != 0 {
		t.Fatalf("expected code 0, got %d", out.Code)
	}
	if out.Data.Name != expectedName || out.Data.Value != expectedValue {
		t.Fatalf("expected config (%q,%q), got (%q,%q)", expectedName, expectedValue, out.Data.Name, out.Data.Value)
	}
}
