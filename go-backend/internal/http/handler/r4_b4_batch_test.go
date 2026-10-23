package handler

import (
	"testing"
)

// R4 批 4 回归（handler 面）：R4X-005（R4-G-01）双权限对共享同一 UT
// 时撤销顺序决定授权生死——creator 侧先撤销后来源标志随 grant 行
// 删除而丢失，最后一组撤销时 UT 不再入候选、永久残留。
//
// 夹具（seedB2Fixture 之上）：UG2（成员 U2）与 TG1 组成第二权限对
// (2,1)，UT10（U2/T1）另有一条登记 grant（cb=0）——即「先展开对
// 建 UT（cb=1）、后展开对仅登记（cb=0）」的真实落库形态。

func seedB4DualGroup(t *testing.T, f *b2Fixture) {
	t.Helper()
	f.execB7(t,
		`INSERT INTO user_group(id, name, created_time, updated_time, status) VALUES(2, 'g-two', 1000, 1000, 1)`,
		`INSERT INTO user_group_user(user_group_id, user_id, created_time) VALUES(2, 2, 1000)`,
		`INSERT INTO group_permission(user_group_id, tunnel_group_id, created_time) VALUES(2, 1, 1000)`,
		`INSERT INTO group_permission_grant(user_group_id, tunnel_group_id, user_tunnel_id, created_by_group, created_time) VALUES(2, 1, 10, 0, 1000)`,
	)
}

func b4GrantCB(t *testing.T, f *b2Fixture, userGroupID int64) int {
	t.Helper()
	return f.count(t, `SELECT COALESCE(MAX(created_by_group), -1) FROM group_permission_grant WHERE user_group_id = ? AND user_tunnel_id = 10`, userGroupID)
}

// 正序（creator 先停用）：停用 UG1 后 UT10 靠 UG2 台账存活（正确），
// 但 cb=1 来源标志必须提升到 UG2 的剩余 grant 行；再停用 UG2 时
// UT10/grant/forward 必须全清（红：UT10=1 grants=0 永久残留）。
func TestR4B4_005_DualGroupDisableCreatorFirst(t *testing.T) {
	f := seedB2Fixture(t)
	seedB4DualGroup(t, f)

	code, body := f.post(t, f.h.groupUserUpdate, map[string]interface{}{"id": 1, "name": "g-one", "status": 0})
	if code != 0 {
		t.Fatalf("disable UG1 code=%d body=%s", code, body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE id = 10`); n != 1 {
		t.Fatalf("UT10 must survive while UG2 still grants it, got %d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM group_permission_grant WHERE user_tunnel_id = 10`); n != 1 {
		t.Fatalf("exactly one grant (UG2) must remain after UG1 disable, got %d", n)
	}
	if cb := b4GrantCB(t, f, 2); cb != 1 {
		t.Fatalf("R4X-005: creator flag must be promoted to the remaining UG2 grant, created_by_group=%d", cb)
	}

	code, body = f.post(t, f.h.groupUserUpdate, map[string]interface{}{"id": 2, "name": "g-two", "status": 0})
	if code != 0 {
		t.Fatalf("disable UG2 code=%d body=%s", code, body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE id = 10`); n != 0 {
		t.Fatalf("R4X-005: UT10 must be revoked after both granting groups are disabled, got %d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM group_permission_grant WHERE user_tunnel_id = 10`); n != 0 {
		t.Fatalf("grant ledger for UT10 must be empty after both disables, got %d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM forward WHERE id = 20`); n != 0 {
		t.Fatalf("forward on revoked UT10 must be cleaned")
	}
	if err := f.h.ensureTunnelPermission(2, 1, 1); err == nil {
		t.Fatalf("ensureTunnelPermission must reject UT10 after both groups disabled")
	}
	// 手工面不受影响：UT11（纯手工）/UT12（手工在先、分组仅登记，
	// 其 (1,1) grant 为 cb=0——不得被误提升/误删）。
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE id IN (11, 12)`); n != 2 {
		t.Fatalf("manual user_tunnels must survive dual-group disable, got %d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM forward WHERE id = 21`); n != 1 {
		t.Fatalf("forward on manual tunnel must survive")
	}
}

// 逆序（registrar 先停用）对照：UG2 的 cb=0 grant 先删、UG1 的
// cb=1 grant 仍在，停用 UG1 时 UT10 本就入候选全清（修复前亦然，
// 防回退对照）。
func TestR4B4_005_DualGroupDisableRegistrarFirst(t *testing.T) {
	f := seedB2Fixture(t)
	seedB4DualGroup(t, f)

	code, body := f.post(t, f.h.groupUserUpdate, map[string]interface{}{"id": 2, "name": "g-two", "status": 0})
	if code != 0 {
		t.Fatalf("disable UG2 code=%d body=%s", code, body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE id = 10`); n != 1 {
		t.Fatalf("UT10 must survive while UG1 still grants it, got %d", n)
	}
	if cb := b4GrantCB(t, f, 1); cb != 1 {
		t.Fatalf("UG1 grant must keep its creator flag, created_by_group=%d", cb)
	}

	code, body = f.post(t, f.h.groupUserUpdate, map[string]interface{}{"id": 1, "name": "g-one", "status": 0})
	if code != 0 {
		t.Fatalf("disable UG1 code=%d body=%s", code, body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE id = 10`); n != 0 {
		t.Fatalf("UT10 must be revoked after both groups disabled (reverse order), got %d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM group_permission_grant WHERE user_tunnel_id = 10`); n != 0 {
		t.Fatalf("grant ledger for UT10 must be empty, got %d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM forward WHERE id = 20`); n != 0 {
		t.Fatalf("forward on revoked UT10 must be cleaned")
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE id IN (11, 12)`); n != 2 {
		t.Fatalf("manual user_tunnels must survive, got %d", n)
	}
}
