package handler

import (
	"net"
	"net/http"
	"testing"
)

// R2-02-02 回归：gist 的 CheckRedirect 此前只按主机判令牌去留——
// 同主机 https→http 降级时 gistTokenAllowedForURL 恒真，Bearer 被
// 原样带过降级跳、明文过线（C4-02 的联邦腿 checkFederationRedirect
// 已按「主机变化或 scheme 降级即剥凭据」修好，gist 孪生面漏修）。
// 本文件以与 federation_c402_test 同形的直接调用钉修复：降级必剥、
// 同 scheme 同主机保留、跨主机剥离（既有行为不回退）。

func stubGistLookupIP(t *testing.T, ip string) {
	t.Helper()
	prev := gistLookupIP
	parsed := net.ParseIP(ip)
	if parsed == nil {
		t.Fatalf("bad stub ip %q", ip)
	}
	gistLookupIP = func(host string) ([]net.IP, error) { return []net.IP{parsed}, nil }
	t.Cleanup(func() { gistLookupIP = prev })
}

func r202GistRequest(t *testing.T, rawurl string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, rawurl, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer r202-test-token")
	return req
}

func TestGistRedirectDowngradeStripsTokenR20202(t *testing.T) {
	t.Setenv("FLMX_GIST_API_BASE", "https://api.github.com")
	stubGistLookupIP(t, "93.184.216.34")

	// 降级跳：https 原始请求 → 同主机 http。修复前 Authorization
	// 被保留（token-allowed 对同主机恒真）；修复后必须剥离。
	origin := r202GistRequest(t, "https://api.github.com/gists")
	down := r202GistRequest(t, "http://api.github.com/gists/abc")
	if err := checkGistRedirect(down, []*http.Request{origin}); err != nil {
		t.Fatal(err)
	}
	if got := down.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization survived an https->http downgrade redirect: %q", got)
	}

	// 对照 1：https → https 同主机，令牌保留。
	origin2 := r202GistRequest(t, "https://api.github.com/gists")
	same := r202GistRequest(t, "https://api.github.com/gists/def")
	if err := checkGistRedirect(same, []*http.Request{origin2}); err != nil {
		t.Fatal(err)
	}
	if got := same.Header.Get("Authorization"); got != "Bearer r202-test-token" {
		t.Fatalf("Authorization lost on a same-scheme same-host redirect: %q", got)
	}

	// 对照 2：http 自有形态 http → http 同主机（不是降级），保留。
	origin3 := r202GistRequest(t, "http://api.github.com/gists")
	plain := r202GistRequest(t, "http://api.github.com/gists/ghi")
	if err := checkGistRedirect(plain, []*http.Request{origin3}); err != nil {
		t.Fatal(err)
	}
	if got := plain.Header.Get("Authorization"); got != "Bearer r202-test-token" {
		t.Fatalf("Authorization lost on an http->http same-host redirect: %q", got)
	}

	// 既有行为钉：跨主机同 scheme 仍剥离（不因本次修改回退）。
	origin4 := r202GistRequest(t, "https://api.github.com/gists")
	cross := r202GistRequest(t, "https://example.com/gists/abc")
	if err := checkGistRedirect(cross, []*http.Request{origin4}); err != nil {
		t.Fatal(err)
	}
	if got := cross.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization survived a cross-host redirect: %q", got)
	}
}
