package repo

import (
	"path/filepath"
	"testing"

	"go-backend/internal/store/model"
)

// C03 repo 层回归：
//   - B09-001（导入载体）：importSpeedLimits 对 speed<1 /
//     status∉{0,1} 整行跳过并计数 + warning，与 create/update
//     写侧合法集同口径。
// （B03-001 分组成员非正 ID 门已随分组三件套于 1.9.6 整套移除。）

func c03OpenRepo(t *testing.T, name string) *Repository {
	t.Helper()
	r, err := Open(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func TestC03ImportSpeedLimitsSkipsIllegalValues(t *testing.T) {
	dest := c03OpenRepo(t, "c03-import-sl.db")
	backup := &model.BackupData{
		Version: "1.0",
		SpeedLimits: []model.SpeedLimitBackup{
			{ID: 1, Name: "bad-speed-zero", Speed: 0, Status: 1},
			{ID: 2, Name: "bad-speed-neg", Speed: -5, Status: 1},
			{ID: 3, Name: "bad-status", Speed: 100, Status: 7},
			{ID: 4, Name: "good", Speed: 100, Status: 1},
			{ID: 5, Name: "good-disabled", Speed: 50, Status: 0},
		},
	}
	result, err := dest.Import(backup, []string{"speedLimits"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.SpeedLimitsImported != 2 {
		t.Fatalf("only the two legal rows may be imported, got %d", result.SpeedLimitsImported)
	}
	if got := result.SkippedRows["speedLimits"]; got != 3 {
		t.Fatalf("three illegal rows must be counted as skipped, got %d", got)
	}
	if !b8WarningsContain(result.ImportWarnings, "已跳过该行未恢复") {
		t.Fatalf("skips must surface in ImportWarnings, got %v", result.ImportWarnings)
	}
	var names []string
	if err := dest.DB().Raw(`SELECT name FROM speed_limit ORDER BY id`).Pluck("name", &names).Error; err != nil {
		t.Fatalf("list imported: %v", err)
	}
	if len(names) != 2 || names[0] != "good" || names[1] != "good-disabled" {
		t.Fatalf("imported rows mismatch: %v", names)
	}
}
