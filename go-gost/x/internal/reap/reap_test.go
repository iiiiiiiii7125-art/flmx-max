package reap

import (
	"testing"
	"time"
)

func saveState(t *testing.T) {
	t.Helper()
	edge, back, trickle := EdgeWindow(), BackstopWindow(), TrickleBps()
	t.Cleanup(func() { Set(edge, back, trickle) })
}

func TestSetGet(t *testing.T) {
	saveState(t)

	Set(0, 0, 0)
	if got := EdgeWindow(); got != 0 {
		t.Fatalf("EdgeWindow after Set(0,0,0) = %v, want 0 (disabled)", got)
	}
	if got := BackstopWindow(); got != 0 {
		t.Fatalf("BackstopWindow after Set(0,0,0) = %v, want 0 (disabled)", got)
	}
	if got := TrickleBps(); got != 0 {
		t.Fatalf("TrickleBps after Set(0,0,0) = %v, want 0 (unpublished)", got)
	}
	// An unpublished trickle resolves to the built-in default.
	if got := EffectiveTrickleBps(); got != DefaultTrickleBps {
		t.Fatalf("EffectiveTrickleBps with none published = %v, want default %v", got, DefaultTrickleBps)
	}

	Set(900*time.Second, 3600*time.Second, 512)
	if got := EdgeWindow(); got != 900*time.Second {
		t.Fatalf("EdgeWindow = %v, want 900s", got)
	}
	if got := BackstopWindow(); got != 3600*time.Second {
		t.Fatalf("BackstopWindow = %v, want 3600s", got)
	}
	if got := EffectiveTrickleBps(); got != 512 {
		t.Fatalf("EffectiveTrickleBps = %v, want published 512", got)
	}

	// Negative values clamp to disabled/unpublished rather than
	// arming a window in the past.
	Set(-time.Second, -time.Second, -5)
	if EdgeWindow() != 0 || BackstopWindow() != 0 || TrickleBps() != 0 {
		t.Fatalf("negative Set armed something: edge=%v backstop=%v trickle=%v, want all 0",
			EdgeWindow(), BackstopWindow(), TrickleBps())
	}
}

func TestPublishAggregation(t *testing.T) {
	saveState(t)

	// Windows take the max across services; trickle takes the
	// smallest present value; missing keys leave knobs disabled.
	Publish([]map[string]any{
		{"edgeIdleTimeout": float64(600), "backstopIdleTimeout": float64(1800), "backstopTrickleBps": float64(1024)},
		{"edgeIdleTimeout": "900s", "backstopIdleTimeout": "3600s", "backstopTrickleBps": 256},
		{},
		nil,
	})
	if got := EdgeWindow(); got != 900*time.Second {
		t.Fatalf("EdgeWindow = %v, want 900s (max)", got)
	}
	if got := BackstopWindow(); got != 3600*time.Second {
		t.Fatalf("BackstopWindow = %v, want 3600s (max)", got)
	}
	if got := TrickleBps(); got != 256 {
		t.Fatalf("TrickleBps = %v, want 256 (min present)", got)
	}

	// Bare-seconds strings decode like the idle knob's.
	Publish([]map[string]any{
		{"edgeIdleTimeout": "45"},
	})
	if got := EdgeWindow(); got != 45*time.Second {
		t.Fatalf("EdgeWindow = %v, want 45s (bare-seconds string)", got)
	}
	if got := BackstopWindow(); got != 0 {
		t.Fatalf("BackstopWindow = %v, want 0 when no service carries the key", got)
	}
	if got := TrickleBps(); got != 0 {
		t.Fatalf("TrickleBps = %v, want 0 when no service carries the key", got)
	}

	// No metadata at all disables everything.
	Publish(nil)
	if EdgeWindow() != 0 || BackstopWindow() != 0 || TrickleBps() != 0 {
		t.Fatalf("Publish(nil) armed something: edge=%v backstop=%v trickle=%v",
			EdgeWindow(), BackstopWindow(), TrickleBps())
	}
}

func TestPublishCaseInsensitiveKeys(t *testing.T) {
	saveState(t)

	// A file-loaded config arrives with viper-lowercased keys
	// (F-212-A); the scan must still find them.
	Publish([]map[string]any{
		{"edgeidletimeout": float64(900), "BACKSTOPIDLETIMEOUT": float64(3600), "BackstopTrickleBps": float64(128)},
	})
	if got := EdgeWindow(); got != 900*time.Second {
		t.Fatalf("EdgeWindow = %v, want 900s via lowercased key", got)
	}
	if got := BackstopWindow(); got != 3600*time.Second {
		t.Fatalf("BackstopWindow = %v, want 3600s via uppercased key", got)
	}
	if got := TrickleBps(); got != 128 {
		t.Fatalf("TrickleBps = %v, want 128 via mixed-case key", got)
	}
}

func TestPublishIgnoresGarbage(t *testing.T) {
	saveState(t)

	Publish([]map[string]any{
		{"edgeIdleTimeout": "soon", "backstopIdleTimeout": true, "backstopTrickleBps": "fast"},
	})
	if EdgeWindow() != 0 || BackstopWindow() != 0 || TrickleBps() != 0 {
		t.Fatalf("garbage metadata armed something: edge=%v backstop=%v trickle=%v",
			EdgeWindow(), BackstopWindow(), TrickleBps())
	}
}
