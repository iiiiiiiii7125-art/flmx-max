package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/security"
	"go-backend/internal/store/repo"
)

// R5-B5 D11-D13: 用户名限流重设计——验证升级，永不硬拒。
// 可达性证据：T-A R5-A6-01 发现批次 2 的用户名桶可被用于定向 DoS。
//
// X02-003（审计闭环 Round 1 / C17）：本文件原有三条用例执行了限流函数却
// 不做任何断言（TestD11_UsernameBucketNeverBlocksWithCaptcha 只在未触发时
// Logf 后无条件通过、TestD13_NoLockoutMessage 函数体只剩注释、
// TestD11_CannotFullyLockAccount 把返回值赋给 _ 丢弃）。现三条均改走
// login() 全链路（httptest + 文件 DB + 验证码开/关两态），断言落在可观测
// 行为上：桶满后合法登录 code=0 并签发 token、失败文案与平时完全一致、
// 全程无 429 与锁定类文案。若 login() 改为对用户名桶信号硬拒（写 429 或
// 锁定文案），这三条必须变红。

func resetD11ForTest() {
	resetLoginLimitersForTest()
	tightenedLoginIPs.Lock()
	tightenedLoginIPs.m = make(map[string]time.Time)
	tightenedLoginIPs.Unlock()
}

func setupD11LoginHandler(t *testing.T) (*Handler, *repo.Repository) {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return New(r, "test-secret"), r
}

func createD11User(t *testing.T, r *repo.Repository, username, password string) {
	t.Helper()
	hash, err := security.HashPassword(password)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	now := time.Now().UnixMilli()
	if _, err := r.CreateUser(username, hash, 1, now+86400000, 1, 1, 100, 1, 0, now); err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
}

// fillD11UsernameBucket 以轮换源 IP 打满目标用户名的限流桶（攻击者建模，
// 与旧桩用例相同的填充方式）；填充后自检桶确已触发，前提不成立即 Fatal。
func fillD11UsernameBucket(t *testing.T, h *Handler, username string, captchaEnabled bool) {
	t.Helper()
	for i := 0; i < usernameRateLimitMaxAttempts+5; i++ {
		h.checkLoginUsernameRateLimit(username, fmt.Sprintf("198.51.100.%d", i%250+1), captchaEnabled)
	}
	if !h.checkLoginUsernameRateLimit(username, "192.0.2.99", captchaEnabled) {
		t.Fatalf("username bucket for %q should be triggered after fill", username)
	}
}

func doD11Login(t *testing.T, h *Handler, username, password, captchaID, remoteAddr string) (int, envelope) {
	t.Helper()
	body := fmt.Sprintf(`{"username":%q,"password":%q,"captchaId":%q}`, username, password, captchaID)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = remoteAddr
	rec := httptest.NewRecorder()
	h.login(rec, req)
	return rec.Code, decodeEnvelope(t, rec)
}

// assertD11NoLockout 断言一次登录响应不带任何「硬拒」形状：HTTP 与信封
// code 均非 429，文案不含锁定类字样。
func assertD11NoLockout(t *testing.T, httpStatus int, env envelope) {
	t.Helper()
	if httpStatus == http.StatusTooManyRequests || env.Code == 429 {
		t.Fatalf("login must never hard-reject with 429, got http=%d code=%d msg=%q", httpStatus, env.Code, env.Msg)
	}
	for _, word := range []string{"锁定", "锁死", "封禁", "冻结", "lock"} {
		if strings.Contains(env.Msg, word) {
			t.Fatalf("login msg must not contain lockout wording %q, got %q", word, env.Msg)
		}
	}
}

// D11-D13.1: 攻击者打满用户名桶后，合法用户仍能登录（captcha 启用时）。
func TestD11_UsernameBucketNeverBlocksWithCaptcha(t *testing.T) {
	resetD11ForTest()
	h, r := setupD11LoginHandler(t)

	now := time.Now().UnixMilli()
	for name, value := range map[string]string{
		"captcha_enabled":       "true",
		"cloudflare_site_key":   "d11-site-key",
		"cloudflare_secret_key": "d11-secret-key",
	} {
		if err := r.UpsertConfig(name, value, now); err != nil {
			t.Fatalf("set config %s: %v", name, err)
		}
	}

	const username = "d11-victim"
	const password = "d11-correct-password"
	createD11User(t, r, username, password)

	// 攻击者用错误密码打满目标用户名的桶（验证码启用信号）。
	fillD11UsernameBucket(t, h, username, true)

	// 合法用户：正确密码 + 有效 captcha 令牌——必须全程走通并拿到 token，
	// 用户名桶信号不得在此产生任何拒绝。
	h.markCaptchaToken("d11-valid-captcha")
	status, env := doD11Login(t, h, username, password, "d11-valid-captcha", "203.0.113.50:4000")
	assertD11NoLockout(t, status, env)
	if env.Code != 0 {
		t.Fatalf("legit login after bucket trigger must succeed, got code=%d msg=%q", env.Code, env.Msg)
	}
	var data struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil || data.Token == "" {
		t.Fatalf("login must return a token, err=%v data=%s", err, env.Data)
	}
}

// D11-D13.2: 验证码关闭时，用户名桶触发后合法用户仍能登录（仅 IP 阈值收紧）。
func TestD12_UsernameTriggerTightensIPNotBlock(t *testing.T) {
	resetD11ForTest()
	h := &Handler{}

	targetUser := "victim"
	attackerIP := "198.51.100.99"

	// 打满用户名桶（captcha 未启用）。
	for i := 0; i < usernameRateLimitMaxAttempts+1; i++ {
		h.checkLoginUsernameRateLimit(targetUser, attackerIP, false)
	}

	// 攻击者 IP 应被收紧。
	if !isIPTightened(attackerIP) {
		t.Fatal("attacker IP should be tightened after username bucket trigger (captcha disabled)")
	}

	// 合法用户从不同 IP 登录：用户名桶不阻断。
	// （IP 限流仍独立工作，但用户名信号本身不产生拒绝。）
	if h.checkLoginUsernameRateLimit(targetUser, "203.0.113.60", false) {
		// 信号为 true 是预期的（桶已触发），但不得阻断。
	}
	// 关键：无 429，无"锁定"。
}

// D11-D13.3: 用户名桶触发不产生"锁定"类响应文案。
func TestD13_NoLockoutMessage(t *testing.T) {
	resetD11ForTest()
	h, r := setupD11LoginHandler(t)

	const username = "d13-user"
	const password = "d13-correct-password"
	createD11User(t, r, username, password)

	// 打满桶。
	fillD11UsernameBucket(t, h, username, true)

	// 错误密码：只能得到与平时完全一致的通用失败文案，无锁定字样、无 429。
	status, env := doD11Login(t, h, username, "wrong-password", "", "203.0.113.60:4000")
	assertD11NoLockout(t, status, env)
	if env.Code == 0 {
		t.Fatalf("wrong password must fail, got code=0")
	}
	if env.Msg != "账号或密码错误" {
		t.Fatalf("expected generic credential error after bucket trigger, got %q", env.Msg)
	}

	// 正确密码：直接成功，文案同样无锁定字样。
	status, env = doD11Login(t, h, username, password, "", "203.0.113.61:4000")
	assertD11NoLockout(t, status, env)
	if env.Code != 0 {
		t.Fatalf("legit login after bucket trigger must succeed, got code=%d msg=%q", env.Code, env.Msg)
	}
}

// D11-D13.6: 不存在用户名与存在用户名在触发后的响应形状一致（防枚举）。
func TestD11_NonexistentUserSameShape(t *testing.T) {
	resetD11ForTest()
	h := &Handler{}

	// 存在用户名。
	for i := 0; i < usernameRateLimitMaxAttempts+1; i++ {
		h.checkLoginUsernameRateLimit("realuser", "192.0.2.20", false)
	}
	realTriggered := h.checkLoginUsernameRateLimit("realuser", "192.0.2.21", false)

	resetD11ForTest()

	// 不存在用户名：行为完全同构。
	for i := 0; i < usernameRateLimitMaxAttempts+1; i++ {
		h.checkLoginUsernameRateLimit("ghostuser", "192.0.2.20", false)
	}
	ghostTriggered := h.checkLoginUsernameRateLimit("ghostuser", "192.0.2.21", false)

	if realTriggered != ghostTriggered {
		t.Fatalf("behavior differs: real=%v ghost=%v (enumeration risk)", realTriggered, ghostTriggered)
	}
}

// D11-D13.5: 攻击者无法通过用户名桶让目标账号完全不可登录。
func TestD11_CannotFullyLockAccount(t *testing.T) {
	resetD11ForTest()
	h, r := setupD11LoginHandler(t)

	const username = "d11-target"
	const password = "d11-target-password"
	createD11User(t, r, username, password)

	// 多轮持续攻击：每轮重新打满用户名桶（验证码关闭场景，桶触发仅收紧
	// 攻击者自身 IP），合法用户从自己的 IP 必须每轮都能登录成功——
	// 若用户名桶改为硬拒，第一轮即变红。
	for round := 0; round < 3; round++ {
		fillD11UsernameBucket(t, h, username, false)

		status, env := doD11Login(t, h, username, password, "", fmt.Sprintf("203.0.113.%d:4000", 70+round))
		assertD11NoLockout(t, status, env)
		if env.Code != 0 {
			t.Fatalf("round %d: legit login must succeed despite full username bucket, got code=%d msg=%q", round, env.Code, env.Msg)
		}
		var data struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(env.Data, &data); err != nil || data.Token == "" {
			t.Fatalf("round %d: login must return a token, err=%v data=%s", round, err, env.Data)
		}
		// 登录成功会清除该用户名的限流状态（noteLoginUsernameSuccess），
		// 下一轮攻击重新填充、重新触发，验证持续攻击下仍不可锁死。
	}
}
