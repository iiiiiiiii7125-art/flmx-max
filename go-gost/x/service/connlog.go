package service

import (
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Inbound connection log collection (入站连接日志, design
// ~/workspace/.int-wave/feat-inbound-connlog-design.md §3.1/§4/§5.3,
// unit U1: agent-gost collection).
//
// The hook lives in defaultService.Serve, the one accept loop every
// gost service shares: an admitted connection produces an "open"
// event where the peer address is final (a PROXY-protocol listener
// has already folded the real client address into RemoteAddr, so the
// collected IP is the real client IP with zero extra work), and a
// "close" event when Handler.Handle returns — the deterministic
// close point of the connection. Only services the panel marked as
// entry points are collected (see ConnLogEntryMetadataKey); hop legs
// (tunnel entrypoint services, chain/mux legs) are constructed
// without the mark and are never recorded, so a pooled or
// multiplexed leg can never masquerade as an inbound connection.
//
// Events land in a bounded in-memory ring buffer (this file) that
// the reporter unit (U2) drains. The hot path cost per connection
// is two clock reads plus one fixed-size struct copy under a short
// lock; the forwarding copy loops are untouched. A full buffer
// drops the oldest event and counts the drop — losing data under
// overload is acceptable, pretending otherwise is not (design §4.2).

// ConnLogEntryMetadataKey is the service-level metadata key that
// marks a service as a node entry point for connection logging.
// The panel (unit U7, buildForwardServiceConfigs) writes
// `"connlogEntry": true` into the metadata of the forward services
// it generates for managed relays; ParseService forwards it into
// NewService via ConnLogEntryOption. The spelling follows the
// existing panel metadata keys ("proxyProtocol", "idleTimeout",
// "interface").
const ConnLogEntryMetadataKey = "connlogEntry"

// ConnLogBufferCapacity is the in-memory ring buffer size (design
// §4.2 suggests 4096 events; the on-disk spool is U2's layer).
const ConnLogBufferCapacity = 4096

// ConnEventType discriminates the two event kinds of the §5.3
// ingest contract: "open" when a connection is admitted, "close"
// when it ends. The panel upserts by (entry node, ConnID): open
// creates the row, close completes it.
type ConnEventType string

const (
	ConnEventOpen  ConnEventType = "open"
	ConnEventClose ConnEventType = "close"
)

// Entry kinds and session kinds (design §5.1). Everything this
// unit emits is a gost entry service, hence EntryKind "forward";
// kernel inbounds ("inbound" / "kernel_session" / "event_only")
// are unit U4's vocabulary.
const (
	ConnEntryKindForward = "forward"

	ConnSessionKindTCP = "tcp_conn"    // true lifecycle: accept → Handle return
	ConnSessionKindUDP = "udp_session" // session semantics: first packet → idle-TTL teardown, TTL-level precision
)

// ConnEvent is one fixed-size connection log event, mirroring the
// §5.3 report contract fields. Close events repeat the open
// event's descriptive fields so a close whose open was dropped
// (buffer overflow) still carries everything the panel needs to
// materialise the row; DurationMs is deliberately absent — the
// panel derives it from EndMs-StartMs at ingest (design §5.1).
type ConnEvent struct {
	Type        ConnEventType
	ConnID      string // per-connection sid generated in Serve; unique per accepted connection
	EntryKind   string // always ConnEntryKindForward for gost services
	EntryID     string // service name (panel encoding carries the forward id)
	ClientIP    string // effective peer at the entry; the real client IP under PROXY-protocol receive
	ClientPort  int    // 0 when the peer address carries no port
	Network     string // "tcp" or "udp"
	SessionKind string // ConnSessionKindTCP or ConnSessionKindUDP
	StartMs     int64  // entry-machine clock, accept time (Unix milliseconds)
	EndMs       int64  // entry-machine clock, close time; 0 on open events
}

// ConnLogBuffer is the bounded ring buffer between the Serve hook
// (producer, one per connection) and the U2 reporter (consumer).
// Push never blocks: when full it overwrites the oldest event and
// increments the dropped counter U2 reports to the panel.
type ConnLogBuffer struct {
	mu      sync.Mutex
	ring    [ConnLogBufferCapacity]ConnEvent
	head    int // index of the oldest live event
	count   int
	dropped atomic.Uint64
}

func NewConnLogBuffer() *ConnLogBuffer {
	return &ConnLogBuffer{}
}

func (b *ConnLogBuffer) Push(ev ConnEvent) {
	b.mu.Lock()
	if b.count == ConnLogBufferCapacity {
		// Full: overwrite the oldest slot and advance head; the
		// overwritten event is lost and must be accounted.
		b.ring[b.head] = ev
		b.head = (b.head + 1) % ConnLogBufferCapacity
		b.mu.Unlock()
		b.dropped.Add(1)
		return
	}
	b.ring[(b.head+b.count)%ConnLogBufferCapacity] = ev
	b.count++
	b.mu.Unlock()
}

// Drain removes and returns up to max oldest events in FIFO order.
// max <= 0 drains everything currently buffered.
func (b *ConnLogBuffer) Drain(max int) []ConnEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := b.count
	if max > 0 && max < n {
		n = max
	}
	if n == 0 {
		return nil
	}
	out := make([]ConnEvent, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, b.ring[b.head])
		b.ring[b.head] = ConnEvent{}
		b.head = (b.head + 1) % ConnLogBufferCapacity
	}
	b.count -= n
	return out
}

func (b *ConnLogBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.count
}

// Dropped returns how many events the buffer has overwritten since
// process start. U2 attaches the counter to its batches so the
// panel can surface it (design §4.2).
func (b *ConnLogBuffer) Dropped() uint64 {
	return b.dropped.Load()
}

func (b *ConnLogBuffer) reset() {
	b.mu.Lock()
	b.head = 0
	b.count = 0
	b.mu.Unlock()
	b.dropped.Store(0)
}

var globalConnLogBuffer = NewConnLogBuffer()

// GetConnLogBuffer returns the process-wide buffer the Serve hook
// writes to and the U2 reporter drains.
func GetConnLogBuffer() *ConnLogBuffer {
	return globalConnLogBuffer
}

// connLogEnabled is the feature master switch for gost entry
// collection. Design §4.5: gost entries carry no per-inbound
// opt-in cost (unlike kernel inbounds), so collection follows the
// master switch and defaults to ON. Driven by the optional
// "connlog" key of the agent config.json (see LoadConfig) or by
// SetConnLogEnabled from the agent's control path.
var connLogEnabled atomic.Bool

func init() {
	connLogEnabled.Store(true)
}

func SetConnLogEnabled(enabled bool) {
	connLogEnabled.Store(enabled)
}

func ConnLogEnabled() bool {
	return connLogEnabled.Load()
}

// ConnLogEntryOption marks the service as a node entry point whose
// connections are recorded (subject to the master switch).
func ConnLogEntryOption(entry bool) Option {
	return func(opts *options) {
		opts.connLogEntry = entry
	}
}

// ConnLogEntryFromMetadata reports whether a service-level metadata
// map carries the entry mark. The key match is case-insensitive on
// purpose, mirroring internal/idle's "idleTimeout" handling: a
// gost.json loaded through viper arrives with lowercased map keys,
// so the panel-written "connlogEntry" may show up as "connlogentry"
// (F-212-A class of bug). Accepted truthy values: bool true, the
// strings "true"/"1", and non-zero numbers (JSON round-trips).
func ConnLogEntryFromMetadata(md map[string]any) bool {
	if len(md) == 0 {
		return false
	}
	if v, ok := md[ConnLogEntryMetadataKey]; ok {
		return connLogTruthy(v)
	}
	for k, v := range md {
		if strings.EqualFold(k, ConnLogEntryMetadataKey) {
			return connLogTruthy(v)
		}
	}
	return false
}

func connLogTruthy(v any) bool {
	switch vv := v.(type) {
	case bool:
		return vv
	case string:
		return vv == "1" || strings.EqualFold(vv, "true")
	case int:
		return vv != 0
	case int64:
		return vv != 0
	case float64:
		return vv != 0
	}
	return false
}

// connNetwork classifies an accepted connection as "tcp" or "udp"
// from its remote address. For the shared UDP listener each session
// surfaces as one accepted conn whose RemoteAddr is the session's
// client UDP address, so this also drives the session-kind split.
func connNetwork(conn net.Conn) string {
	if conn != nil {
		if ra := conn.RemoteAddr(); ra != nil && strings.HasPrefix(ra.Network(), "udp") {
			return "udp"
		}
	}
	return "tcp"
}

// newConnEvent builds one event for the hook. clientAddr/clientIP
// are the values Serve already finalised for the connection; the
// port is split out of clientAddr (0 when unparsable).
func (s *defaultService) newConnEvent(typ ConnEventType, sid, clientAddr, clientIP string, conn net.Conn, startMs, endMs int64) ConnEvent {
	network := connNetwork(conn)
	sessionKind := ConnSessionKindTCP
	if network == "udp" {
		sessionKind = ConnSessionKindUDP
	}
	port := 0
	if _, p, err := net.SplitHostPort(clientAddr); err == nil {
		port, _ = strconv.Atoi(p)
	}
	return ConnEvent{
		Type:        typ,
		ConnID:      sid,
		EntryKind:   ConnEntryKindForward,
		EntryID:     s.name,
		ClientIP:    clientIP,
		ClientPort:  port,
		Network:     network,
		SessionKind: sessionKind,
		StartMs:     startMs,
		EndMs:       endMs,
	}
}
