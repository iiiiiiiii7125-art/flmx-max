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
// dead peer, and returns the error. A rolling deadline on src bounds
// each Read: when it fires, the shared activity tracker decides —
// if the sibling direction moved a byte within the window the copy
// simply re-arms and continues; if the whole session stalled, both
// endpoints are closed and ErrNoProgress is returned. The same
// rule applies to a Write that times out on dst.
//
// Hot-path cost: the copy still runs through io.CopyBuffer with
// the same 64KB pooled buffer, but the endpoints are wrapped in
// trackedRW, so io.CopyBuffer can no longer splice raw TCP
// connections (the wrappers hide *net.TCPConn's WriterTo /
// ReaderFrom) — the sendfile fast path the pre-fix code enjoyed
// on plain-TCP hops is given up, and bulk copies run through the
// buffered path instead. Note the endpoints Transport receives
// are themselves usually wrapped already — the forward handlers
// pass the client connection through the stats wrapper, which
// exposes no WriterTo/ReaderFrom either, so on those hops no
// splice existed even before this change; the splice loss is
// confined to hops whose endpoints arrive as raw connections.
// The throughput delta is measured in the FIX-1 report's
// performance section.
// CopyBuffer reports the error
// of the Read or Write it failed in through the tracking wrappers:
// a read timeout while the session is still progressing resumes
// the copy (a timed-out Read consumed nothing, so resuming loses
// no bytes); a write timeout is never resumed, because CopyBuffer
// cannot report how much of the current chunk was written — it
// falls through to the hard-failure path instead. Progress is
// recorded by the wrappers, one atomic store per chunk moved,
// the same shape as any byte-counting wrapper. Deadlines are
// armed once before the copy blocks and re-armed only at those
// event points, never per chunk.
func copyDirection(dst, src, rw1, rw2 io.ReadWriter, act *sessionActivity) error {
	ts := &trackedRW{rw: src, act: act}
	td := &trackedRW{rw: dst, act: act}
	for {
		ts.armRead()
		_, err := copyBufferTracked(td, ts)
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
		if err == errReadTimeout && ts.writeTimedOut.Load() == 0 {
			if !act.stalled(time.Now()) {
				// The sibling direction made progress within
				// the window; re-arm and resume copying.
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
// read-deadline expiry into errReadTimeout so copyDirection can
// distinguish a resumable read timeout from a write failure
// (whose partial progress CopyBuffer cannot report).
type trackedRW struct {
	rw  io.ReadWriter
	act *sessionActivity
	// writeTimedOut counts Write calls that failed with a
	// timeout; once nonzero the copy is no longer resumable.
	writeTimedOut atomic.Int64
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
		t.writeTimedOut.Add(1)
	}
	return n, err
}

// armRead arms src's rolling read deadline at last-progress +
// timeout. Called before the copy blocks and after a resumable
// read timeout — never per chunk.
func (t *trackedRW) armRead() {
	if t.act.timeout > 0 {
		if d, ok := t.rw.(readDeadliner); ok {
			_ = d.SetReadDeadline(t.act.deadline())
		}
	}
}

// armWrite arms dst's write deadline at now + timeout, so a
// Write blocked on a dead-slow consumer is bounded independently
// of when the session last made progress: bytes written a while
// ago must not make a fresh block expire instantly. The stall
// decision itself is taken on the read side and by session
// activity, never by this deadline alone (a write timeout is a
// hard failure, see copyDirection).
func (t *trackedRW) armWrite() {
	if t.act.timeout > 0 {
		if d, ok := t.rw.(writeDeadliner); ok {
			_ = d.SetWriteDeadline(time.Now().Add(t.act.timeout))
		}
	}
}

// copyBufferTracked is CopyBuffer with tracked endpoints, plus the
// write deadline arming for the destination.
func copyBufferTracked(dst *trackedRW, src *trackedRW) (int64, error) {
	dst.armWrite()
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
