package handler

import (
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"go-backend/internal/clientip"
	"go-backend/internal/http/response"
)

// R5-B2 A-001: 登录限流信任模型重写。
//
// 背景：旧实现取 XFF 最左条目，且把回环/私网 RemoteAddr 一律视为可信代理。
// 在脚本默认反向代理部署（Caddy → 前端 nginx → 后端 127.0.0.1:6365，共两跳）下，攻击者每请求伪造
// 不同的最左 XFF 即得全新限流桶，M-04 形同虚设。
//
// 本文件负责浏览器侧（login）的限流桶与检查入口。客户端 IP 的
// 可信代理链解析已收口到 internal/clientip（R5-FC）：登录、
// gh 镜像、WS 鉴权失败三面共用同一解析（见 clientip 包注释）。

const (
	loginRateLimitWindow      = time.Minute
	loginRateLimitMaxAttempts = 10
	// R5-B5 D11-D13: 紧缩 IP 的阈值（用户名桶触发且验证码未启用时）。
	tightenedLoginMaxAttempts = 3
	loginRateLimitMaxBuckets  = 10000

	// 按用户名的第二道限流：对抗轮换源 IP 的分布式爆破，与 IP 桶独立。
	// 阈值低于 IP 桶（30/10min < 10/min）；超限后指数退避，不做永久锁定，
	// 防止攻击者用错误密码把正常用户长期锁死。
	usernameRateLimitWindow      = 10 * time.Minute
	usernameRateLimitMaxAttempts = 30
	usernameRateLimitMaxBuckets  = 10000
	usernameBackoffBase          = time.Minute
	usernameBackoffMax           = 30 * time.Minute

	// XFF 解析上限的正本在 internal/clientip（限流三面共用），
	// 此处别名保留供本包存量引用与回归测试。
	maxXFFHeaderBytes = clientip.MaxXFFHeaderBytes
	maxXFFEntries     = clientip.MaxXFFEntries

	// K-003: 用户名桶触发后的渐进式延迟上限。
	loginProgressiveDelayMax = 5 * time.Second
)

// TrustedProxyHops 读取 FLMX_TRUSTED_PROXY_HOPS，默认 1。
// 实现正本在 internal/clientip（限流三面共用：登录 / gh 镜像 /
// WS 鉴权失败），此处保留同名入口供本包存量调用。
func TrustedProxyHops() int {
	return clientip.TrustedProxyHops()
}

// trustedXFFRun / trustedXFFChainLen / splitXFFEntries 的实现正本
// 在 internal/clientip，此处薄封装保留供联邦侧与回归测试沿用
// 既有名字。语义见 clientip 包注释（右锚、超配 fail-closed）。
func trustedXFFRun(entries []string, isTrusted func(net.IP) bool) int {
	return clientip.TrustedXFFRun(entries, isTrusted)
}

func trustedXFFChainLen(entries []string, isTrusted func(net.IP) bool) int {
	return clientip.TrustedXFFChainLen(entries, isTrusted)
}

func splitXFFEntries(raw string) []string {
	return clientip.SplitXFFEntries(raw)
}

// loginClientIP 浏览器侧客户端 IP 解析（登录限流用）。
//
// 信任模型与实现正本在 internal/clientip（R5-FC 收口：登录 /
// gh 镜像 / WS 鉴权失败三面共用同一解析）：C4-01 起只有两类
// 直接对端（RemoteAddr）的 XFF 被采信——回环对端，或落在
// FLMX_TRUSTED_PROXIES 显式登记 CIDR 名单内的对端；RFC1918
// 私网对端不再默认可信（旧口径「回环/私网/链路本地」已收窄，
// 勿按旧注释理解——本机代理链的宽谓词只剩 panelRequestScheme
// 的 scheme 推断一处独立面，见 mutations.go 该函数注释）。
// 对端不可信时 XFF 整体忽略（此时 XFF 完全可能是伪造的）。
// XFF 从右按可信跳数解析，链长不足或跳数超配（R24-X-01）时
// fail-closed 回 RemoteAddr——收窄口径与取舍见
// clientip.FromRequest 的包内说明与本文件头注。
func loginClientIP(r *http.Request) net.IP {
	return clientip.FromRequest(r)
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

// R5-B5 D11-D13: 用户名桶触发后收紧的 IP 集合（验证码未启用时）。
// 这些 IP 的登录阈值从严，过期自动解除。
var tightenedLoginIPs = struct {
	sync.Mutex
	m map[string]time.Time // ip -> 紧缩到期时间
}{m: make(map[string]time.Time)}

// tightenedIPRateLimitWindow 紧缩持续时间。
const tightenedIPRateLimitWindow = 30 * time.Minute

// tightenedLoginMaxEntries 紧缩集合容量上限（R4B01-001）：与同仓
// 未认证可写内存表「上限+淘汰」口径对齐——登录 IP 桶/用户名桶各
// 10000（loginRateLimitMaxBuckets/usernameRateLimitMaxBuckets）、
// captcha verify 桶 10000、captcha 令牌池 1000、passkey pending
// 池 1000（handler.go captchaTokenPoolMax 头注自述全集标准）。
// 本集合键空间由远端源 IP 决定，此前无上限、无批量清扫：不再
// 回访的 IP 条目永不删除，映射随不同源 IP 数无界增长，是全集
// 唯一漏网。淘汰形态与 loginRateLimiter.evictIfNeededLocked
// 一致：达上限写入新键时先批量清扫过期条目，仍满则淘汰到期
// 最早者（全部条目窗口同长，到期序即写入序，等价 LRU 最旧）。
const tightenedLoginMaxEntries = 10000

// allow 是登录面的入口：键必须为纯 IP（rateLimitKeyForRequest
// 口径），因为紧缩判定按纯 IP 键查 tightenedLoginIPs。非纯 IP 键
// 或自有上限的限流器不得走此入口——紧缩集只存纯 IP 键，复合键
// 查紧缩永不命中（R7-L8-02 指出的口径分叉），且登录信号触发的
// 紧缩也不应外溢到其他面；这类限流器走 allowWithLimit 并自带
// 上限，两条路径的差异在此钉死，不再靠调用方默契。
func (l *loginRateLimiter) allow(key string, now time.Time) bool {
	// R5-B5 D11-D13: 紧缩 IP 用更严格的阈值。
	maxAttempts := loginRateLimitMaxAttempts
	if isIPTightened(key) {
		maxAttempts = tightenedLoginMaxAttempts
	}
	return l.allowWithLimit(key, now, maxAttempts)
}

// allowWithLimit 是与紧缩机制解耦的桶判定：只按调用方给定的
// 上限计数，不查紧缩集。供键非纯 IP（如 sub_store 的 IP+用户
// 名复合键）或上限独立于登录口径的限流器使用。
func (l *loginRateLimiter) allowWithLimit(key string, now time.Time, maxAttempts int) bool {
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
	return b.count <= maxAttempts
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
	backoffUntil time.Time // 保留字段（D11-D13 后仅用于验证强度冷却，不用于拒绝）
	violations   int
	lastSeen     time.Time
	triggered    bool // R5-B5 D11-D13: 是否已触发阈值（信号）
}

type loginUsernameLimiter struct {
	mu      sync.Mutex
	buckets map[string]*usernameAttemptBucket
}

var globalLoginUsernameLimiter = &loginUsernameLimiter{buckets: make(map[string]*usernameAttemptBucket)}

func normalizeLoginUsername(u string) string {
	return strings.ToLower(strings.TrimSpace(u))
}

// recordAttempt 记录用户名尝试，返回是否触发阈值（信号，非门控）。
// R5-B5 D11-D13: 用户名桶永不作为拒绝依据，只作为"升级验证"信号。
// 注意：无论用户名是否存在都计数，避免用行为差异做用户名枚举。
func (l *loginUsernameLimiter) recordAttempt(username string, now time.Time) (triggered bool) {
	key := normalizeLoginUsername(username)
	if key == "" {
		return false // 空用户名由参数校验处理
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[key]
	if !ok {
		l.evictIfNeededLocked(now)
		l.buckets[key] = &usernameAttemptBucket{count: 1, windowStart: now, lastSeen: now}
		return false
	}
	b.lastSeen = now
	if now.Sub(b.windowStart) >= usernameRateLimitWindow {
		b.count = 1
		b.windowStart = now
		b.triggered = false
		return false
	}
	b.count++
	if b.count > usernameRateLimitMaxAttempts && !b.triggered {
		b.triggered = true
		b.violations++
		log.Printf("login username bucket triggered user=%q count=%d", key, b.count)
		return true
	}
	return b.triggered
}

// K-003: violationsOf 返回该用户名的累计触发次数（用于渐进延迟与新触发判定）。
func (l *loginUsernameLimiter) violationsOf(username string) int {
	key := normalizeLoginUsername(username)
	if key == "" {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if b, ok := l.buckets[key]; ok {
		return b.violations
	}
	return 0
}

// K-003: progressiveDelay 返回该用户名的渐进式登录延迟。
// 旧实现用户名桶只是信号（收紧来源 IP 阈值），分布式 botnet 仍可获约
// 3N 次/分钟 的总预算。触发后按累计触发次数增加延迟（1s→2s→4s，上限 5s），
// 让慢速喷洒的每次尝试都付出时间成本。
// 未触发返回 0；窗口重置不清空 violations，故长期攻击会停在 5s 档。
func (l *loginUsernameLimiter) progressiveDelay(username string) time.Duration {
	key := normalizeLoginUsername(username)
	if key == "" {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok || !b.triggered {
		return 0
	}
	d := time.Second
	for i := 1; i < b.violations && d < loginProgressiveDelayMax; i++ {
		d *= 2
	}
	if d > loginProgressiveDelayMax {
		d = loginProgressiveDelayMax
	}
	return d
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

// rateLimitKeyForRequest 是本包限流键的统一收口（R6-L8-F-04）：
// 走 clientip.RateLimitKey（可信代理链解析 → RemoteAddr 纯 IP 两
// 级，与 gh 镜像面、WS 面同口径）；两级皆无时记入
// clientip.UnresolvedKey 共享桶并告警（fail-closed 记账），绝不
// 以含端口的原始 RemoteAddr 为键——端口轮换即可换桶绕过。此前
// 登录、sub_store、captcha verify 与 login 的用户名信号 IP 各自
// 维护「解析 → 纯 IP → 原始 RemoteAddr」的三级回退链，末端形态
// 与 clientip 契约相悖，现统一经本助手，新增限流面直接调用。
func rateLimitKeyForRequest(r *http.Request, surface string) string {
	if key, ok := clientip.RateLimitKey(r); ok {
		return key
	}
	log.Printf("%s: 无法从 RemoteAddr %q 解析客户端地址，限流记入共享桶（fail-closed）", surface, r.RemoteAddr)
	return clientip.UnresolvedKey
}

// checkLoginRateLimit IP 维度限流，命中时直接写 429 并返回 false。
func (h *Handler) checkLoginRateLimit(w http.ResponseWriter, r *http.Request) bool {
	key := rateLimitKeyForRequest(r, "login rate limit")
	if !globalLoginLimiter.allow(key, time.Now()) {
		w.Header().Set("Retry-After", "60")
		response.WriteJSON(w, response.Err(429, "登录尝试过于频繁，请稍后再试"))
		return false
	}
	return true
}

// tightenIPRateLimit 将 IP 加入紧缩集合（R5-B5 D11-D13）。
func (h *Handler) tightenIPRateLimit(clientIP string) {
	if clientIP == "" {
		return
	}
	now := time.Now()
	tightenedLoginIPs.Lock()
	defer tightenedLoginIPs.Unlock()
	if _, exists := tightenedLoginIPs.m[clientIP]; !exists {
		evictTightenedLoginIPsIfNeededLocked(now)
	}
	tightenedLoginIPs.m[clientIP] = now.Add(tightenedIPRateLimitWindow)
	log.Printf("login ip tightened ip=%s reason=username_bucket_triggered", clientIP)
}

// evictTightenedLoginIPsIfNeededLocked 在紧缩集合达容量上限时腾位
// （R4B01-001，调用方须持 tightenedLoginIPs 锁）：先批量清扫全部
// 过期条目；仍满则按到期时刻淘汰最早者并告警，与
// loginRateLimiter.evictIfNeededLocked 同形态——绝不让新键无条件
// 挤占，也绝不让 map 无界增长。未达上限时不做全表清扫（过期条目
// 的同键惰性删除在 isIPTightened，语义不变）。
func evictTightenedLoginIPsIfNeededLocked(now time.Time) {
	if len(tightenedLoginIPs.m) < tightenedLoginMaxEntries {
		return
	}
	for ip, expiry := range tightenedLoginIPs.m {
		if now.After(expiry) {
			delete(tightenedLoginIPs.m, ip)
		}
	}
	for len(tightenedLoginIPs.m) >= tightenedLoginMaxEntries {
		var evictKey string
		var evictExpiry time.Time
		first := true
		for ip, expiry := range tightenedLoginIPs.m {
			if first || expiry.Before(evictExpiry) {
				evictKey, evictExpiry, first = ip, expiry, false
			}
		}
		if evictKey == "" {
			break
		}
		log.Printf("warning: tightened login IP set full (%d entries), evicting earliest-expiring entry", len(tightenedLoginIPs.m))
		delete(tightenedLoginIPs.m, evictKey)
	}
}

// isIPTightened 检查 IP 是否处于紧缩状态（过期自动清理）。
func isIPTightened(clientIP string) bool {
	tightenedLoginIPs.Lock()
	defer tightenedLoginIPs.Unlock()
	expiry, ok := tightenedLoginIPs.m[clientIP]
	if !ok {
		return false
	}
	if time.Now().After(expiry) {
		delete(tightenedLoginIPs.m, clientIP)
		return false
	}
	return true
}

// checkLoginUsernameRateLimit 用户名维度信号（R5-B5 D11-D13）。
// 永不硬拒：返回 triggered 信号，由调用方决定升级验证强度。
// 错误响应与 IP 限流完全一致，不因用户名是否存在而变化（防枚举）。
func (h *Handler) checkLoginUsernameRateLimit(username string, clientIP string, captchaEnabled bool) (triggered bool) {
	violationsBefore := globalLoginUsernameLimiter.violationsOf(username)
	triggered = globalLoginUsernameLimiter.recordAttempt(username, time.Now())
	if triggered {
		// D11-D13.2: 验证码未启用时，收紧该来源 IP 阈值 + 告警日志。
		// 验证码已启用时，登录流程已强制要求 captcha，此处仅记日志。
		if !captchaEnabled {
			h.tightenIPRateLimit(clientIP)
		}
		log.Printf("login username bucket triggered user=%q ip=%s captcha_enabled=%v", username, clientIP, captchaEnabled)
		// K-003：本次调用刚触发阈值（violations 递增）时施加渐进式延迟，
		// 延迟随累计触发次数增长（1s→2s→4s，上限 5s）。
		// 只在新触发点延迟：避免已触发状态下每次尝试都 sleep，
		// 同时让分布式慢速喷洒在每个窗口的触发点付出时间成本。
		if globalLoginUsernameLimiter.violationsOf(username) > violationsBefore {
			if delay := globalLoginUsernameLimiter.progressiveDelay(username); delay > 0 {
				time.Sleep(delay)
			}
		}
	}
	// D11-D13.3: 绝不返回"账号已锁定"，无 429。
	return triggered
}

// noteLoginUsernameSuccess 登录成功后清除用户名限流状态。
func (h *Handler) noteLoginUsernameSuccess(username string) {
	globalLoginUsernameLimiter.noteSuccess(username)
}
