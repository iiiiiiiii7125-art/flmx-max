package socket

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gost/x/internal/util/download"
)

// R8-T-09: the agent self-upgrade download client carried
// Timeout: 60s — a whole-request wall counted from byte zero (it
// spans the body read). A link slow enough that the ~10MB binary
// needs >60s in a single GET could NEVER upgrade, however patient
// the panel's 300s upgradeTimeout is; and because the body was
// read by the caller after the fallback helper returned, the
// fallback could not rescue the mid-body death either. The fix:
// one shared budget (upgradeDownloadTimeout, mirroring x/kernel's
// downloadTimeout) threaded as a context through both legs and
// their retries; the production client carries no per-GET wall,
// only a per-attempt response-header cap so a hung proxy still
// fails over with budget to spare.
//
// KU4 (1.7.7): the header-stage GET helper these tests originally
// drove (getWithUpgradeProxyFallbackCtx) is deleted — route
// policy now lives in download.TryPlan and the attempt unit is
// GET+body (downloadUpgradeAttempt). The tests below keep every
// T-09 behavioral assertion, re-expressed against
// downloadUpgradeBinary with explicit static route plans.

// dribbleUpgradeServer serves payload slowly: headers and the first
// chunk immediately, then one chunk per gap.
func dribbleUpgradeServer(payload []byte, chunk int, gap time.Duration) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		for off := 0; off < len(payload); off += chunk {
			end := off + chunk
			if end > len(payload) {
				end = len(payload)
			}
			if _, err := w.Write(payload[off:end]); err != nil {
				return
			}
			if fl != nil {
				fl.Flush()
			}
			if end < len(payload) {
				time.Sleep(gap)
			}
		}
	}))
}

func slowUpgradePayload() []byte {
	payload := make([]byte, 16*1024)
	for i := range payload {
		payload[i] = byte(i * 7)
	}
	return payload
}

func TestR8T09SlowBodyCompletesWithinOperationBudget(t *testing.T) {
	payload := slowUpgradePayload()
	srv := dribbleUpgradeServer(payload, 2048, 180*time.Millisecond) // ~1.4s total
	defer srv.Close()

	tmp := filepath.Join(t.TempDir(), "flux_agent.new")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	n, _, err := downloadUpgradeBinary(ctx, newUpgradeDownloadClient(""), download.NewStaticPlan([]download.Route{{}}), srv.URL+"/gost-amd64", tmp, nil)
	if err != nil {
		t.Fatalf("slow transfer within the operation budget must succeed, got: %v", err)
	}
	if n != int64(len(payload)) {
		t.Fatalf("downloaded = %d bytes, want %d", n, len(payload))
	}
	body, err := os.ReadFile(tmp)
	if err != nil || len(body) != len(payload) {
		t.Fatalf("staged body = %d bytes (err=%v), want %d", len(body), err, len(payload))
	}
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Fatalf("transfer finished in %v; the slow path was not exercised", elapsed)
	}
}

func TestR8T09SlowBodyViaProxyDoesNotFallBack(t *testing.T) {
	payload := slowUpgradePayload()
	var directHits int32
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&directHits, 1)
		fmt.Fprint(w, "direct-body")
	}))
	defer direct.Close()
	proxy := dribbleUpgradeServer(payload, 2048, 180*time.Millisecond)
	defer proxy.Close()

	tmp := filepath.Join(t.TempDir(), "flux_agent.new")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	plan := download.NewStaticPlan([]download.Route{{Prefix: proxy.URL}, {}})
	n, _, err := downloadUpgradeBinary(ctx, newUpgradeDownloadClient(""), plan, direct.URL+"/gost-amd64", tmp, nil)
	if err != nil {
		t.Fatalf("slow proxied transfer must succeed without fallback, got: %v", err)
	}
	if n != int64(len(payload)) {
		t.Fatalf("downloaded = %d bytes, want the proxied payload %d", n, len(payload))
	}
	if atomic.LoadInt32(&directHits) != 0 {
		t.Fatalf("a slow-but-working proxy attempt must not fall back, direct hits = %d", directHits)
	}
}

func TestR8T09ProductionClientCarriesNoPerGETWall(t *testing.T) {
	client := newUpgradeDownloadClient("")
	if client.Timeout != 0 {
		t.Fatalf("production upgrade client must not carry a whole-request Timeout wall, got %v", client.Timeout)
	}
	tr, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("production upgrade client transport = %T, want *http.Transport", client.Transport)
	}
	if tr.ResponseHeaderTimeout != upgradeDownloadHeaderTimeout {
		t.Fatalf("ResponseHeaderTimeout = %v, want the per-attempt header cap %v", tr.ResponseHeaderTimeout, upgradeDownloadHeaderTimeout)
	}
	if upgradeDownloadTimeout != 4*time.Minute {
		t.Fatalf("upgradeDownloadTimeout = %v, want 4m (mirrors x/kernel downloadTimeout, inside the panel's 300s patience)", upgradeDownloadTimeout)
	}
}

// Characterization of the defect mechanism (passes before and
// after the fix): a caller-imposed whole-request cap kills a slow
// body mid-transfer, after headers already succeeded — which is
// why the production client must not carry one. Re-expressed with
// a raw client since the header-stage helper it originally used
// is deleted (KU4); the mechanism under characterization is the
// standard library's, not ours.
func TestR8T09LegacyPerGETCapDiesMidBody(t *testing.T) {
	payload := slowUpgradePayload()
	srv := dribbleUpgradeServer(payload, 2048, 180*time.Millisecond)
	defer srv.Close()

	client := &http.Client{Timeout: 300 * time.Millisecond} // legacy 60s wall, scaled down
	resp, err := client.Get(srv.URL + "/gost-amd64")
	if err != nil {
		t.Fatalf("header stage must succeed even under the legacy cap, got: %v", err)
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(resp.Body)
	if readErr == nil {
		t.Fatalf("legacy per-GET cap must kill the slow body mid-transfer")
	}
	if len(body) >= len(payload) {
		t.Fatalf("full body (%d bytes) arrived despite the legacy cap", len(body))
	}
}

func TestR8T09OperationBudgetBoundsWholeTransfer(t *testing.T) {
	payload := slowUpgradePayload()
	srv := dribbleUpgradeServer(payload, 2048, 180*time.Millisecond) // ~1.4s total
	defer srv.Close()

	tmp := filepath.Join(t.TempDir(), "flux_agent.new")
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err := downloadUpgradeBinary(ctx, newUpgradeDownloadClient(""), download.NewStaticPlan([]download.Route{{}}), srv.URL+"/gost-amd64", tmp, nil)
	if err == nil {
		t.Fatalf("transfer exceeding the operation budget must fail, not hang or succeed")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("budget-bound transfer took %v; the shared ctx must cut it", elapsed)
	}
}

func TestR8T09HungProxyFallsBackToDirectWithinBudget(t *testing.T) {
	// Shrink the per-attempt header cap so the hung-proxy attempt
	// fails over fast; the shared budget stays generous.
	old := upgradeDownloadHeaderTimeout
	upgradeDownloadHeaderTimeout = 300 * time.Millisecond
	defer func() { upgradeDownloadHeaderTimeout = old }()

	payload := []byte("direct-body")
	var directHits int32
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&directHits, 1)
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		_, _ = w.Write(payload)
	}))
	defer direct.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Accept and never send headers (stuck, not slow).
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}))
	defer proxy.Close()

	tmp := filepath.Join(t.TempDir(), "flux_agent.new")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	plan := download.NewStaticPlan([]download.Route{{Prefix: proxy.URL}, {}})
	n, _, err := downloadUpgradeBinary(ctx, newUpgradeDownloadClient(""), plan, direct.URL+"/gost-amd64", tmp, nil)
	if err != nil {
		t.Fatalf("hung proxy must fall back to direct within the shared budget, got: %v", err)
	}
	if n != int64(len(payload)) {
		t.Fatalf("downloaded = %d bytes, want the direct payload %d", n, len(payload))
	}
	if atomic.LoadInt32(&directHits) != 1 {
		t.Fatalf("direct must be hit exactly once, got %d", directHits)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("failover took %v; the hung attempt must die at the header cap, not the shared budget", elapsed)
	}
}
