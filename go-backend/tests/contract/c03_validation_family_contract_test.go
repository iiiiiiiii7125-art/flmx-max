package contract_test

import (
	"strings"
	"testing"
	"time"

	"go-backend/internal/auth"
)

// C03 校验族回归（Round 1 分诊簇 C03）：写侧合法集/在场数值校验与
// 口径统一。覆盖 B02-001、B02-002、B02-004、B02-005、B03-001、
// B03-002、B03-003（permission/remove 子项）、B06-001、B09-001、
// B09-002、B04-003。修复前每条断言对应的坏请求均 code=0 落库或
// 回吐 DB 原文。

func c03AdminToken(t *testing.T, secret string) string {
	t.Helper()
	token, err := auth.GenerateToken(1, "admin@user", 0, secret)
	if err != nil {
		t.Fatalf("generate admin token: %v", err)
	}
	return token
}

// B02-001：user/create 对不可解析的数值字段（配额尤甚）必须报错
// 不落库；合法配额必须真落 user_quota 行。
func TestC03UserCreateBadNumericRejected(t *testing.T) {
	secret := "c03-secret"
	router, r := setupContractRouter(t, secret)
	admin := c03AdminToken(t, secret)

	for _, tc := range []struct {
		name string
		body map[string]interface{}
	}{
		{"quota-string", map[string]interface{}{"user": "c03-badquota", "pwd": "secret1", "dailyQuotaGB": "abc"}},
		{"quota-float", map[string]interface{}{"user": "c03-badquota2", "pwd": "secret1", "monthlyQuotaGB": 1.5}},
		{"num-string", map[string]interface{}{"user": "c03-badnum", "pwd": "secret1", "num": "many"}},
	} {
		out := requestContractEnvelope(t, router, admin, "/api/v1/user/create", tc.body)
		if out.Code == 0 {
			t.Fatalf("%s: create with unparsable numeric must fail, got %+v", tc.name, out)
		}
		if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM user WHERE user = ?`, tc.body["user"]); n != 0 {
			t.Fatalf("%s: rejected create must not persist a user row, got %d", tc.name, n)
		}
	}

	// 合法配额：创建成功且配额行真落库（旧 bug 是静默不落库）。
	out := requestContractEnvelope(t, router, admin, "/api/v1/user/create",
		map[string]interface{}{"user": "c03-goodquota", "pwd": "secret1", "dailyQuotaGB": 5, "monthlyQuotaGB": 50})
	if out.Code != 0 {
		t.Fatalf("valid quota create must succeed: %+v", out)
	}
	uid := mustQueryInt64(t, r, `SELECT id FROM user WHERE user = 'c03-goodquota'`)
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM user_quota WHERE user_id = ?`, uid); n != 1 {
		t.Fatalf("valid quota must persist exactly one user_quota row, got %d", n)
	}
}

// B02-002：user/update 改密补最小长度（与 create 的 ≥6 同口径）。
func TestC03UserUpdateShortPasswordRejected(t *testing.T) {
	secret := "c03-secret"
	router, r := setupContractRouter(t, secret)
	admin := c03AdminToken(t, secret)

	out := requestContractEnvelope(t, router, admin, "/api/v1/user/create",
		map[string]interface{}{"user": "c03-upd", "pwd": "secret1"})
	if out.Code != 0 {
		t.Fatalf("seed create: %+v", out)
	}
	uid := mustQueryInt64(t, r, `SELECT id FROM user WHERE user = 'c03-upd'`)

	out = requestContractEnvelope(t, router, admin, "/api/v1/user/update",
		map[string]interface{}{"id": uid, "user": "c03-upd", "pwd": "123"})
	if out.Code == 0 {
		t.Fatalf("3-char password via update must be rejected, got %+v", out)
	}
	out = requestContractEnvelope(t, router, admin, "/api/v1/user/update",
		map[string]interface{}{"id": uid, "user": "c03-upd", "pwd": "123456"})
	if out.Code != 0 {
		t.Fatalf("6-char password via update must succeed: %+v", out)
	}
}

// B02-004：user/groups 查不存在用户报错（不再 code=0 空列表）。
func TestC03UserGroupsPhantomUserRejected(t *testing.T) {
	secret := "c03-secret"
	router, r := setupContractRouter(t, secret)
	admin := c03AdminToken(t, secret)

	out := requestContractEnvelope(t, router, admin, "/api/v1/user/groups",
		map[string]interface{}{"id": 999999})
	if out.Code == 0 {
		t.Fatalf("groups of phantom user must fail, got %+v", out)
	}
	if !strings.Contains(out.Msg, "用户不存在") {
		t.Fatalf("error must name 用户不存在, got %q", out.Msg)
	}

	out = requestContractEnvelope(t, router, admin, "/api/v1/user/create",
		map[string]interface{}{"user": "c03-grp", "pwd": "secret1"})
	if out.Code != 0 {
		t.Fatalf("seed create: %+v", out)
	}
	uid := mustQueryInt64(t, r, `SELECT id FROM user WHERE user = 'c03-grp'`)
	out = requestContractEnvelope(t, router, admin, "/api/v1/user/groups",
		map[string]interface{}{"id": uid})
	if out.Code != 0 {
		t.Fatalf("groups of real user must succeed: %+v", out)
	}
}

// B02-005：user/quota/reset 非法 scope 前置校验、人话报错（不透
// repo 英文内部错误、不回 -2 原文）。
func TestC03QuotaResetBadScopeRejected(t *testing.T) {
	secret := "c03-secret"
	router, r := setupContractRouter(t, secret)
	admin := c03AdminToken(t, secret)

	now := time.Now().UnixMilli()
	uid, err := r.CreateUser("c03-quota-user", "hash", 1, 0, 0, 0, 10, 1, 0, now)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	out := requestContractEnvelope(t, router, admin, "/api/v1/user/quota/reset",
		map[string]interface{}{"userId": uid, "scope": "weekly"})
	if out.Code == 0 {
		t.Fatalf("illegal scope must be rejected, got %+v", out)
	}
	if strings.Contains(out.Msg, "unsupported quota reset scope") {
		t.Fatalf("error must not leak the repo's English internal message, got %q", out.Msg)
	}
	if !strings.Contains(out.Msg, "重置范围无效") {
		t.Fatalf("error must be the friendly scope message, got %q", out.Msg)
	}
	out = requestContractEnvelope(t, router, admin, "/api/v1/user/quota/reset",
		map[string]interface{}{"userId": uid, "scope": "Daily"})
	if out.Code != 0 {
		t.Fatalf("case-insensitive valid scope must succeed: %+v", out)
	}
}

// B03-001：group assign 非正成员 ID 整单拒绝且成员/授权不变（用户
// 侧与隧道侧、混合数组、0 与负数）。
func TestC03GroupAssignNonPositiveIDsRejected(t *testing.T) {
	secret := "c03-secret"
	router, r := setupContractRouter(t, secret)
	admin := c03AdminToken(t, secret)
	now := time.Now().UnixMilli()

	if err := r.DB().Exec(`INSERT INTO user(id, user, pwd, role_id, exp_time, flow, in_flow, out_flow, flow_reset_time, num, created_time, updated_time, status) VALUES(200, 'c03-member', 'x', 1, 2727251700000, 99999, 0, 0, 1, 99999, ?, ?, 1)`, now, now).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if err := r.DB().Exec(`INSERT INTO user_group(name, created_time, updated_time, status) VALUES('c03-ug', ?, ?, 1)`, now, now).Error; err != nil {
		t.Fatalf("insert user_group: %v", err)
	}
	ugID := mustLastInsertID(t, r, "c03-ug")
	if err := r.DB().Exec(`INSERT INTO tunnel(name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, in_ip, inx) VALUES('c03-tun', 1.0, 1, 'tls', 99999, ?, ?, 1, NULL, 0)`, now, now).Error; err != nil {
		t.Fatalf("insert tunnel: %v", err)
	}
	tunID := mustLastInsertID(t, r, "c03-tun")
	if err := r.DB().Exec(`INSERT INTO tunnel_group(name, created_time, updated_time, status) VALUES('c03-tg', ?, ?, 1)`, now, now).Error; err != nil {
		t.Fatalf("insert tunnel_group: %v", err)
	}
	tgID := mustLastInsertID(t, r, "c03-tg")

	// 先合法分配，建立基线成员。
	out := requestContractEnvelope(t, router, admin, "/api/v1/group/user/assign",
		map[string]interface{}{"groupId": ugID, "userIds": []int64{200}})
	if out.Code != 0 {
		t.Fatalf("baseline user assign: %+v", out)
	}
	out = requestContractEnvelope(t, router, admin, "/api/v1/group/tunnel/assign",
		map[string]interface{}{"groupId": tgID, "tunnelIds": []int64{tunID}})
	if out.Code != 0 {
		t.Fatalf("baseline tunnel assign: %+v", out)
	}

	for _, ids := range [][]int64{{-5}, {0}, {200, -5}} {
		out = requestContractEnvelope(t, router, admin, "/api/v1/group/user/assign",
			map[string]interface{}{"groupId": ugID, "userIds": ids})
		if out.Code == 0 {
			t.Fatalf("user assign %v must be rejected, got success", ids)
		}
		if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM user_group_user WHERE user_group_id = ? AND user_id = 200`, ugID); n != 1 {
			t.Fatalf("user assign %v must not clear members, member rows=%d", ids, n)
		}
	}
	for _, ids := range [][]int64{{-5}, {0}} {
		out = requestContractEnvelope(t, router, admin, "/api/v1/group/tunnel/assign",
			map[string]interface{}{"groupId": tgID, "tunnelIds": ids})
		if out.Code == 0 {
			t.Fatalf("tunnel assign %v must be rejected, got success", ids)
		}
		if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM tunnel_group_tunnel WHERE tunnel_group_id = ? AND tunnel_id = ?`, tgID, tunID); n != 1 {
			t.Fatalf("tunnel assign %v must not clear members, member rows=%d", ids, n)
		}
	}
}

// B03-002：分组重名（创建与改名）回人话错误，不泄漏 DB 原文。
func TestC03GroupDuplicateNameFriendlyError(t *testing.T) {
	secret := "c03-secret"
	router, r := setupContractRouter(t, secret)
	admin := c03AdminToken(t, secret)

	out := requestContractEnvelope(t, router, admin, "/api/v1/group/user/create",
		map[string]interface{}{"name": "c03-dup", "status": 1})
	if out.Code != 0 {
		t.Fatalf("first create: %+v", out)
	}
	out = requestContractEnvelope(t, router, admin, "/api/v1/group/user/create",
		map[string]interface{}{"name": "c03-dup", "status": 1})
	if out.Code == 0 {
		t.Fatalf("duplicate create must fail, got %+v", out)
	}
	if out.Msg != "分组名称已存在" {
		t.Fatalf("duplicate create must return the friendly message, got %q", out.Msg)
	}
	if strings.Contains(out.Msg, "UNIQUE") || strings.Contains(out.Msg, "constraint") || strings.Contains(out.Msg, "2067") {
		t.Fatalf("error must not leak DB internals, got %q", out.Msg)
	}

	// 改名撞重名同口径。
	out = requestContractEnvelope(t, router, admin, "/api/v1/group/user/create",
		map[string]interface{}{"name": "c03-other", "status": 1})
	if out.Code != 0 {
		t.Fatalf("second group create: %+v", out)
	}
	otherID := mustQueryInt64(t, r, `SELECT id FROM user_group WHERE name = 'c03-other'`)
	out = requestContractEnvelope(t, router, admin, "/api/v1/group/user/update",
		map[string]interface{}{"id": otherID, "name": "c03-dup"})
	if out.Code == 0 {
		t.Fatalf("rename to existing name must fail, got %+v", out)
	}
	if out.Msg != "分组名称已存在" {
		t.Fatalf("rename duplicate must return the friendly message, got %q", out.Msg)
	}
	if name := mustQueryInt(t, r, `SELECT COUNT(*) FROM user_group WHERE name = 'c03-dup'`); name != 1 {
		t.Fatalf("exactly one group may hold the name, got %d", name)
	}

	// 隧道分组同形。
	out = requestContractEnvelope(t, router, admin, "/api/v1/group/tunnel/create",
		map[string]interface{}{"name": "c03-tdup", "status": 1})
	if out.Code != 0 {
		t.Fatalf("tunnel group create: %+v", out)
	}
	out = requestContractEnvelope(t, router, admin, "/api/v1/group/tunnel/create",
		map[string]interface{}{"name": "c03-tdup", "status": 1})
	if out.Code == 0 || out.Msg != "分组名称已存在" {
		t.Fatalf("tunnel group duplicate must return the friendly message, got %+v", out)
	}
}

// B03-003（拆分项）：permission/remove 对不存在记录报错；存在记录
// 正常移除。分组单删幂等属既定语义，不在本测试范围。
func TestC03GroupPermissionRemoveNonexistentRejected(t *testing.T) {
	secret := "c03-secret"
	router, r := setupContractRouter(t, secret)
	admin := c03AdminToken(t, secret)
	now := time.Now().UnixMilli()

	out := requestContractEnvelope(t, router, admin, "/api/v1/group/permission/remove",
		map[string]interface{}{"id": 88888})
	if out.Code == 0 {
		t.Fatalf("remove of nonexistent permission must fail, got %+v", out)
	}

	if err := r.DB().Exec(`INSERT INTO user_group(name, created_time, updated_time, status) VALUES('c03-pug', ?, ?, 1)`, now, now).Error; err != nil {
		t.Fatalf("insert user_group: %v", err)
	}
	ugID := mustLastInsertID(t, r, "c03-pug")
	if err := r.DB().Exec(`INSERT INTO tunnel_group(name, created_time, updated_time, status) VALUES('c03-ptg', ?, ?, 1)`, now, now).Error; err != nil {
		t.Fatalf("insert tunnel_group: %v", err)
	}
	tgID := mustLastInsertID(t, r, "c03-ptg")
	if err := r.DB().Exec(`INSERT INTO group_permission(user_group_id, tunnel_group_id, created_time) VALUES(?, ?, ?)`, ugID, tgID, now).Error; err != nil {
		t.Fatalf("insert group_permission: %v", err)
	}
	permID := mustQueryInt64(t, r, `SELECT id FROM group_permission WHERE user_group_id = ? AND tunnel_group_id = ?`, ugID, tgID)

	out = requestContractEnvelope(t, router, admin, "/api/v1/group/permission/remove",
		map[string]interface{}{"id": permID})
	if out.Code != 0 {
		t.Fatalf("remove of existing permission must succeed: %+v", out)
	}
	out = requestContractEnvelope(t, router, admin, "/api/v1/group/permission/remove",
		map[string]interface{}{"id": permID})
	if out.Code == 0 {
		t.Fatalf("second remove of the same permission must fail, got %+v", out)
	}
}

// B06-001：隧道改名与现有隧道同名必须拒绝（此前 code=0 双同名）。
func TestC03TunnelRenameToExistingRejected(t *testing.T) {
	secret := "c03-secret"
	router, r := setupContractRouter(t, secret)
	admin := c03AdminToken(t, secret)
	now := time.Now().UnixMilli()

	for _, name := range []string{"c03-t1", "c03-t2"} {
		if err := r.DB().Exec(`INSERT INTO tunnel(name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, in_ip, inx) VALUES(?, 1.0, 1, 'tls', 99999, ?, ?, 1, NULL, 0)`, name, now, now).Error; err != nil {
			t.Fatalf("insert tunnel %s: %v", name, err)
		}
	}
	t2ID := mustLastInsertID(t, r, "c03-t2")

	out := requestContractEnvelope(t, router, admin, "/api/v1/tunnel/update",
		map[string]interface{}{"id": t2ID, "name": "c03-t1"})
	if out.Code == 0 {
		t.Fatalf("rename to an existing tunnel name must fail, got %+v", out)
	}
	if !strings.Contains(out.Msg, "隧道名称重复") {
		t.Fatalf("error must name 隧道名称重复, got %q", out.Msg)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM tunnel WHERE name = 'c03-t1'`); n != 1 {
		t.Fatalf("exactly one tunnel may hold the name, got %d", n)
	}

	// 对照：原名提交（同名即自身）不得被判重名。
	out = requestContractEnvelope(t, router, admin, "/api/v1/tunnel/update",
		map[string]interface{}{"id": t2ID, "name": "c03-t2"})
	if strings.Contains(out.Msg, "隧道名称重复") {
		t.Fatalf("keeping the tunnel's own name must not be a duplicate, got %+v", out)
	}
}

// B09-001：限速 speed/status 合法集在 create/update 两面拒绝；
// rename-only 更新不连坐。B09-002：转发 maxConn 负值拒绝。
func TestC03SpeedLimitLegalSetEnforced(t *testing.T) {
	secret := "c03-secret"
	router, r := setupContractRouter(t, secret)
	admin := c03AdminToken(t, secret)

	for _, tc := range []struct {
		name string
		body map[string]interface{}
	}{
		{"speed-zero", map[string]interface{}{"name": "c03-sl-a", "speed": 0, "status": 1}},
		{"speed-negative", map[string]interface{}{"name": "c03-sl-b", "speed": -5, "status": 1}},
		{"status-illegal", map[string]interface{}{"name": "c03-sl-c", "speed": 100, "status": 7}},
		{"speed-unparsable", map[string]interface{}{"name": "c03-sl-d", "speed": "abc", "status": 1}},
	} {
		out := requestContractEnvelope(t, router, admin, "/api/v1/speed-limit/create", tc.body)
		if out.Code == 0 {
			t.Fatalf("%s: illegal speed limit must be rejected, got %+v", tc.name, out)
		}
		if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM speed_limit WHERE name = ?`, tc.body["name"]); n != 0 {
			t.Fatalf("%s: rejected rule must not persist, got %d rows", tc.name, n)
		}
	}

	out := requestContractEnvelope(t, router, admin, "/api/v1/speed-limit/create",
		map[string]interface{}{"name": "c03-sl-ok", "speed": 100, "status": 1})
	if out.Code != 0 {
		t.Fatalf("valid speed limit create: %+v", out)
	}
	slID := mustQueryInt64(t, r, `SELECT id FROM speed_limit WHERE name = 'c03-sl-ok'`)

	out = requestContractEnvelope(t, router, admin, "/api/v1/speed-limit/update",
		map[string]interface{}{"id": slID, "speed": -99})
	if out.Code == 0 {
		t.Fatalf("update to speed=-99 must be rejected, got %+v", out)
	}
	out = requestContractEnvelope(t, router, admin, "/api/v1/speed-limit/update",
		map[string]interface{}{"id": slID, "status": 7})
	if out.Code == 0 {
		t.Fatalf("update to status=7 must be rejected, got %+v", out)
	}
	if sp := mustQueryInt(t, r, `SELECT speed FROM speed_limit WHERE id = ?`, slID); sp != 100 {
		t.Fatalf("rejected updates must not change stored speed, got %d", sp)
	}
	// rename-only：不带 speed/status 不得被合法集连坐拒绝。
	out = requestContractEnvelope(t, router, admin, "/api/v1/speed-limit/update",
		map[string]interface{}{"id": slID, "name": "c03-sl-ok-renamed"})
	if out.Code != 0 {
		t.Fatalf("rename-only update must succeed: %+v", out)
	}
}

// B09-002：转发 create/update 的 maxConn 负值与用户面 O5-006 同口
// 径拒绝（此前原样落库、静默等价无限制）。
func TestC03ForwardNegativeMaxConnRejected(t *testing.T) {
	secret := "c03-secret"
	router, r := setupContractRouter(t, secret)
	admin := c03AdminToken(t, secret)
	now := time.Now().UnixMilli()

	tunnelID, _ := seedTunnelDeleteTunnelWithNode(t, r, now, "c03-mc-tunnel", "c03-mc-node", "22000-22010")

	out := requestContractEnvelope(t, router, admin, "/api/v1/forward/create",
		map[string]interface{}{"tunnelId": tunnelID, "name": "c03-mc-bad", "remoteAddr": "203.0.113.5:80", "maxConn": -1})
	if out.Code == 0 {
		t.Fatalf("create with maxConn=-1 must be rejected, got %+v", out)
	}
	if !strings.Contains(out.Msg, "连接数限制不能小于0") {
		t.Fatalf("error must mirror the O5-006 message, got %q", out.Msg)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM forward WHERE name = 'c03-mc-bad'`); n != 0 {
		t.Fatalf("rejected create must not persist a forward, got %d", n)
	}

	out = requestContractEnvelope(t, router, admin, "/api/v1/forward/create",
		map[string]interface{}{"tunnelId": tunnelID, "name": "c03-mc-ok", "remoteAddr": "203.0.113.5:80", "maxConn": 5})
	if out.Code != 0 {
		t.Fatalf("valid create: %+v", out)
	}
	fwdID := mustQueryInt64(t, r, `SELECT id FROM forward WHERE name = 'c03-mc-ok'`)
	port := mustQueryInt(t, r, `SELECT port FROM forward_port WHERE forward_id = ?`, fwdID)

	out = requestContractEnvelope(t, router, admin, "/api/v1/forward/update",
		map[string]interface{}{"id": fwdID, "port": port, "maxConn": -2})
	if out.Code == 0 {
		t.Fatalf("update with maxConn=-2 must be rejected, got %+v", out)
	}
	if mc := mustQueryInt(t, r, `SELECT max_conn FROM forward WHERE id = ?`, fwdID); mc != 5 {
		t.Fatalf("rejected update must not change stored max_conn, got %d", mc)
	}
}

// B04-003：节点 port 写侧校验——不可解析/越界/反序在 create 与
// update 两面拒绝，合法值原样落库。
func TestC03NodePortSpecValidatedOnWrite(t *testing.T) {
	secret := "c03-secret"
	router, r := setupContractRouter(t, secret)
	admin := c03AdminToken(t, secret)

	for _, tc := range []struct {
		name string
		port string
	}{
		{"unparsable", "abc"},
		{"reversed", "5000-100"},
		{"out-of-range", "99999-100000"},
		{"zero", "0"},
	} {
		out := requestContractEnvelope(t, router, admin, "/api/v1/node/create",
			map[string]interface{}{"name": "c03-node-" + tc.name, "serverIp": "10.0.0.9", "port": tc.port})
		if out.Code == 0 {
			t.Fatalf("%s: node create with port %q must be rejected, got %+v", tc.name, tc.port, out)
		}
		if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM node WHERE name = ?`, "c03-node-"+tc.name); n != 0 {
			t.Fatalf("%s: rejected create must not persist a node, got %d", tc.name, n)
		}
	}

	out := requestContractEnvelope(t, router, admin, "/api/v1/node/create",
		map[string]interface{}{"name": "c03-node-ok", "serverIp": "10.0.0.9", "port": "1000-2000,3000"})
	if out.Code != 0 {
		t.Fatalf("valid port spec create: %+v", out)
	}
	nodeID := mustQueryInt64(t, r, `SELECT id FROM node WHERE name = 'c03-node-ok'`)
	if p := mustQueryInt(t, r, `SELECT COUNT(*) FROM node WHERE id = ? AND port = '1000-2000,3000'`, nodeID); p != 1 {
		t.Fatalf("valid port spec must persist verbatim")
	}

	out = requestContractEnvelope(t, router, admin, "/api/v1/node/update",
		map[string]interface{}{"id": nodeID, "port": "1-99999"})
	if out.Code == 0 {
		t.Fatalf("node update with out-of-range port must be rejected, got %+v", out)
	}
	if !strings.Contains(out.Msg, "端口") {
		t.Fatalf("update rejection must name the port problem, got %q", out.Msg)
	}
	if p := mustQueryInt(t, r, `SELECT COUNT(*) FROM node WHERE id = ? AND port = '1000-2000,3000'`, nodeID); p != 1 {
		t.Fatalf("rejected update must not change stored port")
	}
}
