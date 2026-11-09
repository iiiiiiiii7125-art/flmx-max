package loader

import (
	"testing"
	"time"

	"github.com/go-gost/x/config"
	"github.com/go-gost/x/internal/idle"
)

// The panel writes the node's idleTimeout (seconds) into every
// generated service's metadata; applyIdleWindow must publish the
// maximum across services (a partially annotated config may only
// widen the window) and must leave the teardown disabled when no
// service carries the key.
func TestApplyIdleWindow(t *testing.T) {
	old := idle.Get()
	t.Cleanup(func() { idle.Set(old) })

	applyIdleWindow(&config.Config{Services: []*config.ServiceConfig{
		{Name: "a", Metadata: map[string]any{"idleTimeout": float64(120)}},
		{Name: "b"},
		{Name: "c", Metadata: map[string]any{"idleTimeout": "300s"}},
		nil,
	}})
	if got := idle.Get(); got != 300*time.Second {
		t.Fatalf("idle window = %v, want 300s (max across services)", got)
	}

	applyIdleWindow(&config.Config{Services: []*config.ServiceConfig{
		{Name: "a", Metadata: map[string]any{"idleTimeout": "45"}},
	}})
	if got := idle.Get(); got != 45*time.Second {
		t.Fatalf("idle window = %v, want 45s (bare-seconds string)", got)
	}

	applyIdleWindow(&config.Config{Services: []*config.ServiceConfig{{Name: "a"}}})
	if got := idle.Get(); got != 0 {
		t.Fatalf("idle window = %v, want 0 (disabled) when unset", got)
	}

	applyIdleWindow(nil)
	if got := idle.Get(); got != 0 {
		t.Fatalf("idle window = %v, want 0 (disabled) for nil config", got)
	}
}
