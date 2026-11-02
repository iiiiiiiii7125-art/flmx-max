package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// RV2-01 (round-5 review of WDG-02): the fixed TrickleMinRate
// floor condemned every leg of a download whose routes were ALL
// slower than the floor — even when a single route could finish
// inside the operation budget (9.3MB agent binary at a uniform
// 40-55KiB/s, or the panel-source route alone at <64KiB/s). The
// judgment is now size/budget-aware: with a goal (remaining
// bytes, from Content-Length) and a deadline on the context, a
// leg dies only when its recent rate projects finishing past the
// deadline. These tests pin both directions and the two review
// scenarios (in scaled-down form: same ratios — rate below the
// fixed floor, rate above body/budget, legs×grace×rate < body).

// throttledRangeServer serves payload at ~rate bytes/s (16KiB
// chunks) and honors Range, so FileTransfer legs can resume.
// requests counts handler entries (the race-free signal for
// which legs ran); served counts bytes written.
func throttledRangeServer(payload []byte, rate int64, served *atomic.Int64, requests *atomic.Int64) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests != nil {
			requests.Add(1)
		}
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
		const chunk = 16 << 10
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
			served.Add(int64(end - off))
			if end < len(body) {
				time.Sleep(delay)
			}
		}
	}))
}

// A leg moving BELOW the fixed floor but fast enough for its
// goal inside the deadline must survive the trickle judgment.
func TestWatcherProjectionKeepsFeasibleSlowLeg(t *testing.T) {
	oldGrace := TrickleGrace
	TrickleGrace = 300 * time.Millisecond
	defer func() { TrickleGrace = oldGrace }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	w := NewWatcher(30 * time.Second) // stall rule far away
	wctx, stop := w.Watch(ctx)
	defer stop()
	w.SetGoal(500 << 10) // 500KB in 10s needs 50KiB/s

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-wctx.Done():
			t.Fatal("projection condemned a leg that finishes inside its deadline")
		default:
		}
		w.Note(6144) // ~60KiB/s: below the 64KiB/s fixed floor, above the required 50KiB/s
		time.Sleep(100 * time.Millisecond)
	}
	if w.TooSlow() {
		t.Fatal("TooSlow() must stay false for a feasible slow leg")
	}
}

// A leg whose rate cannot finish its goal inside the deadline is
// still condemned — the L2-01 aggression is preserved with the
// goal set (a 5MB goal in 10s at ~60KiB/s is the token bucket in
// miniature).
func TestWatcherProjectionCondemnsInfeasibleLeg(t *testing.T) {
	oldGrace := TrickleGrace
	TrickleGrace = 300 * time.Millisecond
	defer func() { TrickleGrace = oldGrace }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	w := NewWatcher(30 * time.Second)
	wctx, stop := w.Watch(ctx)
	defer stop()
	w.SetGoal(5 << 20) // needs ~512KiB/s

	go func() {
		for {
			select {
			case <-wctx.Done():
				return
			default:
			}
			w.Note(6144) // ~60KiB/s
			time.Sleep(100 * time.Millisecond)
		}
	}()
	start := time.Now()
	<-wctx.Done()
	if elapsed := time.Since(start); elapsed > 6*time.Second {
		t.Fatalf("infeasible leg condemned after %v, want shortly after the rate estimate firms up", elapsed)
	}
	if !w.TooSlow() {
		t.Fatal("TooSlow() must be true after the projection condemned the leg")
	}
}

// Review scenario 1, scaled: every route uniformly slow (rate
// below the fixed floor), body feasible on one route inside the
// budget. Under the fixed floor each leg died at the grace and
// legs×grace×rate fell short of the body; the first leg must now
// run to completion.
func TestTryPlanUniformSlowRoutesComplete(t *testing.T) {
	oldGrace, oldRate := TrickleGrace, TrickleMinRate
	TrickleGrace, TrickleMinRate = time.Second, 128<<10 // floor above every route's rate
	defer func() { TrickleGrace, TrickleMinRate = oldGrace, oldRate }()

	payload := resumePayload(600 << 10) // 600KB at 100KiB/s: 6s on one route
	var served [3]atomic.Int64
	var requests [3]atomic.Int64
	var routes []Route
	for i := 0; i < 3; i++ {
		srv := throttledRangeServer(payload, 100<<10, &served[i], &requests[i])
		defer srv.Close()
		routes = append(routes, Route{Prefix: srv.URL})
	}
	plan := NewStaticPlan(routes)
	transfer := NewFileTransfer(filepath.Join(t.TempDir(), "file.bin"), 0, 0o644)
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Second) // budget: needs ~67KiB/s
	defer cancel()
	_, err := TryPlan(plan, "http://example.invalid/file.bin", func(route Route, url string) error {
		watcher := NewWatcher(30 * time.Second)
		return transfer.Attempt(ctx, watcher, http.DefaultClient.Do, url, nil)
	})
	if err != nil {
		t.Fatalf("uniformly slow but feasible download must complete, got: %v", err)
	}
	sum := sha256.Sum256(payload)
	if got := transfer.Sum(); got != hex.EncodeToString(sum[:]) {
		t.Fatalf("assembled hash = %s, want %s", got, hex.EncodeToString(sum[:]))
	}
	if requests[0].Load() != 1 || requests[1].Load() != 0 || requests[2].Load() != 0 {
		t.Fatalf("requests per route = %d/%d/%d, want 1/0/0 (the first leg runs to completion, no failover)",
			requests[0].Load(), requests[1].Load(), requests[2].Load())
	}
}

// Review scenario 2, the sharpest form: the only live route is a
// single slow one (the panel-source-only topology — every other
// route dead). It must finish the body alone inside the budget.
func TestTryPlanSingleSlowRouteCompletes(t *testing.T) {
	oldGrace, oldRate := TrickleGrace, TrickleMinRate
	TrickleGrace, TrickleMinRate = time.Second, 128<<10
	defer func() { TrickleGrace, TrickleMinRate = oldGrace, oldRate }()

	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close() // connections now refused: a dead route

	payload := resumePayload(300 << 10) // 300KB at 100KiB/s: 3s alone
	var served, requests atomic.Int64
	slow := throttledRangeServer(payload, 100<<10, &served, &requests)
	defer slow.Close()

	plan := NewStaticPlan([]Route{{Prefix: deadURL}, {Prefix: slow.URL}})
	transfer := NewFileTransfer(filepath.Join(t.TempDir(), "file.bin"), 0, 0o644)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	_, err := TryPlan(plan, "http://example.invalid/file.bin", func(route Route, url string) error {
		watcher := NewWatcher(30 * time.Second)
		return transfer.Attempt(ctx, watcher, http.DefaultClient.Do, url, nil)
	})
	if err != nil {
		t.Fatalf("single slow route must complete the download alone, got: %v", err)
	}
	sum := sha256.Sum256(payload)
	if got := transfer.Sum(); got != hex.EncodeToString(sum[:]) {
		t.Fatalf("assembled hash = %s, want %s", got, hex.EncodeToString(sum[:]))
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("slow route saw %d requests, want exactly 1 (one leg, run to completion)", got)
	}
}
