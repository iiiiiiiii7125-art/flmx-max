package client

import (
	"net"
	"net/http"
	"testing"
)

// C4-02：checkFederationRedirect 此前只按 Host 字符串判跨主机剥离
// 凭据，完全不看 scheme——对端把 https 联邦请求 302 到同主机 http
// 地址时，Authorization（Bearer share token）与 X-Panel-Domain 被
// 标准库原样带过降级跳，明文过线。本文件以与审计探针同形的直接
// 调用钉修复：https→http 降级（同主机与否）必须同剥两头；同 scheme
// 同主机的正常跳与 http 部署自身的同 scheme 跳不受影响。

func c402Request(t *testing.T, rawurl string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, rawurl, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer share-token")
	req.Header.Set("X-Panel-Domain", "consumer.example.com")
	return req
}

func c402AssertStripped(t *testing.T, req *http.Request) {
	t.Helper()
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization survived a credential-stripping redirect: %q", got)
	}
	if got := req.Header.Get("X-Panel-Domain"); got != "" {
		t.Fatalf("X-Panel-Domain survived a credential-stripping redirect: %q", got)
	}
}

func c402AssertKept(t *testing.T, req *http.Request) {
	t.Helper()
	if got := req.Header.Get("Authorization"); got != "Bearer share-token" {
		t.Fatalf("Authorization lost on a same-scheme same-host redirect: %q", got)
	}
	if got := req.Header.Get("X-Panel-Domain"); got != "consumer.example.com" {
		t.Fatalf("X-Panel-Domain lost on a same-scheme same-host redirect: %q", got)
	}
}

func TestC402RedirectSchemeDowngradeStripsCredentials(t *testing.T) {
	stubFederationLookup(t, map[string][]net.IP{
		"peer.example.com":  {net.ParseIP("93.184.216.34")},
		"other.example.com": {net.ParseIP("93.184.216.34")},
	})

	// 降级跳：https 原始请求 → 同主机 http。标准库已把凭据头复制到
	// 跳转请求上（c402Request 预置），CheckRedirect 必须剥掉。
	origin := c402Request(t, "https://peer.example.com/api/v1/federation/connect")
	down := c402Request(t, "http://peer.example.com/api/v1/federation/connect")
	if err := checkFederationRedirect(down, []*http.Request{origin}); err != nil {
		t.Fatal(err)
	}
	c402AssertStripped(t, down)

	// 对照 1：https → https 同主机，凭据保留（正常对端跳转不受损）。
	origin2 := c402Request(t, "https://peer.example.com/api/v1/federation/connect")
	same := c402Request(t, "https://peer.example.com/api/v1/federation/runtime/command")
	if err := checkFederationRedirect(same, []*http.Request{origin2}); err != nil {
		t.Fatal(err)
	}
	c402AssertKept(t, same)

	// 对照 2：http 部署自身 http → http 同主机（运维自选明文形态，
	// 不是降级），凭据保留——本修复只针对 scheme 降级，不扩面。
	origin3 := c402Request(t, "http://peer.example.com/api/v1/federation/connect")
	plain := c402Request(t, "http://peer.example.com/api/v1/federation/runtime/command")
	if err := checkFederationRedirect(plain, []*http.Request{origin3}); err != nil {
		t.Fatal(err)
	}
	c402AssertKept(t, plain)

	// 既有行为钉：跨主机同 scheme 仍剥离（不因本次修改回退）。
	origin4 := c402Request(t, "https://peer.example.com/api/v1/federation/connect")
	cross := c402Request(t, "https://other.example.com/api/v1/federation/connect")
	if err := checkFederationRedirect(cross, []*http.Request{origin4}); err != nil {
		t.Fatal(err)
	}
	c402AssertStripped(t, cross)
}
