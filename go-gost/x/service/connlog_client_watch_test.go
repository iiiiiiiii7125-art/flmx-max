package service

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestConnLogCloseEndMs(t *testing.T) {
	var g atomic.Int64
	now := time.Now().UnixMilli()

	// 未观测到客户端离开：回退 Handle 返回时刻（约 now）。
	if end := connLogCloseEndMs(&g); end < now || end > now+1000 {
		t.Fatalf("unobserved end = %d, want ~%d", end, now)
	}
	// 观测到 12s 前离开：取离开时刻，不取 now。
	g.Store(now - 12000)
	if end := connLogCloseEndMs(&g); end != now-12000 {
		t.Fatalf("observed-gone end = %d, want %d", end, now-12000)
	}
	// 观测值晚于 now（竞态/时钟抖动）：不取未来时刻，取 now。
	g.Store(now + 60000)
	if end := connLogCloseEndMs(&g); end < now || end > now+1000 {
		t.Fatalf("future-gone end = %d, want ~%d", end, now)
	}
}
