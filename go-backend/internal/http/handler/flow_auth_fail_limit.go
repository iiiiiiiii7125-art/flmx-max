package handler

import (
	"net/http"
	"sync"
	"time"
)

// R8-060（R8-L5-06）：/flow/config 与 /flow/upload 以节点 secret 鉴权，
// 此前无任何失败节流，未知 secret 的每次尝试都直达 GetNodeBySecret
// 查库——与同为节点 secret 面、已有失败限流的 /system-info WS 面
// （wsAuthFailLimiter，D2-03）口径不一致。/flow/upload/v2 上线时漏
// 接本限流（审计 A-T-01），已按同一谓词补齐：凡以节点 secret 为唯
// 一凭据的 agent 面端点，必须接入本限流（预检＋失败记账）。本限流
// 与 WS 面同口径：
//   - 只对「未知 secret」失败计数，有效 secret 的正常上报永不计数、
//     不受影响（WS 限流器同款设计目标）；
//   - 按来源 IP 固定窗口计数，上限与窗口与 WS 面同值（60 次/分钟）；
//   - 键走本包统一收口 rateLimitKeyForRequest（clientip 可信代理链
//     解析，与登录/gh/WS 同口径；解析不出记共享桶 fail-closed）；
//   - 命中上限后不再查库，回包与未知 secret 时同形：flowUpload 回
//     "error"（agent 保留批次、稍后重报）、flowConfig 回 "ok" 且不
//     处理 body、flowUploadV2 回 401 纯文本（非确认回执，agent 保
//     留批次），三面线协议均不变。
const (
	flowAuthFailWindow       = time.Minute
	flowAuthFailMaxPerWindow = 60
	flowAuthFailMaxHosts     = 4096
)

type flowAuthFailBucket struct {
	count       int
	windowStart time.Time
}

type flowAuthFailLimiter struct {
	mu      sync.Mutex
	buckets map[string]*flowAuthFailBucket
}

func newFlowAuthFailLimiter() *flowAuthFailLimiter {
	return &flowAuthFailLimiter{buckets: make(map[string]*flowAuthFailBucket)}
}

// blocked 报告该 IP 在当前窗口内鉴权失败次数是否已达上限。
func (l *flowAuthFailLimiter) blocked(ip string, now time.Time) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[ip]
	if !ok || now.Sub(b.windowStart) >= flowAuthFailWindow {
		return false
	}
	return b.count >= flowAuthFailMaxPerWindow
}

func (l *flowAuthFailLimiter) recordFailure(ip string, now time.Time) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[ip]
	if !ok {
		if len(l.buckets) >= flowAuthFailMaxHosts {
			// 满额先清过期桶，避免 map 无界增长（与 D6-05 同型治理）。
			for k, v := range l.buckets {
				if now.Sub(v.windowStart) >= flowAuthFailWindow {
					delete(l.buckets, k)
				}
			}
			if len(l.buckets) >= flowAuthFailMaxHosts {
				return
			}
		}
		l.buckets[ip] = &flowAuthFailBucket{count: 1, windowStart: now}
		return
	}
	if now.Sub(b.windowStart) >= flowAuthFailWindow {
		b.count = 1
		b.windowStart = now
		return
	}
	b.count++
}

// flowFailLimiter 返回 Handler 的 flow 鉴权失败限流器（惰性初始化，
// 兼容以结构体字面量构造的测试 Handler）。
func (h *Handler) flowFailLimiter() *flowAuthFailLimiter {
	h.flowAuthFailMu.Lock()
	defer h.flowAuthFailMu.Unlock()
	if h.flowAuthFail == nil {
		h.flowAuthFail = newFlowAuthFailLimiter()
	}
	return h.flowAuthFail
}

// flowAuthThrottleKey 解析本请求的限流键，并报告该来源是否已达
// flow 鉴权失败上限（已达上限时调用方应跳过查库直接回拒）。
func (h *Handler) flowAuthThrottleKey(r *http.Request) (key string, throttled bool) {
	key = rateLimitKeyForRequest(r, "flow auth fail limit")
	return key, h.flowFailLimiter().blocked(key, time.Now())
}

// recordFlowAuthFailure 记一次 flow 鉴权失败（未知 secret）。
// key 必须来自同请求的 flowAuthThrottleKey。
func (h *Handler) recordFlowAuthFailure(key string) {
	h.flowFailLimiter().recordFailure(key, time.Now())
}


