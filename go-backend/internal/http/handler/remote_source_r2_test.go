package handler

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R2-017（B-sub-06）：同步截断改在批内去重后判定——501 条/500 唯一
// （溢出纯为重复条目）不再触发截断门，下线节点当轮回收。修复前：
// 截断按去重前计数，本轮恒 KeptStale=1、Removed=0。
func TestSyncDedupeBeforeCapRecyclesInDupOverflowRound(t *testing.T) {
	r, h, src, old := integrityGateSetup(t)

	var b strings.Builder
	b.WriteString(integrityLinkA + "\n")
	for i := 0; i < maxImportNodes-1; i++ {
		fmt.Fprintf(&b, "vless://11111111-2222-3333-4444-555555555555@198.51.%d.%d:443?security=none#bulk-%d\n", i/250+1, i%250+1, i)
	}
	// 第 501 条为 bulk-0 的全量重复（同名同址）：原始 501、唯一 500。
	b.WriteString("vless://11111111-2222-3333-4444-555555555555@198.51.1.1:443?security=none#bulk-0\n")

	res, err := h.syncRemoteSource(context.Background(), src, b.String())
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	dupNotice := false
	for _, e := range res.Errors {
		if strings.Contains(e, "只保留前") {
			t.Fatalf("dup-only overflow must not trigger the truncation gate, errors=%v", res.Errors)
		}
		if strings.Contains(e, "重复条目") {
			dupNotice = true
		}
	}
	if !dupNotice {
		t.Fatalf("duplicate notice must still be recorded for display, errors=%v", res.Errors)
	}
	if res.Removed != 1 || res.KeptStale != 0 {
		t.Fatalf("removed=%d keptStale=%d, want removed=1 keptStale=0 (offline node recycled in the same round)", res.Removed, res.KeptStale)
	}
	if row, _ := r.GetNodeInbound(old.ID); row != nil {
		t.Fatalf("offline node must be recycled in a dup-overflow round, still present: %+v", row)
	}
	if n := sourceRowCount(t, r, src.ID); n != maxImportNodes {
		t.Fatalf("source rows = %d, want %d unique nodes", n, maxImportNodes)
	}
}

// R2-018（B-sub-07）：守卫查询失败的保留单独计 KeptUnknown，摘要如实
// 表述「无法确认」，不得再与「有中转，已保留」混同。故障注入与
// R4B7 同法（改名 forward 表使 ListInboundRelays 单独失败）。
func TestSyncGuardFailureCountsKeptUnknown(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "r2kept.db"))
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
	victim := seedExternalRow(t, r, integrityLinkOld, src.ID, now)

	if err := r.DB().Exec("ALTER TABLE forward RENAME TO forward_r2_bak").Error; err != nil {
		t.Fatalf("rename forward: %v", err)
	}
	defer func() { _ = r.DB().Exec("ALTER TABLE forward_r2_bak RENAME TO forward").Error }()
	if _, err := r.ListInboundRelays(victim.ID); err == nil {
		t.Fatal("injection sanity: ListInboundRelays must fail after forward rename")
	}

	res, err := h.syncRemoteSource(context.Background(), src, integrityLinkA)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	_ = r.DB().Exec("ALTER TABLE forward_r2_bak RENAME TO forward")

	if row, err := r.GetNodeInbound(victim.ID); err != nil || row == nil {
		t.Fatalf("guard-failure keep: victim must survive, got row=%v err=%v", row, err)
	}
	if res.KeptUnknown != 1 || res.Kept != 0 || res.Removed != 0 {
		t.Fatalf("keptUnknown=%d kept=%d removed=%d, want 1/0/0 (errors=%v)", res.KeptUnknown, res.Kept, res.Removed, res.Errors)
	}
	s := res.summary()
	if !strings.Contains(s, "因中转查询失败无法确认") {
		t.Fatalf("summary must name the unknown-relay reason: %s", s)
	}
	if strings.Contains(s, "有中转，已保留") {
		t.Fatalf("summary must not claim relays exist for a guard failure: %s", s)
	}
}

// R2-018 摘要三子因各表述：Kept（有中转/链式引用）、KeptUnknown
// （查询失败未知）、KeptStale（完整性门）同现时互不混同。
func TestRemoteSyncResultSummaryThreeKeptReasons(t *testing.T) {
	s := remoteSyncResult{Kept: 1, KeptUnknown: 2, KeptStale: 3}.summary()
	for _, want := range []string{
		"1 个已不在订阅里但有中转，已保留",
		"2 个已不在订阅里的节点因中转查询失败无法确认，已保留待下轮确认",
		"3 个未匹配节点因本轮同步不完整，已保留待下轮确认",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("summary missing %q in %q", want, s)
		}
	}
}

// R2-033（B-sub-05）：federationConsumerVersionWarning 的两处吞错点
// （提取失败/比较失败）落警告日志，判定与返回值一字不变（仍维持
// 「不可解析不臆断、不警告」的 R4-N1 口径）。
func TestFederationConsumerVersionWarningLogsSwallowedFailures(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	got := federationConsumerVersionWarning("not-a-version!!")
	log.SetOutput(old)
	if got != "" {
		t.Fatalf("unparseable consumer version must still not warn, got %q", got)
	}
	if !strings.Contains(buf.String(), "version warning skipped") {
		t.Fatalf("extraction failure must be logged, log=%q", buf.String())
	}
	// 判定不变对照组。
	if got := federationConsumerVersionWarning("1.3.3"); got == "" {
		t.Error("below-baseline consumer version must warn")
	}
	if got := federationConsumerVersionWarning("1.3.3 (debian/amd64)"); got == "" {
		t.Error("below-baseline consumer version with suffix must warn")
	}
	if got := federationConsumerVersionWarning("1.3.4"); got != "" {
		t.Errorf("baseline consumer version must not warn, got %q", got)
	}
	if got := federationConsumerVersionWarning("dev"); got != "" {
		t.Errorf("dev consumer version must not warn, got %q", got)
	}
	if got := federationConsumerVersionWarning(""); got == "" {
		t.Error("unreported consumer version must warn")
	}
}
