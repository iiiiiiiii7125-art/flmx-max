// Package reap holds the agent-wide zombie-session reaper
// settings for forwarded sessions: the edge consumption window
// and the mid-hop backstop window (plus its trickle ceiling).
//
// The values are node-level panel settings: the panel injects
// them into every generated service's metadata ("edgeIdleTimeout",
// "backstopIdleTimeout" in seconds, "backstopTrickleBps" in
// bytes/sec) and the config funnels publish the scanned values
// here at load/update time (see config and config/loader, next to
// the internal/idle publish). Zero — the zero value, and
// therefore the state of every agent whose panel never configured
// it — disables the corresponding reaper entirely: with both
// windows zero the data plane is bit-for-bit the pre-reaper
// behaviour, exactly like internal/idle.
//
// The two windows are enforced by the sampler in
// internal/net (reap.go), which snapshots them once per session
// at registration time — a republish therefore affects new
// sessions only, the same grandfathering the idle window has.
package reap

import (
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// DefaultTrickleBps is the backstop's server-direction rate
// ceiling (bytes/sec) applied when a backstop window is
// configured but no "backstopTrickleBps" value was published.
// 256 B/s is ~100x above game-server push drips (a few B/s to
// tens of B/s) and ~60x below the slowest real audio streams.
const DefaultTrickleBps int64 = 256

// Published settings; <= 0 means "not configured". Read once per
// session setup by the enforcing face, never per chunk, so the
// data-plane hot path carries no new cost.
var (
	edgeWindowNs     atomic.Int64
	backstopWindowNs atomic.Int64
	trickleBpsVal    atomic.Int64
)

// Set publishes the windows and trickle ceiling scanned from a
// freshly loaded config. Non-positive values disable the
// corresponding knob; a non-positive trickle means "no explicit
// ceiling published" (see EffectiveTrickleBps).
func Set(edgeWindow, backstopWindow time.Duration, trickleBps int64) {
	if edgeWindow < 0 {
		edgeWindow = 0
	}
	if backstopWindow < 0 {
		backstopWindow = 0
	}
	if trickleBps < 0 {
		trickleBps = 0
	}
	edgeWindowNs.Store(int64(edgeWindow))
	backstopWindowNs.Store(int64(backstopWindow))
	trickleBpsVal.Store(trickleBps)
}

// EdgeWindow returns the configured edge consumption window;
// 0 means the edge reaper is disabled.
func EdgeWindow() time.Duration {
	return time.Duration(edgeWindowNs.Load())
}

// BackstopWindow returns the configured mid-hop backstop window;
// 0 means the backstop reaper is disabled.
func BackstopWindow() time.Duration {
	return time.Duration(backstopWindowNs.Load())
}

// TrickleBps returns the published backstop trickle ceiling in
// bytes/sec; 0 means none was published.
func TrickleBps() int64 {
	return trickleBpsVal.Load()
}

// EffectiveTrickleBps returns the trickle ceiling a backstop
// session should enforce: the published value, or
// DefaultTrickleBps when none was published.
func EffectiveTrickleBps() int64 {
	if v := TrickleBps(); v > 0 {
		return v
	}
	return DefaultTrickleBps
}

// Publish scans service metadata maps for the panel's reap
// entries and publishes them node-wide. Aggregation mirrors
// idle.Publish with one deliberate asymmetry: the two windows
// take the MAXIMUM (the panel writes the same node-level value
// into every service it generates, so a partially annotated
// config may only widen a window, never narrow it below the
// node's setting), while the trickle ceiling takes the SMALLEST
// present value — under partial annotation the least aggressive
// reaping wins, because a too-large ceiling only ever spares a
// fast drip, but a too-small one could reap a real stream.
// No metadata carrying a key leaves that knob disabled.
//
// Key lookups are case-insensitive for the same reason as in
// internal/idle: a file-loaded gost.json reaches the agent
// through viper, which lowercases map keys (F-212-A).
func Publish(metadatas []map[string]any) {
	var edgeWindow, backstopWindow time.Duration
	var trickle int64
	for _, md := range metadatas {
		if d := metadataDuration(md, "edgeIdleTimeout"); d > edgeWindow {
			edgeWindow = d
		}
		if d := metadataDuration(md, "backstopIdleTimeout"); d > backstopWindow {
			backstopWindow = d
		}
		if v := metadataInt(md, "backstopTrickleBps"); v > 0 && (trickle == 0 || v < trickle) {
			trickle = v
		}
	}
	Set(edgeWindow, backstopWindow, trickle)
}

func metadataDuration(md map[string]any, key string) time.Duration {
	if v, ok := metadataValue(md, key); ok {
		return timeoutValue(v)
	}
	return 0
}

func metadataInt(md map[string]any, key string) int64 {
	if v, ok := metadataValue(md, key); ok {
		return intValue(v)
	}
	return 0
}

func metadataValue(md map[string]any, key string) (any, bool) {
	if md == nil {
		return nil, false
	}
	if v, ok := md[key]; ok {
		return v, true
	}
	for k, v := range md {
		if strings.EqualFold(k, key) {
			return v, true
		}
	}
	return nil, false
}

// timeoutValue decodes a duration metadata value with the same
// semantics as internal/idle: numbers are seconds, strings are
// durations ("300s") or bare seconds ("300").
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

// intValue decodes an integer metadata value (bytes/sec):
// numbers are taken as-is, strings must parse as a bare integer.
func intValue(v any) int64 {
	switch vv := v.(type) {
	case int:
		return int64(vv)
	case int64:
		return vv
	case float64:
		return int64(vv)
	case string:
		if n, err := strconv.ParseInt(strings.TrimSpace(vv), 10, 64); err == nil {
			return n
		}
	}
	return 0
}
