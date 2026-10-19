package handler

import (
	"fmt"
	"net/http"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

// R5-B2 A-001 回归测试：登录限流信任模型。
//
// 旧缺陷：parseForwardedFor 取 XFF 最左条目。在默认反向代理部署
// （Caddy/nginx → 127.0.0.1:6365，RemoteAddr 为回环）下，攻击者每请求
// 伪造不同的最左 XFF 即得全新限流桶，M-04 被完全绕过。

func resetLoginLimitersForTest() {
	globalLoginLimiter = &loginRateLimiter{buckets: make(map[string]*loginAttemptBucket)}
	globalLoginUsernameLimiter = &loginUsernameLimiter{buckets: make(map[string]*usernameAttemptBucket)}
}

func loginReqWithXFF(remoteAddr, xff string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/user/login", nil)
	r.RemoteAddr = remoteAddr
	if xff != "" {
		r.Header.Set("X-Forwarded-For", xff)
	}
	return r
}

// 场景 1+4：伪造最左 XFF 不得产生新桶；直连时忽略 XFF。
// pre-fix（旧取最左实现）：第 11 次仍放行（红）。
func TestA001_SpoofedLeftmostXFFDoesNotEvadeIPLimit(t *testing.T) {
	t.Setenv("FLVX_TRUSTED_PROXY_HOPS", "1")
	resetLoginLimitersForTest()
	h := &Handler{}

	// 攻击者每次伪造不同的最左 XFF，但最右（可信代理看到的真实 IP）不变。
	blocked := false
	for i := 0; i < 20; i++ {
		xff := fmt.Sprintf("9.9.9.%d, 203.0.113.7", i%250+1)
		w := httptest.NewRecorder()
		if !h.checkLoginRateLimit(w, loginReqWithXFF("127.0.0.1:54321", xff)) {
			blocked = true
			if i < 10 {
				t.Fatalf("blocked too early at attempt %d", i+1)
			}
			break
		}
	}
	if !blocked {
		t.Fatal("spoofed leftmost XFF evaded the IP rate limit: 20 attempts all allowed (want 429 on 11th)")
	}
}

// 场景 4：公网直连时 XFF 必须被忽略。
func TestA001_DirectConnectionIgnoresXFF(t *testing.T) {
	t.Setenv("FLVX_TRUSTED_PROXY_HOPS", "1")
	got := loginClientIP(loginReqWithXFF("203.0.113.5:1234", "1.2.3.4"))
	if got == nil || got.String() != "203.0.113.5" {
		t.Fatalf("direct connection: got %v, want 203.0.113.5 (XFF must be ignored)", got)
	}
}

// 场景 2：hop=1 与 hop=2。
func TestA001_TrustedProxyHops(t *testing.T) {
	t.Setenv("FLVX_TRUSTED_PROXY_HOPS", "1")
	got := loginClientIP(loginReqWithXFF("127.0.0.1:1", "198.51.100.9"))
	if got.String() != "198.51.100.9" {
		t.Fatalf("hops=1: got %v, want 198.51.100.9", got)
	}

	t.Setenv("FLVX_TRUSTED_PROXY_HOPS", "2")
	// Caddy→nginx→backend：XFF="client, caddy"，取最左
	got = loginClientIP(loginReqWithXFF("10.0.0.5:1", "198.51.100.9, 192.0.2.1"))
	if got.String() != "198.51.100.9" {
		t.Fatalf("hops=2: got %v, want 198.51.100.9", got)
	}
}

// 场景 3：解析健壮性。
func TestA001_XFFParsingRobustness(t *testing.T) {
	t.Setenv("FLVX_TRUSTED_PROXY_HOPS", "1")
	cases := []struct {
		name string
		xff  string
		want string
	}{
		{"ipv4", "203.0.113.7", "203.0.113.7"},
		{"ipv4-port", "203.0.113.7:5678", "203.0.113.7"},
		{"ipv6", "2001:db8::1", "2001:db8::1"},
		{"ipv6-port", "[2001:db8::1]:443", "2001:db8::1"},
		{"blank-entries", "  203.0.113.7 ,, ", "203.0.113.7"},
		{"quoted", `"203.0.113.7"`, "203.0.113.7"},
		{"empty", "", "127.0.0.1"},       // 回退 RemoteAddr
		{"all-invalid", "not-an-ip", "127.0.0.1"}, // 回退 RemoteAddr
	}
	for _, tc := range cases {
		got := loginClientIP(loginReqWithXFF("127.0.0.1:1", tc.xff))
		if got == nil || got.String() != tc.want {
			t.Errorf("%s: got %v, want %s", tc.name, got, tc.want)
		}
	}
	// 超长头整体不可信 → 回退 RemoteAddr
	huge := ""
	for i := 0; i < 500; i++ {
		huge += "203.0.113.7, "
	}
	got := loginClientIP(loginReqWithXFF("127.0.0.1:1", huge))
	if got == nil || got.String() != "127.0.0.1" {
		t.Errorf("oversized header: got %v, want 127.0.0.1 fallback", got)
	}
}

// 场景 5：同一用户名跨 1000 个不同 IP，用户名限流仍触发。
func TestA001_UsernameLimitAcrossIPs(t *testing.T) {
	resetLoginLimitersForTest()
	h := &Handler{}
	blockedAt := -1
	for i := 0; i < 1000; i++ {
		w := httptest.NewRecorder()
		if !h.checkLoginUsernameRateLimit(w, "VictimUser") {
			blockedAt = i + 1
			break
		}
	}
	if blockedAt == -1 {
		t.Fatal("username rate limit never triggered across 1000 attempts")
	}
	if blockedAt != usernameRateLimitMaxAttempts+1 {
		t.Fatalf("blocked at attempt %d, want %d", blockedAt, usernameRateLimitMaxAttempts+1)
	}
	// 成功登录后清除
	h.noteLoginUsernameSuccess("victimuser") // 大小写不敏感
	w := httptest.NewRecorder()
	if !h.checkLoginUsernameRateLimit(w, "VictimUser") {
		t.Fatal("username bucket not cleared after successful login")
	}
}

// 场景 5b：退避而非永久锁；错误响应统一。
func TestA001_UsernameBackoffNotPermanent(t *testing.T) {
	resetLoginLimitersForTest()
	h := &Handler{}
	now := time.Now()
	// 快速打满：31 次后进入退避
	for i := 0; i < usernameRateLimitMaxAttempts+1; i++ {
		globalLoginUsernameLimiter.allow("u1", now)
	}
	w := httptest.NewRecorder()
	limited := !h.checkLoginUsernameRateLimit(w, "u1")
	if !limited {
		t.Fatal("expected username rate limit to trigger during backoff")
	}
	// 限流信号在信封 body 的 code 字段（HTTP 状态恒为 200，见 response.WriteJSON）
	var env struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode limit response: %v", err)
	}
	if env.Code != 429 {
		t.Fatalf("want envelope code 429 during backoff, got %d", env.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("missing Retry-After header on rate limit")
	}
	// 退避期过后（窗口+最大退避）恢复
	w2 := httptest.NewRecorder()
	future := now.Add(usernameRateLimitWindow + usernameBackoffMax + time.Minute)
	if !globalLoginUsernameLimiter.allow("u1", future) {
		t.Fatal("username still locked after backoff window; must not be permanent")
	}
	_ = w2
}

// 场景 6：桶满时不顶掉活跃桶（LRU），且有界。
func TestA001_BucketEvictionKeepsActiveBuckets(t *testing.T) {
	resetLoginLimitersForTest()
	now := time.Now()
	// 填满 10000 个活跃桶
	for i := 0; i < loginRateLimitMaxBuckets; i++ {
		globalLoginLimiter.allow(fmt.Sprintf("10.9.%d.%d", i/256, i%256), now)
	}
	if len(globalLoginLimiter.buckets) != loginRateLimitMaxBuckets {
		t.Fatalf("setup: got %d buckets", len(globalLoginLimiter.buckets))
	}
	// 触摸一个键使其成为最近使用
	globalLoginLimiter.allow("10.9.0.1", now.Add(time.Second))
	// 新增键：不得无界增长
	globalLoginLimiter.allow("192.0.2.99", now.Add(2*time.Second))
	if len(globalLoginLimiter.buckets) > loginRateLimitMaxBuckets {
		t.Fatalf("bucket map grew unbounded: %d > %d", len(globalLoginLimiter.buckets), loginRateLimitMaxBuckets)
	}
	// 最近使用的键必须还在（LRU 淘汰的是最旧的）
	globalLoginLimiter.mu.Lock()
	_, ok := globalLoginLimiter.buckets["10.9.0.1"]
	globalLoginLimiter.mu.Unlock()
	if !ok {
		t.Fatal("LRU evicted a recently-active bucket")
	}
}
