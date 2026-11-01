package kernel

// Panel-source GATE F2: the panel gh endpoint 301s every combined
// URL (embedded "https://" double slash) to its own cleaned path.
// fetch() judged that hop with the full non-public rule, so a panel
// addressed by a private IP (LAN / tailnet) could never serve a
// kernel through its own route — while the upgrade path took the
// same 301 fine. Fix: a redirect hop back to the entry URL's own
// host (same origin) is judged by the entry layer (hard-deny only);
// cross-host hops keep the full judgment.

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Private panel origin + same-origin 301 (the panel gh endpoint's
// ServeMux cleaning) must deliver the payload.
func TestPanelSourceFetchSameOriginPrivateRedirectSucceeds(t *testing.T) {
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "https://") {
			// ServeMux-style cleaning 301 to the same-origin
			// single-slash path.
			cleaned := strings.Replace(r.URL.Path, "https://", "https:/", 1)
			http.Redirect(w, r, cleaned, http.StatusMovedPermanently)
			return
		}
		_, _ = w.Write([]byte("kernel-bytes"))
	}))
	defer panel.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var buf bytes.Buffer
	entry := panel.URL + "/api/v1/open_api/gh/https://github.com/XTLS/Xray-core/releases/download/v1.0.0/Xray-linux-64.zip"
	if err := fetch(ctx, entry, 1<<20, &buf); err != nil {
		t.Fatalf("same-origin 301 from a private panel origin must succeed: %v", err)
	}
	if buf.String() != "kernel-bytes" {
		t.Fatalf("content = %q, want %q", buf.String(), "kernel-bytes")
	}
}

// A redirect away from the entry origin to a private target stays
// rejected at the hop; the target must never be contacted.
func TestPanelSourceFetchCrossHostPrivateRedirectStillRejected(t *testing.T) {
	var targetHits atomic.Int32
	ln, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Fatalf("listen on 127.0.0.2: %v", err)
	}
	targetSrv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		_, _ = w.Write([]byte("internal-content"))
	})}
	go func() { _ = targetSrv.Serve(ln) }()
	defer func() { _ = targetSrv.Close() }()
	targetURL := "http://" + ln.Addr().String() + "/payload"

	entry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, targetURL, http.StatusFound)
	}))
	defer entry.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var buf bytes.Buffer
	err = fetch(ctx, entry.URL+"/start", 1<<20, &buf)
	if err == nil {
		t.Fatalf("cross-host redirect to a private target must be rejected, got content %q", buf.String())
	}
	if !strings.Contains(err.Error(), "非公网") {
		t.Fatalf("want non-public policy rejection, got: %v", err)
	}
	if targetHits.Load() != 0 {
		t.Fatalf("private cross-host target contacted %d times, want 0", targetHits.Load())
	}
}

// Closure level: same-origin (host:port identical to the entry)
// private hop passes on the entry layer; same hostname with a
// different port is a different origin and keeps the full judgment.
func TestPanelSourceFetchRedirectSameOriginUnit(t *testing.T) {
	client := fetchHTTPClient()
	mkReq := func(raw string) *http.Request {
		req, err := http.NewRequest(http.MethodGet, raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		return req
	}
	via := []*http.Request{mkReq("http://172.31.240.1:6391/api/v1/open_api/gh/https://github.com/x/y.zip")}

	if err := client.CheckRedirect(mkReq("http://172.31.240.1:6391/api/v1/open_api/gh/https:/github.com/x/y.zip"), via); err != nil {
		t.Errorf("same-origin private hop must pass (entry layer), got %v", err)
	}
	if err := client.CheckRedirect(mkReq("http://172.31.240.1:9999/elsewhere"), via); err == nil {
		t.Error("same hostname but different port is cross-origin: private hop must be rejected")
	}
	if err := client.CheckRedirect(mkReq("https://10.0.0.5/x"), via); err == nil {
		t.Error("cross-host private hop must be rejected")
	}
}
