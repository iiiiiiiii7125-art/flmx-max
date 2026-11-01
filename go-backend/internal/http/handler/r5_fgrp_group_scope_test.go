package handler

import (
	"strings"
	"testing"
)

// R5-FGRP 回归：分组＝未来分配的范围限定器。
// 夹具（seedB2Fixture）：U2/U4 ∈ UG1（启用），TG1={T1,T3}，
// 权限对 (1,1) → U2/U4 的可分配范围 = {T1, T3}；T2 在范围外。
// 存量：UT10 U2/T1、UT11 U2/T2（范围外存量）、UT12 U2/T3、UT13 U4/T1。

// 范围外新建分配：显式拒绝、点名隧道、零落行。
func TestFGRP_ScopeRejectsOutOfScopeAssign(t *testing.T) {
	f := seedB2Fixture(t)
	code, body := f.post(t, f.h.userTunnelAssign, map[string]interface{}{"userId": 4, "tunnelId": 2})
	if code == 0 {
		t.Fatalf("out-of-scope assign must be rejected; body=%s", body)
	}
	if !strings.Contains(body, "授权范围") || !strings.Contains(body, "t-two") {
		t.Fatalf("rejection must name the tunnel and the scope; body=%s", body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE user_id = 4 AND tunnel_id = 2`); n != 0 {
		t.Fatalf("rejected assign must not persist, got %d", n)
	}
}

// 范围内新建分配照常成功。
func TestFGRP_ScopeAllowsInScopeAssign(t *testing.T) {
	f := seedB2Fixture(t)
	code, body := f.post(t, f.h.userTunnelAssign, map[string]interface{}{"userId": 4, "tunnelId": 3})
	if code != 0 {
		t.Fatalf("in-scope assign must succeed, code=%d body=%s", code, body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE user_id = 4 AND tunnel_id = 3`); n != 1 {
		t.Fatalf("in-scope assign must persist, got %d", n)
	}
}

// 批量分配含一条范围外 → 整批拒绝、先行范围内条目不落库。
func TestFGRP_BatchAssignAtomicOnScopeViolation(t *testing.T) {
	f := seedB2Fixture(t)
	code, body := f.post(t, f.h.userTunnelBatchAssign, map[string]interface{}{
		"userId":  4,
		"tunnels": []map[string]interface{}{{"tunnelId": 3}, {"tunnelId": 2}},
	})
	if code == 0 {
		t.Fatalf("batch with an out-of-scope entry must be rejected; body=%s", body)
	}
	if !strings.Contains(body, "授权范围") {
		t.Fatalf("batch rejection must state the scope reason; body=%s", body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE user_id = 4`); n != 1 {
		t.Fatalf("batch must roll back fully (only pre-existing UT13 remains), got %d rows for U4", n)
	}
}

// 存量范围外授权（UT11）经 assign 更新照常：范围门只管新建。
func TestFGRP_ExistingOutOfScopeGrantStillUpdatable(t *testing.T) {
	f := seedB2Fixture(t)
	code, body := f.post(t, f.h.userTunnelAssign, map[string]interface{}{"userId": 2, "tunnelId": 2, "flow": 555})
	if code != 0 {
		t.Fatalf("updating an existing out-of-scope grant must succeed, code=%d body=%s", code, body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE user_id = 2 AND tunnel_id = 2 AND flow = 555`); n != 1 {
		t.Fatalf("existing grant must be updated in place, got %d", n)
	}
}

// 无组用户不受约束；停用侧权限对不产生约束（两形态）。
func TestFGRP_UnconstrainedAndDisabledPairUsersAssignFreely(t *testing.T) {
	f := seedB2Fixture(t)
	f.execB7(t,
		`INSERT INTO user(id, user, pwd, role_id, exp_time, flow, in_flow, out_flow, flow_reset_time, num, created_time, updated_time, status) VALUES(5, 'free', 'pwd', 1, 0, 100, 0, 0, 0, 10, 1000, 1000, 1)`,
		`INSERT INTO user(id, user, pwd, role_id, exp_time, flow, in_flow, out_flow, flow_reset_time, num, created_time, updated_time, status) VALUES(6, 'offmember', 'pwd', 1, 0, 100, 0, 0, 0, 10, 1000, 1000, 1)`,
		// UG9 停用、TG9 启用（含 T1）、权限对在位、U6 为成员：
		// 停用组不产生范围，U6 不受约束。
		`INSERT INTO user_group(id, name, created_time, updated_time, status) VALUES(9, 'g-off9', 1000, 1000, 0)`,
		`INSERT INTO tunnel_group(id, name, created_time, updated_time, status) VALUES(9, 'tg-on9', 1000, 1000, 1)`,
		`INSERT INTO user_group_user(user_group_id, user_id, created_time) VALUES(9, 6, 1000)`,
		`INSERT INTO tunnel_group_tunnel(tunnel_group_id, tunnel_id, created_time) VALUES(9, 1, 1000)`,
		`INSERT INTO group_permission(user_group_id, tunnel_group_id, created_time) VALUES(9, 9, 1000)`,
	)
	code, body := f.post(t, f.h.userTunnelAssign, map[string]interface{}{"userId": 5, "tunnelId": 2})
	if code != 0 {
		t.Fatalf("group-less user must assign freely, code=%d body=%s", code, body)
	}
	code, body = f.post(t, f.h.userTunnelAssign, map[string]interface{}{"userId": 6, "tunnelId": 2})
	if code != 0 {
		t.Fatalf("member of a disabled group must be unconstrained, code=%d body=%s", code, body)
	}
}

// 撤权后约束解除；成员移出后约束解除且既有授权保留。
func TestFGRP_ScopeReleasedByRemoveAndUnbind(t *testing.T) {
	f := seedB2Fixture(t)
	permID := f.count(t, `SELECT id FROM group_permission WHERE user_group_id = 1 AND tunnel_group_id = 1`)
	code, body := f.post(t, f.h.groupPermissionRemove, map[string]interface{}{"id": permID})
	if code != 0 {
		t.Fatalf("groupPermissionRemove code=%d body=%s", code, body)
	}
	f.assertGrantsIntactT1(t)
	code, body = f.post(t, f.h.userTunnelAssign, map[string]interface{}{"userId": 4, "tunnelId": 2})
	if code != 0 {
		t.Fatalf("assign after permission remove must be unconstrained, code=%d body=%s", code, body)
	}
}

func TestFGRP_UnbindReleasesScopeAndKeepsGrants(t *testing.T) {
	f := seedB2Fixture(t)
	code, body := f.post(t, f.h.groupUserAssign, map[string]interface{}{"groupId": 1, "userIds": []int64{2}})
	if code != 0 {
		t.Fatalf("groupUserAssign code=%d body=%s", code, body)
	}
	// U4 已移出：UT13 保留，且 U4 不再受范围约束。
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE id = 13`); n != 1 {
		t.Fatalf("R5-FGRP: unbound member's grant must survive, got %d", n)
	}
	code, body = f.post(t, f.h.userTunnelAssign, map[string]interface{}{"userId": 4, "tunnelId": 2})
	if code != 0 {
		t.Fatalf("unbound user must be unconstrained, code=%d body=%s", code, body)
	}
	// U2 仍在组内：对组外新隧道的分配仍被范围门拒绝。
	f.execB7(t, `INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, in_ip, inx) VALUES(7, 't-seven', 1, 1, 'tls', 1, 1000, 1000, 1, NULL, 0)`)
	code, body = f.post(t, f.h.userTunnelAssign, map[string]interface{}{"userId": 2, "tunnelId": 7})
	if code == 0 {
		t.Fatalf("remaining member must stay constrained; body=%s", body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE user_id = 2 AND tunnel_id = 7`); n != 0 {
		t.Fatalf("constrained assign must not persist, got %d", n)
	}
}

// userCreate 带 groupIds：只记成员、零物化授权。
func TestFGRP_UserCreateWithGroupsMaterializesNothing(t *testing.T) {
	f := seedB2Fixture(t)
	code, body := f.post(t, f.h.userCreate, map[string]interface{}{"user": "scoped-newbie", "pwd": "secret123", "groupIds": []int64{1}})
	if code != 0 {
		t.Fatalf("userCreate code=%d body=%s", code, body)
	}
	newID := f.count(t, `SELECT id FROM user WHERE user = 'scoped-newbie'`)
	if newID <= 0 {
		t.Fatalf("created user not found")
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_group_user WHERE user_group_id = 1 AND user_id = ?`, newID); n != 1 {
		t.Fatalf("membership must be recorded, got %d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE user_id = ?`, newID); n != 0 {
		t.Fatalf("R5-FGRP: userCreate with groups must not materialize grants, got %d", n)
	}
	// 新成员受范围约束：范围外分配被拒、范围内成功。
	code, body = f.post(t, f.h.userTunnelAssign, map[string]interface{}{"userId": newID, "tunnelId": 2})
	if code == 0 {
		t.Fatalf("new member out-of-scope assign must be rejected; body=%s", body)
	}
	code, body = f.post(t, f.h.userTunnelAssign, map[string]interface{}{"userId": newID, "tunnelId": 1})
	if code != 0 {
		t.Fatalf("new member in-scope assign must succeed, code=%d body=%s", code, body)
	}
}
