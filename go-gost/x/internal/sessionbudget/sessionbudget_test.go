package sessionbudget

import (
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// resetStateForTest reinitializes the package state with a
// substituted environment (same-process tests only).
func resetStateForTest(t *testing.T, ram int64, rss *atomic.Int64, skip bool) {
	t.Helper()
	initOnce = sync.Once{}
	budget = Budget{}
	disabled = false
	active.Store(0)
	rejected.Store(0)
	overRSS.Store(false)
	rssMu.Lock()
	rssCached = 0
	rssCachedAt = time.Time{}
	rssMu.Unlock()
	defaultEnv.readRAM = func() (int64, error) { return ram, nil }
	defaultEnv.readRSS = func() (int64, error) { return rss.Load(), nil }
	defaultEnv.sampleTTL = 0 // every read re-samples in tests
	if skip {
		defaultEnv.getenv = func(k string) string {
			if k == SkipEnvName {
				return "1"
			}
			return ""
		}
	} else {
		defaultEnv.getenv = func(string) string { return "" }
	}
}

func TestBudgetForFieldCalibration(t *testing.T) {
	// ECS1686 field numbers: 457MiB RAM, 918 sessions, RSS 286MB.
	b := BudgetFor(457 << 20)
	if b.MaxSessions != 1828 {
		t.Fatalf("MaxSessions = %d, want 1828 (457MiB / 256KiB)", b.MaxSessions)
	}
	if want := int64(457<<20) * 75 / 100; b.MaxRSSBytes != want {
		t.Fatalf("MaxRSSBytes = %d, want %d", b.MaxRSSBytes, want)
	}
	// The observed healthy load must not trip either line.
	if 918 >= b.MaxSessions {
		t.Fatal("field session count 918 trips the session line")
	}
	if rss := int64(286 << 20); rss >= b.MaxRSSBytes {
		t.Fatal("field RSS 286MB trips the RSS line")
	}
	// Doubling the load must trip both lines (the wall shape).
	if 1836 < b.MaxSessions {
		t.Fatal("doubled session count does not trip the session line")
	}
	if int64(390<<20) < b.MaxRSSBytes {
		t.Fatal("near-wall RSS does not trip the RSS line")
	}
	if z := BudgetFor(0); z != (Budget{}) {
		t.Fatalf("BudgetFor(0) = %+v, want zero", z)
	}
}

func TestSessionLineRefusesAndRecovers(t *testing.T) {
	var rss atomic.Int64
	resetStateForTest(t, 4<<20, &rss, false) // 4MiB RAM -> 16 sessions
	var releases []func()
	for i := 0; i < 16; i++ {
		rel, ok := TryAcquire()
		if !ok {
			t.Fatalf("acquire %d refused below the line", i)
		}
		releases = append(releases, rel)
	}
	if _, ok := TryAcquire(); ok {
		t.Fatal("17th session admitted past MaxSessions")
	}
	if RejectedTotal() != 1 {
		t.Fatalf("rejected = %d, want 1", RejectedTotal())
	}
	releases[0]()
	if _, ok := TryAcquire(); !ok {
		t.Fatal("session refused after a release freed a slot")
	}
	if Active() != 16 {
		t.Fatalf("active = %d, want 16", Active())
	}
	// Double release must not double-count the free slot.
	releases[1]()
	releases[1]()
	if Active() != 15 {
		t.Fatalf("active = %d after double release, want 15", Active())
	}
}

func TestRSSLineHysteresis(t *testing.T) {
	var rss atomic.Int64
	resetStateForTest(t, 100<<20, &rss, false)
	b := CurrentBudget()
	rss.Store(b.MaxRSSBytes) // at the line: latch on
	if _, ok := TryAcquire(); ok {
		t.Fatal("admitted at the RSS line")
	}
	rss.Store(b.ResumeRSSBytes + 1) // inside the hysteresis band
	if _, ok := TryAcquire(); ok {
		t.Fatal("admitted inside the hysteresis band while latched")
	}
	rss.Store(b.ResumeRSSBytes) // at the resume line: latch off
	rel, ok := TryAcquire()
	if !ok {
		t.Fatal("refused at the resume line")
	}
	rel()
	if RejectedTotal() != 2 {
		t.Fatalf("rejected = %d, want 2", RejectedTotal())
	}
}

func TestSkipSwitchDisablesEnforcementOnly(t *testing.T) {
	var rss atomic.Int64
	resetStateForTest(t, 4<<20, &rss, true)
	rss.Store(1 << 40)
	for i := 0; i < 32; i++ { // far past the 16-session line
		rel, ok := TryAcquire()
		if !ok {
			t.Fatal("skip switch set but session refused")
		}
		rel()
	}
	if Active() != 0 {
		t.Fatalf("active = %d after releases, want 0", Active())
	}
}

// --- Track + pressure eviction (机制定版第三件) ---

type fakeConn struct {
	net.Conn // embedded nil: only the overridden methods are used
	closed   atomic.Bool
}

func (c *fakeConn) Read(b []byte) (int, error) {
	if c.closed.Load() {
		return 0, io.ErrClosedPipe
	}
	return 0, io.EOF
}

func (c *fakeConn) Write(b []byte) (int, error) {
	if c.closed.Load() {
		return 0, io.ErrClosedPipe
	}
	return len(b), nil
}

func (c *fakeConn) Close() error {
	c.closed.Store(true)
	return nil
}

func resetRegistryForTest() {
	sessionsMu.Lock()
	sessions = map[uint64]*session{}
	sessionsMu.Unlock()
	evicted.Store(0)
	evictorOnce = sync.Once{}
}

func backdateAllSessions(age time.Duration) {
	sessionsMu.Lock()
	defer sessionsMu.Unlock()
	for _, s := range sessions {
		s.lastActivity.Store(time.Now().Add(-age).UnixNano())
	}
}

func TestTrackRegistersAndReleaseUnregisters(t *testing.T) {
	var rss atomic.Int64
	resetStateForTest(t, 100<<20, &rss, false)
	resetRegistryForTest()

	rel, ok := TryAcquire()
	if !ok {
		t.Fatal("acquire refused")
	}
	raw := &fakeConn{}
	tracked, combined := Track(raw, rel)
	if n := len(sessions); n != 1 {
		t.Fatalf("registered sessions = %d, want 1", n)
	}
	// Byte movement stamps activity.
	backdateAllSessions(time.Hour)
	if _, err := tracked.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	sessionsMu.Lock()
	var s *session
	for _, v := range sessions {
		s = v
	}
	sessionsMu.Unlock()
	if age := time.Since(time.Unix(0, s.lastActivity.Load())); age > time.Minute {
		t.Fatalf("activity stamp age = %v after Write, want fresh", age)
	}
	combined()
	combined() // idempotent
	if n := len(sessions); n != 0 {
		t.Fatalf("registered sessions = %d after release, want 0", n)
	}
	if Active() != 0 {
		t.Fatalf("active = %d after release, want 0", Active())
	}
}

func TestEvictUnderPressureClosesColdestOnly(t *testing.T) {
	var rss atomic.Int64
	resetStateForTest(t, 100<<20, &rss, false)
	resetRegistryForTest()
	b := CurrentBudget()

	cold1, cold2, fresh := &fakeConn{}, &fakeConn{}, &fakeConn{}
	var releases []func()
	for _, raw := range []*fakeConn{cold1, cold2, fresh} {
		rel, ok := TryAcquire()
		if !ok {
			t.Fatal("acquire refused")
		}
		_, combined := Track(raw, rel)
		releases = append(releases, combined)
	}
	defer func() {
		for _, r := range releases {
			r()
		}
	}()
	// All three are old; then freshen the third via its stamp.
	backdateAllSessions(evictionMinIdle + time.Minute)
	sessionsMu.Lock()
	for _, s := range sessions {
		if s.raw == net.Conn(fresh) {
			s.lastActivity.Store(time.Now().UnixNano())
		}
	}
	sessionsMu.Unlock()

	// Below the line: no eviction even with cold sessions.
	rss.Store(b.MaxRSSBytes - 1)
	if n := EvictUnderPressure(); n != 0 {
		t.Fatalf("evicted %d below the line, want 0", n)
	}
	// At the line: only the cold sessions are closed.
	rss.Store(b.MaxRSSBytes)
	if n := EvictUnderPressure(); n != 2 {
		t.Fatalf("evicted %d, want 2 (the two cold sessions)", n)
	}
	if !cold1.closed.Load() || !cold2.closed.Load() {
		t.Fatal("cold sessions were not closed")
	}
	if fresh.closed.Load() {
		t.Fatal("fresh session was evicted")
	}
	if EvictedTotal() != 2 {
		t.Fatalf("evicted total = %d, want 2", EvictedTotal())
	}
	// Pressure relieved below the resume line: latch clears.
	rss.Store(b.ResumeRSSBytes)
	if n := EvictUnderPressure(); n != 0 {
		t.Fatalf("evicted %d after relief, want 0", n)
	}
}

// byteFakeConn serves a fixed read buffer so byte counters can
// move in tests (fakeConn.Read never returns bytes).
type byteFakeConn struct {
	fakeConn
	rbuf []byte
}

func (c *byteFakeConn) Read(b []byte) (int, error) {
	if c.closed.Load() {
		return 0, io.ErrClosedPipe
	}
	if len(c.rbuf) == 0 {
		return 0, io.EOF
	}
	n := copy(b, c.rbuf)
	c.rbuf = c.rbuf[n:]
	return n, nil
}

func logFieldInt(t *testing.T, line, key string) int64 {
	t.Helper()
	i := strings.Index(line, key)
	if i < 0 {
		t.Fatalf("log line %q lacks %q", line, key)
	}
	rest := line[i+len(key):]
	end := strings.IndexByte(rest, 's')
	if end < 0 {
		rest = strings.Fields(rest)[0]
	} else {
		rest = rest[:end]
	}
	v, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		t.Fatalf("log line %q: bad %s value: %v", line, key, err)
	}
	return v
}

// The eviction victim log is the forensic record a bare counter
// cannot provide (LIVE canary lesson): it must carry the
// victim's identity, idle and total age, and both byte counters,
// with values matching the session's actual state.
func TestEvictionLogsVictimIdentity(t *testing.T) {
	var rss atomic.Int64
	resetStateForTest(t, 100<<20, &rss, false)
	resetRegistryForTest()

	var logMu sync.Mutex
	var lines []string
	oldLogf := defaultLogf
	defaultLogf = func(format string, args ...any) {
		logMu.Lock()
		lines = append(lines, fmt.Sprintf(format, args...))
		logMu.Unlock()
	}
	defer func() { defaultLogf = oldLogf }()

	rel, ok := TryAcquire()
	if !ok {
		t.Fatal("acquire refused")
	}
	raw := &byteFakeConn{rbuf: []byte("xyz")}
	tracked, combined := Track(raw, rel)
	defer combined()
	buf := make([]byte, 8)
	if n, err := tracked.Read(buf); n != 3 || err != nil {
		t.Fatalf("tracked Read = %d, %v; want 3, nil", n, err)
	}
	if _, err := tracked.Write([]byte("ab")); err != nil {
		t.Fatal(err)
	}
	sessionsMu.Lock()
	for _, s := range sessions {
		s.lastActivity.Store(time.Now().Add(-(evictionMinIdle + 60*time.Second)).UnixNano())
		s.createdAt = time.Now().Add(-(evictionMinIdle + 120*time.Second)).UnixNano()
	}
	sessionsMu.Unlock()

	rss.Store(CurrentBudget().MaxRSSBytes)
	if n := EvictUnderPressure(); n != 1 {
		t.Fatalf("evicted %d, want 1", n)
	}
	if !raw.closed.Load() {
		t.Fatal("victim session was not closed")
	}
	var victim string
	for _, l := range lines {
		if strings.Contains(l, "evicted session id=") {
			victim = l
		}
	}
	if victim == "" {
		t.Fatalf("no per-victim log line captured; lines = %v", lines)
	}
	for _, want := range []string{"bytes_in=3", "bytes_out=2", "remote=?", "active=1", "evicted_total=1"} {
		if !strings.Contains(victim, want) {
			t.Fatalf("victim line %q missing %q", victim, want)
		}
	}
	if idle := logFieldInt(t, victim, "idle="); idle < 239 || idle > 245 {
		t.Fatalf("victim idle = %ds, want ~240s", idle)
	}
	if age := logFieldInt(t, victim, "age="); age < 299 || age > 305 {
		t.Fatalf("victim age = %ds, want ~300s", age)
	}
}
