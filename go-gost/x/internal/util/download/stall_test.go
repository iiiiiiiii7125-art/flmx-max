package download

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

// R3-WDG-01: with zero progress the watchdog must cancel the
// derived context at (roughly) the window, not at the caller's
// outer deadline.
func TestWatcherCancelsOnStall(t *testing.T) {
	w := NewWatcher(200 * time.Millisecond)
	ctx, stop := w.Watch(context.Background())
	defer stop()
	start := time.Now()
	<-ctx.Done()
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("stall cancel took %v, want ~200ms window", elapsed)
	}
	if !w.Stalled() {
		t.Fatal("Stalled() must be true after the watchdog fired")
	}
}

// Steady progress inside the window must keep the attempt alive
// well past one window.
func TestWatcherStaysAliveWithProgress(t *testing.T) {
	w := NewWatcher(200 * time.Millisecond)
	ctx, stop := w.Watch(context.Background())
	defer stop()
	deadline := time.Now().Add(700 * time.Millisecond)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			t.Fatal("watchdog fired despite steady progress")
		default:
		}
		w.Note(1)
		time.Sleep(40 * time.Millisecond)
	}
	if w.Stalled() {
		t.Fatal("Stalled() must stay false while progress continues")
	}
}

// The Writer/Reader adapters must count bytes as progress.
func TestWatcherAdaptersNoteProgress(t *testing.T) {
	w := NewWatcher(150 * time.Millisecond)
	ctx, stop := w.Watch(context.Background())
	defer stop()
	var buf bytes.Buffer
	if _, err := io.Copy(w.Writer(&buf), strings.NewReader("hello")); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "hello" {
		t.Fatalf("writer passthrough broken: %q", buf.String())
	}
	r := w.Reader(strings.NewReader("world"))
	out, err := io.ReadAll(r)
	if err != nil || string(out) != "world" {
		t.Fatalf("reader passthrough broken: %q, %v", out, err)
	}
	// Progress just happened, so the context must still be live.
	select {
	case <-ctx.Done():
		t.Fatal("watchdog fired right after progress")
	default:
	}
}

// WDG-02 (round-5 L2-01): steady progress BELOW the rate floor
// never trips the zero-byte stall rule — bytes keep moving — but
// must trip the trickle judgment once the grace has elapsed.
func TestWatcherCancelsOnTrickle(t *testing.T) {
	oldGrace, oldRate := TrickleGrace, TrickleMinRate
	TrickleGrace, TrickleMinRate = 300*time.Millisecond, 1<<20 // 1 MiB/s floor
	defer func() { TrickleGrace, TrickleMinRate = oldGrace, oldRate }()

	w := NewWatcher(30 * time.Second) // stall window far away: only the trickle rule can fire
	ctx, stop := w.Watch(context.Background())
	defer stop()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			w.Note(1024) // ~20KB/s: above zero, far below the floor
			time.Sleep(50 * time.Millisecond)
		}
	}()
	start := time.Now()
	<-ctx.Done()
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("trickle cancel took %v, want ~grace (300ms)", elapsed)
	}
	if !w.TooSlow() {
		t.Fatal("TooSlow() must be true after the trickle judgment fired")
	}
	if w.Stalled() {
		t.Fatal("Stalled() must stay false: bytes kept moving the whole time")
	}
}

// Progress comfortably above the rate floor must survive well
// past the grace — the trickle judgment only condemns lines that
// could never finish inside the caller's budget.
func TestWatcherFastEnoughSurvivesTrickleJudgment(t *testing.T) {
	oldGrace, oldRate := TrickleGrace, TrickleMinRate
	TrickleGrace, TrickleMinRate = 200*time.Millisecond, 1024 // 1 KiB/s floor
	defer func() { TrickleGrace, TrickleMinRate = oldGrace, oldRate }()

	w := NewWatcher(30 * time.Second)
	ctx, stop := w.Watch(context.Background())
	defer stop()
	deadline := time.Now().Add(900 * time.Millisecond)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			t.Fatal("trickle judgment fired despite above-floor progress")
		default:
		}
		w.Note(4096) // ~80KB/s average, well above the 1KiB/s floor
		time.Sleep(50 * time.Millisecond)
	}
	if w.TooSlow() {
		t.Fatal("TooSlow() must stay false while progress clears the floor")
	}
}

// A disabled (non-positive) window passes the context through and
// never reports a stall.
func TestWatcherDisabled(t *testing.T) {
	w := NewWatcher(0)
	ctx, stop := w.Watch(context.Background())
	defer stop()
	select {
	case <-ctx.Done():
		t.Fatal("disabled watchdog must not cancel")
	case <-time.After(100 * time.Millisecond):
	}
	if w.Stalled() {
		t.Fatal("disabled watchdog must never report stalled")
	}
}
