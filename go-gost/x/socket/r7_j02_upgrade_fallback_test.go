package socket

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// R7-J-02: agent self-upgrade downloads (binary + checksum GETs in
// handleUpgradeAgent) were the only one of six download surfaces with
// no proxy-failure -> strip-prefix direct retry. These tests pin the
// fallback helper used by both GETs: proxy 403 -> direct succeeds,
// double failure -> merged error, no proxy -> no spurious retry,
// proxy success -> direct never touched.

func TestR7J02StripUpgradeProxyPrefix(t *testing.T) {
	cases := map[string]string{
		"https://proxy.example.com/https://github.com/x/y": "https://github.com/x/y",
		"http://127.0.0.1:8080/http://example.com/a":        "http://example.com/a",
		"https://github.com/x/y":                            "",
		"":                                                  "",
	}
	for in, want := range cases {
		if got := stripUpgradeProxyPrefix(in); got != want {
			t.Errorf("stripUpgradeProxyPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestR7J02ProxyForbiddenFallsBackToDirect(t *testing.T) {
	var directHits int32
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&directHits, 1)
		fmt.Fprint(w, "binary-bytes")
	}))
	defer direct.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, "Invalid input.")
	}))
	defer proxy.Close()

	proxiedURL := proxy.URL + "/" + direct.URL + "/gost-amd64"
	resp, err := getWithUpgradeProxyFallback(http.DefaultClient, proxiedURL)
	if err != nil {
		t.Fatalf("proxy 403 must fall back to direct, got error: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "binary-bytes" {
		t.Fatalf("fallback body = %q, want direct body", string(body))
	}
	if atomic.LoadInt32(&directHits) != 1 {
		t.Fatalf("direct must be hit exactly once, got %d", directHits)
	}
}

func TestR7J02ChecksumProxyErrorFallsBackToDirect(t *testing.T) {
	// Transport-level proxy failure (connection refused shape is
	// covered by the 403 case above; here the proxy answers 502 for
	// the checksum GET, the second of the two upgrade GETs).
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "abc123  gost-amd64")
	}))
	defer direct.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()

	proxiedURL := proxy.URL + "/" + direct.URL + "/gost-amd64.sha256"
	resp, err := getWithUpgradeProxyFallback(http.DefaultClient, proxiedURL)
	if err != nil {
		t.Fatalf("checksum proxy failure must fall back to direct, got: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.HasPrefix(string(body), "abc123") {
		t.Fatalf("checksum body = %q", string(body))
	}
}

func TestR7J02DoubleFailureMergesErrors(t *testing.T) {
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer direct.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer proxy.Close()

	proxiedURL := proxy.URL + "/" + direct.URL + "/gost-amd64"
	resp, err := getWithUpgradeProxyFallback(http.DefaultClient, proxiedURL)
	if err == nil {
		if resp != nil {
			resp.Body.Close()
		}
		t.Fatalf("double failure must return an error")
	}
	if !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "500") {
		t.Fatalf("merged error must carry both failures (proxy 403 + direct 500), got: %v", err)
	}
	if !strings.Contains(err.Error(), "直连") {
		t.Fatalf("merged error must mention the direct retry, got: %v", err)
	}
}

func TestR7J02NoProxyNoRetryAndProxySuccessSkipsDirect(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	if _, err := getWithUpgradeProxyFallback(http.DefaultClient, srv.URL+"/gost-amd64"); err == nil {
		t.Fatalf("direct-only 404 must error")
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("no-proxy URL must be fetched exactly once, got %d hits", hits)
	}

	var directHits int32
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&directHits, 1)
		fmt.Fprint(w, "direct")
	}))
	defer direct.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "via-proxy")
	}))
	defer proxy.Close()
	resp, err := getWithUpgradeProxyFallback(http.DefaultClient, proxy.URL+"/"+direct.URL+"/gost-amd64")
	if err != nil {
		t.Fatalf("proxy success must succeed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "via-proxy" || atomic.LoadInt32(&directHits) != 0 {
		t.Fatalf("proxy success must not touch direct: body=%q directHits=%d", string(body), directHits)
	}
}
