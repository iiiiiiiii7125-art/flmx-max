package kernel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Inbound connection log collection for proxy kernels (入站连接日志,
// design ~/workspace/.int-wave/feat-inbound-connlog-design.md
// §3.2/§3.3/§4/§5.3, unit U4: agent-side kernel log parsing).
//
// A direct (unrelayed) kernel inbound's entry point is the kernel
// process itself, so the only truthful source of connection events
// is the kernel's own log stream: sing-box at debug level emits an
// accept line and a terminal line per connection under a shared
// connection id, and Xray's access log emits one accept event per
// connection (Xray has no close event at all — the user ruled
// 2026-11-10 that Xray inbounds degrade to event-only rows: client
// IP + start time, duration shown as "—" by the panel).
//
// The parser here is a NEW surface on purpose: the troubleshooting
// logRing (200 lines, truncated, no timestamps) is not a collection
// pipeline and is never reused as one. Lines the parser recognises
// as per-connection events are diverted out of the ring (design
// §4.5: debug-level connection chatter would otherwise drown the
// troubleshooting tail), parsed in the agent — outside the kernel
// process, so the forwarding path sees zero added work — and turned
// into ConnEvents in the same buffer contract the gost side (unit
// U1, x/service) exposes to the reporter (unit U2).
//
// Truth rules (design §1/§3.3, do not relax):
//   - sing-box durations come from the kernel-reported elapsed in
//     the terminal line's "[id elapsed]" header, never from polling
//     or disappearance guesses.
//   - A sing-box connection is closed by its FIRST terminal signal:
//     the kernel's connection manager closes both conns when the
//     first copy direction finishes (route/conn.go, verified
//     against sing-box v1.12.8 and v1.14.4 sources), so "connection
//     upload/download finished", "… closed: <err>", the router's
//     "connection closed: <err>" and any connection-manager ERROR
//     all terminate the record.
//   - Connections still open when a kernel process generation ends
//     are swept as interrupted with the last known timestamp —
//     never a fabricated close time.
//   - Hysteria2/TUIC streams and every sing-box packet session are
//     proxy sessions, not carrier connections: SessionKind
//     kernel_session, never dressed up as tcp_conn.
//   - Xray: accepted access lines only. Rejected attempts never
//     formed a session and are not recorded; stats/online-list
//     heuristics are never consulted.

// ConnEventType discriminates the two event kinds of the §5.3
// ingest contract (same spelling as x/service).
type ConnEventType string

const (
	ConnEventOpen  ConnEventType = "open"
	ConnEventClose ConnEventType = "close"
)

// Entry/session kinds this unit emits (design §5.1). "inbound" is
// the kernel direct-entry kind; the gost relay-entry kind
// ("forward") belongs to unit U1.
const (
	ConnEntryKindInbound = "inbound"

	ConnSessionKindTCP       = "tcp_conn"       // sing-box stream on a TCP inbound: true lifecycle
	ConnSessionKindKernel    = "kernel_session" // sing-box packet session or QUIC-inbound stream: proxy-session lifecycle
	ConnSessionKindEventOnly = "event_only"     // Xray degraded row: accept event only, no close exists
)

// Close-event status values. Open events leave Status empty; a
// close either reports the kernel-observed end ("closed") or the
// generation-end sweep's honest unknown ("interrupted").
const (
	ConnStatusClosed      = "closed"
	ConnStatusInterrupted = "interrupted"
)

// ConnEvent is one fixed-size connection log event, field-identical
// to x/service's (the kernel package deliberately does not import
// the service stack; U2 maps both onto the §5.3 wire shape). Close
// events repeat the open event's descriptive fields so a close whose
// open was dropped still carries everything the panel needs;
// DurationMs is absent on purpose — the panel derives it from
// EndMs-StartMs at ingest (design §5.1). For kernel closes EndMs is
// StartMs + the kernel-reported elapsed, which is how the true
// duration survives the subtraction.
type ConnEvent struct {
	Type        ConnEventType
	ConnID      string // unique per (agent, process, generation, kernel connection); panel upserts by (entry node, ConnID)
	EntryKind   string // always ConnEntryKindInbound for kernel inbounds
	EntryID     string // kernel inbound tag, "in-<inbound id>"
	ClientIP    string // effective peer at the entry
	ClientPort  int    // 0 when the source address carries no port
	Network     string // "tcp" or "udp"
	SessionKind string // ConnSessionKindTCP / ConnSessionKindKernel / ConnSessionKindEventOnly
	StartMs     int64  // entry-machine clock, connection start (Unix milliseconds)
	EndMs       int64  // entry-machine clock, close time; 0 on open events
	Status      string // close events only: ConnStatusClosed or ConnStatusInterrupted
}

// ConnLogBufferCapacity is the in-memory ring size (design §4.2,
// same value as the gost side; the on-disk spool is U2's layer).
const ConnLogBufferCapacity = 4096

// ConnLogBuffer is the bounded ring buffer between the kernel log
// parser (producer) and the U2 reporter (consumer). Push never
// blocks: when full it overwrites the oldest event and counts the
// drop (design §4.2 — losing data under overload is acceptable,
// pretending otherwise is not).
type ConnLogBuffer struct {
	mu      sync.Mutex
	ring    [ConnLogBufferCapacity]ConnEvent
	head    int
	count   int
	dropped atomic.Uint64
}

func NewConnLogBuffer() *ConnLogBuffer {
	return &ConnLogBuffer{}
}

func (b *ConnLogBuffer) Push(ev ConnEvent) {
	b.mu.Lock()
	if b.count == ConnLogBufferCapacity {
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
// process start. U2 attaches the counter to its batches (design
// §4.2).
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

// GetConnLogBuffer returns the process-wide buffer the kernel log
// parser writes to and the U2 reporter drains.
func GetConnLogBuffer() *ConnLogBuffer {
	return globalConnLogBuffer
}

// connEventSink, when installed by the agent's composition root,
// receives every event the collectors produce in place of the
// package buffer. The kernel package deliberately does not import
// the service stack (the reporter's home); the sink is how the two
// planes meet without an import edge — main wires it to the
// reporter's EnqueueConnEvent. With no sink installed (standalone
// or test use), events land in GetConnLogBuffer() as before, and
// drop accounting stays with that buffer; with a sink installed,
// drops can only occur downstream in the reporter's ring/spool,
// whose counter is the single accounting point the panel sees.
var (
	connSinkMu sync.RWMutex
	connSink   func(ConnEvent)
)

// SetConnEventSink installs (or clears, with nil) the process-wide
// consumer for collected connection events.
func SetConnEventSink(fn func(ConnEvent)) {
	connSinkMu.Lock()
	connSink = fn
	connSinkMu.Unlock()
}

// emit routes one collected event to the sink when installed, else
// to the collector's buffer.
func (c *connLogCollector) emit(ev ConnEvent) {
	connSinkMu.RLock()
	fn := connSink
	connSinkMu.RUnlock()
	if fn != nil {
		fn(ev)
		return
	}
	c.buffer.Push(ev)
}

// connLogTags renders the opt-in inbound id list of an ApplyRequest
// into the tag set the parser filters on. Kernel inbound tags are
// always "in-<id>" (panel kernel model), so the id list and the
// tag set are the same information in two spellings.
func connLogTags(ids []int64) map[string]bool {
	if len(ids) == 0 {
		return nil
	}
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id > 0 {
			out["in-"+strconv.FormatInt(id, 10)] = true
		}
	}
	return out
}

// kernelConn is one sing-box connection being tracked within one
// process generation.
type kernelConn struct {
	connID      string // composed panel-facing id
	tag         string
	clientIP    string
	clientPort  int
	network     string
	sessionKind string
	startMs     int64
	lastSeenMs  int64
}

// connLogCollector parses one kernel instance's log streams into
// connection events. One collector belongs to one kernel instance
// and survives process relaunches. Connection ids are scoped per
// (process, generation): a per-inbound kernel runs several
// processes under one instance, and each launch restarts that
// process's id namespace, so both dimensions are part of every
// tracking key and of the composed ConnID (design §3.3).
type connLogCollector struct {
	kernelType string
	buffer     *ConnLogBuffer

	mu     sync.Mutex
	optIns map[string]bool
	gens   map[string]int64       // process name -> generation start (ms)
	open   map[string]*kernelConn // "<proc>\x00<raw id>" -> connection

	tailer *accessTailer
}

func newConnLogCollector(kernelType string, buffer *ConnLogBuffer) *connLogCollector {
	if buffer == nil {
		buffer = GetConnLogBuffer()
	}
	return &connLogCollector{
		kernelType: kernelType,
		buffer:     buffer,
		gens:       make(map[string]int64),
		open:       make(map[string]*kernelConn),
	}
}

// setOptIns replaces the opt-in tag set (Apply path). Parsing only
// happens while the set is non-empty.
func (c *connLogCollector) setOptIns(tags map[string]bool) {
	c.mu.Lock()
	c.optIns = tags
	c.mu.Unlock()
}

func (c *connLogCollector) active() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.optIns) > 0
}

func (c *connLogCollector) opted(tag string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.optIns[tag]
}

func openKey(proc, rawID string) string { return proc + "\x00" + rawID }

// beginGeneration marks a launch of the named process: its
// connection ids restart from a fresh namespace, and anything
// still open from its previous generation died with that process —
// sweep it as interrupted before the namespace rolls.
func (c *connLogCollector) beginGeneration(proc string, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweepLocked(proc)
	c.gens[proc] = at.UnixMilli()
}

// endGeneration marks an exit of the named process: every
// connection it still has open is swept as interrupted (§4.6).
func (c *connLogCollector) endGeneration(proc string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweepLocked(proc)
}

// sweepLocked emits interrupted closes for the named process's
// tracked connections. EndMs is the last timestamp the connection
// was actually seen at — the honest "last known" moment, never the
// sweep time dressed up as a disconnect time.
func (c *connLogCollector) sweepLocked(proc string) {
	prefix := proc + "\x00"
	for key, conn := range c.open {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		end := conn.lastSeenMs
		if end <= 0 {
			end = conn.startMs
		}
		c.emit(ConnEvent{
			Type:        ConnEventClose,
			ConnID:      conn.connID,
			EntryKind:   ConnEntryKindInbound,
			EntryID:     conn.tag,
			ClientIP:    conn.clientIP,
			ClientPort:  conn.clientPort,
			Network:     conn.network,
			SessionKind: conn.sessionKind,
			StartMs:     conn.startMs,
			EndMs:       end,
			Status:      ConnStatusInterrupted,
		})
		delete(c.open, key)
	}
}

func (c *connLogCollector) genStart(proc string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gens[proc]
}

func (c *connLogCollector) composeID(proc, rawID string) string {
	return "sb-" + proc + "-" + strconv.FormatInt(c.genStart(proc), 10) + "-" + rawID
}

// Feed consumes one raw kernel stdout line from the instance's
// unnamed stream (the Xray access tailer and tests). See FeedAs.
func (c *connLogCollector) Feed(line string, recvAt time.Time) bool {
	return c.FeedAs("", line, recvAt)
}

// FeedAs consumes one raw kernel output line produced by the named
// process. It returns true when the line was recognised as
// per-connection chatter and diverted (the caller then keeps it
// out of the troubleshooting logRing); unrecognised lines return
// false and flow to the ring untouched. recvAt is the agent-side
// receive time, the fallback clock for log formats without their
// own timestamp (design §4.7: event timestamps are taken at the
// entry machine).
func (c *connLogCollector) FeedAs(proc, line string, recvAt time.Time) bool {
	if !c.active() {
		return false
	}
	line = ansiEscape.ReplaceAllString(line, "")
	switch c.kernelType {
	case "singbox":
		return c.feedSingbox(proc, line, recvAt)
	case "xray":
		return c.feedXray(line, recvAt)
	}
	return false
}

// --- sing-box ----------------------------------------------------

// sbLineRe splits a sing-box log line: an optional "+0000
// 2026-11-09 21:25:33" timestamp prefix (log.timestamp rendering),
// the level, and the "[<conn id> <elapsed>]" connection-context
// header. Lines without the header are not per-connection lines.
var sbLineRe = regexp.MustCompile(`^(?:([+-]\d{4}) (\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}) )?(TRACE|DEBUG|INFO|WARN|ERROR|PANIC|FATAL) \[(\d+) ([^\] ]+)\] (.*)$`)

// sbInboundRe extracts type and tag from an "inbound/<type>[<tag>]"
// subsystem name.
var sbInboundRe = regexp.MustCompile(`^inbound/([^/\[]+)\[(.+)\]$`)

// quicInboundTypes are the sing-box inbound types whose streams
// ride a QUIC carrier: each stream is a proxy session, not a TCP
// connection (design §3.3).
var quicInboundTypes = map[string]bool{
	"tuic": true, "hysteria": true, "hysteria2": true,
}

func parseSbTimestamp(zone, datetime string) (int64, bool) {
	t, err := time.Parse("-0700 2006-01-02 15:04:05", zone+" "+datetime)
	if err != nil {
		return 0, false
	}
	return t.UnixMilli(), true
}

// parseSbElapsed decodes sing-box's FormatDuration (log/format.go,
// verified against v1.12.8/v1.14.4 sources): integer "123ms" below
// one second; "<sec>.<hundredths>s" below one minute with the
// hundredths printed UNPADDED (so "2.5s" means 2.05s, not 2.5s —
// a plain time.ParseDuration would misread it by 450ms); and
// "<min>m<sec>s" above one minute.
func parseSbElapsed(s string) (int64, bool) {
	if strings.HasSuffix(s, "ms") {
		if n, err := strconv.ParseInt(strings.TrimSuffix(s, "ms"), 10, 64); err == nil {
			return n, true
		}
		return 0, false
	}
	if i := strings.IndexByte(s, 'm'); i > 0 && strings.HasSuffix(s, "s") && !strings.Contains(s, ".") {
		mins, err1 := strconv.ParseInt(s[:i], 10, 64)
		secs, err2 := strconv.ParseInt(s[i+1:len(s)-1], 10, 64)
		if err1 == nil && err2 == nil {
			return mins*60000 + secs*1000, true
		}
		return 0, false
	}
	if strings.HasSuffix(s, "s") {
		body := s[:len(s)-1]
		if dot := strings.IndexByte(body, '.'); dot >= 0 {
			secs, err1 := strconv.ParseInt(body[:dot], 10, 64)
			frac, err2 := strconv.ParseInt(body[dot+1:], 10, 64)
			if err1 == nil && err2 == nil && frac >= 0 && frac <= 99 {
				return secs*1000 + frac*10, true
			}
			return 0, false
		}
		if secs, err := strconv.ParseInt(body, 10, 64); err == nil {
			return secs * 1000, true
		}
	}
	if d, err := time.ParseDuration(s); err == nil {
		return d.Milliseconds(), true
	}
	return 0, false
}

func splitHostPort(addr string) (string, int) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return addr, 0
	}
	port, _ := strconv.Atoi(portStr)
	return host, port
}

func (c *connLogCollector) feedSingbox(proc, line string, recvAt time.Time) bool {
	m := sbLineRe.FindStringSubmatch(line)
	if m == nil {
		return false
	}
	lineMs := recvAt.UnixMilli()
	if m[1] != "" {
		if ms, ok := parseSbTimestamp(m[1], m[2]); ok {
			lineMs = ms
		}
	}
	level, rawID, elapsedText, rest := m[3], m[4], m[5], m[6]
	idx := strings.Index(rest, ": ")
	if idx <= 0 {
		return false
	}
	subsystem, msg := rest[:idx], rest[idx+2:]
	elapsedMs, elapsedOK := parseSbElapsed(elapsedText)

	switch {
	case strings.HasPrefix(subsystem, "inbound/"):
		return c.sbInboundLine(proc, subsystem, msg, rawID, lineMs, elapsedMs, elapsedOK)
	case subsystem == "connection":
		return c.sbConnectionLine(proc, msg, level, rawID, lineMs, elapsedMs, elapsedOK)
	case strings.HasPrefix(subsystem, "outbound/"):
		// Per-connection chatter ("outbound connection to …"):
		// divert from the ring, never an event anchor.
		c.touch(proc, rawID, lineMs)
		return true
	}
	return false
}

func (c *connLogCollector) touch(proc, rawID string, lineMs int64) {
	c.mu.Lock()
	if conn, ok := c.open[openKey(proc, rawID)]; ok && lineMs > conn.lastSeenMs {
		conn.lastSeenMs = lineMs
	}
	c.mu.Unlock()
}

// sbInboundLine handles lines logged under the inbound's own
// subsystem: the open anchors and the handshake/router terminal
// lines live here.
func (c *connLogCollector) sbInboundLine(proc, subsystem, msg, rawID string, lineMs, elapsedMs int64, elapsedOK bool) bool {
	im := sbInboundRe.FindStringSubmatch(subsystem)
	if im == nil {
		return false
	}
	inboundType, tag := im[1], im[2]

	switch {
	case strings.HasPrefix(msg, "inbound packet connection from "):
		c.sbOpen(proc, tag, inboundType, true, strings.TrimPrefix(msg, "inbound packet connection from "), rawID, lineMs)
	case strings.HasPrefix(msg, "inbound connection from "):
		c.sbOpen(proc, tag, inboundType, false, strings.TrimPrefix(msg, "inbound connection from "), rawID, lineMs)
	case strings.HasPrefix(msg, "process connection from "),
		strings.HasPrefix(msg, "process packet connection from "):
		// Handshake-stage failure (bad auth, undecodable stream):
		// the connection is over. The line carries the source
		// address itself, so it closes a tracked connection and
		// can stand alone when the open line was never seen. The
		// packet form is the packet inbound's counterpart of the
		// stream form; both go through sbTerminal with the
		// inbound's own shape (D-06).
		packet := strings.HasPrefix(msg, "process packet connection from ")
		addr := msg
		if packet {
			addr = strings.TrimPrefix(addr, "process packet connection from ")
		} else {
			addr = strings.TrimPrefix(addr, "process connection from ")
		}
		if i := strings.Index(addr, ": "); i >= 0 {
			addr = addr[:i]
		}
		c.sbTerminal(proc, tag, inboundType, packet, addr, rawID, lineMs, elapsedMs, elapsedOK)
	case strings.HasPrefix(msg, "connection closed"):
		// Router-level terminal line (closed/canceled/rejected
		// class). Carries no address: only meaningful for a
		// tracked connection.
		c.sbCloseTracked(proc, rawID, lineMs, elapsedMs, elapsedOK)
	default:
		// Destination-side chatter ("inbound connection to …",
		// the target address is never recorded, design §8) and
		// bare packet lines: divert, no event.
		c.touch(proc, rawID, lineMs)
	}
	return true
}

// sbConnectionLine handles lines under the "connection" subsystem
// (the kernel's connection manager): copy-direction end lines and
// every fatal connection error. These lines carry no tag — the
// connection id is the only attribution key.
func (c *connLogCollector) sbConnectionLine(proc, msg, level, rawID string, lineMs, elapsedMs int64, elapsedOK bool) bool {
	switch {
	case msg == "connection upload finished" || msg == "connection download finished" ||
		msg == "packet upload finished" || msg == "packet download finished" ||
		strings.HasPrefix(msg, "connection upload closed:") || strings.HasPrefix(msg, "connection download closed:") ||
		strings.HasPrefix(msg, "packet upload closed:") || strings.HasPrefix(msg, "packet download closed:"):
		// First finisher wins: the kernel closes both directions
		// when the first copy ends, so the first of these lines
		// is the connection's end; the peer direction's line
		// (when it is logged at all) finds nothing tracked.
		c.sbCloseTracked(proc, rawID, lineMs, elapsedMs, elapsedOK)
	case level == "ERROR":
		// Dial failure ("open connection to …"), handshake and
		// payload failures: all fatal for the connection.
		c.sbCloseTracked(proc, rawID, lineMs, elapsedMs, elapsedOK)
	default:
		c.touch(proc, rawID, lineMs)
	}
	return true
}

// sbSessionShape derives the wire shape of a sing-box inbound
// connection from its type and packet-ness — the single source of
// truth sbOpen and sbTerminal both follow (truth rule: QUIC-inbound
// streams and packet sessions are kernel_session, never dressed up
// as tcp_conn).
func sbSessionShape(inboundType string, packet bool) (network, sessionKind string) {
	network, sessionKind = "tcp", ConnSessionKindTCP
	if packet {
		network, sessionKind = "udp", ConnSessionKindKernel
	} else if quicInboundTypes[inboundType] {
		sessionKind = ConnSessionKindKernel
	}
	return network, sessionKind
}

// sbOpen records a sing-box connection start and emits the open
// event. Packet sessions and QUIC-inbound streams are kernel
// sessions; plain streams are true TCP connections.
func (c *connLogCollector) sbOpen(proc, tag, inboundType string, packet bool, addr, rawID string, lineMs int64) {
	if !c.opted(tag) {
		return
	}
	host, port := splitHostPort(strings.TrimSpace(addr))
	network, sessionKind := sbSessionShape(inboundType, packet)
	conn := &kernelConn{
		connID:      c.composeID(proc, rawID),
		tag:         tag,
		clientIP:    host,
		clientPort:  port,
		network:     network,
		sessionKind: sessionKind,
		startMs:     lineMs,
		lastSeenMs:  lineMs,
	}
	key := openKey(proc, rawID)
	c.mu.Lock()
	if old, clash := c.open[key]; clash {
		// Connection-id reuse inside one generation (the ids are
		// random uint32s, collisions are a when-not-if at scale):
		// the previous holder produced no terminal line we saw,
		// so close it honestly as interrupted instead of letting
		// the new connection inherit or corrupt its record.
		end := old.lastSeenMs
		if end <= 0 {
			end = old.startMs
		}
		c.emit(ConnEvent{
			Type: ConnEventClose, ConnID: old.connID, EntryKind: ConnEntryKindInbound,
			EntryID: old.tag, ClientIP: old.clientIP, ClientPort: old.clientPort,
			Network: old.network, SessionKind: old.sessionKind,
			StartMs: old.startMs, EndMs: end, Status: ConnStatusInterrupted,
		})
	}
	c.open[key] = conn
	c.mu.Unlock()
	c.emit(ConnEvent{
		Type:        ConnEventOpen,
		ConnID:      conn.connID,
		EntryKind:   ConnEntryKindInbound,
		EntryID:     tag,
		ClientIP:    host,
		ClientPort:  port,
		Network:     network,
		SessionKind: sessionKind,
		StartMs:     lineMs,
	})
}

func (c *connLogCollector) sbCloseTracked(proc, rawID string, lineMs, elapsedMs int64, elapsedOK bool) {
	key := openKey(proc, rawID)
	c.mu.Lock()
	conn, ok := c.open[key]
	if ok {
		delete(c.open, key)
	}
	c.mu.Unlock()
	if !ok {
		return
	}
	c.pushClose(conn, lineMs, elapsedMs, elapsedOK)
}

// sbTerminal closes a connection from a line that carries the tag
// and the source address itself ("process connection from …" /
// "process packet connection from …"). A tracked connection closes
// normally; an untracked one still produces a stand-alone close (the
// panel materialises the row from the close alone, per the U1
// contract) with its start derived from the line's own timestamp and
// elapsed. The stand-alone row takes the inbound's own shape via
// sbSessionShape (D-06): a QUIC-inbound stream or a packet session
// is kernel_session, never a hard-coded tcp_conn.
func (c *connLogCollector) sbTerminal(proc, tag, inboundType string, packet bool, addr, rawID string, lineMs, elapsedMs int64, elapsedOK bool) {
	key := openKey(proc, rawID)
	c.mu.Lock()
	conn, ok := c.open[key]
	if ok {
		delete(c.open, key)
	}
	c.mu.Unlock()
	if ok {
		c.pushClose(conn, lineMs, elapsedMs, elapsedOK)
		return
	}
	if !c.opted(tag) || addr == "" {
		return
	}
	host, port := splitHostPort(strings.TrimSpace(addr))
	startMs := lineMs
	if elapsedOK {
		startMs = lineMs - elapsedMs
	}
	network, sessionKind := sbSessionShape(inboundType, packet)
	c.emit(ConnEvent{
		Type:        ConnEventClose,
		ConnID:      c.composeID(proc, rawID),
		EntryKind:   ConnEntryKindInbound,
		EntryID:     tag,
		ClientIP:    host,
		ClientPort:  port,
		Network:     network,
		SessionKind: sessionKind,
		StartMs:     startMs,
		EndMs:       lineMs,
		Status:      ConnStatusClosed,
	})
}

// pushClose emits the close event for a tracked connection. The
// end is start + the kernel-reported elapsed when the terminal
// line carried one (the true lifecycle, design §3.3); otherwise
// the terminal line's own timestamp.
func (c *connLogCollector) pushClose(conn *kernelConn, lineMs, elapsedMs int64, elapsedOK bool) {
	endMs := lineMs
	if elapsedOK {
		endMs = conn.startMs + elapsedMs
	}
	c.emit(ConnEvent{
		Type:        ConnEventClose,
		ConnID:      conn.connID,
		EntryKind:   ConnEntryKindInbound,
		EntryID:     conn.tag,
		ClientIP:    conn.clientIP,
		ClientPort:  conn.clientPort,
		Network:     conn.network,
		SessionKind: conn.sessionKind,
		StartMs:     conn.startMs,
		EndMs:       endMs,
		Status:      ConnStatusClosed,
	})
}

// --- Xray --------------------------------------------------------

// xrAccessRe matches an Xray access-log line:
// "2026/11/09 21:39:56.020145 from tcp:127.0.0.1:55038 accepted
// tcp:127.0.0.1:18080 [in-201 >> direct]". The source network
// prefix is optional (rejected vless lines omit it); the tag
// bracket only appears on accepted lines.
var xrAccessRe = regexp.MustCompile(`^(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}(?:\.\d+)?) from (?:(tcp|udp):)?(\S+?) (accepted|rejected)(?:\s|$)`)

var xrTagRe = regexp.MustCompile(`\[([^\]\s]+)\s*>>\s*[^\]]*\]`)

func parseXrayTimestamp(text string) (int64, bool) {
	t, err := time.ParseInLocation("2006/01/02 15:04:05.999999999", text, time.Local)
	if err != nil {
		return 0, false
	}
	return t.UnixMilli(), true
}

func (c *connLogCollector) feedXray(line string, recvAt time.Time) bool {
	m := xrAccessRe.FindStringSubmatch(line)
	if m == nil {
		return false
	}
	// An access-log line: divert it (it is a connection event
	// record, not troubleshooting output). Rejected attempts
	// never formed a session and produce no event (design §3.1
	// v1 rule, applied to this stack too).
	if m[4] != "accepted" {
		return true
	}
	tm := xrTagRe.FindStringSubmatch(line)
	if tm == nil {
		return true
	}
	tag := tm[1]
	if !c.opted(tag) {
		return true
	}
	startMs := recvAt.UnixMilli()
	if ms, ok := parseXrayTimestamp(m[1]); ok {
		startMs = ms
	}
	network := m[2]
	if network == "" {
		network = "tcp"
	}
	host, port := splitHostPort(m[3])
	// Xray access lines carry no connection id; derive a stable
	// one from the line's own content so a re-read of the same
	// line (access-file tailer restart) replays to the same
	// panel row instead of double-recording.
	sum := sha256.Sum256([]byte(tag + "|" + m[3] + "|" + m[1] + "|" + line))
	c.emit(ConnEvent{
		Type:        ConnEventOpen,
		ConnID:      "xr-" + hex.EncodeToString(sum[:12]),
		EntryKind:   ConnEntryKindInbound,
		EntryID:     tag,
		ClientIP:    host,
		ClientPort:  port,
		Network:     network,
		SessionKind: ConnSessionKindEventOnly,
		StartMs:     startMs,
	})
	return true
}

// --- Xray access-log file tailer ---------------------------------
//
// U5's rendering decides where Xray's access log goes. When it is
// a file inside the kernel's run directory, stdout never carries
// the lines, so the collector tails the file it was configured
// with (the path is read from the rendered config itself — no
// guessed convention). When the access log is rendered onto
// stdout, the process line tap feeds the same parser and no
// tailer starts.

// xrayAccessPath extracts log.access from a rendered xray
// config.json. "" means no access log is configured; "stdout"/
// "/dev/stdout"/"none" mean the lines do not live in a run-dir
// file.
func xrayAccessPath(files []File) string {
	for _, f := range files {
		if f.Name != "config.json" {
			continue
		}
		var cfg struct {
			Log struct {
				Access string `json:"access"`
			} `json:"log"`
		}
		if err := json.Unmarshal([]byte(f.Content), &cfg); err != nil {
			return ""
		}
		return strings.TrimSpace(cfg.Log.Access)
	}
	return ""
}

type accessTailer struct {
	path string
	stop chan struct{}
	done chan struct{}
}

func startAccessTailer(path string, feed func(string, time.Time) bool) *accessTailer {
	t := &accessTailer{path: path, stop: make(chan struct{}), done: make(chan struct{})}
	go t.run(feed)
	return t
}

func (t *accessTailer) close() {
	select {
	case <-t.stop:
	default:
		close(t.stop)
	}
	<-t.done
}

func (t *accessTailer) run(feed func(string, time.Time) bool) {
	defer close(t.done)
	// Start at the end of the file: history from before the agent
	// attached is not replayed (connection events are live data;
	// ancient accepts would land as garbage rows).
	var offset int64
	if fi, err := os.Stat(t.path); err == nil {
		offset = fi.Size()
	}
	var partial string
	read := func() {
		fi, err := os.Stat(t.path)
		if err != nil {
			return
		}
		if fi.Size() < offset {
			offset = 0 // truncated or rotated: start over
			partial = ""
		}
		if fi.Size() == offset {
			return
		}
		f, err := os.Open(t.path)
		if err != nil {
			return
		}
		defer f.Close()
		if _, err := f.Seek(offset, 0); err != nil {
			return
		}
		buf := make([]byte, fi.Size()-offset)
		n, _ := f.Read(buf)
		if n <= 0 {
			return
		}
		offset += int64(n)
		data := partial + string(buf[:n])
		lines := strings.Split(data, "\n")
		partial = lines[len(lines)-1]
		if len(partial) > 1<<20 {
			partial = "" // a line that never ends is not a log line
		}
		now := time.Now()
		for _, ln := range lines[:len(lines)-1] {
			if ln = strings.TrimRight(ln, "\r"); ln != "" {
				feed(ln, now)
			}
		}
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-t.stop:
			read() // final drain before going away
			return
		case <-ticker.C:
			read()
		}
	}
}

// ensureTailer starts the access-file tailer when the rendered
// xray config points log.access at a file inside the run
// directory, and stops it when the config stops doing so.
func (c *connLogCollector) ensureTailer(runDir string, files []File) {
	if c.kernelType != "xray" {
		return
	}
	access := xrayAccessPath(files)
	var path string
	switch access {
	case "", "none", "stdout", "/dev/stdout":
		path = ""
	default:
		if filepath.IsAbs(access) {
			path = filepath.Clean(access)
			// Only tail files inside the kernel run directory:
			// the config is panel-rendered, but the agent never
			// follows it to arbitrary host paths.
			if rel, err := filepath.Rel(runDir, path); err != nil || strings.HasPrefix(rel, "..") {
				path = ""
			}
		} else {
			path = filepath.Join(runDir, access)
		}
	}
	c.mu.Lock()
	current := ""
	if c.tailer != nil {
		current = c.tailer.path
	}
	c.mu.Unlock()
	if current == path {
		return
	}
	c.stopTailer()
	if path == "" {
		return
	}
	t := startAccessTailer(path, c.Feed)
	c.mu.Lock()
	c.tailer = t
	c.mu.Unlock()
}

func (c *connLogCollector) stopTailer() {
	c.mu.Lock()
	t := c.tailer
	c.tailer = nil
	c.mu.Unlock()
	if t != nil {
		t.close()
	}
}
