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
