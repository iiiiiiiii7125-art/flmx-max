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
