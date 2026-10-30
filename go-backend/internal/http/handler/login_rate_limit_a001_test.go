package handler

import (
	"fmt"
	"net/http"
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
	// hops=1 + 单条客户端条目：合法单跳形态（Caddy/nginx 同机→后端，
	// 回环对端、代理追加的真实客户端 IP 即该条目），采信条目。
	// 复查结论（B01-001 收窄）：此处原期望即编码该合法形态，v1 曾以
	// run==0 fail-closed 改严、误伤默认单跳拓扑，现按对端类型判别
	// 恢复原期望；自填轮换的封堵只针对公网直连对端（见
	// TestA001_DirectConnectionIgnoresXFF 与 C02 验收 1）。
	t.Setenv("FLVX_TRUSTED_PROXY_HOPS", "1")
	got := loginClientIP(loginReqWithXFF("127.0.0.1:1", "198.51.100.9"))
	if got.String() != "198.51.100.9" {
		t.Fatalf("hops=1: got %v, want 198.51.100.9", got)
	}

	t.Setenv("FLVX_TRUSTED_PROXY_HOPS", "2")
	// Caddy→nginx→backend：XFF="client, caddy"，取最左
	// R24-X-01 夹具修正：代理条目须落在可信范围（本例用私网 IP），
	// 否则链长估计无法将其确认为代理追加（见 trustedXFFChainLen）；
	// 断言一字不动——正确 2 跳链仍取客户端。
	got = loginClientIP(loginReqWithXFF("10.0.0.5:1", "198.51.100.9, 192.168.1.1"))
	if got.String() != "198.51.100.9" {
		t.Fatalf("hops=2: got %v, want 198.51.100.9", got)
	}
}

// 场景 3：解析健壮性。
// 复查结论（B01-001 收窄）：v1 曾把本用例改用两跳链行使解析路径
// （单条形态在当时一律 fail-closed）；收窄后单跳单条即合法形态，
// 恢复原始单条用例行使同一批 parseIPLiteral 路径，断言强度不变。
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
		{"empty", "", "127.0.0.1"},                // 回退 RemoteAddr
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

// 场景 5（R5-B5 D11-D13）：同一用户名跨 1000 个不同 IP，用户名桶触发信号，
// 但永不硬拒。D11-D13 要求：用户名桶只升级验证，不拒绝。
func TestA001_UsernameLimitAcrossIPs(t *testing.T) {
	resetLoginLimitersForTest()
	h := &Handler{}
	triggeredAt := -1
	for i := 0; i < 1000; i++ {
		// captcha 未启用场景：触发后收紧 IP，但不返回 429。
		if h.checkLoginUsernameRateLimit("VictimUser", fmt.Sprintf("10.0.0.%d", i%256), false) {
			triggeredAt = i + 1
			break
		}
	}
	if triggeredAt == -1 {
		t.Fatal("username bucket never triggered across 1000 attempts")
	}
	if triggeredAt != usernameRateLimitMaxAttempts+1 {
		t.Fatalf("triggered at attempt %d, want %d", triggeredAt, usernameRateLimitMaxAttempts+1)
	}
	// D11-D13.3: 绝不返回 429（无"锁定"响应）。
	// checkLoginUsernameRateLimit 不写 ResponseWriter，无 429 可能。
	// 成功登录后清除
	h.noteLoginUsernameSuccess("victimuser") // 大小写不敏感
	if h.checkLoginUsernameRateLimit("VictimUser", "10.0.0.1", false) {
		t.Fatal("username bucket not cleared after successful login")
	}
}

// 场景 5b（R5-B5 D11-D13）：用户名桶触发后，信号持续但永不硬拒。
// D11-D13 要求：退避只作用于验证强度，不作用于可用性。
func TestA001_UsernameSignalNotBlock(t *testing.T) {
	resetLoginLimitersForTest()
	h := &Handler{}
	now := time.Now()
	// 快速打满：触发阈值
	for i := 0; i < usernameRateLimitMaxAttempts+1; i++ {
		globalLoginUsernameLimiter.recordAttempt("u1", now)
	}
	// 触发后：返回 true（信号），但不写 429。
	if !h.checkLoginUsernameRateLimit("u1", "192.0.2.1", false) {
		t.Fatal("expected username bucket to signal triggered")
	}
	// D11-D13.3: 无 429 响应（函数不写 ResponseWriter）。
	// 窗口过后信号清除
	future := now.Add(usernameRateLimitWindow + time.Minute)
	if globalLoginUsernameLimiter.recordAttempt("u1", future) {
		t.Fatal("username signal should clear after window")
	}
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
