// Package idle holds the agent-wide no-progress teardown window
// for forwarded sessions (the FIX-1 fuse, reworked by the 2.1.2
// hotfix).
//
// The window is a node-level panel setting: the panel injects it
// into every generated service's metadata ("idleTimeout", seconds)
// and the config loader publishes the scanned value here at load
// time (see config/loader). Zero — the zero value, and therefore
// the state of every agent whose panel never configured it —
// disables the teardown entirely: a session that carries traffic,
// however slowly, is never torn down for silence, and a fully
// silent session is left to the endpoints' own lifecycle, exactly
// as before FIX-1. A positive window restores the FIX-1 criterion
// unchanged: only a session with no byte moved in EITHER direction
// for the full window is torn down.
//
// The panel's per-inbound Xray connIdle is a separate kernel-side
// policy with its own 0/-1 semantics and is deliberately not
// shared with this forwarding-plane window.
package idle

import (
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// windowNs is the configured window in nanoseconds; <= 0 means the
// teardown is disabled. Read once per session/association setup by
// the enforcing faces, never per chunk or per datagram, so the
// data-plane hot path carries no new cost.
var windowNs atomic.Int64

// Set publishes the window scanned from a freshly loaded config.
// Non-positive values disable the teardown.
func Set(d time.Duration) {
	if d < 0 {
		d = 0
	}
	windowNs.Store(int64(d))
}

// Get returns the configured window; 0 means disabled.
func Get() time.Duration {
	return time.Duration(windowNs.Load())
}

// Resolve returns the effective window for one enforcing face: a
// non-zero per-face override (tests, a stricter local policy) wins
// as-is — a negative override pins the teardown off — and a zero
// override defers to the node-wide window.
func Resolve(override time.Duration) time.Duration {
	if override != 0 {
		return override
	}
	return Get()
}

// Publish scans service metadata maps for the panel's "idleTimeout"
// entry (seconds) and publishes the maximum as the node-wide
// window: the panel writes the same node-level value into every
// service it generates, so a partially annotated config can only
// ever widen the window, never narrow it below the node's setting.
// No metadata carrying the key leaves the teardown disabled.
//
// The key lookup is case-insensitive on purpose: a gost.json file
// reaches the agent through viper (config.ReadFile), whose key
// handling lowercases map keys, so the panel-written "idleTimeout"
// arrives as "idletimeout"; only an inline/programmatic config
// keeps the original case. Matching the camelCase spelling alone
// silently disabled the knob for every file-loaded agent (F-212-A).
func Publish(metadatas []map[string]any) {
	var window time.Duration
	for _, md := range metadatas {
		if d := metadataWindow(md); d > window {
			window = d
		}
	}
	Set(window)
}

func metadataWindow(md map[string]any) time.Duration {
	if v, ok := md["idleTimeout"]; ok {
		return timeoutValue(v)
	}
	for k, v := range md {
		if strings.EqualFold(k, "idleTimeout") {
			return timeoutValue(v)
		}
	}
	return 0
}

// timeoutValue decodes one "idleTimeout" metadata value with the
// metadata/util GetDuration semantics: numbers are seconds,
// strings are durations ("300s") or bare seconds ("300").
func timeoutValue(v any) time.Duration {
	switch vv := v.(type) {
	case int:
		return time.Duration(vv) * time.Second
	case int64:
		return time.Duration(vv) * time.Second
	case float64:
		return time.Duration(vv) * time.Second
	case string:
		if d, err := time.ParseDuration(vv); err == nil {
			return d
		}
		if n, err := strconv.Atoi(vv); err == nil {
			return time.Duration(n) * time.Second
		}
	}
	return 0
}
