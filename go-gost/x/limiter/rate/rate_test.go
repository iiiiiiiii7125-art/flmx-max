package rate

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"io"

	corelogger "github.com/go-gost/core/logger"
	xlogger "github.com/go-gost/x/logger"
)

// The per-key limiter table must stay bounded under externally driven
// keys (P1-07 grow-only family): hard cap plus idle sweep, with
// recomputation after eviction returning the same limit values.
// Note: construction without LoggerOption panics in load() on the
// unguarded logger (pre-existing quirk, both limiter twins), so the
// tests attach one.

func TestRateLimiterTableHardCap(t *testing.T) {
	lim := NewRateLimiter(LimitsOption("$$ 1000"), LoggerOption(xlogger.NewLogger(xlogger.OutputOption(io.Discard), xlogger.LevelOption(corelogger.ErrorLevel)))).(*rateLimiter)
	total := maxCachedLimiters + 500
	for i := 0; i < total; i++ {
		lim.Limiter(fmt.Sprintf("10.0.%d.%d", i/256, i%256))
	}
	if n := lim.cachedCount(); n > maxCachedLimiters {
		t.Fatalf("table exceeded hard cap: %d > %d", n, maxCachedLimiters)
	}
}

func TestRateLimiterIdleSweepAndRecompute(t *testing.T) {
	lim := NewRateLimiter(LimitsOption("$$ 1000"), LoggerOption(xlogger.NewLogger(xlogger.OutputOption(io.Discard), xlogger.LevelOption(corelogger.ErrorLevel)))).(*rateLimiter)
	key := "192.0.2.1"
	got := lim.Limiter(key)
	if got == nil || got.Limit() != 1000 {
		t.Fatalf("unexpected limiter for %s: %v", key, got)
	}

	// Age the entry past the idle TTL and force a sweep via a fresh key.
	lim.mu.Lock()
	lim.limits[key].lastUsed = time.Now().Add(-limiterIdleTTL - time.Minute)
	lim.lastSweep = time.Time{}
	lim.mu.Unlock()
	lim.Limiter("192.0.2.2")

	if n := lim.cachedCount(); n != 1 {
		t.Fatalf("idle entry not swept: cached=%d", n)
	}
	// Recompute after eviction must yield the same configured limit.
	got = lim.Limiter(key)
	if got == nil || got.Limit() != 1000 {
		t.Fatalf("recomputed limiter differs: %v", got)
	}
}

func TestRateLimiterConcurrentAccess(t *testing.T) {
	lim := NewRateLimiter(LimitsOption("$$ 1000"), LoggerOption(xlogger.NewLogger(xlogger.OutputOption(io.Discard), xlogger.LevelOption(corelogger.ErrorLevel))))
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				lim.Limiter(fmt.Sprintf("10.1.%d.%d", w, i%128))
			}
		}(w)
	}
	wg.Wait()
}

func BenchmarkRateLimiterCacheHit(b *testing.B) {
	lim := NewRateLimiter(LimitsOption("$$ 1000"), LoggerOption(xlogger.NewLogger(xlogger.OutputOption(io.Discard), xlogger.LevelOption(corelogger.ErrorLevel))))
	lim.Limiter("192.0.2.1")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		lim.Limiter("192.0.2.1")
	}
}
