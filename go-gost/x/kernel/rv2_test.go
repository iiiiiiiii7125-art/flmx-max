package kernel

// Round-5 review fixes RV2-02 / RV2-03: the rule-set leg resumes
// across watchdog verdicts (and a slow leg that can finish inside
// the round budget is no longer condemned at all), and an anchor
// corroboration degradation is announced on the caller's
// panel-visible log surface instead of stdout only.

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gost/x/internal/util/download"
)

// rv2BigSRS builds a valid SRS file of ~size bytes: random
// (incompressible) payload, so the zlib stream stays big.
func rv2BigSRS(t *testing.T, size int) []byte {
	t.Helper()
	raw := make([]byte, size)
	if _, err := rand.New(rand.NewSource(42)).Read(raw); err != nil {
		t.Fatal(err)
	}
	return makeSRS(t, string(raw))
}

// rv2ThrottledRangeServer serves payload at ~rate bytes/s and
// honors Range, counting requests.
func rv2ThrottledRangeServer(payload []byte, rate int64, requests *atomic.Int64) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		start := 0
		if rh := r.Header.Get("Range"); strings.HasPrefix(rh, "bytes=") {
			if n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(rh, "bytes="), "-")); err == nil {
				start = n
			}
		}
		body := payload[start:]
		if start > 0 {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(payload)-1, len(payload)))
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			w.WriteHeader(http.StatusPartialContent)
		} else {
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			w.WriteHeader(http.StatusOK)
		}
		fl, _ := w.(http.Flusher)
		const chunk = 8 << 10
		delay := time.Duration(float64(chunk) / float64(rate) * float64(time.Second))
		for off := 0; off < len(body); off += chunk {
			end := off + chunk
			if end > len(body) {
				end = len(body)
			}
			if _, err := w.Write(body[off:end]); err != nil {
				return
			}
			if fl != nil {
				fl.Flush()
			}
			if end < len(body) {
				time.Sleep(delay)
			}
		}
	}))
}

// RV2-02: a multi-hundred-KB rule set on a route slower than the
// fixed trickle floor used to lose every leg at the grace (the
// in-memory fetch restarted from zero), so the set could never
// land. On the resumable transfer with the size/budget-aware
// judgment, the single slow leg — feasible inside the round
// budget — runs to completion in exactly one request.
func TestFetchRuleSetSlowRouteCompletes(t *testing.T) {
	oldGrace := download.TrickleGrace
	download.TrickleGrace = time.Second
	defer func() { download.TrickleGrace = oldGrace }()

	payload := rv2BigSRS(t, 384<<10) // ~384KB at 48KiB/s: ~8s on one leg
	var requests atomic.Int64
	srv := rv2ThrottledRangeServer(payload, 48<<10, &requests)
	defer srv.Close()

	plan := download.NewStaticPlan([]download.Route{{}})
	ctx, cancel := context.WithTimeout(context.Background(), 13*time.Second) // round budget: needs ~30KiB/s
	defer cancel()
	data, err := fetchRuleSet(ctx, plan, srv.URL+"/geosite-cn.srs")
	if err != nil {
		t.Fatalf("slow-but-feasible rule set must download, got: %v", err)
	}
	if len(data) != len(payload) {
		t.Fatalf("rule set size = %d, want %d", len(data), len(payload))
	}
	if err := validateSRSBytes(data); err != nil {
		t.Fatalf("downloaded rule set does not validate: %v", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("server saw %d requests, want 1 (the slow leg completes alone, no leg churn)", got)
	}
}

// RV2-03: when no second route can corroborate the anchor, the
// degradation is announced through the caller's note surface with
// the degradation named; a differing second opinion still hard
// fails; an agreeing one stays silent.
func TestCorroborateAnchorDigestDegradeNote(t *testing.T) {
	winner := download.Route{Prefix: "https://winner.invalid"}
	target := "https://example.invalid/release.sha256"

	var notes []string
	note := func(msg string) { notes = append(notes, msg) }

	dead := func(context.Context, *download.Plan) (string, error) {
		return "", errors.New("其他线路不可达")
	}
	if err := corroborateAnchorDigest(context.Background(), winner, target, dead, "abc123", note); err != nil {
		t.Fatalf("degradation must not fail the operation: %v", err)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "跨线路锚复核已降级") {
		t.Fatalf("degradation notice = %v, want one note naming 跨线路锚复核已降级", notes)
	}

	diff := func(context.Context, *download.Plan) (string, error) { return "def456", nil }
	if err := corroborateAnchorDigest(context.Background(), winner, target, diff, "abc123", note); err == nil {
		t.Fatal("differing corroboration must hard-fail")
	}

	notes = nil
	same := func(context.Context, *download.Plan) (string, error) { return "abc123", nil }
	if err := corroborateAnchorDigest(context.Background(), winner, target, same, "abc123", note); err != nil {
		t.Fatalf("agreeing corroboration must pass: %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("agreeing corroboration must stay silent, got notes %v", notes)
	}
}
