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

// F-029/F-059：版本发现三处孪生实现（节点侧 fetchGitHubReleases、重定向
// 解析、内核 kernelReleaseVersions）补齐代理先行/直连降级，与面板侧
// fetchSystemUpgradeReleases（U1-001）同口径。代理 URL 形态为
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

	releases, err := fetchGitHubReleasesWithProxy(50, proxy.URL)
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
	clearReleasesCacheForTest(t)
	proxy := failingUpstreamForTest(t)
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"tag_name":"8.8.8","name":"8.8.8","draft":false,"prerelease":false}]`)
	}))
	defer direct.Close()
	swapGitHubBasesForTest(t, direct.URL, direct.URL)

	releases, err := fetchGitHubReleasesWithProxy(50, proxy.URL)
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

	tag, err := resolveLatestTagByRedirectWithProxy(proxy.URL)
	if err != nil {
		t.Fatalf("直连失败时重定向解析应经代理成功: %v", err)
	}
	if tag != "7.7.7" {
		t.Fatalf("解析版本不符: %q", tag)
	}
}

func TestKernelReleaseVersionsViaProxyWhenDirectFails(t *testing.T) {
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

	info, ok := kernel.Lookup(kernel.TypeSingbox)
	if !ok {
		t.Fatal("sing-box info missing")
	}
	items, err := kernelReleaseVersionsWithProxy(info, proxy.URL)
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
