package net

import (
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-gost/core/common/bufpool"
)

var errHalfCloseNotSupported = errors.New("half-close not supported by the underlying connection")

// ErrNoProgress is returned by Transport when a session is torn down
// because neither direction moved a single byte for the whole idle
// window (see defaultIdleTimeout).
var ErrNoProgress = errors.New("transport: no progress in either direction within idle timeout")

const (
	bufferSize = 64 * 1024

	// defaultIdleTimeout bounds how long a forwarded session may go
	// without a single byte moving in either direction before
	// Transport tears it down (FIX-1, audit A-01/B-01).
	//
	// Basis: kernel TCP keepalive already reaps a fully dead endpoint
	// (peer kernel silent) in ~150s (live-measured 140-150s in audit
	// C, 141-241s in C2 cell 4b), so 300s stays roughly 2x above that
	// path and never pre-empts it; the window only fires where the
	// kernel is blind — peer kernel alive but application dead, or a
	// half-close stalled mid-chain. On the other side, production
	// accumulation reached OOM over hours, so capping a dead
	// session's residency at ~5 minutes bounds resident dead
	// sessions to arrival-rate x 300s, orders of magnitude below the
	// thousands observed on ECS1686. The value is an internal
	// constant of the forwarding plane: the panel's connIdle setting
	// is a per-inbound Xray-kernel policy that is never plumbed into
	// gost forwarding services (and carries its own 0/-1 user
	// semantics), so it is deliberately not reused here.
	defaultIdleTimeout = 300 * time.Second
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
}

func newSessionActivity(timeout time.Duration) *sessionActivity {
	a := &sessionActivity{timeout: timeout}
	a.last.Store(time.Now().UnixNano())
	return a
}

func (a *sessionActivity) touch() {
	a.last.Store(time.Now().UnixNano())
}

// deadline is the instant at which the session becomes provably
// stalled if nothing else moves: last progress + timeout.
func (a *sessionActivity) deadline() time.Time {
	return time.Unix(0, a.last.Load()).Add(a.timeout)
}

// stalled reports whether no byte has moved in either direction for
// the full timeout window.
func (a *sessionActivity) stalled(now time.Time) bool {
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
// No-progress bound (FIX-1): a byte moving in either direction counts
// as progress and keeps the session alive; if neither direction moves
// a byte for defaultIdleTimeout, both endpoints are closed and
// ErrNoProgress is returned. This covers the two stall shapes the old
// "ends by the peer's own close" bound never reaped: a peer whose
// kernel still answers keepalive while its application is dead, and
// a half-close that stalls the reverse direction mid-chain (the
// surviving copy goroutine parked on Read forever, pinning the
// FIN-WAIT-2 socket). Endpoints that expose read/write deadlines get
// a rolling deadline armed at last-progress + timeout — no extra
// timer goroutine per session; endpoints without deadline support
// keep the legacy behaviour unless the sibling direction's teardown
// closes them. Deadlines armed by Transport are cleared again before
// it returns.
func Transport(rw1, rw2 io.ReadWriter) error {
	return transport(rw1, rw2, defaultIdleTimeout)
}

func transport(rw1, rw2 io.ReadWriter, idleTimeout time.Duration) error {
	act := newSessionActivity(idleTimeout)
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
	ws := &trackedRW{rw: src, act: act, tcp: srcTCP}
	wd := &trackedRW{rw: dst, act: act, tcp: dstTCP}
	for {
		ws.armRead()
		wd.armWrite()
		_, err := copyBufferTracked(wd, ws)
		if err == nil || err == io.EOF {
			if cw, ok := dst.(closeWriter); ok {
				// Best effort: the reverse direction's completion
				// does not depend on this succeeding.
				_ = cw.CloseWrite()
			}
			return nil
		}
		if act.tornDown.Load() {
			// This error is fallout of the sibling's no-progress
			// teardown (it closed the endpoints), not a failure
			// of this direction.
			return ErrNoProgress
		}
		if err == errReadTimeout {
			if !act.stalled(time.Now()) {
				// A byte moved within the window (necessarily in
				// the sibling direction — this one was blocked):
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
// conversion is state-free and exact: a Go net.Conn Read/Write
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
}

func (t *trackedRW) Read(p []byte) (int, error) {
	n, err := t.rw.Read(p)
	if n > 0 {
		t.act.touch()
	}
	if err != nil && isTimeoutError(err) {
		return n, errReadTimeout
	}
	return n, err
}

func (t *trackedRW) Write(p []byte) (int, error) {
	n, err := t.rw.Write(p)
	if n > 0 {
		t.act.touch()
	}
	if err != nil && isTimeoutError(err) {
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
				}
				if err != nil && isTimeoutError(err) {
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
				}
				if err != nil && isTimeoutError(err) {
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

// armRead arms src's rolling read deadline at last-progress +
// timeout. Called before the copy blocks and after a resumable
// deadline wake — never per chunk.
func (t *trackedRW) armRead() {
	if t.act.timeout > 0 {
		if d, ok := t.rw.(readDeadliner); ok {
			_ = d.SetReadDeadline(t.act.deadline())
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
	if t.act.timeout > 0 {
		if d, ok := t.rw.(readDeadliner); ok {
			dl := t.act.deadline()
			if cap := time.Now().Add(t.act.timeout / 3); cap.Before(dl) {
				dl = cap
			}
			_ = d.SetReadDeadline(dl)
		}
	}
}

// armWrite arms dst's write deadline at now + timeout, so a
// Write blocked on a dead-slow consumer is bounded independently
// of when the session last made progress: bytes written a while
// ago must not make a fresh block expire instantly. The stall
// decision itself is taken from session activity, never by this
// deadline alone.
func (t *trackedRW) armWrite() {
	if t.act.timeout > 0 {
		if d, ok := t.rw.(writeDeadliner); ok {
			_ = d.SetWriteDeadline(time.Now().Add(t.act.timeout))
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
