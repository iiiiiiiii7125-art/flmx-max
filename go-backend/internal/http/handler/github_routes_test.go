package handler

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// 1.7.7（KU5）面板侧测速直选的行为回归（github_routes.go）：
//   - 直连优先短路：直连探测判活时不得探测/尝试任何代理；
//   - 直连判死时才并行探测代理，且按延迟排名选最快者执行真实
//     请求（探测判活以真实目标可解析为准）。
// 本文件用例走真实探测路径（不 seed 路线状态），候选清单替换为
// 本地 stub 以保持密封。

func resetGitHubRouteCacheForTest(t *testing.T) {
	t.Helper()
	clear := func() {
		githubRouteCache.Lock()
		githubRouteCache.at = time.Time{}
		githubRouteCache.directProbed = false
		githubRouteCache.directAlive = false
		githubRouteCache.directLatency = 0
		githubRouteCache.proxiesProbed = false
		githubRouteCache.proxyOrder = nil
		githubRouteCache.proxyLatencies = nil
		githubRouteCache.Unlock()
	}
	clear()
	t.Cleanup(clear)
}

func swapGitHubCandidatesForTest(t *testing.T, prefixes ...string) {
	t.Helper()
	old := githubFetchProxyCandidates
	githubFetchProxyCandidates = prefixes
	t.Cleanup(func() { githubFetchProxyCandidates = old })
}

func redirectTagStub(t *testing.T, tag string, delay time.Duration, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	// R5-FC（L8-F-04）：重定向探测已改走钉死拨号守卫客户端，
	// 本地 stub 须显式放行回环段（与 proxy_fallback 先例同口径）。
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if delay > 0 {
			time.Sleep(delay)
		}
		w.Header().Set("Location", "/"+githubRepo+"/releases/tag/"+tag)
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestGitHubRoutesDirectFirstShortCircuitsProxies(t *testing.T) {
	resetGitHubRouteCacheForTest(t)
	var directHits, proxyHits atomic.Int32
	direct := redirectTagStub(t, "3.2.1", 0, &directHits)
	proxy := redirectTagStub(t, "9.9.9", 0, &proxyHits)
	swapGitHubBasesForTest(t, direct.URL, direct.URL)
	swapGitHubCandidatesForTest(t, proxy.URL)
	// swapGitHubBasesForTest 会 seed 直连-only 路线；本用例要走真实
	// 探测路径，seed 之后再清零路线缓存（cleanup 顺序使最终状态
	// 仍回到未探测）。
	resetGitHubRouteCacheForTest(t)

	tag, err := resolveLatestTagByRedirect()
	if err != nil {
		t.Fatalf("直连可用时重定向解析应成功: %v", err)
	}
	if tag != "3.2.1" {
		t.Fatalf("tag = %q, want 直连解析的 3.2.1", tag)
	}
	if got := proxyHits.Load(); got != 0 {
		t.Fatalf("直连判活时不得触碰任何代理（探测与尝试均不许），代理命中 %d 次", got)
	}
}

// shrinkGitHubDirectGateForTest 把直连短路延迟门调小，使「慢
// 直连」用例无需真的等 800ms（与生产 var 的测试惯例一致）。
func shrinkGitHubDirectGateForTest(t *testing.T, gate time.Duration) {
	t.Helper()
	old := githubDirectShortCircuitMaxLatency
	githubDirectShortCircuitMaxLatency = gate
	t.Cleanup(func() { githubDirectShortCircuitMaxLatency = old })
}

// R2-P2-07：慢直连被门挡——直连判活但实测延迟超门时不得独占
// 首试，门内（更快）的代理赢得真实请求；直连只被探测一次、
// 不承载真实请求。
func TestGitHubRoutesSlowDirectLosesShortCircuitToFasterProxy(t *testing.T) {
	resetGitHubRouteCacheForTest(t)
	shrinkGitHubDirectGateForTest(t, 100*time.Millisecond)
	var directHits, proxyHits atomic.Int32
	direct := redirectTagStub(t, "3.2.1", 300*time.Millisecond, &directHits)
	proxy := redirectTagStub(t, "4.4.4", 0, &proxyHits)
	swapGitHubBasesForTest(t, direct.URL, direct.URL)
	swapGitHubCandidatesForTest(t, proxy.URL)
	resetGitHubRouteCacheForTest(t)

	tag, err := resolveLatestTagByRedirect()
	if err != nil {
		t.Fatalf("慢直连在场时应由更快代理完成解析: %v", err)
	}
	if tag != "4.4.4" {
		t.Fatalf("tag = %q, want 门内代理（proxy）的 4.4.4——慢直连不得独占首试", tag)
	}
	if got := directHits.Load(); got != 1 {
		t.Fatalf("慢直连只应被探测一次、不得承载真实请求，direct=%d", got)
	}
	if got := proxyHits.Load(); got < 2 {
		t.Fatalf("真实请求应落在门内代理上（探测+请求 ≥2 次），proxy=%d", got)
	}
}

// R2-P2-07：慢直连不丢弃——直连超门但仍是判活路线里实测最快
// 时，合并排名仍由直连赢得真实请求（门外仅在无更快门内路线
// 时兜底的另一面：按实测延迟竞争，而非按门一刀切除名）。
func TestGitHubRoutesSlowDirectStillWinsWhenFastestAdmitted(t *testing.T) {
	resetGitHubRouteCacheForTest(t)
	shrinkGitHubDirectGateForTest(t, 100*time.Millisecond)
	var directHits, proxyHits atomic.Int32
	direct := redirectTagStub(t, "3.2.1", 300*time.Millisecond, &directHits)
	proxy := redirectTagStub(t, "4.4.4", 600*time.Millisecond, &proxyHits)
	swapGitHubBasesForTest(t, direct.URL, direct.URL)
	swapGitHubCandidatesForTest(t, proxy.URL)
	resetGitHubRouteCacheForTest(t)

	tag, err := resolveLatestTagByRedirect()
	if err != nil {
		t.Fatalf("慢直连为最快判活路线时解析应成功: %v", err)
	}
	if tag != "3.2.1" {
		t.Fatalf("tag = %q, want 直连的 3.2.1——慢直连应按实测延迟赢回首试", tag)
	}
	if got := directHits.Load(); got != 2 {
		t.Fatalf("直连应被探测一次并承载真实请求（共 2 次），direct=%d", got)
	}
	if got := proxyHits.Load(); got != 1 {
		t.Fatalf("更慢的代理只应被探测一次、不得承载真实请求，proxy=%d", got)
	}
}

// R2-P2-07：静态路线表（面板自升级资产下载面）在慢直连时给
// 合并排名——直连按实测延迟入列、不再恒居首。
func TestGitHubRouteOrderSlowDirectRankedByLatency(t *testing.T) {
	resetGitHubRouteCacheForTest(t)
	shrinkGitHubDirectGateForTest(t, 100*time.Millisecond)
	var directHits, slowHits, fastHits atomic.Int32
	direct := redirectTagStub(t, "3.2.1", 300*time.Millisecond, &directHits)
	slow := redirectTagStub(t, "1.1.1", 500*time.Millisecond, &slowHits)
	fast := redirectTagStub(t, "2.2.2", 50*time.Millisecond, &fastHits)
	swapGitHubBasesForTest(t, direct.URL, direct.URL)
	swapGitHubCandidatesForTest(t, slow.URL, fast.URL)
	resetGitHubRouteCacheForTest(t)

	order := gitHubRouteOrder()
	want := []string{fast.URL, "", slow.URL}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v（按实测延迟：fast 代理 < 直连 < slow 代理）", order, want)
		}
	}
}

func TestGitHubRoutesProbeProxiesAndPickFastestWhenDirectDead(t *testing.T) {
	resetGitHubRouteCacheForTest(t)
	direct := failingUpstreamForTest(t)
	var slowHits, fastHits atomic.Int32
	slow := redirectTagStub(t, "1.1.1", 300*time.Millisecond, &slowHits)
	fast := redirectTagStub(t, "2.2.2", 0, &fastHits)
	swapGitHubBasesForTest(t, direct.URL, direct.URL)
	// 慢代理在清单中靠前：排名必须由探测延迟决定，而非清单序。
	swapGitHubCandidatesForTest(t, slow.URL, fast.URL)
	resetGitHubRouteCacheForTest(t)

	tag, err := resolveLatestTagByRedirect()
	if err != nil {
		t.Fatalf("直连判死时代理应接管解析: %v", err)
	}
	if tag != "2.2.2" {
		t.Fatalf("tag = %q, want 最快代理（fast）的 2.2.2", tag)
	}
	if slowHits.Load() == 0 || fastHits.Load() == 0 {
		t.Fatalf("两名代理都应被探测到，slow=%d fast=%d", slowHits.Load(), fastHits.Load())
	}
	if got := fastHits.Load(); got < 2 {
		t.Fatalf("真实请求应落在最快代理上（探测+请求 ≥2 次），fast=%d", got)
	}
	if got := slowHits.Load(); got != 1 {
		t.Fatalf("慢代理只应被探测一次、不得承载真实请求，slow=%d", got)
	}
}
