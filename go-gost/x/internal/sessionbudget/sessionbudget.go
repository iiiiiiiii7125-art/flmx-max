// Package sessionbudget is the agent-wide session admission
// budget (package K, memory face). It exists because the
// incident shape is not a Go-heap problem: a session's price is
// mostly kernel socket buffers, goroutine stacks and connection
// structures (~156KB per leg, ~312KB per session pair in the
// ECS1686 field measurement), which the 2.1.0 GOMEMLIMIT soft
// cap cannot see — the agent reached 286MB RSS on a 457MB
// machine (918 sessions) with its heap far below the 317MiB
// heap cap. The soft cap stays (it governs the heap); this
// budget governs admission: when the machine-level lines are
// crossed, NEW sessions are refused (closed at accept) while
// established sessions and the agent itself stay alive —
// shedding load instead of eating memory until the OOM killer
// or a full conntrack table takes the whole node down.
//
// Two lines, either one refuses (calibrated on the field
// numbers, where sessions×2 or one retry storm reaches the
// wall):
//
//   - process RSS >= 75% of total RAM (resume at 65% —
//     hysteresis so the gate does not flap per accept);
//   - active sessions >= total RAM / 256KiB (457MB -> 1828
//     sessions; the field count was 918, so the line sits at
//     roughly twice the observed healthy load).
//
// Admission alone cannot reclaim sessions that are already
// parked (the tunnel_49 zombies are ESTABLISHED at the TCP
// layer — the adjacent hop's kernel answers keepalive probes
// for a socket its userspace forgot, so neither the 180/60/6
// probes nor FIN propagation can ever reap them). The third
// mechanism therefore works at this same layer: under budget
// pressure, EvictUnderPressure closes the coldest tracked
// sessions (longest without byte activity). That is a
// pressure-triggered eviction, not an idle timeout — below the
// lines, sessions idle untouched forever.
//
// The budget is process-global (all services share one machine)
// and derives from total RAM once. FLMX_SKIP_SESSION_BUDGET
// disables enforcement (counting and reporting continue).
package sessionbudget

import (
	"bufio"
	"errors"
	stdlog "log"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/go-gost/core/logger"
)

// defaultLogf is the eviction log sink (core default logger,
// the internal/net precedent; std log fallback when no default
// logger has been installed, e.g. in tests).
var defaultLogf = func(format string, args ...any) {
	if l := logger.Default(); l != nil {
		l.Warnf(format, args...)
		return
	}
	stdlog.Printf(format, args...)
}

// Budget is the derived admission budget for one machine size.
type Budget struct {
	TotalRAM       int64
	MaxSessions    int64
	MaxRSSBytes    int64
	ResumeRSSBytes int64
}

// perSessionBytes is the conservative per-session price the
// session-count line divides RAM by (field pair price ~312KB;
// the line pairs it with the RSS line, which binds first in
// practice, so this one is the storm guard).
const perSessionBytes = 256 << 10

// BudgetFor derives the budget from total RAM. A non-positive
// total yields a zero budget (enforcement disabled: the machine
// size is unknown, and guessing a cap could refuse everything).
func BudgetFor(totalRAM int64) Budget {
	if totalRAM <= 0 {
		return Budget{}
	}
	return Budget{
		TotalRAM:       totalRAM,
		MaxSessions:    totalRAM / perSessionBytes,
		MaxRSSBytes:    totalRAM * 75 / 100,
		ResumeRSSBytes: totalRAM * 65 / 100,
	}
}

// env mirrors hostguard's seams so tests can substitute the
// world. Production values wire /proc and os.Getenv.
type env struct {
	getenv    func(string) string
	readRAM   func() (int64, error)
	readRSS   func() (int64, error)
	now       func() time.Time
	sampleTTL time.Duration
}

// The /proc paths and page size are standalone vars (not env
// fields) so the production readers below can use them without
// an initialization cycle through defaultEnv.
var (
	statmPath   = "/proc/self/statm"
	meminfoPath = "/proc/meminfo"
	pageSize    = int64(os.Getpagesize())
)

var defaultEnv = env{
	getenv:    os.Getenv,
	readRAM:   readMemTotalBytes,
	readRSS:   readSelfRSSBytes,
	now:       time.Now,
	sampleTTL: time.Second,
}

var (
	initOnce sync.Once
	budget   Budget
	disabled bool

	active   atomic.Int64
	rejected atomic.Int64

	rssMu       sync.Mutex
	rssCached   int64
	rssCachedAt time.Time
	overRSS     atomic.Bool

	rejectLogMu   sync.Mutex
	lastRejectLog time.Time
)

// SkipEnvName disables enforcement when set to 1/true/yes, in
// the same shape as the host guard switches.
const SkipEnvName = "FLMX_SKIP_SESSION_BUDGET"

func initState() {
	initOnce.Do(func() {
		total, err := defaultEnv.readRAM()
		if err == nil {
			budget = BudgetFor(total)
		}
		switch strings.ToLower(strings.TrimSpace(defaultEnv.getenv(SkipEnvName))) {
		case "1", "true", "yes":
			disabled = true
		}
	})
}

// CurrentBudget returns the budget in force (zero when unknown).
func CurrentBudget() Budget {
	initState()
	return budget
}

// EnforcementDisabled reports whether the skip switch is set.
func EnforcementDisabled() bool {
	initState()
	return disabled
}

// Active is the number of sessions currently admitted.
func Active() int64 { return active.Load() }

// RejectedTotal is the cumulative count of refused sessions.
func RejectedTotal() int64 { return rejected.Load() }

// RSSBytes returns the cached process RSS (sampled at most once
// per sampleTTL); 0 when unreadable, which disables only the
// RSS line, never the session-count line.
func RSSBytes() int64 {
	return sampleRSS()
}

func sampleRSS() int64 {
	rssMu.Lock()
	defer rssMu.Unlock()
	now := defaultEnv.now()
	if !rssCachedAt.IsZero() && now.Sub(rssCachedAt) < defaultEnv.sampleTTL {
		return rssCached
	}
	rss, err := defaultEnv.readRSS()
	if err != nil {
		// Keep the last good sample; 0 only if never sampled.
		if rssCachedAt.IsZero() {
			return 0
		}
		return rssCached
	}
	rssCached = rss
	rssCachedAt = now
	return rss
}

// TryAcquire admits one new session when both budget lines have
// room. On success it returns the release function (call exactly
// once, when the session ends); on refusal it counts the refusal
// and returns ok=false. Counting is unconditional so reporting
// stays truthful even when enforcement is disabled.
func TryAcquire() (release func(), ok bool) {
	initState()
	if !disabled && budget.TotalRAM > 0 {
		if budget.MaxSessions > 0 && active.Load() >= budget.MaxSessions {
			rejected.Add(1)
			return nil, false
		}
		if rss := sampleRSS(); rss > 0 {
			if overRSS.Load() {
				if rss <= budget.ResumeRSSBytes {
					overRSS.Store(false)
				}
			} else if rss >= budget.MaxRSSBytes {
				overRSS.Store(true)
			}
			if overRSS.Load() {
				rejected.Add(1)
				return nil, false
			}
		}
	}
	active.Add(1)
	var once sync.Once
	return func() { once.Do(func() { active.Add(-1) }) }, true
}

// --- 压力驱逐（包 K 机制定版第三件）---
//
// tunnel_49 形态的最后一环：只开不关的会话在 TCP 层「活着」
// （相邻跳内核替死会话应答探活，探针未答计数恒 0），探活与
// FIN 传播都够不着它们——本侧唯一能动手的杠杆是会话本身。
// 准入预算挡的是「再涨」，驱逐收的是「已涨」：RSS 压到预算线
// （或会话数顶到上限）时，按最久无字节活动顺序关闭最冷的会
// 话，直到压力回落到恢复线或没有够冷的候选为止。
//
// 与被禁的「idle 超时拆除」的区别是触发源与对象：这里没有
// 任何按时长到点拆连接的规则——压力不越线时，会话闲置多久
// 都不动；越线时也只动无字节活动超过 evictionMinIdle 的最
// 冷者，活跃会话（有字节在流）永不入选。驱逐一条会话＝关闭
// 其入站腿，Transport 随错全拆双腿，fd/内存成对回收。

// evictionMinIdle 是入选下限：现场健康会话最长约 151s 即自
// 行关闭，无字节活动超过 180s 的会话在该形态下几乎必为僵
// 尸；180s 同时保证任何正常长连接先要「冷」满三分钟且整机
// 已在压力线上才可能被波及（此时拆一条可重连的闲置会话，
// 远好于整机撞墙全灭）。
const evictionMinIdle = 180 * time.Second

// evictionTick 是压力巡检周期；evictionMaxPerTick 封顶单轮
// 驱逐数（32 条/10s ≈ 192 条/分 ≈ 60MB/分的回收能力，对现
// 场 +3.4MB/分 的净累积速率留一个数量级余量，又不至一轮
// 误伤成片）。
const (
	evictionTick       = 10 * time.Second
	evictionMaxPerTick = 32
)

// session is one registered live session.
type session struct {
	id           uint64
	raw          net.Conn
	lastActivity atomic.Int64 // unix nano of last Read/Write byte movement
}

var (
	sessionSeq  atomic.Uint64
	sessionsMu  sync.Mutex
	sessions    = map[uint64]*session{}
	evicted     atomic.Int64
	evictorOnce sync.Once

	evictLogMu   sync.Mutex
	lastEvictLog time.Time
)

// EvictedTotal is the cumulative count of pressure-evicted
// sessions.
func EvictedTotal() int64 { return evicted.Load() }

// Track registers an admitted session for pressure eviction and
// wraps its conn so byte movement stamps the session's activity
// clock. It returns the wrapped conn and a combined release:
// unregister + the TryAcquire release, idempotent. Tracking is
// unconditional (counters stay truthful with enforcement off);
// eviction itself only runs when enforcement is on.
func Track(c net.Conn, release func()) (net.Conn, func()) {
	initState()
	s := &session{id: sessionSeq.Add(1), raw: c}
	s.lastActivity.Store(defaultEnv.now().UnixNano())
	sessionsMu.Lock()
	sessions[s.id] = s
	sessionsMu.Unlock()
	ensureEvictor()
	var once sync.Once
	combined := func() {
		once.Do(func() {
			sessionsMu.Lock()
			delete(sessions, s.id)
			sessionsMu.Unlock()
			if release != nil {
				release()
			}
		})
	}
	return &trackedConn{Conn: c, s: s}, combined
}

// trackedConn stamps activity on byte movement and forwards
// half-close/syscall capabilities of the wrapped conn (the same
// forwarding discipline as the other session wrappers: hiding
// CloseWrite here would re-break Transport's FIN propagation).
type trackedConn struct {
	net.Conn
	s *session
}

func (c *trackedConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.s.lastActivity.Store(defaultEnv.now().UnixNano())
	}
	return n, err
}

func (c *trackedConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if n > 0 {
		c.s.lastActivity.Store(defaultEnv.now().UnixNano())
	}
	return n, err
}

func (c *trackedConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return errHalfCloseUnsupported
}

func (c *trackedConn) CloseRead() error {
	if cr, ok := c.Conn.(interface{ CloseRead() error }); ok {
		return cr.CloseRead()
	}
	return errHalfCloseUnsupported
}

func (c *trackedConn) SyscallConn() (syscall.RawConn, error) {
	if sc, ok := c.Conn.(syscall.Conn); ok {
		return sc.SyscallConn()
	}
	return nil, errHalfCloseUnsupported
}

var errHalfCloseUnsupported = errors.New("half-close not supported by the underlying connection")

func ensureEvictor() {
	evictorOnce.Do(func() {
		if disabled || budget.TotalRAM <= 0 {
			return
		}
		go func() {
			ticker := time.NewTicker(evictionTick)
			defer ticker.Stop()
			for range ticker.C {
				EvictUnderPressure()
			}
		}()
	})
}

// EvictUnderPressure runs one eviction pass: when the process
// is over a budget line (RSS at/over the max line, latched
// until the resume line, or the session count at its cap), it
// closes the coldest sessions — longest without byte activity,
// never younger than evictionMinIdle — up to the per-tick cap,
// and reports how many it closed. Exported for tests and for
// the reporter's diagnostics; the background loop is its only
// production caller.
func EvictUnderPressure() int {
	initState()
	if disabled || budget.TotalRAM <= 0 {
		return 0
	}
	now := defaultEnv.now()
	underPressure := false
	if rss := sampleRSS(); rss > 0 {
		if overRSS.Load() {
			if rss <= budget.ResumeRSSBytes {
				overRSS.Store(false)
			}
		} else if rss >= budget.MaxRSSBytes {
			overRSS.Store(true)
		}
		underPressure = overRSS.Load()
	}
	if !underPressure && budget.MaxSessions > 0 && active.Load() >= budget.MaxSessions {
		underPressure = true
	}
	if !underPressure {
		return 0
	}

	sessionsMu.Lock()
	candidates := make([]*session, 0, len(sessions))
	for _, s := range sessions {
		if now.Sub(time.Unix(0, s.lastActivity.Load())) >= evictionMinIdle {
			candidates = append(candidates, s)
		}
	}
	sessionsMu.Unlock()
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].lastActivity.Load() < candidates[j].lastActivity.Load()
	})
	if len(candidates) > evictionMaxPerTick {
		candidates = candidates[:evictionMaxPerTick]
	}
	for _, s := range candidates {
		// Closing the raw inbound leg fails the session's
		// Transport in both directions; the service's deferred
		// release then unregisters and uncounts it.
		s.raw.Close()
	}
	if len(candidates) > 0 {
		evicted.Add(int64(len(candidates)))
		if EvictLogDue() {
			defaultLogf("session budget: pressure eviction closed %d coldest sessions (evicted_total=%d active=%d rss=%d)",
				len(candidates), evicted.Load(), active.Load(), RSSBytes())
		}
	}
	return len(candidates)
}

// EvictLogDue throttles eviction logging like RejectLogDue.
func EvictLogDue() bool {
	evictLogMu.Lock()
	defer evictLogMu.Unlock()
	now := defaultEnv.now()
	if now.Sub(lastEvictLog) < 30*time.Second {
		return false
	}
	lastEvictLog = now
	return true
}

// RejectLogDue throttles refusal logging to one line per 30s;
// the cumulative counter carries the true volume.
func RejectLogDue() bool {
	rejectLogMu.Lock()
	defer rejectLogMu.Unlock()
	now := defaultEnv.now()
	if now.Sub(lastRejectLog) < 30*time.Second {
		return false
	}
	lastRejectLog = now
	return true
}

func readMemTotalBytes() (int64, error) {
	f, err := os.Open(meminfoPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kb, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				return 0, err
			}
			return kb * 1024, nil
		}
	}
	return 0, os.ErrNotExist
}

// readSelfRSSBytes reads resident pages from /proc/self/statm.
func readSelfRSSBytes() (int64, error) {
	data, err := os.ReadFile(statmPath)
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return 0, os.ErrInvalid
	}
	pages, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return 0, err
	}
	return pages * pageSize, nil
}
