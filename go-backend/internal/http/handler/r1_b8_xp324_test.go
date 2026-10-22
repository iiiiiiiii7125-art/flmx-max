package handler

import (
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// X-P3-24 (L-09-6): subscriptionSave 两步同事务。修复前先单独落
// MarkSubscriptionResetTime 再 SaveSubscription，后者失败时重置
// 时间已改而 ResetDay 未改。用 UPDATE OF reset_day 触发器只让
// Save 失败、Mark 成功，断言重置时间不得被单独改动。
func TestR1B8_XP324_SubscriptionSaveResetTimeAtomic(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/xp324.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now()

	s := &model.Subscription{
		Name: "xp324-sub", Kind: model.SubscriptionShare, Token: strings.Repeat("c", 32),
		UUID: "u-324", Password: "p-324", SourceAll: 1, Enabled: 1,
		ResetDay: 1, TrafficResetTime: 1111111111111,
		CreatedTime: now.UnixMilli(), UpdatedTime: now.UnixMilli(),
	}
	if err := r.SaveSubscription(s); err != nil {
		t.Fatal(err)
	}
	if s.ID == 0 {
		t.Fatal("seed subscription id")
	}
	// 只拦截 Save 的整行更新（含 reset_day 列），放行 Mark 的单列更新。
	if err := r.DB().Exec(`CREATE TRIGGER fail_save_reset_day BEFORE UPDATE OF reset_day ON subscription BEGIN SELECT RAISE(ABORT, 'injected save failure'); END`).Error; err != nil {
		t.Fatal(err)
	}

	res := postJSONToHandler(t, h.subscriptionSave, map[string]interface{}{
		"id": s.ID, "name": "xp324-sub", "sourceAll": true, "resetDay": 15,
	})
	out := decodeCode(t, res.Body.String())
	if out.Code == 0 {
		t.Fatalf("注入 Save 失败时应报错, got %s", res.Body.String())
	}
	got, err := r.GetSubscription(s.ID)
	if err != nil || got == nil {
		t.Fatalf("get sub: %v", err)
	}
	if got.TrafficResetTime != 1111111111111 {
		t.Fatalf("Save 失败时重置时间不得被单独改动: got %d want 1111111111111", got.TrafficResetTime)
	}
	if got.ResetDay != 1 {
		t.Fatalf("Save 失败时 ResetDay 应保持 1, got %d", got.ResetDay)
	}

	// 去掉触发器后正常保存：重置时间与 ResetDay 应一同更新。
	if err := r.DB().Exec(`DROP TRIGGER fail_save_reset_day`).Error; err != nil {
		t.Fatal(err)
	}
	res = postJSONToHandler(t, h.subscriptionSave, map[string]interface{}{
		"id": s.ID, "name": "xp324-sub", "sourceAll": true, "resetDay": 15,
	})
	out = decodeCode(t, res.Body.String())
	if out.Code != 0 {
		t.Fatalf("正常保存应成功, got %s", res.Body.String())
	}
	got, err = r.GetSubscription(s.ID)
	if err != nil || got == nil {
		t.Fatalf("get sub: %v", err)
	}
	if got.ResetDay != 15 || got.TrafficResetTime == 1111111111111 {
		t.Fatalf("正常保存后 ResetDay/重置时间应一同更新: %+v", got)
	}
}
