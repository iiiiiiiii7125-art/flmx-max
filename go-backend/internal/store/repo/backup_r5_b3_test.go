package repo

import (
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R5 批 3 回归（授权台账族）：
//   - R5X-005：备份导入同名组合并去重不得直删 cb=1 grant 而不做来源
//     OR/提升——合并后保留行 cb = 两行 cb 的 OR；继任组停用/撤销时
//     组建 UT 的终态只取决于合并后的 cb（cb=1 被删、cb=0 存活）。
//   - R5X-014：窄硬门案 A——importPermissions 写回时，cb=1 且成员
//     对应不成立（!holds）的 grant 降级为 cb=0 写回 + warning 明示
//     降级；cb=0 行维持原 warning；合法对应 cb=1 行逐字不变。
//   - R5X-015（定案项，过渡 warning）：sweep 对本次新落、无 grant、
//     现行无任何权限对覆盖的 UT 增计一条通用 warning（行仍落库、
//     不删除）；被权限对覆盖的手工 UT 不得新增该 warning（防过修）。

// ─── R5X-005 同名合并 cb 四格 ──────────────────────────────────────────

// b3SeedSameNameMerge 造 G 探针同形态（组名唯一约束下按 R3B6 合并
// 用例的造法：继任组 UG1 以异名 "other" 预置、占位组 UG9 名为
// "dup"，备份把 UG1 改名为 "dup" 触发同名合并），两组各有一条
// 指向同一 UT10 的 grant，cb 分别为 keepCB（继任/保留行）与
// occCB（占位/被删行）。
func b3SeedSameNameMerge(t *testing.T, r *Repository, now int64, occCB, keepCB int) {
	t.Helper()
	createRow(t, r, &model.User{ID: 2, User: "u2", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, r, &model.Tunnel{ID: 1, Name: "t1", TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.UserGroup{ID: 1, Name: "other", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.UserGroup{ID: 9, Name: "dup", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.TunnelGroup{ID: 1, Name: "tg-one", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.UserGroupUser{UserGroupID: 1, UserID: 2, CreatedTime: now})
	createRow(t, r, &model.UserTunnel{ID: 10, UserID: 2, TunnelID: 1, Status: 1})
	createRow(t, r, &model.GroupPermissionGrant{ID: 61, UserGroupID: 1, TunnelGroupID: 1, UserTunnelID: 10, CreatedByGroup: keepCB, CreatedTime: now})
	createRow(t, r, &model.GroupPermissionGrant{ID: 62, UserGroupID: 9, TunnelGroupID: 1, UserTunnelID: 10, CreatedByGroup: occCB, CreatedTime: now})
}

// 四格 = (占位/被删行 cb, 继任/保留行 cb)：
//
//	(1,0) 本条缺陷格：被删行 cb=1 必须提升到保留行，合并后 cb=1，
//	      继任组撤销时 UT10 被删并回传 (2,1)。
//	(0,1) 防过修格：保留行本已 cb=1，被删行 cb=0 不得把保留行 cb
//	      覆盖/降级，合并后仍 cb=1，撤销时 UT10 被删。
//	(1,1) 合并后 cb=1，撤销时 UT10 被删。
//	(0,0) 防过修格：两行皆 cb=0，合并不得凭空造出组建来源，
//	      合并后 cb=0，撤销时 UT10 存活。
func TestR5B3_005_SameNameMergeCreatorFlagOR(t *testing.T) {
	cells := []struct {
		name       string
		occCB      int
		keepCB     int
		wantCB     int
		wantUTGone bool
	}{
		{"deleted cb=1 kept cb=0", 1, 0, 1, true},
		{"deleted cb=0 kept cb=1", 0, 1, 1, true},
		{"both cb=1", 1, 1, 1, true},
		{"both cb=0", 0, 0, 0, false},
	}
	for _, cell := range cells {
		t.Run(cell.name, func(t *testing.T) {
			now := time.Now().UnixMilli()
			r := openBackupRepo(t, "b3-005.db")
			b3SeedSameNameMerge(t, r, now, cell.occCB, cell.keepCB)

			backup := &model.BackupData{
				Version: "1.0",
				UserGroups: []model.UserGroupBackup{
					{ID: 1, Name: "dup", Status: 1, CreatedTime: now, Users: []int64{2}},
				},
			}
			if _, err := r.Import(backup, []string{"userGroups"}); err != nil {
				t.Fatalf("import: %v", err)
			}
			if n := countWhere(t, r, "user_group", "id = ?", 9); n != 0 {
				t.Fatalf("placeholder group 9 must be merged away, got %d rows", n)
			}
			if n := countWhere(t, r, "group_permission_grant", "user_group_id = ? AND tunnel_group_id = ? AND user_tunnel_id = ?", 1, 1, 10); n != 1 {
				t.Fatalf("merged grant for successor key must be exactly 1 row, got %d", n)
			}
			var kept model.GroupPermissionGrant
			if err := r.DB().Where("user_group_id = ? AND tunnel_group_id = ? AND user_tunnel_id = ?", 1, 1, 10).First(&kept).Error; err != nil {
				t.Fatalf("read merged grant: %v", err)
			}
			if kept.CreatedByGroup != cell.wantCB {
				t.Fatalf("R5X-005: merged grant created_by_group = %d, want OR(%d,%d) = %d", kept.CreatedByGroup, cell.occCB, cell.keepCB, cell.wantCB)
			}
			revoked, err := r.RevokeGroupGrantsByGroup("user_group", 1)
			if err != nil {
				t.Fatalf("revoke successor group: %v", err)
			}
			utRows := countWhere(t, r, "user_tunnel", "id = ?", 10)
			if cell.wantUTGone {
				if utRows != 0 {
					t.Fatalf("R5X-005: group-created UT10 must be revoked with the successor group, got %d rows (revoked=%v)", utRows, revoked)
				}
				found := false
				for _, p := range revoked {
					if p.UserID == 2 && p.TunnelID == 1 {
						found = true
					}
				}
				if !found {
					t.Fatalf("revoke must return the (2,1) pair for UT10, got %v", revoked)
				}
			} else {
				if utRows != 1 {
					t.Fatalf("manual UT10 (merged cb=0) must survive successor-group revoke, got %d rows", utRows)
				}
				if len(revoked) != 0 {
					t.Fatalf("no pair may be revoked for a merged cb=0 grant, got %v", revoked)
				}
			}
		})
	}
}

// 隧道组侧同型（两侧共用 deleteSameNameGroupTx，仅组键互换）：
// (占位 cb=1, 继任 cb=0) 格在隧道组面同样须提升。
func TestR5B3_005_SameNameMergeTunnelGroupSide(t *testing.T) {
	now := time.Now().UnixMilli()
	r := openBackupRepo(t, "b3-005-tg.db")
	createRow(t, r, &model.User{ID: 2, User: "u2", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, r, &model.Tunnel{ID: 1, Name: "t1", TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.UserGroup{ID: 1, Name: "ug-one", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.TunnelGroup{ID: 1, Name: "other", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.TunnelGroup{ID: 9, Name: "dup", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.TunnelGroupTunnel{TunnelGroupID: 1, TunnelID: 1, CreatedTime: now})
	createRow(t, r, &model.UserTunnel{ID: 10, UserID: 2, TunnelID: 1, Status: 1})
	createRow(t, r, &model.GroupPermissionGrant{ID: 61, UserGroupID: 1, TunnelGroupID: 1, UserTunnelID: 10, CreatedByGroup: 0, CreatedTime: now})
	createRow(t, r, &model.GroupPermissionGrant{ID: 62, UserGroupID: 1, TunnelGroupID: 9, UserTunnelID: 10, CreatedByGroup: 1, CreatedTime: now})

	backup := &model.BackupData{
		Version: "1.0",
		TunnelGroups: []model.TunnelGroupBackup{
			{ID: 1, Name: "dup", Status: 1, CreatedTime: now, Tunnels: []int64{1}},
		},
	}
	if _, err := r.Import(backup, []string{"tunnelGroups"}); err != nil {
		t.Fatalf("import: %v", err)
	}
	var kept model.GroupPermissionGrant
	if err := r.DB().Where("user_group_id = ? AND tunnel_group_id = ? AND user_tunnel_id = ?", 1, 1, 10).First(&kept).Error; err != nil {
		t.Fatalf("read merged grant: %v", err)
	}
	if kept.CreatedByGroup != 1 {
		t.Fatalf("R5X-005 (tunnel-group side): merged grant created_by_group = %d, want 1 (OR of deleted cb=1 and kept cb=0)", kept.CreatedByGroup)
	}
	revoked, err := r.RevokeGroupGrantsByGroup("tunnel_group", 1)
	if err != nil {
		t.Fatalf("revoke successor tunnel group: %v", err)
	}
	if n := countWhere(t, r, "user_tunnel", "id = ?", 10); n != 0 {
		t.Fatalf("group-created UT10 must be revoked with the successor tunnel group, got %d rows (revoked=%v)", n, revoked)
	}
}

// ─── R5X-014 窄硬门案 A ────────────────────────────────────────────────

// b3SeedMismatch 造 G 探针同形态：手工 UT10（U2/T1、无任何 grant），
// UG1 成员为 U3（U2 不在内）、TG1 成员为 T1，权限对 (1,1) 在位且启用。
func b3SeedMismatch(t *testing.T, r *Repository, now int64) {
	t.Helper()
	createRow(t, r, &model.User{ID: 2, User: "u2", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, r, &model.User{ID: 3, User: "u3", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, r, &model.Tunnel{ID: 1, Name: "t1", TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.UserGroup{ID: 1, Name: "g-one", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.TunnelGroup{ID: 1, Name: "tg-one", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.UserGroupUser{UserGroupID: 1, UserID: 3, CreatedTime: now})
	createRow(t, r, &model.TunnelGroupTunnel{TunnelGroupID: 1, TunnelID: 1, CreatedTime: now})
	createRow(t, r, &model.UserTunnel{ID: 10, UserID: 2, TunnelID: 1, Status: 1})
}

func b3ImportGrant(t *testing.T, r *Repository, now int64, cb int) *model.ImportResult {
	t.Helper()
	backup := &model.BackupData{
		Version: "1.0",
		Permissions: []model.PermissionBackup{
			{ID: 50, UserGroupID: 1, TunnelGroupID: 1, CreatedTime: now, Grants: []model.PermissionGrantBackup{
				{ID: 60, UserGroupID: 1, TunnelGroupID: 1, UserTunnelID: 10, CreatedByGroup: cb, CreatedTime: now},
			}},
		},
	}
	result, err := r.Import(backup, []string{"permissions"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	return result
}

// 案 A 主格：cb=1 且成员对应不成立 → 写回降级 cb=0、warning 明示
// 降级（回执语义与 H 域对齐：走 ImportWarnings、不计 SkippedRows、
// 行照常落库）；其后停用 UG1 不得连带删除手工 UT10（第二跳封堵）。
func TestR5B3_014_MismatchCB1DowngradedOnWriteBack(t *testing.T) {
	now := time.Now().UnixMilli()
	r := openBackupRepo(t, "b3-014-downgrade.db")
	b3SeedMismatch(t, r, now)

	result := b3ImportGrant(t, r, now, 1)
	var grant model.GroupPermissionGrant
	if err := r.DB().Where("id = ?", 60).First(&grant).Error; err != nil {
		t.Fatalf("R5X-014 case A: grant row must still land (downgrade, not skip): %v", err)
	}
	if grant.CreatedByGroup != 0 {
		t.Fatalf("R5X-014 case A: mismatched cb=1 grant must be written back downgraded to cb=0, got %d", grant.CreatedByGroup)
	}
	if !b8WarningsContain(result.ImportWarnings, "成员对应") {
		t.Fatalf("R5X-014 case A: downgrade must be disclosed in ImportWarnings, got %v", result.ImportWarnings)
	}
	if !b8WarningsContain(result.ImportWarnings, "降级") {
		t.Fatalf("R5X-014 case A: warning must explicitly state the downgrade, got %v", result.ImportWarnings)
	}
	if len(result.SkippedRows) != 0 {
		t.Fatalf("R5X-014 case A: downgrade must not be counted as skipped rows, got %v", result.SkippedRows)
	}
	revoked, err := r.RevokeGroupGrantsByGroup("user_group", 1)
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if n := countWhere(t, r, "user_tunnel", "id = ?", 10); n != 1 {
		t.Fatalf("R5X-014 second hop: manual UT10 must survive the unrelated group revoke, got %d rows (revoked=%v)", n, revoked)
	}
	for _, p := range revoked {
		if p.UserID == 2 && p.TunnelID == 1 {
			t.Fatalf("revoked pairs must not contain the manual (2,1) pair, got %v", revoked)
		}
	}
}

// cb=0 行维持 warning 原样：不出现「降级」字样、写回 cb=0 不变。
func TestR5B3_014_MismatchCB0WarningUnchanged(t *testing.T) {
	now := time.Now().UnixMilli()
	r := openBackupRepo(t, "b3-014-cb0.db")
	b3SeedMismatch(t, r, now)

	result := b3ImportGrant(t, r, now, 0)
	var grant model.GroupPermissionGrant
	if err := r.DB().Where("id = ?", 60).First(&grant).Error; err != nil {
		t.Fatalf("grant row must land: %v", err)
	}
	if grant.CreatedByGroup != 0 {
		t.Fatalf("cb=0 grant must stay cb=0, got %d", grant.CreatedByGroup)
	}
	if !b8WarningsContain(result.ImportWarnings, "成员对应") {
		t.Fatalf("cb=0 mismatch must keep the original membership warning, got %v", result.ImportWarnings)
	}
	if b8WarningsContain(result.ImportWarnings, "降级") {
		t.Fatalf("cb=0 mismatch must not be reported as a downgrade, got %v", result.ImportWarnings)
	}
}

// 合法对照（防过修）：成员对应成立的 cb=1 grant 逐字写回、无降级
// warning，其后撤销仍按组建口径删除 UT。
func TestR5B3_014_MatchingCB1Unchanged(t *testing.T) {
	now := time.Now().UnixMilli()
	r := openBackupRepo(t, "b3-014-holds.db")
	createRow(t, r, &model.User{ID: 2, User: "u2", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, r, &model.Tunnel{ID: 1, Name: "t1", TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.UserGroup{ID: 1, Name: "g-one", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.TunnelGroup{ID: 1, Name: "tg-one", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.UserGroupUser{UserGroupID: 1, UserID: 2, CreatedTime: now})
	createRow(t, r, &model.TunnelGroupTunnel{TunnelGroupID: 1, TunnelID: 1, CreatedTime: now})
	createRow(t, r, &model.UserTunnel{ID: 10, UserID: 2, TunnelID: 1, Status: 1})

	result := b3ImportGrant(t, r, now, 1)
	var grant model.GroupPermissionGrant
	if err := r.DB().Where("id = ?", 60).First(&grant).Error; err != nil {
		t.Fatalf("grant row must land: %v", err)
	}
	if grant.CreatedByGroup != 1 {
		t.Fatalf("legitimate cb=1 grant must be written back verbatim, got cb=%d", grant.CreatedByGroup)
	}
	if b8WarningsContain(result.ImportWarnings, "成员对应") || b8WarningsContain(result.ImportWarnings, "降级") {
		t.Fatalf("legitimate grant must produce no mismatch/downgrade warning, got %v", result.ImportWarnings)
	}
	revoked, err := r.RevokeGroupGrantsByGroup("user_group", 1)
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if n := countWhere(t, r, "user_tunnel", "id = ?", 10); n != 0 {
		t.Fatalf("legitimate group-created UT10 must still be revoked with its group, got %d rows (revoked=%v)", n, revoked)
	}
}

// ─── R5X-015 过渡 warning（定案项，非关闭依据） ────────────────────────

func b3ImportUT10(t *testing.T, r *Repository) *model.ImportResult {
	t.Helper()
	backup := &model.BackupData{
		Version:     "1.0",
		UserTunnels: []model.UserTunnelBackup{{ID: 10, UserID: 2, TunnelID: 1, Status: 1}},
	}
	result, err := r.Import(backup, []string{"userTunnels"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	return result
}

// 子形态 ①：权限对保留启用、用户已被移出成员（差集撤销终态）——
// UT 照常落行（不删除、不 skipped），但必须增计一条无台账通用
// warning，不得再静默复活。
func TestR5B3_015_UncoveredNewUTWarnsPairKeptMemberRemoved(t *testing.T) {
	now := time.Now().UnixMilli()
	r := openBackupRepo(t, "b3-015-pair.db")
	createRow(t, r, &model.User{ID: 2, User: "u2", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, r, &model.User{ID: 3, User: "u3", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, r, &model.Tunnel{ID: 1, Name: "t1", TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.UserGroup{ID: 1, Name: "g-one", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.TunnelGroup{ID: 1, Name: "tg-one", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.UserGroupUser{UserGroupID: 1, UserID: 3, CreatedTime: now})
	createRow(t, r, &model.TunnelGroupTunnel{TunnelGroupID: 1, TunnelID: 1, CreatedTime: now})
	createRow(t, r, &model.GroupPermission{ID: 50, UserGroupID: 1, TunnelGroupID: 1, CreatedTime: now})

	result := b3ImportUT10(t, r)
	if n := countWhere(t, r, "user_tunnel", "id = ?", 10); n != 1 {
		t.Fatalf("R5X-015 transition warning must not delete the UT row, got %d rows", n)
	}
	if result.UserTunnelsImported != 1 {
		t.Fatalf("UT must still count as imported, got %d", result.UserTunnelsImported)
	}
	if !b8WarningsContain(result.ImportWarnings, "无台账") {
		t.Fatalf("R5X-015: newly landed UT with no grant and no covering pair must add a generic warning, got %v", result.ImportWarnings)
	}
}

// 子形态 ②：权限对已移除、成员保留（pair 撤销终态）——同口径 warning。
func TestR5B3_015_UncoveredNewUTWarnsPairRemoved(t *testing.T) {
	now := time.Now().UnixMilli()
	r := openBackupRepo(t, "b3-015-nopair.db")
	createRow(t, r, &model.User{ID: 2, User: "u2", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, r, &model.Tunnel{ID: 1, Name: "t1", TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.UserGroup{ID: 1, Name: "g-one", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.TunnelGroup{ID: 1, Name: "tg-one", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.UserGroupUser{UserGroupID: 1, UserID: 2, CreatedTime: now})
	createRow(t, r, &model.TunnelGroupTunnel{TunnelGroupID: 1, TunnelID: 1, CreatedTime: now})
	// 无 GroupPermission 行：pair 已移除，无任何权限对覆盖 (2,1)。

	result := b3ImportUT10(t, r)
	if n := countWhere(t, r, "user_tunnel", "id = ?", 10); n != 1 {
		t.Fatalf("R5X-015 transition warning must not delete the UT row, got %d rows", n)
	}
	if !b8WarningsContain(result.ImportWarnings, "无台账") {
		t.Fatalf("R5X-015: newly landed UT with no grant and no covering pair must add a generic warning, got %v", result.ImportWarnings)
	}
}

// 防过修对照：被现行启用权限对覆盖的手工 UT（无 grant）照常落行
// 且不得新增无台账 warning；被停用对覆盖的 UT 仍按 R4X-006 删除
// 并只计撤销 warning，不得重复计无台账 warning。
func TestR5B3_015_CoveredNewUTNoUncoveredWarning(t *testing.T) {
	now := time.Now().UnixMilli()

	t.Run("covered by enabled pair", func(t *testing.T) {
		r := openBackupRepo(t, "b3-015-covered.db")
		createRow(t, r, &model.User{ID: 2, User: "u2", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
		createRow(t, r, &model.Tunnel{ID: 1, Name: "t1", TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now})
		createRow(t, r, &model.UserGroup{ID: 1, Name: "g-one", Status: 1, CreatedTime: now, UpdatedTime: now})
		createRow(t, r, &model.TunnelGroup{ID: 1, Name: "tg-one", Status: 1, CreatedTime: now, UpdatedTime: now})
		createRow(t, r, &model.UserGroupUser{UserGroupID: 1, UserID: 2, CreatedTime: now})
		createRow(t, r, &model.TunnelGroupTunnel{TunnelGroupID: 1, TunnelID: 1, CreatedTime: now})
		createRow(t, r, &model.GroupPermission{ID: 50, UserGroupID: 1, TunnelGroupID: 1, CreatedTime: now})

		result := b3ImportUT10(t, r)
		if n := countWhere(t, r, "user_tunnel", "id = ?", 10); n != 1 {
			t.Fatalf("manual UT covered by an enabled pair must land, got %d rows", n)
		}
		if b8WarningsContain(result.ImportWarnings, "无台账") {
			t.Fatalf("covered manual UT must not add the uncovered warning, got %v", result.ImportWarnings)
		}
	})

	t.Run("covered by disabled pair still swept without uncovered warning", func(t *testing.T) {
		r := openBackupRepo(t, "b3-015-disabled.db")
		b4SeedRevokedState(t, r, now)

		result := b3ImportUT10(t, r)
		if n := countWhere(t, r, "user_tunnel", "id = ?", 10); n != 0 {
			t.Fatalf("UT covered by a disabled pair must still be swept (R4X-006), got %d rows", n)
		}
		if b8WarningsContain(result.ImportWarnings, "无台账授权存续") {
			t.Fatalf("swept UT must not also be counted in the uncovered warning, got %v", result.ImportWarnings)
		}
	})
}
