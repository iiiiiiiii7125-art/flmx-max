package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// R10 批 6 回归（R10X-007 / R10X-008 / R10X-014）。
// 每条断言独立成案，禁以同批他条 PASS 顶替（N-35）。

// ---------------------------------------------------------------------------
// R10X-007（F-001）：nft 模式节点缺 node_ssh_config 行时 forwardCreate
// 回执必须以正本措辞「nftables 节点缺少 SSH 配置」点名，禁透传
// sql.ErrNoRows 字面量。四格对照：{无行,有行} × {带 ipSpeedId,不带}。
// ---------------------------------------------------------------------------

type r10b6ForwardFixture struct {
	h         *Handler
	tunnelID  int64
	speedID   int64
	hasSSH    bool
	withSpeed bool
}

func setupR10B6ForwardCell(t *testing.T, hasSSH, withSpeed bool) *r10b6ForwardFixture {
	t.Helper()
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	manager := &fakeNftablesManager{}
	h.nftablesManager = newSerializedNftManager(manager)
	if hasSSH {
		seedNftablesSSHConfig(t, h, fixture.nodeID)
	}
	tunnelID := seedTunnelForNftables(t, h, "r10b6-nft-tunnel", fixture.nodeID)
	if err := h.repo.DB().Exec(`
		INSERT INTO user_tunnel(user_id, tunnel_id, speed_id, num, flow, in_flow, out_flow, flow_reset_time, exp_time, status)
		VALUES(1, ?, NULL, 99999, 99999, 0, 0, 1, 2727251700000, 1)
	`, tunnelID).Error; err != nil {
		t.Fatalf("seed user_tunnel: %v", err)
	}
	fx := &r10b6ForwardFixture{h: h, tunnelID: tunnelID, hasSSH: hasSSH, withSpeed: withSpeed}
	if withSpeed {
		speedID, err := h.repo.CreateSpeedLimit("r10b6-limit", 100, time.Now().UnixMilli(), 1)
		if err != nil {
			t.Fatalf("create speed limit: %v", err)
		}
		fx.speedID = speedID
	}
	return fx
}

func (fx *r10b6ForwardFixture) create(t *testing.T) (int, string, []string, string) {
	t.Helper()
	payload := map[string]interface{}{
		"tunnelId":   fx.tunnelID,
		"name":       "r10b6-fwd",
		"remoteAddr": "203.0.113.9:8080",
		"inPort":     20000,
	}
	if fx.withSpeed {
		payload["ipSpeedId"] = fx.speedID
	}
	res := httptest.NewRecorder()
	fx.h.forwardCreate(res, newAuthenticatedJSONRequest(t, payload))
	body := res.Body.String()
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data *struct {
			Warnings []string `json:"warnings"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode forwardCreate response: %v body=%s", err, body)
	}
	var warnings []string
	if out.Data != nil {
		warnings = out.Data.Warnings
	}
	return out.Code, out.Msg, warnings, body
}

func (fx *r10b6ForwardFixture) forwardCount(t *testing.T) int {
	t.Helper()
	return mustQueryInt(t, fx.h.repo, `SELECT COUNT(*) FROM forward`)
}

func TestR10B6ForwardCreateNftMissingSSHConfigFourCells(t *testing.T) {
	// 格 C/D：无 SSH 配置行——回执点名、无 sql 字面量、无残留行（fail-closed 不变）。
	for _, cell := range []struct {
		name      string
		withSpeed bool
	}{
		{name: "missing-row with ipSpeedId", withSpeed: true},
		{name: "missing-row without ipSpeedId", withSpeed: false},
	} {
		t.Run(cell.name, func(t *testing.T) {
			fx := setupR10B6ForwardCell(t, false, cell.withSpeed)
			code, msg, _, body := fx.create(t)
			if code == 0 {
				t.Fatalf("R10X-007: create without SSH config row must fail, got code=0 body=%s", body)
			}
			if !strings.Contains(msg, "nftables 节点缺少 SSH 配置") {
				t.Fatalf("R10X-007: receipt must name the missing SSH config, got msg=%q body=%s", msg, body)
			}
			if strings.Contains(body, "sql: no rows in result set") || strings.Contains(body, "sql.ErrNoRows") {
				t.Fatalf("R10X-007: receipt must not leak sql.ErrNoRows literal, body=%s", body)
			}
			if n := fx.forwardCount(t); n != 0 {
				t.Fatalf("R10X-007: failed create must leave no forward row, got %d", n)
			}
		})
	}

	// 格 A：有行 + 带 ipSpeedId → 成功且 nft 限速警告回执不变。
	t.Run("with-row with ipSpeedId warns", func(t *testing.T) {
		fx := setupR10B6ForwardCell(t, true, true)
		code, _, warnings, body := fx.create(t)
		if code != 0 {
			t.Fatalf("R10X-007: create with SSH config row must succeed, body=%s", body)
		}
		found := false
		for _, w := range warnings {
			if strings.Contains(w, "nftables 模式不支持限速和连接数限制") {
				found = true
			}
		}
		if !found {
			t.Fatalf("R10X-007: nft ineffective-limits warning must be preserved, warnings=%v body=%s", warnings, body)
		}
		if n := fx.forwardCount(t); n != 1 {
			t.Fatalf("expected exactly one forward row, got %d", n)
		}
	})

	// 格 B：有行、不带 ipSpeedId → 成功、无警告。
	t.Run("with-row without ipSpeedId clean", func(t *testing.T) {
		fx := setupR10B6ForwardCell(t, true, false)
		code, _, warnings, body := fx.create(t)
		if code != 0 {
			t.Fatalf("R10X-007: create with SSH config row must succeed, body=%s", body)
		}
		if len(warnings) != 0 {
			t.Fatalf("expected no warnings, got %v body=%s", warnings, body)
		}
		if n := fx.forwardCount(t); n != 1 {
			t.Fatalf("expected exactly one forward row, got %d", n)
		}
	})
}

// ---------------------------------------------------------------------------
// R10X-008（A-01）：captcha 令牌池上限 1000 + 淘汰最旧、verify 端点
// IP 限流、secret 未配置时关闭 data=="ok" 回退（返回未启用语义）。
// ---------------------------------------------------------------------------

func openR10B6Handler(t *testing.T) *Handler {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/r10b6-captcha.db")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return New(r, "test-secret")
}

func captchaPoolSize(h *Handler) int {
	h.captchaMu.Lock()
	defer h.captchaMu.Unlock()
	return len(h.captchaTokens)
}

func callCaptchaVerify(t *testing.T, h *Handler, remoteAddr, id, data string) (int, string, string) {
	t.Helper()
	body, err := json.Marshal(map[string]string{"id": id, "data": data})
	if err != nil {
		t.Fatalf("marshal verify payload: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/captcha/verify", bytes.NewReader(body))
	req.RemoteAddr = remoteAddr
	res := httptest.NewRecorder()
	h.captchaVerify(res, req)
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	raw := res.Body.String()
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode captchaVerify response: %v body=%s", err, raw)
	}
	return out.Code, out.Msg, raw
}

func TestR10B6CaptchaTokenPoolCappedAt1000(t *testing.T) {
	h := openR10B6Handler(t)
	for i := 0; i < 3000; i++ {
		h.markCaptchaToken(fmt.Sprintf("tok-%d", i))
	}
	if n := captchaPoolSize(h); n != 1000 {
		t.Fatalf("R10X-008: captcha token pool must be capped at 1000, got %d", n)
	}
	h.captchaMu.Lock()
	_, oldestPresent := h.captchaTokens["tok-0"]
	_, newestPresent := h.captchaTokens["tok-2999"]
	h.captchaMu.Unlock()
	if oldestPresent {
		t.Fatal("R10X-008: oldest token must be evicted when pool is full")
	}
	if !newestPresent {
		t.Fatal("R10X-008: newest token must be retained when pool is full")
	}
	// 单次消费语义不变。
	if !h.consumeCaptchaToken("tok-2999") {
		t.Fatal("R10X-008: retained token must be consumable once")
	}
	if h.consumeCaptchaToken("tok-2999") {
		t.Fatal("R10X-008: token must not be consumable twice")
	}
}

func TestR10B6CaptchaVerifyDisabledWithoutSecret(t *testing.T) {
	h := openR10B6Handler(t)
	// 默认部署形态：未配置 cloudflare_secret_key。此前 data=="ok" 即
	// 铸令牌；修复后必须返回未启用语义且不铸令牌（BC-R10-07）。
	code, msg, body := callCaptchaVerify(t, h, "198.51.100.10:40001", "cap-1", "ok")
	if code == 0 {
		t.Fatalf("R10X-008: verify without Turnstile secret must not succeed via \"ok\" fallback, body=%s", body)
	}
	if !strings.Contains(msg, "未启用") {
		t.Fatalf("R10X-008: verify without secret must return not-enabled semantics, got msg=%q body=%s", msg, body)
	}
	if n := captchaPoolSize(h); n != 0 {
		t.Fatalf("R10X-008: no token may be minted while disabled, pool=%d", n)
	}
	if h.consumeCaptchaToken("cap-1") {
		t.Fatal("R10X-008: cap-1 must not be consumable")
	}
}

func TestR10B6CaptchaVerifyIPRateLimited(t *testing.T) {
	h := openR10B6Handler(t)
	const ip = "198.51.100.11:40002"
	rateLimited := 0
	succeeded := 0
	for i := 0; i < 40; i++ {
		code, _, _ := callCaptchaVerify(t, h, ip, fmt.Sprintf("rl-%d", i), "ok")
		switch code {
		case 429:
			rateLimited++
		case 0:
			succeeded++
		}
	}
	if succeeded != 0 {
		t.Fatalf("R10X-008: unauthenticated verify must never succeed without secret, got %d successes", succeeded)
	}
	if rateLimited == 0 {
		t.Fatal("R10X-008: verify endpoint must enforce an IP rate limit (no 429 in 40 rapid calls)")
	}
}

func TestR10B6CaptchaVerifyWithSecretStillWorks(t *testing.T) {
	h := openR10B6Handler(t)
	seedConfigValue(t, h.repo, "cloudflare_secret_key", "turnstile-secret")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err == nil && r.Form.Get("response") == "good-token" {
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":false}`))
	}))
	t.Cleanup(srv.Close)
	swapR6B5Turnstile(t, turnstileClient, srv.URL)

	code, _, body := callCaptchaVerify(t, h, "198.51.100.12:40003", "cap-ok", "good-token")
	if code != 0 {
		t.Fatalf("configured Turnstile path must succeed, body=%s", body)
	}
	if !h.consumeCaptchaToken("cap-ok") {
		t.Fatal("verified token must be minted and consumable once")
	}
	code, msg, body := callCaptchaVerify(t, h, "198.51.100.12:40003", "cap-bad", "bad-token")
	if code == 0 {
		t.Fatalf("failed Turnstile check must not succeed, body=%s", body)
	}
	if strings.Contains(msg, "未启用") {
		t.Fatalf("configured deployment must not report not-enabled, msg=%q", msg)
	}
	if h.consumeCaptchaToken("cap-bad") {
		t.Fatal("failed Turnstile check must not mint a token")
	}
}

// ---------------------------------------------------------------------------
// R10X-014（A-02）：读路径统一过 PolicyForConfig——未分类新键普通
// 用户读被拒；现行键全集（补分类后）普通用户读取回归不变；管理员
// 与匿名口径不变。
// ---------------------------------------------------------------------------

func r10b6NonAdminToken(t *testing.T, r *repo.Repository) string {
	t.Helper()
	past := time.Now().UnixMilli() - 10000
	uid, err := r.CreateUser("viewer_r10b6", "hash", 1, past+86400000*365, 100, 1, 100, 1, 0, past)
	if err != nil {
		t.Fatalf("create non-admin user: %v", err)
	}
	return mustGenerateConfigAccessToken(t, uid, "viewer_r10b6", 1)
}

func r10b6ConfigGet(t *testing.T, router http.Handler, token, name string) (int, string, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/config/get", bytes.NewBufferString(`{"name":"`+name+`"}`))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	raw := resp.Body.String()
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data *struct {
			Value string `json:"value"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode config/get response: %v body=%s", err, raw)
	}
	value := ""
	if out.Data != nil {
		value = out.Data.Value
	}
	return out.Code, out.Msg, value
}

func TestR10B6ConfigGetUnclassifiedKeyRejectedForNonAdmin(t *testing.T) {
	router, r := setupConfigAccessTestRouter(t)
	seedConfigValue(t, r, "r10b6_unclassified_key", "unclassified-value")
	userToken := r10b6NonAdminToken(t, r)
	adminToken := mustGenerateConfigAccessToken(t, 1, "admin_user", 0)

	code, _, _ := r10b6ConfigGet(t, router, userToken, "r10b6_unclassified_key")
	if code != 403 {
		t.Fatalf("R10X-014: unclassified key must be rejected for non-admin (fail-closed), got code=%d", code)
	}
	code, _, value := r10b6ConfigGet(t, router, adminToken, "r10b6_unclassified_key")
	if code != 0 || value != "unclassified-value" {
		t.Fatalf("R10X-014: admin read of unclassified key must be unchanged, got code=%d value=%q", code, value)
	}
}

func TestR10B6ConfigGetClassifiedKeysRegressionForNonAdmin(t *testing.T) {
	router, r := setupConfigAccessTestRouter(t)
	userToken := r10b6NonAdminToken(t, r)

	// 现行在用键全集（前端 config 页 + forward 页 + 监控面 + 后端引用，
	// 补分类后）：普通用户读取必须逐字不变。
	authenticatedKeys := []string{
		"forward_compact_mode", "forward_group_order_map", "forward_group_collapsed_map",
		"latency_targets",
		"monitor_tunnel_quality_enabled", "monitor_tunnel_quality_interval_sec", "monitor_retention_days",
		"captcha_enabled", "panel_domain", "ip",
		"github_proxy_enabled", "github_proxy_url", "allow_local_remote_addr",
		"notify_enabled", "notify_events", "notify_daily_hour",
		"notify_latency_loss_pct", "notify_latency_ms",
		"service_monitor_checker_scan_interval_sec", "service_monitor_worker_limit",
		"service_monitor_min_interval_sec", "service_monitor_default_interval_sec",
		"service_monitor_min_timeout_sec", "service_monitor_default_timeout_sec",
		"service_monitor_max_timeout_sec",
	}
	for _, key := range authenticatedKeys {
		seedConfigValue(t, r, key, "v-"+key)
		code, msg, value := r10b6ConfigGet(t, router, userToken, key)
		if code != 0 || value != "v-"+key {
			t.Fatalf("R10X-014 regression: non-admin read of in-use key %q changed: code=%d msg=%q value=%q", key, code, msg, value)
		}
	}
	// 公开键：普通用户与匿名均可读（不变）。
	seedConfigValue(t, r, "app_name", "BrandX")
	if code, _, value := r10b6ConfigGet(t, router, userToken, "app_name"); code != 0 || value != "BrandX" {
		t.Fatalf("R10X-014 regression: non-admin read of public key changed: code=%d value=%q", code, value)
	}
	if code, _, value := r10b6ConfigGet(t, router, "", "app_name"); code != 0 || value != "BrandX" {
		t.Fatalf("R10X-014 regression: anonymous read of public key changed: code=%d value=%q", code, value)
	}
	// 敏感键：普通用户仍 403（不变）。
	for _, key := range []string{"jwt_secret", "cloudflare_secret_key", "notify_telegram_bot_token", "notify_webhook_url", "node_sub_token"} {
		seedConfigValue(t, r, key, "s-"+key)
		if code, _, _ := r10b6ConfigGet(t, router, userToken, key); code != 403 {
			t.Fatalf("R10X-014 regression: sensitive key %q must stay 403 for non-admin, got %d", key, code)
		}
	}
}

func TestR10B6ConfigListFiltersUnclassifiedForNonAdmin(t *testing.T) {
	router, r := setupConfigAccessTestRouter(t)
	seedConfigValue(t, r, "r10b6_unclassified_key", "unclassified-value")
	seedConfigValue(t, r, "forward_compact_mode", "true")
	seedConfigValue(t, r, "jwt_secret", "jwt-value")
	userToken := r10b6NonAdminToken(t, r)
	adminToken := mustGenerateConfigAccessToken(t, 1, "admin_user", 0)

	list := func(token string) map[string]string {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/config/list", nil)
		req.Header.Set("Authorization", token)
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		var out struct {
			Code int               `json:"code"`
			Data map[string]string `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode config/list: %v", err)
		}
		if out.Code != 0 {
			t.Fatalf("config/list code=%d", out.Code)
		}
		return out.Data
	}

	userList := list(userToken)
	if _, ok := userList["r10b6_unclassified_key"]; ok {
		t.Fatal("R10X-014: unclassified key must not appear in non-admin config/list")
	}
	if _, ok := userList["jwt_secret"]; ok {
		t.Fatal("R10X-014 regression: sensitive key must not appear in non-admin config/list")
	}
	if userList["forward_compact_mode"] != "true" {
		t.Fatal("R10X-014 regression: classified in-use key must remain in non-admin config/list")
	}

	adminList := list(adminToken)
	if adminList["r10b6_unclassified_key"] != "unclassified-value" || adminList["forward_compact_mode"] != "true" {
		t.Fatalf("R10X-014 regression: admin config/list changed: %v", adminList)
	}
}
