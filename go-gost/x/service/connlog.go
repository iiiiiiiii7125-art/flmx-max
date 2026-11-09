package service

import (
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Inbound connection log (入站连接日志, design
// ~/workspace/.int-wave/feat-inbound-connlog-design.md §3.1/§4/§5.3,
// units U1+U2, unified at integration).
//
// Data path: producers — the gost service-layer capture hook in
// defaultService.Serve (U1) and the kernel log parser (U4, via the
// sink wired in the agent's main) — hand events to the reporter
// through EnqueueConnEvent. The reporter (connlog_reporter.go)
// drains its bounded ring into the on-disk spool and batch-posts to
// the panel's POST /flow/connlog/v2; the panel upserts by
// (entry node, ConnID): open creates the row, close completes it.
//
// Capture discipline: only services the panel marked as entry
// points produce events (the mark is parsed at service build time,
// see config/parsing/service and internal/connlog); hop legs are
// constructed without the mark and are never recorded, so a pooled
// or multiplexed leg can never masquerade as an inbound connection.
// The hot path cost per connection is two clock reads plus one
// fixed-size struct copy into the ring under a short lock; the
// forwarding copy loops are untouched. A full ring drops the oldest
// event and counts the drop — losing data under overload is
// acceptable, pretending otherwise is not (design §4.2).

// Event types of the §5.3 ingest contract: "open" when a
// connection is admitted, "close" when it ends.
const (
	ConnEventOpen  = "open"
	ConnEventClose = "close"
)

// Close-event status values (design §5.1). Open events leave
// Status empty; a close either reports the observed end ("closed")
// or an honest unknown end swept at a generation boundary
// ("interrupted") — a sweep time must never pose as a real
// disconnect time.
const (
	ConnStatusClosed      = "closed"
	ConnStatusInterrupted = "interrupted"
)

// Entry kinds (design §5.1): gost entry services are "forward";
// kernel direct inbounds are "inbound" (emitted by unit U4).
const (
	ConnEntryForward = "forward"
	ConnEntryInbound = "inbound"
)

// Session kinds (design §5.1). The gost hook emits the first two;
// the kernel parser's vocabulary (kernel_session / event_only) is
// declared here as well so the wire contract has one spelling.
const (
	ConnSessionKindTCP       = "tcp_conn"       // true lifecycle: accept → Handle return
	ConnSessionKindUDP       = "udp_session"    // session semantics: first packet → idle-TTL teardown, TTL-level precision
	ConnSessionKindKernel    = "kernel_session" // sing-box packet session or QUIC-inbound stream: proxy-session lifecycle
	ConnSessionKindEventOnly = "event_only"     // Xray degraded row: accept event only, no close exists
)

// ConnEvent is one fixed-size connection log event, in the §5.3
// wire shape (the panel handler's decode is the contract's fixed
// point; field names are camelCase like every other panel-facing
// payload of this feature). Close events repeat the open event's
// descriptive fields so a close whose open was dropped (ring
// overflow) still carries everything the panel needs to
// materialise the row; duration is deliberately absent — the panel
// derives it from EndMs-StartMs at ingest (design §5.1). Times are
// entry-machine clock Unix milliseconds (design §4.7).
type ConnEvent struct {
	Type        string `json:"type"`                 // open | close
	ConnID      string `json:"connId"`               // per-connection id, unique per accepted connection; panel upserts by (entry node, ConnID)
	EntryKind   string `json:"entryKind"`            // forward | inbound
	EntryID     string `json:"entryId"`              // forward service name (panel encoding carries the forward id) / inbound tag in-<ID>
	ClientIP    string `json:"clientIp"`             // effective peer at the entry; the real client IP under PROXY-protocol receive
	PeerIP      string `json:"peerIp,omitempty"`     // raw previous-hop address when the stack exposes both (gost entries leave it empty)
	ClientPort  int    `json:"clientPort,omitempty"` // 0 when the peer address carries no port
	Network     string `json:"network"`              // tcp | udp
	SessionKind string `json:"sessionKind"`          // tcp_conn | udp_session | kernel_session | event_only
	StartMs     int64  `json:"startTime"`            // connection start (entry-machine clock, milliseconds)
	EndMs       int64  `json:"endTime,omitempty"`    // close time; for interrupted closes the last known moment
	Status      string `json:"status,omitempty"`     // close events only: closed | interrupted
}

// connRing is the bounded in-memory ring between the producers and
// the reporter (design §4.2 first level): fixed size, short lock,
// full → overwrite the oldest and report the drop. Push never
// blocks and never fails.
type connRing struct {
	mu    sync.Mutex
	buf   []ConnEvent
	head  int // index of the oldest live event
	count int
}

func newConnRing(capacity int) *connRing {
	if capacity <= 0 {
		capacity = 1
	}
	return &connRing{buf: make([]ConnEvent, capacity)}
}

// Push enqueues one event; when full it drops the oldest event and
// returns true (the caller accumulates the drop count).
func (r *connRing) Push(ev ConnEvent) (droppedOldest bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.count == len(r.buf) {
		r.buf[r.head] = ev
		r.head = (r.head + 1) % len(r.buf)
		return true
	}
	r.buf[(r.head+r.count)%len(r.buf)] = ev
	r.count++
	return false
}

// Drain removes and returns all buffered events in FIFO order.
func (r *connRing) Drain() []ConnEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.count == 0 {
		return nil
	}
	out := make([]ConnEvent, 0, r.count)
	for i := 0; i < r.count; i++ {
		out = append(out, r.buf[(r.head+i)%len(r.buf)])
	}
	r.head = 0
	r.count = 0
	return out
}

func (r *connRing) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count
}

// connLogEnabled is the agent-side master switch for gost entry
// collection. Design §4.5: gost entries carry no per-inbound opt-in
// cost (unlike kernel inbounds), so collection follows the master
// switch and defaults to ON. Driven by the optional "connlog" key
// of the agent config.json (see LoadConfig) or by
// SetConnLogEnabled from the agent's control path. The panel-side
// master switch (repo.ConnLogEnabledConfigKey) acts upstream of
// this one: it stops the entry marks themselves from being issued.
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

// newConnEvent builds one event for the Serve hook. clientAddr and
// clientIP are the values Serve already finalised for the
// connection; the port is split out of clientAddr (0 when
// unparsable).
func (s *defaultService) newConnEvent(typ, sid, clientAddr, clientIP string, conn net.Conn, startMs, endMs int64) ConnEvent {
	network := connNetwork(conn)
	sessionKind := ConnSessionKindTCP
	if network == "udp" {
		sessionKind = ConnSessionKindUDP
	}
	port := 0
	if _, p, err := net.SplitHostPort(clientAddr); err == nil {
		port, _ = strconv.Atoi(p)
	}
	status := ""
	if typ == ConnEventClose {
		status = ConnStatusClosed
	}
	return ConnEvent{
		Type:        typ,
		ConnID:      sid,
		EntryKind:   ConnEntryForward,
		EntryID:     s.name,
		ClientIP:    clientIP,
		ClientPort:  port,
		Network:     network,
		SessionKind: sessionKind,
		StartMs:     startMs,
		EndMs:       endMs,
		Status:      status,
	}
}
