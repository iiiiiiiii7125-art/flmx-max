package service

import (
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
