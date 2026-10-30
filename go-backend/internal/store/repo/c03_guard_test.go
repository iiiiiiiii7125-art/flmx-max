package repo

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// C03 repo 层回归：
//   - B03-001：Replace*GroupMembersTx 对非正成员 ID fail-closed，
//     不再把 [-5]/[0] 等价处理为「替换为空集」清空全组。
//   - B09-001（导入载体）：importSpeedLimits 对 speed<1 /
//     status∉{0,1} 整行跳过并计数 + warning，与 create/update
//     写侧合法集同口径。

func c03OpenRepo(t *testing.T, name string) *Repository {
	t.Helper()
	r, err := Open(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func TestC03ReplaceGroupMembersRejectsNonPositiveIDs(t *testing.T) {
	r := c03OpenRepo(t, "c03-assign.db")
	now := time.Now().UnixMilli()

	if err := r.DB().Exec(`INSERT INTO user(id, user, pwd, role_id, exp_time, flow, flow_reset_time, num, created_time, updated_time, status) VALUES(11, 'c03-u1', 'x', 1, 0, 0, 1, 0, ?, ?, 1)`, now, now).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if err := r.DB().Exec(`INSERT INTO user_group(name, created_time, updated_time, status) VALUES('c03-ug', ?, ?, 1)`, now, now).Error; err != nil {
		t.Fatalf("insert user_group: %v", err)
	}
	if err := r.DB().Exec(`INSERT INTO tunnel(name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, inx) VALUES('c03-t1', 1.0, 1, 'tls', 1, ?, ?, 1, 0)`, now, now).Error; err != nil {
		t.Fatalf("insert tunnel: %v", err)
	}
	if err := r.DB().Exec(`INSERT INTO tunnel_group(name, created_time, updated_time, status) VALUES('c03-tg', ?, ?, 1)`, now, now).Error; err != nil {
		t.Fatalf("insert tunnel_group: %v", err)
	}
	var ugID, tgID, tunID int64
	if err := r.DB().Raw(`SELECT id FROM user_group WHERE name = 'c03-ug'`).Row().Scan(&ugID); err != nil {
		t.Fatalf("user_group id: %v", err)
	}
	if err := r.DB().Raw(`SELECT id FROM tunnel_group WHERE name = 'c03-tg'`).Row().Scan(&tgID); err != nil {
		t.Fatalf("tunnel_group id: %v", err)
	}
	if err := r.DB().Raw(`SELECT id FROM tunnel WHERE name = 'c03-t1'`).Row().Scan(&tunID); err != nil {
		t.Fatalf("tunnel id: %v", err)
	}

	replaceUsers := func(ids []int64) error {
		tx := r.BeginTx()
		defer tx.Rollback()
		if err := r.ReplaceUserGroupMembersTx(tx, ugID, ids, now); err != nil {
			return err
		}
		return tx.Commit().Error
	}
	replaceTunnels := func(ids []int64) error {
		tx := r.BeginTx()
		defer tx.Rollback()
		if err := r.ReplaceTunnelGroupMembersTx(tx, tgID, ids, now); err != nil {
			return err
		}
		return tx.Commit().Error
	}

	if err := replaceUsers([]int64{11}); err != nil {
		t.Fatalf("baseline user replace: %v", err)
	}
	if err := replaceTunnels([]int64{tunID}); err != nil {
		t.Fatalf("baseline tunnel replace: %v", err)
	}

	for _, ids := range [][]int64{{-5}, {0}, {11, -5}} {
		err := replaceUsers(ids)
		if err == nil {
			t.Fatalf("user replace %v must fail", ids)
		}
		if !strings.Contains(err.Error(), "成员ID非法") {
			t.Fatalf("user replace %v error must name 成员ID非法, got %v", ids, err)
		}
		var n int64
		if err := r.DB().Raw(`SELECT COUNT(*) FROM user_group_user WHERE user_group_id = ? AND user_id = 11`, ugID).Row().Scan(&n); err != nil {
			t.Fatalf("count members: %v", err)
		}
		if n != 1 {
			t.Fatalf("user replace %v must keep existing members, got %d", ids, n)
		}
	}
	for _, ids := range [][]int64{{-5}, {0}} {
		if err := replaceTunnels(ids); err == nil {
			t.Fatalf("tunnel replace %v must fail", ids)
		}
		var n int64
		if err := r.DB().Raw(`SELECT COUNT(*) FROM tunnel_group_tunnel WHERE tunnel_group_id = ? AND tunnel_id = ?`, tgID, tunID).Row().Scan(&n); err != nil {
			t.Fatalf("count members: %v", err)
		}
		if n != 1 {
			t.Fatalf("tunnel replace %v must keep existing members, got %d", ids, n)
		}
	}

	// 合法空集替换（清空）仍是 assign 的既有语义，不得被守卫误伤。
	if err := replaceUsers([]int64{}); err != nil {
		t.Fatalf("empty replace must stay legal: %v", err)
	}
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
