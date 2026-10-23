package handler

import (
	"bytes"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R4 批 8 回归（R4X-014 / [I]-R4-01）：订阅流量重置的两处 clearAlerts
// 调用点（周期重置 syncSubscriptions、手动重置 subscriptionResetTraffic）
// 只再武装 alertOnce、未连带清指纹——重置后 30 分钟指纹窗内同文案再越界
// 被指纹抑制、整轮不再告警。R4-I Probe3 转正：原探针以与旧调用点逐字
// 相同的 notifier 动作（单独 clearAlerts）模拟重置面；转正版改经真实
// 重置路径（handler 函数本身）驱动，断言不变（同文案再越界须第二次投递）。

func insertR4B8Sub(t *testing.T, h *Handler, name, token string, resetDay int, resetTime int64) *model.Subscription {
	t.Helper()
	now := time.Now().UnixMilli()
	s := &model.Subscription{
		Name: name, Kind: model.SubscriptionShare,
		Token:   token,
		Enabled: 1, AppliedActive: 1,
		TrafficLimit: 100 * r3b11MiB, UploadBytes: 95 * r3b11MiB,
		ResetDay: resetDay, TrafficResetTime: resetTime,
		CreatedTime: now, UpdatedTime: now,
	}
	if err := h.repo.SaveSubscription(s); err != nil {
		t.Fatalf("save subscription: %v", err)
	}
	got, err := h.repo.GetSubscription(s.ID)
	if err != nil || got == nil {
		t.Fatalf("reload subscription: %v", err)
	}
	return got
}

// 手动重置路径：送达 → subscriptionResetTraffic → 同文案再越界须达 2。
func TestR4B8ManualResetRearmDeliversAgain(t *testing.T) {
	wh := newFixdWebhook(t)
	h, _ := setupFixdNotifyHandler(t, wh)
	s := insertR4B8Sub(t, h, "r4b8-manual", "manual-token-r4b8-000000000001", 0, 0)

	h.checkSubQuota(s)
	wh.waitCount(t, 1, 3*time.Second)
	if titles := wh.titles(); titles[0] != "分享订阅流量即将用完" {
		t.Fatalf("titles = %v", titles)
	}

	// 经真实手动重置 handler（内部 ResetSubscriptionTraffic + clearAlerts）。
	body := bytes.NewBufferString(fmt.Sprintf(`{"id":%d}`, s.ID))
	req := httptest.NewRequest("POST", "/subscription/reset-traffic", body)
	rec := httptest.NewRecorder()
	h.subscriptionResetTraffic(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"code":0`) {
		t.Fatalf("manual reset response = %d %s", rec.Code, rec.Body.String())
	}
	if got, _ := h.repo.GetSubscription(s.ID); got == nil || subUsed(got) != 0 {
		t.Fatalf("reset must zero traffic, got %+v", got)
	}

	// 重置后在下一次低用量观测之前重新积累到逐字相同文案（95%）。
	s.UploadBytes = 95 * r3b11MiB
	h.checkSubQuota(s)
	wh.waitCount(t, 2, 3*time.Second)
	if titles := wh.titles(); titles[1] != "分享订阅流量即将用完" {
		t.Fatalf("titles = %v", titles)
	}
}

// 周期重置路径：送达 → syncSubscriptions（subDueReset 到期）→ 再越界须达 2。
func TestR4B8PeriodicResetRearmDeliversAgain(t *testing.T) {
	wh := newFixdWebhook(t)
	h, _ := setupFixdNotifyHandler(t, wh)
	now := time.Now()
	// ResetDay=1、上次重置早于本月 1 日 00:00 → 本轮 sync 到期重置。
	stale := lastResetAt(1, now).Add(-24 * time.Hour).UnixMilli()
	s := insertR4B8Sub(t, h, "r4b8-periodic", "periodic-token-r4b8-0000000002", 1, stale)

	h.checkSubQuota(s)
	wh.waitCount(t, 1, 3*time.Second)

	h.syncSubscriptions(now)
	if got, _ := h.repo.GetSubscription(s.ID); got == nil || subUsed(got) != 0 {
		t.Fatalf("periodic reset must zero traffic, got %+v", got)
	}

	s.UploadBytes = 95 * r3b11MiB
	h.checkSubQuota(s)
	wh.waitCount(t, 2, 3*time.Second)
	if titles := wh.titles(); titles[1] != "分享订阅流量即将用完" {
		t.Fatalf("titles = %v", titles)
	}
}

// 防过修对照：无重置的持续越界仍被指纹抑制（重置面修复不得拆防抖）。
func TestR4B8SustainedCrossingStillSuppressed(t *testing.T) {
	wh := newFixdWebhook(t)
	h, _ := setupFixdNotifyHandler(t, wh)
	s := insertR4B8Sub(t, h, "r4b8-sustained", "sustained-token-r4b8-00000003", 0, 0)

	h.checkSubQuota(s)
	wh.waitCount(t, 1, 3*time.Second)
	h.checkSubQuota(s)
	time.Sleep(300 * time.Millisecond)
	if got := wh.count(); got != 1 {
		t.Fatalf("sustained crossing without reset must stay suppressed, got %d", got)
	}
}
