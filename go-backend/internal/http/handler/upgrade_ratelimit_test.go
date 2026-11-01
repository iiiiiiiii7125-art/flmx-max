package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// GitHub API 限流回归（2026-10-20 实测）：未认证额度 60 次/小时/IP，
// 面板出口为共享 NAT 时版本列表与"留空用最新版"会整体失败。三项防线：
// 成功结果缓存 10 分钟、403 限流返回中文人话、最新版解析回退重定向。

func clearReleasesCacheForTest(t *testing.T) {
	t.Helper()
	releasesCache.Lock()
	releasesCache.entries = map[string]releasesCacheEntry{}
	releasesCache.Unlock()
	t.Cleanup(func() {
		releasesCache.Lock()
		releasesCache.entries = map[string]releasesCacheEntry{}
		releasesCache.Unlock()
	})
}

func swapGitHubBasesForTest(t *testing.T, api, html string) {
	t.Helper()
	oldAPI, oldHTML := githubAPIBase, githubHTMLBase
	githubAPIBase, githubHTMLBase = api, html
	t.Cleanup(func() { githubAPIBase, githubHTMLBase = oldAPI, oldHTML })
	// 1.7.7：换 stub 基址的用例一律钉直连-only 路线，避免真实候选
	// 探测/尝试污染断言（命中计数、限流口径）；需要代理路线的用例
	// 在 swap 之后再 seedGitHubRoutesForTest 覆盖。
	seedGitHubRoutesForTest(t, true, nil)
}

// seedGitHubRoutesForTest 钉死面板侧直选的路线状态（1.7.7 KU5）：
// directAlive 为直连探测结论，proxyOrder 为代理尝试序（nil 表示无
// 代理尝试）；cleanup 恢复先前状态。探测缓存按新鲜处理，不触发
// 任何真实网络探测。
func seedGitHubRoutesForTest(t *testing.T, directAlive bool, proxyOrder []string) {
	t.Helper()
	githubRouteCache.Lock()
	prevAt := githubRouteCache.at
	prevDirectProbed, prevDirectAlive := githubRouteCache.directProbed, githubRouteCache.directAlive
	prevProxiesProbed, prevProxyOrder := githubRouteCache.proxiesProbed, githubRouteCache.proxyOrder
	githubRouteCache.at = time.Now()
	githubRouteCache.directProbed = true
	githubRouteCache.directAlive = directAlive
	githubRouteCache.proxiesProbed = true
	githubRouteCache.proxyOrder = append([]string(nil), proxyOrder...)
	githubRouteCache.Unlock()
	t.Cleanup(func() {
		githubRouteCache.Lock()
		githubRouteCache.at = prevAt
		githubRouteCache.directProbed, githubRouteCache.directAlive = prevDirectProbed, prevDirectAlive
		githubRouteCache.proxiesProbed, githubRouteCache.proxyOrder = prevProxiesProbed, prevProxyOrder
		githubRouteCache.Unlock()
	})
}

func TestFetchGitHubReleasesCachesSuccess(t *testing.T) {
	// C-02：版本发现走加固 fetch 助手后，本地测试服务器须配 allowlist
	// 才放行（与生产内网代理同口径）。
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	clearReleasesCacheForTest(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"tag_name":"1.0.14","name":"1.0.14","draft":false,"prerelease":false}]`)
	}))
	defer srv.Close()
	swapGitHubBasesForTest(t, srv.URL, srv.URL)

	for i := 0; i < 3; i++ {
		releases, err := fetchGitHubReleases(50)
		if err != nil {
			t.Fatalf("第 %d 次调用失败: %v", i+1, err)
		}
		if len(releases) != 1 || releases[0].TagName != "1.0.14" {
			t.Fatalf("版本列表不符: %+v", releases)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("缓存未生效：API 被请求 %d 次，期望 1 次", got)
	}
}

func TestFetchGitHubReleasesRateLimitMessage(t *testing.T) {
	// C-02：同上，本地测试服务器须配 allowlist。
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	clearReleasesCacheForTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"message":"API rate limit exceeded for 16.76.14.51. (But here's the good news: Authenticated requests get a higher rate limit.)"}`)
	}))
	defer srv.Close()
	swapGitHubBasesForTest(t, srv.URL, srv.URL)

	_, err := fetchGitHubReleases(50)
	if err == nil || !strings.Contains(err.Error(), "已限流") {
		t.Fatalf("限流时应返回中文限流提示，实际: %v", err)
	}
}

func TestResolveLatestFallsBackToRedirect(t *testing.T) {
	// C-02：同上，API 侧本地测试服务器须配 allowlist（重定向侧
	// fetchLatestTagByRedirect 保持不跟随、不走加固助手）。
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	clearReleasesCacheForTest(t)
	limited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"message":"API rate limit exceeded for 16.76.14.51."}`)
	}))
	defer limited.Close()
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/releases/latest") {
			w.Header().Set("Location", "/"+githubRepo+"/releases/tag/9.9.9")
			w.WriteHeader(http.StatusFound)
			return
		}
		http.NotFound(w, r)
	}))
	defer web.Close()
	swapGitHubBasesForTest(t, limited.URL, web.URL)

	tag, err := resolveLatestReleaseByChannel("stable")
	if err != nil {
		t.Fatalf("API 限流时稳定版应回退重定向解析: %v", err)
	}
	if tag != "9.9.9" {
		t.Fatalf("回退解析版本不符: %q", tag)
	}
}
