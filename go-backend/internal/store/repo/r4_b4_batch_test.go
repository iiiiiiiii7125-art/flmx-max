package repo

import (
	"testing"
	"time"

	"go-backend/internal/store/model"

	"gorm.io/gorm"
)

// R4 批 4 回归（repo 面）：R4X-005 候选逻辑来源标志提升
// （共享 helper + Pair 自有实现），及防误提升（剩余 grant 的成员
// 对应关系不成立时不得提升）。R4X-006 / G-S02 用例见
// backup_r4_b4_test.go。

// ─── R4X-005 Pair 自有实现 ──────────────────────────────────────────────

func b4SeedDualPair(t *testing.T, r *Repository, now int64, secondPairMemberOK bool) {
	t.Helper()
	createRow(t, r, &model.User{ID: 2, User: "u2", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, r, &model.Tunnel{ID: 1, Name: "t1", TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.UserGroup{ID: 1, Name: "g-one", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.UserGroup{ID: 2, Name: "g-two", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.TunnelGroup{ID: 1, Name: "tg-one", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.UserGroupUser{UserGroupID: 1, UserID: 2, CreatedTime: now})
	if secondPairMemberOK {
		createRow(t, r, &model.UserGroupUser{UserGroupID: 2, UserID: 2, CreatedTime: now})
	}
	createRow(t, r, &model.TunnelGroupTunnel{TunnelGroupID: 1, TunnelID: 1, CreatedTime: now})
	createRow(t, r, &model.GroupPermission{ID: 50, UserGroupID: 1, TunnelGroupID: 1, CreatedTime: now})
	createRow(t, r, &model.GroupPermission{ID: 51, UserGroupID: 2, TunnelGroupID: 1, CreatedTime: now})
	createRow(t, r, &model.UserTunnel{ID: 10, UserID: 2, TunnelID: 1, Status: 1})
	createRow(t, r, &model.GroupPermissionGrant{ID: 60, UserGroupID: 1, TunnelGroupID: 1, UserTunnelID: 10, CreatedByGroup: 1, CreatedTime: now})
	createRow(t, r, &model.GroupPermissionGrant{ID: 61, UserGroupID: 2, TunnelGroupID: 1, UserTunnelID: 10, CreatedByGroup: 0, CreatedTime: now})
}

func b4RevokePair(t *testing.T, r *Repository, ug, tg int64) []RevokedUserTunnelPair {
	t.Helper()
	var pairs []RevokedUserTunnelPair
	err := r.DB().Transaction(func(tx *gorm.DB) error {
		var err error
		pairs, err = r.RevokeGroupPermissionPairTx(tx, ug, tg)
		return err
	})
	if err != nil {
		t.Fatalf("revoke pair (%d,%d): %v", ug, tg, err)
	}
	return pairs
}

func b4GrantCB(t *testing.T, r *Repository, id int64) int {
	t.Helper()
	var g model.GroupPermissionGrant
	if err := r.DB().Where("id = ?", id).First(&g).Error; err != nil {
		t.Fatalf("grant %d: %v", id, err)
	}
	return g.CreatedByGroup
}

// Pair 路径同型：先撤 creator 对 → 剩余 grant 提升 cb=1；再撤
// registrar 对 → UT 删除（红：Pair 内联实现无提升、UT 残留）。
func TestR4B4_005_PairRevokePromotesCreatorFlag(t *testing.T) {
	now := time.Now().UnixMilli()
	r := openBackupRepo(t, "b4-pair-promote.db")
	b4SeedDualPair(t, r, now, true)

	pairs := b4RevokePair(t, r, 1, 1)
	if len(pairs) != 0 {
		t.Fatalf("UT10 must survive first pair revoke (second grant remains), got revoked %v", pairs)
	}
	if cb := b4GrantCB(t, r, 61); cb != 1 {
		t.Fatalf("R4X-005 (Pair): remaining grant must be promoted to created_by_group=1, got %d", cb)
	}
	pairs = b4RevokePair(t, r, 2, 1)
	if len(pairs) != 1 || pairs[0].UserID != 2 || pairs[0].TunnelID != 1 {
		t.Fatalf("second pair revoke must revoke UT10, got %v", pairs)
	}
	if n := countWhere(t, r, "user_tunnel", "id = ?", 10); n != 0 {
		t.Fatalf("UT10 must be deleted after last pair revoke, got %d", n)
	}
}

// 防误提升：剩余 grant 的权限对成员对应关系不成立（U2 不在 UG2）
// 时，creator 行删除后不得把该行提升为组建——随后撤销该对时 UT
// 必须存活（按手工/来源不可考行处置）。
func TestR4B4_005_NoPromotionWhenMembershipMismatch(t *testing.T) {
	now := time.Now().UnixMilli()
	r := openBackupRepo(t, "b4-pair-nopromote.db")
	b4SeedDualPair(t, r, now, false)

	if _, err := r.RevokeGroupGrantsByGroup("user_group", 1); err != nil {
		t.Fatalf("revoke group 1: %v", err)
	}
	if n := countWhere(t, r, "user_tunnel", "id = ?", 10); n != 1 {
		t.Fatalf("UT10 must survive while a grant remains, got %d", n)
	}
	if cb := b4GrantCB(t, r, 61); cb != 0 {
		t.Fatalf("R4X-005 anti-promotion: grant whose pair membership does not hold must NOT be promoted, got created_by_group=%d", cb)
	}
	if _, err := r.RevokeGroupGrantsByGroup("user_group", 2); err != nil {
		t.Fatalf("revoke group 2: %v", err)
	}
	if n := countWhere(t, r, "user_tunnel", "id = ?", 10); n != 1 {
		t.Fatalf("UT10 with no legitimately promoted grant must survive last revoke, got %d", n)
	}
}

