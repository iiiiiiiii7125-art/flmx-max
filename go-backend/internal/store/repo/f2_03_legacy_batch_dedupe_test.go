package repo

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// F2-03: legacy (no-seq) v1 batches carry no replay protection, so a
// captured batch could be re-POSTed verbatim and booked again on
// every replay (live-d scenario 5). The fix claims the batch's
// content hash in the accounting transaction itself: a second apply
// of the same (node, hash) fails with ErrLegacyBatchReplay and the
// whole transaction rolls back.
func TestLegacyBatchContentDedupeF203(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "f203.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()
	now := time.Now()
	nowMs := now.UnixMilli()
	if err := r.DB().Create(&model.User{ID: 2, User: "f203-user", Pwd: "pwd", RoleID: 1, ExpTime: 2727251700000, Flow: 99999, Num: 99999, CreatedTime: nowMs, Status: 1}).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := r.DB().Create(&model.Tunnel{ID: 1, Name: "tunnel-1", TrafficRatio: 1, Type: 1, Protocol: "tls", Flow: 1, CreatedTime: nowMs, UpdatedTime: nowMs, Status: 1}).Error; err != nil {
		t.Fatalf("seed tunnel: %v", err)
	}
	if err := r.DB().Create(&model.UserTunnel{ID: 10, UserID: 2, TunnelID: 1, Num: 99999, Flow: 0, ExpTime: 2727251700000, Status: 1}).Error; err != nil {
		t.Fatalf("seed user tunnel: %v", err)
	}
	if err := r.DB().Create(&model.Forward{ID: 20, UserID: 2, UserName: "f203-user", Name: "forward-20", TunnelID: 1, RemoteAddr: "1.1.1.1:80", Strategy: "fifo", CreatedTime: nowMs, UpdatedTime: nowMs, Status: 1}).Error; err != nil {
		t.Fatalf("seed forward: %v", err)
	}
	deltas := []FlowUploadCounterDelta{{ForwardID: 20, UserID: 2, UserTunnelID: 10, InFlow: 50, OutFlow: 100}}
	usages := map[int64]int64{2: 150}
	legacyAdv := &FlowSeqAdvance{NodeID: 9, Seq: nil, LegacyBatchHash: "hash-1"}

	if _, err := r.ApplyFlowUploadAccounting(deltas, usages, now, legacyAdv); err != nil {
		t.Fatalf("first legacy apply: %v", err)
	}
	// Verbatim replay: same node, same content hash → rejected, and
	// the accounting transaction rolls back completely.
	if _, err := r.ApplyFlowUploadAccounting(deltas, usages, now, legacyAdv); !errors.Is(err, ErrLegacyBatchReplay) {
		t.Fatalf("replayed legacy apply: err=%v, want ErrLegacyBatchReplay", err)
	}
	var fwd model.Forward
	if err := r.DB().First(&fwd, 20).Error; err != nil {
		t.Fatal(err)
	}
	if fwd.InFlow != 50 || fwd.OutFlow != 100 {
		t.Fatalf("F2-03: forward double-accounted after replay: in=%d out=%d, want 50/100", fwd.InFlow, fwd.OutFlow)
	}

	// A different batch (different hash) from the same node books.
	otherAdv := &FlowSeqAdvance{NodeID: 9, Seq: nil, LegacyBatchHash: "hash-2"}
	if _, err := r.ApplyFlowUploadAccounting(deltas, usages, now, otherAdv); err != nil {
		t.Fatalf("distinct legacy apply: %v", err)
	}
	if err := r.DB().First(&fwd, 20).Error; err != nil {
		t.Fatal(err)
	}
	if fwd.InFlow != 100 || fwd.OutFlow != 200 {
		t.Fatalf("F2-03: distinct batch not booked: in=%d out=%d, want 100/200", fwd.InFlow, fwd.OutFlow)
	}

	// The same hash from a different node is a different claim.
	foreignAdv := &FlowSeqAdvance{NodeID: 10, Seq: nil, LegacyBatchHash: "hash-1"}
	if _, err := r.ApplyFlowUploadAccounting(deltas, usages, now, foreignAdv); err != nil {
		t.Fatalf("other-node same-hash apply: %v", err)
	}

	// Retention: a claim older than the window no longer blocks —
	// the claim step prunes it and the batch books again.
	staleAdv := &FlowSeqAdvance{NodeID: 9, Seq: nil, LegacyBatchHash: "hash-stale"}
	if err := r.DB().Create(&FlowLegacyBatchDedupe{NodeID: 9, BatchHash: "hash-stale", FirstSeen: nowMs - FlowLegacyBatchDedupeRetention - 1}).Error; err != nil {
		t.Fatalf("seed stale claim: %v", err)
	}
	if _, err := r.ApplyFlowUploadAccounting(deltas, usages, now, staleAdv); err != nil {
		t.Fatalf("stale-claim apply: %v", err)
	}
	var staleRows int64
	if err := r.DB().Model(&FlowLegacyBatchDedupe{}).Where("node_id = ? AND batch_hash = ?", 9, "hash-stale").Count(&staleRows).Error; err != nil {
		t.Fatal(err)
	}
	if staleRows != 1 {
		t.Fatalf("F2-03: stale claim rows=%d, want 1 (pruned and re-claimed)", staleRows)
	}

	// Seq'd batches never touch the dedupe table even if a hash is
	// supplied: the seq advance is their replay protection.
	seq := int64(3)
	seqAdv := &FlowSeqAdvance{NodeID: 9, Seq: &seq, LegacyBatchHash: "hash-seq"}
	if _, err := r.ApplyFlowUploadAccounting(deltas, usages, now, seqAdv); err != nil {
		t.Fatalf("seq apply: %v", err)
	}
	var seqRows int64
	if err := r.DB().Model(&FlowLegacyBatchDedupe{}).Where("batch_hash = ?", "hash-seq").Count(&seqRows).Error; err != nil {
		t.Fatal(err)
	}
	if seqRows != 0 {
		t.Fatalf("F2-03: seq batch left %d dedupe rows, want 0", seqRows)
	}
}
