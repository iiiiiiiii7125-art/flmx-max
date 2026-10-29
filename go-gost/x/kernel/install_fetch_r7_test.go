package kernel

// R7-C-03: the shared download primitive fetch() must not follow
// redirects blindly. Policy (mirroring the verdict's adopted lane
// scheme): the entry URL is judged against the hard-deny layer;
// every redirect hop is re-judged — https must be preserved (no
// downgrade), the hop target must not be hard-denied or non-public,
// and hops are capped at 3 like the agent self-upgrade path (S-02).

import (
	"bytes"
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A redirect from the entry URL to a loopback target must be blocked
// at the hop and the target must never be contacted.
func TestR7C03FetchRedirectToInternalBlocked(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		_, _ = w.Write([]byte("internal-content"))
	}))
	defer target.Close()
	entry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/payload", http.StatusFound)
	}))
	defer entry.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var buf bytes.Buffer
	if err := fetch(ctx, entry.URL+"/start", 1<<20, &buf); err == nil {
		t.Fatalf("redirect to loopback target must be rejected, got content %q", buf.String())
	}
	if targetHits.Load() != 0 {
		t.Fatalf("internal target contacted %d times, want 0", targetHits.Load())
	}
}

// https entry redirecting to a plain-http target is a downgrade and
// must be blocked even before the target's own judgment. The test
// swaps http.DefaultTransport for one trusting the TLS test server,
// which fetch's client construction must inherit (DefaultClient did).
func TestR7C03FetchRedirectDowngradeBlocked(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		_, _ = w.Write([]byte("downgraded-content"))
	}))
	defer target.Close()
	entry := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/payload", http.StatusFound)
	}))
	defer entry.Close()

	old := http.DefaultTransport
	http.DefaultTransport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} // test-only trust for the local TLS server
	defer func() { http.DefaultTransport = old }()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var buf bytes.Buffer
	if err := fetch(ctx, entry.URL+"/start", 1<<20, &buf); err == nil {
		t.Fatalf("https -> http downgrade redirect must be rejected, got content %q", buf.String())
	}
	if targetHits.Load() != 0 {
		t.Fatalf("downgrade target contacted %d times, want 0", targetHits.Load())
	}
}

// Entry URL straight at a hard-deny literal must get a policy
// rejection, not a dial attempt.
func TestR7C03FetchHardDenyEntryBlocked(t *testing.T) {
	for _, u := range []string{
		"http://169.254.169.254/latest/meta-data",
		"http://[64:ff9b::a9fe:a9fe]/latest",
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		var buf bytes.Buffer
		err := fetch(ctx, u, 1<<20, &buf)
		cancel()
		if err == nil {
			t.Fatalf("%s: must be rejected", u)
		}
		if !strings.Contains(err.Error(), "已拒绝") && !strings.Contains(err.Error(), "blocked") {
			t.Fatalf("%s: want policy rejection, got: %v", u, err)
		}
	}
}

// Redirect policy unit格: the CheckRedirect closure itself —
// downgrade, hard-deny hop, non-public hop, hop cap (3, same as
// S-02), and a public https hop as the passing control. IP literals
// only, so no DNS is involved.
func TestR7C03FetchRedirectPolicyUnit(t *testing.T) {
	client := fetchHTTPClient()
	mkReq := func(raw string) *http.Request {
		req, err := http.NewRequest(http.MethodGet, raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		return req
	}
	viaHTTPS := []*http.Request{mkReq("https://example.com/start")}

	if err := client.CheckRedirect(mkReq("http://8.8.8.8/x"), viaHTTPS); err == nil {
		t.Error("https -> http downgrade hop must be rejected")
	}
	if err := client.CheckRedirect(mkReq("https://169.254.169.254/x"), viaHTTPS); err == nil {
		t.Error("hard-deny hop must be rejected")
	}
	if err := client.CheckRedirect(mkReq("https://10.0.0.5/x"), viaHTTPS); err == nil {
		t.Error("non-public hop must be rejected")
	}
	if err := client.CheckRedirect(mkReq("https://8.8.8.8/x"), viaHTTPS); err != nil {
		t.Errorf("public https hop must pass, got %v", err)
	}
	via3 := []*http.Request{mkReq("https://example.com/a"), mkReq("https://example.com/b"), mkReq("https://example.com/c")}
	if err := client.CheckRedirect(mkReq("https://8.8.8.8/x"), via3); err == nil {
		t.Error("hop beyond the cap of 3 must be rejected")
	}
}

// Positive control: a direct fetch from the (trusted, caller-built)
// entry URL still works — the install/geo consumers and their
// existing mirror-fallback tests depend on entry fetches to local
// test servers; the new policy lives on the hard-deny layer and on
// redirect hops, not on banning the entry outright.
func TestR7C03FetchDirectEntryStillWorks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("payload"))
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var buf bytes.Buffer
	if err := fetch(ctx, srv.URL+"/file", 1<<20, &buf); err != nil {
		t.Fatalf("direct entry fetch: %v", err)
	}
	if buf.String() != "payload" {
		t.Fatalf("content = %q, want payload", buf.String())
	}
}
