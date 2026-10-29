package handler

// R7-C-01 regression tests: Gist outbound must go through a controlled
// client — hard-deny layer always on (initial URL and every redirect
// hop), and the Authorization Bearer token must only be attached to
// same-origin / official API host requests. The raw_url download leg
// must never carry the token.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const r7GistToken = "r7-test-token-1234567890"

// raw_url leg: a second server (different origin than the API base)
// records the Authorization header it receives. Post-fix it must be
// empty; pre-fix gistRequest attached the token unconditionally.
func TestR7C01GistRawURLDoesNotCarryToken(t *testing.T) {
	plain := []byte(`{"version":"1.0","users":[]}`)
	var rawAuth atomic.Value
	rawAuth.Store("")
	var rawHits atomic.Int32
	rawSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawHits.Add(1)
		rawAuth.Store(r.Header.Get("Authorization"))
		_, _ = w.Write(plain)
	}))
	defer rawSrv.Close()

	var apiSrv *httptest.Server
	apiSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gists/g1/v1" {
			writeJSON(w, 200, map[string]interface{}{"files": map[string]interface{}{
				gistFileName: map[string]interface{}{"content": "stale", "truncated": true, "raw_url": rawSrv.URL + "/raw/v1"},
			}})
			return
		}
		writeJSON(w, 404, map[string]interface{}{"message": "not found"})
	}))
	defer apiSrv.Close()
	t.Setenv("FLMX_GIST_API_BASE", apiSrv.URL)
	t.Setenv("FLVX_GIST_API_BASE", "")

	got, _, err := gistVersionContent(context.Background(), r7GistToken, "g1", "v1", "")
	if err != nil {
		t.Fatalf("gistVersionContent: %v", err)
	}
	if string(got) != string(plain) {
		t.Fatalf("content = %q, want %q", got, plain)
	}
	if rawHits.Load() != 1 {
		t.Fatalf("raw server hits = %d, want 1", rawHits.Load())
	}
	if auth := rawAuth.Load().(string); auth != "" {
		t.Fatalf("raw_url leg carried Authorization %q, want none", auth)
	}
}

// raw_url pointing at a hard-deny literal (cloud metadata) must be
// rejected by policy before any dial, even when masked with userinfo:
// https://example.com@169.254.169.254/ — the real host is the metadata
// address. The reverse userinfo form (userinfo = metadata-looking
// string, real host = a reachable test server) must still fetch, and
// still without a token.
func TestR7C01GistRawURLHardDenyAndUserinfo(t *testing.T) {
	plain := []byte(`{"version":"1.0","users":[]}`)
	var rawAuth atomic.Value
	rawAuth.Store("unset")
	rawSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawAuth.Store(r.Header.Get("Authorization"))
		_, _ = w.Write(plain)
	}))
	defer rawSrv.Close()
	rawHost := strings.TrimPrefix(rawSrv.URL, "http://")

	var mode atomic.Int32 // 0 = metadata raw_url, 1 = reverse userinfo
	var apiSrv *httptest.Server
	apiSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := "http://example.com@169.254.169.254/raw"
		if mode.Load() == 1 {
			raw = "http://169.254.169.254@" + rawHost + "/raw"
		}
		writeJSON(w, 200, map[string]interface{}{"files": map[string]interface{}{
			gistFileName: map[string]interface{}{"content": "stale", "truncated": true, "raw_url": raw},
		}})
	}))
	defer apiSrv.Close()
	t.Setenv("FLMX_GIST_API_BASE", apiSrv.URL)
	t.Setenv("FLVX_GIST_API_BASE", "")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, _, err := gistVersionContent(ctx, r7GistToken, "g1", "v1", ""); err == nil {
		t.Fatal("raw_url to metadata address must be rejected")
	} else if !strings.Contains(err.Error(), "拒绝") && !strings.Contains(err.Error(), "元数据") && !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("rejection must be a policy rejection, got: %v", err)
	}

	mode.Store(1)
	got, _, err := gistVersionContent(ctx, r7GistToken, "g1", "v1", "")
	if err != nil {
		t.Fatalf("reverse userinfo raw_url (real host = test server) must fetch: %v", err)
	}
	if string(got) != string(plain) {
		t.Fatalf("content = %q, want %q", got, plain)
	}
	// The Gist Bearer token must not reach the raw leg. (Go's HTTP
	// stack may synthesize "Authorization: Basic <userinfo>" from the
	// raw_url's own userinfo — that string is part of the URL the API
	// returned, not the panel's token, and is not what R7-C-01 is
	// about; assert on the token itself.)
	if auth := rawAuth.Load().(string); strings.Contains(auth, r7GistToken) || strings.HasPrefix(auth, "Bearer ") {
		t.Fatalf("raw_url leg carried the Gist token, Authorization %q", auth)
	}
}

// Direct gistRequest to a hard-deny target (metadata literal and the
// NAT64-embedded form) must be rejected by policy, not by a dial
// failure.
func TestR7C01GistRequestHardDenyDirect(t *testing.T) {
	for _, target := range []string{
		"http://169.254.169.254/latest/meta-data",
		"http://[64:ff9b::a9fe:a9fe]/latest/meta-data",
		"http://[2002:a9fe:a9fe::]/latest/meta-data",
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := gistRequest(ctx, http.MethodGet, target, r7GistToken, nil, nil)
		cancel()
		if err == nil {
			t.Fatalf("%s: request must be rejected", target)
		}
		if !strings.Contains(err.Error(), "拒绝") && !strings.Contains(err.Error(), "元数据") && !strings.Contains(err.Error(), "blocked") {
			t.Fatalf("%s: rejection must be a policy rejection, got: %v", target, err)
		}
	}
}

// A 302 from the API base to a hard-deny target must be stopped at the
// redirect hop (per-hop revalidation), with a policy error.
func TestR7C01GistRedirectToHardDenyBlocked(t *testing.T) {
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/evil", http.StatusFound)
	}))
	defer apiSrv.Close()
	t.Setenv("FLMX_GIST_API_BASE", apiSrv.URL)
	t.Setenv("FLVX_GIST_API_BASE", "")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := gistRequest(ctx, http.MethodGet, apiSrv.URL+"/gists?per_page=1", r7GistToken, nil, nil)
	if err == nil {
		t.Fatal("redirect to metadata address must be rejected")
	}
	if !strings.Contains(err.Error(), "拒绝") && !strings.Contains(err.Error(), "元数据") && !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("rejection must be a policy rejection, got: %v", err)
	}
}

// Same-origin API requests keep the token (the fix must not break the
// normal Gist API path): the mock records Authorization on the base.
func TestR7C01GistAPITokenSameOriginPreserved(t *testing.T) {
	m := newMockGistServer(t, func(w http.ResponseWriter, req *http.Request, rec *mockGistServer) {
		writeJSON(w, 200, map[string]interface{}{"id": "x"})
	})
	if err := gistRequest(context.Background(), http.MethodGet, gistAPIBase()+"/gists?per_page=1", r7GistToken, nil, nil); err != nil {
		t.Fatalf("gistRequest same-origin: %v", err)
	}
	if len(m.reqs) != 1 || m.reqs[0].Auth != "Bearer "+r7GistToken {
		t.Fatalf("same-origin API request must carry the token, reqs=%+v", m.reqs)
	}
}
