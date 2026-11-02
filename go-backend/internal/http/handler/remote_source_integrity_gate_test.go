package handler

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// 订阅同步完整性门：本轮出现任何解析/转换/写入错误时，未匹配的旧
// 节点一律保留（删除推迟到下一轮干净同步）；整轮无这类错误时删除
// 照常。展示专用提示（F-025 重复条目）不计入门——见对应用例。
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

// 批内去重（F-025）的重复条目提示只进展示错误集、不计入完整性门：
// 重复条目每个都被处理过（首条入册、余者跳过），不是未处理条目。
// 计入门会让含重复条目的源每轮必然再现该提示、永远等不到干净轮、
// 已下线节点永不回收——故这类轮次删除照常。
func TestSyncIntegrityGateDuplicateEntryDoesNotDeferDeletion(t *testing.T) {
	r, h, src, old := integrityGateSetup(t)

	body := integrityLinkA + "\n" + integrityLinkA
	res, err := h.syncRemoteSource(context.Background(), src, body)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if len(res.Errors) == 0 {
		t.Fatal("duplicate entry must still be recorded in res.Errors for display")
	}
	if res.KeptStale != 0 {
		t.Fatalf("duplicate-entry round: res.KeptStale = %d, want 0 (kept=%d)", res.KeptStale, res.Kept)
	}
	if res.Removed != 1 {
		t.Fatalf("duplicate-entry round: res.Removed = %d, want 1", res.Removed)
	}
	if row, _ := r.GetNodeInbound(old.ID); row != nil {
		t.Fatalf("duplicate-entry round: unmatched node must be deleted, still present: %+v", row)
	}
}

// 多轮收敛：源长期含重复条目时，提供方下线的节点必须在下线当轮就被
// 回收，而不是等一个永远不会出现的「无重复轮」。
func TestSyncIntegrityGateDuplicateSourceRecyclesRemovedNode(t *testing.T) {
	r, h, src, old := integrityGateSetup(t)

	// 第一轮：A 与 old 都在源里，old 条目重复出现一次（提示照常产生）。
	body := integrityLinkA + "\n" + integrityLinkOld + "\n" + integrityLinkOld
	res, err := h.syncRemoteSource(context.Background(), src, body)
	if err != nil {
		t.Fatalf("round 1 sync: %v", err)
	}
	if len(res.Errors) == 0 {
		t.Fatal("round 1: duplicate notice must be recorded")
	}
	if res.Removed != 0 {
		t.Fatalf("round 1: res.Removed = %d, want 0 (old still in source)", res.Removed)
	}
	if row, err := r.GetNodeInbound(old.ID); err != nil || row == nil {
		t.Fatalf("round 1: old must be kept while still in source, got row=%v err=%v", row, err)
	}

	// 第二轮：提供方下线 old，源里只剩 A（仍带重复条目）。
	body = integrityLinkA + "\n" + integrityLinkA
	res, err = h.syncRemoteSource(context.Background(), src, body)
	if err != nil {
		t.Fatalf("round 2 sync: %v", err)
	}
	if res.Removed != 1 || res.KeptStale != 0 {
		t.Fatalf("round 2: removed=%d keptStale=%d, want removed=1 keptStale=0 (errors=%v)", res.Removed, res.KeptStale, res.Errors)
	}
	if row, _ := r.GetNodeInbound(old.ID); row != nil {
		t.Fatalf("round 2: provider-removed node must be recycled, still present: %+v", row)
	}
	if n := sourceRowCount(t, r, src.ID); n != 1 {
		t.Fatalf("round 2: only the matched row must remain, got %d", n)
	}
}

// 超量截断与重复提示不同：截断意味着确有条目未被处理，仍计入门、
// 推迟删除，计数进 KeptStale。
func TestSyncIntegrityGateTruncationStillDefersDeletion(t *testing.T) {
	r, h, src, old := integrityGateSetup(t)

	var b strings.Builder
	b.WriteString(integrityLinkA + "\n")
	for i := 0; i < maxImportNodes; i++ {
		fmt.Fprintf(&b, "vless://11111111-2222-3333-4444-555555555555@198.51.100.%d:443?security=none#bulk-%d\n", i%250+1, i)
	}
	res, err := h.syncRemoteSource(context.Background(), src, b.String())
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	truncated := false
	for _, e := range res.Errors {
		if strings.Contains(e, "只保留前") {
			truncated = true
		}
	}
	if !truncated {
		t.Fatalf("truncation notice must be recorded, errors=%v", res.Errors)
	}
	if res.Removed != 0 || res.KeptStale != 1 {
		t.Fatalf("truncated round: removed=%d keptStale=%d, want removed=0 keptStale=1", res.Removed, res.KeptStale)
	}
	if row, err := r.GetNodeInbound(old.ID); err != nil || row == nil {
		t.Fatalf("truncated round: unmatched node must be KEPT, got row=%v err=%v", row, err)
	}
}

// 摘要必须区分两种保留原因：中转守卫保留（Kept）与完整性门保留
// （KeptStale）各表述，不得再把门保留一律说成「有中转，已保留」。
func TestRemoteSyncResultSummaryDistinguishesKeptReasons(t *testing.T) {
	s := remoteSyncResult{Kept: 2, KeptStale: 3}.summary()
	if !strings.Contains(s, "2 个已不在订阅里但有中转，已保留") {
		t.Fatalf("summary must keep the relay wording for Kept: %s", s)
	}
	if !strings.Contains(s, "3 个未匹配节点因本轮同步不完整，已保留待下轮确认") {
		t.Fatalf("summary must name the gate wording for KeptStale: %s", s)
	}
	zero := remoteSyncResult{}
	if got := zero.summary(); strings.Contains(got, "已保留") {
		t.Fatalf("zero keeps must not mention keeping: %s", got)
	}
}
