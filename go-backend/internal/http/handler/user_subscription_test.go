package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/auth"
	"go-backend/internal/http/middleware"
	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// 1.0.12: /user-subscription/mine 只返回当前用户自己的分配。
func TestUserSubscriptionMineReturnsOnlyOwn(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/usersub_mine.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	now := time.Now().UnixMilli()

	mkSub := func(token, name string) *model.Subscription {
		s := &model.Subscription{Name: name, Kind: model.SubscriptionShare, Token: token,
			UUID: "u-" + token[:2], Password: "p", Enabled: 1, TrafficLimit: 1000, CreatedTime: now, UpdatedTime: now}
		if err := r.SaveSubscription(s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	mine := mkSub(strings.Repeat("a", 32), "我的订阅")
	other := mkSub(strings.Repeat("b", 32), "别人的订阅")

	if err := r.SetUserSubscriptions(2, []int64{mine.ID}); err != nil {
		t.Fatal(err)
	}
	if err := r.SetUserSubscriptions(3, []int64{other.ID}); err != nil {
		t.Fatal(err)
	}

	callMine := func(userID int64) (int, []userSubscriptionMineView) {
		req := httptest.NewRequest("POST", "/api/v1/user-subscription/mine", strings.NewReader("{}"))
		ctx := context.WithValue(req.Context(), middleware.ClaimsContextKey,
			auth.Claims{Sub: fmt.Sprint(userID), RoleID: 1})
		rec := httptest.NewRecorder()
		h.userSubscriptionMine(rec, req.WithContext(ctx))
		var env struct {
			Code int `json:"code"`
			Data struct {
				Subscriptions []userSubscriptionMineView `json:"subscriptions"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode mine response: %v (body=%s)", err, rec.Body.String())
		}
		return env.Code, env.Data.Subscriptions
	}

	code, items := callMine(2)
	if code != 0 || len(items) != 1 {
		t.Fatalf("user 2 mine: code=%d items=%+v", code, items)
	}
	if items[0].ID != mine.ID || items[0].Name != "我的订阅" ||
		items[0].Path != "/api/v1/open_api/sub/"+mine.Token ||
		items[0].TrafficLimit != 1000 || !items[0].Enabled {
		t.Fatalf("unexpected mine view: %+v", items[0])
	}

	code, items = callMine(3)
	if code != 0 || len(items) != 1 || items[0].ID != other.ID {
		t.Fatalf("user 3 mine: code=%d items=%+v", code, items)
	}

	code, items = callMine(9)
	if code != 0 || len(items) != 0 {
		t.Fatalf("user 9 (no assignments) mine: code=%d items=%+v", code, items)
	}
}

// 1.0.12: /user-subscription/set 非管理员 403；不存在的订阅 ID 报错不静默丢弃。
func TestUserSubscriptionSetGuards(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/usersub_set.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	now := time.Now().UnixMilli()
	sub := &model.Subscription{Name: "s", Kind: model.SubscriptionShare, Token: strings.Repeat("a", 32),
		UUID: "u", Password: "p", Enabled: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.SaveSubscription(sub); err != nil {
		t.Fatal(err)
	}
	// R2X-012 后果：分配目标用户必须真实存在（此前夹具以不存在的
	// userId=2 走通成功路径，与新口径冲突，故补建真实用户；断言不变）。
	if err := r.DB().Exec(`INSERT INTO user(id, user, pwd, role_id, exp_time, flow, in_flow, out_flow, flow_reset_time, num, created_time, status) VALUES(2, 'member', 'pwd', 1, 0, 100, 0, 0, 0, 10, 1000, 1)`).Error; err != nil {
		t.Fatal(err)
	}

	callSet := func(roleID int, body string) (int, string) {
		req := httptest.NewRequest("POST", "/api/v1/user-subscription/set", strings.NewReader(body))
		ctx := context.WithValue(req.Context(), middleware.ClaimsContextKey,
			auth.Claims{Sub: "1", RoleID: roleID})
		rec := httptest.NewRecorder()
		h.userSubscriptionSet(rec, req.WithContext(ctx))
		var env struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &env)
		return env.Code, env.Msg
	}

	if code, _ := callSet(1, fmt.Sprintf(`{"userId":2,"subscriptionIds":[%d]}`, sub.ID)); code != 403 {
		t.Fatalf("non-admin set should be 403, got code=%d", code)
	}
	if code, msg := callSet(0, `{"userId":2,"subscriptionIds":[99999]}`); code == 0 || !strings.Contains(msg, "订阅不存在") {
		t.Fatalf("nonexistent subscription should error, got code=%d msg=%q", code, msg)
	}
	if code, _ := callSet(0, fmt.Sprintf(`{"userId":2,"subscriptionIds":[%d]}`, sub.ID)); code != 0 {
		t.Fatalf("admin set should succeed, got code=%d", code)
	}
	if ids, _ := r.ListUserSubscriptionIDs(2); len(ids) != 1 || ids[0] != sub.ID {
		t.Fatalf("assignment not persisted: %v", ids)
	}
	if code, _ := callSet(0, `{"userId":0,"subscriptionIds":[]}`); code == 0 {
		t.Fatalf("userId<=0 should error, got code=%d", code)
	}
}

// 1.0.12: 中转范围过滤的白名单语义（纯逻辑全分支）。
func TestFilterEndpointsByRelayIDs(t *testing.T) {
	mk := func() []kernel.Endpoint {
		return []kernel.Endpoint{
			{Name: "direct", RelayID: 0, Direct: true},
			{Name: "relay2", RelayID: 2},
			{Name: "relay3", RelayID: 3},
		}
	}
	if got := filterEndpointsByRelayIDs(mk(), nil); len(got) != 3 {
		t.Fatalf("nil filter must keep all endpoints, got %d", len(got))
	}
	if got := filterEndpointsByRelayIDs(mk(), []int64{}); len(got) != 3 {
		t.Fatalf("empty filter must keep all endpoints, got %d", len(got))
	}
	got := filterEndpointsByRelayIDs(mk(), []int64{2})
	if len(got) != 1 || got[0].RelayID != 2 {
		t.Fatalf("filter [2] must keep only relay 2, got %+v", got)
	}
	got = filterEndpointsByRelayIDs(mk(), []int64{0, 2})
	if len(got) != 2 || got[0].RelayID != 0 || got[1].RelayID != 2 {
		t.Fatalf("filter [0,2] must keep direct+relay2, got %+v", got)
	}
	if got := filterEndpointsByRelayIDs(mk(), []int64{0}); len(got) != 1 || got[0].RelayID != 0 {
		t.Fatalf("filter [0] must keep only direct, got %+v", got)
	}
}

// 1.0.12: subEndpoints 实际应用 RelayIDs（真实入站夹具，直连端点）。
func TestSubEndpointsAppliesRelayIDs(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/usersub_filter.db")
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
	if err := r.DB().Exec(`INSERT INTO node_inbound(kernel_id, node_id, name, protocol, listen, port, settings, enabled, created_time, updated_time)
		VALUES(?, ?, 'a', 'trojan', '::', 443, '{"password":"owner-pw"}', 1, ?, ?)`, kernelID, nodeID, now, now).Error; err != nil {
		t.Fatal(err)
	}
	inboundID := mustLastInsertID(t, r, "inbound")
	rows, err := r.ListNodeInbounds()
	if err != nil {
		t.Fatal(err)
	}

	mkSub := func(options string) *model.Subscription {
		return &model.Subscription{Name: "x", Kind: model.SubscriptionShare, Token: strings.Repeat("a", 32),
			UUID: "u", Password: "p", InboundIDs: fmt.Sprint(inboundID), Options: options, Enabled: 1}
	}

	if got := h.subEndpoints(mkSub(""), rows); len(got) != 1 || got[0].RelayID != 0 {
		t.Fatalf("no filter: want 1 direct endpoint, got %+v", got)
	}
	if got := h.subEndpoints(mkSub(`{"relayIds":[0]}`), rows); len(got) != 1 {
		t.Fatalf("filter [0]: want the direct endpoint, got %+v", got)
	}
	if got := h.subEndpoints(mkSub(`{"relayIds":[2]}`), rows); len(got) != 0 {
		t.Fatalf("filter [2]: direct endpoint must be filtered out, got %+v", got)
	}
}

// 1.0.12: subPipeline.normalize 归一化 RelayIDs（去重、剔除负数、保留 0）。
func TestSubPipelineNormalizeRelayIDs(t *testing.T) {
	p := subPipeline{RelayIDs: []int64{2, -1, 2, 0, -5, 3}}
	if err := p.normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	want := []int64{2, 0, 3}
	if len(p.RelayIDs) != len(want) {
		t.Fatalf("normalized relayIds = %v, want %v", p.RelayIDs, want)
	}
	for i := range want {
		if p.RelayIDs[i] != want[i] {
			t.Fatalf("normalized relayIds = %v, want %v", p.RelayIDs, want)
		}
	}
}

// 1.0.12: subscriptionView 暴露 relayIds 与 isDefault 字段形态。
func TestSubscriptionListMarksDefault(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/usersub_default.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	now := time.Now().UnixMilli()
	for _, tok := range []string{strings.Repeat("a", 32), strings.Repeat("b", 32)} {
		s := &model.Subscription{Name: "s", Kind: model.SubscriptionShare, Token: tok,
			UUID: "u", Password: "p", Enabled: 1, CreatedTime: now, UpdatedTime: now}
		if err := r.SaveSubscription(s); err != nil {
			t.Fatal(err)
		}
	}
	// 新库打开时可能已种入一条默认订阅，以 repo 的默认 ID 为准断言。
	def, err := r.DefaultSubscription(now)
	if err != nil || def == nil {
		t.Fatalf("DefaultSubscription: %v", err)
	}
	req := httptest.NewRequest("POST", "/api/v1/subscription/list", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	h.subscriptionList(rec, req)
	var env struct {
		Code int `json:"code"`
		Data struct {
			Items []subscriptionView `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode list response: %v (body=%s)", err, rec.Body.String())
	}
	if env.Code != 0 || len(env.Data.Items) == 0 {
		t.Fatalf("list: code=%d items=%d", env.Code, len(env.Data.Items))
	}
	defaults := 0
	for _, it := range env.Data.Items {
		if it.IsDefault {
			defaults++
			if it.ID != def.ID {
				t.Fatalf("isDefault marked on id=%d, want default id=%d", it.ID, def.ID)
			}
		}
		if it.RelayIDs == nil {
			t.Fatalf("relayIds must serialize as [] not null: %+v", it)
		}
	}
	if defaults != 1 {
		t.Fatalf("exactly one subscription must be marked default, got %d", defaults)
	}
}
