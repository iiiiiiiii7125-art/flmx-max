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

	"go-backend/internal/http/response"
	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
)

// 下一波修复·路 3（T-001 / T-002 / RAW291）行为回归。
//
// - T-001：白名单刷新失败必须进回执（成功回执带 warning、失败回执
//   拼 note、后台面记日志），不得静默成功。
// - T-002：inboundAllowListIx 的静默丢弃点补日志后方向仍为
//   fail-closed——坏条目不进 allow、回环仍在。
// - RAW291：仓储原文不得进回执（通用 "Internal error"）；固定业务
//   文案仍原样回；下发失败 Msg 里仓储原文换固定文案、数据面原文保留。

// TestR2W3RefreshInboundWhitelistReturnsError：入站的 kernel 行删除后
// applyKernel 必失败——refreshInboundWhitelist 返非 nil，
// refreshInboundWhitelists 返回失败 ID 集。
func TestR2W3RefreshInboundWhitelistReturnsError(t *testing.T) {
	s := setupR1B3Whitelist(t, 2)
	if err := s.h.refreshInboundWhitelist(s.inboundID); err != nil {
		t.Fatalf("control: refresh must succeed while kernel exists: %v", err)
	}
	if err := s.repo.DB().Exec(`DELETE FROM node_kernel WHERE id = ?`, s.kernelID).Error; err != nil {
		t.Fatalf("delete kernel row: %v", err)
	}
	if err := s.h.refreshInboundWhitelist(s.inboundID); err == nil {
		t.Fatalf("refresh must fail after its kernel row is deleted")
	}
	failed := s.h.refreshInboundWhitelists([]int64{s.inboundID, s.inboundID})
	if len(failed) != 1 || failed[0] != s.inboundID {
		t.Fatalf("refreshInboundWhitelists must return the failed id once, got %v", failed)
	}
}

// TestR2W3RelayDeleteWarnsOnRefreshFailure：删中转成功、但白名单刷新
// 失败时回执须为 Code 0 + warning 点名入站。
func TestR2W3RelayDeleteWarnsOnRefreshFailure(t *testing.T) {
	s := setupR1B3Whitelist(t, 2)
	var relayID int64
	if err := s.repo.DB().Raw(`SELECT id FROM node_inbound_relay WHERE inbound_id = ?`, s.inboundID).Scan(&relayID).Error; err != nil || relayID == 0 {
		t.Fatalf("find relay row: id=%d err=%v", relayID, err)
	}
	if err := s.repo.DB().Exec(`DELETE FROM node_kernel WHERE id = ?`, s.kernelID).Error; err != nil {
		t.Fatalf("delete kernel row: %v", err)
	}
	body := r2b2AuthPost(t, s.h.inboundRelayDelete, "/api/v1/inbound/relay/delete", map[string]interface{}{"id": relayID}, "r2w3-relay-del-1")
	var env response.R
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("decode delete response: %v (%s)", err, body)
	}
	// OKWithWarning 把警告放在独立 warning 字段（msg 仍为操作成功）。
	if env.Code != 0 || !strings.Contains(env.Warning, "白名单刷新失败") || !strings.Contains(env.Warning, fmt.Sprint(s.inboundID)) {
		t.Fatalf("relay delete must succeed with a whitelist-refresh warning naming the inbound, got %s", body)
	}
}

// TestR2W3RelayCreateWarnsOnRefreshFailure：新建中转的对照格——内核
// 在时成功且无警告；内核行删除后成功但回执带白名单刷新失败警告。
func TestR2W3RelayCreateWarnsOnRefreshFailure(t *testing.T) {
	s := setupR1B3Whitelist(t, 2)
	var kb model.NodeKernel
	if err := s.repo.DB().First(&kb, s.kernelID).Error; err != nil {
		t.Fatalf("load kernel row: %v", err)
	}
	mkInbound := func(name string, port int) int64 {
		in := seedChainInbound(t, s.repo, &kb, name, kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: port,
			Security: kernel.Security{Type: kernel.SecurityReality}}, 0)
		in.RelayWhitelist = 1
		if err := s.repo.SaveNodeInbound(in); err != nil {
			t.Fatalf("save whitelisted inbound: %v", err)
		}
		return in.ID
	}
	ctrlID := mkInbound("r2w3-ctrl", 8443)
	body := r2b2AuthPost(t, s.h.inboundRelayCreate, "/api/v1/inbound/relay/create",
		map[string]interface{}{"inboundId": ctrlID, "tunnelId": s.tunnelID, "inPort": 30007, "name": "r2w3-ctrl"}, "r2w3-relay-create-ctrl")
	var env response.R
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("decode control create response: %v (%s)", err, body)
	}
	if env.Code != 0 || strings.Contains(env.Warning, "白名单刷新失败") {
		t.Fatalf("control create must succeed without a refresh warning, got %s", body)
	}
	if err := s.repo.DB().Exec(`DELETE FROM node_kernel WHERE id = ?`, s.kernelID).Error; err != nil {
		t.Fatalf("delete kernel row: %v", err)
	}
	failID := mkInbound("r2w3-fail", 8444)
	body = r2b2AuthPost(t, s.h.inboundRelayCreate, "/api/v1/inbound/relay/create",
		map[string]interface{}{"inboundId": failID, "tunnelId": s.tunnelID, "inPort": 30008, "name": "r2w3-fail"}, "r2w3-relay-create-fail")
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("decode failing create response: %v (%s)", err, body)
	}
	if env.Code != 0 || !strings.Contains(env.Warning, "白名单刷新失败") {
		t.Fatalf("create with refresh failure must succeed with a whitelist-refresh warning, got %s", body)
	}
}

// assertGenericInternalErrorBody 是 assertGenericInternalError 的
// 字符串体版本（r2b2AuthPost 返回体字符串）。
func assertGenericInternalErrorBody(t *testing.T, body string) {
	t.Helper()
	var out response.R
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode response: %v body=%s", err, body)
	}
	if out.Code != -2 || out.Msg != "Internal error" {
		t.Fatalf("expected generic internal error (-2, \"Internal error\"), got (%d, %q)", out.Code, out.Msg)
	}
	if strings.Contains(body, "no such table") || strings.Contains(body, "sql:") {
		t.Fatalf("response leaks raw storage error: %s", body)
	}
}

// TestR2W3SubscriptionStorageErrorsAreGeneric：掉表法——路 3 五文件
// 的纯仓储错误面只回通用文案，不含 "no such table"。
func TestR2W3SubscriptionStorageErrorsAreGeneric(t *testing.T) {
	cases := []struct {
		name    string
		table   string
		fn      func(http.ResponseWriter, *http.Request)
		path    string
		payload map[string]interface{}
	}{
		{"subscriptionList", "subscription", nil, "/api/v1/subscription/list", map[string]interface{}{}},
		{"subscriptionTemplateList", "subscription_template", nil, "/api/v1/subscription/template/list", map[string]interface{}{}},
		{"remoteSourceList", "remote_source", nil, "/api/v1/subscription/source/list", map[string]interface{}{}},
		{"getUserInbounds", "user_inbound", nil, "/api/v1/user/inbound/get", map[string]interface{}{"userId": 1}},
		{"userSubscriptionList", "user_subscription", nil, "/api/v1/user/subscription/list", map[string]interface{}{"userId": 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, r := newPanelsecHandler(t)
			switch tc.name {
			case "subscriptionList":
				tc.fn = h.subscriptionList
			case "subscriptionTemplateList":
				tc.fn = h.subscriptionTemplateList
			case "remoteSourceList":
				tc.fn = h.remoteSourceList
			case "getUserInbounds":
				tc.fn = h.getUserInbounds
			case "userSubscriptionList":
				tc.fn = h.userSubscriptionList
			}
			dropTableForTest(t, r, tc.table)
			body := r2b2AuthPost(t, tc.fn, tc.path, tc.payload, "")
			assertGenericInternalErrorBody(t, body)
		})
	}
}

// TestR2W3ValidateIDsExistMixedError：ValidateIDsExist 混合错误——
// 幻影 ID 固定文案原样回（Type B fall-through），仓储错误走通用文案。
func TestR2W3ValidateIDsExistMixedError(t *testing.T) {
	h, r := newPanelsecHandler(t)
	if _, err := r.CreateUser("admin", "hash", 0, time.Now().Add(24*time.Hour).UnixMilli(), 1, 1, 100, 1, 0, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	// 幻影用户：表在，固定文案原样回。
	body := r2b2AuthPost(t, h.setUserInbounds, "/api/v1/user/inbound/set", map[string]interface{}{"userId": 9999, "inboundIds": []int64{}}, "")
	var env response.R
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("decode phantom response: %v (%s)", err, body)
	}
	if env.Code != -2 || !strings.Contains(env.Msg, "用户不存在") {
		t.Fatalf("phantom user must be reported verbatim, got (%d, %q)", env.Code, env.Msg)
	}
	// 仓储错误：user 表掉，通用文案。
	dropTableForTest(t, r, "user")
	body = r2b2AuthPost(t, h.setUserInbounds, "/api/v1/user/inbound/set", map[string]interface{}{"userId": 1, "inboundIds": []int64{}}, "")
	assertGenericInternalErrorBody(t, body)
}

// TestR2W3SyncStorageErrorIsGeneric：respondRemoteSync 的仓储直通
// 错误（掉 node_inbound 表）——回执保持 Code -1 + Data，只把 Msg
// 换成通用文案；last_error 同样不得存原文。
func TestR2W3SyncStorageErrorIsGeneric(t *testing.T) {
	h, r := newPanelsecHandler(t)
	now := time.Now().UnixMilli()
	// 文件源 + 单条 ss 分享链接作上传体：跳过 fetch、解析出 1 个
	// 节点，随后 ListNodeInbounds 撞掉表——正好走到 fail() 的仓储错。
	src := &model.RemoteSource{Kind: model.RemoteSourceFile, Enabled: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.DB().Create(src).Error; err != nil {
		t.Fatalf("create remote source: %v", err)
	}
	dropTableForTest(t, r, "node_inbound")
	rec := httptest.NewRecorder()
	h.respondRemoteSync(rec, context.Background(), src, "ss://YWVzLTI1Ni1nY206cGFzc3dvcmQ@203.0.113.10:8388#test-node")
	var env struct {
		Code int                    `json:"code"`
		Msg  string                 `json:"msg"`
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode sync response: %v (%s)", err, rec.Body.String())
	}
	if env.Code != -1 || env.Msg != "Internal error" || env.Data == nil {
		t.Fatalf("sync storage error must keep code -1 + data with generic msg, got (%d, %q, data=%v)", env.Code, env.Msg, env.Data != nil)
	}
	if strings.Contains(rec.Body.String(), "no such table") {
		t.Fatalf("sync response leaks raw storage error: %s", rec.Body.String())
	}
	var lastErr string
	if err := r.DB().Raw(`SELECT last_error FROM remote_source WHERE id = ?`, src.ID).Scan(&lastErr).Error; err != nil {
		t.Fatalf("read last_error: %v", err)
	}
	if strings.Contains(lastErr, "no such table") {
		t.Fatalf("last_error stores raw storage error: %q", lastErr)
	}
}

// TestR2W3T002BadAllowlistExtraDroppedFailClosed：附加名单写坏值时
// 解析失败的条目不进 allow、回环仍在（fail-closed 不变）。
func TestR2W3T002BadAllowlistExtraDroppedFailClosed(t *testing.T) {
	h, _ := newPanelsecHandler(t)
	row := model.NodeInbound{ID: 7, WhitelistExtra: "not-an-ip"}
	allow, err := h.inboundAllowListIx(row, nil)
	if err != nil {
		t.Fatalf("allow list must not error on a bad extra entry: %v", err)
	}
	allowSet := map[string]bool{}
	for _, c := range allow {
		allowSet[c] = true
	}
	if !allowSet["127.0.0.0/8"] || !allowSet["::1/128"] {
		t.Fatalf("loopback must stay allowed, got %v", allow)
	}
	for _, c := range allow {
		if strings.Contains(c, "not-an-ip") {
			t.Fatalf("bad extra entry must not enter the allow list, got %v", allow)
		}
	}
	// 对照：合法附加条目正常进 allow。
	row.WhitelistExtra = "203.0.113.99"
	allow, err = h.inboundAllowListIx(row, nil)
	if err != nil {
		t.Fatalf("allow list must not error on a good extra entry: %v", err)
	}
	found := false
	for _, c := range allow {
		if c == "203.0.113.99/32" {
			found = true
		}
	}
	if !found {
		t.Fatalf("good extra entry must enter the allow list, got %v", allow)
	}
}
