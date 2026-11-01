package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// Round-5 L2-01, integration form: a token-bucket route that
// starts fast and then trickles used to hold the whole shared
// budget — the zero-byte stall watchdog never fired while bytes
// kept moving, and every later route starved on the expired
// context. The trickle leg must now fail with ErrTooSlow and
// TryPlan must complete the file over the next route, resuming
// from the bytes the trickling leg managed to persist.

// trickleServer serves payload in small chunks with a sleep
// between them (no Range support: a plain 200 body).
func trickleServer(payload []byte, chunk int, interval time.Duration) *httptest.Server {
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
			time.Sleep(interval)
		}
	}))
}

func TestTryPlanTrickleLegFailsOverToFastRoute(t *testing.T) {
	oldGrace, oldRate := TrickleGrace, TrickleMinRate
	TrickleGrace, TrickleMinRate = 300*time.Millisecond, 256<<10 // 256 KiB/s floor
	defer func() { TrickleGrace, TrickleMinRate = oldGrace, oldRate }()

	payload := resumePayload(64 << 10)                         // 64KB
	slow := trickleServer(payload, 1024, 100*time.Millisecond) // ~10KB/s: hours-fast start, then a trickle
	defer slow.Close()
	var fastServed atomic.Int64
	fast := rangeCapableServer(payload, &fastServed)
	defer fast.Close()

	plan := NewStaticPlan([]Route{{Prefix: slow.URL}, {Prefix: fast.URL}})
	transfer := NewFileTransfer(filepath.Join(t.TempDir(), "file.bin"), 0, 0o644)
	var slowErr error
	start := time.Now()
	_, err := TryPlan(plan, "http://example.invalid/file.bin", func(route Route, url string) error {
		watcher := NewWatcher(30 * time.Second) // stall window far away: only WDG-02 can fail the slow leg
		err := transfer.Attempt(context.Background(), watcher, http.DefaultClient.Do, url, nil)
		if route.Prefix == slow.URL {
			slowErr = err
		}
		return err
	})
	if err != nil {
		t.Fatalf("trickle leg must fail over to the fast route, got: %v", err)
	}
	if !errors.Is(slowErr, ErrTooSlow) {
		t.Fatalf("trickling leg error = %v, want ErrTooSlow", slowErr)
	}
	sum := sha256.Sum256(payload)
	if got := transfer.Sum(); got != hex.EncodeToString(sum[:]) {
		t.Fatalf("assembled file hash = %s, want %s (resume after the trickle leg must stay byte-exact)", got, hex.EncodeToString(sum[:]))
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("failover took %v; the trickle leg must die at ~grace, not burn the budget", elapsed)
	}
}
