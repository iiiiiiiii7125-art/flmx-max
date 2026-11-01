package handler

import (
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R3 批 5 回归：分组授权生命周期。
//   - R3X-004（R5-FGRP 改版）：存量停用分组的组建授权不再追溯
//     撤销——groupUpdate 0→0、成员/隧道替换等任何组操作后逐行
//     保留；手工授权同样不动、启用组不受影响。
//   - R3X-020：groupUpdate presence 语义——未传 name/status 保持现值。
//   - R3X-022：setUserInbounds 回执补 skippedRelays 计数，跳过语义与
//     整体成败不变。
// 复用 seedB2Fixture（G1/TG1 启用、UT10/13 组建、UT11/12 手工、
// forward20/21）与 b2Fixture 的 post/postB7Admin/execB7 帮手。

// seedB5Stock 造「存量停用」形态（模拟升级前已形成）：
//   - UG2（status=userStatus，成员 U4）↔ TG2（status=tunnelStatus，含 T2）
//     有 group_permission 对；
//   - UT50：U4/T2 组建授权（grant created_by_group=1）+ forward50；
//   - UT51：U4/T3 纯手工授权（无 grant 行）+ forward51，必须不受影响。
func seedB5Stock(t *testing.T, f *b2Fixture, userStatus, tunnelStatus int) {
	t.Helper()
	f.execB7(t,
		`INSERT INTO user_group(id, name, created_time, updated_time, status) VALUES(2, 'g-off', 1000, 1000, `+b5StatusLiteral(userStatus)+`)`,
		`INSERT INTO tunnel_group(id, name, created_time, updated_time, status) VALUES(2, 'tg-two', 1000, 1000, `+b5StatusLiteral(tunnelStatus)+`)`,
		`INSERT INTO user_group_user(user_group_id, user_id, created_time) VALUES(2, 4, 1000)`,
		`INSERT INTO tunnel_group_tunnel(tunnel_group_id, tunnel_id, created_time) VALUES(2, 2, 1000)`,
		`INSERT INTO group_permission(user_group_id, tunnel_group_id, created_time) VALUES(2, 2, 1000)`,
		`INSERT INTO user_tunnel(id, user_id, tunnel_id, num, flow, in_flow, out_flow, flow_reset_time, exp_time, status) VALUES(50, 4, 2, 10, 100, 0, 0, 0, 0, 1)`,
		`INSERT INTO group_permission_grant(user_group_id, tunnel_group_id, user_tunnel_id, created_by_group, created_time) VALUES(2, 2, 50, 1, 1000)`,
		`INSERT INTO user_tunnel(id, user_id, tunnel_id, num, flow, in_flow, out_flow, flow_reset_time, exp_time, status) VALUES(51, 4, 3, 10, 100, 0, 0, 0, 0, 1)`,
		`INSERT INTO forward(id, user_id, user_name, name, tunnel_id, remote_addr, strategy, in_flow, out_flow, created_time, updated_time, status, inx) VALUES(50, 4, 'member2', 'f-stock', 2, '10.0.0.2:80', 'fifo', 0, 0, 1000, 1000, 1, 0)`,
		`INSERT INTO forward(id, user_id, user_name, name, tunnel_id, remote_addr, strategy, in_flow, out_flow, created_time, updated_time, status, inx) VALUES(51, 4, 'member2', 'f-manual', 3, '10.0.0.3:80', 'fifo', 0, 0, 1000, 1000, 1, 0)`,
	)
}

func b5StatusLiteral(v int) string {
	if v == 0 {
		return "0"
	}
	return "1"
}

// assertB5StockIntact 断言 R5-FGRP 语义：存量组建授权（UT50/台账/
// forward50）与手工授权（UT51/forward51）在任何组操作后逐行保留、
// 权限照常可用，启用组（G1）的授权无误伤。
func assertB5StockIntact(t *testing.T, f *b2Fixture) {
	t.Helper()
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE id = 50`); n != 1 {
		t.Fatalf("R5-FGRP: stock group-created user_tunnel 50 must survive, got %d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM group_permission_grant WHERE user_tunnel_id = 50`); n != 1 {
		t.Fatalf("R5-FGRP: grant ledger for UT50 must be retained, got %d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM forward WHERE id = 50`); n != 1 {
		t.Fatalf("R5-FGRP: forward on stock tunnel must survive")
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE id = 51`); n != 1 {
		t.Fatalf("manual user_tunnel 51 must survive, got %d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM forward WHERE id = 51`); n != 1 {
		t.Fatalf("forward on manual tunnel must survive")
	}
	if err := f.h.ensureTunnelPermission(4, 1, 2); err != nil {
		t.Fatalf("R5-FGRP: stock tunnel permission must stay usable: %v", err)
	}
	if err := f.h.ensureTunnelPermission(4, 1, 3); err != nil {
		t.Fatalf("manual tunnel permission must stay usable: %v", err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE id = 10`); n != 1 {
		t.Fatalf("enabled group G1 grant UT10 must be untouched, got %d", n)
	}
}

func b5GroupNameStatus(t *testing.T, f *b2Fixture, table string, id int64) (string, int) {
	t.Helper()
	var name string
	var status int
	q := `SELECT name, status FROM ` + table + ` WHERE id = ?`
	if err := f.r.DB().Raw(q, id).Row().Scan(&name, &status); err != nil {
		t.Fatalf("read group row: %v", err)
	}
	return name, status
}

// ─── R3X-004（R5-FGRP 改版）：组操作不追溯撤销存量授权 ──────────────

// 旧 sync 触点已随组语义改版删除；现存触点（groupUpdate 0→0、
// 成员/隧道替换）对存量停用分组的组建授权一律不动。
func TestR3B5_004_StockDisabledGrantsSurviveGroupOps(t *testing.T) {
	t.Run("user group rename while disabled", func(t *testing.T) {
		f := seedB2Fixture(t)
		seedB5Stock(t, f, 0, 1)
		code, body := f.post(t, f.h.groupUserUpdate, map[string]interface{}{"id": 2, "name": "g-off-renamed", "status": 0})
		if code != 0 {
			t.Fatalf("groupUserUpdate 0->0 code=%d body=%s", code, body)
		}
		assertB5StockIntact(t, f)
		name, status := b5GroupNameStatus(t, f, "user_group", 2)
		if name != "g-off-renamed" || status != 0 {
			t.Fatalf("group row must be renamed and stay disabled, got name=%q status=%d", name, status)
		}
	})
	t.Run("tunnel group rename while disabled", func(t *testing.T) {
		f := seedB2Fixture(t)
		seedB5Stock(t, f, 1, 0)
		code, body := f.post(t, f.h.groupTunnelUpdate, map[string]interface{}{"id": 2, "name": "tg-two-renamed", "status": 0})
		if code != 0 {
			t.Fatalf("groupTunnelUpdate 0->0 code=%d body=%s", code, body)
		}
		assertB5StockIntact(t, f)
		name, status := b5GroupNameStatus(t, f, "tunnel_group", 2)
		if name != "tg-two-renamed" || status != 0 {
			t.Fatalf("group row must be renamed and stay disabled, got name=%q status=%d", name, status)
		}
	})
	t.Run("both sides disabled, member removed", func(t *testing.T) {
		f := seedB2Fixture(t)
		seedB5Stock(t, f, 0, 0)
		code, body := f.post(t, f.h.groupUserAssign, map[string]interface{}{"groupId": 2, "userIds": []int64{}})
		if code != 0 {
			t.Fatalf("groupUserAssign code=%d body=%s", code, body)
		}
		assertB5StockIntact(t, f)
	})
	t.Run("both sides disabled, tunnel removed", func(t *testing.T) {
		f := seedB2Fixture(t)
		seedB5Stock(t, f, 0, 0)
		code, body := f.post(t, f.h.groupTunnelAssign, map[string]interface{}{"groupId": 2, "tunnelIds": []int64{}})
		if code != 0 {
			t.Fatalf("groupTunnelAssign code=%d body=%s", code, body)
		}
		assertB5StockIntact(t, f)
	})
}

// 防过修：启用组成员原样替换（无实际变动）不得增删任何授权/台账。
func TestR3B5_004_EnabledGroupOpsUnaffected(t *testing.T) {
	f := seedB2Fixture(t)
	code, body := f.post(t, f.h.groupUserAssign, map[string]interface{}{"groupId": 1, "userIds": []int64{2, 4}})
	if code != 0 {
		t.Fatalf("groupUserAssign code=%d body=%s", code, body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE id IN (10, 13)`); n != 2 {
		t.Fatalf("enabled group grants must survive member replace, got %d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM group_permission_grant WHERE user_group_id = 1 AND user_tunnel_id IN (10, 13)`); n != 2 {
		t.Fatalf("enabled group ledger must survive member replace, got %d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM forward WHERE id = 20`); n != 1 {
		t.Fatalf("forward on enabled group tunnel must survive")
	}
	if err := f.h.ensureTunnelPermission(2, 1, 1); err != nil {
		t.Fatalf("enabled group permission must stay usable: %v", err)
	}
}

// ─── R3X-020 presence ──────────────────────────────────────────────────────

// seedB5DisabledNoStock 造停用组（有 permission 对、无任何 UT/grant 存量）。
func seedB5DisabledNoStock(t *testing.T, f *b2Fixture) {
	t.Helper()
	f.execB7(t,
		`INSERT INTO user_group(id, name, created_time, updated_time, status) VALUES(2, 'g-off', 1000, 1000, 0)`,
		`INSERT INTO tunnel_group(id, name, created_time, updated_time, status) VALUES(2, 'tg-two', 1000, 1000, 1)`,
		`INSERT INTO user_group_user(user_group_id, user_id, created_time) VALUES(2, 4, 1000)`,
		`INSERT INTO tunnel_group_tunnel(tunnel_group_id, tunnel_id, created_time) VALUES(2, 2, 1000)`,
		`INSERT INTO group_permission(user_group_id, tunnel_group_id, created_time) VALUES(2, 2, 1000)`,
	)
}

func TestR3B5_020_GroupUpdatePresence(t *testing.T) {
	t.Run("only id on disabled user group keeps name and status, grants nothing", func(t *testing.T) {
		f := seedB2Fixture(t)
		seedB5DisabledNoStock(t, f)
		code, body := f.post(t, f.h.groupUserUpdate, map[string]interface{}{"id": 2})
		if code != 0 {
			t.Fatalf("groupUserUpdate {id} code=%d body=%s", code, body)
		}
		name, status := b5GroupNameStatus(t, f, "user_group", 2)
		if name != "g-off" || status != 0 {
			t.Fatalf("absent name/status must keep current values, got name=%q status=%d", name, status)
		}
		if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE user_id = 4 AND tunnel_id = 2`); n != 0 {
			t.Fatalf("disabled group must not gain grants on presence update, got %d", n)
		}
		if n := f.count(t, `SELECT COUNT(*) FROM group_permission_grant WHERE user_group_id = 2`); n != 0 {
			t.Fatalf("disabled group must not gain ledger rows, got %d", n)
		}
	})
	t.Run("only name on disabled user group keeps status, grants nothing", func(t *testing.T) {
		f := seedB2Fixture(t)
		seedB5DisabledNoStock(t, f)
		code, body := f.post(t, f.h.groupUserUpdate, map[string]interface{}{"id": 2, "name": "still-off"})
		if code != 0 {
			t.Fatalf("groupUserUpdate {name} code=%d body=%s", code, body)
		}
		name, status := b5GroupNameStatus(t, f, "user_group", 2)
		if name != "still-off" || status != 0 {
			t.Fatalf("rename must apply while status stays disabled, got name=%q status=%d", name, status)
		}
		if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE user_id = 4 AND tunnel_id = 2`); n != 0 {
			t.Fatalf("renaming a disabled group must not materialize grants, got %d", n)
		}
	})
	t.Run("only name on enabled user group keeps status and grants", func(t *testing.T) {
		f := seedB2Fixture(t)
		code, body := f.post(t, f.h.groupUserUpdate, map[string]interface{}{"id": 1, "name": "g-one-renamed"})
		if code != 0 {
			t.Fatalf("groupUserUpdate {name} code=%d body=%s", code, body)
		}
		name, status := b5GroupNameStatus(t, f, "user_group", 1)
		if name != "g-one-renamed" || status != 1 {
			t.Fatalf("rename must apply while status stays enabled, got name=%q status=%d", name, status)
		}
		if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE id = 10`); n != 1 {
			t.Fatalf("enabled group grant must survive presence update, got %d", n)
		}
		if n := f.count(t, `SELECT COUNT(*) FROM group_permission_grant WHERE user_group_id = 1`); n != 3 {
			t.Fatalf("enabled group ledger must be unchanged, got %d", n)
		}
	})
	t.Run("only id on enabled tunnel group keeps name and status", func(t *testing.T) {
		f := seedB2Fixture(t)
		code, body := f.post(t, f.h.groupTunnelUpdate, map[string]interface{}{"id": 1})
		if code != 0 {
			t.Fatalf("groupTunnelUpdate {id} code=%d body=%s", code, body)
		}
		name, status := b5GroupNameStatus(t, f, "tunnel_group", 1)
		if name != "tg-one" || status != 1 {
			t.Fatalf("absent name/status must keep current values, got name=%q status=%d", name, status)
		}
	})
}

// ─── R3X-022 skippedRelays 回执 ────────────────────────────────────────────

func seedB5Inbound(t *testing.T, f *b2Fixture) int64 {
	t.Helper()
	now := time.Now().UnixMilli()
	f.execB7(t, `INSERT INTO node(name, secret, server_ip, port, created_time, status) VALUES('n-b5', 's', '203.0.113.10', '1000-2000', 1000, 1)`)
	nodeID := mustLastInsertID(t, f.r, "node")
	if err := f.r.DB().Exec(`INSERT INTO node_kernel(node_id, kernel_type, version, status, created_time, updated_time) VALUES(?, 'xray', 'latest', 'pending', ?, ?)`, nodeID, now, now).Error; err != nil {
		t.Fatalf("seed kernel: %v", err)
	}
	kernelID := mustLastInsertID(t, f.r, "kernel")
	if err := f.r.DB().Exec(`INSERT INTO node_inbound(kernel_id, node_id, name, protocol, listen, port, settings, enabled, created_time, updated_time) VALUES(?, ?, 'ib-b5', 'trojan', '::', 443, '{"password":"x"}', 1, ?, ?)`, kernelID, nodeID, now, now).Error; err != nil {
		t.Fatalf("seed inbound: %v", err)
	}
	return mustLastInsertID(t, f.r, "inbound")
}

func TestR3B5_022_SetUserInboundsPhantomRelayReceipt(t *testing.T) {
	f := seedB2Fixture(t)
	inboundID := seedB5Inbound(t, f)

	// 幻影 relay：整体成功（code=0）、不落行、回执含 skippedRelays=1。
	code, body := f.postB7Admin(t, f.h.setUserInbounds, map[string]interface{}{
		"userId":      2,
		"assignments": []map[string]interface{}{{"inboundId": inboundID, "relayId": 99999}},
	})
	if code != 0 {
		t.Fatalf("phantom relay must keep overall success, code=%d body=%s", code, body)
	}
	if !strings.Contains(body, `"skippedRelays":1`) {
		t.Fatalf("receipt must carry skippedRelays=1; body=%s", body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_inbound WHERE user_id = 2`); n != 0 {
		t.Fatalf("phantom relay must not persist rows, got %d", n)
	}

	// 混合：直连落行 + 幻影 relay 跳过计数。
	code, body = f.postB7Admin(t, f.h.setUserInbounds, map[string]interface{}{
		"userId": 2,
		"assignments": []map[string]interface{}{
			{"inboundId": inboundID, "relayId": 0},
			{"inboundId": inboundID, "relayId": 99999},
		},
	})
	if code != 0 {
		t.Fatalf("mixed assignments code=%d body=%s", code, body)
	}
	if !strings.Contains(body, `"skippedRelays":1`) {
		t.Fatalf("mixed receipt must carry skippedRelays=1; body=%s", body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_inbound WHERE user_id = 2 AND inbound_id = ? AND relay_id = 0`, inboundID); n != 1 {
		t.Fatalf("direct assignment must persist, got %d", n)
	}

	// 真实 relay：不跳过、计数 0、行落库。
	relay := &model.NodeInboundRelay{InboundID: inboundID, ForwardID: 20, Name: "relay-b5", Managed: 1, CreatedTime: time.Now().UnixMilli()}
	if err := f.r.CreateInboundRelay(relay); err != nil {
		t.Fatalf("create relay: %v", err)
	}
	relayID := int64(f.count(t, `SELECT COALESCE(MAX(id),0) FROM node_inbound_relay`))
	if relayID <= 0 {
		t.Fatalf("relay id not assigned")
	}
	code, body = f.postB7Admin(t, f.h.setUserInbounds, map[string]interface{}{
		"userId":      2,
		"assignments": []map[string]interface{}{{"inboundId": inboundID, "relayId": relayID}},
	})
	if code != 0 {
		t.Fatalf("valid relay assignment code=%d body=%s", code, body)
	}
	if !strings.Contains(body, `"skippedRelays":0`) {
		t.Fatalf("valid relay receipt must carry skippedRelays=0; body=%s", body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_inbound WHERE user_id = 2 AND inbound_id = ? AND relay_id = ?`, inboundID, relayID); n != 1 {
		t.Fatalf("valid relay assignment must persist, got %d", n)
	}
}
