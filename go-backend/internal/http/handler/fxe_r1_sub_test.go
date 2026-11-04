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
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R1-02-L1 回归：迁移保留的旧 token 长度 ≠32（旧默认 token >32、旧
// SubUser token 逐字复制任意长度）必须仍可拉取——拉取面只按实存
// token 精确查验，未知 token 仍 404。
func TestOpenSubscriptionAcceptsLegacyTokenLengths(t *testing.T) {
	h, r, _ := subFixture(t)
	long := createShareSub(t, r, &model.Subscription{
		Name: "旧长token", Kind: model.SubscriptionShare, Token: strings.Repeat("a", 40),
		UUID: "uuid-long", Password: "pw-long", Enabled: 1,
	})
	short := createShareSub(t, r, &model.Subscription{
		Name: "旧短token", Kind: model.SubscriptionShare, Token: strings.Repeat("b", 20),
		UUID: "uuid-short", Password: "pw-short", Enabled: 1,
	})
	for _, s := range []*model.Subscription{long, short} {
		code, body := fetchSub(t, h, s.Token, "clash")
		if code != http.StatusOK {
			t.Fatalf("legacy token len=%d: fetch = %d, want 200 (body %q)", len(s.Token), code, body)
		}
	}
	if code, _ := fetchSub(t, h, strings.Repeat("c", 40), "clash"); code != http.StatusNotFound {
		t.Fatalf("unknown 40-char token: fetch = %d, want 404", code)
	}
	if code, _ := fetchSub(t, h, strings.Repeat("d", 20), "clash"); code != http.StatusNotFound {
		t.Fatalf("unknown 20-char token: fetch = %d, want 404", code)
	}
}

// R1-02-L3 回归：删除墓碑滞留窗口内，「我的订阅」须带 pendingDelete
// 标记（与管理员面同口径），正常订阅为 false。
func TestUserSubscriptionMineMarksPendingDelete(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/fxe_mine_pending.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	now := time.Now().UnixMilli()

	mkSub := func(token, name string) *model.Subscription {
		s := &model.Subscription{Name: name, Kind: model.SubscriptionShare, Token: token,
			UUID: "u-" + token[:2], Password: "p", Enabled: 1, CreatedTime: now, UpdatedTime: now}
		if err := r.SaveSubscription(s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	normal := mkSub(strings.Repeat("e", 32), "正常订阅")
	tomb := mkSub(strings.Repeat("f", 32), "删除中订阅")
	if err := r.SetUserSubscriptions(2, []int64{normal.ID, tomb.ID}); err != nil {
		t.Fatal(err)
	}
	if err := r.MarkSubscriptionPendingDelete(tomb.ID); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/api/v1/user-subscription/mine", strings.NewReader("{}"))
	ctx := context.WithValue(req.Context(), middleware.ClaimsContextKey,
		auth.Claims{Sub: fmt.Sprint(2), RoleID: 1})
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
	if env.Code != 0 || len(env.Data.Subscriptions) != 2 {
		t.Fatalf("mine: code=%d items=%+v", env.Code, env.Data.Subscriptions)
	}
	byID := map[int64]userSubscriptionMineView{}
	for _, it := range env.Data.Subscriptions {
		byID[it.ID] = it
	}
	if !byID[tomb.ID].PendingDelete {
		t.Fatalf("tombstoned subscription must carry pendingDelete=true: %+v", byID[tomb.ID])
	}
	if byID[normal.ID].PendingDelete {
		t.Fatalf("normal subscription must carry pendingDelete=false: %+v", byID[normal.ID])
	}
}
