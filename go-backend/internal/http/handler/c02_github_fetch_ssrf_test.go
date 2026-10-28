package handler

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// C-02（P1）：三处 GitHub 版本发现走原始 http.Client，无重定向/SSRF
// 校验，代理 302 可把面板引向内网地址（盲 SSRF，违反 S-C）。
// 修复：三处同一切换到共用加固 fetch 助手（钉死拨号 + 逐跳重定向复检）。
// 先红后绿：修复前代理 302 到内网目标会被跟随（测试失败），修复后
// 报错且内网目标零命中；allowlist 配置后私网目标仍可通。

// c02InternalServer 在 127.0.0.2 上起一个"内网目标"服务器（与代理的
// 127.0.0.1 不同 IP，以便 allowlist 只放行代理时仍能区分两跳）。
func c02InternalServer(t *testing.T, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Fatalf("listen 127.0.0.2: %v", err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"tag_name":"9.9.9","name":"9.9.9","draft":false,"prerelease":false}]`)
	}))
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

func c02RedirectProxy(t *testing.T, target string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestC02RedirectToPrivateNotFollowed(t *testing.T) {
	// allowlist 只放行代理本机（127.0.0.1/32），不放行内网目标（127.0.0.2）：
	// 修复前三路原始客户端跟随 302 并取回内网响应（红）；修复后报错且
	// 内网目标零命中（绿）。
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.1/32")
	clearReleasesCacheForTest(t)
	var internalHits atomic.Int32
	internal := c02InternalServer(t, &internalHits)
	proxy := c02RedirectProxy(t, internal.URL+"/latest/meta-data/")
	proxyURL := proxy.URL + "/https://api.github.com/repos/x/y/releases"

	if releases, err := fetchGitHubReleasesPage(proxyURL); err == nil {
		t.Fatalf("fetchGitHubReleasesPage must not follow 302 to private target, got %+v", releases)
	}
	if releases, err := fetchKernelReleaseVersionsPage(proxyURL); err == nil {
		t.Fatalf("fetchKernelReleaseVersionsPage must not follow 302 to private target, got %+v", releases)
	}
	if got := internalHits.Load(); got != 0 {
		t.Fatalf("private target must never be hit, hits=%d", got)
	}
}

func TestC02RedirectToPrivateNotFollowedSystemUpgrade(t *testing.T) {
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.1/32")
	clearReleasesCacheForTest(t)
	var internalHits atomic.Int32
	internal := c02InternalServer(t, &internalHits)
	proxy := c02RedirectProxy(t, internal.URL+"/latest/meta-data/")

	// 直连端点恒失败，迫使 fetchSystemUpgradeReleases 只走代理路径。
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer direct.Close()
	oldAPIBase := systemUpgradeAPIBaseURL
	systemUpgradeAPIBaseURL = direct.URL
	t.Cleanup(func() { systemUpgradeAPIBaseURL = oldAPIBase })

	r, err := repo.Open(t.TempDir() + "/c02-sysup.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	now := time.Now().UnixMilli()
	if err := r.UpsertConfig("github_proxy_enabled", "true", now); err != nil {
		t.Fatalf("enable proxy: %v", err)
	}
	if err := r.UpsertConfig("github_proxy_url", proxy.URL, now); err != nil {
		t.Fatalf("set proxy url: %v", err)
	}
	h := &Handler{repo: r}
	if releases, err := h.fetchSystemUpgradeReleases(50); err == nil {
		t.Fatalf("fetchSystemUpgradeReleases must not follow 302 to private target, got %+v", releases)
	}
	if got := internalHits.Load(); got != 0 {
		t.Fatalf("private target must never be hit, hits=%d", got)
	}
}

func TestC02PrivateTargetBlockedWithoutAllowlist(t *testing.T) {
	// 无 allowlist 时私网代理本身即被钉死拨号拦截：修复前直通（红），
	// 修复后报错且代理零命中（绿）。
	clearReleasesCacheForTest(t)
	var proxyHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"tag_name":"9.9.9","name":"9.9.9","draft":false,"prerelease":false}]`)
	}))
	defer proxy.Close()
	if releases, err := fetchGitHubReleasesPage(proxy.URL + "/https://api.github.com/repos/x/y/releases"); err == nil {
		t.Fatalf("private fetch target must be blocked without allowlist, got %+v", releases)
	}
	if got := proxyHits.Load(); got != 0 {
		t.Fatalf("blocked private target must never be hit, hits=%d", got)
	}
}

func TestC02AllowlistedPrivateTargetStillWorks(t *testing.T) {
	// allowlist 配置后私网目标可通（防过度拦截）：代理直返 200 与
	// 代理 302 到同段内网目标均须成功。
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	clearReleasesCacheForTest(t)
	var internalHits atomic.Int32
	internal := c02InternalServer(t, &internalHits)
	proxy := c02RedirectProxy(t, internal.URL+"/releases")
	releases, err := fetchGitHubReleasesPage(proxy.URL + "/https://api.github.com/repos/x/y/releases")
	if err != nil {
		t.Fatalf("allowlisted private redirect must still work: %v", err)
	}
	if len(releases) != 1 || releases[0].TagName != "9.9.9" {
		t.Fatalf("allowlisted fetch returned %+v, want one 9.9.9 release", releases)
	}
	if got := internalHits.Load(); got != 1 {
		t.Fatalf("allowlisted internal target must be hit once, hits=%d", got)
	}

	// 正常（无重定向）私网代理同样可通。
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"tag_name":"8.8.8","name":"8.8.8","draft":false,"prerelease":false}]`)
	}))
	defer direct.Close()
	clearReleasesCacheForTest(t)
	releases, err = fetchGitHubReleasesPage(direct.URL + "/repos/x/y/releases")
	if err != nil {
		t.Fatalf("allowlisted direct private fetch must work: %v", err)
	}
	if len(releases) != 1 || releases[0].TagName != "8.8.8" {
		t.Fatalf("direct fetch returned %+v, want one 8.8.8 release", releases)
	}
}

func TestC02DirectPrivateURLBlocked(t *testing.T) {
	// 初始 URL 本身就是私网地址时，钉死拨号须在首跳拦截（不只拦重定向）。
	clearReleasesCacheForTest(t)
	var internalHits atomic.Int32
	internal := c02InternalServer(t, &internalHits)
	if releases, err := fetchKernelReleaseVersionsPage(internal.URL + "/repos/x/y/releases"); err == nil {
		t.Fatalf("direct private URL must be blocked without allowlist, got %+v", releases)
	}
	if got := internalHits.Load(); got != 0 {
		t.Fatalf("blocked private target must never be hit, hits=%d", got)
	}
}
