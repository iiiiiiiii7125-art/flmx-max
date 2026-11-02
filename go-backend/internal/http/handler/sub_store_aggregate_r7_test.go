package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// R7-L8-02 回归：sub_store 双桶——复合桶（IP+用户名，10/min）之
// 外并行纯 IP 聚合桶（100/min）封顶单 IP 轮换用户名的跨账号喷
// 洒；聚合桶与 login 桶隔离，刷爆 sub_store 不得锁死同 NAT 的
// 正常登录；两桶走 allowWithLimit，不受登录紧缩信号影响（紧缩
// 对复合键旧 allow() 永不命中的口径分叉已处置）。

func prefillAggregateBucket(t *testing.T, ip string, count int) {
	t.Helper()
	now := time.Now()
	subStoreIPAggregateLimiter.mu.Lock()
	defer subStoreIPAggregateLimiter.mu.Unlock()
	subStoreIPAggregateLimiter.buckets[ip] = &loginAttemptBucket{count: count, windowStart: now, lastSeen: now}
}

func subStorePost(t *testing.T, h *Handler, remoteAddr, user string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/open_api/sub_store",
		strings.NewReader(`{"user":"`+user+`","password":"wrong-pass","tunnel":"-1"}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = remoteAddr
	w := httptest.NewRecorder()
	h.openAPISubStore(w, req)
	return decodeEnvelopeCode(t, w.Body.Bytes())
}

// 单 IP 轮换用户名喷洒被聚合桶封顶：每个新用户名在复合桶里都
// 是新对（单独永远达不到 10/min），但聚合桶按纯 IP 计数，触顶
// 后同 IP 换任何用户名都 429；换 IP 不受影响。
func TestR7SubStoreIPAggregateCapsUsernameSpray(t *testing.T) {
	resetSubStoreLimiterForTest()
	resetLoginLimitersForTest()
	h := newSubStoreTestHandler(t)

	const remoteAddr = "203.0.113.50:9000"
	const ip = "203.0.113.50"
	// 预填到上限前一格：下一次尝试恰好用满，再下一次触顶。
	prefillAggregateBucket(t, ip, subStoreIPAggregateMaxAttempts-1)

	if code := subStorePost(t, h, remoteAddr, "spray-a"); code == 429 {
		t.Fatalf("attempt at aggregate cap-1 must pass the limiter, got 429")
	}
	if code := subStorePost(t, h, remoteAddr, "spray-b"); code != 429 {
		t.Fatalf("rotating-username spray beyond aggregate cap: got code %d, want 429", code)
	}
	if code := subStorePost(t, h, remoteAddr, "spray-c"); code != 429 {
		t.Fatalf("further spray from same IP: got code %d, want 429", code)
	}
	// 不同 IP 的正常拉取不被聚合桶误伤（复合桶与聚合桶都是新键）。
	if code := subStorePost(t, h, "203.0.113.51:9000", "spray-a"); code == 429 {
		t.Fatal("unrelated IP must not be limited by another IP's aggregate bucket")
	}
}

// 聚合桶耗尽只拒 sub_store 本面：同 IP 的 login 限流桶纹丝不动
// （原复合键设计要保的「刷 sub_store 不锁死同 NAT 登录」隔离在
// 加入聚合桶后仍成立——聚合桶独立于 globalLoginLimiter）。
func TestR7SubStoreAggregateExhaustionDoesNotLockLogin(t *testing.T) {
	resetSubStoreLimiterForTest()
	resetLoginLimitersForTest()
	h := newSubStoreTestHandler(t)

	const remoteAddr = "203.0.113.60:9000"
	const ip = "203.0.113.60"
	prefillAggregateBucket(t, ip, subStoreIPAggregateMaxAttempts)

	if code := subStorePost(t, h, remoteAddr, "anyone"); code != 429 {
		t.Fatalf("aggregate-exhausted sub_store: got code %d, want 429", code)
	}
	lw := httptest.NewRecorder()
	if !h.checkLoginRateLimit(lw, loginReqWithXFF(remoteAddr, "")) {
		t.Fatal("sub_store aggregate exhaustion leaked into the login limiter")
	}
}

// 紧缩口径分叉处置钉死：登录用户名信号把某 IP 加入紧缩集后，
// sub_store 两桶的阈值不变（复合桶仍 10/min、聚合桶仍 100/min）
// ——allowWithLimit 不查紧缩集；而 login 面 allow() 的紧缩语义
// 由 TestR4B01_TightenedJudgementUnchanged 继续钉住。
func TestR7SubStoreLimitersIgnoreLoginTightening(t *testing.T) {
	resetSubStoreLimiterForTest()
	resetD11ForTest()
	h := &Handler{}
	const ip = "203.0.113.70"
	h.tightenIPRateLimit(ip)
	if !isIPTightened(ip) {
		t.Fatal("precondition: IP must be tightened")
	}
	now := time.Now()
	composite := ip + "\x00" + "victim"
	for i := 1; i <= loginRateLimitMaxAttempts; i++ {
		if !subStoreLimiter.allowWithLimit(composite, now, loginRateLimitMaxAttempts) {
			t.Fatalf("composite attempt %d must be allowed (tightening must not shrink sub_store)", i)
		}
	}
	if subStoreLimiter.allowWithLimit(composite, now, loginRateLimitMaxAttempts) {
		t.Fatal("composite bucket must still cap at its own limit")
	}
	for i := 1; i <= subStoreIPAggregateMaxAttempts; i++ {
		if !subStoreIPAggregateLimiter.allowWithLimit(ip, now, subStoreIPAggregateMaxAttempts) {
			t.Fatalf("aggregate attempt %d must be allowed (tightening must not shrink sub_store)", i)
		}
	}
	if subStoreIPAggregateLimiter.allowWithLimit(ip, now, subStoreIPAggregateMaxAttempts) {
		t.Fatal("aggregate bucket must still cap at its own limit")
	}
}
