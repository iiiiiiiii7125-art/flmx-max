package net

import (
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-gost/core/logger"
	"github.com/go-gost/core/metrics"
	xmetrics "github.com/go-gost/x/metrics"
)

// This file implements the zombie-session reaper sampler: one
// agent-wide goroutine that, every reapTickInterval, evaluates
// the sessions TransportScoped registered with it against two
// criteria (spec: Lane F3 §3–§4):
//
//   - edge (the service terminating the true end-client leg):
//     the originator has sent nothing for the full edge window
//     AND TCP_INFO proves the endpoint is not consuming — data
//     offered to it stands unacknowledged / unsent / behind a
//     shut receive window, with zero ACK progress across three
//     consecutive samples. Silence alone never reaps here; a
//     hung application behind a live kernel (which answers every
//     TCP keepalive) is the shape this catches, from the only
//     hop that can see it.
//   - backstop (mid-chain relay segments, whose peer is always
//     the previous hop's agent, so consumption is unprovable):
//     the originator direction has been silent for the full
//     backstop window while the server direction moved at least
//     one byte but stayed below the trickle ceiling — a drip
//     keeping a corpse warm. A both-directions-silent session
//     (zero server bytes) is deliberately NOT touched: that
//     shape belongs to the classic idle window, and the floor
//     keeps the backstop from double-owning it.
//
// Both roles are opt-in per node via internal/reap and default
// off; any doubt (probe unavailable, probe error, conflicting
// evidence) resolves to do-not-reap. A reap closes the endpoints
// ABORTIVELY (linger-0 where resolvable) so the next hop sees a
// hard failure and unwinds promptly, instead of a graceful FIN
// that would stall every downstream hop in a half-close drain.

type reapRole uint8

const (
	reapRoleNone reapRole = iota
	reapRoleEdge
	reapRoleBackstop
)

func parseReapRole(s string) reapRole {
	switch s {
	case "edge":
		return reapRoleEdge
	case "backstop":
		return reapRoleBackstop
	}
	return reapRoleNone
}

func (r reapRole) String() string {
	switch r {
	case reapRoleEdge:
		return "edge"
	case reapRoleBackstop:
		return "backstop"
	}
	return "none"
}

func reapReasonName(reason int32) string {
	switch reason {
	case reapReasonEdge:
		return "edge"
	case reapReasonBackstop:
		return "backstop"
	}
	return "none"
}

// reapTickInterval is the sampler cadence (fixed in phase 1).
const reapTickInterval = 30 * time.Second

// reapEdgeFailStreak is the number of consecutive failing
// samples the edge criterion requires on top of the full edge
// window: non-consumption must persist, not merely appear once.
const reapEdgeFailStreak = 3

// reapRingMaxPoints bounds a backstop entry's rate ring;
// beyond it the ring is thinned (see thinRing).
const reapRingMaxPoints = 256

type reapSessionInfo struct {
	role    reapRole
	edgeWin time.Duration
	backWin time.Duration
	trickle int64
	service string
}

type ratePoint struct {
	nano  int64
	bytes int64
}

type reapEntry struct {
	act      *sessionActivity
	rw1, rw2 io.ReadWriter
	role     reapRole
	edgeWin  time.Duration            // windows snapshotted at registration —
	backWin  time.Duration            // a republish affects new sessions only,
	trickle  int64                    // exactly like the idle window
	probe    func() (*tcpInfo, error) // nil: unresolvable / not applicable
	service  string
	client   string // rw1 remote address at registration
	target   string // rw2 remote address at registration
	born     time.Time

	// Sampler-owned state; only the sampler goroutine (and
	// tests driving it directly) touches these.
	prevAcked       uint64
	prevSent        uint64
	havePrev        bool
	failStreak      int
	lastInfo        *tcpInfo // last successful probe snapshot, for the reap log
	rateRing        []ratePoint
	lastServerBytes int64
	lastRateBps     int64
}

var (
	reapRegistry  sync.Map // *sessionActivity -> *reapEntry
	reapTracked   atomic.Int64
	reapStartOnce sync.Once
)

func registerReapSession(act *sessionActivity, rw1, rw2 io.ReadWriter, info reapSessionInfo) *reapEntry {
	e := &reapEntry{
		act:     act,
		rw1:     rw1,
		rw2:     rw2,
		role:    info.role,
		edgeWin: info.edgeWin,
		backWin: info.backWin,
		trickle: info.trickle,
		service: info.service,
		born:    time.Now(),
	}
	if c, ok := rw1.(net.Conn); ok {
		e.client = c.RemoteAddr().String()
	}
	if c, ok := rw2.(net.Conn); ok {
		e.target = c.RemoteAddr().String()
	}
	// The consumption probe exists only for the edge role, whose
	// rw1 terminates the true client leg on the direct accept
	// path. Backstop sessions never resolve one — in particular
	// mux-carried sessions (whose SyscallConn chain would land
	// on the shared session conn and describe other streams)
	// stay probe-free and therefore edge-unreapable, fail-safe
	// by construction.
	if info.role == reapRoleEdge {
		if c, ok := rw1.(net.Conn); ok {
			e.probe = tcpInfoProbe(c)
		}
	}
	if info.role == reapRoleBackstop {
		e.rateRing = append(e.rateRing, ratePoint{
			nano:  e.born.UnixNano(),
			bytes: act.bytesFrom[1].Load(),
		})
	}
	reapRegistry.Store(act, e)
	reapTracked.Add(1)
	xmetrics.GetGauge(xmetrics.MetricReapTrackedSessionsGauge, metrics.Labels{}).
		Set(float64(reapTracked.Load()))
	reapStartOnce.Do(func() { go reapSamplerLoop() })
	return e
}

func unregisterReapSession(act *sessionActivity) {
	if _, loaded := reapRegistry.LoadAndDelete(act); loaded {
		reapTracked.Add(-1)
		xmetrics.GetGauge(xmetrics.MetricReapTrackedSessionsGauge, metrics.Labels{}).
			Set(float64(reapTracked.Load()))
	}
}

func reapSamplerLoop() {
	ticker := time.NewTicker(reapTickInterval)
	defer ticker.Stop()
	for now := range ticker.C {
		reapSampleAll(now)
	}
}

func reapSampleAll(now time.Time) {
	var entries []*reapEntry
	reapRegistry.Range(func(_, v any) bool {
		entries = append(entries, v.(*reapEntry))
		return true
	})
	for _, e := range entries {
		e.sample(now)
	}
}

func (e *reapEntry) sample(now time.Time) {
	if e.act.tornDown.Load() {
		return
	}
	switch e.role {
	case reapRoleEdge:
		e.sampleEdge(now)
	case reapRoleBackstop:
		e.sampleBackstop(now)
	}
}

// sampleEdge evaluates the edge consumption criterion (F3 §4.1).
func (e *reapEntry) sampleEdge(now time.Time) {
	if e.probe == nil {
		return
	}
	info, err := e.probe()
	if err != nil || info == nil {
		// Evidence must be positive and continuous: any probe
		// failure resets the streak.
		e.failStreak = 0
		return
	}
	e.lastInfo = info

	clientSilentFor := now.Sub(time.Unix(0, e.act.lastFrom[0].Load()))
	fail := info.State == tcpStateEstablished &&
		clientSilentFor >= e.edgeWin &&
		(info.Unacked > 0 || info.NotsentBytes > 0 || info.SndWnd == 0) &&
		e.havePrev && info.BytesAcked == e.prevAcked
	e.prevAcked, e.prevSent, e.havePrev = info.BytesAcked, info.BytesSent, true

	if !fail {
		e.failStreak = 0
		return
	}
	e.failStreak++
	if e.failStreak >= reapEdgeFailStreak {
		e.reap(reapReasonEdge, now)
	}
}

// sampleBackstop evaluates the mid-hop backstop criterion
// (F3 §4.2): both inputs are full-window aggregates, so the
// window itself is the consecutive-evidence requirement and no
// streak is needed.
func (e *reapEntry) sampleBackstop(now time.Time) {
	cur := e.act.bytesFrom[1].Load()
	serverBytes, span := e.ringWindowBytes(now, cur)
	e.lastServerBytes = serverBytes
	if e.backWin > 0 {
		e.lastRateBps = serverBytes * int64(time.Second) / int64(e.backWin)
	}
	if span < e.backWin/2 {
		// The retained ring does not cover enough of the window
		// to judge (young session, or a thinned ring's degraded
		// span): do not reap on partial evidence.
		return
	}
	clientSilentFor := now.Sub(time.Unix(0, e.act.lastFrom[0].Load()))
	if clientSilentFor >= e.backWin && serverBytes >= 1 && e.lastRateBps < e.trickle {
		e.reap(reapReasonBackstop, now)
	}
}

// ringWindowBytes appends the current server-direction counter
// to the rate ring, prunes points older than the window start
// (keeping the newest at-or-before it as the baseline), and
// returns the bytes that arrived within the window plus the
// span the retained ring actually covers.
func (e *reapEntry) ringWindowBytes(now time.Time, cur int64) (serverBytes int64, span time.Duration) {
	e.rateRing = append(e.rateRing, ratePoint{nano: now.UnixNano(), bytes: cur})

	winStart := now.Add(-e.backWin).UnixNano()
	drop := 0
	for i := 0; i+1 < len(e.rateRing); i++ {
		if e.rateRing[i+1].nano <= winStart {
			drop = i + 1
		} else {
			break
		}
	}
	if drop > 0 {
		e.rateRing = append(e.rateRing[:0], e.rateRing[drop:]...)
	}
	if len(e.rateRing) > reapRingMaxPoints {
		e.thinRing()
	}

	base := e.rateRing[0]
	return cur - base.bytes, now.Sub(time.Unix(0, base.nano))
}

// thinRing halves the ring's resolution, always keeping the
// baseline (oldest) and newest points. Rate evaluation then runs
// over the oldest retained point and its actual span — the
// span guard in sampleBackstop keeps that conservative.
func (e *reapEntry) thinRing() {
	ring := e.rateRing
	out := make([]ratePoint, 0, len(ring)/2+2)
	out = append(out, ring[0])
	for i := 2; i < len(ring)-1; i += 2 {
		out = append(out, ring[i])
	}
	out = append(out, ring[len(ring)-1])
	e.rateRing = out
}

// reap claims the kill (reason CAS + not-already-torn-down),
// records it, and tears the session down abortively.
func (e *reapEntry) reap(reason int32, now time.Time) {
	if e.act.tornDown.Load() {
		return
	}
	if !e.act.reapReason.CompareAndSwap(reapReasonNone, reason) {
		return
	}
	e.logReap(now, reason)
	xmetrics.GetCounter(xmetrics.MetricReapTotalCounter, metrics.Labels{
		"reason":  reapReasonName(reason),
		"service": e.service,
	}).Inc()
	e.act.teardownAbortive(e.rw1, e.rw2)
}

// teardownAbortive is the sampler's teardown: like teardown, but
// each endpoint is closed abortively (linger-0 where resolvable)
// so the adjacent hop observes a hard failure (RST) and unwinds
// via the prompt hard-error path, instead of a graceful FIN
// that would divert it into an unbounded half-close drain
// (F2 fix spec S1).
func (a *sessionActivity) teardownAbortive(rw1, rw2 io.ReadWriter) {
	a.tornDown.Store(true)
	abortiveClose(rw1)
	abortiveClose(rw2)
}

func abortiveClose(rw io.ReadWriter) {
	setLingerZero(rw) // best effort; plain close when unresolvable
	closeEndpoint(rw)
}

// logReap emits the per-reap evidence line (F3 §6): the
// TCP_INFO snapshot is the audit trail — it shows the client
// had ACKed nothing while data stood unacked / window-shut for
// the whole streak, i.e. a corpse, not a user.
func (e *reapEntry) logReap(now time.Time, reason int32) {
	act := e.act
	fields := map[string]any{
		"service":         e.service,
		"client":          e.client,
		"target":          e.target,
		"reason":          reapReasonName(reason),
		"age":             now.Sub(e.born).Round(time.Second),
		"clientSilentFor": now.Sub(time.Unix(0, act.lastFrom[0].Load())).Round(time.Second),
		"bytesFromClient": act.bytesFrom[0].Load(),
		"bytesFromServer": act.bytesFrom[1].Load(),
	}
	log := logger.Default()
	if log == nil {
		// No default logger configured (early startup, tests):
		// the reap itself already happened — never let the
		// audit line take the sampler down.
		return
	}
	switch reason {
	case reapReasonEdge:
		fields["edgeWin"] = e.edgeWin
		fields["ackFrozenFor"] = time.Duration(e.failStreak) * reapTickInterval
		if info := e.lastInfo; info != nil {
			fields["tcpState"] = info.State
			fields["tcpUnacked"] = info.Unacked
			fields["tcpSndWnd"] = info.SndWnd
			fields["tcpNotsentBytes"] = info.NotsentBytes
			fields["tcpProbes"] = info.Probes
			fields["tcpBytesAcked"] = info.BytesAcked
			fields["tcpBytesSent"] = info.BytesSent
			fields["tcpTotalRetrans"] = info.TotalRetrans
		}
		log.WithFields(fields).Infof("session reaped: no consumption")
	case reapReasonBackstop:
		fields["backWin"] = e.backWin
		fields["serverBytesInWindow"] = e.lastServerBytes
		fields["serverRateBps"] = e.lastRateBps
		fields["trickleBps"] = e.trickle
		log.WithFields(fields).Infof("session reaped: backstop trickle")
	}
}
