package repo

import (
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R6 批 2 回归（grant 生命周期两端对账）：
//   - R6X-004：importPermissions 的 grant 同键占位直删须先做来源
//     OR——占位行 cb=1 且备份行有效 cb（effectiveCB，经 R5X-014
//     降级判定后）为 0 时，把落库行提升为 cb=1 并以 warning 披露
//     合并；四格与 R5X-005 同构。
//   - R6X-005：sweep 的 grants>0 豁免须改逐条 grantPairMembershipHoldsTx
//     验对应——任一 grant 对应成立才豁免；全假时走停用对裁定并
//     对错位 grant 计 warning。ShieldChain / Control / 合法覆盖
//     变体三例互为对照。

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
//	(1,0) 本条缺陷格：落库行 cb 须提升为 1、warning 含合并披露；
//	      后续组撤销可达该 UT（UT10 被删、回传 (2,1) 对）。
//	(0,1) 防过修格：备份行本已 cb=1，占位 cb=0 不得覆盖/降级，
//	      落库 cb=1、无合并披露（无损）。
//	(1,1) 落库 cb=1、无合并披露（无损）。
//	(0,0) 防过修格：两行皆 cb=0，不得凭空造出组建来源，落库
//	      cb=0、后续组撤销不可达（UT10 存活）。
func TestR6B2_004_PlaceholderGrantCreatorFlagOR(t *testing.T) {
	cells := []struct {
		name       string
		occCB      int
		backupCB   int
		wantCB     int
		wantMerge  bool
		wantUTGone bool
	}{
		{"placeholder cb=1 backup cb=0", 1, 0, 1, true, true},
		{"placeholder cb=0 backup cb=1", 0, 1, 1, false, true},
		{"both cb=1", 1, 1, 1, false, true},
		{"both cb=0", 0, 0, 0, false, false},
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
			revoked, err := r.RevokeGroupGrantsByGroup("user_group", 1)
			if err != nil {
				t.Fatalf("revoke group: %v", err)
			}
			utRows := countWhere(t, r, "user_tunnel", "id = ?", 10)
			if cell.wantUTGone {
				if utRows != 0 {
					t.Fatalf("R6X-004: group-created UT10 must be reachable by the later group revoke, got %d rows (revoked=%v)", utRows, revoked)
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
					t.Fatalf("manual UT10 (merged cb=0) must survive group revoke, got %d rows", utRows)
				}
				if len(revoked) != 0 {
					t.Fatalf("no pair may be revoked for a merged cb=0 grant, got %v", revoked)
				}
			}
		})
	}
}

// ─── R6X-005 sweep 逐条验对应三例 ────────────────────────────────────

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
// 不得再屏蔽停用对裁定——UT10 被 sweep 裁定删除、skipped 含
// userTunnelsRevoked、错位 grant 计 warning；其后 UG1/UG2 两组
// 撤销对该 UT 均无对象（已裁定，不再复活）。
func TestR6B2_005_ShieldChainSweptDespiteMisplacedGrant(t *testing.T) {
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
	if n := countWhere(t, r, "user_tunnel", "id = ?", 10); n != 0 {
		t.Fatalf("R6X-005: UT10 shielded only by a misplaced grant must be swept by the disabled pair, got %d rows (warnings=%v)", n, result.ImportWarnings)
	}
	if result.SkippedRows["userTunnelsRevoked"] != 1 {
		t.Fatalf("R6X-005: skipped must count userTunnelsRevoked=1, got %v", result.SkippedRows)
	}
	if !b8WarningsContain(result.ImportWarnings, "错位") {
		t.Fatalf("R6X-005: misplaced grant must be counted in ImportWarnings, got %v", result.ImportWarnings)
	}
}

// Control 对照：同预置、无屏蔽 grant，仅导入 userTunnels——UT10
// 照旧被 sweep 裁定（与 ShieldChain 修复后终态一致、与既有
// TestR4B4_006 探针 C 同终态）。
func TestR6B2_005_ControlNoGrantSwept(t *testing.T) {
	now := time.Now().UnixMilli()
	r := openBackupRepo(t, "b2-005-control.db")
	b2SeedShield(t, r, now, false)

	result, err := r.Import(b2ShieldBackup(now, false), []string{"userTunnels"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if n := countWhere(t, r, "user_tunnel", "id = ?", 10); n != 0 {
		t.Fatalf("control: UT10 covered by a disabled pair with no grant must be swept, got %d rows", n)
	}
	if result.SkippedRows["userTunnelsRevoked"] != 1 {
		t.Fatalf("control: skipped must count userTunnelsRevoked=1, got %v", result.SkippedRows)
	}
}

// 合法覆盖变体（防过修）：grant60 改挂成员齐备的启用对 (2,2)
// （U2∈UG2、T1∈TG2）——对应成立的 grant 仍构成豁免，UT10 存续
// 逐字不变、无撤销 skipped、无错位 warning。
func TestR6B2_005_LegitimateGrantStillExempts(t *testing.T) {
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
