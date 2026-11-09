package config

import (
	"testing"
	"time"

	"github.com/go-gost/x/internal/idle"
)

// F-212-A (agent layer, third break): the idle teardown window was
// published only by the config loader (full Load/Reload), but the
// panel's service commands reach a running agent through the
// incremental mutation funnel — x/socket create/update/delete via
// config.OnUpdate — which never re-published. A threshold pushed
// to a live agent landed in gost.json and in the registry while
// the forwarding plane kept the boot-time window (0), so the knob
// did nothing until an agent restart (LIVE-3 PF-3a, fix212a first
// rerun). The window must track the config in force at BOTH commit
// funnels of this package: Set and OnUpdate.
func TestIdleWindowTracksConfigMutations(t *testing.T) {
	unlock := LockMutation()
	defer unlock()
	original := Global()
	oldWindow := idle.Get()
	defer func() {
		Set(original)
		idle.Set(oldWindow)
	}()

	Set(&Config{Services: []*ServiceConfig{{Name: "a"}}})
	if got := idle.Get(); got != 0 {
		t.Fatalf("after Set without metadata: window = %v, want 0", got)
	}

	// The panel-push shape: an incremental mutation adds the
	// annotated services to the config in force.
	err := OnUpdate(func(c *Config) error {
		c.Services = append(c.Services,
			&ServiceConfig{Name: "b", Metadata: map[string]any{"idleTimeout": float64(60)}})
		return nil
	})
	if err != nil {
		t.Fatalf("OnUpdate: %v", err)
	}
	if got := idle.Get(); got != 60*time.Second {
		t.Fatalf("after OnUpdate adding idleTimeout=60: window = %v, want 60s (mutation funnel never published)", got)
	}

	// Turning the knob back off through the same funnel must close
	// the window again, not leave the stale value armed.
	err = OnUpdate(func(c *Config) error {
		c.Services = c.Services[:1]
		return nil
	})
	if err != nil {
		t.Fatalf("OnUpdate: %v", err)
	}
	if got := idle.Get(); got != 0 {
		t.Fatalf("after OnUpdate removing the annotation: window = %v, want 0", got)
	}

	// Whole-config replacement (rollback shape) re-publishes too.
	Set(&Config{Services: []*ServiceConfig{
		{Name: "c", Metadata: map[string]any{"idletimeout": float64(30)}},
	}})
	if got := idle.Get(); got != 30*time.Second {
		t.Fatalf("after Set with lowercased key: window = %v, want 30s", got)
	}
}
