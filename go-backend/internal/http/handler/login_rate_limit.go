package handler

import (
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"go-backend/internal/http/response"
)

// R5-B2 A-001: 登录限流信任模型重写。
//
// 背景：旧实现取 XFF 最左条目，且把回环/私网 RemoteAddr 一律视为可信代理。
// 在默认反向代理部署（Caddy/nginx → 127.0.0.1:6365）下，攻击者每请求伪造
// 不同的最左 XFF 即得全新限流桶，M-04 形同虚设。
//
// 本文件只负责浏览器侧（login）。联邦侧（federation AllowedIPs）是另一种
// 信任模型，解析逻辑在 federation.go 中分开实现，两者不共用函数。

const (
	loginRateLimitWindow      = time.Minute
	loginRateLimitMaxAttempts = 10
	loginRateLimitMaxBuckets  = 10000

	// 按用户名的第二道限流：对抗轮换源 IP 的分布式爆破，与 IP 桶独立。
	// 阈值低于 IP 桶（30/10min < 10/min）；超限后指数退避，不做永久锁定，
	// 防止攻击者用错误密码把正常用户长期锁死。
	usernameRateLimitWindow       = 10 * time.Minute
	usernameRateLimitMaxAttempts  = 30
	usernameRateLimitMaxBuckets   = 10000
	usernameBackoffBase           = time.Minute
	usernameBackoffMax            = 30 * time.Minute

	maxXFFHeaderBytes = 4096
	maxXFFEntries     = 64
)

// TrustedProxyHops 读取 FLVX_TRUSTED_PROXY_HOPS，默认 1。
// 覆盖两种默认拓扑：panel_install.sh 的 Caddy→backend，
// docker-compose 的 nginx(frontend 容器)→backend。
// 多一跳（如 Caddy→nginx→backend）请设为 2，并见部署文档。
func TrustedProxyHops() int {
	raw := strings.TrimSpace(os.Getenv("FLVX_TRUSTED_PROXY_HOPS"))
	if raw == "" {
		return 1
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		log.Printf("warning: invalid FLVX_TRUSTED_PROXY_HOPS=%q, using 1", raw)
		return 1
	}
	if n > 16 {
		log.Printf("warning: FLVX_TRUSTED_PROXY_HOPS=%d too large, capped at 16", n)
		return 16
	}
	return n
}

// loginClientIP 浏览器侧客户端 IP 解析（登录限流用）。
//
// 信任模型：只有当直接对端（RemoteAddr）落在可信代理范围
// （回环/私网/链路本地，即本机或内网的反向代理）时才采信 XFF；
// 公网直连时忽略 XFF（此时 XFF 完全可能是伪造的）。
//
// XFF 从右按可信跳数解析：最后 hops 个条目视为可信代理追加，
// 其前一个条目是第一跳可信代理实际看到的客户端 IP。
// 链长不足（拓扑与跳数配置不符）或选中条目非法时，回退到 RemoteAddr，
// 绝不因单一条目非法就信任整条链。
func loginClientIP(r *http.Request) net.IP {
	remoteIP := parseIPLiteral(r.RemoteAddr)
	if !isTrustedProxyIP(remoteIP) {
		return remoteIP // 直连：忽略 XFF
	}
	entries := splitXFFEntries(r.Header.Get("X-Forwarded-For"))
	if len(entries) == 0 {
		return remoteIP
	}
	hops := TrustedProxyHops()
	if hops <= 0 {
		return remoteIP
	}
	idx := len(entries) - hops
	if idx < 0 {
		// 链长 < 配置跳数：说明实际拓扑少于配置，可信条目不足。
		// 取最左可用条目是次优但可用的选择，同时告警提示检查配置。
		log.Printf("warning: XFF has %d entries but FLVX_TRUSTED_PROXY_HOPS=%d; using leftmost entry", len(entries), hops)
		idx = 0
	}
	if ip := parseIPLiteral(entries[idx]); ip != nil {
		return ip
	}
	return remoteIP
}

// splitXFFEntries 健壮切分 XFF：去空白/引号，跳过空条目，
// 超长头整体视为不可信（返回 nil），条目数封顶。
// 注意：只做切分，不做单个条目的合法性判定，留给 parseIPLiteral。
func splitXFFEntries(raw string) []string {
	if len(raw) > maxXFFHeaderBytes {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.Trim(strings.TrimSpace(p), "\"")
		if p == "" {
			continue
		}
		if len(out) >= maxXFFEntries {
			break
		}
		out = append(out, p)
	}
	return out
}

// ---------------------------------------------------------------------------
// IP 限流桶（M-04，R5-B2 重写淘汰策略）
// ---------------------------------------------------------------------------

type loginAttemptBucket struct {
	count       int
	windowStart time.Time
	lastSeen    time.Time
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
	if !ok {
		l.evictIfNeededLocked(now)
		l.buckets[key] = &loginAttemptBucket{count: 1, windowStart: now, lastSeen: now}
		return true
	}
	b.lastSeen = now
	if now.Sub(b.windowStart) >= loginRateLimitWindow {
		b.count = 1
		b.windowStart = now
		return true
	}
	b.count++
	return b.count <= loginRateLimitMaxAttempts
}

// evictIfNeededLocked 淘汰策略：优先清理过期桶；
// 无过期桶时按 lastSeen 淘汰最旧（LRU）并告警；
// 绝不让新键无条件挤占，也绝不让 map 无界增长。
func (l *loginRateLimiter) evictIfNeededLocked(now time.Time) {
	if len(l.buckets) < loginRateLimitMaxBuckets {
		return
	}
	for k, v := range l.buckets {
		if now.Sub(v.windowStart) >= loginRateLimitWindow {
			delete(l.buckets, k)
		}
	}
	if len(l.buckets) < loginRateLimitMaxBuckets {
		return
	}
	var oldestKey string
	var oldest time.Time
	first := true
	for k, v := range l.buckets {
		if first || v.lastSeen.Before(oldest) {
			oldestKey, oldest, first = k, v.lastSeen, false
		}
	}
	if oldestKey != "" {
		log.Printf("warning: login IP limiter full (%d buckets), LRU-evicting oldest active bucket", len(l.buckets))
		delete(l.buckets, oldestKey)
	}
}

// ---------------------------------------------------------------------------
// 用户名限流桶（R5-B2 新增）：对抗轮换 IP 的分布式爆破
// ---------------------------------------------------------------------------

type usernameAttemptBucket struct {
	count        int
	windowStart  time.Time
	backoffUntil time.Time
	violations   int
	lastSeen     time.Time
}

type loginUsernameLimiter struct {
	mu      sync.Mutex
	buckets map[string]*usernameAttemptBucket
}

var globalLoginUsernameLimiter = &loginUsernameLimiter{buckets: make(map[string]*usernameAttemptBucket)}

func normalizeLoginUsername(u string) string {
	return strings.ToLower(strings.TrimSpace(u))
}

// allow 用户名限流：命中返回 false（调用方统一写 429）。
// 注意：无论用户名是否存在都计数，避免用“429 与否”做用户名枚举。
func (l *loginUsernameLimiter) allow(username string, now time.Time) bool {
	key := normalizeLoginUsername(username)
	if key == "" {
		return true // 空用户名由参数校验处理
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[key]
	if !ok {
		l.evictIfNeededLocked(now)
		l.buckets[key] = &usernameAttemptBucket{count: 1, windowStart: now, lastSeen: now}
		return true
	}
	b.lastSeen = now
	if now.Before(b.backoffUntil) {
		return false
	}
	if now.Sub(b.windowStart) >= usernameRateLimitWindow {
		b.count = 1
		b.windowStart = now
		return true
	}
	b.count++
	if b.count > usernameRateLimitMaxAttempts {
		b.violations++
		backoff := usernameBackoffBase << (b.violations - 1)
		if backoff <= 0 || backoff > usernameBackoffMax {
			backoff = usernameBackoffMax
		}
		b.backoffUntil = now.Add(backoff)
		log.Printf("login username rate limit hit user=%q violations=%d backoff=%s", key, b.violations, backoff)
		return false
	}
	return true
}

// noteSuccess 登录成功后清除该用户名的限流状态（含退避），
// 防止正常用户被历史失败计数误伤。
func (l *loginUsernameLimiter) noteSuccess(username string) {
	key := normalizeLoginUsername(username)
	if key == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.buckets, key)
}

func (l *loginUsernameLimiter) evictIfNeededLocked(now time.Time) {
	if len(l.buckets) < usernameRateLimitMaxBuckets {
		return
	}
	for k, v := range l.buckets {
		if now.Sub(v.windowStart) >= usernameRateLimitWindow && now.After(v.backoffUntil) {
			delete(l.buckets, k)
		}
	}
	if len(l.buckets) < usernameRateLimitMaxBuckets {
		return
	}
	var oldestKey string
	var oldest time.Time
	first := true
	for k, v := range l.buckets {
		if first || v.lastSeen.Before(oldest) {
			oldestKey, oldest, first = k, v.lastSeen, false
		}
	}
	if oldestKey != "" {
		log.Printf("warning: login username limiter full (%d buckets), LRU-evicting oldest active bucket", len(l.buckets))
		delete(l.buckets, oldestKey)
	}
}

// ---------------------------------------------------------------------------
// 对 handler 暴露的检查入口
// ---------------------------------------------------------------------------

// checkLoginRateLimit IP 维度限流，命中时直接写 429 并返回 false。
func (h *Handler) checkLoginRateLimit(w http.ResponseWriter, r *http.Request) bool {
	ip := loginClientIP(r)
	key := ""
	if ip != nil {
		key = ip.String()
	} else {
		// R-001: 降级时提取纯 IP，不含端口（防端口轮换绕过）
		if parsed := parseIPLiteral(r.RemoteAddr); parsed != nil {
			key = parsed.String()
		} else {
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

// checkLoginUsernameRateLimit 用户名维度限流，与 IP 桶独立。
// 错误响应与 IP 限流完全一致，不因用户名是否存在而变化。
func (h *Handler) checkLoginUsernameRateLimit(w http.ResponseWriter, username string) bool {
	if !globalLoginUsernameLimiter.allow(username, time.Now()) {
		w.Header().Set("Retry-After", "60")
		response.WriteJSON(w, response.Err(429, "登录尝试过于频繁，请稍后再试"))
		return false
	}
	return true
}

// noteLoginUsernameSuccess 登录成功后清除用户名限流状态。
func (h *Handler) noteLoginUsernameSuccess(username string) {
	globalLoginUsernameLimiter.noteSuccess(username)
}
