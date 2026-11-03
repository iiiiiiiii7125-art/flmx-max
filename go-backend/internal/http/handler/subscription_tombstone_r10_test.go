package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/auth"
	"go-backend/internal/http/middleware"
	"go-backend/internal/http/response"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R10-02/R10-03/S7（I-F3-01/I-F3-02/I-F3-06）：订阅删除墓碑必须是吸收态。
// 墓碑（pending_delete=1）行在删除完成前：
//   - subscriptionSave / subscriptionReset / subscriptionResetTraffic
//     一律拒绝并点名「删除中」，不得把墓碑改回启用；
//   - 内核 payload 来源 rawActiveUsersByInbound 恒排除墓碑（纵深防御，
//     即使墓碑行被任何路径写成 Enabled=1 也不下发凭据）；
//   - subscriptionView 透出 pendingDelete，前端列表据此呈「删除中」态；
//   - resetTraffic 回读为空（请求内删除已完成）时按已删除收口、不 panic；
//   - userSubscriptionSet 不得把墓碑订阅分配给用户（点名拒绝）。

func seedTombstoneFixture(t *testing.T) (*repo.Repository, *Handler, int64) {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/tombstone_r10.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	now := time.Now().UnixMilli()
	if err := r.DB().Exec(`INSERT INTO node(name, secret, server_ip, port, created_time, status) VALUES('n', 's', '203.0.113.1', '1000-2000', ?, 1)`, now).Error; err != nil {
		t.Fatal(err)
	}
	nodeID := mustLastInsertID(t, r, "node")
	if err := r.DB().Exec(`INSERT INTO node_kernel(node_id, kernel_type, version, status, created_time, updated_time) VALUES(?, 'xray', 'latest', 'pending', ?, ?)`, nodeID, now, now).Error; err != nil {
		t.Fatal(err)
	}
	kernelID := mustLastInsertID(t, r, "kernel")
	if err := r.DB().Exec(`INSERT INTO node_inbound(kernel_id, node_id, name, protocol, listen, port, settings, created_time, updated_time)
		VALUES(?, ?, 'a', 'trojan', '::', 443, '{"password":"owner-pw"}', ?, ?)`, kernelID, nodeID, now, now).Error; err != nil {
		t.Fatal(err)
	}
	inboundID := mustLastInsertID(t, r, "inbound")
	return r, h, inboundID
}

func mkShareSub(t *testing.T, r *repo.Repository, uuid string, sourceAll int) *model.Subscription {
	t.Helper()
	now := time.Now().UnixMilli()
	// Token 必须在同一库内逐订阅唯一：用完整 uuid 派生（旧写法只取
	// uuid[:2]，同测内两个订阅恒撞 subscription.token 唯一约束）。
	tokenSrc := strings.ReplaceAll(uuid, "-", "")
	token := (tokenSrc + strings.Repeat("a", 32))[:32]
	s := &model.Subscription{Name: "tomb-" + uuid, Kind: model.SubscriptionShare, Token: token,
		UUID: uuid, Password: "user-pw", SourceAll: sourceAll, Enabled: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.SaveSubscription(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func payloadHasUUID(h *Handler, uuid string) bool {
	for _, users := range h.rawActiveUsersByInbound(time.Now()) {
		for _, u := range users {
			if u.UUID == uuid {
				return true
			}
		}
	}
	return false
}

func TestR10TombstoneSaveRejectedAndStaysDisabled(t *testing.T) {
	r, h, _ := seedTombstoneFixture(t)
	s := mkShareSub(t, r, "uuid-save", 1)
	if !payloadHasUUID(h, s.UUID) {
		t.Fatal("precondition: active share sub must be in payload")
	}
	if err := r.MarkSubscriptionPendingDelete(s.ID); err != nil {
		t.Fatal(err)
	}
	// 管理员对残留墓碑行点启用保存（基线：写出 Enabled=1∧PendingDelete=1）。
	out := decodeCode(t, postJSONToHandler(t, h.subscriptionSave, map[string]interface{}{
		"id": s.ID, "name": s.Name, "sourceAll": true, "enabled": true,
	}).Body.String())
	if out.Code == 0 {
		t.Fatalf("save on tombstone must be rejected, got code 0: %+v", out)
	}
	if !strings.Contains(out.Msg, "删除中") {
		t.Fatalf("rejection must name 删除中, got msg %q", out.Msg)
	}
	got, err := r.GetSubscription(s.ID)
	if err != nil || got == nil {
		t.Fatalf("tombstone row must survive a rejected save: %v", err)
	}
	if got.Enabled != 0 || got.PendingDelete != 1 {
		t.Fatalf("tombstone must stay disabled+pending, got enabled=%d pending=%d", got.Enabled, got.PendingDelete)
	}
	if payloadHasUUID(h, s.UUID) {
		t.Fatal("tombstone credentials must not be in kernel payload")
	}
}

func TestR10TombstonePayloadExcludesEvenIfForceEnabled(t *testing.T) {
	r, h, _ := seedTombstoneFixture(t)
	s := mkShareSub(t, r, "uuid-force", 1)
	if err := r.MarkSubscriptionPendingDelete(s.ID); err != nil {
		t.Fatal(err)
	}
	// 模拟任何残余写路径把墓碑写成 Enabled=1∧PendingDelete=1（状态机外
	// 组合）：payload 侧必须仍排除——完成路径的前提由构造保证。
	if err := r.DB().Exec(`UPDATE subscription SET enabled = 1 WHERE id = ?`, s.ID).Error; err != nil {
		t.Fatal(err)
	}
	if payloadHasUUID(h, s.UUID) {
		t.Fatal("force-enabled tombstone credentials leaked into kernel payload")
	}
}

func TestR10TombstoneResetRejected(t *testing.T) {
	r, h, _ := seedTombstoneFixture(t)
	s := mkShareSub(t, r, "uuid-reset", 1)
	if err := r.MarkSubscriptionPendingDelete(s.ID); err != nil {
		t.Fatal(err)
	}
	out := decodeCode(t, postJSONToHandler(t, h.subscriptionReset, map[string]interface{}{"id": s.ID}).Body.String())
	if out.Code == 0 {
		t.Fatalf("reset on tombstone must be rejected, got code 0: %+v", out)
	}
	if !strings.Contains(out.Msg, "删除中") {
		t.Fatalf("rejection must name 删除中, got msg %q", out.Msg)
	}
	got, _ := r.GetSubscription(s.ID)
	if got == nil || got.Token != s.Token || got.UUID != s.UUID {
		t.Fatalf("tombstone credentials must not rotate: %+v", got)
	}
}

func TestR10TombstoneResetTrafficRejectedNoPanic(t *testing.T) {
	r, h, _ := seedTombstoneFixture(t)
	// 无入站的 share 订阅：基线下请求内 syncSubscriptions 可当场完成
	// 墓碑删除，随后回读 nil 解引用 panic（I-F3-02）。
	s := mkShareSub(t, r, "uuid-rt", 0)
	if err := r.MarkSubscriptionPendingDelete(s.ID); err != nil {
		t.Fatal(err)
	}
	var out response.R
	panicked := func() (p bool) {
		defer func() {
			if rec := recover(); rec != nil {
				p = true
			}
		}()
		out = decodeCode(t, postJSONToHandler(t, h.subscriptionResetTraffic, map[string]interface{}{"id": s.ID}).Body.String())
		return false
	}()
	if panicked {
		t.Fatal("resetTraffic on tombstone panicked (nil deref after in-request delete)")
	}
	if out.Code == 0 {
		t.Fatalf("resetTraffic on tombstone must be rejected, got code 0: %+v", out)
	}
	if !strings.Contains(out.Msg, "删除中") {
		t.Fatalf("rejection must name 删除中, got msg %q", out.Msg)
	}
}

func TestR10SubscriptionViewExposesPendingDelete(t *testing.T) {
	r, h, _ := seedTombstoneFixture(t)
	live := mkShareSub(t, r, "uuid-live", 1)
	dead := mkShareSub(t, r, "uuid-dead", 1)
	if err := r.MarkSubscriptionPendingDelete(dead.ID); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/subscription/list", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	h.subscriptionList(rec, req)
	var env struct {
		Code int `json:"code"`
		Data struct {
			Items []subscriptionView `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode list: %v (body=%s)", err, rec.Body.String())
	}
	seen := map[int64]bool{}
	for _, it := range env.Data.Items {
		seen[it.ID] = it.PendingDelete
	}
	if v, ok := seen[dead.ID]; !ok || !v {
		t.Fatalf("tombstone item must expose pendingDelete=true: %v", seen)
	}
	if v, ok := seen[live.ID]; !ok || v {
		t.Fatalf("live item must expose pendingDelete=false: %v", seen)
	}
}

func TestR10UserSubscriptionSetRejectsTombstone(t *testing.T) {
	r, h, _ := seedTombstoneFixture(t)
	now := time.Now().UnixMilli()
	_ = now
	if err := r.DB().Exec(`INSERT INTO user(id, user, pwd, role_id, exp_time, flow, in_flow, out_flow, flow_reset_time, num, created_time, status) VALUES(2, 'member', 'pwd', 1, 0, 100, 0, 0, 0, 10, 1000, 1)`).Error; err != nil {
		t.Fatal(err)
	}
	dead := mkShareSub(t, r, "uuid-asn", 1)
	if err := r.MarkSubscriptionPendingDelete(dead.ID); err != nil {
		t.Fatal(err)
	}
	callSet := func(body string) (int, string) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/user-subscription/set", strings.NewReader(body))
		ctx := context.WithValue(req.Context(), middleware.ClaimsContextKey,
			auth.Claims{Sub: "1", RoleID: 0})
		rec := httptest.NewRecorder()
		h.userSubscriptionSet(rec, req.WithContext(ctx))
		var env struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &env)
		return env.Code, env.Msg
	}
	code, msg := callSet(fmt.Sprintf(`{"userId":2,"subscriptionIds":[%d]}`, dead.ID))
	if code == 0 {
		t.Fatalf("assigning a tombstone subscription must be rejected, got code 0")
	}
	if !strings.Contains(msg, "删除中") {
		t.Fatalf("rejection must name 删除中, got msg %q", msg)
	}
	if ids, _ := r.ListUserSubscriptionIDs(2); len(ids) != 0 {
		t.Fatalf("tombstone assignment must not persist: %v", ids)
	}
	// 对照：正常订阅仍可分配。
	live := mkShareSub(t, r, "uuid-asn2", 1)
	if code, msg := callSet(fmt.Sprintf(`{"userId":2,"subscriptionIds":[%d]}`, live.ID)); code != 0 {
		t.Fatalf("assigning a live subscription must succeed, got code=%d msg=%q", code, msg)
	}
}
