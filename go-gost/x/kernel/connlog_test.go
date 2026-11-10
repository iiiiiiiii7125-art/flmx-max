package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Sing-box fixture lines below are in the real LEVEL[uptime]
// [connID elapsed] shape (LIVE-215 true-machine 1.14.4 lines pin
// the grammar; see TestSingboxReal1144Lines). The pre-fix fixtures
// carried the fabricated bare "LEVEL [id elapsed]" shape (no
// [uptime] bracket, never emitted by any sing-box version — see
// sbLineRe) and were migrated by inserting the [0000] uptime
// bracket the real logger always emits; ids, elapsed values,
// subsystems and messages are unchanged. The Xray lines are
// untouched (out of scope for this fix).
const (
	sbOpenSlow   = "+0000 2026-11-09 21:25:34 INFO[0000] [431050131 0ms] inbound/socks[in-101]: inbound connection from 127.0.0.1:54754"
	sbToSlow     = "+0000 2026-11-09 21:25:34 INFO[0000] [431050131 0ms] inbound/socks[in-101]: inbound connection to 127.0.0.1:18080"
	sbDownFin    = "+0000 2026-11-09 21:25:36 DEBUG[0000] [431050131 2.60s] connection: connection download finished"
	sbUpFin      = "+0000 2026-11-09 21:25:36 DEBUG[0000] [431050131 2.65s] connection: connection upload finished"
	sbOpenAuth   = "+0000 2026-11-09 21:25:35 INFO[0000] [3060665359 0ms] inbound/socks[in-101]: inbound connection from 127.0.0.1:57134"
	sbAuthFail   = "+0000 2026-11-09 21:25:35 ERROR[0000] [3060665359 0ms] inbound/socks[in-101]: process connection from 127.0.0.1:57134: socks5: authentication failed, username=u1, password=hunter2pw"
	sbOpenRefuse = "+0000 2026-11-09 21:25:35 INFO[0000] [4140354012 0ms] inbound/socks[in-101]: inbound connection from 127.0.0.1:57144"
	sbRefused    = "+0000 2026-11-09 21:25:35 ERROR[0000] [4140354012 3ms] connection: open connection to 127.0.0.1:19999 using outbound/direct[direct]: dial tcp 127.0.0.1:19999: connect: connection refused"
	sbOpenSS     = "+0000 2026-11-09 21:25:35 INFO[0000] [2867101150 0ms] inbound/shadowsocks[in-102]: inbound connection from 127.0.0.1:39386"
	sbSSFail     = "+0000 2026-11-09 21:25:35 ERROR[0000] [2867101150 0ms] inbound/shadowsocks[in-102]: process connection from 127.0.0.1:39386: shadowsocks: serve TCP from 127.0.0.1:39386: cipher: message authentication failed"
	sbOpenPkt    = "+0000 2026-11-09 21:25:36 INFO[0000] [2803896598 0ms] inbound/socks[in-101]: inbound packet connection from 127.0.0.1:40000"
	sbPktFin     = "+0000 2026-11-09 21:25:38 DEBUG[0000] [2803896598 2.60s] connection: packet download finished"

	xrAccepted = "2026/11/09 21:39:56.020145 from tcp:127.0.0.1:55038 accepted tcp:127.0.0.1:18080 [in-201 >> direct]"
	xrRejected = "2026/11/09 21:39:59.236063 from tcp:127.0.0.1:55040 rejected  proxy/socks: invalid username or password"
	xrRejectV  = "2026/11/09 21:40:01.227131 from 127.0.0.1:45848 rejected  proxy/vless/encoding: invalid request user id: 00000000-0000-0000-0000-000000000000"
)

func utcMs(sec int) int64 {
	return time.Date(2026, 11, 9, 21, 25, sec, 0, time.UTC).UnixMilli()
}

func newTestCollector(kernelType string, ids ...int64) (*connLogCollector, *ConnLogBuffer) {
	buf := NewConnLogBuffer()
	c := newConnLogCollector(kernelType, buf)
	c.setOptIns(connLogTags(ids))
	return c, buf
}

// LIVE-215 true-machine captured lines (sing-box 1.14.4, grid 2A:
// agent log ring on node 1, 2026-11-11). Verbatim, except the
// finished line: the 2A evidence prints it as
// "DEBUG[0025] [834714304 10.0s] connection upload/download finished"
// (slash = either direction, subsystem prefix elided in the prose);
// a literal slash line has no "tag: message" split and cannot occur
// in sing-box output, so it is restored to the source-verified form
// "connection: connection upload finished" (route/conn.go, per U4;
// same form as sbUpFin). Level, uptime, id and elapsed are verbatim.
const (
	sbReal1144OpenSS  = "INFO[0015] [834714304 0ms] inbound/shadowsocks[in-69]: inbound connection from 127.0.0.1:30304"
	sbReal1144Fin     = "DEBUG[0025] [834714304 10.0s] connection: connection upload finished"
	sbReal1144OpenHy2 = "INFO[0026] [978362404 0ms] inbound/hysteria2[in-70]: inbound packet connection from 127.0.0.1:44244"
)

// TestSingboxReal1144Lines pins the parser to the sing-box 1.14.4
// lines the LIVE-215 true machine actually emitted (grid 2A, red 2):
// LEVEL[uptime] [connID elapsed], no wall-clock prefix. The old
// grammar (LEVEL [connID elapsed], no [uptime] bracket) never
// matched these lines, so the whole direct sing-box path was blind.
func TestSingboxReal1144Lines(t *testing.T) {
	c, buf := newTestCollector("singbox", 69, 70)
	c.beginGeneration("singbox", time.UnixMilli(utcMs(30)))
	now := time.UnixMilli(utcMs(40))
	if !c.FeedAs("singbox", sbReal1144OpenSS, now) {
		t.Fatal("real 1.14.4 SS open line not consumed")
	}
	if !c.FeedAs("singbox", sbReal1144Fin, now) {
		t.Fatal("real 1.14.4 finished line not consumed")
	}
	if !c.FeedAs("singbox", sbReal1144OpenHy2, now) {
		t.Fatal("real 1.14.4 Hy2 packet open line not consumed")
	}
	evs := buf.Drain(0)
	if len(evs) != 3 {
		t.Fatalf("events = %d, want 3 (SS open+close, Hy2 open): %+v", len(evs), evs)
	}
	open, cls, hy2 := evs[0], evs[1], evs[2]
	if open.Type != ConnEventOpen || open.EntryID != "in-69" || open.ClientIP != "127.0.0.1" ||
		open.ClientPort != 30304 || open.Network != "tcp" || open.SessionKind != ConnSessionKindTCP ||
		open.StartMs != utcMs(40) {
		t.Errorf("SS open event wrong: %+v", open)
	}
	if cls.Type != ConnEventClose || cls.ConnID != open.ConnID || cls.Status != ConnStatusClosed ||
		cls.StartMs != utcMs(40) || cls.EndMs != utcMs(40)+10000 {
		t.Errorf("SS close event wrong (kernel-reported 10.0s elapsed): %+v", cls)
	}
	if hy2.Type != ConnEventOpen || hy2.EntryID != "in-70" || hy2.ClientIP != "127.0.0.1" ||
		hy2.ClientPort != 44244 || hy2.Network != "udp" || hy2.SessionKind != ConnSessionKindKernel {
		t.Errorf("Hy2 packet open event wrong: %+v", hy2)
	}
}

func TestParseSbElapsed(t *testing.T) {
	cases := map[string]int64{
		"0ms": 0, "3ms": 3, "999ms": 999,
		"2.5s": 2050, "2.50s": 2500, "2.60s": 2600, "2.65s": 2650, "45s": 45000,
		"1m30s": 90000, "2m5s": 125000,
	}
	for in, want := range cases {
		got, ok := parseSbElapsed(in)
		if !ok || got != want {
			t.Errorf("parseSbElapsed(%q) = %d,%v; want %d,true", in, got, ok, want)
		}
	}
	if _, ok := parseSbElapsed("soon"); ok {
		t.Error("parseSbElapsed(soon) should fail")
	}
}

func TestSingboxPairing(t *testing.T) {
	c, buf := newTestCollector("singbox", 101)
	c.beginGeneration("singbox", time.UnixMilli(utcMs(30)))
	now := time.UnixMilli(utcMs(40))
	if !c.FeedAs("singbox", sbOpenSlow, now) {
		t.Fatal("open line not consumed")
	}
	if !c.FeedAs("singbox", sbToSlow, now) {
		t.Fatal("to line not consumed")
	}
	if !c.FeedAs("singbox", sbDownFin, now) {
		t.Fatal("download finished not consumed")
	}
	if !c.FeedAs("singbox", sbUpFin, now) {
		t.Fatal("upload finished not consumed")
	}
	evs := buf.Drain(0)
	if len(evs) != 2 {
		t.Fatalf("events = %d, want 2 (open+close): %+v", len(evs), evs)
	}
	open, cls := evs[0], evs[1]
	if open.Type != ConnEventOpen || open.EntryID != "in-101" || open.ClientIP != "127.0.0.1" ||
		open.ClientPort != 54754 || open.Network != "tcp" || open.SessionKind != ConnSessionKindTCP ||
		open.StartMs != utcMs(34) || open.EndMs != 0 || open.EntryKind != ConnEntryKindInbound {
		t.Errorf("open event wrong: %+v", open)
	}
	if cls.Type != ConnEventClose || cls.ConnID != open.ConnID || cls.Status != ConnStatusClosed ||
		cls.StartMs != utcMs(34) || cls.EndMs != utcMs(34)+2600 {
		t.Errorf("close event wrong (duration must be the kernel-reported 2600ms): %+v", cls)
	}
}

func TestSingboxErrorCloses(t *testing.T) {
	now := time.UnixMilli(utcMs(40))

	// Handshake failure under the inbound subsystem.
	c, buf := newTestCollector("singbox", 101, 102)
	c.beginGeneration("singbox", time.UnixMilli(utcMs(30)))
	c.FeedAs("singbox", sbOpenAuth, now)
	c.FeedAs("singbox", sbAuthFail, now)
	evs := buf.Drain(0)
	if len(evs) != 2 || evs[1].Type != ConnEventClose || evs[1].Status != ConnStatusClosed || evs[1].EndMs != evs[1].StartMs {
		t.Fatalf("auth-fail pair wrong: %+v", evs)
	}

	// Dial failure under the connection subsystem.
	c, buf = newTestCollector("singbox", 101)
	c.beginGeneration("singbox", time.UnixMilli(utcMs(30)))
	c.FeedAs("singbox", sbOpenRefuse, now)
	c.FeedAs("singbox", sbRefused, now)
	evs = buf.Drain(0)
	if len(evs) != 2 || evs[1].Type != ConnEventClose || evs[1].EndMs != evs[1].StartMs+3 {
		t.Fatalf("refused pair wrong: %+v", evs)
	}

	// A stand-alone process-connection failure (open line never
	// seen) still materialises a close.
	c, buf = newTestCollector("singbox", 102)
	c.beginGeneration("singbox", time.UnixMilli(utcMs(30)))
	c.FeedAs("singbox", sbSSFail, now)
	evs = buf.Drain(0)
	if len(evs) != 1 || evs[0].Type != ConnEventClose || evs[0].EntryID != "in-102" ||
		evs[0].ClientIP != "127.0.0.1" || evs[0].ClientPort != 39386 || evs[0].Status != ConnStatusClosed {
		t.Fatalf("stand-alone close wrong: %+v", evs)
	}
}

// D-06：握手失败的独立终结行（open 行未见）不得硬编码
// tcp/tcp_conn——QUIC 入站（tuic/hysteria2）流与包会话按
// truth rule 是 kernel_session；包入站还是 udp。已跟踪连接经
// pushClose 带正确 kind 的路径作回归对照。
func TestSingboxTerminalKinds(t *testing.T) {
	now := time.UnixMilli(utcMs(40))
	c, buf := newTestCollector("singbox", 101, 103, 104)
	c.beginGeneration("singbox", time.UnixMilli(utcMs(30)))
	// Stand-alone handshake-failure terminals (open line never seen).
	c.FeedAs("singbox", "+0000 2026-11-09 21:25:35 ERROR[0000] [777001 0ms] inbound/tuic[in-103]: process connection from 10.0.0.9:5000: tuic: handshake failed", now)
	c.FeedAs("singbox", "+0000 2026-11-09 21:25:35 ERROR[0000] [777002 0ms] inbound/hysteria2[in-104]: process connection from 10.0.0.10:5001: hysteria2: handshake failed", now)
	c.FeedAs("singbox", "+0000 2026-11-09 21:25:35 ERROR[0000] [777003 0ms] inbound/socks[in-101]: process packet connection from 127.0.0.1:40000: packet: invalid header", now)
	evs := buf.Drain(0)
	if len(evs) != 3 {
		t.Fatalf("events = %d, want 3 stand-alone closes: %+v", len(evs), evs)
	}
	for _, ev := range evs {
		if ev.Type != ConnEventClose || ev.Status != ConnStatusClosed {
			t.Fatalf("stand-alone terminal must be a closed close: %+v", ev)
		}
	}
	if evs[0].EntryID != "in-103" || evs[0].ClientIP != "10.0.0.9" || evs[0].ClientPort != 5000 ||
		evs[0].Network != "tcp" || evs[0].SessionKind != ConnSessionKindKernel {
		t.Errorf("tuic terminal must be a tcp kernel_session, never tcp_conn: %+v", evs[0])
	}
	if evs[1].EntryID != "in-104" || evs[1].ClientIP != "10.0.0.10" || evs[1].ClientPort != 5001 ||
		evs[1].Network != "tcp" || evs[1].SessionKind != ConnSessionKindKernel {
		t.Errorf("hysteria2 terminal must be a tcp kernel_session, never tcp_conn: %+v", evs[1])
	}
	if evs[2].EntryID != "in-101" || evs[2].ClientIP != "127.0.0.1" || evs[2].ClientPort != 40000 ||
		evs[2].Network != "udp" || evs[2].SessionKind != ConnSessionKindKernel {
		t.Errorf("packet terminal must be a udp kernel_session, never tcp_conn: %+v", evs[2])
	}

	// Tracked QUIC stream closed by its terminal line keeps kernel_session.
	c2, buf2 := newTestCollector("singbox", 103)
	c2.beginGeneration("singbox", time.UnixMilli(utcMs(30)))
	c2.FeedAs("singbox", "+0000 2026-11-09 21:25:34 INFO[0000] [555 0ms] inbound/tuic[in-103]: inbound connection from 10.0.0.9:5000", now)
	c2.FeedAs("singbox", "+0000 2026-11-09 21:25:35 ERROR[0000] [555 0ms] inbound/tuic[in-103]: process connection from 10.0.0.9:5000: tuic: handshake failed", now)
	evs = buf2.Drain(0)
	if len(evs) != 2 || evs[1].Type != ConnEventClose || evs[1].SessionKind != ConnSessionKindKernel {
		t.Fatalf("tracked tuic terminal wrong: %+v", evs)
	}
}

func TestSingboxGenerationIsolation(t *testing.T) {
	c, buf := newTestCollector("singbox", 101)
	now := time.UnixMilli(utcMs(40))
	open111 := "+0000 2026-11-09 21:25:34 INFO[0000] [111 0ms] inbound/socks[in-101]: inbound connection from 127.0.0.1:50001"

	// Generation 1: a connection left open at process exit is
	// swept as interrupted with its last known timestamp.
	c.beginGeneration("singbox", time.UnixMilli(1000))
	c.FeedAs("singbox", open111, now)
	c.endGeneration("singbox")
	evs := buf.Drain(0)
	if len(evs) != 2 || evs[1].Type != ConnEventClose || evs[1].Status != ConnStatusInterrupted ||
		evs[1].EndMs != utcMs(34) {
		t.Fatalf("generation sweep wrong: %+v", evs)
	}
	gen1ID := evs[0].ConnID

	// Generation 2: the same raw id is a different connection.
	c.beginGeneration("singbox", time.UnixMilli(2000))
	c.FeedAs("singbox", open111, now)
	c.FeedAs("singbox", "+0000 2026-11-09 21:25:36 DEBUG[0000] [111 1.20s] connection: connection download finished", now)
	evs = buf.Drain(0)
	if len(evs) != 2 || evs[0].ConnID == gen1ID || evs[1].Status != ConnStatusClosed || evs[1].EndMs != utcMs(34)+1200 {
		t.Fatalf("generation 2 pairing wrong: %+v (gen1 id %s)", evs, gen1ID)
	}

	// Same-generation id reuse: the second open interrupts the
	// first instead of corrupting it.
	c2, buf2 := newTestCollector("singbox", 101)
	c2.beginGeneration("singbox", time.UnixMilli(3000))
	c2.FeedAs("singbox", "+0000 2026-11-09 21:25:34 INFO[0000] [222 0ms] inbound/socks[in-101]: inbound connection from 127.0.0.1:50002", now)
	c2.FeedAs("singbox", "+0000 2026-11-09 21:25:35 INFO[0000] [222 0ms] inbound/socks[in-101]: inbound connection from 127.0.0.1:50003", now)
	evs = buf2.Drain(0)
	if len(evs) != 3 || evs[1].Type != ConnEventClose || evs[1].Status != ConnStatusInterrupted ||
		evs[1].ClientPort != 50002 || evs[2].Type != ConnEventOpen || evs[2].ClientPort != 50003 {
		t.Fatalf("id-reuse handling wrong: %+v", evs)
	}
}

func TestSingboxPacketAndQuicKinds(t *testing.T) {
	c, buf := newTestCollector("singbox", 101, 103)
	c.beginGeneration("singbox", time.UnixMilli(utcMs(30)))
	now := time.UnixMilli(utcMs(40))
	c.FeedAs("singbox", sbOpenPkt, now)
	c.FeedAs("singbox", sbPktFin, now)
	c.FeedAs("singbox", "+0000 2026-11-09 21:25:36 INFO[0000] [555 0ms] inbound/tuic[in-103]: inbound connection from 10.0.0.9:5000", now)
	evs := buf.Drain(0)
	if len(evs) != 3 {
		t.Fatalf("events = %d, want 3: %+v", len(evs), evs)
	}
	if evs[0].SessionKind != ConnSessionKindKernel || evs[0].Network != "udp" {
		t.Errorf("packet open must be a udp kernel_session: %+v", evs[0])
	}
	if evs[1].Type != ConnEventClose || evs[1].EndMs != evs[1].StartMs+2600 {
		t.Errorf("packet close wrong: %+v", evs[1])
	}
	if evs[2].SessionKind != ConnSessionKindKernel || evs[2].Network != "tcp" {
		t.Errorf("tuic stream must be kernel_session (never tcp_conn): %+v", evs[2])
	}
}

func TestSingboxOptInFiltering(t *testing.T) {
	c, buf := newTestCollector("singbox", 101)
	c.beginGeneration("singbox", time.UnixMilli(utcMs(30)))
	now := time.UnixMilli(utcMs(40))
	// Non-opted inbound: lines divert, no events.
	if !c.FeedAs("singbox", sbOpenSS, now) {
		t.Error("non-opted open line should still be diverted")
	}
	c.FeedAs("singbox", sbSSFail, now)
	if n := buf.Len(); n != 0 {
		t.Fatalf("non-opted inbound produced %d events", n)
	}
	// Not a per-connection line: not diverted.
	if c.FeedAs("singbox", "+0000 2026-11-09 21:25:36 INFO inbound/socks: started", now) {
		t.Error("bracket-less line must not be diverted")
	}
	// Collection switched off entirely: nothing is diverted.
	c.setOptIns(nil)
	if c.FeedAs("singbox", sbOpenSlow, now) {
		t.Error("inactive collector must not divert")
	}
	// Switched back on without any process restart (the
	// same-hash Apply case): parsing follows immediately.
	c.setOptIns(connLogTags([]int64{101}))
	if !c.FeedAs("singbox", sbOpenSlow, now) || buf.Len() != 1 {
		t.Error("reactivated collector must parse again")
	}
}

func TestSingboxLineShapes(t *testing.T) {
	c, buf := newTestCollector("singbox", 101)
	c.beginGeneration("singbox", time.UnixMilli(utcMs(30)))
	// ANSI-wrapped variant (colorised rendering): the connection
	// id and level arrive wrapped in escapes and must still parse.
	wrapped := "+0000 2026-11-09 21:25:33 \x1b[36mINFO[0000]\x1b[0m [\x1b[38;5;38m4294444415\x1b[0m 0ms] inbound/socks[in-101]: inbound connection from 127.0.0.1:54750"
	if !c.FeedAs("singbox", wrapped, time.UnixMilli(utcMs(40))) {
		t.Fatal("ANSI-wrapped line not consumed")
	}
	// No timestamp prefix (log.timestamp=false): recvAt is the clock.
	plain := "INFO[0000] [777 0ms] inbound/socks[in-101]: inbound connection from 127.0.0.1:40001"
	recv := time.UnixMilli(123456789)
	if !c.FeedAs("singbox", plain, recv) {
		t.Fatal("timestamp-less line not consumed")
	}
	evs := buf.Drain(0)
	if len(evs) != 2 || evs[0].ClientPort != 54750 || evs[1].StartMs != 123456789 {
		t.Fatalf("line-shape events wrong: %+v", evs)
	}
}

func TestXrayEventOnly(t *testing.T) {
	c, buf := newTestCollector("xray", 201)
	now := time.Now()
	if !c.Feed(xrAccepted, now) {
		t.Fatal("accepted line not consumed")
	}
	evs := buf.Drain(0)
	if len(evs) != 1 {
		t.Fatalf("events = %d, want 1: %+v", len(evs), evs)
	}
	ev := evs[0]
	wantStart := time.Date(2026, 11, 9, 21, 39, 56, 20*1000*1000, time.Local).UnixMilli()
	if ev.Type != ConnEventOpen || ev.SessionKind != ConnSessionKindEventOnly || ev.EndMs != 0 ||
		ev.Status != "" || ev.EntryID != "in-201" || ev.ClientIP != "127.0.0.1" || ev.ClientPort != 55038 ||
		ev.Network != "tcp" || ev.StartMs != wantStart || !strings.HasPrefix(ev.ConnID, "xr-") {
		t.Errorf("event_only shape wrong: %+v (want start %d)", ev, wantStart)
	}
	// Deterministic id: replaying the same line replays to the
	// same panel row.
	c.Feed(xrAccepted, now)
	evs = buf.Drain(0)
	if len(evs) != 1 || evs[0].ConnID != ev.ConnID {
		t.Fatalf("replay id not deterministic: %+v vs %s", evs, ev.ConnID)
	}
	// Rejected attempts: diverted, no events (both source shapes).
	if !c.Feed(xrRejected, now) || !c.Feed(xrRejectV, now) {
		t.Error("rejected lines should be diverted")
	}
	if n := buf.Len(); n != 0 {
		t.Fatalf("rejected lines produced %d events", n)
	}
	// Non-access output is not diverted.
	if c.Feed("2026/11/09 21:39:56.000000 [Info] infra/conf: converter: converting config", now) {
		t.Error("non-access line must not be diverted")
	}
	// Non-opted tag: diverted, no event.
	c2, buf2 := newTestCollector("xray", 999)
	c2.Feed(xrAccepted, now)
	if n := buf2.Len(); n != 0 {
		t.Fatalf("non-opted xray tag produced %d events", n)
	}
}

func TestConnLogBufferDropAccounting(t *testing.T) {
	buf := NewConnLogBuffer()
	for i := 0; i < ConnLogBufferCapacity+10; i++ {
		buf.Push(ConnEvent{ConnID: string(rune('a' + i%26)), StartMs: int64(i)})
	}
	if buf.Len() != ConnLogBufferCapacity || buf.Dropped() != 10 {
		t.Fatalf("len=%d dropped=%d, want %d/10", buf.Len(), buf.Dropped(), ConnLogBufferCapacity)
	}
	out := buf.Drain(1)
	if len(out) != 1 || out[0].StartMs != 10 {
		t.Fatalf("oldest surviving event should be #10, got %+v", out)
	}
}

func TestXrayAccessPath(t *testing.T) {
	files := []File{{Name: "config.json", Content: `{"log":{"access":"access.log","loglevel":"warning"}}`}}
	if got := xrayAccessPath(files); got != "access.log" {
		t.Errorf("access path = %q, want access.log", got)
	}
	if got := xrayAccessPath([]File{{Name: "config.json", Content: `{"log":{"loglevel":"warning"}}`}}); got != "" {
		t.Errorf("missing access = %q, want empty", got)
	}
	if got := xrayAccessPath([]File{{Name: "config.json", Content: `{`}}); got != "" {
		t.Errorf("broken json = %q, want empty", got)
	}
}

func TestXrayAccessTailer(t *testing.T) {
	dir := t.TempDir()
	access := filepath.Join(dir, "access.log")
	history := "2026/11/09 20:00:00.000001 from tcp:127.0.0.1:40000 accepted tcp:127.0.0.1:18080 [in-201 >> direct]\n"
	if err := os.WriteFile(access, []byte(history), 0o600); err != nil {
		t.Fatal(err)
	}
	c, buf := newTestCollector("xray", 201)
	c.ensureTailer(dir, []File{{Name: "config.json", Content: `{"log":{"access":"access.log"}}`}})
	defer c.stopTailer()
	// History from before the tailer attached is not replayed.
	time.Sleep(400 * time.Millisecond)
	if n := buf.Len(); n != 0 {
		t.Fatalf("tailer replayed history: %d events", n)
	}
	f, err := os.OpenFile(access, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(xrAccepted + "\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if buf.Len() == 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	evs := buf.Drain(0)
	if len(evs) != 1 || evs[0].SessionKind != ConnSessionKindEventOnly || evs[0].EntryID != "in-201" {
		t.Fatalf("tailer events wrong: %+v", evs)
	}
	// stdout-rendered access: no tailer.
	c.ensureTailer(dir, []File{{Name: "config.json", Content: `{"log":{"access":"/dev/stdout"}}`}})
	c.mu.Lock()
	tailed := c.tailer != nil
	c.mu.Unlock()
	if tailed {
		t.Error("tailer must stop when access moves to stdout")
	}
}

func TestApplyConnLogTagsValidation(t *testing.T) {
	req := ApplyRequest{Type: "singbox", Version: "1.12.8", Download: &Download{BinName: "sing-box", Archive: "raw"}, ConnLogInbounds: []int64{0}}
	if err := req.validate(); err == nil {
		t.Error("connLogInbounds with id 0 must be rejected")
	}
	req.ConnLogInbounds = []int64{101}
	if err := req.validate(); err != nil {
		t.Errorf("valid connLogInbounds rejected: %v", err)
	}
}

func TestConnlogEffectiveOptIns(t *testing.T) {
	entries := []ConnlogEntry{{InboundID: 7, Tag: "in-7"}, {InboundID: 8, Tag: "in-8"}, {InboundID: 9, Tag: "in-9"}}
	cases := []struct {
		name  string
		optIn []int64
		want  []int64
	}{
		{"both lists agree", []int64{7, 9}, []int64{7, 9}},
		{"opt-in without topology qualification is dropped", []int64{7, 42}, []int64{7}},
		{"topology without opt-in collects nothing", nil, nil},
		{"opt-in with empty topology collects nothing", []int64{7}, nil},
	}
	for _, tc := range cases {
		req := ApplyRequest{ConnLogInbounds: tc.optIn}
		if tc.name != "opt-in with empty topology collects nothing" {
			req.ConnlogEntries = entries
		}
		got := connlogEffectiveOptIns(req)
		if len(got) != len(tc.want) {
			t.Fatalf("%s: connlogEffectiveOptIns = %v, want %v", tc.name, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("%s: connlogEffectiveOptIns = %v, want %v", tc.name, got, tc.want)
			}
		}
	}
}
