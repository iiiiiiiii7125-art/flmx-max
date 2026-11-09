package tun

import (
	"github.com/go-gost/core/logger"
	"github.com/go-gost/x/internal/util/eventlog"
)

// tunPacketEvents aggregates the TUN handler's per-packet warning
// points (R2-P1-F10). The fourteen Warn points in transportServer
// and transportClient sat inside the per-packet read loops and
// logged once per malformed or unroutable packet — 10⁴–10⁵
// lines/second under a garbage-packet flood, competing with
// forwarding for CPU and disk on small nodes. They now follow the
// F11 form (ss/udp): each event kind is counted through an
// eventlog DetailCounter/Counter, which costs two atomic
// increments on the hot path and emits at most one summary line
// per kind per aggregation window, plus a final Flush when the
// transport ends. Packet handling itself is unchanged: bad-header
// and unknown packets are still discarded, keepalive failures
// still skip the reply — only the log shape changes.
type tunPacketEvents struct {
	agg       *eventlog.Aggregator
	badHeader *eventlog.Counter
	unknown   *eventlog.Counter
	keepalive *eventlog.Counter
}

func newTunPacketEvents(log logger.Logger) *tunPacketEvents {
	agg := eventlog.New(log, eventlog.DefaultInterval)
	return &tunPacketEvents{
		agg:       agg,
		badHeader: agg.DetailCounter("tun: %d packet(s) discarded (bad IP header), last: %s, %d total"),
		unknown:   agg.Counter("tun: %d unknown packet(s) discarded, %d total"),
		keepalive: agg.DetailCounter("tun: %d keepalive failure(s), last: %s, %d total"),
	}
}

// Flush emits the pending per-kind summaries. The transport
// functions call it on return so a burst that ends with the
// transport is not silently lost.
func (e *tunPacketEvents) Flush() {
	e.agg.Flush()
}
