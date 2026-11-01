package download

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Test harness for the KU4 route plan (direct-first short-circuit):
// httptest servers stand in for mirror proxies (a "proxy" answers
// any path; the prefixed form is irrelevant to it) and for the
// direct origin. The production candidate list is substituted per
// test.

func setMirrorPrefixes(t *testing.T, prefixes ...string) {
	t.Helper()
	old := MirrorPrefixes
	MirrorPrefixes = prefixes
	t.Cleanup(func() { MirrorPrefixes = old })
}

func setProbeBudgets(t *testing.T, perRoute, total time.Duration) {
	t.Helper()
	oldR, oldB := ProbeRouteTimeout, ProbeBudget
	ProbeRouteTimeout, ProbeBudget = perRoute, total
	t.Cleanup(func() { ProbeRouteTimeout, ProbeBudget = oldR, oldB })
}

// delayedServer answers 200 "ok" after delay on every path,
// counting hits.
func delayedServer(delay time.Duration, hits *atomic.Int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		if delay > 0 {
			time.Sleep(delay)
		}
		_, _ = w.Write([]byte("ok"))
	}))
}

func okProbe(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func routeNames(routes []Route) []string {
	names := make([]string, len(routes))
	for i, r := range routes {
		names[i] = r.Name()
	}
	return names
}

// 直连通 -> 短路：计划只有直连一条，代理桩零请求（不触发任何
// 代理探测），且直连带实测延迟。
func TestPlanDirectAdmittedShortCircuitsProxies(t *testing.T) {
	var proxyHits atomic.Int32
	proxy := delayedServer(0, &proxyHits)
	defer proxy.Close()
	direct := delayedServer(10*time.Millisecond, nil)
	defer direct.Close()
	setMirrorPrefixes(t, proxy.URL)

	plan := NewPlan(context.Background(), direct.URL+"/file.sha256", okProbe)
	routes := plan.Routes()
	if len(routes) != 1 || routes[0].Prefix != "" {
		t.Fatalf("直连准入时计划应只有直连, got %v", routeNames(routes))
	}
	if !routes[0].Probed || routes[0].Latency <= 0 {
		t.Fatalf("直连应带实测延迟, got %+v", routes[0])
	}
	if proxyHits.Load() != 0 {
		t.Fatalf("直连短路后代理不得被探测, proxy hits=%d", proxyHits.Load())
	}
}

// 直连死 -> 并行探测代理并按延迟排名；直连不进排名。
func TestPlanDirectDeadRanksProxiesByLatency(t *testing.T) {
	dead := delayedServer(0, nil)
	deadURL := dead.URL
	dead.Close() // connection refused
	slow := delayedServer(250*time.Millisecond, nil)
	defer slow.Close()
	fast := delayedServer(20*time.Millisecond, nil)
	defer fast.Close()
	setMirrorPrefixes(t, slow.URL, fast.URL)

	plan := NewPlan(context.Background(), deadURL+"/file.sha256", okProbe)
	routes := plan.Routes()
	if len(routes) != 2 {
		t.Fatalf("排名应含两个准入代理, got %v", routeNames(routes))
	}
	if routes[0].Prefix != fast.URL || routes[1].Prefix != slow.URL {
		t.Fatalf("排名应为 快代理 > 慢代理, got %v", routeNames(routes))
	}
	for _, r := range routes {
		if r.Prefix == "" {
			t.Fatal("直连已死, 不得进入代理排名")
		}
		if !r.Probed {
			t.Fatalf("排名中的线路应带探测标记: %+v", r)
		}
	}
}

// 直连探测通过但下载中途失败 -> 补测代理并按排名续切成功。
func TestPlanDirectMidDownloadFailureSupplementsProxies(t *testing.T) {
	var proxyHits atomic.Int32
	proxy := delayedServer(0, &proxyHits)
	defer proxy.Close()
	direct := delayedServer(0, nil) // probe passes (200), download attempt is failed by the test itself
	defer direct.Close()
	setMirrorPrefixes(t, proxy.URL)

	plan := NewPlan(context.Background(), direct.URL+"/file.sha256", okProbe)
	var order []string
	used, err := TryPlan(plan, direct.URL+"/file", func(route Route, url string) error {
		order = append(order, route.Name())
		if route.Prefix == "" {
			return errors.New("direct died mid-download")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("直连中途失败应补测代理续切: %v", err)
	}
	if used.Prefix != proxy.URL {
		t.Fatalf("胜出线路 = %q, want 代理", used.Prefix)
	}
	if len(order) != 2 || order[0] != "直连" {
		t.Fatalf("尝试顺序应为 直连 -> 代理, got %v", order)
	}
	if proxyHits.Load() == 0 {
		t.Fatal("补测应真实探测过代理")
	}
}

// 排名内下载失败 -> 顺排名下一位；胜出后的线路不被尝试。
func TestTryPlanWalksRankingAndStopsAtWinner(t *testing.T) {
	var hits1, hits2, hits3 atomic.Int32
	routes := []Route{{Prefix: "https://a.example"}, {Prefix: "https://b.example"}, {Prefix: "https://c.example"}}
	attempt := func(route Route, url string) error {
		switch route.Prefix {
		case "https://a.example":
			hits1.Add(1)
			return errors.New("boom-a")
		case "https://b.example":
			hits2.Add(1)
			return nil
		default:
			hits3.Add(1)
			return nil
		}
	}
	used, err := TryPlan(NewStaticPlan(routes), "https://github.com/x/y", attempt)
	if err != nil {
		t.Fatalf("第二名线路应胜出: %v", err)
	}
	if used.Prefix != "https://b.example" {
		t.Fatalf("winner = %q, want b", used.Prefix)
	}
	if hits1.Load() != 1 || hits2.Load() != 1 || hits3.Load() != 0 {
		t.Fatalf("attempts a/b/c = %d/%d/%d, want 1/1/0", hits1.Load(), hits2.Load(), hits3.Load())
	}
}

// 全部探测皆死 -> 计划退化为静态全序逐个试（不劣于旧行为），
// TryPlan 的聚合错误点名每条线路。
func TestPlanAllProbesDeadDegradesToStaticFullOrder(t *testing.T) {
	dead := delayedServer(0, nil)
	deadURL := dead.URL
	dead.Close()
	setMirrorPrefixes(t, deadURL, "https://mirror.invalid")

	plan := NewPlan(context.Background(), "http://127.0.0.1:1/file.sha256", okProbe)
	var attempted []string
	_, err := TryPlan(plan, "https://github.com/x/y", func(route Route, url string) error {
		attempted = append(attempted, route.Name())
		return fmt.Errorf("dial failed for %s", route.Name())
	})
	if err == nil {
		t.Fatal("全部线路失败必须报错")
	}
	all := AllRoutes()
	if len(attempted) != len(all) {
		t.Fatalf("静态退化应逐个试完全部 %d 条线路, 实际 %v", len(all), attempted)
	}
	for _, r := range all {
		if !strings.Contains(err.Error(), r.Name()) {
			t.Fatalf("聚合错误应点名线路 %q, got: %v", r.Name(), err)
		}
	}
}

// 探测耗时有界：直连挂起在单次超时内被判死并转入代理探测；
// 代理挂起在阶段总界内被排除。
func TestPlanProbePhasesAreBounded(t *testing.T) {
	setProbeBudgets(t, 200*time.Millisecond, 400*time.Millisecond)
	hung := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
		}
	}))
	defer hung.Close()
	alive := delayedServer(0, nil)
	defer alive.Close()
	setMirrorPrefixes(t, hung.URL, alive.URL)

	// 直连 = hung 服务器：先单独测直连（~200ms 判死），再并行测代理。
	plan := NewPlan(context.Background(), hung.URL+"/file.sha256", okProbe)
	start := time.Now()
	routes := plan.Routes()
	elapsed := time.Since(start)
	if elapsed > 3*time.Second {
		t.Fatalf("两段探测耗时 %v, 应被单次 200ms + 阶段 400ms 约束", elapsed)
	}
	if len(routes) != 1 || routes[0].Prefix != alive.URL {
		t.Fatalf("排名应只有活代理准入, got %v", routeNames(routes))
	}
}

func TestRouteURLAndName(t *testing.T) {
	direct := Route{}
	if got := direct.URL("https://github.com/a/b"); got != "https://github.com/a/b" {
		t.Fatalf("direct URL = %q", got)
	}
	if direct.Name() != "直连" {
		t.Fatalf("direct name = %q", direct.Name())
	}
	mirror := Route{Prefix: "https://ghfast.top"}
	if got := mirror.URL("https://github.com/a/b"); got != "https://ghfast.top/https://github.com/a/b" {
		t.Fatalf("mirror URL = %q", got)
	}
	if mirror.Name() != "ghfast.top" {
		t.Fatalf("mirror name = %q", mirror.Name())
	}
	hosts := MirrorHosts()
	if len(hosts) != len(MirrorPrefixes) || len(MirrorPrefixes) != 9 {
		t.Fatalf("MirrorHosts = %v, want one per built-in candidate (9)", hosts)
	}
	all := AllRoutes()
	if len(all) != 10 || all[0].Prefix != "" {
		t.Fatalf("AllRoutes 应为 直连 + 9 代理, got %v", routeNames(all))
	}
}
