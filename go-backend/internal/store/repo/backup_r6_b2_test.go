package repo

import (
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R6 批 2 回归（grant 台账保真）：
//   - R6X-004：importPermissions 的 grant 同键占位直删须先做来源
//     OR——占位行 cb=1 且备份行有效 cb（effectiveCB，经 R5X-014
//     降级判定后）为 0 时，把落库行提升为 cb=1 并以 warning 披露
//     合并；四格与 R5X-005 同构。R5-FGRP 后 cb 只作来源记录：
//     四格删除组后 UT10 一律存活。
//   - R6X-005（原 sweep 豁免口径已随 R5-FGRP 删除）：导入 UT 不再
//     按停用对/台账对应裁定——ShieldChain / Control / 合法覆盖
//     三例的 UT10 一律逐字落行、无 userTunnelsRevoked skipped、
//     无错位 warning。

// ─── R6X-004 同键占位 cb 四格 ─────────────────────────────────────────

// b2SeedPlaceholder 造 H6-01 探针 P3 同形态：目标库已有 grant #99
// （UG1/TG1/UT10，cb=occCB），两组均启用、成员对应成立（U2∈UG1、
// T1∈TG1），故备份 grant 的 effectiveCB 即其自带 cb。
func b2SeedPlaceholder(t *testing.T, r *Repository, now int64, occCB int) {
	t.Helper()
	createRow(t, r, &model.User{ID: 2, User: "u2", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, r, &model.Tunnel{ID: 1, Name: "t1", TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.UserGroup{ID: 1, Name: "g-one", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.TunnelGroup{ID: 1, Name: "tg-one", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.UserGroupUser{UserGroupID: 1, UserID: 2, CreatedTime: now})
	createRow(t, r, &model.TunnelGroupTunnel{TunnelGroupID: 1, TunnelID: 1, CreatedTime: now})
	createRow(t, r, &model.GroupPermission{ID: 50, UserGroupID: 1, TunnelGroupID: 1, CreatedTime: now})
	createRow(t, r, &model.UserTunnel{ID: 10, UserID: 2, TunnelID: 1, Status: 1})
	createRow(t, r, &model.GroupPermissionGrant{ID: 99, UserGroupID: 1, TunnelGroupID: 1, UserTunnelID: 10, CreatedByGroup: occCB, CreatedTime: now})
}

// 四格 = (占位行 cb, 备份有效 cb)：
//
//	(1,0) 本条缺陷格：落库行 cb 须提升为 1、warning 含合并披露。
//	(0,1) 防过修格：备份行本已 cb=1，占位 cb=0 不得覆盖/降级，
//	      落库 cb=1、无合并披露（无损）。
//	(1,1) 落库 cb=1、无合并披露（无损）。
//	(0,0) 防过修格：两行皆 cb=0，不得凭空造出组建来源，落库 cb=0。
//
// 四格共同尾声（R5-FGRP）：删除组只做记账级联，UT10 一律存活。
func TestR6B2_004_PlaceholderGrantCreatorFlagOR(t *testing.T) {
	cells := []struct {
		name      string
		occCB     int
		backupCB  int
		wantCB    int
		wantMerge bool
	}{
		{"placeholder cb=1 backup cb=0", 1, 0, 1, true},
		{"placeholder cb=0 backup cb=1", 0, 1, 1, false},
		{"both cb=1", 1, 1, 1, false},
		{"both cb=0", 0, 0, 0, false},
	}
	for _, cell := range cells {
		t.Run(cell.name, func(t *testing.T) {
			now := time.Now().UnixMilli()
			r := openBackupRepo(t, "b2-004.db")
			b2SeedPlaceholder(t, r, now, cell.occCB)

			backup := &model.BackupData{
				Version: "1.0",
				Permissions: []model.PermissionBackup{
					{ID: 50, UserGroupID: 1, TunnelGroupID: 1, CreatedTime: now, Grants: []model.PermissionGrantBackup{
						{ID: 100, UserGroupID: 1, TunnelGroupID: 1, UserTunnelID: 10, CreatedByGroup: cell.backupCB, CreatedTime: now},
					}},
				},
			}
			result, err := r.Import(backup, []string{"permissions"})
			if err != nil {
				t.Fatalf("import: %v", err)
			}
			if n := countWhere(t, r, "group_permission_grant", "id = ?", 99); n != 0 {
				t.Fatalf("placeholder grant 99 must be replaced, got %d rows", n)
			}
			var kept model.GroupPermissionGrant
			if err := r.DB().Where("id = ?", 100).First(&kept).Error; err != nil {
				t.Fatalf("read restored grant 100: %v", err)
			}
			if kept.CreatedByGroup != cell.wantCB {
				t.Fatalf("R6X-004: restored grant created_by_group = %d, want OR(%d,%d) = %d", kept.CreatedByGroup, cell.occCB, cell.backupCB, cell.wantCB)
			}
			if got := b8WarningsContain(result.ImportWarnings, "合并"); got != cell.wantMerge {
				t.Fatalf("R6X-004: merge disclosure in warnings = %v, want %v (warnings=%v)", got, cell.wantMerge, result.ImportWarnings)
			}
			if err := r.GroupDeleteCascade("user_group", 1); err != nil {
				t.Fatalf("delete group: %v", err)
			}
			if n := countWhere(t, r, "user_tunnel", "id = ?", 10); n != 1 {
				t.Fatalf("R5-FGRP: UT10 must survive group deletion in every merge cell (merged cb=%d), got %d rows", cell.wantCB, n)
			}
		})
	}
}

// ─── R6X-005 原 sweep 三例（裁定已删，改为逐字落行口径） ───────────

// b2SeedShield 造 R6-G-01 探针同形态：停用覆盖对 UG1（status=0，
// 成员含 U2）×TG1（成员含 T1）、权限对 (1,1)；另置 UG2×TG2 权限对
// (2,2)，两组启用。legitVariant 时 UG2/TG2 成员齐备（U2/T1 在内），
// 否则两组无任何成员。
func b2SeedShield(t *testing.T, r *Repository, now int64, legitVariant bool) {
	t.Helper()
	createRow(t, r, &model.User{ID: 2, User: "u2", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, r, &model.Tunnel{ID: 1, Name: "t1", TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.UserGroup{ID: 1, Name: "g-off", Status: 0, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.TunnelGroup{ID: 1, Name: "tg-one", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.UserGroup{ID: 2, Name: "g-two", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.TunnelGroup{ID: 2, Name: "tg-two", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.UserGroupUser{UserGroupID: 1, UserID: 2, CreatedTime: now})
	createRow(t, r, &model.TunnelGroupTunnel{TunnelGroupID: 1, TunnelID: 1, CreatedTime: now})
	if legitVariant {
		createRow(t, r, &model.UserGroupUser{UserGroupID: 2, UserID: 2, CreatedTime: now})
		createRow(t, r, &model.TunnelGroupTunnel{TunnelGroupID: 2, TunnelID: 1, CreatedTime: now})
	}
	createRow(t, r, &model.GroupPermission{ID: 50, UserGroupID: 1, TunnelGroupID: 1, CreatedTime: now})
	createRow(t, r, &model.GroupPermission{ID: 51, UserGroupID: 2, TunnelGroupID: 2, CreatedTime: now})
}

func b2ShieldBackup(now int64, withGrant bool) *model.BackupData {
	b := &model.BackupData{
		Version:     "1.0",
		UserTunnels: []model.UserTunnelBackup{{ID: 10, UserID: 2, TunnelID: 1, Status: 1}},
	}
	if withGrant {
		b.Permissions = []model.PermissionBackup{
			{ID: 51, UserGroupID: 2, TunnelGroupID: 2, CreatedTime: now, Grants: []model.PermissionGrantBackup{
				{ID: 60, UserGroupID: 2, TunnelGroupID: 2, UserTunnelID: 10, CreatedByGroup: 0, CreatedTime: now},
			}},
		}
	}
	return b
}

// ShieldChain 例：同批落位的错位 grant（cb=0、成员对应不成立）
// 在 R5-FGRP 后不再触发任何裁定——UT10 逐字落行、无
// userTunnelsRevoked skipped、无错位 warning（grant 行本身仍按
// 写回口径落库）。
func TestR6B2_005_ShieldChainLandsVerbatim(t *testing.T) {
	now := time.Now().UnixMilli()
	r := openBackupRepo(t, "b2-005-shield.db")
	b2SeedShield(t, r, now, false)

	result, err := r.Import(b2ShieldBackup(now, true), []string{"userTunnels", "permissions"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if n := countWhere(t, r, "group_permission_grant", "id = ?", 60); n != 1 {
		t.Fatalf("misplaced grant row itself still lands (write-back口径不变), got %d rows", n)
	}
	if n := countWhere(t, r, "user_tunnel", "id = ?", 10); n != 1 {
		t.Fatalf("R5-FGRP: UT10 must land verbatim regardless of pair/grant state, got %d rows (warnings=%v)", n, result.ImportWarnings)
	}
	if result.SkippedRows["userTunnelsRevoked"] != 0 {
		t.Fatalf("R5-FGRP: no userTunnelsRevoked skip may be counted, got %v", result.SkippedRows)
	}
	if b8WarningsContain(result.ImportWarnings, "错位") {
		t.Fatalf("R5-FGRP: the removed misplaced-grant sweep warning must not be emitted, got %v", result.ImportWarnings)
	}
}

// Control 对照：同预置、无屏蔽 grant，仅导入 userTunnels——UT10
// 同样逐字落行（旧 sweep 终态已废止）。
func TestR6B2_005_ControlNoGrantLandsVerbatim(t *testing.T) {
	now := time.Now().UnixMilli()
	r := openBackupRepo(t, "b2-005-control.db")
	b2SeedShield(t, r, now, false)

	result, err := r.Import(b2ShieldBackup(now, false), []string{"userTunnels"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if n := countWhere(t, r, "user_tunnel", "id = ?", 10); n != 1 {
		t.Fatalf("R5-FGRP: UT10 covered by a disabled pair with no grant must land verbatim, got %d rows", n)
	}
	if result.SkippedRows["userTunnelsRevoked"] != 0 {
		t.Fatalf("R5-FGRP: no userTunnelsRevoked skip may be counted, got %v", result.SkippedRows)
	}
}

// 合法覆盖变体：grant60 挂成员齐备的启用对 (2,2)（U2∈UG2、
// T1∈TG2）——UT10 逐字落行、grant 存续、无撤销 skipped、无错位
// warning（与另两例同终态：分组状态不再裁定授权）。
func TestR6B2_005_LegitimateGrantLandsVerbatim(t *testing.T) {
	now := time.Now().UnixMilli()
	r := openBackupRepo(t, "b2-005-legit.db")
	b2SeedShield(t, r, now, true)

	result, err := r.Import(b2ShieldBackup(now, true), []string{"userTunnels", "permissions"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if n := countWhere(t, r, "user_tunnel", "id = ?", 10); n != 1 {
		t.Fatalf("R6X-005 anti-overfix: UT10 covered by a legitimate grant must survive verbatim, got %d rows (warnings=%v)", n, result.ImportWarnings)
	}
	if n := countWhere(t, r, "group_permission_grant", "id = ?", 60); n != 1 {
		t.Fatalf("legitimate grant 60 must survive, got %d rows", n)
	}
	if result.SkippedRows["userTunnelsRevoked"] != 0 {
		t.Fatalf("legitimate variant must not be counted as revoked, got %v", result.SkippedRows)
	}
	if b8WarningsContain(result.ImportWarnings, "错位") {
		t.Fatalf("legitimate variant must not add a misplaced-grant warning, got %v", result.ImportWarnings)
	}
}
