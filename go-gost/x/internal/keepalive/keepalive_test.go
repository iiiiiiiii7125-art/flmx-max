package keepalive

import (
	"net"
	"testing"
	"time"
)

// restoreDefaults returns the package to its production policy.
func restoreDefaults() {
	Set(0, 0, 0)
	SetEnabled(true)
}

func TestDefaults(t *testing.T) {
	restoreDefaults()

	if !Enabled() {
		t.Fatal("keepalive probing must be enabled by default")
	}
	cfg := Config()
	if !cfg.Enable {
		t.Fatal("default Config must have Enable=true")
	}
	if cfg.Idle != DefaultIdle || cfg.Interval != DefaultInterval || cfg.Count != DefaultCount {
		t.Fatalf("default Config = %+v, want idle=%v interval=%v count=%d",
			cfg, DefaultIdle, DefaultInterval, DefaultCount)
	}
	// User-set parameter policy (2026-11-09): interval is 60s-class
	// with a 30s floor, idle is minutes-scale, and the total death
	// verdict stays inside roughly 5-10 minutes.
	if cfg.Interval < 30*time.Second {
		t.Fatalf("default interval %v below the 30s production floor", cfg.Interval)
	}
	if cfg.Idle < time.Minute {
		t.Fatalf("default idle %v is not minutes-scale", cfg.Idle)
	}
	if v := Verdict(); v < 5*time.Minute || v > 10*time.Minute {
		t.Fatalf("default death verdict %v outside the 5-10 minute band", v)
	}
}

func TestSetInjectionSeam(t *testing.T) {
	defer restoreDefaults()

	Set(20*time.Second, 5*time.Second, 3)
	cfg := Config()
	if cfg.Idle != 20*time.Second || cfg.Interval != 5*time.Second || cfg.Count != 3 {
		t.Fatalf("Config after Set(20s,5s,3) = %+v", cfg)
	}
	if v := Verdict(); v != 35*time.Second {
		t.Fatalf("Verdict after Set(20s,5s,3) = %v, want 35s", v)
	}

	// Non-positive fields fall back to defaults rather than
	// producing a nonsensical socket policy.
	Set(0, 0, 0)
	cfg = Config()
	if cfg.Idle != DefaultIdle || cfg.Interval != DefaultInterval || cfg.Count != DefaultCount {
		t.Fatalf("Config after Set(0,0,0) = %+v, want defaults", cfg)
	}
}

func TestDisabledEncoding(t *testing.T) {
	defer restoreDefaults()

	SetEnabled(false)
	if Enabled() {
		t.Fatal("Enabled() must be false after SetEnabled(false)")
	}
	if cfg := Config(); cfg.Enable {
		t.Fatalf("Config while disabled = %+v, want Enable=false", cfg)
	}
	// The disabled state must reach the socket as a negative
	// KeepAlive duration: a zero KeepAliveConfig alone would fall
	// back to the stdlib default probing (15s/15s/9) instead of
	// turning probing off.
	d := &net.Dialer{}
	ApplyDialer(d)
	if d.KeepAlive != -1 {
		t.Fatalf("disabled dialer KeepAlive = %v, want -1", d.KeepAlive)
	}
	lc := &net.ListenConfig{}
	ApplyListenConfig(lc)
	if lc.KeepAlive != -1 {
		t.Fatalf("disabled listener KeepAlive = %v, want -1", lc.KeepAlive)
	}

	SetEnabled(true)
	d = &net.Dialer{}
	ApplyDialer(d)
	if d.KeepAliveConfig != Config() {
		t.Fatalf("enabled dialer KeepAliveConfig = %+v, want %+v", d.KeepAliveConfig, Config())
	}
}
