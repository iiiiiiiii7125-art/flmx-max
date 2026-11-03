package handler

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// F6-02 钉格：删除屏蔽登记随 inbounds 段备份往返——管理员手工删
// 过的导入节点在全量恢复后，订阅源下一轮同步不得复活（修复前：
// 屏蔽不入任何备份段，恢复后同步以新行复活，与 subscription.md
// 的粘性承诺不符且回执无披露）。同批钉：节点组段（已随功能移除）
// 在旧备份导入时只计数点名披露、订阅 groupIds 引用被忽略。

func TestShieldSurvivesBackupRestoreAndBlocksResurrection(t *testing.T) {
	r, h, src := r10ShieldSetup(t)

	res := r10SyncOne(t, h, src, r10LinkA)
	if res.Added != 1 {
		t.Fatalf("sync 1: added=%d, want 1 (errors=%v)", res.Added, res.Errors)
	}
	row := r10OnlyRow(t, r, src.ID)
	if row == nil {
		t.Fatal("sync 1: row must exist")
	}
	out := r10DeleteInbound(t, h, row.ID)
	if out.Code != 0 {
		t.Fatalf("delete: code=%d msg=%q", out.Code, out.Msg)
	}

	// 导出并过一遍 JSON 序列化边界（备份文件的真实形态）。
	backup, err := r.ExportAll()
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if backup.Inbounds == nil || len(backup.Inbounds.Shields) != 1 {
		t.Fatalf("exported inbounds segment must carry the shield, got %+v", backup.Inbounds)
	}
	raw, err := json.Marshal(backup)
	if err != nil {
		t.Fatalf("marshal backup: %v", err)
	}
	var decoded model.BackupData
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal backup: %v", err)
	}

	// 全新库恢复（走 Gist 恢复的 13 段全集，与真实恢复同路径）。
	dest, err := repo.Open(filepath.Join(t.TempDir(), "shield-restore.db"))
	if err != nil {
		t.Fatalf("open dest repo: %v", err)
	}
	t.Cleanup(func() { _ = dest.Close() })
	result, err := dest.Import(&decoded, gistRestoreTypes)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.InboundShieldsImported != 1 {
		t.Fatalf("receipt must count the restored shield, got %d (warnings=%v)", result.InboundShieldsImported, result.ImportWarnings)
	}
	shields, err := dest.ListRemoteSourceShields(src.ID)
	if err != nil || len(shields) != 1 {
		t.Fatalf("restored shields=%v err=%v, want exactly 1", shields, err)
	}

	// 恢复出的源行（按 ID 保号）再同步：屏蔽必须生效、节点不复活。
	destSrc, err := dest.GetRemoteSource(src.ID)
	if err != nil || destSrc == nil {
		t.Fatalf("restored source: %v / %v", destSrc, err)
	}
	h2 := New(dest, "unit-test-secret")
	res2 := r10SyncOne(t, h2, destSrc, r10LinkA)
	if res2.Added != 0 || res2.Shielded != 1 {
		t.Fatalf("post-restore sync: added=%d shielded=%d, want added=0 shielded=1 (errors=%v)", res2.Added, res2.Shielded, res2.Errors)
	}
	if got := r10OnlyRow(t, dest, src.ID); got != nil {
		t.Fatalf("deleted node resurrected after restore as id=%d", got.ID)
	}
}

func TestImportDisclosesRemovedNodeGroupsAndGroupIDs(t *testing.T) {
	dest, err := repo.Open(filepath.Join(t.TempDir(), "removed-groups.db"))
	if err != nil {
		t.Fatalf("open dest repo: %v", err)
	}
	t.Cleanup(func() { _ = dest.Close() })

	backup := &model.BackupData{
		Version: "1.0",
		Inbounds: &model.InboundsBackup{
			NodeGroups: []model.NodeGroupBackup{
				{ID: 7, Name: "legacy-group", SourceAll: 1},
			},
			Subscriptions: []model.SubscriptionBackup{
				{ID: 9, Name: "sub-with-group", Kind: model.SubscriptionShare, Token: strings.Repeat("a", 32),
					GroupIDs: "7", Enabled: 1},
			},
		},
	}
	result, err := dest.Import(backup, gistRestoreTypes)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	joined := strings.Join(result.ImportWarnings, "\n")
	if !strings.Contains(joined, "nodeGroups") {
		t.Fatalf("warnings must name the dropped nodeGroups segment, got %v", result.ImportWarnings)
	}
	if !strings.Contains(joined, "groupIds") {
		t.Fatalf("warnings must disclose ignored subscription groupIds, got %v", result.ImportWarnings)
	}
	// R3-S2-01：nodeGroups 段永不落库，被忽略条数据实计入
	// InboundNodeGroupsIgnored（夹具含 1 条），与 warnings 文本同数。
	if result.InboundNodeGroupsIgnored != 1 {
		t.Fatalf("removed node groups must be counted as ignored, got %d", result.InboundNodeGroupsIgnored)
	}
	subs, err := dest.ListSubscriptions()
	if err != nil {
		t.Fatalf("list subscriptions: %v", err)
	}
	// 目标行按名定位：新库另有迁移自带的默认订阅，不计在内。
	var restored *model.Subscription
	for i := range subs {
		if subs[i].Name == "sub-with-group" {
			if restored != nil {
				t.Fatal("imported subscription must land exactly once")
			}
			restored = &subs[i]
		}
	}
	if restored == nil {
		t.Fatalf("subscription itself must restore, got rows %v", subs)
	}
	// The restored row cannot carry groupIds: model.Subscription no
	// longer has a GroupIDs field at all (removed with the node-group
	// feature), so the guarantee is structural; the disclosure side is
	// asserted above via the import warnings.
}
