package kernel

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// F-061 回归：resolveGitHub 的 API 请求也必须走代理（镜像）前缀，失败时
// 回退直连——此前代理只拼下载地址，API 直连 api.github.com，镜像-only
// 节点（ghfast 类镜像不转发 API）在安装 sing-box 等内核时整单失败。

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

func TestResolveGitHubAPIThroughProxy(t *testing.T) {
	backend := f061APIServer(t)

	// 直连 API 一律 403（模拟 api.github.com 在该节点网络不可用）。
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer direct.Close()

	// 镜像代理：路径为 /<目标完整URL>，取其中 /repos/ 起的后缀转发给后端。
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
		_, _ = io.Copy(w, resp.Body)
	}))
	defer proxy.Close()

	old := githubAPIBase
	githubAPIBase = direct.URL
	defer func() { githubAPIBase = old }()

	g := &GitHubRelease{Repo: "SagerNet/sing-box", Asset: "sing-box-{VERSION}-{ARCH}.tar.gz", Proxy: proxy.URL}
	url, digest, err := resolveGitHub(context.Background(), g, "amd64")
	if err != nil {
		t.Fatalf("带代理时 API 必须经代理成功: %v", err)
	}
	wantURL := proxy.URL + "/https://github.com/SagerNet/sing-box/releases/download/v1.2.3/sing-box-1.2.3-amd64.tar.gz"
	if url != wantURL {
		t.Fatalf("url = %q, want %q（下载地址继续拼代理前缀）", url, wantURL)
	}
	if digest != strings.Repeat("a", 64) {
		t.Fatalf("digest = %q", digest)
	}
}

func TestResolveGitHubAPIFallsBackToDirect(t *testing.T) {
	backend := f061APIServer(t)

	// 代理对 API 回 403（如 ghfast 不转发 API），必须剥离前缀回退直连。
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, "Invalid input.")
	}))
	defer proxy.Close()

	old := githubAPIBase
	githubAPIBase = backend.URL
	defer func() { githubAPIBase = old }()

	g := &GitHubRelease{Repo: "SagerNet/sing-box", Asset: "sing-box-{VERSION}-{ARCH}.tar.gz", Proxy: proxy.URL}
	_, digest, err := resolveGitHub(context.Background(), g, "amd64")
	if err != nil {
		t.Fatalf("代理拒绝 API 时必须回退直连: %v", err)
	}
	if digest != strings.Repeat("a", 64) {
		t.Fatalf("digest = %q", digest)
	}
}
