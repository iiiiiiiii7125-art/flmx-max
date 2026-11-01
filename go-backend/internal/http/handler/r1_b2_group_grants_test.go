package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"go-backend/internal/store/repo"
)

// R1 批次 2（G 族）回归测试：R5-FGRP 组语义（分组只限定分配
// 范围、组操作不增删任何实体授权）、替换事务化、范围登记回执、
// groupIds 存在性校验、批量分配原子性。

type b2Fixture struct {
	h *Handler
	r *repo.Repository
}

// seedB2Fixture 造数：
//   - 用户组 G=1（成员 U=2、U=4）；隧道组 TG=1（含隧道 T=1、T=3）
//   - group_permission G↔TG
//   - UT10: U2/T1 组建授权（grant created_by_group=1）
//   - UT13: U4/T1 组建授权（grant created_by_group=1）
//   - UT12: U2/T3 手工在先、分组仅登记（grant created_by_group=0）
//   - UT11: U2/T2 纯手工授权（无 grant 行）
//   - forward20: U2/T1；forward21: U2/T3
func seedB2Fixture(t *testing.T) *b2Fixture {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "r1-b2.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	stmts := []string{
		`INSERT INTO user(id, user, pwd, role_id, exp_time, flow, in_flow, out_flow, flow_reset_time, num, created_time, updated_time, status) VALUES(2, 'member', 'pwd', 1, 0, 100, 0, 0, 0, 10, 1000, 1000, 1)`,
		`INSERT INTO user(id, user, pwd, role_id, exp_time, flow, in_flow, out_flow, flow_reset_time, num, created_time, updated_time, status) VALUES(4, 'member2', 'pwd', 1, 0, 100, 0, 0, 0, 10, 1000, 1000, 1)`,
		`INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, in_ip, inx) VALUES(1, 't-one', 1, 1, 'tls', 1, 1000, 1000, 1, NULL, 0)`,
		`INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, in_ip, inx) VALUES(2, 't-two', 1, 1, 'tls', 1, 1000, 1000, 1, NULL, 0)`,
		`INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, in_ip, inx) VALUES(3, 't-three', 1, 1, 'tls', 1, 1000, 1000, 1, NULL, 0)`,
		`INSERT INTO user_group(id, name, created_time, updated_time, status) VALUES(1, 'g-one', 1000, 1000, 1)`,
		`INSERT INTO tunnel_group(id, name, created_time, updated_time, status) VALUES(1, 'tg-one', 1000, 1000, 1)`,
		`INSERT INTO user_group_user(user_group_id, user_id, created_time) VALUES(1, 2, 1000)`,
		`INSERT INTO user_group_user(user_group_id, user_id, created_time) VALUES(1, 4, 1000)`,
		`INSERT INTO tunnel_group_tunnel(tunnel_group_id, tunnel_id, created_time) VALUES(1, 1, 1000)`,
		`INSERT INTO tunnel_group_tunnel(tunnel_group_id, tunnel_id, created_time) VALUES(1, 3, 1000)`,
		`INSERT INTO group_permission(user_group_id, tunnel_group_id, created_time) VALUES(1, 1, 1000)`,
		`INSERT INTO user_tunnel(id, user_id, tunnel_id, num, flow, in_flow, out_flow, flow_reset_time, exp_time, status) VALUES(10, 2, 1, 10, 100, 0, 0, 0, 0, 1)`,
		`INSERT INTO user_tunnel(id, user_id, tunnel_id, num, flow, in_flow, out_flow, flow_reset_time, exp_time, status) VALUES(11, 2, 2, 10, 100, 0, 0, 0, 0, 1)`,
		`INSERT INTO user_tunnel(id, user_id, tunnel_id, num, flow, in_flow, out_flow, flow_reset_time, exp_time, status) VALUES(12, 2, 3, 10, 100, 0, 0, 0, 0, 1)`,
		`INSERT INTO user_tunnel(id, user_id, tunnel_id, num, flow, in_flow, out_flow, flow_reset_time, exp_time, status) VALUES(13, 4, 1, 10, 100, 0, 0, 0, 0, 1)`,
		`INSERT INTO group_permission_grant(user_group_id, tunnel_group_id, user_tunnel_id, created_by_group, created_time) VALUES(1, 1, 10, 1, 1000)`,
		`INSERT INTO group_permission_grant(user_group_id, tunnel_group_id, user_tunnel_id, created_by_group, created_time) VALUES(1, 1, 12, 0, 1000)`,
		`INSERT INTO group_permission_grant(user_group_id, tunnel_group_id, user_tunnel_id, created_by_group, created_time) VALUES(1, 1, 13, 1, 1000)`,
		`INSERT INTO forward(id, user_id, user_name, name, tunnel_id, remote_addr, strategy, in_flow, out_flow, created_time, updated_time, status, inx) VALUES(20, 2, 'member', 'f-t1', 1, '10.0.0.1:80', 'fifo', 0, 0, 1000, 1000, 1, 0)`,
		`INSERT INTO forward(id, user_id, user_name, name, tunnel_id, remote_addr, strategy, in_flow, out_flow, created_time, updated_time, status, inx) VALUES(21, 2, 'member', 'f-t3', 3, '10.0.0.3:80', 'fifo', 0, 0, 1000, 1000, 1, 0)`,
	}
	for _, s := range stmts {
		if err := r.DB().Exec(s).Error; err != nil {
			t.Fatalf("seed: %v (%s)", err, s)
		}
	}
	return &b2Fixture{h: New(r, "test-secret"), r: r}
}

func (f *b2Fixture) count(t *testing.T, query string, args ...interface{}) int {
	t.Helper()
	return mustQueryInt(t, f.r, query, args...)
}

func (f *b2Fixture) post(t *testing.T, fn func(http.ResponseWriter, *http.Request), body interface{}) (int, string) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/test", bytes.NewReader(raw))
	res := httptest.NewRecorder()
	fn(res, req)
	var payload struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, res.Body.String())
	}
	return payload.Code, res.Body.String()
}

// assertGrantsIntactT1 断言 R5-FGRP 语义：任一组操作之后，夹具的
// 全部实体授权（组展开来源 UT10/UT13、手工 UT11/UT12）与转发
// （20/21）逐行存续、隧道 1 权限照常可用。
func (f *b2Fixture) assertGrantsIntactT1(t *testing.T) {
	t.Helper()
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE id IN (10, 11, 12, 13)`); n != 4 {
		t.Fatalf("R5-FGRP: all fixture user_tunnels must survive group operations, got %d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM forward WHERE id IN (20, 21)`); n != 2 {
		t.Fatalf("R5-FGRP: forwards must survive group operations, got %d", n)
	}
	if err := f.h.ensureTunnelPermission(2, 1, 1); err != nil {
		t.Fatalf("R5-FGRP: tunnel 1 permission must remain usable after group operations: %v", err)
	}
}

// X-P1-2 路径①（R5-FGRP 改版）：userUpdate 改 groupIds 移出分组
// 只改成员记账——既有授权（含组展开来源的 UT10）与转发逐行保留。
func TestB2_XP12_UserUpdateLeavesGrantsIntact(t *testing.T) {
	f := seedB2Fixture(t)
	if err := f.h.ensureTunnelPermission(2, 1, 1); err != nil {
		t.Fatalf("precondition: user must hold tunnel 1 permission: %v", err)
	}
	code, body := f.post(t, f.h.userUpdate, map[string]interface{}{"id": 2, "user": "member", "groupIds": []int64{}})
	if code != 0 {
		t.Fatalf("userUpdate code=%d body=%s", code, body)
	}
	ids, err := f.r.GetUserGroupIDsByUserID(2)
	if err != nil || len(ids) != 0 {
		t.Fatalf("membership must be cleared, got %v err=%v", ids, err)
	}
	f.assertGrantsIntactT1(t)
}

// X-P1-2 路径②（R5-FGRP 改版）：groupTunnelAssign 换隧道组成员
// （移除隧道 1）只改范围记账——既有授权与转发逐行保留。
func TestB2_XP12_GroupTunnelAssignLeavesGrantsIntact(t *testing.T) {
	f := seedB2Fixture(t)
	code, body := f.post(t, f.h.groupTunnelAssign, map[string]interface{}{"groupId": 1, "tunnelIds": []int64{3}})
	if code != 0 {
		t.Fatalf("groupTunnelAssign code=%d body=%s", code, body)
	}
	f.assertGrantsIntactT1(t)
}

// X-P1-2 路径③a（R5-FGRP 改版）：groupUserDelete 删用户组只做记账
// 级联（成员/权限对/台账行/组行）——既有授权与转发逐行保留。
func TestB2_XP12_GroupUserDeleteLeavesGrantsIntact(t *testing.T) {
	f := seedB2Fixture(t)
	code, body := f.post(t, f.h.groupUserDelete, map[string]interface{}{"id": 1})
	if code != 0 {
		t.Fatalf("groupUserDelete code=%d body=%s", code, body)
	}
	f.assertGrantsIntactT1(t)
	if n := f.count(t, `SELECT COUNT(*) FROM group_permission_grant WHERE user_group_id = 1`); n != 0 {
		t.Fatalf("deleted group's grant ledger rows must be cascade-cleared, got %d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM group_permission WHERE user_group_id = 1`); n != 0 {
		t.Fatalf("deleted group's permission rows must be cascade-cleared, got %d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_group_user WHERE user_group_id = 1`); n != 0 {
		t.Fatalf("deleted group's membership rows must be cascade-cleared, got %d", n)
	}
}

// X-P1-2 路径③b（R5-FGRP 改版）：groupTunnelDelete 删隧道组同口径。
func TestB2_XP12_GroupTunnelDeleteLeavesGrantsIntact(t *testing.T) {
	f := seedB2Fixture(t)
	code, body := f.post(t, f.h.groupTunnelDelete, map[string]interface{}{"id": 1})
	if code != 0 {
		t.Fatalf("groupTunnelDelete code=%d body=%s", code, body)
	}
	f.assertGrantsIntactT1(t)
	if n := f.count(t, `SELECT COUNT(*) FROM group_permission_grant WHERE tunnel_group_id = 1`); n != 0 {
		t.Fatalf("deleted tunnel group's grant ledger rows must be cascade-cleared, got %d", n)
	}
}

// X-P2-6：替换中途注入约束失败 → 成员保持原样且接口报错。
func TestB2_XP26_UserUpdateReplaceAtomicAndReported(t *testing.T) {
	f := seedB2Fixture(t)
	if err := f.r.DB().Exec(`INSERT INTO user_group(id, name, created_time, updated_time, status) VALUES(7, 'trap', 1000, 1000, 1)`).Error; err != nil {
		t.Fatalf("seed trap group: %v", err)
	}
	if err := f.r.DB().Exec(`CREATE TRIGGER trap_ugu BEFORE INSERT ON user_group_user WHEN NEW.user_group_id = 7 BEGIN SELECT RAISE(ABORT, 'injected constraint failure'); END`).Error; err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	code, body := f.post(t, f.h.userUpdate, map[string]interface{}{"id": 2, "user": "member", "groupIds": []int64{1, 7}})
	if code == 0 {
		t.Fatalf("replace failure must be reported, got success; body=%s", body)
	}
	ids, err := f.r.GetUserGroupIDsByUserID(2)
	if err != nil {
		t.Fatalf("get groups: %v", err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("membership must remain original [1], got %v", ids)
	}
}

// X-P2-16（R5-FGRP 改版）：组授权只登记范围——回执不再有展开失败
// 条目；授权后 group_permission 多一行、台账零新增、实体授权零变化
// （成员对组内隧道不因授权自动获得任何 UT）。
func TestB2_XP216_GroupPermissionAssignRegistersScopeOnly(t *testing.T) {
	f := seedB2Fixture(t)
	stmts := []string{
		`INSERT INTO user_group(id, name, created_time, updated_time, status) VALUES(2, 'g-two', 1000, 1000, 1)`,
		`INSERT INTO tunnel_group(id, name, created_time, updated_time, status) VALUES(2, 'tg-two', 1000, 1000, 1)`,
		`INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, in_ip, inx) VALUES(4, 't-four', 1, 1, 'tls', 1, 1000, 1000, 1, NULL, 0)`,
		`INSERT INTO user_group_user(user_group_id, user_id, created_time) VALUES(2, 2, 1000)`,
		`INSERT INTO user_group_user(user_group_id, user_id, created_time) VALUES(2, 4, 1000)`,
		`INSERT INTO tunnel_group_tunnel(tunnel_group_id, tunnel_id, created_time) VALUES(2, 4, 1000)`,
	}
	for _, s := range stmts {
		if err := f.r.DB().Exec(s).Error; err != nil {
			t.Fatalf("seed xp216: %v (%s)", err, s)
		}
	}
	utBefore := f.count(t, `SELECT COUNT(*) FROM user_tunnel`)
	grantBefore := f.count(t, `SELECT COUNT(*) FROM group_permission_grant`)
	code, body := f.post(t, f.h.groupPermissionAssign, map[string]interface{}{"userGroupId": 2, "tunnelGroupId": 2})
	if code != 0 {
		t.Fatalf("groupPermissionAssign code=%d body=%s", code, body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM group_permission WHERE user_group_id = 2 AND tunnel_group_id = 2`); n != 1 {
		t.Fatalf("scope pair must be registered, got %d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel`); n != utBefore {
		t.Fatalf("R5-FGRP: group assign must not materialize any user_tunnel, before=%d after=%d", utBefore, n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM group_permission_grant`); n != grantBefore {
		t.Fatalf("R5-FGRP: group assign must not write grant ledger rows, before=%d after=%d", grantBefore, n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE tunnel_id = 4`); n != 0 {
		t.Fatalf("R5-FGRP: no member may gain tunnel 4 from a group assign, got %d", n)
	}
}

// X-P3-5：groupIds 含不存在的组 → 建/改用户报错且零落行。
func TestB2_XP35_NonexistentGroupRejected(t *testing.T) {
	t.Run("update", func(t *testing.T) {
		f := seedB2Fixture(t)
		code, body := f.post(t, f.h.userUpdate, map[string]interface{}{"id": 2, "user": "member", "groupIds": []int64{99999}})
		if code == 0 {
			t.Fatalf("nonexistent group must error; body=%s", body)
		}
		if n := f.count(t, `SELECT COUNT(*) FROM user_group_user WHERE user_group_id = 99999`); n != 0 {
			t.Fatalf("phantom membership row written")
		}
		ids, err := f.r.GetUserGroupIDsByUserID(2)
		if err != nil || len(ids) != 1 || ids[0] != 1 {
			t.Fatalf("membership must be unchanged [1], got %v err=%v", ids, err)
		}
	})
	t.Run("create", func(t *testing.T) {
		f := seedB2Fixture(t)
		code, body := f.post(t, f.h.userCreate, map[string]interface{}{"user": "newbie", "pwd": "secret123", "groupIds": []int64{99999}})
		if code == 0 {
			t.Fatalf("nonexistent group must error; body=%s", body)
		}
		if n := f.count(t, `SELECT COUNT(*) FROM user WHERE user = 'newbie'`); n != 0 {
			t.Fatalf("user row must not be created")
		}
		if n := f.count(t, `SELECT COUNT(*) FROM user_group_user WHERE user_group_id = 99999`); n != 0 {
			t.Fatalf("phantom membership row written")
		}
	})
}

// X-P3-22：批量分配中途失败 → 整批回滚、先行条目未生效。
func TestB2_XP322_UserTunnelBatchAssignAtomic(t *testing.T) {
	f := seedB2Fixture(t)
	stmts := []string{
		`INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, in_ip, inx) VALUES(5, 't-five', 1, 1, 'tls', 1, 1000, 1000, 1, NULL, 0)`,
		`INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, in_ip, inx) VALUES(6, 't-six', 1, 1, 'tls', 1, 1000, 1000, 1, NULL, 0)`,
		// R5-FGRP：5/6 须先入 TG1 范围，否则批量在范围门即被拒、
		// 触发器永不命中、原子性无从证明。
		`INSERT INTO tunnel_group_tunnel(tunnel_group_id, tunnel_id, created_time) VALUES(1, 5, 1000)`,
		`INSERT INTO tunnel_group_tunnel(tunnel_group_id, tunnel_id, created_time) VALUES(1, 6, 1000)`,
		`CREATE TRIGGER fail_ut BEFORE INSERT ON user_tunnel WHEN NEW.tunnel_id = 6 BEGIN SELECT RAISE(ABORT, 'injected user_tunnel failure'); END`,
	}
	for _, s := range stmts {
		if err := f.r.DB().Exec(s).Error; err != nil {
			t.Fatalf("seed xp322: %v", err)
		}
	}
	code, body := f.post(t, f.h.userTunnelBatchAssign, map[string]interface{}{
		"userId":  2,
		"tunnels": []map[string]interface{}{{"tunnelId": 5}, {"tunnelId": 6}},
	})
	if code == 0 {
		t.Fatalf("mid-batch failure must error; body=%s", body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE user_id = 2 AND tunnel_id = 5`); n != 0 {
		t.Fatalf("first batch entry must be rolled back, found %d rows", n)
	}
}
