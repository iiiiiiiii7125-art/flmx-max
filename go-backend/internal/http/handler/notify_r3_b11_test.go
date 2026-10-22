package handler

import (
	"context"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R3 批 11 回归（R3X-030 / [I]-R3-S01）：
// user_quota 流量类告警的静默再武装（用量回落 clearAlerts）必须连带
// 清除该用户/订阅的 user_quota 指纹，与 kernel_error 静默恢复口径
// （A7-02 forget）对齐；否则回落后 30 分钟内同文案再越界被指纹抑制。
// 到期类子型（以 ExpTime 为键、无回落语义）指纹不得被回落清理波及。

const r3b11MiB = int64(1024 * 1024)

func insertR3B11User(t *testing.T, h *Handler, name string, limitMiB, usedMiB int64) {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := h.repo.DB().Exec(`INSERT INTO user(user, pwd, role_id, exp_time, flow, flow_mib, in_flow, out_flow, flow_reset_time, num, created_time, status)
		VALUES(?, 'x', 1, 0, 0, ?, ?, 0, 0, 10, ?, 1)`, name, limitMiB, usedMiB*r3b11MiB, now).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}
}

func setR3B11UserUsed(t *testing.T, h *Handler, name string, usedMiB int64) {
	t.Helper()
	if err := h.repo.DB().Exec(`UPDATE user SET in_flow = ? WHERE user = ?`, usedMiB*r3b11MiB, name).Error; err != nil {
		t.Fatalf("update user flow: %v", err)
	}
}

// Probe3 转正（用户面）：送达 → 回落 → 同文案再越界，第二次必须真实投递。
func TestR3B11UserQuotaRearmDeliversAgain(t *testing.T) {
	wh := newFixdWebhook(t)
	h, _ := setupFixdNotifyHandler(t, wh)
	now := time.Now()
	// 100 MiB 上限、已用 95 MiB（95%）：warn 子型，文案两次越界逐字相同。
	insertR3B11User(t, h, "r3b11-alice", 100, 95)

	h.checkUserReminders(now)
	wh.waitCount(t, 1, 3*time.Second)
	if titles := wh.titles(); titles[0] != "用户流量即将用完" {
		t.Fatalf("titles = %v", titles)
	}

	// 用量回落到阈值以下：default 分支 clearAlerts 再武装。
	setR3B11UserUsed(t, h, "r3b11-alice", 10)
	h.checkUserReminders(now.Add(time.Minute))
	time.Sleep(200 * time.Millisecond)
	if got := wh.count(); got != 1 {
		t.Fatalf("fallback must not deliver, got %d", got)
	}

	// 同文案再越界：修复前被指纹 Suppressed（计数恒 1），修复后须达 2。
	setR3B11UserUsed(t, h, "r3b11-alice", 95)
	h.checkUserReminders(now.Add(2 * time.Minute))
	wh.waitCount(t, 2, 3*time.Second)
	if titles := wh.titles(); titles[1] != "用户流量即将用完" {
		t.Fatalf("titles = %v", titles)
	}
}

// Probe3 转正（订阅面）：checkSubQuota 同型——送达 → 回落 → 再越界达 2。
func TestR3B11SubQuotaRearmDeliversAgain(t *testing.T) {
	wh := newFixdWebhook(t)
	h, _ := setupFixdNotifyHandler(t, wh)
	s := &model.Subscription{
		ID: 42, Name: "r3b11-sub", Kind: model.SubscriptionShare,
		Enabled: 1, TrafficLimit: 100 * r3b11MiB, UploadBytes: 95 * r3b11MiB,
	}

	h.checkSubQuota(s)
	wh.waitCount(t, 1, 3*time.Second)

	s.UploadBytes = 10 * r3b11MiB
	h.checkSubQuota(s)
	time.Sleep(200 * time.Millisecond)
	if got := wh.count(); got != 1 {
		t.Fatalf("fallback must not deliver, got %d", got)
	}

	s.UploadBytes = 95 * r3b11MiB
	h.checkSubQuota(s)
	wh.waitCount(t, 2, 3*time.Second)
	if titles := wh.titles(); titles[1] != "分享订阅流量即将用完" {
		t.Fatalf("titles = %v", titles)
	}
}

// 防过修：无回落的持续越界在 30 分钟窗内仍被抑制（防抖不破）。
// 经真实检查路径连查两次计数恒 1，且同指纹直派仍 Suppressed。
func TestR3B11UserQuotaSustainedCrossingStillSuppressed(t *testing.T) {
	wh := newFixdWebhook(t)
	h, _ := setupFixdNotifyHandler(t, wh)
	now := time.Now()
	insertR3B11User(t, h, "r3b11-bob", 100, 95)

	h.checkUserReminders(now)
	wh.waitCount(t, 1, 3*time.Second)
	h.checkUserReminders(now.Add(time.Minute))
	h.checkUserReminders(now.Add(2 * time.Minute))
	time.Sleep(300 * time.Millisecond)
	if got := wh.count(); got != 1 {
		t.Fatalf("sustained crossing without fallback must stay suppressed, got %d", got)
	}
	// 指纹防抖本身仍在：同文案直派仍被抑制。
	title, msg := "用户流量即将用完", "用户：r3b11-bob\n已用：95.00 MB / 100.00 MB（95%）"
	if out := h.dispatchNotify(context.Background(), notifyEventUserQuota, title, msg); out != notifyOutcomeSuppressed {
		t.Fatalf("direct re-dispatch outcome = %v, want Suppressed", out)
	}
	if got := wh.count(); got != 1 {
		t.Fatalf("suppressed dispatch delivered, got %d", got)
	}
}

// 到期类对照：流量回落清理不得波及到期类子型指纹——回落后同文案
// 到期通知直派仍被抑制（到期类以 ExpTime 为键、无回落语义，不动）。
func TestR3B11ExpiryFingerprintSurvivesFlowFallback(t *testing.T) {
	wh := newFixdWebhook(t)
	h, _ := setupFixdNotifyHandler(t, wh)
	now := time.Now()

	// 用户到期子型先送达一次（同步直派，锁定其指纹）。
	userExpTitle, userExpMsg := "用户即将到期", "用户：r3b11-carol\n到期：2026-10-25 10:00（剩余约 2 天）"
	if out := h.dispatchNotify(context.Background(), notifyEventUserQuota, userExpTitle, userExpMsg); out != notifyOutcomeSent {
		t.Fatalf("user expiry first dispatch = %v, want Sent", out)
	}
	// 订阅到期子型先送达一次。
	subExpTitle, subExpMsg := "分享订阅即将到期", "订阅：r3b11-subexp\n到期：2026-10-25 10:00（剩余约 2 天）"
	if out := h.dispatchNotify(context.Background(), notifyEventUserQuota, subExpTitle, subExpMsg); out != notifyOutcomeSent {
		t.Fatalf("sub expiry first dispatch = %v, want Sent", out)
	}
	if got := wh.count(); got != 2 {
		t.Fatalf("expiry deliveries = %d, want 2", got)
	}

	// 触发两处回落清理：该用户流量处于低位（default 分支）、该订阅
	// 流量处于低位（checkSubQuota default 分支）。
	insertR3B11User(t, h, "r3b11-carol", 100, 10)
	h.checkUserReminders(now)
	sub := &model.Subscription{
		ID: 43, Name: "r3b11-subexp", Kind: model.SubscriptionShare,
		Enabled: 1, TrafficLimit: 100 * r3b11MiB, UploadBytes: 10 * r3b11MiB,
	}
	h.checkSubQuota(sub)
	time.Sleep(200 * time.Millisecond)
	if got := wh.count(); got != 2 {
		t.Fatalf("fallback must not deliver, got %d", got)
	}

	// 到期指纹必须仍在：同文案再派仍 Suppressed。
	if out := h.dispatchNotify(context.Background(), notifyEventUserQuota, userExpTitle, userExpMsg); out != notifyOutcomeSuppressed {
		t.Fatalf("user expiry re-dispatch after flow fallback = %v, want Suppressed (expiry fingerprint must survive)", out)
	}
	if out := h.dispatchNotify(context.Background(), notifyEventUserQuota, subExpTitle, subExpMsg); out != notifyOutcomeSuppressed {
		t.Fatalf("sub expiry re-dispatch after flow fallback = %v, want Suppressed (expiry fingerprint must survive)", out)
	}
	if got := wh.count(); got != 2 {
		t.Fatalf("expiry re-dispatch delivered after fallback, got %d", got)
	}
}
