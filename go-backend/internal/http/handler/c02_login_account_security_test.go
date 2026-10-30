package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/auth"
	"go-backend/internal/http/middleware"
	"go-backend/internal/store/repo"
)

// Round-1 C02 回归测试（登录与账号安全）。
//
// B01-001（收窄口径，取代 v1 的 run==0 一律 fail-closed）：判别点
// 是直接对端类型。公网直连对端时 XFF 完全不可信、整体忽略，
// 轮换自填落 RemoteAddr 同一桶（验收 1）；回环/私网对端即本机或
// 容器网络的代理对端（默认单跳生产拓扑），沿用 hops 索引解析，
// 代理右侧追加的真实 IP 锚定限流键、不同真实 IP 不同桶、左侧
// 预置伪造条目无效（验收 2）；超配 hops 的 chainLen fail-closed
// 由 R24-A 守护、语义不回退（验收 3）。
// B01-002：管理员停用用户构成会话吊销——重新启用后，停用前签发
// 的 token 必须仍然 401，不得复活；新登录签发的 token 正常可用。

// B01-001 验收 1（解析层）：直接对端为公网地址时 XFF 完全不可信，
// 自填条目（单条/多条/轮换形态）一律忽略，解析值恒为 RemoteAddr。
func TestC02_PublicPeerSelfFilledXFFIgnored(t *testing.T) {
	t.Setenv("FLMX_TRUSTED_PROXY_HOPS", "1")
	for _, xff := range []string{"203.0.113.7", "198.51.100.9", "9.9.9.9, 203.0.113.7", "2001:db8::1"} {
		got := loginClientIP(loginReqWithXFF("203.0.113.5:1", xff))
		if got == nil || got.String() != "203.0.113.5" {
			t.Fatalf("public direct peer with XFF %q: loginClientIP = %v, want 203.0.113.5 (XFF ignored)", xff, got)
		}
	}
}

// B01-001 验收 1（限流层）：公网直连对端轮换自填 XFF 13 次——XFF
// 被整体忽略、全部落 RemoteAddr 同一桶，第 11 次起必须 429。
func TestC02_PublicPeerRotatingXFFRateLimited(t *testing.T) {
	t.Setenv("FLMX_TRUSTED_PROXY_HOPS", "1")
	resetLoginLimitersForTest()
	h := &Handler{}

	blockedAt := -1
	for i := 0; i < 13; i++ {
		xff := fmt.Sprintf("198.51.100.%d", i+1)
		w := httptest.NewRecorder()
		if !h.checkLoginRateLimit(w, loginReqWithXFF("203.0.113.5:54321", xff)) {
			blockedAt = i + 1
			break
		}
	}
	if blockedAt == -1 {
		t.Fatal("public peer rotating self-filled XFF evaded the IP rate limit: 13 attempts all allowed (want 429)")
	}
	if blockedAt != loginRateLimitMaxAttempts+1 {
		t.Fatalf("blocked at attempt %d, want %d (all attempts share the RemoteAddr bucket)", blockedAt, loginRateLimitMaxAttempts+1)
	}
}

// B01-001 验收 2：回环对端 + 单跳代理追加形态（默认生产拓扑）。
// 代理追加的真实 IP 即限流键：不同真实 IP 不同桶互不影响；攻击者
// 在真实 IP 左侧预置轮换伪造条目，索引仍锚定右侧追加的真实 IP、
// 同一真实 IP 的尝试恒落同一桶。
func TestC02_SingleHopProxyPeerResolvesRealClient(t *testing.T) {
	t.Setenv("FLMX_TRUSTED_PROXY_HOPS", "1")
	// 解析层：追加的真实 IP 被采信，不同真实 IP 解析出不同值。
	for _, tc := range []struct{ xff, want string }{
		{"198.51.100.9", "198.51.100.9"},
		{"198.51.100.10", "198.51.100.10"},
		// 左侧预置伪造条目不移动索引：取右侧追加的真实 IP。
		{"9.9.9.9, 203.0.113.7", "203.0.113.7"},
	} {
		got := loginClientIP(loginReqWithXFF("127.0.0.1:1", tc.xff))
		if got == nil || got.String() != tc.want {
			t.Fatalf("single-hop proxy XFF %q: loginClientIP = %v, want %s", tc.xff, got, tc.want)
		}
	}

	// 限流层隔离：真实客户端 A 打满自己的桶后，同一代理对端下的
	// 真实客户端 B 首尝仍放行（不同桶，互不影响）。
	resetLoginLimitersForTest()
	h := &Handler{}
	blockedAtA := -1
	for i := 0; i < 13; i++ {
		w := httptest.NewRecorder()
		if !h.checkLoginRateLimit(w, loginReqWithXFF("127.0.0.1:54321", "198.51.100.9")) {
			blockedAtA = i + 1
			break
		}
	}
	if blockedAtA != loginRateLimitMaxAttempts+1 {
		t.Fatalf("client A blocked at %d, want %d", blockedAtA, loginRateLimitMaxAttempts+1)
	}
	w := httptest.NewRecorder()
	if !h.checkLoginRateLimit(w, loginReqWithXFF("127.0.0.1:54321", "198.51.100.10")) {
		t.Fatal("client B must have its own bucket: first attempt via same proxy peer blocked by client A's limit")
	}

	// 预置轮换 + 同一真实 IP：恒落真实 IP 同一桶，第 11 次 429。
	resetLoginLimitersForTest()
	blockedAt := -1
	for i := 0; i < 13; i++ {
		xff := fmt.Sprintf("9.9.9.%d, 203.0.113.7", i+1)
		w := httptest.NewRecorder()
		if !h.checkLoginRateLimit(w, loginReqWithXFF("127.0.0.1:54321", xff)) {
			blockedAt = i + 1
			break
		}
	}
	if blockedAt != loginRateLimitMaxAttempts+1 {
		t.Fatalf("prefilled rotation over one real client: blocked at %d, want %d", blockedAt, loginRateLimitMaxAttempts+1)
	}
}

// B01-001 非回归：链内确有可信代理条目时（追加型多跳链），预置的
// 左侧轮换条目仍不得移动限流键——真实客户端条目恒定、第 11 次 429；
// 且 loginClientIP 仍解析出真实客户端（既有多跳行为不回退）。
func TestC02_AppendedChainStillResolvesAndLimits(t *testing.T) {
	t.Setenv("FLMX_TRUSTED_PROXY_HOPS", "2")
	got := loginClientIP(loginReqWithXFF("127.0.0.1:1", "9.9.9.9, 203.0.113.7, 172.30.0.5"))
	if got == nil || got.String() != "203.0.113.7" {
		t.Fatalf("appended chain: loginClientIP = %v, want 203.0.113.7", got)
	}

	resetLoginLimitersForTest()
	h := &Handler{}
	blockedAt := -1
	for i := 0; i < 13; i++ {
		xff := fmt.Sprintf("9.9.9.%d, 203.0.113.7, 172.30.0.5", i+1)
		w := httptest.NewRecorder()
		if !h.checkLoginRateLimit(w, loginReqWithXFF("127.0.0.1:54321", xff)) {
			blockedAt = i + 1
			break
		}
	}
	if blockedAt != loginRateLimitMaxAttempts+1 {
		t.Fatalf("appended chain: blocked at %d, want %d", blockedAt, loginRateLimitMaxAttempts+1)
	}
}

func c02CallUserUpdate(t *testing.T, h *Handler, payload map[string]interface{}) (int, string) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/update", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	h.userUpdate(rec, req)
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode userUpdate response: %v (body %q)", err, rec.Body.String())
	}
	return env.Code, env.Msg
}

func c02ProbeToken(t *testing.T, mw func(http.Handler) http.Handler, token string) int {
	t.Helper()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok"}`))
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/passkey/list", nil)
	req.Header.Set("Authorization", token)
	rec := httptest.NewRecorder()
	mw(next).ServeHTTP(rec, req)
	var env struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode probe response: %v (body %q)", err, rec.Body.String())
	}
	return env.Code
}

// B01-002 基准：停用→启用全链，旧 token 不得复活。
// pre-fix：启用后旧 token 直接 code=0（红）。
func TestC02_DisableUserRevokesTokensPermanently(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/c02.db")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	const secret = "c02-test-secret"
	h := New(r, secret)

	base := time.Now().Add(-2 * time.Hour)
	exp := time.Now().Add(24 * time.Hour).UnixMilli()
	if _, err := r.CreateUser("admin", "hash", 0, exp, 1, 1, 100, 1, 0, base.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	aliceID, err := r.CreateUser("alice", "hash", 1, exp, 1, 1, 100, 1, 0, base.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}

	// 停用前签发的 token（IatMs 晚于建号时的 PasswordChangedAt）。
	oldToken, err := auth.GenerateTokenAt(aliceID, "alice", 1, secret, base.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	mw := middleware.JWT(middleware.AuthOptions{
		JWTSecret:        secret,
		GetUserAuthState: r.GetUserAuthState,
	})
	if code := c02ProbeToken(t, mw, oldToken); code != 0 {
		t.Fatalf("pre-disable probe: code = %d, want 0 (token should be valid)", code)
	}

	// 停用：旧 token 立即失效（Status 门，既有行为）。
	if code, msg := c02CallUserUpdate(t, h, map[string]interface{}{"id": aliceID, "user": "alice", "status": 0}); code != 0 {
		t.Fatalf("disable userUpdate: code = %d (%s), want 0", code, msg)
	}
	if code := c02ProbeToken(t, mw, oldToken); code != 401 {
		t.Fatalf("post-disable probe: code = %d, want 401", code)
	}
	state, err := r.GetUserAuthState(aliceID)
	if err != nil {
		t.Fatal(err)
	}
	if state.PasswordChangedAt <= base.Add(time.Hour).UnixMilli() {
		t.Fatalf("disable must advance PasswordChangedAt past old token IatMs: got %d", state.PasswordChangedAt)
	}

	// 重新启用：旧 token 必须仍然失效（B01-002 的核心断言）。
	if code, msg := c02CallUserUpdate(t, h, map[string]interface{}{"id": aliceID, "user": "alice", "status": 1}); code != 0 {
		t.Fatalf("enable userUpdate: code = %d (%s), want 0", code, msg)
	}
	if code := c02ProbeToken(t, mw, oldToken); code != 401 {
		t.Fatalf("post-enable probe with pre-disable token: code = %d, want 401 (token must not revive)", code)
	}

	// 启用后新签发的 token（模拟重新登录）正常可用，不误伤。
	newToken, err := auth.GenerateTokenAt(aliceID, "alice", 1, secret, time.Now().Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if code := c02ProbeToken(t, mw, newToken); code != 0 {
		t.Fatalf("post-enable probe with fresh token: code = %d, want 0", code)
	}
}
