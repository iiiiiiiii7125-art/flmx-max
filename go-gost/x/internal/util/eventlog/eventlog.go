// Package eventlog aggregates high-rate event logging on the
// connection/session hot paths.
//
// Several listener and dispatch points used to emit one Warn/Error
// line per event inside the accept/admission/dispatch path —
// connection-queue-full discards, handshake failures, per-request
// errors — so under a flood or a scan the log rate equalled the
// event rate: up to tens of thousands of lines per second through
// journald, competing with forwarding for CPU and disk on small
// nodes (P1-03; the UDP member of the family was fixed first, in
// x/internal/net/udp, and this package is the shared form of that
// aggregator so every member of the family uses one implementation
// instead of a copy per file).
//
// An Aggregator owns a set of Counters, one per event kind. Each
// Counter keeps two atomic counters (cumulative total, and pending
// since the last summary) plus, for detail counters, the most
// recent event's detail string (client address or error text). A
// Note on the hot path costs two atomic increments and a window
// check; at most one summary line per kind is emitted per
// aggregation window, and Flush forces the pending counts out when
// the owner shuts down, so a burst that ends with the owner is not
// silently lost and the cumulative total is always visible in the
// summary. Nothing about the underlying event changes: only the
// log shape does.
package eventlog

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-gost/core/logger"
)

// DefaultInterval is the aggregation window used when New is given
// a non-positive interval. It matches the window of the original
// UDP aggregator (P1-03).
const DefaultInterval = 30 * time.Second

// Aggregator window-aggregates the log output of one owner (one
// listener, one service, ...). Counters must be registered with
// Counter / DetailCounter before the Aggregator is put to use;
// registration is not expected to race with Note calls. The zero
// value is not usable; a nil *Aggregator is safe to Note and Flush
// (all calls are no-ops), so owners may call through an
// uninitialised field without panicking.
type Aggregator struct {
	logger   logger.Logger
	interval time.Duration

	mu        sync.Mutex // guards lastFlush and counters
	lastFlush time.Time
	counters  []*Counter

	// lastFlushNano mirrors lastFlush for the lock-free fast path
	// in note; it is written under mu whenever lastFlush changes.
	lastFlushNano atomic.Int64
}

// New returns an Aggregator emitting summary lines at Warn level
// through l (which may be nil: counts still advance, summaries are
// dropped). A non-positive interval selects DefaultInterval.
func New(l logger.Logger, interval time.Duration) *Aggregator {
	if interval <= 0 {
		interval = DefaultInterval
	}
	now := time.Now()
	a := &Aggregator{
		logger:    l,
		interval:  interval,
		lastFlush: now,
	}
	a.lastFlushNano.Store(now.UnixNano())
	return a
}

// Counter registers an event kind whose summary line is produced
// by formatting format with (window count, cumulative total), e.g.
// "listener: %d connection(s) discarded (queue full), %d total".
func (a *Aggregator) Counter(format string) *Counter {
	return a.register(format, false)
}

// DetailCounter registers an event kind whose summary line is
// produced by formatting format with (window count, most recent
// detail, cumulative total), e.g. "listener: %d handshake
// failure(s), last: %s, %d total". The detail of the most recent
// event is supplied through NoteDetail / NoteErr.
func (a *Aggregator) DetailCounter(format string) *Counter {
	return a.register(format, true)
}

func (a *Aggregator) register(format string, detail bool) *Counter {
	if a == nil {
		return nil
	}
	c := &Counter{agg: a, format: format, detail: detail}
	a.mu.Lock()
	a.counters = append(a.counters, c)
	a.mu.Unlock()
	return c
}

// Flush emits one summary line per counter that has pending
// events, regardless of the aggregation window. Owners call it on
// shutdown so trailing counts are not lost.
func (a *Aggregator) Flush() {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.flushLocked(time.Now(), true)
	a.mu.Unlock()
}

func (a *Aggregator) tryFlush() {
	a.mu.Lock()
	a.flushLocked(time.Now(), false)
	a.mu.Unlock()
}

// flushLocked emits the summaries. The caller must hold a.mu.
func (a *Aggregator) flushLocked(now time.Time, force bool) {
	if a.logger == nil {
		for _, c := range a.counters {
			c.pend.Store(0)
		}
		return
	}
	if !force && now.Sub(a.lastFlush) < a.interval {
		return
	}
	flushed := false
	for _, c := range a.counters {
		n := c.pend.Swap(0)
		if n == 0 {
			continue
		}
		if c.detail {
			last, _ := c.last.Load().(string)
			a.logger.Warnf(c.format, n, last, c.total.Load())
		} else {
			a.logger.Warnf(c.format, n, c.total.Load())
		}
		flushed = true
	}
	if flushed || force {
		a.lastFlush = now
		a.lastFlushNano.Store(now.UnixNano())
	}
}

// A Counter counts one kind of event for its Aggregator. The zero
// value is not usable; a nil *Counter is safe to Note (no-op).
type Counter struct {
	agg    *Aggregator
	format string
	detail bool

	total atomic.Uint64 // cumulative events since creation
	pend  atomic.Uint64 // events since the last summary
	last  atomic.Value  // string: detail of the most recent event
}

// Note records one event.
func (c *Counter) Note() {
	c.note("")
}

// NoteDetail records one event, remembering detail (a client
// address, an error text, ...) as the most recent sample for the
// next summary line. Only meaningful for DetailCounter kinds; on a
// plain Counter the detail is discarded.
func (c *Counter) NoteDetail(detail string) {
	c.note(detail)
}

// NoteErr is NoteDetail with the error's text as the detail.
func (c *Counter) NoteErr(err error) {
	if err == nil {
		c.note("")
		return
	}
	c.note(err.Error())
}

func (c *Counter) note(detail string) {
	if c == nil || c.agg == nil {
		return
	}
	c.total.Add(1)
	c.pend.Add(1)
	if c.detail && detail != "" {
		c.last.Store(detail)
	}
	a := c.agg
	// Fast path: the window has not elapsed, so no summary can be
	// due. time.Since reads the monotonic clock once; the window
	// check itself is one atomic load and a comparison, so a Note
	// inside the window costs two atomic increments, at most one
	// atomic.Value store, and no allocation for plain counters.
	if time.Since(time.Unix(0, a.lastFlushNano.Load())) < a.interval {
		return
	}
	a.tryFlush()
}

// Total returns the cumulative number of events recorded.
func (c *Counter) Total() uint64 {
	if c == nil {
		return 0
	}
	return c.total.Load()
}

// Pending returns the number of events recorded since the last
// summary was emitted.
func (c *Counter) Pending() uint64 {
	if c == nil {
		return 0
	}
	return c.pend.Load()
}
