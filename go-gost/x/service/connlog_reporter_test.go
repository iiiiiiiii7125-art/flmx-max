package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-gost/x/internal/util/crypto"
)

// ---- 测试脚手架 ----

// fakeConnlogSender 可编程的假面板：记录每次收到的批次，按脚本
// 返回确认/失败/拒绝。
type fakeConnlogSender struct {
	calls   []connlogEnvelope
	handler func(call int, b connlogEnvelope) (int64, error)
}

func (f *fakeConnlogSender) send(_ context.Context, b connlogEnvelope) (int64, error) {
	f.calls = append(f.calls, b)
	if f.handler != nil {
		return f.handler(len(f.calls), b)
	}
	return b.Sequence, nil // 默认：确认，cursor=所发序号
}

// delivered 汇总所有「被确认」批次的事件（handler 返回 nil 的次）。
// 本脚手架按调用序号在各测试内自行断言，delivered 只做收集辅助。
func (f *fakeConnlogSender) allEvents() []ConnEvent {
	var out []ConnEvent
	for _, c := range f.calls {
		out = append(out, c.Events...)
	}
	return out
}

func newTestConnlogReporter(t *testing.T, dir string, sender *fakeConnlogSender, boot *string, tweak func(*connlogConfig)) *connlogReporter {
	t.Helper()
	sp, err := openConnlogSpool(filepath.Join(dir, connlogSpoolFileName("connlog-test-identity")))
	if err != nil {
		t.Fatalf("open spool: %v", err)
	}
	cfg := connlogConfig{
		spool:    sp,
		sendFn:   sender.send,
		bootIDFn: func() string { return *boot },
	}
	if tweak != nil {
		tweak(&cfg)
	}
	r := newConnlogReporter(cfg)
	if err := r.prepare(); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	return r
}

func reopenTestConnlogReporter(t *testing.T, dir string, sender *fakeConnlogSender, boot *string, tweak func(*connlogConfig)) *connlogReporter {
	// 与 newTest 相同（openConnlogSpool 本就从盘载入）——单列一
	// 个名字让「进程重启」语义在测试里可读。
	return newTestConnlogReporter(t, dir, sender, boot, tweak)
}

func testOpenEvent(connID string, startMs int64) ConnEvent {
	return ConnEvent{
		Type: ConnEventOpen, ConnID: connID, EntryKind: ConnEntryForward,
		EntryID: "svc_tcp", ClientIP: "203.0.113.7", ClientPort: 51000,
		Network: "tcp", SessionKind: "tcp_conn", StartMs: startMs,
	}
}

func testCloseEvent(connID string, startMs, endMs int64) ConnEvent {
	ev := testOpenEvent(connID, startMs)
	ev.Type = ConnEventClose
	ev.EndMs = endMs
	ev.Status = ConnStatusClosed
	return ev
}

func readSpoolStateFromDisk(t *testing.T, dir string) connlogSpoolState {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, connlogSpoolFileName("connlog-test-identity")))
	if err != nil {
		t.Fatalf("read spool file: %v", err)
	}
	var st connlogSpoolState
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("unmarshal spool file: %v", err)
	}
	return st
}

// ---- 1. 断连续报：面板不可达期间事件落盘，进程重启后全量续报 ----

func TestConnlogResumeAfterDisconnectAndRestart(t *testing.T) {
	dir := t.TempDir()
	boot := "boot-A"
	down := &fakeConnlogSender{handler: func(int, connlogEnvelope) (int64, error) {
		return 0, fmt.Errorf("connection refused")
	}}
	r1 := newTestConnlogReporter(t, dir, down, &boot, nil)
	r1.Enqueue(testOpenEvent("c1", 1000))
	r1.Enqueue(testCloseEvent("c1", 1000, 1600))
	r1.Enqueue(testOpenEvent("c2", 2000))
	r1.Enqueue(testCloseEvent("c2", 2000, 2900))
	r1.cycle(context.Background())

	if len(down.calls) == 0 {
		t.Fatal("面板不可达时应至少尝试发送一次")
	}
	// 事件必须已落盘：盘上状态里 Pending 含整批 4 条、顺序不变。
	st := readSpoolStateFromDisk(t, dir)
	if st.Pending == nil || len(st.Pending.Events) != 4 {
		t.Fatalf("断连后盘上 Pending 应含 4 条事件, got %+v", st.Pending)
	}
	for i, want := range []string{"c1", "c1", "c2", "c2"} {
		if st.Pending.Events[i].ConnID != want {
			t.Fatalf("盘上事件顺序错位: idx %d = %s, want %s", i, st.Pending.Events[i].ConnID, want)
		}
	}

	// 进程重启：新 reporter 从同一 spool 续报，全量送达不多不少。
	up := &fakeConnlogSender{}
	r2 := reopenTestConnlogReporter(t, dir, up, &boot, nil)
	r2.cycle(context.Background())
	if len(up.calls) != 1 {
		t.Fatalf("续报应只发一批, got %d 批", len(up.calls))
	}
	got := up.calls[0].Events
	if len(got) != 4 {
		t.Fatalf("续报事件数 = %d, want 4", len(got))
	}
	st = readSpoolStateFromDisk(t, dir)
	if st.Pending != nil || len(st.Buffered) != 0 {
		t.Fatalf("确认后续报状态应清空, pending=%v buffered=%d", st.Pending, len(st.Buffered))
	}
	if st.Next != 2 {
		t.Fatalf("确认后 Next 应为 2, got %d", st.Next)
	}
}

// ---- 2. 重复投递幂等：回执丢失后整批原样重报（同 stream/seq/digest） ----

func TestConnlogReplayIsByteIdentical(t *testing.T) {
	dir := t.TempDir()
	boot := "boot-A"
	attempts := 0
	sender := &fakeConnlogSender{handler: func(_ int, b connlogEnvelope) (int64, error) {
		attempts++
		if attempts <= 2 {
			return 0, fmt.Errorf("ack lost in transit") // 面板已入账、回执丢失
		}
		return b.Sequence, nil
	}}
	r := newTestConnlogReporter(t, dir, sender, &boot, nil)
	r.Enqueue(testOpenEvent("c1", 1000))
	r.Enqueue(testCloseEvent("c1", 1000, 1500))

	for i := 0; i < 3; i++ {
		r.retryAt = time.Time{} // 测试手动越过退避
		r.cycle(context.Background())
	}
	if len(sender.calls) != 3 {
		t.Fatalf("应发送 3 次（2 失败 1 成功）, got %d", len(sender.calls))
	}
	first := sender.calls[0]
	for i, c := range sender.calls[1:] {
		if c.Stream != first.Stream || c.Sequence != first.Sequence || c.Digest != first.Digest {
			t.Fatalf("重报 %d 与首发不一致: stream/seq/digest 必须全同 (first %s/%d/%s, got %s/%d/%s)",
				i+2, first.Stream, first.Sequence, first.Digest, c.Stream, c.Sequence, c.Digest)
		}
		if len(c.Events) != len(first.Events) || c.Events[0].ConnID != first.Events[0].ConnID {
			t.Fatalf("重报 %d 事件集与首发不一致", i+2)
		}
	}
	// 成功后不得再发：面板靠 (stream,seq,digest)+conn_id 双重幂等，
	// agent 侧靠确认后清 Pending。
	r.retryAt = time.Time{}
	r.cycle(context.Background())
	if len(sender.calls) != 3 {
		t.Fatalf("确认后不应再发送, got %d 次", len(sender.calls))
	}
}

// ---- 3. spool 上限：溢出丢最旧并计数，计数随批上行、确认后清零 ----

func TestConnlogSpoolCapDropsOldestAndReportsCount(t *testing.T) {
	dir := t.TempDir()
	boot := "boot-A"
	fail := true
	sender := &fakeConnlogSender{handler: func(_ int, b connlogEnvelope) (int64, error) {
		if fail {
			return 0, fmt.Errorf("panel down")
		}
		return b.Sequence, nil
	}}
	r := newTestConnlogReporter(t, dir, sender, &boot, func(c *connlogConfig) {
		c.spoolMax = 400 // 约容 2 条事件
		c.openThrottle = time.Nanosecond
	})
	for i := 0; i < 10; i++ {
		r.Enqueue(testOpenEvent(fmt.Sprintf("c%02d", i), int64(1000+i)))
	}
	r.cycle(context.Background())

	st := readSpoolStateFromDisk(t, dir)
	if st.Dropped <= 0 {
		t.Fatalf("超上限后 Dropped 应 > 0, got %d", st.Dropped)
	}
	if qb := st.queuedBytes(); qb > 400 {
		t.Fatalf("spool 占用 %d 字节超过上限 400", qb)
	}
	droppedTotal := st.Dropped
	// 丢的是最旧：留下的应是序号最大的那批 conn。
	for _, ev := range st.Pending.Events {
		if ev.ConnID < "c05" {
			t.Fatalf("留下的事件应为最新, 却见 %s（丢最旧口径被破坏）", ev.ConnID)
		}
	}

	fail = false
	r.retryAt = time.Time{}
	r.cycle(context.Background())
	// 承载丢弃计数的那一批必须已确认且计数与落盘一致。
	found := false
	for _, c := range sender.calls {
		if c.Dropped == droppedTotal {
			found = true
		}
	}
	if !found {
		t.Fatalf("没有任何已发批次携带 Dropped=%d（各批: %+v）", droppedTotal, sender.calls)
	}
	if got := r.DroppedPending(); got != 0 {
		t.Fatalf("确认后未报丢弃计数应清零, got %d", got)
	}
}

// ---- 4. boot 切换结清：上代遗留 open 批量 interrupted，end=最后已知时刻 ----

func TestConnlogBootChangeSettlesOpenAsInterrupted(t *testing.T) {
	dir := t.TempDir()
	boot := "boot-A"
	sender1 := &fakeConnlogSender{handler: func(int, connlogEnvelope) (int64, error) {
		return 0, fmt.Errorf("panel down")
	}}
	r1 := newTestConnlogReporter(t, dir, sender1, &boot, nil)
	r1.Enqueue(testOpenEvent("x1", 1000))
	r1.Enqueue(testOpenEvent("x2", 2000))
	r1.cycle(context.Background()) // 落盘进台账（纯 open 节流未组批也无妨）
	if st := readSpoolStateFromDisk(t, dir); len(st.Open) != 2 {
		t.Fatalf("台账应有 2 条未闭合, got %d", len(st.Open))
	}
	// r1 进程死亡（不发 close、不 Stop）。

	boot = "boot-B"
	sender2 := &fakeConnlogSender{}
	r2 := reopenTestConnlogReporter(t, dir, sender2, &boot, nil)
	r2.cycle(context.Background())
	if len(sender2.calls) == 0 {
		t.Fatal("结清事件应随首批上报")
	}
	events := sender2.calls[0].Events
	// 结清事件在最前（起始最早），按 start 排序：x1(1000) 先于 x2(2000)。
	var settles []ConnEvent
	for _, ev := range events {
		if ev.Type == ConnEventClose && ev.Status == ConnStatusInterrupted {
			settles = append(settles, ev)
		}
	}
	if len(settles) != 2 {
		t.Fatalf("应结清 2 条 interrupted, got %d（批次: %+v）", len(settles), events)
	}
	if settles[0].ConnID != "x1" || settles[1].ConnID != "x2" {
		t.Fatalf("结清顺序应按起始时刻: got %s, %s", settles[0].ConnID, settles[1].ConnID)
	}
	if settles[0].EndMs != 1000 || settles[1].EndMs != 2000 {
		t.Fatalf("interrupted 的 end 必须是最后已知时刻（=各自 start）, got %d / %d",
			settles[0].EndMs, settles[1].EndMs)
	}
	if st := readSpoolStateFromDisk(t, dir); len(st.Open) != 0 {
		t.Fatalf("结清后台账应为空, got %d", len(st.Open))
	}
}

// ---- 5. 三路拒绝自愈：invalid_batch 二分、identity_conflict 隔离留证 ----

func TestConnlogRecoverRejectedBisectAndQuarantine(t *testing.T) {
	dir := t.TempDir()
	boot := "boot-A"
	call := 0
	sender := &fakeConnlogSender{handler: func(_ int, b connlogEnvelope) (int64, error) {
		call++
		if call == 1 {
			return 0, connlogRejectionInvalidBatch
		}
		return b.Sequence, nil
	}}
	r := newTestConnlogReporter(t, dir, sender, &boot, nil)
	r.Enqueue(testOpenEvent("c1", 1000))
	r.Enqueue(testCloseEvent("c1", 1000, 1100))
	r.Enqueue(testOpenEvent("c2", 2000))
	r.Enqueue(testCloseEvent("c2", 2000, 2100))

	r.cycle(context.Background()) // 组批 4 条 → 被判 invalid_batch → 二分
	st := r.spool.state
	if st.Pending == nil || len(st.Pending.Events) != 2 {
		t.Fatalf("二分后 Pending 应含前半 2 条, got %+v", st.Pending)
	}
	if len(st.Retry) != 1 || len(st.Retry[0]) != 2 {
		t.Fatalf("二分后 Retry 应含后半 2 条, got %+v", st.Retry)
	}
	firstStream := sender.calls[0].Stream
	if st.Stream == firstStream {
		t.Fatal("二分自愈应轮换 stream")
	}

	r.cycle(context.Background()) // 发前半 → 确认
	r.cycle(context.Background()) // Retry 组批 → 发后半 → 确认
	delivered := map[string]int{}
	for i, c := range sender.calls {
		if i == 0 {
			continue // 被拒的那批不算送达
		}
		for _, ev := range c.Events {
			delivered[ev.ConnID+"/"+ev.Type]++
		}
	}
	for _, key := range []string{"c1/open", "c1/close", "c2/open", "c2/close"} {
		if delivered[key] != 1 {
			t.Fatalf("事件 %s 应恰送达 1 次, got %d", key, delivered[key])
		}
	}

	// identity_conflict：单条批次隔离留证、绝不重放、计入丢弃。
	dir2 := t.TempDir()
	sender2 := &fakeConnlogSender{handler: func(int, connlogEnvelope) (int64, error) {
		return 0, connlogRejectionIdentityConflict
	}}
	r2 := newTestConnlogReporter(t, dir2, sender2, &boot, nil)
	r2.Enqueue(testCloseEvent("q1", 1000, 1100))
	r2.cycle(context.Background())
	st2 := r2.spool.state
	if st2.Pending != nil {
		t.Fatal("隔离后 Pending 应清空")
	}
	if st2.Dropped != 1 {
		t.Fatalf("隔离事件应计入 Dropped=1, got %d", st2.Dropped)
	}
	archives, _ := filepath.Glob(filepath.Join(dir2, "*.rejected-*.json"))
	spoolName := connlogSpoolFileName("connlog-test-identity")
	archives, _ = filepath.Glob(filepath.Join(dir2, spoolName+".rejected-*.json"))
	if len(archives) != 1 {
		t.Fatalf("应有 1 份隔离留证, got %v", archives)
	}
	callsBefore := len(sender2.calls)
	r2.cycle(context.Background())
	if len(sender2.calls) != callsBefore {
		t.Fatal("隔离批次绝不重放")
	}
}

// ---- 6. open 节流：纯 open 批次等满 5s 级窗口才发，含 close 即时发 ----

func TestConnlogOpenThrottleAndClosePromptness(t *testing.T) {
	dir := t.TempDir()
	boot := "boot-A"
	sender := &fakeConnlogSender{}
	r := newTestConnlogReporter(t, dir, sender, &boot, func(c *connlogConfig) {
		c.openThrottle = time.Hour
	})
	r.Enqueue(testOpenEvent("c1", 1000))
	r.cycle(context.Background())
	if len(sender.calls) != 0 {
		t.Fatal("纯 open 未满节流窗口不应发送")
	}
	r.bufferedSince = time.Now().Add(-2 * time.Hour) // 模拟窗口已满
	r.cycle(context.Background())
	if len(sender.calls) != 1 {
		t.Fatal("节流窗口满后纯 open 批次应发送")
	}

	r.Enqueue(testCloseEvent("c1", 1000, 1500))
	r.cycle(context.Background())
	if len(sender.calls) != 2 {
		t.Fatal("含 close 的批次应即时发送（不等节流窗口）")
	}
}

// ---- 7. 线上契约：AES 信封、路径改写、回执 stream+sequence 对齐、三路拒绝 ----

func TestConnlogWireContract(t *testing.T) {
	secret := "connlog-test-secret"
	oldURL, oldCrypto, oldDo := httpReportURL, httpAESCrypto, reportDo
	defer func() { httpReportURL, httpAESCrypto, reportDo = oldURL, oldCrypto, oldDo }()
	SetHTTPReportURL("panel.example.com:8443", secret)

	batch := connlogEnvelope{
		Stream: "0123456789abcdef0123456789abcdef", Sequence: 7,
		BootID: "boot-A", Dropped: 3,
		Events: []ConnEvent{testOpenEvent("c1", 1000)},
	}
	batch.Digest = connlogBatchDigest(batch.Events)

	var gotPath string
	var gotBatch connlogEnvelope
	reportDo = func(_ context.Context, req *http.Request, _ time.Duration) (*http.Response, error) {
		gotPath = req.URL.Path
		body, _ := io.ReadAll(req.Body)
		var envelope struct {
			Encrypted bool   `json:"encrypted"`
			Data      string `json:"data"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil || !envelope.Encrypted {
			t.Errorf("上报体应为 AES 信封: %v", err)
		}
		aes, _ := crypto.NewAESCrypto(secret)
		plain, err := aes.Decrypt(envelope.Data)
		if err != nil {
			t.Errorf("上报体解密失败: %v", err)
		}
		if err := json.Unmarshal(plain, &gotBatch); err != nil {
			t.Errorf("批次 JSON 解析失败: %v", err)
		}
		ack, _ := json.Marshal(map[string]any{"stream": batch.Stream, "sequence": batch.Sequence, "cursor": batch.Sequence})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(ack)))}, nil
	}
	cursor, err := sendConnlogBatch(context.Background(), batch)
	if err != nil {
		t.Fatalf("sendConnlogBatch: %v", err)
	}
	if cursor != batch.Sequence {
		t.Fatalf("cursor = %d, want %d", cursor, batch.Sequence)
	}
	if gotPath != "/flow/connlog/v2" {
		t.Fatalf("上报路径 = %s, want /flow/connlog/v2", gotPath)
	}
	if gotBatch.Digest != batch.Digest || gotBatch.BootID != "boot-A" || gotBatch.Dropped != 3 || len(gotBatch.Events) != 1 {
		t.Fatalf("线上批次字段不符: %+v", gotBatch)
	}

	// 回执与批次不对齐 → 普通失败（保留重报），不是拒绝。
	reportDo = func(_ context.Context, req *http.Request, _ time.Duration) (*http.Response, error) {
		ack, _ := json.Marshal(map[string]any{"stream": batch.Stream, "sequence": batch.Sequence + 1, "cursor": 0})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(ack)))}, nil
	}
	if _, err := sendConnlogBatch(context.Background(), batch); err == nil {
		t.Fatal("回执 sequence 不对齐应报错")
	} else {
		var rej connlogReportRejection
		if errors.As(err, &rej) {
			t.Fatalf("回执不对齐不应判为拒绝, got %v", rej)
		}
	}

	// 三路拒绝：409 reset_stream。
	reportDo = func(_ context.Context, req *http.Request, _ time.Duration) (*http.Response, error) {
		ack, _ := json.Marshal(map[string]any{"stream": batch.Stream, "sequence": batch.Sequence, "error": "reset_stream"})
		return &http.Response{StatusCode: http.StatusConflict, Body: io.NopCloser(strings.NewReader(string(ack)))}, nil
	}
	_, err = sendConnlogBatch(context.Background(), batch)
	var rej connlogReportRejection
	if !errors.As(err, &rej) || rej != connlogRejectionResetStream {
		t.Fatalf("409 reset_stream 应判为拒绝, got %v", err)
	}
}
