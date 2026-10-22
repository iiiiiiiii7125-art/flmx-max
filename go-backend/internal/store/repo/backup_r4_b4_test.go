package repo

import (
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R4 批 4 回归（repo 导入面）：
//   - R4X-006：导入段授权收口落 repo 事务尾——部分导入 userTunnels
//     不得复活已被停用组覆盖的授权、forwards 须验 UT 授权存否；
//     同批全量恢复终态不变。
//   - G-S02 顺查：importPermissions grants 成员对应校验计 warning
//     （写回仍按 R2X-037 父组键口径，不跳行）。

// ─── R4X-006 事务尾收口 ─────────────────────────────────────────────────

// b4SeedRevokedState 造「目标库现行撤销终态」：UG1 已停用（成员/
// 权限对保留、grant 与 UT 已按停用口径清零，与 B5 断言同终态）。
func b4SeedRevokedState(t *testing.T, r *Repository, now int64) {
	t.Helper()
	createRow(t, r, &model.User{ID: 2, User: "u2", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, r, &model.Tunnel{ID: 1, Name: "t1", TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.Tunnel{ID: 2, Name: "t2", TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.UserGroup{ID: 1, Name: "g-one", Status: 0, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.TunnelGroup{ID: 1, Name: "tg-one", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.UserGroupUser{UserGroupID: 1, UserID: 2, CreatedTime: now})
	createRow(t, r, &model.TunnelGroupTunnel{TunnelGroupID: 1, TunnelID: 1, CreatedTime: now})
	createRow(t, r, &model.GroupPermission{ID: 50, UserGroupID: 1, TunnelGroupID: 1, CreatedTime: now})
}

// 探针 C 转绿：部分导入 userTunnels 不得复活停用组覆盖下的授权
// （跳过 + skipped + warning）；未被任何停用对覆盖的手工 UT 照常
// 落行（防过修对照同批）。
func TestR4B4_006_PartialUserTunnelsDoesNotResurrectRevoked(t *testing.T) {
	now := time.Now().UnixMilli()
	r := openBackupRepo(t, "b4-imp-ut.db")
	b4SeedRevokedState(t, r, now)

	backup := &model.BackupData{
		Version: "1.0",
		UserTunnels: []model.UserTunnelBackup{
			{ID: 10, UserID: 2, TunnelID: 1, Status: 1},
			{ID: 11, UserID: 2, TunnelID: 2, Status: 1},
		},
	}
	result, err := r.Import(backup, []string{"userTunnels"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if n := countWhere(t, r, "user_tunnel", "id = ?", 10); n != 0 {
		t.Fatalf("R4X-006 probe C: UT covered by a disabled permission pair must not be resurrected, got %d rows", n)
	}
	if n := countWhere(t, r, "user_tunnel", "id = ?", 11); n != 1 {
		t.Fatalf("manual UT not covered by any disabled pair must import as usual, got %d", n)
	}
	if result.UserTunnelsImported != 1 {
		t.Fatalf("imported count must exclude the tail-skipped UT, got %d", result.UserTunnelsImported)
	}
	totalSkipped := 0
	for _, v := range result.SkippedRows {
		totalSkipped += v
	}
	if totalSkipped != 1 {
		t.Fatalf("tail skip must be counted in SkippedRows, got %v", result.SkippedRows)
	}
	if len(result.ImportWarnings) == 0 {
		t.Fatalf("tail skip must be counted in ImportWarnings, got %v", result.ImportWarnings)
	}
}

// 探针 D 转绿：部分导入 forwards 须验对应 UT 授权存否——无 UT 的
// 转发不落行（skipped + warning）；有 UT（存量或同批）时照常落行。
func TestR4B4_006_PartialForwardsRequireUserTunnel(t *testing.T) {
	now := time.Now().UnixMilli()

	t.Run("no UT in target or batch: forward skipped", func(t *testing.T) {
		r := openBackupRepo(t, "b4-imp-fwd-none.db")
		b4SeedRevokedState(t, r, now)
		backup := &model.BackupData{
			Version:  "1.0",
			Forwards: []model.ForwardBackup{{ID: 20, UserID: 2, UserName: "u2", Name: "f1", TunnelID: 1, RemoteAddr: "1.2.3.4:80", Status: 1}},
		}
		result, err := r.Import(backup, []string{"forwards"})
		if err != nil {
			t.Fatalf("import: %v", err)
		}
		if n := countWhere(t, r, "forward", "id = ?", 20); n != 0 {
			t.Fatalf("R4X-006 probe D: forward without a user_tunnel authorization must not land, got %d rows", n)
		}
		if result.ForwardsImported != 0 {
			t.Fatalf("imported count must exclude the tail-skipped forward, got %d", result.ForwardsImported)
		}
		if len(result.ImportWarnings) == 0 {
			t.Fatalf("tail skip must be counted in ImportWarnings, got %v", result.ImportWarnings)
		}
	})

	t.Run("UT in same batch: forward lands (segment order must not misfire)", func(t *testing.T) {
		r := openBackupRepo(t, "b4-imp-fwd-batch.db")
		createRow(t, r, &model.User{ID: 2, User: "u2", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
		createRow(t, r, &model.Tunnel{ID: 1, Name: "t1", TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now})
		backup := &model.BackupData{
			Version: "1.0",
			Forwards: []model.ForwardBackup{
				{ID: 20, UserID: 2, UserName: "u2", Name: "f1", TunnelID: 1, RemoteAddr: "1.2.3.4:80", Status: 1},
			},
			UserTunnels: []model.UserTunnelBackup{
				{ID: 10, UserID: 2, TunnelID: 1, Status: 1},
			},
		}
		result, err := r.Import(backup, []string{"forwards", "userTunnels"})
		if err != nil {
			t.Fatalf("import: %v", err)
		}
		if n := countWhere(t, r, "forward", "id = ?", 20); n != 1 {
			t.Fatalf("forward whose UT is restored in the same batch must land, got %d", n)
		}
		if result.ForwardsImported != 1 || result.UserTunnelsImported != 1 {
			t.Fatalf("same-batch counts must be intact, forwards=%d userTunnels=%d", result.ForwardsImported, result.UserTunnelsImported)
		}
	})

	t.Run("UT pre-existing in target: forward lands", func(t *testing.T) {
		r := openBackupRepo(t, "b4-imp-fwd-pre.db")
		createRow(t, r, &model.User{ID: 2, User: "u2", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
		createRow(t, r, &model.Tunnel{ID: 1, Name: "t1", TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now})
		createRow(t, r, &model.UserTunnel{ID: 10, UserID: 2, TunnelID: 1, Status: 1})
		backup := &model.BackupData{
			Version:  "1.0",
			Forwards: []model.ForwardBackup{{ID: 20, UserID: 2, UserName: "u2", Name: "f1", TunnelID: 1, RemoteAddr: "1.2.3.4:80", Status: 1}},
		}
		if _, err := r.Import(backup, []string{"forwards"}); err != nil {
			t.Fatalf("import: %v", err)
		}
		if n := countWhere(t, r, "forward", "id = ?", 20); n != 1 {
			t.Fatalf("forward with a pre-existing UT authorization must land, got %d", n)
		}
	})
}

// 全量恢复终态不变（逐行对账）：停用组 + 其成员的手工 UT（无
// grant）在整表恢复后必须原样存活——组状态本身来自备份时，事务
// 尾收口不得按目标库口径误删；组建 UT + grant + forward 同理。
func TestR4B4_006_FullRestoreFinalStateUnchanged(t *testing.T) {
	now := time.Now().UnixMilli()
	pwd := "$2a$10$abcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ012"
	source := openBackupRepo(t, "b4-full-src.db")
	createRow(t, source, &model.User{ID: 2, User: "u2", Pwd: pwd, RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, source, &model.Tunnel{ID: 1, Name: "t1", TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, source, &model.Tunnel{ID: 2, Name: "t2", TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now})
	// 启用对 (1,1)：组建 UT10 + grant；停用对 (2,2)：成员 U2、
	// 含 T2，UT11 为该成员的手工授权（无 grant，停用组下合法存续）。
	createRow(t, source, &model.UserGroup{ID: 1, Name: "g-on", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, source, &model.UserGroup{ID: 2, Name: "g-off", Status: 0, CreatedTime: now, UpdatedTime: now})
	createRow(t, source, &model.TunnelGroup{ID: 1, Name: "tg-on", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, source, &model.TunnelGroup{ID: 2, Name: "tg-off", Status: 0, CreatedTime: now, UpdatedTime: now})
	createRow(t, source, &model.UserGroupUser{UserGroupID: 1, UserID: 2, CreatedTime: now})
	createRow(t, source, &model.UserGroupUser{UserGroupID: 2, UserID: 2, CreatedTime: now})
	createRow(t, source, &model.TunnelGroupTunnel{TunnelGroupID: 1, TunnelID: 1, CreatedTime: now})
	createRow(t, source, &model.TunnelGroupTunnel{TunnelGroupID: 2, TunnelID: 2, CreatedTime: now})
	createRow(t, source, &model.GroupPermission{ID: 50, UserGroupID: 1, TunnelGroupID: 1, CreatedTime: now})
	createRow(t, source, &model.GroupPermission{ID: 51, UserGroupID: 2, TunnelGroupID: 2, CreatedTime: now})
	createRow(t, source, &model.UserTunnel{ID: 10, UserID: 2, TunnelID: 1, Status: 1})
	createRow(t, source, &model.UserTunnel{ID: 11, UserID: 2, TunnelID: 2, Status: 1})
	createRow(t, source, &model.GroupPermissionGrant{ID: 60, UserGroupID: 1, TunnelGroupID: 1, UserTunnelID: 10, CreatedByGroup: 1, CreatedTime: now})
	createRow(t, source, &model.Forward{ID: 20, UserID: 2, UserName: "u2", Name: "f1", TunnelID: 1, RemoteAddr: "1.2.3.4:80", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, source, &model.Forward{ID: 21, UserID: 2, UserName: "u2", Name: "f2", TunnelID: 2, RemoteAddr: "1.2.3.5:80", Status: 1, CreatedTime: now, UpdatedTime: now})

	dest := openBackupRepo(t, "b4-full-dst.db")
	result := roundTripBackup(t, source, dest)
	if n := countWhere(t, dest, "user_tunnel", "id = ?", 10); n != 1 {
		t.Fatalf("full restore: group-created UT10 must survive, got %d", n)
	}
	if n := countWhere(t, dest, "user_tunnel", "id = ?", 11); n != 1 {
		t.Fatalf("full restore: manual UT11 under a disabled pair (state restored from the same backup) must survive, got %d", n)
	}
	if n := countWhere(t, dest, "group_permission_grant", "id = ?", 60); n != 1 {
		t.Fatalf("full restore: grant 60 must survive, got %d", n)
	}
	if n := countWhere(t, dest, "forward", "id IN (20, 21)"); n != 2 {
		t.Fatalf("full restore: both forwards must survive, got %d", n)
	}
	if len(result.SkippedRows) != 0 {
		t.Fatalf("full restore must skip zero rows, got %v", result.SkippedRows)
	}
	_ = result
}

// ─── G-S02 顺查 ─────────────────────────────────────────────────────────

// grants 成员对应校验：UT 的 (user,tunnel) 与权限对成员对应关系
// 不成立时计 warning（写回仍按父组键落库，与 R2X-037 口径兼容；
// 红：无任何对应关系提示）。
func TestR4B4_GS02_GrantMembershipMismatchWarns(t *testing.T) {
	now := time.Now().UnixMilli()
	r := openBackupRepo(t, "b4-gs02.db")
	createRow(t, r, &model.User{ID: 2, User: "u2", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, r, &model.Tunnel{ID: 1, Name: "t1", TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.UserGroup{ID: 1, Name: "g-one", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.TunnelGroup{ID: 1, Name: "tg-one", Status: 1, CreatedTime: now, UpdatedTime: now})
	// 无任何成员行：grant 指向的 UT(2,1) 与权限对 (1,1) 成员对应不成立。
	createRow(t, r, &model.UserTunnel{ID: 10, UserID: 2, TunnelID: 1, Status: 1})

	backup := &model.BackupData{
		Version: "1.0",
		Permissions: []model.PermissionBackup{
			{ID: 50, UserGroupID: 1, TunnelGroupID: 1, CreatedTime: now, Grants: []model.PermissionGrantBackup{
				{ID: 60, UserGroupID: 1, TunnelGroupID: 1, UserTunnelID: 10, CreatedByGroup: 1, CreatedTime: now},
			}},
		},
	}
	result, err := r.Import(backup, []string{"permissions"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if n := countWhere(t, r, "group_permission_grant", "id = ?", 60); n != 1 {
		t.Fatalf("grant must still land (R2X-037 parent-key write-back stays), got %d", n)
	}
	if !b8WarningsContain(result.ImportWarnings, "成员对应") {
		t.Fatalf("G-S02: membership mismatch of an imported grant must be counted in ImportWarnings, got %v", result.ImportWarnings)
	}
}
