package download

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// 面板源线路（面板源分发一期）：调用方从 agent 自身配置的面板地址
// 派生出的额外 Route 与内置镜像同场参与阶段 2 探测排名——实测
// 延迟定胜负，没有任何无条件先行；直连快速准入时它根本不被
// 探测（海外健康机行为与 1.7.12 逐字一致）。

// 直连已死、面板线路最快 -> 面板线路夺冠，慢镜像随后。
func TestPlanPanelRouteWinsStage2WhenFastest(t *testing.T) {
	dead := delayedServer(0, nil)
	deadURL := dead.URL
	dead.Close()
	mirror := delayedServer(250*time.Millisecond, nil)
	defer mirror.Close()
	panel := delayedServer(0, nil)
	defer panel.Close()
	setMirrorPrefixes(t, mirror.URL)

	plan := NewPlan(context.Background(), deadURL+"/file.sha256", okProbe, Route{Prefix: panel.URL})
	routes := plan.Routes()
	if len(routes) != 2 {
		t.Fatalf("排名应含面板线路与镜像两条, got %v", routeNames(routes))
	}
	if routes[0].Prefix != panel.URL {
		t.Fatalf("最快的面板线路应排第一, got %v", routeNames(routes))
	}
	if routes[1].Prefix != mirror.URL {
		t.Fatalf("慢镜像应排第二, got %v", routeNames(routes))
	}
}

// 直连快速准入短路 -> 面板线路零探测、计划只有直连（不挡直连短路）。
func TestPlanPanelRouteNotProbedWhenDirectShortCircuits(t *testing.T) {
	var panelHits atomic.Int32
	panel := delayedServer(0, &panelHits)
	defer panel.Close()
	direct := delayedServer(10*time.Millisecond, nil)
	defer direct.Close()
	setMirrorPrefixes(t, "https://mirror.invalid")

	plan := NewPlan(context.Background(), direct.URL+"/file.sha256", okProbe, Route{Prefix: panel.URL})
	routes := plan.Routes()
	if len(routes) != 1 || routes[0].Prefix != "" {
		t.Fatalf("直连短路时计划应只有直连, got %v", routeNames(routes))
	}
	if panelHits.Load() != 0 {
		t.Fatalf("直连短路后不得探测面板线路, panel hits=%d", panelHits.Load())
	}
}

// 慢直连（>800ms）+ 慢面板线路 + 快镜像 -> 纯按实测延迟排名：
// 镜像 > 面板 > 直连。面板线路慢时不靠身份赢，只靠速度赢。
func TestPlanPanelRouteRankedPurelyByLatency(t *testing.T) {
	direct := delayedServer(900*time.Millisecond, nil)
	defer direct.Close()
	panel := delayedServer(300*time.Millisecond, nil)
	defer panel.Close()
	mirror := delayedServer(20*time.Millisecond, nil)
	defer mirror.Close()
	setMirrorPrefixes(t, mirror.URL)

	plan := NewPlan(context.Background(), direct.URL+"/file.sha256", okProbe, Route{Prefix: panel.URL})
	routes := plan.Routes()
	if len(routes) != 3 {
		t.Fatalf("排名应含镜像/面板/直连三条, got %v", routeNames(routes))
	}
	if routes[0].Prefix != mirror.URL || routes[1].Prefix != panel.URL || routes[2].Prefix != "" {
		t.Fatalf("排名应为 快镜像 > 面板 > 慢直连, got %v", routeNames(routes))
	}
}

// 全部探测皆死 -> 静态尾部里板线路紧随直连、在镜像之前，
// TryPlan 按此序逐个尝试。
func TestPlanPanelRouteInStaticTailAfterDirect(t *testing.T) {
	dead := delayedServer(0, nil)
	deadURL := dead.URL
	dead.Close()
	setMirrorPrefixes(t, "https://m1.invalid", "https://m2.invalid")

	plan := NewPlan(context.Background(), "http://127.0.0.1:1/file.sha256", okProbe, Route{Prefix: deadURL})
	var attempted []string
	_, err := TryPlan(plan, "https://github.com/x/y", func(route Route, url string) error {
		attempted = append(attempted, route.Prefix)
		return errors.New("dial failed")
	})
	if err == nil {
		t.Fatal("全部线路失败必须报错")
	}
	want := []string{"", deadURL, "https://m1.invalid", "https://m2.invalid"}
	if len(attempted) != len(want) {
		t.Fatalf("静态尾部应逐个试完 %v, 实际 %v", want, attempted)
	}
	for i := range want {
		if attempted[i] != want[i] {
			t.Fatalf("静态尾部顺序应为 直连 > 面板 > 镜像, got %v", attempted)
		}
	}
}

// 额外线路的过滤：空前缀（与直连重复）、与内置镜像同前缀、
// 自身重复，一律在构造期剔除，静态尾部不出现重复项。
func TestPlanExtraRouteFiltering(t *testing.T) {
	setMirrorPrefixes(t, "https://m1.example", "https://m2.example")
	plan := NewPlan(context.Background(), "", nil,
		Route{Prefix: ""},
		Route{Prefix: "https://panel.example"},
		Route{Prefix: "https://panel.example"},
		Route{Prefix: "https://m1.example"},
	)
	routes := plan.Routes() // 无探针 -> 静态尾部
	want := []string{"", "https://panel.example", "https://m1.example", "https://m2.example"}
	if len(routes) != len(want) {
		t.Fatalf("静态尾部 = %v, want %v", routeNames(routes), want)
	}
	for i := range want {
		if routes[i].Prefix != want[i] {
			t.Fatalf("静态尾部 = %v, want %v", routeNames(routes), want)
		}
	}
}
