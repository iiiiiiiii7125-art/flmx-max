package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"go-backend/internal/auth"
	"go-backend/internal/http/middleware"
	"go-backend/internal/store/repo"
)

// R1 批次 2（G 族）回归测试：分组授权撤销完整性、替换事务化、
// 授权展开回执、groupIds 存在性校验、批量分配原子性。

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

// assertRevokedT1 断言组建授权（隧道 1）已被撤销、转发已清、手工授权不受影响。
func (f *b2Fixture) assertRevokedT1(t *testing.T) {
	t.Helper()
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE id IN (10, 13)`); n != 0 {
		t.Fatalf("group-created user_tunnel for tunnel 1 still present: %d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM group_permission_grant WHERE user_tunnel_id IN (10, 13)`); n != 0 {
		t.Fatalf("grant ledger rows for revoked user_tunnels remain: %d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM forward WHERE id = 20`); n != 0 {
		t.Fatalf("forward on revoked tunnel still present")
	}
	if n := f.count(t, `SELECT COUNT(*) FROM forward WHERE id = 21`); n != 1 {
		t.Fatalf("forward on manual tunnel must survive")
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE id IN (11, 12)`); n != 2 {
		t.Fatalf("manual user_tunnels must survive, got %d", n)
	}
	if err := f.h.ensureTunnelPermission(2, 1, 1); err == nil {
		t.Fatalf("ensureTunnelPermission must reject revoked tunnel")
	}
	// forwardCreate(T) 应被权限门拒绝
	raw, _ := json.Marshal(map[string]interface{}{"tunnelId": 1, "name": "x", "remoteAddr": "10.0.0.9:80"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/forward/create", bytes.NewReader(raw))
	claims := auth.Claims{Sub: strconv.FormatInt(2, 10), RoleID: 1}
	req = req.WithContext(context.WithValue(req.Context(), middleware.ClaimsContextKey, claims))
	res := httptest.NewRecorder()
	f.h.forwardCreate(res, req)
	var payload struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode forwardCreate response: %v", err)
	}
	if payload.Code == 0 {
		t.Fatalf("forwardCreate on revoked tunnel must be rejected; body=%s", res.Body.String())
	}
}

// X-P1-2 路径①：userUpdate 改 groupIds 移出分组 → 撤销组建授权。
func TestB2_XP12_UserUpdateRevokesGroupGrants(t *testing.T) {
	f := seedB2Fixture(t)
	if err := f.h.ensureTunnelPermission(2, 1, 1); err != nil {
		t.Fatalf("precondition: user must hold tunnel 1 permission: %v", err)
	}
	code, body := f.post(t, f.h.userUpdate, map[string]interface{}{"id": 2, "user": "member", "groupIds": []int64{}})
	if code != 0 {
		t.Fatalf("userUpdate code=%d body=%s", code, body)
	}
	// 只有 U2 被移出：UT10 撤销；U4 的 UT13 仍在组内应保留。
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE id = 10`); n != 0 {
		t.Fatalf("UT10 (group-created, user removed) must be revoked")
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE id = 13`); n != 1 {
		t.Fatalf("UT13 (user still in group) must survive")
	}
	if n := f.count(t, `SELECT COUNT(*) FROM forward WHERE id = 20`); n != 0 {
		t.Fatalf("forward on revoked tunnel must be cleaned")
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE id IN (11, 12)`); n != 2 {
		t.Fatalf("manual user_tunnels must survive")
	}
	if err := f.h.ensureTunnelPermission(2, 1, 1); err == nil {
		t.Fatalf("ensureTunnelPermission must reject revoked tunnel")
	}
}

// X-P1-2 路径②：groupTunnelAssign 换隧道组成员（移除隧道 1）→ 撤销组建授权。
func TestB2_XP12_GroupTunnelAssignRevokesGroupGrants(t *testing.T) {
	f := seedB2Fixture(t)
	code, body := f.post(t, f.h.groupTunnelAssign, map[string]interface{}{"groupId": 1, "tunnelIds": []int64{3}})
	if code != 0 {
		t.Fatalf("groupTunnelAssign code=%d body=%s", code, body)
	}
	f.assertRevokedT1(t)
}

// X-P1-2 路径③a：groupUserDelete 删用户组 → 撤销组建授权并清理转发。
func TestB2_XP12_GroupUserDeleteRevokesGroupGrants(t *testing.T) {
	f := seedB2Fixture(t)
	code, body := f.post(t, f.h.groupUserDelete, map[string]interface{}{"id": 1})
	if code != 0 {
		t.Fatalf("groupUserDelete code=%d body=%s", code, body)
	}
	f.assertRevokedT1(t)
}

// X-P1-2 路径③b：groupTunnelDelete 删隧道组 → 撤销组建授权并清理转发。
func TestB2_XP12_GroupTunnelDeleteRevokesGroupGrants(t *testing.T) {
	f := seedB2Fixture(t)
	code, body := f.post(t, f.h.groupTunnelDelete, map[string]interface{}{"id": 1})
	if code != 0 {
		t.Fatalf("groupTunnelDelete code=%d body=%s", code, body)
	}
	f.assertRevokedT1(t)
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

// X-P2-16：注入一条 grant 写失败 → 回执含失败条目；全失败报错。
func TestB2_XP216_GroupPermissionAssignReportsGrantFailures(t *testing.T) {
	setup := func(t *testing.T, trigger string) *b2Fixture {
		f := seedB2Fixture(t)
		stmts := []string{
			`INSERT INTO user_group(id, name, created_time, updated_time, status) VALUES(2, 'g-two', 1000, 1000, 1)`,
			`INSERT INTO tunnel_group(id, name, created_time, updated_time, status) VALUES(2, 'tg-two', 1000, 1000, 1)`,
			`INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, in_ip, inx) VALUES(4, 't-four', 1, 1, 'tls', 1, 1000, 1000, 1, NULL, 0)`,
			`INSERT INTO user_group_user(user_group_id, user_id, created_time) VALUES(2, 2, 1000)`,
			`INSERT INTO user_group_user(user_group_id, user_id, created_time) VALUES(2, 4, 1000)`,
			`INSERT INTO tunnel_group_tunnel(tunnel_group_id, tunnel_id, created_time) VALUES(2, 4, 1000)`,
			`INSERT INTO user_tunnel(id, user_id, tunnel_id, num, flow, in_flow, out_flow, flow_reset_time, exp_time, status) VALUES(40, 2, 4, 10, 100, 0, 0, 0, 0, 1)`,
			`INSERT INTO user_tunnel(id, user_id, tunnel_id, num, flow, in_flow, out_flow, flow_reset_time, exp_time, status) VALUES(41, 4, 4, 10, 100, 0, 0, 0, 0, 1)`,
			trigger,
		}
		for _, s := range stmts {
			if err := f.r.DB().Exec(s).Error; err != nil {
				t.Fatalf("seed xp216: %v (%s)", err, s)
			}
		}
		return f
	}

	t.Run("partial failure is listed in receipt", func(t *testing.T) {
		f := setup(t, `CREATE TRIGGER fail_grant BEFORE INSERT ON group_permission_grant WHEN NEW.user_tunnel_id = 41 BEGIN SELECT RAISE(ABORT, 'injected grant failure'); END`)
		code, body := f.post(t, f.h.groupPermissionAssign, map[string]interface{}{"userGroupId": 2, "tunnelGroupId": 2})
		if code != 0 {
			t.Fatalf("partial failure should still succeed overall, code=%d body=%s", code, body)
		}
		if !strings.Contains(body, "failures") || !strings.Contains(body, `"userId":4`) {
			t.Fatalf("receipt must list the failed entry (userId=4); body=%s", body)
		}
		if n := f.count(t, `SELECT COUNT(*) FROM group_permission_grant WHERE user_group_id = 2 AND user_tunnel_id = 40`); n != 1 {
			t.Fatalf("successful grant must be recorded")
		}
	})

	t.Run("all failures are an error", func(t *testing.T) {
		f := setup(t, `CREATE TRIGGER fail_grant_all BEFORE INSERT ON group_permission_grant WHEN NEW.user_tunnel_id IN (40, 41) BEGIN SELECT RAISE(ABORT, 'injected grant failure'); END`)
		code, body := f.post(t, f.h.groupPermissionAssign, map[string]interface{}{"userGroupId": 2, "tunnelGroupId": 2})
		if code == 0 {
			t.Fatalf("all-failed assignment must return error; body=%s", body)
		}
	})
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
