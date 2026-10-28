package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// J-03（P3）：Go 侧 /releases/latest 重定向兜底不分渠道——API 失效
// 窗口内 stable 最新版解析可能命中 beta tag。修复：稳定渠道只接受
// 纯数字 tag（releaseChannelFromTag(tag)==stable），否则继续报错；
// upgrade.go 与 system_upgrade.go 两处同改。先红后绿：修复前 API
// 故障 + 重定向指向 1.7.2-beta1 时返回 beta（测试失败），修复后报错。

func j03RateLimitedAPI(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"message":"API rate limit exceeded for 16.76.14.51."}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func j03RedirectWeb(t *testing.T, tag string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/releases/latest") {
			// 绝对 Location，host 与被换的 githubHTMLBase 一致（过 C-01 host 校验）。
			w.Header().Set("Location", srv.URL+"/"+githubRepo+"/releases/tag/"+tag)
			w.WriteHeader(http.StatusFound)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestJ03RedirectFallbackRejectsBetaForStable(t *testing.T) {
	// API 限流 + 重定向指向 1.7.2-beta1：稳定渠道必须报错、不返回 beta。
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	clearReleasesCacheForTest(t)
	swapGitHubBasesForTest(t, j03RateLimitedAPI(t).URL, j03RedirectWeb(t, "1.7.2-beta1").URL)

	tag, err := resolveLatestReleaseByChannel("stable")
	if err == nil {
		t.Fatalf("stable channel must not resolve to a beta tag via redirect fallback, got %q", tag)
	}
	if !strings.Contains(err.Error(), "已限流") {
		t.Fatalf("stable fallback rejection must surface the API error, got: %v", err)
	}
}

func TestJ03RedirectFallbackStillResolvesStable(t *testing.T) {
	// 正例：重定向指向纯数字 tag 时稳定渠道仍能解析。
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	clearReleasesCacheForTest(t)
	swapGitHubBasesForTest(t, j03RateLimitedAPI(t).URL, j03RedirectWeb(t, "1.7.2").URL)

	tag, err := resolveLatestReleaseByChannel("stable")
	if err != nil {
		t.Fatalf("stable redirect fallback must still resolve a numeric tag: %v", err)
	}
	if tag != "1.7.2" {
		t.Fatalf("stable redirect fallback resolved %q, want 1.7.2", tag)
	}
}

func TestJ03DevChannelUnaffected(t *testing.T) {
	// dev 渠道不受影响：API 路径返回 beta tag 仍可解析。
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	clearReleasesCacheForTest(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"tag_name":"1.7.2-beta1","name":"x","draft":false,"prerelease":true},{"tag_name":"1.7.1","name":"y","draft":false,"prerelease":false}]`)
	}))
	defer api.Close()
	swapGitHubBasesForTest(t, api.URL, api.URL)

	tag, err := resolveLatestReleaseByChannel("dev")
	if err != nil {
		t.Fatalf("dev channel must still resolve a beta tag: %v", err)
	}
	if tag != "1.7.2-beta1" {
		t.Fatalf("dev channel resolved %q, want 1.7.2-beta1", tag)
	}
}

func TestJ03SystemUpgradeRedirectFallbackRejectsBeta(t *testing.T) {
	// 面板侧同口径：API 故障 + 重定向指向 beta 时稳定版解析必须报错。
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	clearReleasesCacheForTest(t)
	limited := j03RateLimitedAPI(t)
	web := j03RedirectWeb(t, "1.7.2-beta1")
	oldAPIBase := systemUpgradeAPIBaseURL
	systemUpgradeAPIBaseURL = limited.URL
	t.Cleanup(func() { systemUpgradeAPIBaseURL = oldAPIBase })
	swapGitHubBasesForTest(t, limited.URL, web.URL)

	r, err := repo.Open(t.TempDir() + "/j03-sysup.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if err := r.UpsertConfig("github_proxy_enabled", "false", time.Now().UnixMilli()); err != nil {
		t.Fatalf("disable github proxy: %v", err)
	}
	h := &Handler{repo: r}
	tag, err := h.resolveSystemUpgradeLatestReleaseByChannel("stable")
	if err == nil {
		t.Fatalf("system upgrade stable channel must not resolve to a beta tag via redirect fallback, got %q", tag)
	}

	// 正例：重定向指向纯数字 tag 时面板侧仍能解析。
	clearReleasesCacheForTest(t)
	webOK := j03RedirectWeb(t, "1.7.2")
	swapGitHubBasesForTest(t, limited.URL, webOK.URL)
	tag, err = h.resolveSystemUpgradeLatestReleaseByChannel("stable")
	if err != nil {
		t.Fatalf("system upgrade stable redirect fallback must still resolve a numeric tag: %v", err)
	}
	if tag != "1.7.2" {
		t.Fatalf("system upgrade stable redirect fallback resolved %q, want 1.7.2", tag)
	}
}
