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
//     also averages the attempt's throughput from its start: once
//     TrickleGrace has elapsed, an attempt whose average rate is
//     below TrickleMinRate fails with ErrTooSlow. That floor is
//     far under what a viable line needs (a body of even 15MB
//     needs ~64KB/s to land inside the 240s budget at all), so a
//     line it condemns could never have finished in budget — and
//     thanks to FileTransfer resume, the bytes it did deliver are
//     kept for the next route.
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
// because bytes kept moving, but at an average rate below
// TrickleMinRate — progress too slow to ever finish inside the
// caller's whole-operation budget (round-5 L2-01).
var ErrTooSlow = errors.New("下载过慢：平均速率低于可用下限，已判失败")

// Trickle judgment parameters (WDG-02), registered alongside
// StallWindow in doc/audit/timeout-budgets.tsv rows A001/A002.
// TrickleGrace is the observation period before the rate judgment
// applies, so a line is never condemned on its first seconds
// (connect + TLS + slow start are measured into the average, but
// a viable line recovers its average well inside the grace).
// TrickleMinRate is the floor on the attempt's average throughput
// since its start. Vars so tests can shrink them.
var (
	TrickleGrace   = 15 * time.Second
	TrickleMinRate = int64(64 << 10) // 64 KiB/s
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
				// the attempt's average rate since its start must
				// clear the floor. Zero bytes is the stall rule's
				// case, not this one's (a peer still inside its
				// header wait is not "trickling").
				if elapsed := now.Sub(start); elapsed >= TrickleGrace {
					if b := w.bytes.Load(); b > 0 && b*int64(time.Second) < TrickleMinRate*int64(elapsed) {
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

// Note records byte progress. Zero or negative counts are ignored.
func (w *Watcher) Note(n int) {
	if n > 0 {
		w.last.Store(time.Now().UnixNano())
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
