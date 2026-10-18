package handler

import (
	"net/http"
	"sync"
	"time"

	"go-backend/internal/http/response"
)

// M-04: 登录暴力破解防护。之前 /api/v1/user/login 无任何限流。
// 按客户端 IP 做滑动窗口限流（XFF 仅在可信代理后采信，复用 resolvePeerClientIP）。

const (
	loginRateLimitWindow       = time.Minute
	loginRateLimitMaxAttempts  = 10
	loginRateLimitMaxBuckets   = 10000
)

type loginAttemptBucket struct {
	count       int
	windowStart time.Time
}

type loginRateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*loginAttemptBucket
}

var globalLoginLimiter = &loginRateLimiter{buckets: make(map[string]*loginAttemptBucket)}

func (l *loginRateLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[key]
	if !ok || now.Sub(b.windowStart) >= loginRateLimitWindow {
		// 惰性清理过期桶，防止 map 无限增长
		if len(l.buckets) >= loginRateLimitMaxBuckets {
			for k, v := range l.buckets {
				if now.Sub(v.windowStart) >= loginRateLimitWindow {
					delete(l.buckets, k)
				}
			}
		}
		l.buckets[key] = &loginAttemptBucket{count: 1, windowStart: now}
		return true
	}
	b.count++
	return b.count <= loginRateLimitMaxAttempts
}

// checkLoginRateLimit 限流命中时直接写 429 并返回 false。
func (h *Handler) checkLoginRateLimit(w http.ResponseWriter, r *http.Request) bool {
	ip := resolvePeerClientIP(r)
	key := ""
	if ip != nil {
		key = ip.String()
	} else {
		// R-001: 降级时提取纯 IP，不含端口（防端口轮换绕过）
		// parseIPLiteral 处理 "IP:port" 和 "[IPv6]:port" 格式
		if parsed := parseIPLiteral(r.RemoteAddr); parsed != nil {
			key = parsed.String()
		} else {
			// 极端情况：无法解析则用 RemoteAddr（仍好于直接失败）
			key = r.RemoteAddr
		}
	}
	if !globalLoginLimiter.allow(key, time.Now()) {
		w.Header().Set("Retry-After", "60")
		response.WriteJSON(w, response.Err(429, "登录尝试过于频繁，请稍后再试"))
		return false
	}
	return true
}
