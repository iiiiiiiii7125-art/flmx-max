package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
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
}

func TestFetchGitHubReleasesCachesSuccess(t *testing.T) {
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
