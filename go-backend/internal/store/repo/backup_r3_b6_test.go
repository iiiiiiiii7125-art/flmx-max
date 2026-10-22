package repo

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R3 批 6 回归：备份导入生命周期与引用处置（R3X-005 三 facet、
// R3X-023、卫生项 H-S01）。
//
// facet (a) 路线定死（本批）：成员替换撤销的 UT 经 ImportResult 新增
// 字段 revokedUserTunnels 回传 handler 清转发（与运行时全部撤销路径
// 同口径），不用「回执明示待清理」的弱路线。本文件的 revoked 断言
// 走结果 JSON 线格式（userId/tunnelId），与字段的线上契约一致。

type b6RevokedPair struct {
	UserID   int64 `json:"userId"`
	TunnelID int64 `json:"tunnelId"`
}

func b6RevokedPairs(t *testing.T, result *model.ImportResult) []b6RevokedPair {
	t.Helper()
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal import result: %v", err)
	}
	var view struct {
		RevokedUserTunnels []b6RevokedPair `json:"revokedUserTunnels"`
	}
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatalf("unmarshal import result: %v", err)
	}
	return view.RevokedUserTunnels
}

func b6HasPair(pairs []b6RevokedPair, userID, tunnelID int64) bool {
	for _, p := range pairs {
		if p.UserID == userID && p.TunnelID == tunnelID {
			return true
		}
	}
	return false
}

func b6SeedGroupDeps(t *testing.T, r *Repository, now int64) {
	t.Helper()
	createRow(t, r, &model.User{ID: 2, User: "u2", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, r, &model.User{ID: 4, User: "u4", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, r, &model.Tunnel{ID: 1, Name: "t1", TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.Tunnel{ID: 2, Name: "t2", TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now})
}

// R3X-005 facet (a) 用户组侧：成员替换必须在替换前按台账撤销被移出
// 成员的组建授权（红：UT10/grant 原样保留、回执无撤销清单）。
func TestR3B6ImportUserGroupsMemberReplacementRevokes(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "b6-faceta-user.db")
	b6SeedGroupDeps(t, dest, now)
	createRow(t, dest, &model.UserGroup{ID: 1, Name: "g-one", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.TunnelGroup{ID: 1, Name: "tg-one", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.UserGroupUser{UserGroupID: 1, UserID: 2, CreatedTime: now})
	createRow(t, dest, &model.UserGroupUser{UserGroupID: 1, UserID: 4, CreatedTime: now})
	createRow(t, dest, &model.GroupPermission{ID: 50, UserGroupID: 1, TunnelGroupID: 1, CreatedTime: now})
	// UT10: U2/T1 组建授权；UT11: U4/T1 手工在先、分组仅登记。
	createRow(t, dest, &model.UserTunnel{ID: 10, UserID: 2, TunnelID: 1, Status: 1})
	createRow(t, dest, &model.UserTunnel{ID: 11, UserID: 4, TunnelID: 1, Status: 1})
	createRow(t, dest, &model.GroupPermissionGrant{ID: 60, UserGroupID: 1, TunnelGroupID: 1, UserTunnelID: 10, CreatedByGroup: 1, CreatedTime: now})
	createRow(t, dest, &model.GroupPermissionGrant{ID: 61, UserGroupID: 1, TunnelGroupID: 1, UserTunnelID: 11, CreatedByGroup: 0, CreatedTime: now})

	backup := &model.BackupData{
		Version: "1.0",
		UserGroups: []model.UserGroupBackup{
			{ID: 1, Name: "g-one", Status: 1, CreatedTime: now, Users: []int64{4}},
		},
	}
	result, err := dest.Import(backup, []string{"userGroups"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.UserGroupsImported != 1 {
		t.Fatalf("userGroups imported = %d, want 1", result.UserGroupsImported)
	}
	if n := countWhere(t, dest, "user_group_user", "user_group_id = ? AND user_id = ?", 1, 2); n != 0 {
		t.Fatalf("member row for removed user 2 must be replaced away, got %d", n)
	}
	if n := countWhere(t, dest, "user_tunnel", "id = ?", 10); n != 0 {
		t.Fatalf("R3X-005(a): group-created UT of removed member must be revoked, got %d rows", n)
	}
	if n := countWhere(t, dest, "group_permission_grant", "user_tunnel_id = ?", 10); n != 0 {
		t.Fatalf("R3X-005(a): grant ledger of removed member must be revoked, got %d rows", n)
	}
	if n := countWhere(t, dest, "user_tunnel", "id = ?", 11); n != 1 {
		t.Fatalf("manual UT of remaining member must survive, got %d", n)
	}
	if pairs := b6RevokedPairs(t, result); !b6HasPair(pairs, 2, 1) {
		t.Fatalf("R3X-005(a): revoked UT must be returned via revokedUserTunnels for handler forward cleanup, got %v (raw result %+v)", pairs, result)
	}
}

// R3X-005 facet (a) 隧道组侧：成员替换同口径撤销被移出隧道的组建授权。
func TestR3B6ImportTunnelGroupsMemberReplacementRevokes(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "b6-faceta-tunnel.db")
	b6SeedGroupDeps(t, dest, now)
	createRow(t, dest, &model.UserGroup{ID: 1, Name: "g-one", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.TunnelGroup{ID: 1, Name: "tg-one", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.TunnelGroupTunnel{TunnelGroupID: 1, TunnelID: 1, CreatedTime: now})
	createRow(t, dest, &model.TunnelGroupTunnel{TunnelGroupID: 1, TunnelID: 2, CreatedTime: now})
	createRow(t, dest, &model.GroupPermission{ID: 50, UserGroupID: 1, TunnelGroupID: 1, CreatedTime: now})
	createRow(t, dest, &model.UserTunnel{ID: 12, UserID: 2, TunnelID: 1, Status: 1})
	createRow(t, dest, &model.UserTunnel{ID: 13, UserID: 2, TunnelID: 2, Status: 1})
	createRow(t, dest, &model.GroupPermissionGrant{ID: 62, UserGroupID: 1, TunnelGroupID: 1, UserTunnelID: 12, CreatedByGroup: 1, CreatedTime: now})
	createRow(t, dest, &model.GroupPermissionGrant{ID: 63, UserGroupID: 1, TunnelGroupID: 1, UserTunnelID: 13, CreatedByGroup: 1, CreatedTime: now})

	backup := &model.BackupData{
		Version: "1.0",
		TunnelGroups: []model.TunnelGroupBackup{
			{ID: 1, Name: "tg-one", Status: 1, CreatedTime: now, Tunnels: []int64{1}},
		},
	}
	result, err := dest.Import(backup, []string{"tunnelGroups"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if n := countWhere(t, dest, "user_tunnel", "id = ?", 13); n != 0 {
		t.Fatalf("R3X-005(a): group-created UT of removed tunnel must be revoked, got %d rows", n)
	}
	if n := countWhere(t, dest, "group_permission_grant", "user_tunnel_id = ?", 13); n != 0 {
		t.Fatalf("R3X-005(a): grant ledger of removed tunnel must be revoked, got %d rows", n)
	}
	if n := countWhere(t, dest, "user_tunnel", "id = ?", 12); n != 1 {
		t.Fatalf("UT of remaining tunnel must survive, got %d", n)
	}
	if pairs := b6RevokedPairs(t, result); !b6HasPair(pairs, 2, 2) {
		t.Fatalf("R3X-005(a): revoked UT must be returned via revokedUserTunnels, got %v", pairs)
	}
}

// R3X-005 facet (b)（verdicts §5 从 H 案）：同名占位组删除前，
// permission/grant 的组键必须重指向同名继任组并计 ImportWarnings，
// 不得留悬空、不得按 G 案删台账（红：组键仍指向已删组 1）。
func TestR3B6ImportSameNameUserGroupRepointsPermissions(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "b6-facetb-user.db")
	b6SeedGroupDeps(t, dest, now)
	createRow(t, dest, &model.UserGroup{ID: 1, Name: "g-one", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.TunnelGroup{ID: 1, Name: "tg-one", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.UserGroupUser{UserGroupID: 1, UserID: 2, CreatedTime: now})
	createRow(t, dest, &model.GroupPermission{ID: 50, UserGroupID: 1, TunnelGroupID: 1, CreatedTime: now})
	createRow(t, dest, &model.UserTunnel{ID: 10, UserID: 2, TunnelID: 1, Status: 1})
	createRow(t, dest, &model.UserTunnel{ID: 11, UserID: 4, TunnelID: 1, Status: 1})
	createRow(t, dest, &model.UserTunnel{ID: 12, UserID: 2, TunnelID: 2, Status: 1})
	createRow(t, dest, &model.GroupPermissionGrant{ID: 60, UserGroupID: 1, TunnelGroupID: 1, UserTunnelID: 10, CreatedByGroup: 1, CreatedTime: now})
	createRow(t, dest, &model.GroupPermissionGrant{ID: 61, UserGroupID: 1, TunnelGroupID: 1, UserTunnelID: 11, CreatedByGroup: 1, CreatedTime: now})
	createRow(t, dest, &model.GroupPermissionGrant{ID: 62, UserGroupID: 1, TunnelGroupID: 1, UserTunnelID: 12, CreatedByGroup: 1, CreatedTime: now})

	backup := &model.BackupData{
		Version: "1.0",
		UserGroups: []model.UserGroupBackup{
			{ID: 99, Name: "g-one", Status: 1, CreatedTime: now, Users: []int64{2}},
		},
	}
	result, err := dest.Import(backup, []string{"userGroups"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if n := countWhere(t, dest, "user_group", "id = ?", 1); n != 0 {
		t.Fatalf("placeholder group 1 must be replaced, got %d", n)
	}
	var perm model.GroupPermission
	if err := dest.DB().Where("id = ?", 50).First(&perm).Error; err != nil {
		t.Fatalf("R3X-005(b): permission row must survive via repoint, not be deleted: %v", err)
	}
	if perm.UserGroupID != 99 || perm.TunnelGroupID != 1 {
		t.Fatalf("R3X-005(b): permission group keys = (%d,%d), want repointed to successor (99,1)", perm.UserGroupID, perm.TunnelGroupID)
	}
	if n := countWhere(t, dest, "group_permission_grant", "user_group_id = ?", 99); n != 3 {
		t.Fatalf("R3X-005(b): all 3 grants must be repointed to successor group 99, got %d", n)
	}
	if n := countWhere(t, dest, "group_permission_grant", "user_group_id = ?", 1); n != 0 {
		t.Fatalf("R3X-005(b): no grant may keep pointing at deleted group 1, got %d", n)
	}
	if n := countWhere(t, dest, "user_tunnel", "id IN (10, 11, 12)"); n != 3 {
		t.Fatalf("R3X-005(b): group-created UTs must survive (grants follow the successor group), got %d", n)
	}
	if !b8WarningsContain(result.ImportWarnings, "重指向") {
		t.Fatalf("R3X-005(b): repointing must be counted in ImportWarnings, got %v", result.ImportWarnings)
	}
}

// facet (b) 合并分支：继任组已存在同键 permission/grant 时，重指向
// 必须合并去重而非撞唯一约束整单回滚。
func TestR3B6ImportSameNameRepointMergesDuplicates(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "b6-facetb-merge.db")
	b6SeedGroupDeps(t, dest, now)
	// 占位组 99 与继任组 4（备份 id）异名并存；同名删除命中的是 99。
	createRow(t, dest, &model.UserGroup{ID: 99, Name: "grp", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.UserGroup{ID: 4, Name: "other", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.TunnelGroup{ID: 98, Name: "tg", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.GroupPermission{ID: 50, UserGroupID: 99, TunnelGroupID: 98, CreatedTime: now})
	createRow(t, dest, &model.GroupPermission{ID: 51, UserGroupID: 4, TunnelGroupID: 98, CreatedTime: now})
	createRow(t, dest, &model.UserTunnel{ID: 20, UserID: 2, TunnelID: 1, Status: 1})
	createRow(t, dest, &model.GroupPermissionGrant{ID: 60, UserGroupID: 99, TunnelGroupID: 98, UserTunnelID: 20, CreatedByGroup: 1, CreatedTime: now})
	createRow(t, dest, &model.GroupPermissionGrant{ID: 61, UserGroupID: 4, TunnelGroupID: 98, UserTunnelID: 20, CreatedByGroup: 1, CreatedTime: now})

	backup := &model.BackupData{
		Version: "1.0",
		UserGroups: []model.UserGroupBackup{
			{ID: 4, Name: "grp", Status: 1, CreatedTime: now, Users: []int64{2}},
		},
	}
	result, err := dest.Import(backup, []string{"userGroups"})
	if err != nil {
		t.Fatalf("import must not fail on repoint collision: %v", err)
	}
	if n := countWhere(t, dest, "group_permission", "user_group_id = ? AND tunnel_group_id = ?", 4, 98); n != 1 {
		t.Fatalf("merged permission for successor pair must be exactly 1 row, got %d", n)
	}
	if n := countWhere(t, dest, "group_permission_grant", "user_group_id = ? AND tunnel_group_id = ? AND user_tunnel_id = ?", 4, 98, 20); n != 1 {
		t.Fatalf("merged grant for successor key must be exactly 1 row, got %d", n)
	}
	if n := countWhere(t, dest, "group_permission", "user_group_id = ?", 99); n != 0 {
		t.Fatalf("no permission may keep pointing at deleted group 99, got %d", n)
	}
	if n := countWhere(t, dest, "group_permission_grant", "user_group_id = ?", 99); n != 0 {
		t.Fatalf("no grant may keep pointing at deleted group 99, got %d", n)
	}
	if len(result.ImportWarnings) == 0 {
		t.Fatalf("repoint/merge must be counted in ImportWarnings, got %v", result.ImportWarnings)
	}
}

// R3X-005 facet (c) 组段面：导入把组置 status=0 时，其组建授权必须
// 统一收敛撤销（红：status=0 而 grants/UT 全数存续）；手工 UT 不动。
func TestR3B6ImportDisabledUserGroupConvergesGrants(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "b6-facetc-group.db")
	b6SeedGroupDeps(t, dest, now)
	createRow(t, dest, &model.UserGroup{ID: 1, Name: "g-one", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.TunnelGroup{ID: 1, Name: "tg-one", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.UserGroupUser{UserGroupID: 1, UserID: 2, CreatedTime: now})
	createRow(t, dest, &model.UserGroupUser{UserGroupID: 1, UserID: 4, CreatedTime: now})
	createRow(t, dest, &model.GroupPermission{ID: 50, UserGroupID: 1, TunnelGroupID: 1, CreatedTime: now})
	createRow(t, dest, &model.UserTunnel{ID: 10, UserID: 2, TunnelID: 1, Status: 1})
	createRow(t, dest, &model.UserTunnel{ID: 11, UserID: 4, TunnelID: 1, Status: 1})
	createRow(t, dest, &model.GroupPermissionGrant{ID: 60, UserGroupID: 1, TunnelGroupID: 1, UserTunnelID: 10, CreatedByGroup: 1, CreatedTime: now})
	createRow(t, dest, &model.GroupPermissionGrant{ID: 61, UserGroupID: 1, TunnelGroupID: 1, UserTunnelID: 11, CreatedByGroup: 0, CreatedTime: now})

	backup := &model.BackupData{
		Version: "1.0",
		UserGroups: []model.UserGroupBackup{
			{ID: 1, Name: "g-one", Status: 0, CreatedTime: now, Users: []int64{2, 4}},
		},
	}
	result, err := dest.Import(backup, []string{"userGroups"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	var grp model.UserGroup
	if err := dest.DB().Where("id = ?", 1).First(&grp).Error; err != nil {
		t.Fatalf("group row: %v", err)
	}
	if grp.Status != 0 {
		t.Fatalf("group status must be restored as 0, got %d", grp.Status)
	}
	if n := countWhere(t, dest, "group_permission_grant", "user_tunnel_id = ?", 10); n != 0 {
		t.Fatalf("R3X-005(c): disabled group must not keep its group-created grants, got %d", n)
	}
	if n := countWhere(t, dest, "user_tunnel", "id = ?", 10); n != 0 {
		t.Fatalf("R3X-005(c): group-created UT of disabled group must be revoked on import convergence, got %d", n)
	}
	if n := countWhere(t, dest, "user_tunnel", "id = ?", 11); n != 1 {
		t.Fatalf("manual UT must survive disabled-group convergence, got %d", n)
	}
	if pairs := b6RevokedPairs(t, result); !b6HasPair(pairs, 2, 1) {
		t.Fatalf("R3X-005(c): converged UT must be returned via revokedUserTunnels, got %v", pairs)
	}
}

// R3X-005 facet (c) 权限段面：任一侧组 status≠1 时跳过该 pair 的
// grants 并计 warning，且该 pair 的既有组建授权一并收敛（红：
// 停用对的 grant 直接落库、既有 grant 原样保留、零警告）。
func TestR3B6ImportPermissionsSkipsDisabledPair(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "b6-facetc-perm.db")
	b6SeedGroupDeps(t, dest, now)
	createRow(t, dest, &model.UserGroup{ID: 1, Name: "g-one", Status: 0, CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.TunnelGroup{ID: 1, Name: "tg-one", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.UserTunnel{ID: 10, UserID: 2, TunnelID: 1, Status: 1})
	createRow(t, dest, &model.UserTunnel{ID: 11, UserID: 4, TunnelID: 1, Status: 1})
	createRow(t, dest, &model.GroupPermissionGrant{ID: 59, UserGroupID: 1, TunnelGroupID: 1, UserTunnelID: 11, CreatedByGroup: 1, CreatedTime: now})

	backup := &model.BackupData{
		Version: "1.0",
		Permissions: []model.PermissionBackup{
			{ID: 50, UserGroupID: 1, TunnelGroupID: 1, CreatedTime: now, Grants: []model.PermissionGrantBackup{
				{ID: 60, UserGroupID: 1, TunnelGroupID: 1, UserTunnelID: 10, CreatedByGroup: 1, CreatedTime: now},
			}},
		},
	}
	result, err := dest.Import(backup, []string{"permissions"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.PermissionsImported != 1 {
		t.Fatalf("permission pair itself must still import, got %d", result.PermissionsImported)
	}
	if n := countWhere(t, dest, "group_permission_grant", "id = ?", 60); n != 0 {
		t.Fatalf("R3X-005(c): grants of a disabled pair must be skipped, got %d rows", n)
	}
	if n := countWhere(t, dest, "group_permission_grant", "id = ?", 59); n != 0 {
		t.Fatalf("R3X-005(c): pre-existing grants of a disabled pair must converge (be revoked), got %d rows", n)
	}
	if n := countWhere(t, dest, "user_tunnel", "id = ?", 11); n != 0 {
		t.Fatalf("R3X-005(c): group-created UT behind the revoked pre-existing grant must be revoked, got %d", n)
	}
	if n := countWhere(t, dest, "user_tunnel", "id = ?", 10); n != 1 {
		t.Fatalf("UT referenced only by the skipped backup grant must survive, got %d", n)
	}
	if !b8WarningsContain(result.ImportWarnings, "停用") {
		t.Fatalf("R3X-005(c): skipping a disabled pair must be counted in ImportWarnings, got %v", result.ImportWarnings)
	}
	if pairs := b6RevokedPairs(t, result); !b6HasPair(pairs, 4, 1) {
		t.Fatalf("R3X-005(c): converged UT must be returned via revokedUserTunnels, got %v", pairs)
	}
}

// H 探针 P-D / R3X-005 facet (b) 双侧：同名占位用户组与隧道组同时
// 被替换时，备份外 permission/grant 的双组键都必须重指向继任组。
func TestR3B6ImportGroupPlaceholdersRepointPermissions(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "b6-h-pd.db")
	b6SeedGroupDeps(t, dest, now)
	createRow(t, dest, &model.UserGroup{ID: 99, Name: "grp", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.TunnelGroup{ID: 98, Name: "tg", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.GroupPermission{ID: 50, UserGroupID: 99, TunnelGroupID: 98, CreatedTime: now})
	createRow(t, dest, &model.UserTunnel{ID: 20, UserID: 2, TunnelID: 1, Status: 1})
	createRow(t, dest, &model.GroupPermissionGrant{ID: 60, UserGroupID: 99, TunnelGroupID: 98, UserTunnelID: 20, CreatedByGroup: 1, CreatedTime: now})

	backup := &model.BackupData{
		Version: "1.0",
		UserGroups: []model.UserGroupBackup{
			{ID: 4, Name: "grp", Status: 1, CreatedTime: now, Users: []int64{2}},
		},
		TunnelGroups: []model.TunnelGroupBackup{
			{ID: 3, Name: "tg", Status: 1, CreatedTime: now, Tunnels: []int64{1}},
		},
	}
	result, err := dest.Import(backup, []string{"userGroups", "tunnelGroups"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	var perm model.GroupPermission
	if err := dest.DB().Where("id = ?", 50).First(&perm).Error; err != nil {
		t.Fatalf("permission row must survive via repoint: %v", err)
	}
	if perm.UserGroupID != 4 || perm.TunnelGroupID != 3 {
		t.Fatalf("H P-D: permission group keys = (%d,%d), want repointed to successors (4,3)", perm.UserGroupID, perm.TunnelGroupID)
	}
	var grant model.GroupPermissionGrant
	if err := dest.DB().Where("id = ?", 60).First(&grant).Error; err != nil {
		t.Fatalf("grant row must survive via repoint: %v", err)
	}
	if grant.UserGroupID != 4 || grant.TunnelGroupID != 3 {
		t.Fatalf("H P-D: grant group keys = (%d,%d), want repointed to successors (4,3)", grant.UserGroupID, grant.TunnelGroupID)
	}
	if n := countWhere(t, dest, "user_tunnel", "id = ?", 20); n != 1 {
		t.Fatalf("group-created UT must survive repoint, got %d", n)
	}
	if len(result.ImportWarnings) == 0 {
		t.Fatalf("H P-D: repointing must be counted in ImportWarnings, got %v", result.ImportWarnings)
	}
}

// H 探针 P-C / R3X-023：relay 占位删除前，备份外 user_inbound 的
// relay_id 必须重指向继任 relay 并计 ImportWarnings（红：悬空
// relay_id=99、warnings 为空）。
func TestR3B6ImportRelayPlaceholderRepointsUserInbound(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "b6-h-pc.db")
	createRow(t, dest, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "10000-10100",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	createRow(t, dest, &model.User{ID: 7, User: "u7", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, dest, &model.Tunnel{ID: 1, Name: "t1", TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.Forward{ID: 20, UserID: 7, UserName: "u7", Name: "f1", TunnelID: 1, RemoteAddr: "1.2.3.4:80", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.NodeInbound{ID: 10, KernelID: 0, NodeID: 1, Name: "in", Protocol: "vless", Listen: "::", Port: 443, Enabled: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.NodeInboundRelay{ID: 99, InboundID: 10, ForwardID: 20, Name: "old", Managed: 1, CreatedTime: now})
	createRow(t, dest, &model.UserInbound{ID: 30, UserID: 7, InboundID: 10, RelayID: 99})

	backup := &model.BackupData{
		Version: "1.0",
		Inbounds: &model.InboundsBackup{
			Relays: []model.InboundRelayBackup{
				{ID: 5, InboundID: 10, ForwardID: 20, Name: "new", Managed: 1, CreatedTime: now},
			},
		},
	}
	result, err := dest.Import(backup, []string{"inbounds"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if n := countWhere(t, dest, "node_inbound_relay", "id = ?", 99); n != 0 {
		t.Fatalf("placeholder relay 99 must be replaced, got %d", n)
	}
	var ui model.UserInbound
	if err := dest.DB().Where("id = ?", 30).First(&ui).Error; err != nil {
		t.Fatalf("user_inbound row: %v", err)
	}
	if ui.RelayID != 5 {
		t.Fatalf("R3X-023: user_inbound relay_id = %d, want repointed to successor relay 5", ui.RelayID)
	}
	if !b8WarningsContain(result.ImportWarnings, "中转") {
		t.Fatalf("R3X-023: repointing must be counted in ImportWarnings, got %v", result.ImportWarnings)
	}
}

// R3X-023 防过修对照：备份 relay 与目标 relay 同 id 时不得产生
// 重指向/警告，分配保持原样。
func TestR3B6ImportSameIDRelayNoRepoint(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "b6-relay-sameid.db")
	createRow(t, dest, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "10000-10100",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	createRow(t, dest, &model.User{ID: 7, User: "u7", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, dest, &model.Tunnel{ID: 1, Name: "t1", TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.Forward{ID: 20, UserID: 7, UserName: "u7", Name: "f1", TunnelID: 1, RemoteAddr: "1.2.3.4:80", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.NodeInbound{ID: 10, KernelID: 0, NodeID: 1, Name: "in", Protocol: "vless", Listen: "::", Port: 443, Enabled: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.NodeInboundRelay{ID: 5, InboundID: 10, ForwardID: 20, Name: "same", Managed: 1, CreatedTime: now})
	createRow(t, dest, &model.UserInbound{ID: 30, UserID: 7, InboundID: 10, RelayID: 5})

	backup := &model.BackupData{
		Version: "1.0",
		Inbounds: &model.InboundsBackup{
			Relays: []model.InboundRelayBackup{
				{ID: 5, InboundID: 10, ForwardID: 20, Name: "same", Managed: 1, CreatedTime: now},
			},
		},
	}
	result, err := dest.Import(backup, []string{"inbounds"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	var ui model.UserInbound
	if err := dest.DB().Where("id = ?", 30).First(&ui).Error; err != nil {
		t.Fatalf("user_inbound row: %v", err)
	}
	if ui.RelayID != 5 {
		t.Fatalf("same-id relay import must not touch relay_id, got %d", ui.RelayID)
	}
	if len(result.ImportWarnings) != 0 {
		t.Fatalf("same-id relay import must produce no repoint warning, got %v", result.ImportWarnings)
	}
}

// H-S01：同 token 占位 share 删除时连带删除其 runtime/resource 行
// （与 DeletePeerShare 级联口径对齐；红：两行悬空残留）。
func TestR3B6ImportPeerSharePlaceholderDeletesRuntime(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "b6-hs01.db")
	createRow(t, dest, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "10000-10100",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	createRow(t, dest, &model.PeerShare{ID: 99, Name: "sh", NodeID: 1, Token: "tokX", IsActive: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.PeerShareRuntime{ShareID: 99, NodeID: 1, ReservationID: "res-1", ResourceKey: "rk-1", CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.PeerShareResource{ShareID: 99, NodeID: 1, Kind: "service", OriginalName: "orig", RuntimeName: "rt", UpdatedTime: now})

	backup := &model.BackupData{
		Version: "1.0",
		PeerShares: []model.PeerShareBackup{
			{ID: 5, Name: "sh", NodeID: 1, Token: "tokX", IsActive: 1, CreatedTime: now},
		},
	}
	result, err := dest.Import(backup, []string{"nodes"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.PeerSharesImported != 1 {
		t.Fatalf("peer share must import, got %d", result.PeerSharesImported)
	}
	if n := countWhere(t, dest, "peer_share", "id = ?", 99); n != 0 {
		t.Fatalf("placeholder share 99 must be replaced, got %d", n)
	}
	if n := countWhere(t, dest, "peer_share_runtime", "share_id = ?", 99); n != 0 {
		t.Fatalf("H-S01: runtime rows of the placeholder share must be deleted with it, got %d", n)
	}
	if n := countWhere(t, dest, "peer_share_resource", "share_id = ?", 99); n != 0 {
		t.Fatalf("H-S01: resource rows of the placeholder share must be deleted with it, got %d", n)
	}
}

// 防过修对照（H 探针 P-A 口径）：空库全量恢复往返零警告零跳行，
// 终态与 1.3.6 一致——组/权限/授权/relay 分配全部原样落位、无撤销。
func TestR3B6FullRestoreRoundTripClean(t *testing.T) {
	now := time.Now().UnixMilli()
	source := openBackupRepo(t, "b6-pa-src.db")
	pwd := "$2a$10$abcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ012"
	createRow(t, source, &model.User{ID: 2, User: "u2", Pwd: pwd, RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, source, &model.User{ID: 4, User: "u4", Pwd: pwd, RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, source, &model.Tunnel{ID: 1, Name: "t1", TrafficRatio: 2, Status: 1, CreatedTime: now, UpdatedTime: now})
	// 种子直写 0：GORM Create 会被 default:1.0 吞掉意图值 0（H 域
	// P-A 同款造数注记），故 Create 后以 SQL 把倍率钉为 0 再导出。
	if err := source.DB().Exec(`UPDATE tunnel SET traffic_ratio = 0 WHERE id = 1`).Error; err != nil {
		t.Fatalf("pin source tunnel ratio to 0: %v", err)
	}
	createRow(t, source, &model.UserGroup{ID: 1, Name: "g-one", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, source, &model.TunnelGroup{ID: 1, Name: "tg-one", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, source, &model.UserGroupUser{UserGroupID: 1, UserID: 2, CreatedTime: now})
	createRow(t, source, &model.UserGroupUser{UserGroupID: 1, UserID: 4, CreatedTime: now})
	createRow(t, source, &model.TunnelGroupTunnel{TunnelGroupID: 1, TunnelID: 1, CreatedTime: now})
	createRow(t, source, &model.GroupPermission{ID: 50, UserGroupID: 1, TunnelGroupID: 1, CreatedTime: now})
	createRow(t, source, &model.UserTunnel{ID: 10, UserID: 2, TunnelID: 1, Status: 1})
	createRow(t, source, &model.GroupPermissionGrant{ID: 60, UserGroupID: 1, TunnelGroupID: 1, UserTunnelID: 10, CreatedByGroup: 1, CreatedTime: now})
	createRow(t, source, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "10000-10100",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	createRow(t, source, &model.NodeKernel{ID: 1, NodeID: 1, KernelType: "xray", Version: "latest", Status: "ready", Enabled: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, source, &model.NodeInbound{ID: 10, KernelID: 1, NodeID: 1, Name: "in", Protocol: "vless", Listen: "::", Port: 443, Enabled: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, source, &model.Forward{ID: 20, UserID: 2, UserName: "u2", Name: "f1", TunnelID: 1, RemoteAddr: "1.2.3.4:80", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, source, &model.NodeInboundRelay{ID: 5, InboundID: 10, ForwardID: 20, Name: "rel", Managed: 1, CreatedTime: now})
	createRow(t, source, &model.UserInbound{ID: 30, UserID: 2, InboundID: 10, RelayID: 5})

	dest := openBackupRepo(t, "b6-pa-dst.db")
	result := roundTripBackup(t, source, dest)
	if len(result.ImportWarnings) != 0 {
		t.Fatalf("clean full restore must produce zero warnings, got %v", result.ImportWarnings)
	}
	if len(result.SkippedRows) != 0 {
		t.Fatalf("clean full restore must skip zero rows, got %v", result.SkippedRows)
	}
	if result.PermissionsImported != 1 || result.UserGroupsImported != 1 || result.TunnelGroupsImported != 1 {
		t.Fatalf("restore counts changed: permissions=%d userGroups=%d tunnelGroups=%d",
			result.PermissionsImported, result.UserGroupsImported, result.TunnelGroupsImported)
	}
	if n := countWhere(t, dest, "group_permission_grant", "id = ?", 60); n != 1 {
		t.Fatalf("grant must round-trip intact, got %d", n)
	}
	if n := countWhere(t, dest, "user_tunnel", "id = ?", 10); n != 1 {
		t.Fatalf("group-created UT must round-trip intact (no revoke on clean restore), got %d", n)
	}
	if pairs := b6RevokedPairs(t, result); len(pairs) != 0 {
		t.Fatalf("clean full restore must revoke nothing, got %v", pairs)
	}
	var ui model.UserInbound
	if err := dest.DB().Where("id = ?", 30).First(&ui).Error; err != nil {
		t.Fatalf("user_inbound must round-trip: %v", err)
	}
	if ui.RelayID != 5 {
		t.Fatalf("user_inbound relay_id must round-trip as 5, got %d", ui.RelayID)
	}
	var tun model.Tunnel
	if err := dest.DB().Where("id = ?", 1).First(&tun).Error; err != nil {
		t.Fatalf("tunnel must round-trip: %v", err)
	}
	if tun.TrafficRatio != 0 {
		t.Fatalf("tunnel ratio must round-trip as 0, got %v", tun.TrafficRatio)
	}
}

// 防过修对照：备份自带 permissions 段引用备份组 id（空库新组）时
// 照常导入、计数不变、grants 全数落库。
func TestR3B6ImportPermissionsWithBackupGroupIDs(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "b6-perm-backupids.db")
	b6SeedGroupDeps(t, dest, now)
	createRow(t, dest, &model.UserTunnel{ID: 20, UserID: 2, TunnelID: 1, Status: 1})

	backup := &model.BackupData{
		Version: "1.0",
		UserGroups: []model.UserGroupBackup{
			{ID: 4, Name: "ug", Status: 1, CreatedTime: now, Users: []int64{2}},
		},
		TunnelGroups: []model.TunnelGroupBackup{
			{ID: 3, Name: "tg", Status: 1, CreatedTime: now, Tunnels: []int64{1}},
		},
		Permissions: []model.PermissionBackup{
			{ID: 50, UserGroupID: 4, TunnelGroupID: 3, CreatedTime: now, Grants: []model.PermissionGrantBackup{
				{ID: 60, UserGroupID: 4, TunnelGroupID: 3, UserTunnelID: 20, CreatedByGroup: 1, CreatedTime: now},
			}},
		},
	}
	result, err := dest.Import(backup, []string{"userGroups", "tunnelGroups", "permissions"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.UserGroupsImported != 1 || result.TunnelGroupsImported != 1 || result.PermissionsImported != 1 {
		t.Fatalf("import counts changed: userGroups=%d tunnelGroups=%d permissions=%d",
			result.UserGroupsImported, result.TunnelGroupsImported, result.PermissionsImported)
	}
	var grant model.GroupPermissionGrant
	if err := dest.DB().Where("id = ?", 60).First(&grant).Error; err != nil {
		t.Fatalf("grant referencing backup group ids must import as usual: %v", err)
	}
	if grant.UserGroupID != 4 || grant.TunnelGroupID != 3 {
		t.Fatalf("grant group keys = (%d,%d), want (4,3)", grant.UserGroupID, grant.TunnelGroupID)
	}
	if len(result.ImportWarnings) != 0 {
		t.Fatalf("backup-group-id permissions import must produce no warnings, got %v", result.ImportWarnings)
	}
}
