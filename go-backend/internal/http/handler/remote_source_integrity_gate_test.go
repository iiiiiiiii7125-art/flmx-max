package handler

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// 订阅同步完整性门：本轮出现任何解析/写入错误时，未匹配的旧节点
// 一律保留（删除推迟到下一轮干净同步）；整轮零错误时删除照常。
// 场景搭建与 r4_b7_guard_test 同口径：文件源 + ParseImport 播种行。

const (
	integrityLinkA   = "vless://11111111-2222-3333-4444-555555555555@203.0.113.10:443?security=none#node-a"
	integrityLinkOld = "vless://11111111-2222-3333-4444-555555555555@203.0.113.20:443?security=none#node-old"
)

func integrityGateSetup(t *testing.T) (*repo.Repository, *Handler, *model.RemoteSource, *model.NodeInbound) {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "integrity.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	now := time.Now().UnixMilli()
	src := &model.RemoteSource{Name: "源", Kind: model.RemoteSourceFile, Enabled: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.SaveRemoteSource(src); err != nil {
		t.Fatalf("save source: %v", err)
	}
	seedExternalRow(t, r, integrityLinkA, src.ID, now)
	old := seedExternalRow(t, r, integrityLinkOld, src.ID, now)
	return r, h, src, old
}

func sourceRowCount(t *testing.T, r *repo.Repository, sourceID int64) int {
	t.Helper()
	rows, err := r.ListNodeInbounds()
	if err != nil {
		t.Fatalf("list inbounds: %v", err)
	}
	n := 0
	for _, row := range rows {
		if row.SourceID == sourceID {
			n++
		}
	}
	return n
}

// 部分解析失败（有效节点 + 一行坏内容）时，未匹配的旧节点必须保留：
// 坏行对应的可能正是旧节点的新形态，本轮无法证明它已被源删除。
func TestSyncIntegrityGateKeepsUnmatchedOnPartialParseFailure(t *testing.T) {
	r, h, src, old := integrityGateSetup(t)

	body := integrityLinkA + "\n" + "broken-entry-not-a-link"
	res, err := h.syncRemoteSource(context.Background(), src, body)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if len(res.Errors) == 0 {
		t.Fatalf("partial parse failure must be recorded in res.Errors, got none (added=%d updated=%d)", res.Added, res.Updated)
	}
	if res.Removed != 0 {
		t.Fatalf("integrity gate: res.Removed = %d, want 0 (errors=%v)", res.Removed, res.Errors)
	}
	if row, err := r.GetNodeInbound(old.ID); err != nil || row == nil {
		t.Fatalf("integrity gate: unmatched node must be KEPT on a partial-failure round, got row=%v err=%v", row, err)
	}
	if n := sourceRowCount(t, r, src.ID); n != 2 {
		t.Fatalf("both source rows must survive, got %d", n)
	}
}

// 对照组：整轮零错误时，未匹配的旧节点照常删除——门不得过度拦截。
func TestSyncIntegrityGateCleanRoundStillDeletes(t *testing.T) {
	r, h, src, old := integrityGateSetup(t)

	res, err := h.syncRemoteSource(context.Background(), src, integrityLinkA)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("clean round must have no errors, got %v", res.Errors)
	}
	if res.Removed != 1 {
		t.Fatalf("clean round: res.Removed = %d, want 1", res.Removed)
	}
	if row, _ := r.GetNodeInbound(old.ID); row != nil {
		t.Fatalf("clean round: unmatched node must be deleted, still present: %+v", row)
	}
	if n := sourceRowCount(t, r, src.ID); n != 1 {
		t.Fatalf("only the matched row must remain, got %d", n)
	}
}

// 批内去重（F-025）的重复条目提示同样计入错误：这类轮次也推迟删除
// （保守口径，与完整性门同一判定）。
func TestSyncIntegrityGateDuplicateEntryDefersDeletion(t *testing.T) {
	r, h, src, old := integrityGateSetup(t)

	body := integrityLinkA + "\n" + integrityLinkA
	res, err := h.syncRemoteSource(context.Background(), src, body)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if len(res.Errors) == 0 {
		t.Fatal("duplicate entry must be recorded in res.Errors")
	}
	if res.Removed != 0 {
		t.Fatalf("duplicate-entry round: res.Removed = %d, want 0", res.Removed)
	}
	if row, err := r.GetNodeInbound(old.ID); err != nil || row == nil {
		t.Fatalf("duplicate-entry round: unmatched node must be KEPT, got row=%v err=%v", row, err)
	}
}
