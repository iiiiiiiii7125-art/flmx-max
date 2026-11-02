// Package download holds the agent's shared download-progress
// primitive (R3-WDG-01). Every bulk download surface in the agent —
// kernel installs, geodata refreshes, the agent self-upgrade — used
// to be bounded only by a whole-operation budget (240s / 5min): a
// peer that answers 200 and then stalls or trickles the body burned
// the entire budget before the mirror/direct fallback leg could run
// (measured in audit/round-2/batch-upgrade-repro.md: 240s for 165KB
// of a 9.3MB body). The Watcher below adds the missing progress
// judgments:
//
//   - No progress (WDG-01): if no byte moves for StallWindow, the
//     download context is cancelled and the attempt fails fast
//     with ErrNoProgress.
//   - Trickle (WDG-02, round-5 L2-01): a peer that keeps making
//     SOME progress resets the no-progress timer forever, so the
//     zero-byte rule alone let a token-bucket line (~30KB/s after
//     a fast start) hold the whole 240s budget while every later
//     route starved on the expired context. The watcher therefore
//     also judges the attempt's throughput past TrickleGrace and
//     fails it with ErrTooSlow when the throughput cannot finish
//     the work — and thanks to FileTransfer resume, the bytes it
//     did deliver are kept for the next route.
//
// The throughput judgment is size/budget-aware whenever the
// attempt knows its remaining bytes and the operation deadline
// (RV2-01, round-5 review): FileTransfer and fetch report the
// goal from the response's Content-Length via SetGoal, and Watch
// captures the deadline of the context it wraps. The judgment
// then projects the finish from the attempt's recent-window
// rate: a leg is condemned only when the remaining bytes, at the
// rate it is actually moving, would land past the deadline. A
// slow leg that CAN still finish inside the budget is therefore
// never condemned — the fixed floor's blind spot (a uniform
// 40-55KiB/s fleet, or the panel-source route as the only live
// route, where every leg died at the grace with budget to spare)
// is closed, while a leg that cannot finish (the L2-01 token
// bucket: ~30KiB/s against a 9.3MB body in a 240s budget) is
// still condemned at the grace, so the first pass over the
// routes stays aggressive. When the size or the deadline is
// unknown (small in-memory fetches, chunked bodies), the fixed
// TrickleMinRate floor on the attempt's average rate applies,
// exactly the original WDG-02 rule.
//
// Either way the caller's existing fallback sequence runs with
// budget to spare. The watcher never extends any budget — the
// caller's overall deadline still applies unchanged.
package download

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"time"
)

// StallWindow is the no-progress window shared by every agent
// download surface: an attempt with zero byte progress for this
// long is declared stalled and fails. Registered in
// doc/audit/timeout-budgets.tsv (rows A001/A002, R3-WDG-01) and
// pinned by the socket/kernel budget tests.
const StallWindow = 45 * time.Second

// ErrNoProgress reports a download attempt aborted by the watchdog
// because no byte moved for the whole stall window.
var ErrNoProgress = errors.New("下载停滞：长时间无字节进展，已判失败")

// ErrTooSlow reports a download attempt aborted by the watchdog
// because bytes kept moving, but too slowly to finish inside the
// caller's whole-operation budget — under the fixed floor
// (average rate below TrickleMinRate) or, when the attempt knows
// its remaining bytes and deadline, under the size/budget-aware
// projection (round-5 L2-01, RV2-01).
var ErrTooSlow = errors.New("下载过慢：平均速率低于可用下限，已判失败")

// Trickle judgment parameters (WDG-02), registered alongside
// StallWindow in doc/audit/timeout-budgets.tsv rows A001/A002.
// TrickleGrace is the observation period before the rate judgment
// applies, so a line is never condemned on its first seconds
// (connect + TLS + slow start are measured into the average, but
// a viable line recovers its average well inside the grace).
// TrickleMinRate is the floor on the attempt's average throughput
// since its start — the fallback judgment when the attempt does
// not know its remaining bytes or the operation deadline (with a
// known goal and deadline the projection in trickleCondemned
// takes over, RV2-01). TrickleRateWindow is the look-back window
// of the rate estimate the projection uses: recent throughput,
// not the since-start average, so a burst-then-trickle line is
// judged by its current speed and a slow-headers-then-fast line
// is not condemned for its header wait. Vars so tests can shrink
// them.
var (
	TrickleGrace      = 15 * time.Second
	TrickleMinRate    = int64(64 << 10) // 64 KiB/s
	TrickleRateWindow = 10 * time.Second
)

// Watcher tracks byte progress of one download attempt. Create one
// per attempt (fallback legs get a fresh window), wrap the attempt
// context with Watch, and feed every transferred chunk to Note —
// directly or through the Writer/Reader adapters. Not safe to
// reuse across attempts.
type Watcher struct {
	window  time.Duration
	start   atomic.Int64 // unix nano of Watch start (trickle average base)
	bytes   atomic.Int64 // total bytes noted since Watch start
	last    atomic.Int64 // unix nano of last progress (or Watch start)
	stalled atomic.Bool
	slow    atomic.Bool

	// Size/budget awareness (RV2-01): goal is the number of bytes
	// this attempt must still deliver for its transfer to complete
	// (0 = unknown, see SetGoal); deadlineNano is the operation
	// deadline captured from the context Watch wrapped (0 = none);
	// firstByte is the unix nano of the first noted byte (the
	// projection's rate clock starts there, so header wait before
	// the body does not count against the line).
	goal         atomic.Int64
	deadlineNano atomic.Int64
	firstByte    atomic.Int64
}

// rateSample is one (time, cumulative bytes) point of an attempt,
// taken by the watch goroutine on every tick for the projection's
// recent-window rate estimate.
type rateSample struct {
	at    time.Time
	bytes int64
}

// NewWatcher returns a Watcher with the given no-progress window.
// A non-positive window disables the watchdog (Watch passes the
// context through unchanged).
func NewWatcher(window time.Duration) *Watcher {
	return &Watcher{window: window}
}

// Watch returns a context derived from ctx that is cancelled when
// no progress has been reported for the watcher's window, counted
// from the Watch call itself (so a peer that never sends headers
// also trips it). The returned stop function ends the watchdog
// goroutine and cancels the derived context; callers must defer
// it exactly like a context.CancelFunc.
func (w *Watcher) Watch(ctx context.Context) (context.Context, context.CancelFunc) {
	if w.window <= 0 {
		return ctx, func() {}
	}
	start := time.Now()
	w.start.Store(start.UnixNano())
	w.last.Store(start.UnixNano())
	if dl, ok := ctx.Deadline(); ok {
		w.deadlineNano.Store(dl.UnixNano())
	}
	dctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		interval := w.window / 4
		if interval < 5*time.Millisecond {
			interval = 5 * time.Millisecond
		}
		if interval > time.Second {
			interval = time.Second
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		var samples []rateSample
		for {
			select {
			case <-done:
				return
			case <-dctx.Done():
				return
			case now := <-ticker.C:
				if now.Sub(time.Unix(0, w.last.Load())) >= w.window {
					w.stalled.Store(true)
					cancel()
					return
				}
				// WDG-02 trickle judgment: past the grace period,
				// the attempt's throughput must be able to finish
				// the work (see trickleCondemned). Zero bytes is
				// the stall rule's case, not this one's (a peer
				// still inside its header wait is not "trickling").
				b := w.bytes.Load()
				samples = append(samples, rateSample{at: now, bytes: b})
				if elapsed := now.Sub(start); elapsed >= TrickleGrace {
					if b > 0 && w.trickleCondemned(now, elapsed, b, samples) {
						w.slow.Store(true)
						cancel()
						return
					}
				}
			}
		}
	}()
	var stopOnce atomic.Bool
	stop := func() {
		if stopOnce.CompareAndSwap(false, true) {
			close(done)
		}
		cancel()
	}
	return dctx, stop
}

// SetGoal reports how many bytes this attempt must still deliver
// for its transfer to complete (the response's total size minus
// the bytes already persisted by earlier legs). With a goal and a
// deadline on the watched context, the trickle judgment becomes
// the size/budget-aware projection (see the package note); with
// no goal (or no deadline) the fixed TrickleMinRate floor applies.
// A non-positive goal clears the awareness. Callers invoke it as
// soon as the response headers reveal the total — the judgment
// only starts at TrickleGrace, so the goal always lands in time.
func (w *Watcher) SetGoal(remaining int64) {
	if remaining < 0 {
		remaining = 0
	}
	w.goal.Store(remaining)
}

// trickleCondemned decides the WDG-02 verdict for one tick: b
// bytes delivered in elapsed since Watch start.
//
// Size/budget-aware form (goal and deadline both known): the
// attempt is condemned only when the remaining bytes, moving at
// the attempt's recent rate, would land past the deadline —
// equivalently, the effective rate floor is dynamic,
// remainingBytes/remainingBudget, recomputed on every tick as
// bytes land and the budget burns down. A leg running at or above
// that floor always survives, so whenever ONE route could finish
// the transfer inside the budget on its own (the pre-WDG-02
// capability), the first such leg simply runs to completion
// instead of dying at the grace; a leg below the floor even at
// the grace (the L2-01 token bucket) is condemned as early as
// under the fixed rule.
//
// Fallback form (goal or deadline unknown): the original fixed
// floor on the attempt's average rate since its start.
func (w *Watcher) trickleCondemned(now time.Time, elapsed time.Duration, b int64, samples []rateSample) bool {
	if goal := w.goal.Load(); goal > 0 {
		if dl := w.deadlineNano.Load(); dl > 0 {
			remaining := goal - b
			if remaining <= 0 {
				return false
			}
			rate, ok := recentRate(samples, now, b, time.Unix(0, w.firstByte.Load()))
			if !ok {
				return false // not enough body-phase observation yet
			}
			projected := now.Add(time.Duration(float64(remaining) / rate * float64(time.Second)))
			return projected.After(time.Unix(0, dl))
		}
	}
	return b*int64(time.Second) < TrickleMinRate*int64(elapsed)
}

// recentRate estimates the attempt's current body throughput:
// the bytes delivered inside the last TrickleRateWindow over that
// span, so the estimate tracks the line's present speed (a burst
// ages out of the window; a line that sped up after slow headers
// is credited with its real rate). When the window holds no
// measurable span or no progress — ticks too sparse, or the line
// stopped inside the window while the stall rule has yet to
// fire — it falls back to the average since the first byte. ok is
// false while the body phase is too young to estimate at all.
func recentRate(samples []rateSample, now time.Time, b int64, firstByte time.Time) (float64, bool) {
	bodyElapsed := now.Sub(firstByte)
	if bodyElapsed < 2*time.Second {
		return 0, false
	}
	cutoff := now.Add(-TrickleRateWindow)
	if firstByte.After(cutoff) {
		cutoff = firstByte
	}
	for _, s := range samples {
		if s.at.Before(cutoff) {
			continue
		}
		if span := now.Sub(s.at); span >= 2*time.Second {
			if d := b - s.bytes; d > 0 {
				return float64(d) / span.Seconds(), true
			}
		}
		break // earliest in-window sample found but unusable: average below
	}
	return float64(b) / bodyElapsed.Seconds(), true
}

// Note records byte progress. Zero or negative counts are ignored.
func (w *Watcher) Note(n int) {
	if n > 0 {
		now := time.Now().UnixNano()
		w.last.Store(now)
		if w.firstByte.Load() == 0 {
			w.firstByte.CompareAndSwap(0, now)
		}
		w.bytes.Add(int64(n))
	}
}

// Stalled reports whether the no-progress watchdog fired for this
// attempt.
func (w *Watcher) Stalled() bool {
	return w.stalled.Load()
}

// TooSlow reports whether the trickle judgment fired for this
// attempt (progress existed but averaged below TrickleMinRate past
// TrickleGrace).
func (w *Watcher) TooSlow() bool {
	return w.slow.Load()
}

// Writer wraps wr so every successful write counts as progress.
func (w *Watcher) Writer(wr io.Writer) io.Writer {
	return &watchWriter{w: w, wr: wr}
}

type watchWriter struct {
	w  *Watcher
	wr io.Writer
}

func (ww *watchWriter) Write(p []byte) (int, error) {
	n, err := ww.wr.Write(p)
	ww.w.Note(n)
	return n, err
}

// Reader wraps r so every successful read counts as progress.
func (w *Watcher) Reader(r io.Reader) io.Reader {
	return &watchReader{w: w, r: r}
}

type watchReader struct {
	w *Watcher
	r io.Reader
}

func (wr *watchReader) Read(p []byte) (int, error) {
	n, err := wr.r.Read(p)
	wr.w.Note(n)
	return n, err
}
