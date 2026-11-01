package kernel

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-gost/x/internal/util/download"
)

// F-061 回归、KU4 (1.7.7) 形态：resolveGitHub 的 API 请求与下载必须
// 走同一路由策略。此前 F-061 让 API 先经面板下发的代理前缀、失败
// 回退直连；KU4 起代理改由本机测速计划决定（download.Plan，探针
// 目标就是 API 地址、须回可解析的发布 JSON 才准入）：直连 API 被
// 阻断但某个内置候选可用时安装不再整单失败；面板下发的 g.Proxy
// 被忽略（字段仅保留 wire 兼容）；返回的资产地址为直连形式，
// 由 install 的计划负责选线。

const f061ReleaseJSON = `{"tag_name":"v1.2.3","assets":[{"name":"sing-box-1.2.3-amd64.tar.gz","digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","browser_download_url":"https://github.com/SagerNet/sing-box/releases/download/v1.2.3/sing-box-1.2.3-amd64.tar.gz"}]}`

func f061APIServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/repos/SagerNet/sing-box/releases/") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(f061ReleaseJSON))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func setKernelMirrorCandidates(t *testing.T, prefixes ...string) {
	t.Helper()
	old := download.MirrorPrefixes
	download.MirrorPrefixes = prefixes
	t.Cleanup(func() { download.MirrorPrefixes = old })
}

// f061ForwardingProxy stands in for a mirror proxy: paths of the
// form /<full target URL> are forwarded to the backend by their
// /repos/ suffix.
func f061ForwardingProxy(t *testing.T, backend *httptest.Server) *httptest.Server {
	t.Helper()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := strings.Index(r.URL.Path, "/repos/")
		if i < 0 {
			http.NotFound(w, r)
			return
		}
		resp, err := http.Get(backend.URL + r.URL.Path[i:])
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(proxy.Close)
	return proxy
}

// 直连 API 一律 403、候选代理能转发 API -> 准入走代理，返回直连
// 形式的资产地址（不再拼代理前缀）。
func TestKU4ResolveGitHubAPIThroughAdmittedProxy(t *testing.T) {
	backend := f061APIServer(t)

	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer direct.Close()
	proxy := f061ForwardingProxy(t, backend)
	setKernelMirrorCandidates(t, proxy.URL)

	old := githubAPIBase
	githubAPIBase = direct.URL
	defer func() { githubAPIBase = old }()

	g := &GitHubRelease{Repo: "SagerNet/sing-box", Asset: "sing-box-{VERSION}-{ARCH}.tar.gz"}
	url, digest, err := resolveGitHub(context.Background(), g, "amd64")
	if err != nil {
		t.Fatalf("直连 API 被阻断时必须经准入代理成功: %v", err)
	}
	wantURL := "https://github.com/SagerNet/sing-box/releases/download/v1.2.3/sing-box-1.2.3-amd64.tar.gz"
	if url != wantURL {
		t.Fatalf("url = %q, want 直连形式 %q", url, wantURL)
	}
	if digest != strings.Repeat("a", 64) {
		t.Fatalf("digest = %q", digest)
	}
}

// 面板下发的 Proxy 指向一个全 403 的地址、直连 API 可用 -> Proxy
// 必须被忽略，直连成功（路由由本机测速决定，不受面板设置摆布）。
func TestKU4ResolveGitHubIgnoresPanelProxy(t *testing.T) {
	backend := f061APIServer(t)

	badProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer badProxy.Close()
	// 候选清单保持 TestMain 的空集：计划只有直连。
	old := githubAPIBase
	githubAPIBase = backend.URL
	defer func() { githubAPIBase = old }()

	g := &GitHubRelease{Repo: "SagerNet/sing-box", Asset: "sing-box-{VERSION}-{ARCH}.tar.gz", Proxy: badProxy.URL}
	url, digest, err := resolveGitHub(context.Background(), g, "amd64")
	if err != nil {
		t.Fatalf("面板 Proxy 必须被忽略、直连成功: %v", err)
	}
	if !strings.HasPrefix(url, "https://github.com/") {
		t.Fatalf("url 应为直连形式, got %q", url)
	}
	if digest != strings.Repeat("a", 64) {
		t.Fatalf("digest = %q", digest)
	}
}
