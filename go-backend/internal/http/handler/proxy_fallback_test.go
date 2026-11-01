package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"go-backend/internal/kernel"
)

// F-029/F-059 的「代理先行/直连降级」在 1.7.7 升级为内置候选测速
// 直选（KU5，github_routes.go）：版本发现三处孪生实现（节点侧
// fetchGitHubReleases、重定向解析、内核 kernelReleaseVersions）与
// 面板侧 fetchSystemUpgradeReleases 同口径——直连优先短路、失败
// 按代理排名续切、直连判死时殿后兜底。本文件的用例以
// seedGitHubRoutesForTest 钉死路线状态（直连判死 + 指定代理序），
// 断言尝试确实经指定代理/直连到达 stub。代理 URL 形态为
// <proxy>/<完整目标 URL>，与 gh-proxy 约定一致。

func failingUpstreamForTest(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, "bad gateway")
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchGitHubReleasesViaProxyWhenDirectFails(t *testing.T) {
	// C-02：版本发现走加固 fetch 助手后，本地测试代理须配 allowlist
	// 才放行（与生产内网代理同口径）。
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	clearReleasesCacheForTest(t)
	direct := failingUpstreamForTest(t)
	var proxyHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		if !strings.Contains(r.URL.Path, "/repos/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"tag_name":"9.9.9","name":"9.9.9","draft":false,"prerelease":false}]`)
	}))
	defer proxy.Close()
	swapGitHubBasesForTest(t, direct.URL, direct.URL)
	seedGitHubRoutesForTest(t, false, []string{proxy.URL})

	releases, err := fetchGitHubReleases(50)
	if err != nil {
		t.Fatalf("直连失败时应经代理成功: %v", err)
	}
	if len(releases) != 1 || releases[0].TagName != "9.9.9" {
		t.Fatalf("版本列表不符: %+v", releases)
	}
	if proxyHits.Load() == 0 {
		t.Fatal("应先经代理请求版本列表")
	}
}

func TestFetchGitHubReleasesFallsBackToDirectWhenProxyFails(t *testing.T) {
	// C-02：同上，本地测试代理须配 allowlist。
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	clearReleasesCacheForTest(t)
	proxy := failingUpstreamForTest(t)
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"tag_name":"8.8.8","name":"8.8.8","draft":false,"prerelease":false}]`)
	}))
	defer direct.Close()
	swapGitHubBasesForTest(t, direct.URL, direct.URL)
	// 直连被探测判死时排在代理之后兜底：代理失败后仍应落到直连。
	seedGitHubRoutesForTest(t, false, []string{proxy.URL})

	releases, err := fetchGitHubReleases(50)
	if err != nil {
		t.Fatalf("代理失败时应降级直连: %v", err)
	}
	if len(releases) != 1 || releases[0].TagName != "8.8.8" {
		t.Fatalf("版本列表不符: %+v", releases)
	}
}

func TestResolveLatestTagByRedirectViaProxy(t *testing.T) {
	direct := failingUpstreamForTest(t)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/releases/latest") {
			w.Header().Set("Location", "/"+githubRepo+"/releases/tag/7.7.7")
			w.WriteHeader(http.StatusFound)
			return
		}
		http.NotFound(w, r)
	}))
	defer proxy.Close()
	swapGitHubBasesForTest(t, direct.URL, direct.URL)
	seedGitHubRoutesForTest(t, false, []string{proxy.URL})

	tag, err := resolveLatestTagByRedirect()
	if err != nil {
		t.Fatalf("直连失败时重定向解析应经代理成功: %v", err)
	}
	if tag != "7.7.7" {
		t.Fatalf("解析版本不符: %q", tag)
	}
}

func TestKernelReleaseVersionsViaProxyWhenDirectFails(t *testing.T) {
	// C-02：同上，本地测试代理须配 allowlist。
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	resetKernelVersionCache(t)
	direct := failingUpstreamForTest(t)
	var proxyHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"tag_name":"v1.14.0","draft":false,"prerelease":false,"published_at":"2026-01-01T00:00:00Z"}]`)
	}))
	defer proxy.Close()
	swapGitHubBasesForTest(t, direct.URL, direct.URL)
	seedGitHubRoutesForTest(t, false, []string{proxy.URL})

	info, ok := kernel.Lookup(kernel.TypeSingbox)
	if !ok {
		t.Fatal("sing-box info missing")
	}
	items, err := kernelReleaseVersions(info)
	if err != nil {
		t.Fatalf("直连失败时内核版本列表应经代理成功: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	if proxyHits.Load() == 0 {
		t.Fatal("应先经代理请求内核版本列表")
	}
}
