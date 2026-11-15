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

// C-07（流量审计）：个人（非分享）订阅永不计量——其计数只由分享
// 订阅的 kernel 用户样本写，个人订阅的 UploadBytes/DownloadBytes
// 恒为 0，TrafficLimit 永不触发；视图须把这类订阅标为
// MeteringUnsupported（与 L4-08 的 ID 越界同口径显式可见），
// 而不是在用户页显示「已用 0／限额」。
func TestSubscriptionViewOwnKindIsMeteringUnsupported(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/c07.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	now := time.Now()

	// 生产态的个人订阅：计数恒为 0（无写入方）、限额已设——此前
	// 用户页显示「已用 0／限额」，限额死信却无任何提示。
	own := &model.Subscription{ID: 7, Name: "c07-own", Kind: model.SubscriptionOwn,
		Token: "c0000000000000000000000000000000", Enabled: 1,
		TrafficLimit: 1000, UploadBytes: 0, DownloadBytes: 0,
		CreatedTime: now.UnixMilli(), UpdatedTime: now.UnixMilli()}
	view := h.subscriptionView(own, nil, now, kernel.FormatBase64)
	if !view.MeteringUnsupported {
		t.Fatalf("own subscription view MeteringUnsupported = false, want true")
	}
	if view.OverQuota {
		t.Fatalf("own subscription view OverQuota = true, want false")
	}
	if view.TrafficLimit != 1000 {
		t.Fatalf("own subscription view TrafficLimit = %d, want 1000 (the dead limit itself is still exposed)", view.TrafficLimit)
	}

	// 对照：同等数字的分享订阅限额照常触发、不标未计量。
	share := &model.Subscription{ID: 8, Name: "c07-share", Kind: model.SubscriptionShare,
		Token: "c1111111111111111111111111111111", Enabled: 1,
		TrafficLimit: 1000, UploadBytes: 3000, DownloadBytes: 2000,
		CreatedTime: now.UnixMilli(), UpdatedTime: now.UnixMilli()}
	shareView := h.subscriptionView(share, nil, now, kernel.FormatBase64)
	if shareView.MeteringUnsupported {
		t.Fatalf("share subscription view MeteringUnsupported = true, want false")
	}
	if !shareView.OverQuota {
		t.Fatalf("share subscription view OverQuota = false, want true")
	}

	// 对照：L4-08 既有行为——分享订阅 ID 越界仍标未计量。
	big := &model.Subscription{ID: kernel.MaxUserMarkID + 1, Name: "c07-big", Kind: model.SubscriptionShare,
		Token: "c2222222222222222222222222222222", Enabled: 1,
		TrafficLimit: 1000, UploadBytes: 3000, DownloadBytes: 2000,
		CreatedTime: now.UnixMilli(), UpdatedTime: now.UnixMilli()}
	bigView := h.subscriptionView(big, nil, now, kernel.FormatBase64)
	if !bigView.MeteringUnsupported {
		t.Fatalf("over-limit-ID share view MeteringUnsupported = false, want true")
	}
}

// C-07（流量审计）：mine 端点把 MeteringUnsupported 透传到用户页
// （userSubscriptionMineView 是独立投影，漏拷即用户页永远看不到
// 该标记——tsc 构建曾在此处红过）。个人订阅限额已设但不计量时
// 用户页须收到 true，分享订阅对照为 false。
func TestUserSubscriptionMineCarriesMeteringUnsupported(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/c07-mine.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	now := time.Now().UnixMilli()

	mkSub := func(token, name, kind string, limit int64) *model.Subscription {
		s := &model.Subscription{Name: name, Kind: kind, Token: token,
			UUID: "u-" + token[:2], Password: "p", Enabled: 1,
			TrafficLimit: limit, CreatedTime: now, UpdatedTime: now}
		if err := r.SaveSubscription(s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	own := mkSub(strings.Repeat("a", 32), "c07-mine-own", model.SubscriptionOwn, 1000)
	share := mkSub(strings.Repeat("b", 32), "c07-mine-share", model.SubscriptionShare, 1000)
	if err := r.SetUserSubscriptions(2, []int64{own.ID, share.ID}); err != nil {
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
	for _, item := range env.Data.Subscriptions {
		byID[item.ID] = item
	}
	if !byID[own.ID].MeteringUnsupported {
		t.Fatalf("mine own sub MeteringUnsupported = false, want true")
	}
	if byID[share.ID].MeteringUnsupported {
		t.Fatalf("mine share sub MeteringUnsupported = true, want false")
	}
}
