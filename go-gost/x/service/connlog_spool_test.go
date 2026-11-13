package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// 2.1.12 回归：enforceCap 在大 spool 上不得 O(n²) 重 marshal。
// 生产实证：瑞驰3 spool 积到 73489 条时，旧版每周期全量重 marshal
// 整个 spool 量尺寸、每丢一条又全量重算一次，75.2% CPU 烧在
// encoding/json.Marshal。修法：事件入队时算好尺寸缓存在事件自身
// （json:"-"），queuedBytes/enforceCap 此后只做整数加减。

// oracleBytes 按容量口径定义独立重算一组事件的序列化字节
// （逐条 json.Marshal 求和，每条 +1 换行），作为测试 oracle。
func oracleBytes(t *testing.T, events []ConnEvent) int64 {
	t.Helper()
	var n int64
	for _, ev := range events {
		line, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("marshal event: %v", err)
		}
		n += int64(len(line)) + 1
	}
	return n
}

func TestConnlogEnforceCapLargeSpool(t *testing.T) {
	const n = 5000
	st := connlogSpoolState{}
	for i := 0; i < n; i++ {
		st.Buffered = append(st.Buffered, testOpenEvent(fmt.Sprintf("conn-%d", i), int64(1000+i)))
	}
	before := st.queuedBytes()
	if before <= 0 {
		t.Fatalf("queuedBytes = %d, want > 0", before)
	}
	if got := oracleBytes(t, st.Buffered); got != before {
		t.Fatalf("queuedBytes = %d, oracle = %d", before, got)
	}
	if st.Buffered[0].size <= 0 {
		t.Fatalf("event size not cached after queuedBytes")
	}

	// 上限只容约一半：应丢最旧的一批，余量不超上限。
	half := before / 2
	start := time.Now()
	dropped := st.enforceCap(half)
	elapsed := time.Since(start)
	t.Logf("enforceCap dropped %d of %d events in %v (cap %d bytes)", dropped, n, elapsed, half)

	if dropped <= 0 || dropped >= n {
		t.Fatalf("dropped = %d, want 0 < dropped < %d", dropped, n)
	}
	if got := len(st.Buffered); got != n-dropped {
		t.Fatalf("len(Buffered) = %d, want %d", got, n-dropped)
	}
	if st.Dropped != int64(dropped) {
		t.Fatalf("Dropped = %d, want %d", st.Dropped, dropped)
	}
	// 只丢最旧：剩余头部应为第 dropped 条原事件。
	if want := fmt.Sprintf("conn-%d", dropped); st.Buffered[0].ConnID != want {
		t.Fatalf("head after drop = %s, want %s (must drop oldest first)", st.Buffered[0].ConnID, want)
	}
	// 容量口径前后一致：余量不超上限，且与 oracle 独立重算相等；
	// 重复调用结果稳定（缓存路径不得漂移）。
	after := st.queuedBytes()
	if after > half {
		t.Fatalf("queuedBytes after cap = %d, want <= %d", after, half)
	}
	if got := oracleBytes(t, st.Buffered); got != after {
		t.Fatalf("queuedBytes after cap = %d, oracle = %d", after, got)
	}
	if again := st.queuedBytes(); again != after {
		t.Fatalf("queuedBytes not stable: %d then %d", after, again)
	}
	// 性能门：旧版 O(n²) 在此规模上需数秒以上（每丢一条全量重
	// marshal）；新版首次量尺寸只 marshal 一次、丢弃只做减法。
	if elapsed > time.Second {
		t.Fatalf("enforceCap took %v, want < 1s", elapsed)
	}
}

func TestConnlogQueuedBytesCoversPendingRetryBuffered(t *testing.T) {
	pending := &connlogEnvelope{Stream: "teststream", Sequence: 1, Events: []ConnEvent{
		testOpenEvent("p-1", 100), testCloseEvent("p-1", 100, 200),
	}}
	retry := [][]ConnEvent{
		{testOpenEvent("r-1", 300)},
		{testOpenEvent("r-2", 400), testCloseEvent("r-2", 400, 500)},
	}
	buffered := []ConnEvent{testOpenEvent("b-1", 600), testOpenEvent("b-2", 700)}
	st := connlogSpoolState{Pending: pending, Retry: retry, Buffered: buffered}

	want := oracleBytes(t, pending.Events)
	for _, g := range retry {
		want += oracleBytes(t, g)
	}
	want += oracleBytes(t, buffered)
	if got := st.queuedBytes(); got != want {
		t.Fatalf("queuedBytes = %d, want %d (Pending+Retry+Buffered)", got, want)
	}

	// enforceCap 只许动 Buffered：Pending/Retry 原样，丢弃只从
	// Buffered 头部发生。上限定在「Pending+Retry 全量＋Buffered
	// 一半」处——Pending/Retry 本身已超半数总量，定在总量一半
	// 是达不到的（设计如此：已承诺批次不许丢）。
	pendingRetryBytes := oracleBytes(t, pending.Events)
	for _, g := range retry {
		pendingRetryBytes += oracleBytes(t, g)
	}
	bufferedBytes := oracleBytes(t, buffered)
	capBytes := pendingRetryBytes + bufferedBytes/2
	st2 := st
	dropped := st2.enforceCap(capBytes)
	if dropped <= 0 {
		t.Fatalf("dropped = %d, want > 0", dropped)
	}
	if len(st2.Pending.Events) != len(pending.Events) || len(st2.Retry) != len(retry) {
		t.Fatalf("enforceCap touched Pending/Retry: pending=%d retry groups=%d", len(st2.Pending.Events), len(st2.Retry))
	}
	if got := len(st2.Buffered); got != len(buffered)-dropped {
		t.Fatalf("len(Buffered) = %d, want %d", got, len(buffered)-dropped)
	}
	if got := st2.queuedBytes(); got > capBytes {
		t.Fatalf("queuedBytes after cap = %d, want <= %d", got, capBytes)
	}

	// 落盘往返：size 是 json:"-"，重载后尺寸为 0 应惰性重算，
	// 容量口径与重载前一致。
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	var reloaded connlogSpoolState
	if err := json.Unmarshal(data, &reloaded); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}
	if got := reloaded.queuedBytes(); got != want {
		t.Fatalf("queuedBytes after reload = %d, want %d", got, want)
	}
}

// 2.1.12 补测 L1：cap 小于 Pending+Retry 字节时，enforceCap 须
// 停在 Buffered 耗尽——Pending/Retry 是已承诺批次不许丢，余量
// 仍超上限是设计行为，不得为达标而动 Pending/Retry。
func TestConnlogEnforceCapPendingRetryExceedCap(t *testing.T) {
	pending := &connlogEnvelope{Stream: "teststream", Sequence: 1, Events: []ConnEvent{
		testOpenEvent("p-1", 100), testCloseEvent("p-1", 100, 200),
		testOpenEvent("p-2", 150), testCloseEvent("p-2", 150, 250),
	}}
	retry := [][]ConnEvent{
		{testOpenEvent("r-1", 300), testCloseEvent("r-1", 300, 400)},
		{testOpenEvent("r-2", 500)},
	}
	buffered := []ConnEvent{
		testOpenEvent("b-1", 600),
		testOpenEvent("b-2", 700), testCloseEvent("b-2", 700, 800),
	}
	st := connlogSpoolState{Pending: pending, Retry: retry, Buffered: buffered}

	pendingRetryBytes := oracleBytes(t, pending.Events)
	for _, g := range retry {
		pendingRetryBytes += oracleBytes(t, g)
	}
	bufferedBytes := oracleBytes(t, buffered)
	if total := st.queuedBytes(); total != pendingRetryBytes+bufferedBytes {
		t.Fatalf("queuedBytes = %d, want %d (Pending+Retry+Buffered)", total, pendingRetryBytes+bufferedBytes)
	}

	// 上限定在 Pending+Retry 字节之下：即使 Buffered 全丢也
	// 降不到上限以下。
	capBytes := pendingRetryBytes - 1
	dropped := st.enforceCap(capBytes)
	if dropped != len(buffered) {
		t.Fatalf("dropped = %d, want %d (all Buffered drained)", dropped, len(buffered))
	}
	if len(st.Buffered) != 0 {
		t.Fatalf("len(Buffered) = %d, want 0", len(st.Buffered))
	}
	// Pending/Retry 原样保留：条数与内容都不动。
	if len(st.Pending.Events) != len(pending.Events) {
		t.Fatalf("len(Pending.Events) = %d, want %d", len(st.Pending.Events), len(pending.Events))
	}
	for i, ev := range st.Pending.Events {
		if ev.ConnID != pending.Events[i].ConnID || ev.Type != pending.Events[i].Type {
			t.Fatalf("Pending event %d changed: %+v", i, ev)
		}
	}
	if len(st.Retry) != len(retry) {
		t.Fatalf("len(Retry) = %d, want %d", len(st.Retry), len(retry))
	}
	for gi, g := range st.Retry {
		if len(g) != len(retry[gi]) {
			t.Fatalf("len(Retry[%d]) = %d, want %d", gi, len(g), len(retry[gi]))
		}
		for i, ev := range g {
			if ev.ConnID != retry[gi][i].ConnID {
				t.Fatalf("Retry[%d] event %d changed: %+v", gi, i, ev)
			}
		}
	}
	// 余量仍超上限，且恰等于 Pending+Retry 口径。
	after := st.queuedBytes()
	if after <= capBytes {
		t.Fatalf("queuedBytes after cap = %d, want > %d (still over cap by design)", after, capBytes)
	}
	if after != pendingRetryBytes {
		t.Fatalf("queuedBytes after cap = %d, want %d (Pending+Retry only)", after, pendingRetryBytes)
	}
	// Dropped 只计实际丢的条数。
	if st.Dropped != int64(dropped) {
		t.Fatalf("Dropped = %d, want %d", st.Dropped, dropped)
	}
	// Buffered 已空时重复调用不再丢，行为稳定。
	if again := st.enforceCap(capBytes); again != 0 {
		t.Fatalf("second enforceCap dropped %d, want 0", again)
	}
	if st.Dropped != int64(dropped) {
		t.Fatalf("Dropped after second enforceCap = %d, want %d", st.Dropped, dropped)
	}
}

// 2.1.12 补测 L2：ConnEvent.size 是 json:"-"——字节级断言落盘
// JSON 里无 size 字段、加载后 size 回 0、首次 queuedBytes 惰性
// 重算后与落盘前一致。
func TestConnlogEventSizeRoundTrip(t *testing.T) {
	evs := []ConnEvent{testOpenEvent("s-1", 100), testCloseEvent("s-1", 100, 200)}
	st := connlogSpoolState{Buffered: evs}
	before := st.queuedBytes() // 填充尺寸缓存
	if st.Buffered[0].size <= 0 {
		t.Fatalf("size not cached after queuedBytes")
	}

	data, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	// 字节级断言：落盘 JSON 里不得出现 size 字段（缓存已填充
	// 的事件也一样——json:"-" 必须把它排除在线上/落盘格式外）。
	if bytes.Contains(data, []byte(`"size"`)) {
		t.Fatalf("spool JSON contains size field: %s", data)
	}
	line, err := json.Marshal(st.Buffered[0])
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	if bytes.Contains(line, []byte(`"size"`)) {
		t.Fatalf("event JSON contains size field: %s", line)
	}

	var reloaded connlogSpoolState
	if err := json.Unmarshal(data, &reloaded); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}
	// 加载后 size 回 0。
	for i := range reloaded.Buffered {
		if reloaded.Buffered[i].size != 0 {
			t.Fatalf("reloaded event %d size = %d, want 0", i, reloaded.Buffered[i].size)
		}
	}
	// 首次 queuedBytes 惰性重算后与落盘前一致，且与 oracle 独立
	// 重算相等；重算后缓存已填充，再次调用稳定。
	if got := reloaded.queuedBytes(); got != before {
		t.Fatalf("queuedBytes after reload = %d, want %d", got, before)
	}
	if reloaded.Buffered[0].size <= 0 {
		t.Fatalf("size not re-cached after queuedBytes on reload")
	}
	if again := reloaded.queuedBytes(); again != before {
		t.Fatalf("queuedBytes not stable after reload: %d then %d", before, again)
	}
	if got := oracleBytes(t, reloaded.Buffered); got != before {
		t.Fatalf("oracle after reload = %d, want %d", got, before)
	}
}

// 2.1.12 补测 L3：Open 台账（§4.6 结清台账）不计入 spool 容量
// 口径——它只是已见 open 未见 close 的连接备忘，同一条连接的
// open 事件已在 Buffered（或 Pending/Retry）里计过一次；误纳
// 会双倍计数，enforceCap 也会因此多丢事件。
func TestConnlogQueuedBytesExcludesOpenLedger(t *testing.T) {
	buffered := []ConnEvent{testOpenEvent("o-1", 100), testOpenEvent("o-2", 200)}
	st := connlogSpoolState{
		Buffered: buffered,
		Open: map[string]ConnEvent{
			"o-1": testOpenEvent("o-1", 100),
			"o-2": testOpenEvent("o-2", 200),
			"o-3": testOpenEvent("o-3", 300),
		},
		OpenLastKnown: map[string]int64{"o-1": 150, "o-2": 250, "o-3": 350},
	}
	want := oracleBytes(t, buffered)
	if got := st.queuedBytes(); got != want {
		t.Fatalf("queuedBytes = %d, want %d (Open ledger must not count)", got, want)
	}

	// 仅有 Open 台账的 spool 容量占用为 0。
	onlyOpen := connlogSpoolState{
		Open:          map[string]ConnEvent{"o-9": testOpenEvent("o-9", 900)},
		OpenLastKnown: map[string]int64{"o-9": 950},
	}
	if got := onlyOpen.queuedBytes(); got != 0 {
		t.Fatalf("queuedBytes with only Open ledger = %d, want 0", got)
	}

	// enforceCap 只按 Pending+Retry+Buffered 口径执行：Open 台
	// 账原样保留，不因容量压力被丢。
	st2 := st
	dropped := st2.enforceCap(want - 1)
	if dropped != 1 {
		t.Fatalf("dropped = %d, want 1", dropped)
	}
	if len(st2.Open) != 3 {
		t.Fatalf("enforceCap touched Open ledger: %d entries, want 3", len(st2.Open))
	}
	if _, ok := st2.Open["o-3"]; !ok {
		t.Fatalf("Open ledger lost entry o-3")
	}
	if got := st2.queuedBytes(); got > want-1 {
		t.Fatalf("queuedBytes after cap = %d, want <= %d", got, want-1)
	}
}
