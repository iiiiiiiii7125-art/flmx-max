// Package keepalive holds the agent-wide kernel TCP keepalive
// policy for agent-owned TCP connections (added by the 2.1.2
// hotfix expansion, alongside the no-progress teardown rework in
// internal/idle).
//
// The criterion is deliberately not a timer on the session: the
// kernel probes a connection only after it has been completely
// idle for Idle, and the connection is reaped only when the peer
// fails to answer Count consecutive probes spaced Interval apart.
// A peer that is alive — even if its application never sends a
// single byte — answers the probes at kernel level and the
// connection is never touched. A connection that carries any data
// at all is never even probed: any byte in either direction resets
// the kernel's idle clock. What gets reaped is therefore exactly
// the half-open residue whose adjacent peer has silently died
// (host down, path blackholed) — the fd/RSS accumulation behind
// the ECS1686 OOM kills — and nothing else.
//
// Kernel keepalive proves only the ADJACENT peer's kernel. In a
// chain, a session whose far end died beyond a live next hop is
// invisible to it; that residue is what the opt-in no-progress
// window (internal/idle, default off) exists for. The two
// mechanisms are complements, not substitutes.
//
// Defaults: Idle 180s, Interval 60s, Count 6 — a dead verdict
// needs roughly nine minutes of total silence (three idle, then
// six unanswered probes over six more minutes), far inside the
// hours-scale accumulation this defends against, and far too
// patient for transient loss to fake: a live peer must miss six
// consecutive probes spread over six minutes to be misjudged.
// Production tuning must keep Interval at or above 30s (probe
// traffic / carrier optics floor); the default sits at 60s, so a
// fully silent connection emits one bare probe segment per minute
// and a busy connection emits none.
//
// Parameters live in this package only — agent-side constants with
// an environment override for diagnostics and short-parameter
// verification builds (FLMX_KEEPALIVE=off disables;
// FLMX_KEEPALIVE_IDLE / FLMX_KEEPALIVE_INTERVAL take Go durations;
// FLMX_KEEPALIVE_COUNT takes an int). There is deliberately no
// panel field this wave.
package keepalive

import (
	"net"
	"os"
	"strconv"
	"sync/atomic"
	"time"
)

// policy is the immutable parameter set swapped atomically.
type policy struct {
	enabled  bool
	idle     time.Duration
	interval time.Duration
	count    int
}

// Default parameters. Death verdict ≈ idle + interval*count
// ≈ 180s + 60s*6 = 540s of total silence.
const (
	DefaultIdle     = 180 * time.Second
	DefaultInterval = 60 * time.Second
	DefaultCount    = 6
)

var current atomic.Pointer[policy]

func init() {
	p := &policy{
		enabled:  true,
		idle:     DefaultIdle,
		interval: DefaultInterval,
		count:    DefaultCount,
	}
	if v := os.Getenv("FLMX_KEEPALIVE"); v == "off" || v == "0" || v == "false" {
		p.enabled = false
	}
	if v := os.Getenv("FLMX_KEEPALIVE_IDLE"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			p.idle = d
		}
	}
	if v := os.Getenv("FLMX_KEEPALIVE_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			p.interval = d
		}
	}
	if v := os.Getenv("FLMX_KEEPALIVE_COUNT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			p.count = n
		}
	}
	current.Store(p)
}

// Set replaces the policy. It is the injection seam for tests and
// short-parameter verification builds; unlike production tuning it
// applies values verbatim (no 30s interval floor), because its
// purpose is to make probe behaviour observable in minutes.
// Non-positive fields fall back to the defaults; call SetEnabled
// to turn probing off.
func Set(idle, interval time.Duration, count int) {
	p := &policy{
		enabled:  true,
		idle:     DefaultIdle,
		interval: DefaultInterval,
		count:    DefaultCount,
	}
	if idle > 0 {
		p.idle = idle
	}
	if interval > 0 {
		p.interval = interval
	}
	if count > 0 {
		p.count = count
	}
	current.Store(p)
}

// SetEnabled turns probing on or off. Turning it off restores the
// pre-hotfix behaviour of relying on the stdlib zero-value default
// is NOT what happens: off here means SO_KEEPALIVE disabled, via
// the negative KeepAlive duration the stdlib honours.
func SetEnabled(enabled bool) {
	for {
		old := current.Load()
		p := *old
		p.enabled = enabled
		if current.CompareAndSwap(old, &p) {
			return
		}
	}
}

// Config returns the net.KeepAliveConfig for the current policy.
// When disabled it returns the zero config; callers must go
// through ApplyDialer / ApplyListenConfig, which encode the
// disabled state as a negative KeepAlive duration (a zero
// KeepAliveConfig alone would silently fall back to the stdlib
// default probing instead of turning probing off).
func Config() net.KeepAliveConfig {
	p := current.Load()
	if !p.enabled {
		return net.KeepAliveConfig{}
	}
	return net.KeepAliveConfig{
		Enable:   true,
		Idle:     p.idle,
		Interval: p.interval,
		Count:    p.count,
	}
}

// Enabled reports whether probing is on.
func Enabled() bool {
	return current.Load().enabled
}

// Verdict returns the approximate total silence after which an
// unresponsive peer is declared dead (idle + interval*count).
func Verdict() time.Duration {
	p := current.Load()
	return p.idle + p.interval*time.Duration(p.count)
}

// ApplyDialer stamps the policy onto a net.Dialer under
// construction.
func ApplyDialer(d *net.Dialer) {
	if Enabled() {
		d.KeepAliveConfig = Config()
		return
	}
	d.KeepAliveConfig = net.KeepAliveConfig{}
	d.KeepAlive = -1
}

// ApplyListenConfig stamps the policy onto a net.ListenConfig
// under construction.
func ApplyListenConfig(lc *net.ListenConfig) {
	if Enabled() {
		lc.KeepAliveConfig = Config()
		return
	}
	lc.KeepAliveConfig = net.KeepAliveConfig{}
	lc.KeepAlive = -1
}
