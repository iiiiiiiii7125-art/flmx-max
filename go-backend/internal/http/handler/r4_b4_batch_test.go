package handler

import (
	"testing"
)

// R4 批 4 回归（handler 面）：原 R4X-005 测「双权限对共享同一 UT
// 时停用顺序与 cb 来源标志提升决定授权生死」。R5-FGRP 后停用组
// 只改状态——不删台账、不提升 cb、不撤销授权，顺序不再有任何
// 影响；本文件改测该不变量（两序对照）。
//
// 夹具（seedB2Fixture 之上）：UG2（成员 U2）与 TG1 组成第二权限对
// (2,1)，UT10（U2/T1）另有一条登记 grant（cb=0）。

func seedB4DualGroup(t *testing.T, f *b2Fixture) {
	t.Helper()
	f.execB7(t,
		`INSERT INTO user_group(id, name, created_time, updated_time, status) VALUES(2, 'g-two', 1000, 1000, 1)`,
		`INSERT INTO user_group_user(user_group_id, user_id, created_time) VALUES(2, 2, 1000)`,
		`INSERT INTO group_permission(user_group_id, tunnel_group_id, created_time) VALUES(2, 1, 1000)`,
		`INSERT INTO group_permission_grant(user_group_id, tunnel_group_id, user_tunnel_id, created_by_group, created_time) VALUES(2, 1, 10, 0, 1000)`,
	)
}

// assertB4DualDisabledIntact 断言两组皆停用后：UT10、两条 grant
// 台账（cb 逐字不变）、forward 20 与手工面（UT11/UT12/forward 21）
// 全部存续，隧道 1 权限照常可用。
func assertB4DualDisabledIntact(t *testing.T, f *b2Fixture) {
	t.Helper()
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE id = 10`); n != 1 {
		t.Fatalf("R5-FGRP: UT10 must survive dual-group disable, got %d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM group_permission_grant WHERE user_tunnel_id = 10`); n != 2 {
		t.Fatalf("R5-FGRP: both grant ledger rows must survive dual-group disable, got %d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM group_permission_grant WHERE user_group_id = 1 AND user_tunnel_id = 10 AND created_by_group = 1`); n != 1 {
		t.Fatalf("R5-FGRP: UG1 grant cb must stay 1 (no promotion/demotion on disable), got %d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM group_permission_grant WHERE user_group_id = 2 AND user_tunnel_id = 10 AND created_by_group = 0`); n != 1 {
		t.Fatalf("R5-FGRP: UG2 grant cb must stay 0 (no promotion on disable), got %d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM forward WHERE id = 20`); n != 1 {
		t.Fatalf("R5-FGRP: forward on UT10 must survive dual-group disable")
	}
	if err := f.h.ensureTunnelPermission(2, 1, 1); err != nil {
		t.Fatalf("R5-FGRP: UT10 permission must stay usable after both groups disabled: %v", err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE id IN (11, 12)`); n != 2 {
		t.Fatalf("manual user_tunnels must survive dual-group disable, got %d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM forward WHERE id = 21`); n != 1 {
		t.Fatalf("forward on manual tunnel must survive")
	}
}

func TestR4B4_005_DualGroupDisableCreatorFirst(t *testing.T) {
	f := seedB2Fixture(t)
	seedB4DualGroup(t, f)

	code, body := f.post(t, f.h.groupUserUpdate, map[string]interface{}{"id": 1, "name": "g-one", "status": 0})
	if code != 0 {
		t.Fatalf("disable UG1 code=%d body=%s", code, body)
	}
	assertB4DualDisabledIntact(t, f)

	code, body = f.post(t, f.h.groupUserUpdate, map[string]interface{}{"id": 2, "name": "g-two", "status": 0})
	if code != 0 {
		t.Fatalf("disable UG2 code=%d body=%s", code, body)
	}
	assertB4DualDisabledIntact(t, f)
}

func TestR4B4_005_DualGroupDisableRegistrarFirst(t *testing.T) {
	f := seedB2Fixture(t)
	seedB4DualGroup(t, f)

	code, body := f.post(t, f.h.groupUserUpdate, map[string]interface{}{"id": 2, "name": "g-two", "status": 0})
	if code != 0 {
		t.Fatalf("disable UG2 code=%d body=%s", code, body)
	}
	assertB4DualDisabledIntact(t, f)

	code, body = f.post(t, f.h.groupUserUpdate, map[string]interface{}{"id": 1, "name": "g-one", "status": 0})
	if code != 0 {
		t.Fatalf("disable UG1 code=%d body=%s", code, body)
	}
	assertB4DualDisabledIntact(t, f)
}
