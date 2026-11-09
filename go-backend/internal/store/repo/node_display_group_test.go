package repo

import (
	"path/filepath"
	"strings"
	"testing"

	"go-backend/internal/store/model"
)

// 节点展示分组标签（纯展示功能）：备份往返保真＋恢复面归一＋写面
// （UpdateNodeDisplayGroup）与列表读面（ListNodes）联动。

func TestBackupNodeDisplayGroupRoundTrip(t *testing.T) {
	dst, err := Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dst.Close() })

	long := strings.Repeat("组", 120)
	backup := &model.BackupData{
		Version: "1.0",
		Nodes: []model.NodeBackup{
			{ID: 41, Name: "grouped", Secret: "s", ServerIP: "192.0.2.41", Status: 1, DisplayGroup: " 中转 "},
			{ID: 42, Name: "overlong", Secret: "s", ServerIP: "192.0.2.42", Status: 1, DisplayGroup: long},
			// 旧备份形态：字段全缺（空＝未分组）。
			{ID: 43, Name: "legacy", Secret: "s", ServerIP: "192.0.2.43", Status: 1},
		},
	}
	result, err := dst.Import(backup, []string{"nodes"})
	if err != nil {
		t.Fatal(err)
	}
	if result.NodesImported != 3 {
		t.Fatalf("NodesImported = %d, want 3, warnings=%v", result.NodesImported, result.ImportWarnings)
	}
	readGroup := func(id int64) string {
		t.Helper()
		var n model.Node
		if err := dst.db.Where("id = ?", id).First(&n).Error; err != nil {
			t.Fatalf("read node #%d: %v", id, err)
		}
		return n.DisplayGroup
	}
	if g := readGroup(41); g != "中转" {
		t.Fatalf("node 41 display group = %q, want %q (trimmed verbatim)", g, "中转")
	}
	if g := readGroup(42); len([]rune(g)) != 100 {
		t.Fatalf("node 42 display group runes = %d, want 100 (truncated to column width)", len([]rune(g)))
	}
	if warnText := strings.Join(result.ImportWarnings, "\n"); !strings.Contains(warnText, long) {
		t.Fatalf("receipt must name the original overlong label, warnings=%v", result.ImportWarnings)
	}
	if g := readGroup(43); g != "" {
		t.Fatalf("node 43 display group = %q, want empty (legacy backup = ungrouped)", g)
	}

	// 导出面：恢复后的标签再导出须原样携带（导出赋值缺失会回落空）。
	exported, err := dst.ExportAll()
	if err != nil {
		t.Fatal(err)
	}
	byID := map[int64]string{}
	for _, n := range exported.Nodes {
		byID[n.ID] = n.DisplayGroup
	}
	if byID[41] != "中转" || len([]rune(byID[42])) != 100 || byID[43] != "" {
		t.Fatalf("exported display groups = %v, want 41=中转 / 42=100 runes / 43=empty", byID)
	}

	// 写面＋列表读面：UpdateNodeDisplayGroup 落库、ListNodes 回带。
	if err := dst.UpdateNodeDisplayGroup(43, "落地"); err != nil {
		t.Fatal(err)
	}
	items, err := dst.ListNodes()
	if err != nil {
		t.Fatal(err)
	}
	listGroup := map[int64]string{}
	for _, item := range items {
		id, _ := item["id"].(int64)
		g, _ := item["displayGroup"].(string)
		listGroup[id] = g
	}
	if listGroup[43] != "落地" || listGroup[41] != "中转" {
		t.Fatalf("ListNodes displayGroup = %v, want 43=落地 / 41=中转", listGroup)
	}
	// 脱组：显式空串回未分组。
	if err := dst.UpdateNodeDisplayGroup(43, ""); err != nil {
		t.Fatal(err)
	}
	if g := readGroup(43); g != "" {
		t.Fatalf("node 43 display group after clear = %q, want empty", g)
	}
}

// S3-2：改名/删组的名单写与成员写同事务（Rename/ClearNodeDisplay-
// GroupWithRegistry）。注入成员更新失败时名单与标签整体回滚、不
// 留分叉，且失败后同名重试直接成功——旧两步写先落名单时，名单里
// 的新名空组会把同名重试堵在重名门外。
func TestNodeDisplayGroupRegistryWritesAreAtomic(t *testing.T) {
	dst, err := Open(filepath.Join(t.TempDir(), "group-atomic.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dst.Close() })

	backup := &model.BackupData{
		Version: "1.0",
		Nodes: []model.NodeBackup{
			{ID: 51, Name: "atomic-a", Secret: "s", ServerIP: "192.0.2.51", Status: 1, DisplayGroup: "旧名"},
			{ID: 52, Name: "atomic-b", Secret: "s", ServerIP: "192.0.2.52", Status: 1, DisplayGroup: "旧名"},
			{ID: 53, Name: "atomic-c", Secret: "s", ServerIP: "192.0.2.53", Status: 1, DisplayGroup: "其他"},
		},
	}
	if _, err := dst.Import(backup, []string{"nodes"}); err != nil {
		t.Fatal(err)
	}
	if err := dst.UpsertConfig(nodeDisplayGroupOrderConfigKey, `["旧名","其他"]`, 1); err != nil {
		t.Fatal(err)
	}
	readGroup := func(id int64) string {
		t.Helper()
		var n model.Node
		if err := dst.db.Where("id = ?", id).First(&n).Error; err != nil {
			t.Fatalf("read node #%d: %v", id, err)
		}
		return n.DisplayGroup
	}
	readRegistry := func() string {
		t.Helper()
		raw, err := dst.GetViteConfigValue(nodeDisplayGroupOrderConfigKey)
		if err != nil {
			t.Fatalf("read registry: %v", err)
		}
		return raw
	}

	// 正常改名：名单原位替换与成员级联同落。
	updated, err := dst.RenameNodeDisplayGroupWithRegistry("旧名", "新名", `["新名","其他"]`, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if updated != 2 {
		t.Fatalf("rename updated = %d, want 2", updated)
	}
	if got := readRegistry(); got != `["新名","其他"]` {
		t.Fatalf("registry = %s, want [\"新名\",\"其他\"]", got)
	}
	if g := readGroup(51); g != "新名" {
		t.Fatalf("node 51 group = %q, want 新名", g)
	}

	// 注入成员更新失败：改名整体回滚——名单不落、标签不变。
	if err := dst.db.Exec(`CREATE TRIGGER fail_display_group_update BEFORE UPDATE ON node BEGIN SELECT RAISE(ABORT, 'injected failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := dst.RenameNodeDisplayGroupWithRegistry("新名", "终名", `["终名","其他"]`, 1001); err == nil {
		t.Fatal("rename with injected member failure must return error")
	}
	if got := readRegistry(); got != `["新名","其他"]` {
		t.Fatalf("registry after failed rename = %s, want unchanged (no fork)", got)
	}
	if g := readGroup(51); g != "新名" {
		t.Fatalf("node 51 group after failed rename = %q, want 新名 unchanged", g)
	}
	if err := dst.db.Exec(`DROP TRIGGER fail_display_group_update`).Error; err != nil {
		t.Fatal(err)
	}

	// 失败后同名重试：名单里没有半成品新名，重试直接成功。
	updated, err = dst.RenameNodeDisplayGroupWithRegistry("新名", "终名", `["终名","其他"]`, 1002)
	if err != nil {
		t.Fatal(err)
	}
	if updated != 2 || readGroup(52) != "终名" || readRegistry() != `["终名","其他"]` {
		t.Fatalf("retry rename: updated=%d node52=%q registry=%s", updated, readGroup(52), readRegistry())
	}

	// 删组原子写：名单摘除与成员回落同落。
	updated, err = dst.ClearNodeDisplayGroupWithRegistry("终名", `["其他"]`, 1003)
	if err != nil {
		t.Fatal(err)
	}
	if updated != 2 {
		t.Fatalf("clear updated = %d, want 2", updated)
	}
	if g := readGroup(51); g != "" {
		t.Fatalf("node 51 group after delete = %q, want empty", g)
	}
	if got := readRegistry(); got != `["其他"]` {
		t.Fatalf("registry after delete = %s, want [\"其他\"]", got)
	}
	if g := readGroup(53); g != "其他" {
		t.Fatalf("node 53 group = %q, want 其他 untouched", g)
	}
}
