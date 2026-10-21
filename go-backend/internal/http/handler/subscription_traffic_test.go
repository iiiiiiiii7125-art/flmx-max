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

// Hours land in the viewer's local days and months; older ones are left
// out.（移植自上游 3.3.29 subscription_traffic_test，断言不变。）
func TestSubTrafficBuckets(t *testing.T) {
	cst := time.FixedZone("", 8*3600)
	now := time.Date(2026, 3, 2, 10, 30, 0, 0, cst)
	at := func(y int, m time.Month, d, h int) int64 { return time.Date(y, m, d, h, 0, 0, 0, cst).UnixMilli() }
	hours := []model.SubscriptionTraffic{
		{Hour: at(2025, 1, 31, 23), DownloadBytes: 1}, // before the 12 months
		{Hour: at(2026, 2, 28, 23), DownloadBytes: 10, UploadBytes: 1},
		{Hour: at(2026, 3, 1, 0), DownloadBytes: 20},
		{Hour: at(2026, 3, 2, 9), DownloadBytes: 30},
		{Hour: at(2026, 3, 2, 10), DownloadBytes: 40, UploadBytes: 4},
	}
	hour := subTraffic(hours, "hour", now)
	if len(hour.Points) != 24 || hour.Points[23].Down != 40 || hour.Points[22].Down != 30 || hour.Down != 70 ||
		hour.Points[0].Time != at(2026, 3, 1, 11) {
		t.Fatalf("hour: %+v", hour)
	}
	day := subTraffic(hours, "day", now)
	if len(day.Points) != 30 || day.Points[29].Down != 70 || day.Points[28].Down != 20 || day.Points[27].Down != 10 || day.Up != 5 {
		t.Fatalf("day: %+v", day)
	}
	month := subTraffic(hours, "month", now)
	if len(month.Points) != 12 || month.Points[11].Down != 90 || month.Points[10].Down != 10 || month.Down != 100 ||
		month.Points[0].Time != at(2025, 4, 1, 0) {
		t.Fatalf("month: %+v", month)
	}
}

// #88 用户端鉴权按我方分配模型：未分配查他人订阅 403、已分配可查、
// 管理员可查任意订阅；管理端 handler 同口径出数。
func TestUserSubscriptionTrafficGuards(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/sub_traffic_guard.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	now := time.Now().UnixMilli()

	mkSub := func(token string) *model.Subscription {
		s := &model.Subscription{Name: "s-" + token[:2], Kind: model.SubscriptionShare, Token: token,
			UUID: "u-" + token[:2], Password: "p", Enabled: 1, CreatedTime: now, UpdatedTime: now}
		if err := r.SaveSubscription(s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	assigned := mkSub(strings.Repeat("a", 32))
	other := mkSub(strings.Repeat("b", 32))
	if err := r.SetUserSubscriptions(2, []int64{assigned.ID}); err != nil {
		t.Fatal(err)
	}
	if err := r.ApplySubscriptionCounterSamples(1, "e", []repo.SubscriptionCounterSample{
		{SubscriptionID: assigned.ID, Direction: "up", Bytes: 100},
		{SubscriptionID: assigned.ID, Direction: "down", Bytes: 200},
	}, now); err != nil {
		t.Fatal(err)
	}

	type trafficEnv struct {
		Code int            `json:"code"`
		Data subTrafficView `json:"data"`
	}
	call := func(userFn func(w http.ResponseWriter, r *http.Request), userID int64, roleID int, subID int64) trafficEnv {
		req := httptest.NewRequest("POST", "/api/v1/user/subscription/traffic",
			strings.NewReader(fmt.Sprintf(`{"id":%d,"range":"hour","tzOffset":480}`, subID)))
		ctx := context.WithValue(req.Context(), middleware.ClaimsContextKey,
			auth.Claims{Sub: fmt.Sprint(userID), RoleID: roleID})
		rec := httptest.NewRecorder()
		userFn(rec, req.WithContext(ctx))
		var env trafficEnv
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode traffic response: %v (body=%s)", err, rec.Body.String())
		}
		return env
	}

	// 已分配用户查自己的订阅：出数且合计与记账一致。
	env := call(h.userSubscriptionTraffic, 2, 1, assigned.ID)
	if env.Code != 0 || env.Data.Range != "hour" || len(env.Data.Points) != 24 ||
		env.Data.Up != 100 || env.Data.Down != 200 {
		t.Fatalf("assigned user traffic: %+v", env)
	}
	// 未分配用户查他人订阅：403。
	if env := call(h.userSubscriptionTraffic, 2, 1, other.ID); env.Code != 403 {
		t.Fatalf("unassigned user querying other's subscription must be 403, got %+v", env)
	}
	// 无任何分配的用户查已分配给别人的订阅：403。
	if env := call(h.userSubscriptionTraffic, 3, 1, assigned.ID); env.Code != 403 {
		t.Fatalf("user without assignment must be 403, got %+v", env)
	}
	// 管理员查任意订阅：放行。
	if env := call(h.userSubscriptionTraffic, 1, 0, other.ID); env.Code != 0 {
		t.Fatalf("admin via user endpoint must pass, got %+v", env)
	}
	// 管理端 handler 出数口径一致。
	env = call(h.subscriptionTraffic, 1, 0, assigned.ID)
	if env.Code != 0 || env.Data.Up != 100 || env.Data.Down != 200 {
		t.Fatalf("admin endpoint traffic: %+v", env)
	}
}
