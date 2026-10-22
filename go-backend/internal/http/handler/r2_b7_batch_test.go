package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/auth"
	"go-backend/internal/http/middleware"
	"go-backend/internal/store/model"
)

// R2 批 7 回归：分配引用完整性（R2X-012）/ 分组停用执法（R2X-013）/
// UT 单删台账（R2X-035）/ 校验前置（R2X-036）/ 删除幂等回执（R2X-048）。
// 复用 r1_b2_group_grants_test.go 的 seedB2Fixture（G=1/TG=1、UT10/13 组建、
// UT11/12 手工、forward20/21）。

// postB7Admin 以管理员 claims 调用需要 roleID==0 的 handler。
func (f *b2Fixture) postB7Admin(t *testing.T, fn func(http.ResponseWriter, *http.Request), body interface{}) (int, string) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/test", bytes.NewReader(raw))
	ctx := context.WithValue(req.Context(), middleware.ClaimsContextKey, auth.Claims{Sub: "1", RoleID: 0})
	res := httptest.NewRecorder()
	fn(res, req.WithContext(ctx))
	var payload struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, res.Body.String())
	}
	return payload.Code, res.Body.String()
}

func (f *b2Fixture) execB7(t *testing.T, stmts ...string) {
	t.Helper()
	for _, s := range stmts {
		if err := f.r.DB().Exec(s).Error; err != nil {
			t.Fatalf("seed b7: %v (%s)", err, s)
		}
	}
}

func (f *b2Fixture) mkSubscriptionB7(t *testing.T) int64 {
	t.Helper()
	now := time.Now().UnixMilli()
	sub := &model.Subscription{Name: "b7-sub", Kind: model.SubscriptionShare, Token: strings.Repeat("c", 32),
		UUID: "u-b7", Password: "p", Enabled: 1, CreatedTime: now, UpdatedTime: now}
	if err := f.r.SaveSubscription(sub); err != nil {
		t.Fatalf("save subscription: %v", err)
	}
	return sub.ID
}

// ─── R2X-012 ────────────────────────────────────────────────────────────────

func TestR2B7_012_GroupUserAssignPhantomUserRejected(t *testing.T) {
	f := seedB2Fixture(t)
	code, body := f.post(t, f.h.groupUserAssign, map[string]interface{}{"groupId": 1, "userIds": []int64{2, 99999}})
	if code == 0 {
		t.Fatalf("phantom user must error; body=%s", body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_group_user WHERE user_id = 99999`); n != 0 {
		t.Fatalf("phantom membership rows=%d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_group_user WHERE user_group_id = 1`); n != 2 {
		t.Fatalf("existing membership must be unchanged, got %d", n)
	}
}

func TestR2B7_012_GroupTunnelAssignPhantomTunnelRejected(t *testing.T) {
	f := seedB2Fixture(t)
	code, body := f.post(t, f.h.groupTunnelAssign, map[string]interface{}{"groupId": 1, "tunnelIds": []int64{1, 99998}})
	if code == 0 {
		t.Fatalf("phantom tunnel must error; body=%s", body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM tunnel_group_tunnel WHERE tunnel_id = 99998`); n != 0 {
		t.Fatalf("phantom membership rows=%d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM tunnel_group_tunnel WHERE tunnel_group_id = 1`); n != 2 {
		t.Fatalf("existing membership must be unchanged, got %d", n)
	}
}

func TestR2B7_012_GroupAssignPhantomGroupRejected(t *testing.T) {
	f := seedB2Fixture(t)
	code, body := f.post(t, f.h.groupUserAssign, map[string]interface{}{"groupId": 99999, "userIds": []int64{2}})
	if code == 0 {
		t.Fatalf("phantom user group must error; body=%s", body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_group_user WHERE user_group_id = 99999`); n != 0 {
		t.Fatalf("phantom rows=%d", n)
	}
	code, body = f.post(t, f.h.groupTunnelAssign, map[string]interface{}{"groupId": 99999, "tunnelIds": []int64{1}})
	if code == 0 {
		t.Fatalf("phantom tunnel group must error; body=%s", body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM tunnel_group_tunnel WHERE tunnel_group_id = 99999`); n != 0 {
		t.Fatalf("phantom rows=%d", n)
	}
}

func TestR2B7_012_GroupPermissionPhantomGroupsRejected(t *testing.T) {
	f := seedB2Fixture(t)
	code, body := f.post(t, f.h.groupPermissionAssign, map[string]interface{}{"userGroupId": 99998, "tunnelGroupId": 99999})
	if code == 0 {
		t.Fatalf("phantom groups must error; body=%s", body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM group_permission WHERE user_group_id = 99998 OR tunnel_group_id = 99999`); n != 0 {
		t.Fatalf("phantom permission rows=%d", n)
	}
}

func TestR2B7_012_UserTunnelAssignPhantomRejected(t *testing.T) {
	f := seedB2Fixture(t)
	code, body := f.post(t, f.h.userTunnelAssign, map[string]interface{}{"userId": 99999, "tunnelId": 1})
	if code == 0 {
		t.Fatalf("phantom user must error; body=%s", body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE user_id = 99999`); n != 0 {
		t.Fatalf("phantom user_tunnel rows=%d", n)
	}
	code, body = f.post(t, f.h.userTunnelAssign, map[string]interface{}{"userId": 2, "tunnelId": 99997})
	if code == 0 {
		t.Fatalf("phantom tunnel must error; body=%s", body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE tunnel_id = 99997`); n != 0 {
		t.Fatalf("phantom user_tunnel rows=%d", n)
	}
	// 批量分配同口径（共用 upsertUserTunnelDB 的事务分支）。
	code, body = f.post(t, f.h.userTunnelBatchAssign, map[string]interface{}{
		"userId":  2,
		"tunnels": []map[string]interface{}{{"tunnelId": 99997}},
	})
	if code == 0 {
		t.Fatalf("batch phantom tunnel must error; body=%s", body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE tunnel_id = 99997`); n != 0 {
		t.Fatalf("phantom user_tunnel rows=%d", n)
	}
}

func TestR2B7_012_LatentTunnelGrantDoesNotActivate(t *testing.T) {
	f := seedB2Fixture(t)
	// 隧道 4 尚不存在（下一个自增 ID）：预分配必须被拒。
	code, body := f.post(t, f.h.userTunnelAssign, map[string]interface{}{"userId": 2, "tunnelId": 4})
	if code == 0 {
		t.Fatalf("assign of not-yet-created tunnel must error; body=%s", body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE user_id = 2 AND tunnel_id = 4`); n != 0 {
		t.Fatalf("phantom user_tunnel rows=%d", n)
	}
	f.execB7(t, `INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, in_ip, inx) VALUES(4, 't-four', 1, 1, 'tls', 1, 1000, 1000, 1, NULL, 0)`)
	if err := f.h.ensureTunnelPermission(2, 1, 4); err == nil {
		t.Fatalf("ensureTunnelPermission must still reject tunnel 4 after its creation")
	}
}

func TestR2B7_012_UserSubscriptionSetPhantomUserRejected(t *testing.T) {
	f := seedB2Fixture(t)
	subID := f.mkSubscriptionB7(t)
	code, body := f.postB7Admin(t, f.h.userSubscriptionSet, map[string]interface{}{"userId": 99999, "subscriptionIds": []int64{subID}})
	if code == 0 {
		t.Fatalf("phantom user must error; body=%s", body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_subscription WHERE user_id = 99999`); n != 0 {
		t.Fatalf("phantom user_subscription rows=%d", n)
	}
	// 正常路径不回归。
	code, body = f.postB7Admin(t, f.h.userSubscriptionSet, map[string]interface{}{"userId": 2, "subscriptionIds": []int64{subID}})
	if code != 0 {
		t.Fatalf("valid assignment must succeed; body=%s", body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_subscription WHERE user_id = 2 AND subscription_id = ?`, subID); n != 1 {
		t.Fatalf("valid assignment rows=%d", n)
	}
}

func TestR2B7_012_SetUserInboundsPhantomRejected(t *testing.T) {
	f := seedB2Fixture(t)
	// legacy 格式传不存在入站（真实用户）。
	code, body := f.postB7Admin(t, f.h.setUserInbounds, map[string]interface{}{"userId": 2, "inboundIds": []int64{99996}})
	if code == 0 {
		t.Fatalf("legacy phantom inbound must error; body=%s", body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_inbound WHERE inbound_id = 99996`); n != 0 {
		t.Fatalf("phantom user_inbound rows=%d", n)
	}
	// legacy 格式传不存在用户。
	code, body = f.postB7Admin(t, f.h.setUserInbounds, map[string]interface{}{"userId": 99999, "inboundIds": []int64{1}})
	if code == 0 {
		t.Fatalf("legacy phantom user must error; body=%s", body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_inbound WHERE user_id = 99999`); n != 0 {
		t.Fatalf("phantom user_inbound rows=%d", n)
	}
	// assignments 格式传不存在用户（复核扩围新探针）。
	code, body = f.postB7Admin(t, f.h.setUserInbounds, map[string]interface{}{
		"userId":      99999,
		"assignments": []map[string]interface{}{{"inboundId": 1, "relayId": 0}},
	})
	if code == 0 {
		t.Fatalf("assignments phantom user must error; body=%s", body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_inbound WHERE user_id = 99999`); n != 0 {
		t.Fatalf("phantom user_inbound rows=%d", n)
	}
}

func TestR2B7_012_SetUserInboundsValidInboundSucceeds(t *testing.T) {
	f := seedB2Fixture(t)
	now := time.Now().UnixMilli()
	f.execB7(t,
		`INSERT INTO node(name, secret, server_ip, port, created_time, status) VALUES('n-b7', 's', '203.0.113.9', '1000-2000', 1000, 1)`,
	)
	nodeID := mustLastInsertID(t, f.r, "node")
	if err := f.r.DB().Exec(`INSERT INTO node_kernel(node_id, kernel_type, version, status, created_time, updated_time) VALUES(?, 'xray', 'latest', 'pending', ?, ?)`, nodeID, now, now).Error; err != nil {
		t.Fatalf("seed kernel: %v", err)
	}
	kernelID := mustLastInsertID(t, f.r, "kernel")
	if err := f.r.DB().Exec(`INSERT INTO node_inbound(kernel_id, node_id, name, protocol, listen, port, settings, enabled, created_time, updated_time) VALUES(?, ?, 'ib-b7', 'trojan', '::', 443, '{"password":"x"}', 1, ?, ?)`, kernelID, nodeID, now, now).Error; err != nil {
		t.Fatalf("seed inbound: %v", err)
	}
	inboundID := mustLastInsertID(t, f.r, "inbound")
	code, body := f.postB7Admin(t, f.h.setUserInbounds, map[string]interface{}{"userId": 2, "inboundIds": []int64{inboundID}})
	if code != 0 {
		t.Fatalf("valid legacy assignment must succeed; body=%s", body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_inbound WHERE user_id = 2 AND inbound_id = ?`, inboundID); n != 1 {
		t.Fatalf("valid assignment rows=%d", n)
	}
	code, body = f.postB7Admin(t, f.h.setUserInbounds, map[string]interface{}{
		"userId":      2,
		"assignments": []map[string]interface{}{{"inboundId": inboundID, "relayId": 0}},
	})
	if code != 0 {
		t.Fatalf("valid assignments-format assignment must succeed; body=%s", body)
	}
}

// ─── R2X-013 ────────────────────────────────────────────────────────────────

func TestR2B7_013_AssignToDisabledGroupRejected(t *testing.T) {
	f := seedB2Fixture(t)
	// UG2 停用（成员 U4）；TG2 启用（含隧道 T2，U4 对 T2 无现存 UT）。
	f.execB7(t,
		`INSERT INTO user_group(id, name, created_time, updated_time, status) VALUES(2, 'g-off', 1000, 1000, 0)`,
		`INSERT INTO tunnel_group(id, name, created_time, updated_time, status) VALUES(2, 'tg-on', 1000, 1000, 1)`,
		`INSERT INTO user_group_user(user_group_id, user_id, created_time) VALUES(2, 4, 1000)`,
		`INSERT INTO tunnel_group_tunnel(tunnel_group_id, tunnel_id, created_time) VALUES(2, 2, 1000)`,
		// UG3 启用；TG3 停用。
		`INSERT INTO user_group(id, name, created_time, updated_time, status) VALUES(3, 'g-on', 1000, 1000, 1)`,
		`INSERT INTO tunnel_group(id, name, created_time, updated_time, status) VALUES(3, 'tg-off', 1000, 1000, 0)`,
		`INSERT INTO user_group_user(user_group_id, user_id, created_time) VALUES(3, 4, 1000)`,
		`INSERT INTO tunnel_group_tunnel(tunnel_group_id, tunnel_id, created_time) VALUES(3, 2, 1000)`,
	)
	code, body := f.post(t, f.h.groupPermissionAssign, map[string]interface{}{"userGroupId": 2, "tunnelGroupId": 2})
	if code == 0 {
		t.Fatalf("assign with disabled user group must error; body=%s", body)
	}
	if !strings.Contains(body, "停用") {
		t.Fatalf("error must state the group is disabled; body=%s", body)
	}
	code, body = f.post(t, f.h.groupPermissionAssign, map[string]interface{}{"userGroupId": 3, "tunnelGroupId": 3})
	if code == 0 {
		t.Fatalf("assign with disabled tunnel group must error; body=%s", body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM group_permission WHERE user_group_id IN (2, 3)`); n != 0 {
		t.Fatalf("permission rows for disabled assigns=%d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE user_id = 4 AND tunnel_id = 2`); n != 0 {
		t.Fatalf("disabled assign must not materialize user_tunnel, got %d", n)
	}
}

func TestR2B7_013_DisableUserGroupRevokesGrants(t *testing.T) {
	f := seedB2Fixture(t)
	code, body := f.post(t, f.h.groupUserUpdate, map[string]interface{}{"id": 1, "name": "g-one", "status": 0})
	if code != 0 {
		t.Fatalf("groupUserUpdate disable code=%d body=%s", code, body)
	}
	f.assertRevokedT1(t)
	if n := f.count(t, `SELECT COUNT(*) FROM group_permission_grant WHERE user_group_id = 1`); n != 0 {
		t.Fatalf("grants of disabled group must be revoked, got %d", n)
	}
	// 重新启用按成员关系补发组授权。
	code, body = f.post(t, f.h.groupUserUpdate, map[string]interface{}{"id": 1, "name": "g-one", "status": 1})
	if code != 0 {
		t.Fatalf("groupUserUpdate enable code=%d body=%s", code, body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE user_id = 2 AND tunnel_id = 1`); n != 1 {
		t.Fatalf("re-enable must restore group grant for U2/T1, got %d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE user_id = 4 AND tunnel_id = 1`); n != 1 {
		t.Fatalf("re-enable must restore group grant for U4/T1, got %d", n)
	}
	if err := f.h.ensureTunnelPermission(2, 1, 1); err != nil {
		t.Fatalf("re-enabled grant must be usable: %v", err)
	}
}

func TestR2B7_013_DisableTunnelGroupRevokesGrants(t *testing.T) {
	f := seedB2Fixture(t)
	code, body := f.post(t, f.h.groupTunnelUpdate, map[string]interface{}{"id": 1, "name": "tg-one", "status": 0})
	if code != 0 {
		t.Fatalf("groupTunnelUpdate disable code=%d body=%s", code, body)
	}
	f.assertRevokedT1(t)
	if n := f.count(t, `SELECT COUNT(*) FROM group_permission_grant WHERE tunnel_group_id = 1`); n != 0 {
		t.Fatalf("grants of disabled group must be revoked, got %d", n)
	}
}

func TestR2B7_013_SyncSkipsDisabledSide(t *testing.T) {
	f := seedB2Fixture(t)
	// 存量停用分组（直接落库模拟升级前已停用）：UG2 停用 + TG2 启用，
	// 已有 group_permission 对；sync 不得展开出任何新 UT。
	f.execB7(t,
		`INSERT INTO user_group(id, name, created_time, updated_time, status) VALUES(2, 'g-off', 1000, 1000, 0)`,
		`INSERT INTO tunnel_group(id, name, created_time, updated_time, status) VALUES(2, 'tg-on', 1000, 1000, 1)`,
		`INSERT INTO user_group_user(user_group_id, user_id, created_time) VALUES(2, 4, 1000)`,
		`INSERT INTO tunnel_group_tunnel(tunnel_group_id, tunnel_id, created_time) VALUES(2, 2, 1000)`,
		`INSERT INTO group_permission(user_group_id, tunnel_group_id, created_time) VALUES(2, 2, 1000)`,
		`INSERT INTO user_group(id, name, created_time, updated_time, status) VALUES(3, 'g-on', 1000, 1000, 1)`,
		`INSERT INTO tunnel_group(id, name, created_time, updated_time, status) VALUES(3, 'tg-off', 1000, 1000, 0)`,
		`INSERT INTO user_group_user(user_group_id, user_id, created_time) VALUES(3, 4, 1000)`,
		`INSERT INTO tunnel_group_tunnel(tunnel_group_id, tunnel_id, created_time) VALUES(3, 2, 1000)`,
		`INSERT INTO group_permission(user_group_id, tunnel_group_id, created_time) VALUES(3, 3, 1000)`,
	)
	if err := f.h.syncPermissionsByUserGroup(2); err != nil {
		t.Fatalf("sync by disabled user group: %v", err)
	}
	if err := f.h.syncPermissionsByTunnelGroup(3); err != nil {
		t.Fatalf("sync by disabled tunnel group: %v", err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE user_id = 4 AND tunnel_id = 2`); n != 0 {
		t.Fatalf("sync must skip pairs with a disabled side, materialized %d", n)
	}
}

// ─── R2X-035 + R2X-048（联动） ─────────────────────────────────────────────

func TestR2B7_035_UserTunnelRemoveClearsGrant(t *testing.T) {
	f := seedB2Fixture(t)
	code, body := f.post(t, f.h.userTunnelRemove, map[string]interface{}{"id": 10})
	if code != 0 {
		t.Fatalf("userTunnelRemove code=%d body=%s", code, body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE id = 10`); n != 0 {
		t.Fatalf("user_tunnel 10 must be deleted")
	}
	if n := f.count(t, `SELECT COUNT(*) FROM group_permission_grant WHERE user_tunnel_id = 10`); n != 0 {
		t.Fatalf("grant ledger for removed user_tunnel must be cleared, got %d", n)
	}
	// R2X-048 联动：组建 UT 的转发清理照常。
	if n := f.count(t, `SELECT COUNT(*) FROM forward WHERE id = 20`); n != 0 {
		t.Fatalf("forward on removed user_tunnel must be cleaned")
	}
	// repo 非 Tx 变体同口径。
	if err := f.r.DeleteUserTunnel(12); err != nil {
		t.Fatalf("DeleteUserTunnel: %v", err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM group_permission_grant WHERE user_tunnel_id = 12`); n != 0 {
		t.Fatalf("DeleteUserTunnel must clear grant ledger, got %d", n)
	}
}

func TestR2B7_048_UserTunnelRemoveNonexistentIsIdempotent(t *testing.T) {
	f := seedB2Fixture(t)
	code, body := f.post(t, f.h.userTunnelRemove, map[string]interface{}{"id": 99999})
	if code != 0 {
		t.Fatalf("removing a nonexistent user_tunnel must be idempotent success (V-12), code=%d body=%s", code, body)
	}
}

// ─── R2X-036 ────────────────────────────────────────────────────────────────

func TestR2B7_036_UserUpdateValidationBeforeWrite(t *testing.T) {
	f := seedB2Fixture(t)
	code, body := f.post(t, f.h.userUpdate, map[string]interface{}{"id": 2, "user": "renamed-member", "groupIds": []int64{99999}})
	if code == 0 {
		t.Fatalf("nonexistent group must error; body=%s", body)
	}
	var name string
	if err := f.r.DB().Raw(`SELECT user FROM user WHERE id = 2`).Scan(&name).Error; err != nil {
		t.Fatalf("read username: %v", err)
	}
	if name != "member" {
		t.Fatalf("username must stay original when validation fails, got %q", name)
	}
	// 配额校验同为首写之前：负配额 + 改名 → 报错且不落库。
	code, body = f.post(t, f.h.userUpdate, map[string]interface{}{"id": 2, "user": "renamed-member", "dailyQuotaGB": 10, "monthlyQuotaGB": -5})
	if code == 0 {
		t.Fatalf("negative quota must error; body=%s", body)
	}
	if err := f.r.DB().Raw(`SELECT user FROM user WHERE id = 2`).Scan(&name).Error; err != nil {
		t.Fatalf("read username: %v", err)
	}
	if name != "member" {
		t.Fatalf("username must stay original when quota validation fails, got %q", name)
	}
}

func TestR2B7_036_GroupPermissionAssignRejectsBeforeInsert(t *testing.T) {
	// 代码顺序断言的动态面：假组 ID 必须在 Insert 之前被拒（零落行）。
	// 故障注入验证未做（见批报告），关闭证据限于本用例 + 预读代码顺序。
	f := seedB2Fixture(t)
	code, body := f.post(t, f.h.groupPermissionAssign, map[string]interface{}{"userGroupId": 99998, "tunnelGroupId": 1})
	if code == 0 {
		t.Fatalf("phantom user group must error before insert; body=%s", body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM group_permission WHERE user_group_id = 99998`); n != 0 {
		t.Fatalf("no permission row may be inserted, got %d", n)
	}
}
