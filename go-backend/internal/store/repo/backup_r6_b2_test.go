package repo

import (
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R6 批 2 回归（导入 UT 逐字落行）：
//   - R6X-005 口径在分组三件套移除后（1.9.6）的现行形态：导入
//     userTunnels 不按任何分组/台账状态裁定——UT 一律逐字落行、
//     无 userTunnelsRevoked skipped。
// （原 R6X-004 grant 台账保真与 R6X-005 ShieldChain/合法覆盖两例
// 均以分组权限段为前提，已随功能整套移除删除。）

func TestR6B2_005_ControlNoGrantLandsVerbatim(t *testing.T) {
	now := time.Now().UnixMilli()
	r := openBackupRepo(t, "b2-005-control.db")
	createRow(t, r, &model.User{ID: 2, User: "u2", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, r, &model.Tunnel{ID: 1, Name: "t1", TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now})

	backup := &model.BackupData{
		Version:     "1.0",
		UserTunnels: []model.UserTunnelBackup{{ID: 10, UserID: 2, TunnelID: 1, Status: 1}},
	}
	result, err := r.Import(backup, []string{"userTunnels"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if n := countWhere(t, r, "user_tunnel", "id = ?", 10); n != 1 {
		t.Fatalf("UT10 must land verbatim, got %d rows", n)
	}
	if result.SkippedRows["userTunnelsRevoked"] != 0 {
		t.Fatalf("no userTunnelsRevoked skip may be counted, got %v", result.SkippedRows)
	}
}
