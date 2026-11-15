package handler

import (
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// ---- C-(6)③：dailyQuotaGB / monthlyQuotaGB 只认整数 ----
//
// 0.01 / 1.5 报「参数无效」，整数 1 通过；文档与 UI 均无整数说明，
// ParseInt fail-closed 是有意但未言明、报错无指引。修法：接受小数
// GB（0.01 GB ≈ 10 MB），限额换算截断到字节，执法按换算后的字节
// 数判定；「abc」仍 fail-closed。

func TestC6DecimalQuotaAccepted(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/c6-decimal-quota.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	// Create 面：0.01 / 1.5 必须通过并落库。
	res := postJSONToHandler(t, h.userCreate, map[string]interface{}{
		"user": "c6-decimal-user", "pwd": "c6-password",
		"dailyQuotaGB": 0.01, "monthlyQuotaGB": 1.5,
	})
	if code, _ := decodeEnvelopeA204(t, res); code != 0 {
		t.Fatalf("C-(6)③: userCreate with decimal quotas must succeed, got body=%s", res.Body.String())
	}
	var userID int64
	if err := r.DB().Raw(`SELECT id FROM "user" WHERE "user" = ?`, "c6-decimal-user").Scan(&userID).Error; err != nil || userID == 0 {
		t.Fatalf("C-(6)③: created user must be findable: id=%d err=%v", userID, err)
	}
	view, err := r.GetUserQuotaView(userID, time.Now())
	if err != nil || view == nil {
		t.Fatalf("C-(6)③: quota view: %v / %v", view, err)
	}
	if view.DailyLimitGB != 0.01 || view.MonthlyLimitGB != 1.5 {
		t.Fatalf("C-(6)③: stored limits = %v/%v GB, want 0.01/1.5", view.DailyLimitGB, view.MonthlyLimitGB)
	}
	if got := repo.QuotaLimitBytes(0.01); got != 10737418 {
		t.Fatalf("C-(6)③: QuotaLimitBytes(0.01) = %d, want 10737418", got)
	}
	if got := repo.QuotaLimitBytes(1.5); got != 1610612736 {
		t.Fatalf("C-(6)③: QuotaLimitBytes(1.5) = %d, want 1610612736", got)
	}

	// Update 面：改到 1.5 / 0.01 同样通过并落库。
	res = postJSONToHandler(t, h.userUpdate, map[string]interface{}{
		"id": userID, "user": "c6-decimal-user",
		"dailyQuotaGB": 1.5, "monthlyQuotaGB": 0.01,
	})
	if code, _ := decodeEnvelopeA204(t, res); code != 0 {
		t.Fatalf("C-(6)③: userUpdate with decimal quotas must succeed, got body=%s", res.Body.String())
	}
	view, err = r.GetUserQuotaView(userID, time.Now())
	if err != nil || view == nil {
		t.Fatalf("C-(6)③: quota view after update: %v / %v", view, err)
	}
	if view.DailyLimitGB != 1.5 || view.MonthlyLimitGB != 0.01 {
		t.Fatalf("C-(6)③: updated limits = %v/%v GB, want 1.5/0.01", view.DailyLimitGB, view.MonthlyLimitGB)
	}

	// 「abc」仍 fail-closed：报错、旧值不动。
	res = postJSONToHandler(t, h.userUpdate, map[string]interface{}{
		"id": userID, "user": "c6-decimal-user", "dailyQuotaGB": "abc",
	})
	if code, _ := decodeEnvelopeA204(t, res); code == 0 {
		t.Fatalf("C-(6)③: userUpdate with unparsable dailyQuotaGB must fail, got success")
	}
	view, err = r.GetUserQuotaView(userID, time.Now())
	if err != nil || view == nil {
		t.Fatalf("C-(6)③: quota view after rejected update: %v / %v", view, err)
	}
	if view.DailyLimitGB != 1.5 {
		t.Fatalf("C-(6)③: rejected update must not rewrite the limit, got %v want 1.5", view.DailyLimitGB)
	}

	// 执法：0.01 GB 限额下，用量到 10737418 B 即超限。
	if err := r.DB().Exec(`UPDATE user_quota SET daily_limit_gb = 0.01, daily_used_bytes = 10737418 WHERE user_id = ?`, userID).Error; err != nil {
		t.Fatal(err)
	}
	view, err = r.GetUserQuotaView(userID, time.Now())
	if err != nil || view == nil {
		t.Fatalf("C-(6)③: quota view for enforcement: %v / %v", view, err)
	}
	if !isUserQuotaExceeded(view) {
		t.Fatalf("C-(6)③: 0.01 GB limit with 10737418 B used must judge exceeded: %+v", view)
	}
	// 差 1 B 未到限额：不超限（截断换算的边界）。
	if err := r.DB().Exec(`UPDATE user_quota SET daily_used_bytes = 10737417 WHERE user_id = ?`, userID).Error; err != nil {
		t.Fatal(err)
	}
	view, err = r.GetUserQuotaView(userID, time.Now())
	if err != nil || view == nil {
		t.Fatalf("C-(6)③: quota view below limit: %v / %v", view, err)
	}
	if isUserQuotaExceeded(view) {
		t.Fatalf("C-(6)③: 10737417 B under a 0.01 GB limit must not judge exceeded: %+v", view)
	}
}
