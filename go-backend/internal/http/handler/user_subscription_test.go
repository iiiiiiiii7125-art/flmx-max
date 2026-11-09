package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"sort"
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

// 中转范围三档（2.1.2）：渲染分类口径——direct 只出 RelayID==0、
// relay 只出 RelayID!=0、all 全部（kernel.FilterEndpoints 的 Lines 条款）。
func TestFilterEndpointsByScopeLines(t *testing.T) {
	mk := func() []kernel.Endpoint {
		return []kernel.Endpoint{
			{Name: "direct", RelayID: 0, Direct: true},
			{Name: "relay2", RelayID: 2},
			{Name: "relay3", RelayID: 3},
		}
	}
	if got := kernel.FilterEndpoints(mk(), kernel.SubOptions{}); len(got) != 3 {
		t.Fatalf("empty scope must keep all endpoints, got %d", len(got))
	}
	got := kernel.FilterEndpoints(mk(), kernel.SubOptions{Lines: "relay"})
	if len(got) != 2 || got[0].RelayID != 2 || got[1].RelayID != 3 {
		t.Fatalf("relay scope must keep only relayed endpoints, got %+v", got)
	}
	if got := kernel.FilterEndpoints(mk(), kernel.SubOptions{Lines: "direct"}); len(got) != 1 || got[0].RelayID != 0 {
		t.Fatalf("direct scope must keep only the direct endpoint, got %+v", got)
	}
}

// relayIds 白名单（逐端点精确集）：非空时按 RelayID 精确过滤并
// 优先于 lines 三档；[0] 只出直连、单条中转只出该条、混合子集
// 只出子集本身——不再折算放宽到整档。
func TestFilterEndpointsByRelayIDsWhitelist(t *testing.T) {
	mk := func() []kernel.Endpoint {
		return []kernel.Endpoint{
			{Name: "direct", RelayID: 0, Direct: true},
			{Name: "relay2", RelayID: 2},
			{Name: "relay3", RelayID: 3},
		}
	}
	ids := func(eps []kernel.Endpoint) []int64 {
		out := []int64{}
		for _, e := range eps {
			out = append(out, e.RelayID)
		}
		return out
	}
	cases := []struct {
		opts kernel.SubOptions
		want []int64
	}{
		{kernel.SubOptions{RelayIDs: []int64{2}}, []int64{2}},
		{kernel.SubOptions{RelayIDs: []int64{0}}, []int64{0}},
		{kernel.SubOptions{RelayIDs: []int64{0, 3}}, []int64{0, 3}},
		{kernel.SubOptions{RelayIDs: []int64{3, 2}}, []int64{2, 3}},
		// 白名单优先于 lines：lines 说 relay，白名单 [0] 只出直连。
		{kernel.SubOptions{Lines: "relay", RelayIDs: []int64{0}}, []int64{0}},
		{kernel.SubOptions{Lines: "direct", RelayIDs: []int64{2, 3}}, []int64{2, 3}},
		// 空白名单不生效，lines 照常。
		{kernel.SubOptions{Lines: "relay", RelayIDs: []int64{}}, []int64{2, 3}},
	}
	for _, c := range cases {
		got := ids(kernel.FilterEndpoints(mk(), c.opts))
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Fatalf("FilterEndpoints(%+v) relay ids = %v, want %v", c.opts, got, c.want)
		}
	}
}

// 中转范围在 subEndpoints 装配收口的实际应用（真实夹具：一个落地
// 入站 + 两条中转线）：lines 三档直接生效；relayIds 白名单是精确
// 端点集——只挑一条中转线时只出那一条（2.1.2 折算曾放宽成整档
// relay 全出），[0] 只出直连，混合子集只出子集，白名单优先于 lines。
func TestSubEndpointsAppliesRelayIDs(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/usersub_filter.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	now := time.Now().UnixMilli()
	db := r.DB()

	landingNode := seedRelayTargetNode(t, r, "wl-landing", "192.0.2.30")
	row := seedShareInbound(t, r, landingNode, "wl-in")

	mkRelay := func(name string, entryIP string, port int) int64 {
		entry := seedRelayTargetNode(t, r, name+"-entry", entryIP)
		tunnel := model.Tunnel{Name: name, Type: 1, Protocol: "tcp", Flow: 99999, Status: 1, CreatedTime: now, UpdatedTime: now}
		if err := db.Create(&tunnel).Error; err != nil {
			t.Fatal(err)
		}
		fwd := model.Forward{UserID: 1, UserName: "admin", Name: name, TunnelID: tunnel.ID, RemoteAddr: "203.0.113.1:443", Status: 1, CreatedTime: now, UpdatedTime: now}
		if err := db.Create(&fwd).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.ForwardPort{ForwardID: fwd.ID, NodeID: entry, Port: port}).Error; err != nil {
			t.Fatal(err)
		}
		rel := model.NodeInboundRelay{InboundID: row.ID, ForwardID: fwd.ID, Name: name, Managed: 1, CreatedTime: now}
		if err := db.Create(&rel).Error; err != nil {
			t.Fatal(err)
		}
		return rel.ID
	}
	relayA := mkRelay("line-a", "203.0.113.10", 31001)
	relayB := mkRelay("line-b", "203.0.113.11", 31002)

	rows, err := r.ListNodeInbounds()
	if err != nil {
		t.Fatal(err)
	}
	mkSub := func(options string) *model.Subscription {
		return &model.Subscription{Name: "x", Kind: model.SubscriptionShare, Token: strings.Repeat("a", 32),
			UUID: "u", Password: "p", InboundIDs: fmt.Sprint(row.ID), Options: options, Enabled: 1}
	}
	relayIDsOf := func(eps []kernel.Endpoint) []int64 {
		out := []int64{}
		for _, e := range eps {
			out = append(out, e.RelayID)
		}
		sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
		return out
	}
	want := func(options string, ids ...int64) {
		t.Helper()
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		if got := relayIDsOf(h.subEndpoints(mkSub(options), rows)); fmt.Sprint(got) != fmt.Sprint(ids) {
			t.Fatalf("subEndpoints(%s) relay ids = %v, want %v", options, got, ids)
		}
	}

	// 无范围：直连 + 两条中转全出。
	want(``, 0, relayA, relayB)
	want(`{"lines":"relay"}`, relayA, relayB)
	want(`{"lines":"direct"}`, 0)
	// 白名单精确集：单条中转只出该条（存量 Options 原文即此形，
	// 解析恢复后原样生效）。
	want(fmt.Sprintf(`{"relayIds":[%d]}`, relayA), relayA)
	want(`{"relayIds":[0]}`, 0)
	want(fmt.Sprintf(`{"relayIds":[0,%d]}`, relayB), 0, relayB)
	// 白名单优先于 lines 三档。
	want(`{"lines":"relay","relayIds":[0]}`, 0)
	want(fmt.Sprintf(`{"lines":"direct","relayIds":[%d]}`, relayB), relayB)
}


// relayIds 解析保真（subPipeline）：白名单原样保留、与 lines 相互
// 独立（不再折算），options() 两者都带，marshal 往返不丢白名单
// （保存回写口径）；空白名单不落键。
func TestParsePipelineRelayIDsPreserved(t *testing.T) {
	p := parsePipeline(`{"lines":"relay","relayIds":[0,7]}`)
	if p.Lines != "relay" {
		t.Fatalf("lines must parse independently of relayIds, got %q", p.Lines)
	}
	if fmt.Sprint(p.RelayIDs) != "[0 7]" {
		t.Fatalf("relayIds must be preserved verbatim, got %v", p.RelayIDs)
	}
	opts := p.options()
	if opts.Lines != "relay" || fmt.Sprint(opts.RelayIDs) != "[0 7]" {
		t.Fatalf("options() must carry both scope fields, got %+v", opts)
	}
	out, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"relayIds":[0,7]`) {
		t.Fatalf("marshal round-trip must keep the whitelist: %s", out)
	}
	// 存量白名单单独出现（2.1.2 前的 Options 原文）：lines 不被
	// 折算改写，白名单原样复活。
	p = parsePipeline(`{"relayIds":[3]}`)
	if p.Lines != "" || fmt.Sprint(p.RelayIDs) != "[3]" {
		t.Fatalf("legacy whitelist-only options must revive verbatim, got %+v", p)
	}
	p = parsePipeline(`{"relayIds":[]}`)
	if len(p.RelayIDs) != 0 {
		t.Fatalf("empty whitelist parses to no whitelist, got %v", p.RelayIDs)
	}
	out, err = json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "relayIds") {
		t.Fatalf("empty whitelist must not be re-marshalled: %s", out)
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
	}
	if defaults != 1 {
		t.Fatalf("exactly one subscription must be marked default, got %d", defaults)
	}
}
