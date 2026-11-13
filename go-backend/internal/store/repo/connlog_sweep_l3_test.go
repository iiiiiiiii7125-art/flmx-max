package repo

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// SWEEP-CPU L3-02 回归：ApplyConnlogStreamBatch 按 200 事件分
// tx 应用。450 事件一批跨 3 个 chunk：open 落 chunk 1、close
// 落 chunk 2（跨 chunk 状态机必须与单事务版一致——同连接合
// 并为一行 closed），重放/冲突/缺口口径不变。

func TestConnlogStreamBatchChunkedAppliesAcrossChunks(t *testing.T) {
	r := newConnlogTestRepo(t)
	scope := "connlog:3:stream-chunked"
	base := int64(1_700_000_000_000)
	now := time.UnixMilli(base + 60_000)

	events := make([]ConnlogEvent, 0, 450)
	for i := 0; i < 200; i++ {
		events = append(events, ConnlogEvent{Type: "open", ConnID: fmt.Sprintf("c%03d", i),
			EntryNodeID: 3, EntryKind: ConnlogEntryInbound, EntryID: "in-7",
			ClientIP: "203.0.113.1", Network: "tcp", SessionKind: "tcp_conn",
			StartTime: base + int64(i)*1000})
	}
	for i := 0; i < 200; i++ {
		events = append(events, ConnlogEvent{Type: "close", ConnID: fmt.Sprintf("c%03d", i),
			EntryNodeID: 3, EntryKind: ConnlogEntryInbound, EntryID: "in-7",
			EndTime: base + int64(i)*1000 + 30_000})
	}
	for i := 200; i < 250; i++ {
		events = append(events, ConnlogEvent{Type: "open", ConnID: fmt.Sprintf("c%03d", i),
			EntryNodeID: 3, EntryKind: ConnlogEntryInbound, EntryID: "in-7",
			ClientIP: "203.0.113.2", Network: "tcp", SessionKind: "tcp_conn",
			StartTime: base + int64(i)*1000})
	}
	digest := connlogDigest("chunked-batch-1")

	ack, err := r.ApplyConnlogStreamBatch(scope, 1, digest, now, events)
	if err != nil {
		t.Fatalf("apply 450-event batch: %v", err)
	}
	if !ack.Applied || ack.Cursor != 1 {
		t.Fatalf("ack = %+v, want applied with cursor 1", ack)
	}
	rows := connlogRows(t, r)
	if len(rows) != 250 {
		t.Fatalf("rows = %d, want 250 (200 open+close pairs merged, 50 opens)", len(rows))
	}
	byConn := map[string]model.InboundConnLog{}
	for _, row := range rows {
		byConn[row.ConnID] = row
	}
	for i := 0; i < 200; i++ {
		row := byConn[fmt.Sprintf("c%03d", i)]
		if row.Status != ConnlogStatusClosed || row.DurationMs != 30_000 {
			t.Fatalf("c%03d = %+v, want closed with duration 30000 (close in chunk 2 must apply to open in chunk 1)", i, row)
		}
	}
	for i := 200; i < 250; i++ {
		row := byConn[fmt.Sprintf("c%03d", i)]
		if row.Status != ConnlogStatusOpen {
			t.Fatalf("c%03d = %+v, want still open", i, row)
		}
	}

	// 真重放：同序号同摘要 → 确认、不重应用、不增行。
	ack, err = r.ApplyConnlogStreamBatch(scope, 1, digest, now, events)
	if err != nil {
		t.Fatalf("replay chunked batch: %v", err)
	}
	if ack.Applied || !ack.Replay || ack.Cursor != 1 {
		t.Fatalf("replay ack = %+v, want replay with cursor 1 and no apply", ack)
	}
	if rows := connlogRows(t, r); len(rows) != 250 {
		t.Fatalf("after replay rows = %d, want 250", len(rows))
	}

	// 同序号不同摘要 → 冲突；序号跳跃 → 缺口（期望 2 收到 3）。
	if _, err := r.ApplyConnlogStreamBatch(scope, 1, connlogDigest("chunked-batch-1-mutated"), now, events); !errors.Is(err, ErrTrafficStreamConflict) {
		t.Fatalf("mutated digest err = %v, want ErrTrafficStreamConflict", err)
	}
	var gap *TrafficStreamGap
	if _, err := r.ApplyConnlogStreamBatch(scope, 3, connlogDigest("chunked-batch-3"), now, events); !errors.As(err, &gap) || gap.Expected != 2 {
		t.Fatalf("gap err = %v, want TrafficStreamGap{Expected:2}", err)
	}
	if rows := connlogRows(t, r); len(rows) != 250 {
		t.Fatalf("after conflict/gap rows = %d, want 250 (rejected batches must not apply)", len(rows))
	}
}

// SWEEP-CPU L3-02 判别：分段提交的续接语义。450 事件一批，
// 第 250 个事件为非法类型（对应连接已在 chunk 1 建行，upsert
// 报 unknown event type）→ chunk 2 回滚、chunk 1 已提交、游
// 标未推进；修正后同序号重报可续接补齐。旧单事务版遇此错整
// 批回滚（0 行），本测试在旧代码上变红。
func TestConnlogStreamBatchChunkedResumesAfterMidBatchFailure(t *testing.T) {
	r := newConnlogTestRepo(t)
	scope := "connlog:4:stream-resume"
	base := int64(1_700_000_000_000)
	now := time.UnixMilli(base + 60_000)

	build := func(bad bool) []ConnlogEvent {
		events := make([]ConnlogEvent, 0, 450)
		for i := 0; i < 200; i++ {
			events = append(events, ConnlogEvent{Type: "open", ConnID: fmt.Sprintf("c%03d", i),
				EntryNodeID: 4, EntryKind: ConnlogEntryInbound, EntryID: "in-7",
				ClientIP: "203.0.113.1", Network: "tcp", SessionKind: "tcp_conn",
				StartTime: base + int64(i)*1000})
		}
		for i := 0; i < 200; i++ {
			ev := ConnlogEvent{Type: "close", ConnID: fmt.Sprintf("c%03d", i),
				EntryNodeID: 4, EntryKind: ConnlogEntryInbound, EntryID: "in-7",
				EndTime: base + int64(i)*1000 + 30_000}
			if bad && i == 50 {
				ev.Type = "bogus"
			}
			events = append(events, ev)
		}
		for i := 200; i < 250; i++ {
			events = append(events, ConnlogEvent{Type: "open", ConnID: fmt.Sprintf("c%03d", i),
				EntryNodeID: 4, EntryKind: ConnlogEntryInbound, EntryID: "in-7",
				ClientIP: "203.0.113.2", Network: "tcp", SessionKind: "tcp_conn",
				StartTime: base + int64(i)*1000})
		}
		return events
	}

	// 坏批必须报错，且 chunk 1 的 200 个 open 已落盘、游标未推进。
	if _, err := r.ApplyConnlogStreamBatch(scope, 1, connlogDigest("resume-bad"), now, build(true)); err == nil {
		t.Fatal("batch with a bogus event must fail")
	}
	if rows := connlogRows(t, r); len(rows) != 200 {
		t.Fatalf("after mid-batch failure rows = %d, want 200 (chunk 1 committed, chunk 2 rolled back)", len(rows))
	}
	var cursor TrafficStreamCursor
	if err := r.db.Where("scope = ?", scope).First(&cursor).Error; err != nil {
		t.Fatalf("cursor row must exist after first chunk: %v", err)
	}
	if cursor.Sequence != 0 {
		t.Fatalf("cursor sequence = %d after failed batch, want 0 (not advanced)", cursor.Sequence)
	}

	// 修正后同序号重报：续接补齐，游标推进到 1，全批落地。
	ack, err := r.ApplyConnlogStreamBatch(scope, 1, connlogDigest("resume-fixed"), now, build(false))
	if err != nil {
		t.Fatalf("corrected re-apply: %v", err)
	}
	if !ack.Applied || ack.Cursor != 1 {
		t.Fatalf("corrected ack = %+v, want applied with cursor 1", ack)
	}
	rows := connlogRows(t, r)
	if len(rows) != 250 {
		t.Fatalf("after corrected re-apply rows = %d, want 250", len(rows))
	}
	byConn := map[string]model.InboundConnLog{}
	for _, row := range rows {
		byConn[row.ConnID] = row
	}
	if row := byConn["c050"]; row.Status != ConnlogStatusClosed || row.DurationMs != 30_000 {
		t.Fatalf("c050 = %+v, want closed with duration 30000 after resume", row)
	}
}
