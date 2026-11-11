package net

import (
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/go-gost/core/common/bufpool"
	"github.com/go-gost/x/internal/idle"
	"github.com/go-gost/x/internal/reap"
)

var errHalfCloseNotSupported = errors.New("half-close not supported by the underlying connection")

// ErrNoProgress is returned by Transport when a session is torn down
// because neither direction moved a single byte for the whole idle
// window (see idle.Get).
var ErrNoProgress = errors.New("transport: no progress in either direction within idle timeout")

// ErrEdgeReap is returned by Transport when the reaper sampler
// (reap.go) tore the session down at the edge: the originator
// sent nothing for the full edge window while data offered to it
// stood unacknowledged with its receive window shut — the
// endpoint is not consuming, whatever its kernel claims.
var ErrEdgeReap = errors.New("transport: session reaped at edge, client not consuming")

// ErrBackstopReap is returned by Transport when the reaper
// sampler tore the session down at a mid-hop backstop: the
// originator direction was silent for the full backstop window
// while the server direction moved only a sub-trickle drip.
var ErrBackstopReap = errors.New("transport: session reaped by backstop, originator silent with server trickle only")

// Reap reasons recorded on a sessionActivity by the sampler
// before it tears the session down, so the copy directions can
// report the distinguishing sentinel above instead of
// ErrNoProgress. Zero means the teardown (if any) is the classic
// no-progress judge's.
const (
	reapReasonNone     int32 = 0
	reapReasonEdge     int32 = 1
	reapReasonBackstop int32 = 2
)

const (
	bufferSize = 64 * 1024
)

type closeWriter interface {
	CloseWrite() error
}

type readDeadliner interface {
	SetReadDeadline(time.Time) error
}

type writeDeadliner interface {
	SetWriteDeadline(time.Time) error
}

// sessionActivity tracks the last time any byte moved in either
// direction of a Transport session. The two copy goroutines share
// one instance; a direction that wakes on its rolling deadline
// consults it to decide whether the session as a whole is stalled.
type sessionActivity struct {
	timeout time.Duration
	last    atomic.Int64 // UnixNano of the last byte moved, either direction
	// tornDown is set by the direction that initiates a no-progress
	// teardown, before it closes the endpoints, so the sibling —
	// whose pending Read/Write then fails with a closed-connection
	// error — reports the teardown's cause instead of that
	// secondary error.
	tornDown atomic.Bool

	// Direction-split progress, maintained only when trackDirs
	// is set — a reaper role (see TransportScoped) whose window
	// is enabled. Index 0 is rw1 (the client-side endpoint),
	// index 1 is rw2 (the server-side endpoint). A byte is
	// credited to the endpoint it arrived FROM, from READ-side
	// returns only: Write returns never credit, because every
	// written byte was already credited when it was read from
	// the sibling endpoint — and a wrapper that misreports its
	// write count therefore cannot forge direction progress.
	trackDirs  bool
	lastFrom   [2]atomic.Int64 // UnixNano of the last byte arrived per endpoint
	bytesFrom  [2]atomic.Int64 // cumulative bytes arrived per endpoint
	reapReason atomic.Int32    // reapReason* set by the sampler before its teardown

	// eofAt stamps the first clean EOF of either direction
	// (UnixNano, 0 = none so far). Once set, the surviving
	// direction's silence is bounded by the post-EOF drain bound
	// (see drainBound) even when the session window is off.
	eofAt atomic.Int64
}

func newSessionActivity(timeout time.Duration) *sessionActivity {
	return newSessionActivityDirs(timeout, false)
}

func newSessionActivityDirs(timeout time.Duration, trackDirs bool) *sessionActivity {
	a := &sessionActivity{timeout: timeout, trackDirs: trackDirs}
	now := time.Now().UnixNano()
	a.last.Store(now)
	if trackDirs {
		// Every session gets one full window of grace per
		// direction from establishment, mirroring the combined
		// stamp.
		a.lastFrom[0].Store(now)
		a.lastFrom[1].Store(now)
	}
	return a
}

func (a *sessionActivity) touch() {
	a.last.Store(time.Now().UnixNano())
}

// creditDir records n bytes arriving from endpoint dir (0 = rw1,
// 1 = rw2). Called only from read-side returns of trackedRW and
// from splice segment returns — never from writes.
func (a *sessionActivity) creditDir(dir int, n int) {
	now := time.Now().UnixNano()
	a.lastFrom[dir].Store(now)
	a.bytesFrom[dir].Add(int64(n))
}

// reapError returns the error a copy direction reports when the
// session was torn down underneath it: the sampler's sentinel
// when it recorded a reap reason, else the classic ErrNoProgress.
func (a *sessionActivity) reapError() error {
	switch a.reapReason.Load() {
	case reapReasonEdge:
		return ErrEdgeReap
	case reapReasonBackstop:
		return ErrBackstopReap
	}
	return ErrNoProgress
}

// deadline is the instant at which the session becomes provably
// stalled if nothing else moves: last progress + timeout.
func (a *sessionActivity) deadline() time.Time {
	return time.Unix(0, a.last.Load()).Add(a.timeout)
}

// stalled reports whether no byte has moved in either direction for
// the full timeout window. A non-positive window disables the
// teardown (the 2.1.2 default): stalled is then never true. (With
// the window off, timeout-class errors from an endpoint are not
// converted into deadline wakes at all — see trackedRW — so this
// judge is only ever consulted for wakes Transport armed itself.)
func (a *sessionActivity) stalled(now time.Time) bool {
	if a.timeout <= 0 {
		return false
	}
	return now.Sub(time.Unix(0, a.last.Load())) >= a.timeout
}

// teardown marks the session as being torn down for lack of
// progress and closes both endpoints, unblocking the sibling copy.
// rw1/rw2 are the session endpoints.
func (a *sessionActivity) teardown(rw1, rw2 io.ReadWriter) {
	a.tornDown.Store(true)
	closeEndpoint(rw1)
	closeEndpoint(rw2)
}

// defaultDrainBound bounds a silent post-EOF drain when the
// session carries no configured window: the historical FIX-1
// default and the UDP relay's association window. A var only so
// tests can shrink it; production code never writes it.
var defaultDrainBound = 300 * time.Second

// markEOF stamps the first clean EOF of either direction. The
// surviving direction is from then on subject to the drain bound.
func (a *sessionActivity) markEOF() {
	a.eofAt.CompareAndSwap(0, time.Now().UnixNano())
}

func (a *sessionActivity) eofPending() bool {
	return a.eofAt.Load() != 0
}

// drainBound is the post-EOF silence bound: the session window
// when one is configured (the existing judge then already bounds
// the drain, and this changes nothing), else defaultDrainBound.
func (a *sessionActivity) drainBound() time.Duration {
	if a.timeout > 0 {
		return a.timeout
	}
	return defaultDrainBound
}

// drainExpired reports whether the surviving direction has stayed
// byte-silent for the full drain bound since the last byte moved,
// with a peer EOF pending. Silence is the only kill condition: a
// byte arriving after the EOF advances the progress stamp and
// re-arms, so a flowing post-EOF response — however long — is
// never cut (F-212-B, server-first half-close contract).
func (a *sessionActivity) drainExpired(now time.Time) bool {
	if !a.eofPending() {
		return false
	}
	return now.Sub(time.Unix(0, a.last.Load())) >= a.drainBound()
}

// convertsWake reports whether a timeout-class error may be
// converted into a resumable deadline wake: while the session
// window is armed (the pending deadlines are then Transport's
// own), or once a peer EOF has armed the post-EOF drain
// deadlines. With neither in force, a timeout-class error cannot
// be one of Transport's wakes and must surface as the leg's real
// failure (F-212-C).
func (a *sessionActivity) convertsWake() bool {
	return a.timeout > 0 || a.eofPending()
}

// deadlinePromoted reports whether rw's deadline setters act on
// a connection shared with other sessions — a mux streamConn
// promotes them to the underlying session conn. Arming a
// per-session drain deadline on such an endpoint would disturb
// sibling streams, so drain arming (only) skips it; the endpoint
// keeps today's behaviour.
func deadlinePromoted(rw io.ReadWriter) bool {
	if p, ok := rw.(interface{ DeadlinePromoted() bool }); ok {
		return p.DeadlinePromoted()
	}
	return false
}

// armSurvivorDrain performs the first drain arming for the
// surviving direction when this direction reaches its EOF with
// the session window off: until the EOF landed, no deadline was
// armed anywhere, so the survivor may be blocked in a Read or
// Write that nothing would ever wake. The endpoints here are
// copyDirection's (dst, src): the survivor reads from dst and
// writes to src. With a window armed this is unnecessary — the
// rolling deadlines already cover the survivor — and skipped, so
// armed sessions stay bit-for-bit unchanged.
func (a *sessionActivity) armSurvivorDrain(dst, src io.ReadWriter) {
	if a.timeout > 0 {
		return
	}
	dl := time.Unix(0, a.last.Load()).Add(a.drainBound())
	if !deadlinePromoted(dst) {
		if d, ok := dst.(readDeadliner); ok {
			_ = d.SetReadDeadline(dl)
		}
	}
	if !deadlinePromoted(src) {
		if d, ok := src.(writeDeadliner); ok {
			_ = d.SetWriteDeadline(dl)
		}
	}
}

// Transport copies data between rw1 and rw2 in both directions.
//
// Half-close semantics (D3-03): when one direction reaches its end,
// the destination's write side is half-closed (CloseWrite, where the
// transport supports it) so the peer observes the end of that stream,
// and the reverse direction keeps copying until it ends too.
// Transport returns only once both directions are done. On a hard
// (non-EOF) failure both endpoints are closed to unblock the sibling
// copy, and the error is returned.
//
// Previously the first direction to finish — including a clean client
// shutdown(WR) — returned from Transport at once, and the handlers'
// deferred closes then discarded whatever the reverse direction still
// had in flight: a half-closing client lost the tail of the response.
//
// No-progress bound (FIX-1, reworked 2.1.2): a byte moving in either
// direction counts as progress and keeps the session alive; if
// neither direction moves a byte for the configured window, both
// endpoints are closed and ErrNoProgress is returned. The window is
// the node-level panel setting published via internal/idle (service
// metadata "idleTimeout") and is DISABLED by default: with no
// window configured, no deadline is ever armed and Transport keeps
// the pre-FIX-1 unbounded behaviour. The bound exists for the two
// stall shapes the old "ends by the peer's own close" bound never
// reaped: a peer whose kernel still answers keepalive while its
// application is dead, and a half-close that stalls the reverse
// direction mid-chain (the surviving copy goroutine parked on Read
// forever, pinning the FIN-WAIT-2 socket) — a node operator opts
// into reaping those by setting the window. Endpoints that expose
// read/write deadlines get a rolling deadline armed at
// last-progress + timeout — no extra timer goroutine per session;
// endpoints without deadline support keep the legacy behaviour
// unless the sibling direction's teardown closes them. Deadlines
// armed by Transport are cleared again before it returns.
//
// Post-EOF drain bound (independent of the opt-in window): once
// one direction has cleanly ended, the surviving direction may
// keep flowing without any wall-clock cap, but it may not stay
// byte-silent for longer than the drain bound (the window when
// configured, else 300s) — a parked post-EOF drain is the
// FIN-WAIT-2 pin named above, and silence is the only thing the
// bound ever judges, so a flowing server-first response is never
// cut. Endpoints whose deadline setters are promoted to a shared
// connection (mux streams) are exempt from drain arming and keep
// the legacy behaviour.
func Transport(rw1, rw2 io.ReadWriter) error {
	return transport(rw1, rw2, idle.Get())
}

// Scope selects a zombie-session reaper role for a Transport
// session (see reap.go). The zero Scope assigns no role: the
// session is never direction-tracked or sampler-registered, and
// behaves exactly like a plain Transport session.
//
// Role is "edge" for the service whose accepted connection
// terminates the true end-client leg (its consumption is
// TCP_INFO-visible), or "backstop" for mid-chain relay segment
// services, whose accepted peer is always the previous hop's
// agent. Any other value assigns no role. Service is the
// handler's service name, carried into reap logs and metrics.
type Scope struct {
	Role    string
	Service string
}

// TransportScoped is Transport with a reaper Scope. CONTRACT:
// rw1 must be the client-side endpoint and rw2 the server-side
// endpoint — direction accounting (and therefore both reaper
// criteria) is defined in those terms. The role's window is
// snapshotted from internal/reap at session start; a republish
// affects new sessions only, like the idle window. With the
// role's window disabled (the default) the session is
// indistinguishable from a plain Transport session.
func TransportScoped(rw1, rw2 io.ReadWriter, sc Scope) error {
	return transportScoped(rw1, rw2, idle.Get(), sc)
}

func transport(rw1, rw2 io.ReadWriter, idleTimeout time.Duration) error {
	return transportScoped(rw1, rw2, idleTimeout, Scope{})
}

func transportScoped(rw1, rw2 io.ReadWriter, idleTimeout time.Duration, sc Scope) error {
	role := parseReapRole(sc.Role)
	var edgeWin, backWin time.Duration
	var trickle int64
	switch role {
	case reapRoleEdge:
		edgeWin = reap.EdgeWindow()
	case reapRoleBackstop:
		backWin = reap.BackstopWindow()
		trickle = reap.EffectiveTrickleBps()
	}
	trackDirs := (role == reapRoleEdge && edgeWin > 0) ||
		(role == reapRoleBackstop && backWin > 0)

	act := newSessionActivityDirs(idleTimeout, trackDirs)
	if trackDirs {
		registerReapSession(act, rw1, rw2, reapSessionInfo{
			role:    role,
			edgeWin: edgeWin,
			backWin: backWin,
			trickle: trickle,
			service: sc.Service,
		})
		defer unregisterReapSession(act)
	}
	defer clearDeadlines(rw1)
	defer clearDeadlines(rw2)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		errs[0] = copyDirection(rw1, rw2, rw1, rw2, act)
	}()
	go func() {
		defer wg.Done()
		errs[1] = copyDirection(rw2, rw1, rw1, rw2, act)
	}()
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func clearDeadlines(rw io.ReadWriter) {
	if d, ok := rw.(readDeadliner); ok {
		_ = d.SetReadDeadline(time.Time{})
	}
	if d, ok := rw.(writeDeadliner); ok {
		_ = d.SetWriteDeadline(time.Time{})
	}
}

func closeEndpoint(rw io.ReadWriter) {
	if c, ok := rw.(io.Closer); ok {
		c.Close()
	}
}

func isTimeoutError(err error) bool {
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var nerr net.Error
	return errors.As(err, &nerr) && nerr.Timeout()
}

// isResumableWake reports whether a timeout-class error may be
// converted into a resumable deadline wake (errReadTimeout). One
// timeout shape is provably NOT a wake: a kernel timeout verdict,
// which carries syscall.ETIMEDOUT — the socket's own keepalive /
// user-timeout machinery has already declared the peer dead, so
// the leg will never move another byte. A genuine expiry of an
// armed deadline instead reports os.ErrDeadlineExceeded, and
// after re-arming the copy parks in the poller until data or the
// new deadline, so resuming on it cannot spin. Converting an
// ETIMEDOUT, by contrast, re-polls a dead socket whose failure
// returns at once; with the idle window on, the stall judge keeps
// the session "alive" for as long as the sibling direction moves
// any byte, so the dead leg busy-loops for the whole session (the
// window-on mirror of F-212-C, which gated only the window-off
// case). A kernel verdict must therefore reach copyDirection
// unconverted in both window states, so the session unwinds
// whole. Every other timeout-class error stays convertible: the
// stall judge remains the arbiter for shapes the wrapper cannot
// tell apart from Transport's own wakes.
func isResumableWake(err error) bool {
	return isTimeoutError(err) && !errors.Is(err, syscall.ETIMEDOUT)
}

// copyDirection copies src -> dst until src is exhausted or fails.
// A clean end propagates a half-close to dst; a hard failure closes
// both endpoints so the sibling direction cannot park forever on a
// dead peer, and returns the error.
//
// No-progress bound: each endpoint carries a rolling read/write
// deadline set to the session's last-progress instant + timeout.
// When a copy's pending Read or Write wakes on its deadline, the
// shared activity tracker decides: if any byte moved in either
// direction within the window, the copy re-arms and resumes; if
// the whole session stalled, both endpoints are closed and
// ErrNoProgress is returned. Resuming is byte-exact by
// construction: a deadline expiry on a plain Read/Write moved no
// bytes (the net package's deadline abort is all-or-nothing), and
// a splice segment that moved bytes before its wake reports them
// and refreshes the progress stamp before the wrapper converts
// the wake into errReadTimeout (see trackedRW) — so nothing is
// ever re-sent or skipped, and bytes the kernel already accepted
// stay accepted.
//
// Hot-path cost: raw TCP hops still run through io.CopyBuffer with
// the same 64KB pooled buffer, and raw TCP source/destination
// pairs keep their kernel sendfile splice — the wrappers forward
// WriterTo/ReaderFrom to the raw connections when both wrapped
// endpoints are *net.TCPConn. Progress is recorded by the
// wrappers, one atomic store per chunk moved on the buffered path
// and one per segment on the splice path. Deadlines are armed
// once before the copy blocks and
// re-armed only at deadline-wake event points, never per chunk.
func copyDirection(dst, src, rw1, rw2 io.ReadWriter, act *sessionActivity) error {
	ts, srcTCP := src.(*net.TCPConn)
	td, dstTCP := dst.(*net.TCPConn)
	_, _ = ts, td
	// Direction tags for the reaper's split accounting: bytes are
	// credited to the endpoint they arrive FROM, i.e. to src's
	// index. rw1 is index 0 (client side), rw2 index 1.
	srcDir, dstDir := 1, 0
	if src == rw1 {
		srcDir, dstDir = 0, 1
	}
	ws := &trackedRW{rw: src, act: act, tcp: srcTCP, dir: srcDir}
	wd := &trackedRW{rw: dst, act: act, tcp: dstTCP, dir: dstDir}
	for {
		ws.armRead()
		wd.armWrite()
		_, err := copyBufferTracked(wd, ws)
		if err == nil || err == io.EOF {
			// Stamp the EOF before the half-close: from here on
			// the surviving direction's silence is bounded by
			// the post-EOF drain bound (see drainExpired), and
			// its currently blocked copy — which may hold no
			// armed deadline at all when the window is off —
			// gets its first drain arming.
			act.markEOF()
			act.armSurvivorDrain(dst, src)
			if cw, ok := dst.(closeWriter); ok {
				// Best effort: the reverse direction's completion
				// does not depend on this succeeding.
				_ = cw.CloseWrite()
			}
			return nil
		}
		if act.tornDown.Load() {
			// This error is fallout of a teardown (the sibling's
			// no-progress teardown or the reaper sampler closed
			// the endpoints), not a failure of this direction.
			return act.reapError()
		}
		if err == errReadTimeout {
			now := time.Now()
			if !act.stalled(now) && !act.drainExpired(now) {
				// A byte moved within the window (necessarily in
				// the sibling direction — this one was blocked),
				// or the post-EOF drain still has silence budget:
				// re-arm and resume. Resuming is exact: the
				// wrappers never lose bytes across a deadline
				// wake (see trackedRW).
				continue
			}
			act.teardown(rw1, rw2)
			return ErrNoProgress
		}
		closeEndpoint(rw1)
		closeEndpoint(rw2)
		return err
	}
}

var errReadTimeout = errors.New("transport: read deadline exceeded")

// trackedRW wraps one endpoint of a Transport session: it records
// byte progress into the shared activity tracker and converts a
// deadline expiry into errReadTimeout so copyDirection can
// distinguish a resumable deadline wake from a hard failure. The
// conversion applies only while a judge is armed — the session
// window, or the post-EOF drain bound once a peer EOF has landed
// (act.convertsWake): the only deadlines Transport itself ever
// arms belong to one of those two, so a timeout-class error
// surfacing with neither in force cannot be one of Transport's
// wakes — it is a real failure of that leg (F-212-C: a kernel
// keepalive verdict on a silently dead peer returns ETIMEDOUT)
// and must reach copyDirection unconverted, so the session
// unwinds whole instead of "resuming" against a dead socket
// forever. The same carve-out
// applies with the window ON to the one timeout shape that is
// provably not a wake — a kernel ETIMEDOUT verdict (see
// isResumableWake): it converted too while the window was armed,
// and the dead leg then spun against the stall judge for as long
// as the sibling direction kept moving bytes. The conversion is
// state-free and exact: a Go net.Conn Read/Write
// that reports a timeout error has transferred no bytes (its
// internal deadline abort is all-or-nothing), so a timed-out call
// contributes nothing and the copy can resume as if the wake had
// never happened — while any call that moved bytes reports them
// with a nil or non-timeout error. A splice segment (below) is
// the one call shape that can report bytes and a timeout
// together; its wrapper touches the progress stamp before
// converting, so the copy loop's stalled check already accounts
// for those bytes when it decides to resume. For raw TCP
// endpoints it also forwards the WriterTo/ReaderFrom fast paths,
// so io.CopyBuffer keeps its kernel splice.
type trackedRW struct {
	rw  io.ReadWriter
	act *sessionActivity
	tcp bool
	// dir is this endpoint's direction index (0 = rw1, 1 = rw2)
	// for the reaper's split accounting. Only read-side returns
	// credit it (see sessionActivity.creditDir).
	dir int
}

func (t *trackedRW) Read(p []byte) (int, error) {
	n, err := t.rw.Read(p)
	if n > 0 {
		t.act.touch()
		if t.act.trackDirs {
			t.act.creditDir(t.dir, n)
		}
	}
	if err != nil && t.act.convertsWake() && isResumableWake(err) {
		return n, errReadTimeout
	}
	return n, err
}

func (t *trackedRW) Write(p []byte) (int, error) {
	n, err := t.rw.Write(p)
	if n > 0 {
		t.act.touch()
		// No direction credit here, by design: the byte was
		// already direction-credited when the sibling direction
		// read it from its own endpoint.
	}
	if err != nil && t.act.convertsWake() && isResumableWake(err) {
		return n, errReadTimeout
	}
	return n, err
}

// WriteTo implements io.WriterTo. For a raw TCP source/destination
// pair it forwards io.CopyBuffer's splice to the underlying
// connection. Every other shape falls back to a buffered copy
// through the tracked Read path: io.CopyBuffer dispatches to
// WriterTo unconditionally and treats any error it returns as
// fatal (there is no fallback in io.CopyBuffer itself), so this
// method must never decline the copy — an earlier version returned
// errFastPathUnavailable for non-TCP endpoints, which killed every
// Transport session whose endpoints were wrapped (stats/traffic
// wrappers, readWriteConn, UDP session conns) the moment it
// started; the connector/relay UDP tests caught it.
func (t *trackedRW) WriteTo(w io.Writer) (int64, error) {
	if t.tcp {
		if dst, ok := w.(*trackedRW); ok && dst.tcp {
			src, ok1 := t.rw.(*net.TCPConn)
			dstc, ok2 := dst.rw.(*net.TCPConn)
			if ok1 && ok2 {
				// Arm the same deadlines the buffered path would
				// carry, so a stalled splice wakes on the session
				// deadline too — with the read side capped per
				// segment (see armReadSegment).
				t.armReadSegment()
				dst.armWrite()
				// The kernel splice entry point is
				// (*net.TCPConn).ReadFrom with the raw source
				// connection: any intervening wrapper (the tee
				// wrappers an earlier version used to observe
				// progress) defeats the net package's *TCPConn
				// type switch and silently degrades the transfer
				// to a generic buffered copy, ~30% slower on
				// loopback. Progress is therefore recorded per
				// splice segment instead: the call returns on
				// EOF, on error, or when an armed deadline wakes
				// it, and the touch below credits the bytes it
				// moved before copyDirection decides whether the
				// session stalled.
				n, err := dstc.ReadFrom(src)
				if n > 0 {
					t.act.touch()
					if t.act.trackDirs {
						// t is the source wrapper: the segment's
						// bytes arrived FROM t's endpoint.
						t.act.creditDir(t.dir, int(n))
					}
				}
				if err != nil && t.act.convertsWake() && isResumableWake(err) {
					return n, errReadTimeout
				}
				return n, err
			}
		}
	}
	buf := bufpool.Get(bufferSize)
	defer bufpool.Put(buf)
	// readerOnly hides this method from io.CopyBuffer so the
	// fallback cannot dispatch back into WriteTo.
	return io.CopyBuffer(w, readerOnly{t}, buf)
}

// ReadFrom implements io.ReaderFrom. For a raw TCP source/
// destination pair it forwards io.CopyBuffer's splice to the
// underlying connection; every other shape falls back to a
// buffered copy through the tracked Write path, for the same
// reason as WriteTo: io.CopyBuffer treats a ReaderFrom error as
// fatal, so this method must never decline the copy.
func (t *trackedRW) ReadFrom(r io.Reader) (int64, error) {
	if t.tcp {
		if src, ok := r.(*trackedRW); ok && src.tcp {
			srcc, ok1 := src.rw.(*net.TCPConn)
			dst, ok2 := t.rw.(*net.TCPConn)
			if ok1 && ok2 {
				src.armReadSegment()
				t.armWrite()
				// Raw source connection, as in WriteTo: wrapping
				// it would defeat the splice type switch.
				n, err := dst.ReadFrom(srcc)
				if n > 0 {
					t.act.touch()
					if t.act.trackDirs {
						// The segment's bytes arrived FROM the
						// source wrapper's endpoint (src), not
						// from t (the destination wrapper).
						t.act.creditDir(src.dir, int(n))
					}
				}
				if err != nil && t.act.convertsWake() && isResumableWake(err) {
					return n, errReadTimeout
				}
				return n, err
			}
		}
	}
	buf := bufpool.Get(bufferSize)
	defer bufpool.Put(buf)
	// writerOnly hides this method from io.CopyBuffer so the
	// fallback cannot dispatch back into ReadFrom.
	return io.CopyBuffer(writerOnly{t}, r, buf)
}

// readerOnly exposes only io.Reader of the wrapped value, and
// writerOnly only io.Writer; they keep io.CopyBuffer's interface
// dispatch from recursing into trackedRW's WriterTo/ReaderFrom on
// the generic fallback path.
type readerOnly struct{ io.Reader }

type writerOnly struct{ io.Writer }

// Splice transfers record progress per segment in WriteTo/
// ReadFrom above, not per chunk: the kernel moves the bytes, the
// segment's return value counts them, and a deadline wake ends a
// segment the same way it ends a blocked Read — so the copy loop
// sees the same event shape on both paths.

// readDeadline computes this endpoint's effective read deadline:
// the session stall deadline when the window is armed, tightened
// by the post-EOF drain deadline once a peer EOF is pending. The
// drain component is skipped for endpoints whose deadline
// setters are not endpoint-local (deadlinePromoted): arming it
// would write a deadline onto a shared mux session conn.
func (t *trackedRW) readDeadline() (time.Time, bool) {
	var dl time.Time
	var armed bool
	if t.act.timeout > 0 {
		dl = t.act.deadline()
		armed = true
	}
	if t.act.eofPending() && !deadlinePromoted(t.rw) {
		ddl := time.Unix(0, t.act.last.Load()).Add(t.act.drainBound())
		if !armed || ddl.Before(dl) {
			dl, armed = ddl, true
		}
	}
	return dl, armed
}

// armRead arms src's rolling read deadline at last-progress +
// timeout. Called before the copy blocks and after a resumable
// deadline wake — never per chunk.
func (t *trackedRW) armRead() {
	if d, ok := t.rw.(readDeadliner); ok {
		if dl, armed := t.readDeadline(); armed {
			_ = d.SetReadDeadline(dl)
		}
	}
}

// armReadSegment arms the read deadline for one kernel splice
// segment: the earlier of the session stall deadline and
// now + timeout/3. A splice moves bytes invisibly to userspace
// until the call returns, so an uncapped segment would batch a
// whole window of progress credit into its return instant —
// which is exactly when the sibling direction's own deadline,
// armed from the same progress stamp, also expires — and the
// sibling could judge the session stalled in the gap between
// its wake and the segment's credit landing. (The buffered path
// has no such gap: every chunk returns through Read and credits
// immediately.) Capping the segment makes a flowing direction
// land its credit at least three times per window, while the
// min() with the stall deadline keeps a truly stalled session's
// teardown at the same instant as the uncapped shape.
func (t *trackedRW) armReadSegment() {
	if d, ok := t.rw.(readDeadliner); ok {
		dl, armed := t.readDeadline()
		if !armed {
			return
		}
		// Cap the segment at a third of the governing bound (the
		// session window, or the drain bound when only the drain
		// is in force), for the batching reason above.
		bound := t.act.timeout
		if bound <= 0 {
			bound = t.act.drainBound()
		}
		if cap := time.Now().Add(bound / 3); cap.Before(dl) {
			dl = cap
		}
		_ = d.SetReadDeadline(dl)
	}
}

// armWrite arms dst's write deadline at now + timeout, so a
// Write blocked on a dead-slow consumer is bounded independently
// of when the session last made progress: bytes written a while
// ago must not make a fresh block expire instantly. The stall
// decision itself is taken from session activity, never by this
// deadline alone.
func (t *trackedRW) armWrite() {
	if d, ok := t.rw.(writeDeadliner); ok {
		var dl time.Time
		var armed bool
		if t.act.timeout > 0 {
			dl = time.Now().Add(t.act.timeout)
			armed = true
		}
		if t.act.eofPending() && !deadlinePromoted(t.rw) {
			ddl := time.Unix(0, t.act.last.Load()).Add(t.act.drainBound())
			if !armed || ddl.Before(dl) {
				dl, armed = ddl, true
			}
		}
		if armed {
			_ = d.SetWriteDeadline(dl)
		}
	}
}

// copyBufferTracked is io.CopyBuffer over the tracked endpoints
// with the pooled 64KB buffer, unchanged from the pre-fix shape.
func copyBufferTracked(dst *trackedRW, src *trackedRW) (int64, error) {
	buf := bufpool.Get(bufferSize)
	defer bufpool.Put(buf)
	return io.CopyBuffer(dst, src, buf)
}

func CopyBuffer(dst io.Writer, src io.Reader, bufSize int) error {
	buf := bufpool.Get(bufSize)
	defer bufpool.Put(buf)

	_, err := io.CopyBuffer(dst, src, buf)
	return err
}
