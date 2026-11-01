package download

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// 高延迟直连不得短路（2026-11-01 生产实报的修复口径）：直连探针
// 虽准入、但实测延迟高于生产阈值（800ms）时，不得进入 stage-1
// 短路把整程预算独占——直连应带其实测延迟与代理一并排名，慢则
// 自然排后。本测试的直连桩延迟 900ms（> 800ms 阈值、< 3s 探针
// 超时），代理桩零延迟。
func TestPlanSlowDirectRankedWithProxiesNotShortCircuited(t *testing.T) {
	var proxyHits atomic.Int32
	proxy := delayedServer(0, &proxyHits)
	defer proxy.Close()
	direct := delayedServer(900*time.Millisecond, nil)
	defer direct.Close()
	setMirrorPrefixes(t, proxy.URL)

	plan := NewPlan(context.Background(), direct.URL+"/file.sha256", okProbe)
	routes := plan.Routes()
	if len(routes) != 2 {
		t.Fatalf("慢直连应与代理一并进入排名 (共 2 条), got %v", routeNames(routes))
	}
	if routes[0].Prefix != proxy.URL {
		t.Fatalf("排名第一应为快速代理而非慢直连, got %v", routeNames(routes))
	}
	if routes[1].Prefix != "" || !routes[1].Probed {
		t.Fatalf("直连应带实测延迟排在第二, got %+v", routes[1])
	}
	if routes[1].Latency < 800*time.Millisecond {
		t.Fatalf("直连实测延迟应 >= 800ms (桩延迟 900ms), got %v", routes[1].Latency)
	}
	if proxyHits.Load() == 0 {
		t.Fatal("慢直连场景下代理必须被真实探测（短路即零探测）")
	}
}
