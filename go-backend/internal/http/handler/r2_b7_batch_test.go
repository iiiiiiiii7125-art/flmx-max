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

// R2 批 7 回归：分配引用完整性（R2X-012）/ 校验前置（R2X-036）/
// 删除幂等回执（R2X-048）。分组三件套已于 1.9.6 整套移除，原
// R2X-013 分组停用执法与 R2X-035 UT 单删台账两族随功能删除。
// 复用 b2_fixture_test.go 的 seedB2Fixture（UT10–UT13、forward20/21）。

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

// ─── R2X-035 + R2X-048（联动） ─────────────────────────────────────────────

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
	// 配额校验在首写之前：负配额 + 改名 → 报错且不落库。
	var name string
	code, body := f.post(t, f.h.userUpdate, map[string]interface{}{"id": 2, "user": "renamed-member", "dailyQuotaGB": 10, "monthlyQuotaGB": -5})
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
