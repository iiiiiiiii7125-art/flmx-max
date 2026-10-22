package handler

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// X-P3-13（L-03-2）：legacy /inbound/sub/info 的 reset 此前仅换 token，
// 不轮换 UUID/密码、不下发内核——经此路重置者误判旧配置已失效。
// 修复后 legacy 与主路径（subscriptionReset）共用同一全轮换实现：
// token + UUID/密码一起轮换。默认订阅是 share 型，UUID/Password 非空。
func TestR1B7LegacySubInfoResetRotatesFullCredentials(t *testing.T) {
	env := newSubInfoTestEnv(t)
	oldToken, oldUUID, oldPassword := env.sub.Token, env.sub.UUID, env.sub.Password
	if oldUUID == "" || oldPassword == "" {
		t.Fatalf("default share subscription must carry credentials, got uuid=%q password=%q", oldUUID, oldPassword)
	}

	resp := callSubInfo(t, env.h, env.adminID, 0, true)
	if resp.Code != 0 {
		t.Fatalf("legacy reset must succeed for admin, got %+v", resp)
	}
	cur, err := env.repo.GetSubscription(env.sub.ID)
	if err != nil || cur == nil {
		t.Fatalf("reload subscription: %v", err)
	}
	if cur.Token == oldToken || cur.Token != resp.Data.Token {
		t.Fatalf("token must rotate and match response: old=%q cur=%q resp=%q", oldToken, cur.Token, resp.Data.Token)
	}
	if cur.UUID == oldUUID {
		t.Fatalf("legacy reset must rotate UUID like the main path (old=%q)", oldUUID)
	}
	if cur.Password == oldPassword {
		t.Fatal("legacy reset must rotate password like the main path")
	}
}

// 主路径行为不变对照：subscriptionReset 仍全轮换（token+UUID/密码）。
func TestR1B7MainSubscriptionResetStillRotatesFullCredentials(t *testing.T) {
	env := newSubInfoTestEnv(t)
	oldToken, oldUUID, oldPassword := env.sub.Token, env.sub.UUID, env.sub.Password

	body := `{"id":` + strconv.FormatInt(env.sub.ID, 10) + `}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/subscription/reset", strings.NewReader(body))
	rec := httptest.NewRecorder()
	env.h.subscriptionReset(rec, req)
	if !strings.Contains(rec.Body.String(), `"code":0`) {
		t.Fatalf("main reset must succeed: %s", rec.Body.String())
	}
	cur, err := env.repo.GetSubscription(env.sub.ID)
	if err != nil || cur == nil {
		t.Fatalf("reload subscription: %v", err)
	}
	if cur.Token == oldToken || cur.UUID == oldUUID || cur.Password == oldPassword {
		t.Fatalf("main reset must rotate token+UUID+password, got tokenChanged=%v uuidChanged=%v passwordChanged=%v",
			cur.Token != oldToken, cur.UUID != oldUUID, cur.Password != oldPassword)
	}
}
