package loader

import (
	"testing"
	"time"

	"github.com/go-gost/x/config"
	"github.com/go-gost/x/internal/reap"
)

// The panel writes the node's reap settings into every generated
// service's metadata alongside idleTimeout; applyIdleWindow must
// publish the windows (max across services), the trickle ceiling
// (min present) and leave every knob disabled when no service
// carries the keys.
func TestApplyReapWindow(t *testing.T) {
	oldEdge, oldBack, oldTrickle := reap.EdgeWindow(), reap.BackstopWindow(), reap.TrickleBps()
	t.Cleanup(func() { reap.Set(oldEdge, oldBack, oldTrickle) })

	applyIdleWindow(&config.Config{Services: []*config.ServiceConfig{
		{Name: "a", Metadata: map[string]any{
			"edgeIdleTimeout":     float64(600),
			"backstopIdleTimeout": float64(1800),
			"backstopTrickleBps":  float64(1024),
		}},
		{Name: "b"},
		{Name: "c", Metadata: map[string]any{
			"edgeIdleTimeout":     "900s",
			"backstopIdleTimeout": "3600s",
			"backstopTrickleBps":  256,
		}},
		nil,
	}})
	if got := reap.EdgeWindow(); got != 900*time.Second {
		t.Fatalf("edge window = %v, want 900s (max across services)", got)
	}
	if got := reap.BackstopWindow(); got != 3600*time.Second {
		t.Fatalf("backstop window = %v, want 3600s (max across services)", got)
	}
	if got := reap.TrickleBps(); got != 256 {
		t.Fatalf("trickle = %v, want 256 (min present across services)", got)
	}

	applyIdleWindow(&config.Config{Services: []*config.ServiceConfig{{Name: "a"}}})
	if reap.EdgeWindow() != 0 || reap.BackstopWindow() != 0 || reap.TrickleBps() != 0 {
		t.Fatalf("unset config armed the reaper: edge=%v backstop=%v trickle=%v, want all 0",
			reap.EdgeWindow(), reap.BackstopWindow(), reap.TrickleBps())
	}

	applyIdleWindow(nil)
	if reap.EdgeWindow() != 0 || reap.BackstopWindow() != 0 || reap.TrickleBps() != 0 {
		t.Fatalf("nil config armed the reaper: edge=%v backstop=%v trickle=%v, want all 0",
			reap.EdgeWindow(), reap.BackstopWindow(), reap.TrickleBps())
	}
}
