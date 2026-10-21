package handler

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// ─── G3-131 / BUG-A2-5: /tunnel/user/update must be a true partial update ───

func seedG3UserTunnel(t *testing.T, r *repo.Repository) *model.UserTunnel {
	t.Helper()
	ut := &model.UserTunnel{
		UserID:        2,
		TunnelID:      3,
		Num:           20,
		Flow:          100,
		FlowMiB:       2048,
		FlowResetTime: 7,
		ExpTime:       1893456000000,
		Status:        1,
	}
	if err := r.DB().Create(ut).Error; err != nil {
		t.Fatalf("seed user_tunnel: %v", err)
	}
	return ut
}

func seedG3SpeedLimit(t *testing.T, r *repo.Repository, name string) int64 {
	t.Helper()
	sl := &model.SpeedLimit{Name: name, Speed: 10, CreatedTime: 1, Status: 1}
	if err := r.DB().Create(sl).Error; err != nil {
		t.Fatalf("seed speed_limit %s: %v", name, err)
	}
	return sl.ID
}

func postG3(t *testing.T, router http.Handler, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	return resp
}

func reloadG3UserTunnel(t *testing.T, r *repo.Repository, id int64) *model.UserTunnel {
	t.Helper()
	ut, err := r.GetUserTunnelByID(id)
	if err != nil {
		t.Fatalf("reload user_tunnel: %v", err)
	}
	if ut == nil {
		t.Fatalf("user_tunnel %d missing after update", id)
	}
	return ut
}

func TestUserTunnelUpdatePartialKeepsUnsetFields(t *testing.T) {
	router, r := setupConfigAccessTestRouter(t)
	adminToken := mustGenerateConfigAccessToken(t, 1, "admin_user", 0)
	ut := seedG3UserTunnel(t, r)

	// Only status is supplied; every other field must keep its stored value
	// (previously flow/num/expTime/flowResetTime were silently reset).
	resp := postG3(t, router, "/api/v1/tunnel/user/update", adminToken, `{"id":`+itoa(ut.ID)+`,"status":0}`)
	assertHandlerCode(t, resp, 0)

	got := reloadG3UserTunnel(t, r, ut.ID)
	if got.Status != 0 {
		t.Fatalf("status: want 0, got %d", got.Status)
	}
	if got.Flow != 100 || got.FlowMiB != 2048 {
		t.Fatalf("flow must stay 100/2048MiB, got %d/%d", got.Flow, got.FlowMiB)
	}
	if got.Num != 20 {
		t.Fatalf("num must stay 20, got %d", got.Num)
	}
	if got.ExpTime != 1893456000000 {
		t.Fatalf("expTime must stay 1893456000000, got %d", got.ExpTime)
	}
	if got.FlowResetTime != 7 {
		t.Fatalf("flowResetTime must stay 7, got %d", got.FlowResetTime)
	}
	if got.SpeedID.Valid {
		t.Fatalf("speedId must stay unset, got %v", got.SpeedID)
	}
}

func TestUserTunnelUpdateExplicitValuesStillApply(t *testing.T) {
	router, r := setupConfigAccessTestRouter(t)
	adminToken := mustGenerateConfigAccessToken(t, 1, "admin_user", 0)
	ut := seedG3UserTunnel(t, r)

	// Explicit zero is a real value, distinct from "not supplied".
	resp := postG3(t, router, "/api/v1/tunnel/user/update", adminToken, `{"id":`+itoa(ut.ID)+`,"num":0}`)
	assertHandlerCode(t, resp, 0)
	got := reloadG3UserTunnel(t, r, ut.ID)
	if got.Num != 0 {
		t.Fatalf("explicit num=0 must apply, got %d", got.Num)
	}
	if got.Flow != 100 || got.FlowMiB != 2048 {
		t.Fatalf("flow must stay 100/2048MiB, got %d/%d", got.Flow, got.FlowMiB)
	}

	// flow-only update applies, and re-sending the same flow keeps FlowMiB.
	resp = postG3(t, router, "/api/v1/tunnel/user/update", adminToken, `{"id":`+itoa(ut.ID)+`,"flow":50}`)
	assertHandlerCode(t, resp, 0)
	got = reloadG3UserTunnel(t, r, ut.ID)
	if got.Flow != 50 {
		t.Fatalf("explicit flow=50 must apply, got %d", got.Flow)
	}
	if got.Num != 0 {
		t.Fatalf("num must stay 0, got %d", got.Num)
	}
}

func TestUserTunnelUpdateSpeedIDKeepClearAndSet(t *testing.T) {
	router, r := setupConfigAccessTestRouter(t)
	adminToken := mustGenerateConfigAccessToken(t, 1, "admin_user", 0)
	ut := seedG3UserTunnel(t, r)
	s1 := seedG3SpeedLimit(t, r, "g3-s1")
	s2 := seedG3SpeedLimit(t, r, "g3-s2")
	if err := r.DB().Model(&model.UserTunnel{}).Where("id = ?", ut.ID).
		Update("speed_id", sql.NullInt64{Int64: s1, Valid: true}).Error; err != nil {
		t.Fatalf("attach speed limit: %v", err)
	}

	// No speedId key: the existing reference must survive a partial update.
	resp := postG3(t, router, "/api/v1/tunnel/user/update", adminToken, `{"id":`+itoa(ut.ID)+`,"num":5}`)
	assertHandlerCode(t, resp, 0)
	got := reloadG3UserTunnel(t, r, ut.ID)
	if !got.SpeedID.Valid || got.SpeedID.Int64 != s1 {
		t.Fatalf("speedId must stay %d, got %v", s1, got.SpeedID)
	}
	if got.Num != 5 {
		t.Fatalf("num must be 5, got %d", got.Num)
	}

	// Explicit null clears the reference.
	resp = postG3(t, router, "/api/v1/tunnel/user/update", adminToken, `{"id":`+itoa(ut.ID)+`,"speedId":null}`)
	assertHandlerCode(t, resp, 0)
	got = reloadG3UserTunnel(t, r, ut.ID)
	if got.SpeedID.Valid {
		t.Fatalf("explicit speedId:null must clear, got %v", got.SpeedID)
	}

	// A concrete value sets a new reference.
	resp = postG3(t, router, "/api/v1/tunnel/user/update", adminToken, `{"id":`+itoa(ut.ID)+`,"speedId":`+itoa(s2)+`}`)
	assertHandlerCode(t, resp, 0)
	got = reloadG3UserTunnel(t, r, ut.ID)
	if !got.SpeedID.Valid || got.SpeedID.Int64 != s2 {
		t.Fatalf("speedId must become %d, got %v", s2, got.SpeedID)
	}
}

// ─── G3-131 / A3 F-1: config text fields can be cleared (empty = delete) ───

func TestConfigUpdateClearDeletesOnlyThatKey(t *testing.T) {
	router, r := setupConfigAccessTestRouter(t)
	adminToken := mustGenerateConfigAccessToken(t, 1, "admin_user", 0)
	seedConfigValue(t, r, "notify_telegram_bot_token", "bot-token-secret")
	seedConfigValue(t, r, "notify_telegram_chat_id", "123456789")
	seedConfigValue(t, r, "app_name", "Brand")

	resp := postG3(t, router, "/api/v1/config/update", adminToken, `{"notify_telegram_bot_token":""}`)
	assertHandlerCode(t, resp, 0)

	if cfg, err := r.GetConfigByName("notify_telegram_bot_token"); err != nil || cfg != nil {
		t.Fatalf("cleared key must be deleted, got %#v err=%v", cfg, err)
	}
	if cfg, err := r.GetConfigByName("notify_telegram_chat_id"); err != nil || cfg == nil || cfg.Value != "123456789" {
		t.Fatalf("sibling key must be untouched, got %#v err=%v", cfg, err)
	}
	if cfg, err := r.GetConfigByName("app_name"); err != nil || cfg == nil || cfg.Value != "Brand" {
		t.Fatalf("unrelated key must be untouched, got %#v err=%v", cfg, err)
	}
}

func TestConfigUpdateMixedBatchClearAndSet(t *testing.T) {
	router, r := setupConfigAccessTestRouter(t)
	adminToken := mustGenerateConfigAccessToken(t, 1, "admin_user", 0)
	seedConfigValue(t, r, "notify_webhook_url", "https://example.invalid/hook")
	seedConfigValue(t, r, "app_name", "Old")

	// The exact shape A3 hit on the real panel: one cleared field used to
	// reject the whole batch, discarding the valid edit alongside it.
	resp := postG3(t, router, "/api/v1/config/update", adminToken, `{"app_name":"New","notify_webhook_url":""}`)
	assertHandlerCode(t, resp, 0)

	if cfg, err := r.GetConfigByName("app_name"); err != nil || cfg == nil || cfg.Value != "New" {
		t.Fatalf("set value must land, got %#v err=%v", cfg, err)
	}
	if cfg, err := r.GetConfigByName("notify_webhook_url"); err != nil || cfg != nil {
		t.Fatalf("cleared webhook must be deleted, got %#v err=%v", cfg, err)
	}
}

func TestConfigUpdateSingleClearCredential(t *testing.T) {
	router, r := setupConfigAccessTestRouter(t)
	adminToken := mustGenerateConfigAccessToken(t, 1, "admin_user", 0)
	seedConfigValue(t, r, "notify_telegram_bot_token", "bot-token-secret")

	resp := postG3(t, router, "/api/v1/config/update-single", adminToken, `{"name":"notify_telegram_bot_token","value":""}`)
	assertHandlerCode(t, resp, 0)

	if cfg, err := r.GetConfigByName("notify_telegram_bot_token"); err != nil || cfg != nil {
		t.Fatalf("cleared key must be deleted, got %#v err=%v", cfg, err)
	}
}

func TestConfigUpdateEmptyRequiredKeyRejectedWithFieldName(t *testing.T) {
	router, r := setupConfigAccessTestRouter(t)
	adminToken := mustGenerateConfigAccessToken(t, 1, "admin_user", 0)
	seedConfigValue(t, r, "jwt_secret", "keep-me")
	seedConfigValue(t, r, "monitor_retention_days", "30")

	resp := postG3(t, router, "/api/v1/config/update", adminToken, `{"jwt_secret":""}`)
	assertHandlerCodeMsgContains(t, resp, "jwt_secret")
	if cfg, err := r.GetConfigByName("jwt_secret"); err != nil || cfg == nil || cfg.Value != "keep-me" {
		t.Fatalf("required key must be unchanged, got %#v err=%v", cfg, err)
	}

	resp = postG3(t, router, "/api/v1/config/update-single", adminToken, `{"name":"jwt_secret","value":""}`)
	assertHandlerCodeMsgContains(t, resp, "jwt_secret")
	if cfg, err := r.GetConfigByName("jwt_secret"); err != nil || cfg == nil || cfg.Value != "keep-me" {
		t.Fatalf("required key must be unchanged, got %#v err=%v", cfg, err)
	}

	resp = postG3(t, router, "/api/v1/config/update-single", adminToken, `{"name":"monitor_retention_days","value":""}`)
	var out struct {
		Code int `json:"code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.Code == 0 {
		t.Fatal("empty monitor_retention_days must be rejected")
	}
	if cfg, err := r.GetConfigByName("monitor_retention_days"); err != nil || cfg == nil || cfg.Value != "30" {
		t.Fatalf("required key must be unchanged, got %#v err=%v", cfg, err)
	}
}

// ─── G3-131 / A3 F-4: notify credential group hidden from non-admins ───

func TestConfigListHidesNotifyCredentialGroupFromNonAdmin(t *testing.T) {
	router, r := setupConfigAccessTestRouter(t)
	if err := r.DB().Exec(`INSERT INTO user(id, user, pwd, role_id, exp_time, flow, in_flow, out_flow, flow_reset_time, num, created_time, updated_time, status) VALUES(2, 'plain_user', 'pwd', 1, 2727251700000, 99999, 0, 0, 1, 99999, 1, 1, 1)`).Error; err != nil {
		t.Fatalf("seed plain user: %v", err)
	}
	userToken := mustGenerateConfigAccessToken(t, 2, "plain_user", 1)
	seedConfigValue(t, r, "notify_telegram_bot_token", "bot-token-secret")
	seedConfigValue(t, r, "notify_telegram_chat_id", "123456789")
	seedConfigValue(t, r, "notify_telegram_api_base", "https://tg-proxy.internal")
	seedConfigValue(t, r, "notify_webhook_url", "https://example.invalid/hook")
	seedConfigValue(t, r, "app_name", "Brand")

	resp := postG3(t, router, "/api/v1/config/list", userToken, "")
	var out struct {
		Code int               `json:"code"`
		Data map[string]string `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.Code != 0 {
		t.Fatalf("expected code 0, got %d", out.Code)
	}
	for _, key := range []string{"notify_telegram_bot_token", "notify_telegram_chat_id", "notify_telegram_api_base", "notify_webhook_url"} {
		if _, ok := out.Data[key]; ok {
			t.Fatalf("%s must not be visible to non-admins", key)
		}
	}
	if out.Data["app_name"] != "Brand" {
		t.Fatalf("public keys must stay visible, got %v", out.Data["app_name"])
	}
}

func TestConfigPolicyClearableClassification(t *testing.T) {
	for _, key := range []string{"notify_telegram_bot_token", "notify_webhook_url", "notify_telegram_chat_id", "github_proxy_url", "app_name", "panel_domain"} {
		if !repo.IsClearableConfigKey(key) {
			t.Fatalf("%s must be clearable", key)
		}
	}
	for _, key := range []string{"jwt_secret", "monitor_retention_days", "monitor_tunnel_quality_enabled", "monitor_tunnel_quality_interval_sec", "license_key", "node_sub_token"} {
		if repo.IsClearableConfigKey(key) {
			t.Fatalf("%s must not be clearable", key)
		}
	}
	if !repo.IsSensitiveConfigKey("notify_telegram_chat_id") {
		t.Fatal("notify_telegram_chat_id must be sensitive (A3 F-4)")
	}
}

func assertHandlerCodeMsgContains(t *testing.T, rec *httptest.ResponseRecorder, substr string) {
	t.Helper()
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.Code == 0 {
		t.Fatalf("expected rejection, got code 0")
	}
	if !strings.Contains(out.Msg, substr) {
		t.Fatalf("error must name the field %q, got msg %q", substr, out.Msg)
	}
}
