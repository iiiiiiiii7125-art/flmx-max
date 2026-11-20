package handler

import (
	"fmt"
	"testing"
	"time"
)

// R18-B1-03 回归：flowAuthFailLimiter 与 WS 面 wsAuthFailLimiter
// 同型——满 4096 桶后新 IP 的失败被静默丢弃（fail-open），
// /flow/config 与 /flow/upload 的未知 secret 限流可被分布式喷洒
// 绕过。修复口径与 loginUsernameLimiter 一致：桶满时先清过期桶，
// 仍满则淘汰最旧活跃桶并告警，新 IP 的失败必须被计数。
func TestFlowAuthFailLimiterFullEvictsOldestActiveBucket(t *testing.T) {
	l := newFlowAuthFailLimiter()
	now := time.Now()
	oldest := "203.0.113.0"
	// 最旧桶：窗口起点 now，之后不再触碰。
	l.recordFailure(oldest, now)
	// 其余桶在 30 秒后填入，全部仍在同一分钟窗口内。
	later := now.Add(30 * time.Second)
	for i := 1; i < flowAuthFailMaxHosts; i++ {
		l.recordFailure(fmt.Sprintf("198.51.100.%d.%d", i/256, i%256), later)
	}
	if got := len(l.buckets); got != flowAuthFailMaxHosts {
		t.Fatalf("buckets = %d, want %d before eviction", got, flowAuthFailMaxHosts)
	}
	// 新 IP 在 50 秒时到来：满桶不得静默丢弃，必须淘汰最旧桶后计数。
	fresh := "192.0.2.99"
	l.recordFailure(fresh, now.Add(50*time.Second))
	if _, ok := l.buckets[fresh]; !ok {
		t.Fatal("fresh IP failure must be recorded when the limiter is full, got silently dropped (fail-open)")
	}
	if _, ok := l.buckets[oldest]; ok {
		t.Fatal("oldest active bucket must be evicted to make room for the fresh IP")
	}
	if got := len(l.buckets); got != flowAuthFailMaxHosts {
		t.Fatalf("buckets = %d, want capped at %d", got, flowAuthFailMaxHosts)
	}
	// 新 IP 继续失败达上限后必须被拦截。
	at := now.Add(50 * time.Second)
	for i := 0; i < flowAuthFailMaxPerWindow-1; i++ {
		l.recordFailure(fresh, at)
	}
	if !l.blocked(fresh, at) {
		t.Fatalf("fresh IP must be blocked after %d failures in the window", flowAuthFailMaxPerWindow)
	}
	// 被淘汰的最旧 IP 从空桶重新计数，不再残留旧计数。
	if l.blocked(oldest, at) {
		t.Fatal("evicted IP must not stay blocked")
	}
}
