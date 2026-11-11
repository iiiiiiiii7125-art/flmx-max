package net

import (
	"bytes"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gost/x/internal/idle"
	"github.com/go-gost/x/internal/reap"
)

func closeWrite(t *testing.T, c net.Conn) {
	t.Helper()
	cw, ok := c.(interface{ CloseWrite() error })
	if !ok {
		t.Fatalf("%T has no CloseWrite", c)
	}
	if err := cw.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite: %v", err)
	}
}

// memEnd is an in-memory io.ReadWriter + io.Closer for trackedRW
// unit tests: reads drain rbuf, writes append to wbuf.
type memEnd struct {
	rbuf   bytes.Buffer
	wbuf   bytes.Buffer
	closes atomic.Int32
}

func (m *memEnd) Read(p []byte) (int, error)  { return m.rbuf.Read(p) }
func (m *memEnd) Write(p []byte) (int, error) { return m.wbuf.Write(p) }
func (m *memEnd) Close() error                { m.closes.Add(1); return nil }

func TestDirectionCreditingReadSideOnly(t *testing.T) {
	act := newSessionActivityDirs(0, true)
	end := &memEnd{}
	end.rbuf.WriteString("hello")
	ws := &trackedRW{rw: end, act: act, dir: 0}

	buf := make([]byte, 16)
	n, err := ws.Read(buf)
	if err != nil || n != 5 {
		t.Fatalf("Read = (%d, %v), want (5, nil)", n, err)
	}
	if got := act.bytesFrom[0].Load(); got != 5 {
		t.Fatalf("bytesFrom[client] = %d, want 5 after read", got)
	}
	if got := act.bytesFrom[1].Load(); got != 0 {
		t.Fatalf("bytesFrom[server] = %d, want 0", got)
	}

	// A Write moves bytes but must NOT direction-credit: the byte
	// was already credited when read from the sibling endpoint.
	// (This is also what makes a wrapper that misreports its
	// write count unable to forge direction progress.)
	if _, err := ws.Write([]byte("world")); err != nil {
		t.Fatalf("Write err = %v", err)
	}
	if got := act.bytesFrom[0].Load(); got != 5 {
		t.Fatalf("bytesFrom[client] = %d after Write, want still 5 (read-side only)", got)
	}

	// With direction tracking off, reads credit nothing.
	act2 := newSessionActivityDirs(0, false)
	end2 := &memEnd{}
	end2.rbuf.WriteString("hello")
	ws2 := &trackedRW{rw: end2, act: act2, dir: 0}
	if _, err := ws2.Read(buf); err != nil {
		t.Fatalf("Read err = %v", err)
	}
	if got := act2.bytesFrom[0].Load(); got != 0 {
		t.Fatalf("bytesFrom with trackDirs off = %d, want 0", got)
	}
}

func TestDirectionCreditingSplice(t *testing.T) {
	// WriteTo: t is the source wrapper — the segment's bytes are
	// credited to t's endpoint, once per segment return.
	t.Run("WriteTo credits source endpoint", func(t *testing.T) {
		act := newSessionActivityDirs(0, true)
		clientSide, transportClient := tcpPair(t)
		defer clientSide.Close()
		backendSide, transportBackend := tcpPair(t)
		defer backendSide.Close()

		ws := &trackedRW{rw: transportClient, act: act, tcp: true, dir: 0}
		wd := &trackedRW{rw: transportBackend, act: act, tcp: true, dir: 1}

		if _, err := clientSide.Write([]byte("ping")); err != nil {
			t.Fatalf("client write: %v", err)
		}
		closeWrite(t, clientSide)
		n, err := ws.WriteTo(wd)
		if err != nil {
			t.Fatalf("WriteTo err = %v", err)
		}
		if n != 4 {
			t.Fatalf("WriteTo moved %d bytes, want 4", n)
		}
		if got := act.bytesFrom[0].Load(); got != 4 {
			t.Fatalf("bytesFrom[client] = %d, want 4 (segment credit)", got)
		}
		if got := act.bytesFrom[1].Load(); got != 0 {
			t.Fatalf("bytesFrom[server] = %d, want 0", got)
		}
		got := make([]byte, 4)
		if _, err := io.ReadFull(backendSide, got); err != nil || string(got) != "ping" {
			t.Fatalf("backend received %q, err %v; want \"ping\"", got, err)
		}
	})

	// ReadFrom: t is the destination wrapper, r is the source —
	// the bytes are credited to the SOURCE's endpoint.
	t.Run("ReadFrom credits source endpoint", func(t *testing.T) {
		act := newSessionActivityDirs(0, true)
		clientSide, transportClient := tcpPair(t)
		defer clientSide.Close()
		backendSide, transportBackend := tcpPair(t)
		defer backendSide.Close()
		_ = backendSide

		ws := &trackedRW{rw: transportClient, act: act, tcp: true, dir: 0}
		wd := &trackedRW{rw: transportBackend, act: act, tcp: true, dir: 1}

		if _, err := clientSide.Write([]byte("pong")); err != nil {
			t.Fatalf("client write: %v", err)
		}
		closeWrite(t, clientSide)
		n, err := wd.ReadFrom(ws)
		if err != nil {
			t.Fatalf("ReadFrom err = %v", err)
		}
		if n != 4 {
			t.Fatalf("ReadFrom moved %d bytes, want 4", n)
		}
		if got := act.bytesFrom[0].Load(); got != 4 {
			t.Fatalf("bytesFrom[client] = %d, want 4 (source credit)", got)
		}
		if got := act.bytesFrom[1].Load(); got != 0 {
			t.Fatalf("bytesFrom[server] = %d, want 0", got)
		}
	})
}

// fakeEndpoint is a sampler-test endpoint: the sampler only ever
// closes endpoints (abortive teardown), so Read/Write are inert.
type fakeEndpoint struct {
	closes atomic.Int32
}

func (f *fakeEndpoint) Read(p []byte) (int, error)  { return 0, io.EOF }
func (f *fakeEndpoint) Write(p []byte) (int, error) { return len(p), nil }
func (f *fakeEndpoint) Close() error                { f.closes.Add(1); return nil }

// scriptedProbe replays a fixed TCPInfo series, then keeps
// returning the last sample.
type scriptedProbe struct {
	series []*tcpInfo
	idx    int
}

func (s *scriptedProbe) next() (*tcpInfo, error) {
	if s.idx < len(s.series) {
		v := s.series[s.idx]
		s.idx++
		return v, nil
	}
	return s.series[len(s.series)-1], nil
}

func newEdgeEntry(act *sessionActivity, edgeWin time.Duration, probe func() (*tcpInfo, error)) (*reapEntry, *fakeEndpoint, *fakeEndpoint) {
	rw1, rw2 := &fakeEndpoint{}, &fakeEndpoint{}
	return &reapEntry{
		act: act, rw1: rw1, rw2: rw2, role: reapRoleEdge,
		edgeWin: edgeWin, probe: probe, service: "test", born: time.Now(),
	}, rw1, rw2
}

func silentAct(base time.Time) *sessionActivity {
	act := newSessionActivityDirs(0, true)
	act.lastFrom[0].Store(base.UnixNano())
	act.lastFrom[1].Store(base.UnixNano())
	return act
}

// F3 §4.3 case 1: a live quiet game client — reads constantly,
// sends rarely. Its ACKs advance whenever traffic flows and its
// window stays open, so the consumption conjunction never holds.
func TestEdgeQuietGameClientSafe(t *testing.T) {
	base := time.Now()
	act := silentAct(base)
	var acked uint64
	probe := func() (*tcpInfo, error) {
		acked += 512 // every server packet the client receives is ACKed
		return &tcpInfo{State: tcpStateEstablished, Unacked: 0, NotsentBytes: 0, SndWnd: 65535, BytesAcked: acked, BytesSent: acked}, nil
	}
	e, rw1, _ := newEdgeEntry(act, 600*time.Second, probe)
	for k := 1; k <= 8; k++ {
		e.sampleEdge(base.Add(time.Duration(k) * 30 * time.Second))
	}
	if act.reapReason.Load() != reapReasonNone || rw1.closes.Load() != 0 {
		t.Fatalf("quiet game client was reaped (reason=%d, closes=%d)", act.reapReason.Load(), rw1.closes.Load())
	}

	// Fully quiet stretch: nothing is ever offered, so nothing is
	// pending either — the conjunction's evidence clause fails.
	act2 := silentAct(base)
	still := &tcpInfo{State: tcpStateEstablished, Unacked: 0, NotsentBytes: 0, SndWnd: 65535, BytesAcked: 4096, BytesSent: 4096}
	e2, rw12, _ := newEdgeEntry(act2, 600*time.Second, func() (*tcpInfo, error) { return still, nil })
	for k := 1; k <= 8; k++ {
		e2.sampleEdge(base.Add(time.Duration(k) * 30 * time.Second))
	}
	if act2.reapReason.Load() != reapReasonNone || rw12.closes.Load() != 0 {
		t.Fatalf("idle-but-healthy client was reaped (reason=%d, closes=%d)", act2.reapReason.Load(), rw12.closes.Load())
	}
}

// F3 §4.3 case 2 (edge half): a long download — the client sends
// nothing for hours but consumes continuously, so Bytes_acked
// advances every tick and the streak can never form.
func TestEdgeDownloadSafe(t *testing.T) {
	base := time.Now()
	act := silentAct(base)
	var acked uint64
	probe := func() (*tcpInfo, error) {
		acked += 1 << 20
		return &tcpInfo{State: tcpStateEstablished, Unacked: 8, NotsentBytes: 0, SndWnd: 65535, BytesAcked: acked, BytesSent: acked}, nil
	}
	e, rw1, _ := newEdgeEntry(act, 600*time.Second, probe)
	for k := 1; k <= 10; k++ {
		e.sampleEdge(base.Add(time.Duration(k) * 30 * time.Second))
	}
	if act.reapReason.Load() != reapReasonNone || rw1.closes.Load() != 0 {
		t.Fatalf("downloading client was reaped (reason=%d, closes=%d)", act.reapReason.Load(), rw1.closes.Load())
	}
}

// F3 §4.3 case 5: a hung application on a live machine — the
// kernel ACKs at first, then the receive buffer fills, the
// window shuts, and Bytes_acked freezes while data stands
// unacked. Exactly the ECS1686 zombie shape, reaped at the edge.
func TestEdgeHungAppReaped(t *testing.T) {
	base := time.Now()
	act := silentAct(base)
	hung := &tcpInfo{State: tcpStateEstablished, Unacked: 3, NotsentBytes: 5000, SndWnd: 0, BytesAcked: 12345, BytesSent: 17345}
	probe := &scriptedProbe{series: []*tcpInfo{hung}}
	e, rw1, rw2 := newEdgeEntry(act, 600*time.Second, probe.next)

	// Tick 1 primes prevAcked (no verdict without a previous
	// sample); ticks 2..4 build the streak of 3; the reap lands
	// on tick 4.
	for k := 1; k <= 3; k++ {
		e.sampleEdge(base.Add(600*time.Second + time.Duration(k)*30*time.Second))
		if rw1.closes.Load() != 0 {
			t.Fatalf("reaped early at tick %d (streak %d), want reap at tick 4", k, e.failStreak)
		}
	}
	e.sampleEdge(base.Add(600*time.Second + 4*30*time.Second))
	if act.reapReason.Load() != reapReasonEdge {
		t.Fatalf("reapReason = %d, want edge (%d)", act.reapReason.Load(), reapReasonEdge)
	}
	if rw1.closes.Load() != 1 || rw2.closes.Load() != 1 {
		t.Fatalf("endpoint closes = (%d, %d), want (1, 1)", rw1.closes.Load(), rw2.closes.Load())
	}
	if !act.tornDown.Load() {
		t.Fatal("session not marked tornDown after reap")
	}
}

// A single tick of ACK progress in the middle of a failing
// series resets the streak: the evidence must be continuous.
func TestEdgeStreakResetByAckProgress(t *testing.T) {
	base := time.Now()
	act := silentAct(base)
	hung := &tcpInfo{State: tcpStateEstablished, Unacked: 3, NotsentBytes: 5000, SndWnd: 0, BytesAcked: 1000, BytesSent: 6000}
	progress := &tcpInfo{State: tcpStateEstablished, Unacked: 3, NotsentBytes: 5000, SndWnd: 0, BytesAcked: 1001, BytesSent: 6001}
	// After the progress tick, consumption freezes again at the
	// new level (Bytes_acked is monotone on a real socket).
	hungAfter := &tcpInfo{State: tcpStateEstablished, Unacked: 3, NotsentBytes: 5000, SndWnd: 0, BytesAcked: 1001, BytesSent: 6001}
	probe := &scriptedProbe{series: []*tcpInfo{hung, hung, hung, progress, hungAfter, hungAfter}}
	e, rw1, _ := newEdgeEntry(act, 600*time.Second, probe.next)

	at := func(k int) time.Time { return base.Add(600*time.Second + time.Duration(k)*30*time.Second) }
	e.sampleEdge(at(1)) // prime
	e.sampleEdge(at(2)) // fail 1
	e.sampleEdge(at(3)) // fail 2
	e.sampleEdge(at(4)) // ACK advanced -> streak reset
	if e.failStreak != 0 {
		t.Fatalf("failStreak = %d after ACK progress, want 0", e.failStreak)
	}
	e.sampleEdge(at(5)) // fail 1
	e.sampleEdge(at(6)) // fail 2
	if rw1.closes.Load() != 0 {
		t.Fatal("reaped without 3 consecutive failing samples")
	}
	e.sampleEdge(at(7)) // fail 3 (script exhausted -> last sample repeats)
	if act.reapReason.Load() != reapReasonEdge {
		t.Fatal("not reaped after 3 consecutive failing samples post-reset")
	}
}

// F3 §4.3 case 6 — admitted trade, documented: a client asleep
// or on a dead network for longer than the edge window plus the
// streak, with a server drip pending, presents byte-identical
// evidence to the hung-app corpse (case 5) for the entire
// observation window and IS reaped. The window sizing guidance
// (>= 900s) exists to push this past realistic sleep scales.
func TestEdgeAsleepClientReapedAdmittedTrade(t *testing.T) {
	base := time.Now()
	act := silentAct(base)
	asleep := &tcpInfo{State: tcpStateEstablished, Unacked: 1, NotsentBytes: 200, SndWnd: 0, BytesAcked: 777, BytesSent: 977}
	probe := &scriptedProbe{series: []*tcpInfo{asleep}}
	e, rw1, _ := newEdgeEntry(act, 900*time.Second, probe.next)
	for k := 1; k <= 4; k++ {
		e.sampleEdge(base.Add(900*time.Second + time.Duration(k)*30*time.Second))
	}
	if act.reapReason.Load() != reapReasonEdge || rw1.closes.Load() != 1 {
		t.Fatal("asleep-client evidence did not reap (admitted trade: indistinguishable from a corpse)")
	}
}

func newBackstopEntry(act *sessionActivity, backWin time.Duration, trickle int64, base time.Time) (*reapEntry, *fakeEndpoint, *fakeEndpoint) {
	rw1, rw2 := &fakeEndpoint{}, &fakeEndpoint{}
	e := &reapEntry{
		act: act, rw1: rw1, rw2: rw2, role: reapRoleBackstop,
		backWin: backWin, trickle: trickle, service: "test", born: base,
	}
	e.rateRing = append(e.rateRing, ratePoint{nano: base.UnixNano(), bytes: act.bytesFrom[1].Load()})
	return e, rw1, rw2
}

// The backstop corpse: originator silent for the full window
// while the server drips at 10 B/s — reaped exactly when the
// window completes, not before.
func TestBackstopDripCorpseReaped(t *testing.T) {
	base := time.Now()
	act := silentAct(base)
	e, rw1, rw2 := newBackstopEntry(act, 300*time.Second, 256, base)

	for k := 1; k <= 9; k++ {
		act.bytesFrom[1].Store(int64(300 * k)) // 10 B/s drip
		e.sampleBackstop(base.Add(time.Duration(k) * 30 * time.Second))
		if rw1.closes.Load() != 0 {
			t.Fatalf("reaped at tick %d, before the 300s window completed", k)
		}
	}
	act.bytesFrom[1].Store(3000)
	e.sampleBackstop(base.Add(300 * time.Second))
	if act.reapReason.Load() != reapReasonBackstop {
		t.Fatalf("reapReason = %d, want backstop (%d)", act.reapReason.Load(), reapReasonBackstop)
	}
	if rw1.closes.Load() != 1 || rw2.closes.Load() != 1 {
		t.Fatalf("endpoint closes = (%d, %d), want (1, 1)", rw1.closes.Load(), rw2.closes.Load())
	}
}

// F3 §4.3 cases 2 (backstop half) and 4: downloads and streams
// pour far above the trickle ceiling — safe at any silence.
func TestBackstopPouringFlowsSafe(t *testing.T) {
	for name, rateBps := range map[string]int64{"streaming 16KB/s": 16_000, "download 1MB/s": 1_000_000} {
		t.Run(name, func(t *testing.T) {
			base := time.Now()
			act := silentAct(base)
			e, rw1, _ := newBackstopEntry(act, 300*time.Second, 256, base)
			for k := 1; k <= 12; k++ {
				act.bytesFrom[1].Store(rateBps * 30 * int64(k))
				e.sampleBackstop(base.Add(time.Duration(k) * 30 * time.Second))
			}
			if act.reapReason.Load() != reapReasonNone || rw1.closes.Load() != 0 {
				t.Fatalf("pouring flow reaped (reason=%d)", act.reapReason.Load())
			}
		})
	}
}

// F3 §4.3 case 3: idle SSH — no bytes either way for hours. The
// backstop's serverBytes >= 1 floor leaves it untouched (the
// classic idle window owns that shape, if enabled at all).
func TestBackstopIdleSessionUntouched(t *testing.T) {
	base := time.Now()
	act := silentAct(base)
	e, rw1, _ := newBackstopEntry(act, 300*time.Second, 256, base)
	for k := 1; k <= 12; k++ {
		e.sampleBackstop(base.Add(time.Duration(k) * 30 * time.Second))
	}
	if act.reapReason.Load() != reapReasonNone || rw1.closes.Load() != 0 {
		t.Fatal("idle (zero server bytes) session was reaped by the backstop")
	}
}

// F3 §4.3 case 3 (edge half) is covered by the second half of
// TestEdgeQuietGameClientSafe: nothing offered means no
// unacked/notsent/zero-window evidence, so the edge never fires.

// Scoped sessions register with the sampler only when their
// role's window is enabled, and unregister when Transport
// returns.
func TestTransportScopedRegistration(t *testing.T) {
	oldEdge, oldBack, oldTrickle := reap.EdgeWindow(), reap.BackstopWindow(), reap.TrickleBps()
	t.Cleanup(func() { reap.Set(oldEdge, oldBack, oldTrickle) })
	oldIdle := idle.Get()
	t.Cleanup(func() { idle.Set(oldIdle) })
	idle.Set(0)
	reap.Set(time.Minute, 0, 0)

	before := reapTracked.Load()

	clientSide, transportClient := tcpPair(t)
	defer clientSide.Close()
	backendSide, transportBackend := tcpPair(t)
	defer backendSide.Close()

	// Wrap the endpoints so the buffered path runs and the
	// session looks like a handler's stack.
	wrap := func(c io.ReadWriter) io.ReadWriter { return &memRW{RW: c} }
	done := make(chan error, 1)
	go func() {
		done <- TransportScoped(wrap(transportClient), wrap(transportBackend), Scope{Role: "edge", Service: "svc"})
	}()

	// The session must be registered while Transport runs.
	deadline := time.Now().Add(2 * time.Second)
	for reapTracked.Load() != before+1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if reapTracked.Load() != before+1 {
		t.Fatalf("tracked sessions = %d, want %d while scoped transport runs", reapTracked.Load(), before+1)
	}

	if _, err := clientSide.Write([]byte("x")); err != nil {
		t.Fatalf("client write: %v", err)
	}
	buf := make([]byte, 1)
	if _, err := io.ReadFull(backendSide, buf); err != nil {
		t.Fatalf("backend read: %v", err)
	}
	clientSide.Close()
	backendSide.Close()
	if err := <-done; err != nil {
		t.Fatalf("TransportScoped err = %v, want nil", err)
	}
	if reapTracked.Load() != before {
		t.Fatalf("tracked sessions = %d after transport, want %d (unregistered)", reapTracked.Load(), before)
	}
}

// memRW hides the concrete conn type behind an io.ReadWriter,
// like the handler wrapper stacks do. Deadlines delegate to the
// wrapped conn, mirroring production wrappers (which embed
// net.Conn and so delegate for real).
type memRW struct{ RW io.ReadWriter }

func (m *memRW) Read(p []byte) (int, error)  { return m.RW.Read(p) }
func (m *memRW) Write(p []byte) (int, error) { return m.RW.Write(p) }
func (m *memRW) Close() error {
	if c, ok := m.RW.(io.Closer); ok {
		return c.Close()
	}
	return nil
}
func (m *memRW) SetDeadline(t time.Time) error {
	if d, ok := m.RW.(interface{ SetDeadline(time.Time) error }); ok {
		return d.SetDeadline(t)
	}
	return nil
}
func (m *memRW) SetReadDeadline(t time.Time) error {
	if d, ok := m.RW.(interface{ SetReadDeadline(time.Time) error }); ok {
		return d.SetReadDeadline(t)
	}
	return nil
}
func (m *memRW) SetWriteDeadline(t time.Time) error {
	if d, ok := m.RW.(interface{ SetWriteDeadline(time.Time) error }); ok {
		return d.SetWriteDeadline(t)
	}
	return nil
}

// Drain bound (F2 fix spec S2): with the window OFF, a session
// whose client EOFs and whose server then stays silent must
// unwind at the drain bound instead of parking forever.
func TestTransportSilentPostEOFDrainBounded(t *testing.T) {
	oldBound := defaultDrainBound
	defaultDrainBound = 400 * time.Millisecond
	defer func() { defaultDrainBound = oldBound }()
	oldIdle := idle.Get()
	idle.Set(0)
	defer idle.Set(oldIdle)

	clientSide, transportClient := tcpPair(t)
	defer clientSide.Close()
	backendSide, transportBackend := tcpPair(t)
	defer backendSide.Close()

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		done <- transport(&memRW{RW: transportClient}, &memRW{RW: transportBackend}, 0)
	}()

	closeWrite(t, clientSide)
	// The backend stays open and silent: the drain must expire.
	select {
	case err := <-done:
		if err != ErrNoProgress {
			t.Fatalf("transport err = %v, want ErrNoProgress (drain bound)", err)
		}
		if elapsed := time.Since(start); elapsed < 300*time.Millisecond {
			t.Fatalf("drain expired after %v, before the 400ms bound", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("silent post-EOF drain was not bounded (transport still running)")
	}
}

// Drain bound: a post-EOF response that keeps FLOWING is never
// cut, however long it runs past the bound (F-212-B).
func TestTransportFlowingPostEOFDrainNotCut(t *testing.T) {
	oldBound := defaultDrainBound
	defaultDrainBound = 300 * time.Millisecond
	defer func() { defaultDrainBound = oldBound }()
	oldIdle := idle.Get()
	idle.Set(0)
	defer idle.Set(oldIdle)

	clientSide, transportClient := tcpPair(t)
	defer clientSide.Close()
	backendSide, transportBackend := tcpPair(t)
	defer backendSide.Close()

	done := make(chan error, 1)
	go func() {
		done <- transport(&memRW{RW: transportClient}, &memRW{RW: transportBackend}, 0)
	}()

	closeWrite(t, clientSide)

	// The server speaks first and at length after the client's
	// half-close: 10 chunks at 100ms intervals = 1s of flow,
	// more than 3x the drain bound.
	const chunks = 10
	go func() {
		for i := 0; i < chunks; i++ {
			backendSide.Write([]byte("chunk;"))
			time.Sleep(100 * time.Millisecond)
		}
		backendSide.(interface{ CloseWrite() error }).CloseWrite()
	}()

	got := make([]byte, 0, chunks*6)
	buf := make([]byte, 64)
	for len(got) < chunks*6 {
		n, err := clientSide.Read(buf)
		if err != nil {
			t.Fatalf("client read after %d bytes: %v (flowing drain was cut)", len(got), err)
		}
		got = append(got, buf[:n]...)
	}
	if string(got) != string(bytes.Repeat([]byte("chunk;"), chunks)) {
		t.Fatalf("client received %q, want %d intact chunks", got, chunks)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("transport err = %v, want nil (clean two-sided EOF)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("transport did not return after both sides EOF'd")
	}
}
