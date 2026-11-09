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
