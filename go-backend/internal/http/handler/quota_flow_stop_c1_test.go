package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// C-(1)（流量审计）：流停路径只写转发 status、从不写配额恢复清单，
// 配额执法又只列 active 转发——流停的转发在重置/backstop 眼里不
// 存在，用户见“重置成功但转发仍停”。修复：流停时把实际停住的
// 转发记入恢复清单；手动重置即使未解禁也尝试待恢复清单；backstop
// 本就覆盖 disabled=0 的待恢复行（F4 同款）。

// F1：流停记入是并集追加——已有清单保留、新 ID 并入、重复/空串无操作；
// 无配额行用户也能记（行按需创建，零限额即不限流，不阻塞任何事）。
func TestC1FlowStopRecordUnionsIntoRecoveryList(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now()
	userID, forwardID := c05SeedBlockedUser(t, r, now, false)

	// 第二条流停转发（status 0）。
	if err := r.DB().Exec(`INSERT INTO forward(user_id, user_name, name, tunnel_id, remote_addr, strategy, in_flow, out_flow, created_time, updated_time, status, inx)
		VALUES(?, 'c05-user', 'c05-fwd2', 41, '203.0.113.2:443', 'fifo', 0, 0, ?, ?, 0, 0)`, userID, now.UnixMilli(), now.UnixMilli()).Error; err != nil {
		t.Fatalf("insert second forward: %v", err)
	}
	forwardID2 := mustLastInsertID(t, r, "c05-fwd2")

	h.recordFlowStoppedForwards(userID, []int64{forwardID2})
	if got := c05PausedList(t, r, userID); got != fmt.Sprintf("%d,%d", forwardID, forwardID2) {
		t.Fatalf("C-(1): PausedForwardIDs = %q after record, want %q", got, fmt.Sprintf("%d,%d", forwardID, forwardID2))
	}
	// 重复记入与空记入均无操作。
	h.recordFlowStoppedForwards(userID, []int64{forwardID})
	if got := c05PausedList(t, r, userID); got != fmt.Sprintf("%d,%d", forwardID, forwardID2) {
		t.Fatalf("C-(1): duplicate record changed list to %q", got)
	}
	h.recordFlowStoppedForwards(userID, nil)
	if got := c05PausedList(t, r, userID); got != fmt.Sprintf("%d,%d", forwardID, forwardID2) {
		t.Fatalf("C-(1): empty record changed list to %q", got)
	}

	// 无配额行用户：记入时创建行。
	userID2, err := r.CreateUser("c1-user2", "hash", 1, 0, 0, 0, 10, 1, 0, now.UnixMilli())
	if err != nil {
		t.Fatalf("create user2: %v", err)
	}
	h.recordFlowStoppedForwards(userID2, []int64{forwardID})
	if got := c05PausedList(t, r, userID2); got != fmt.Sprintf("%d", forwardID) {
		t.Fatalf("C-(1): PausedForwardIDs for row-less user = %q, want %q", got, fmt.Sprintf("%d", forwardID))
	}
}

// F2：仅流停（从未被配额封停、未超限）的用户手动重置也恢复转发，
// 且发“已恢复”通知。注：全成功时回执是 OKEmpty（无 data 段，
// 与既有解禁成功路径同口径），点名义务落在失败腿（见下测）。
func TestC1ResetAttemptsFlowStoppedList(t *testing.T) {
	wh := newR171B5CountingWebhook(t)
	h, r := r171b5SetupQuotaRecoveryHandler(t, wh)
	now := time.Now()
	userID, forwardID := c05SeedBlockedUser(t, r, now, false)
	// 只剩流停：解禁标志与用量清零，待恢复清单保留。
	if err := r.DB().Exec(`UPDATE user_quota SET daily_used_bytes = 0, monthly_used_bytes = 0, disabled_by_quota = 0, disabled_at = 0 WHERE user_id = ?`, userID).Error; err != nil {
		t.Fatalf("seed flow-stop-only quota: %v", err)
	}
	if got := c05ForwardStatus(t, r, forwardID); got != 0 {
		t.Fatalf("setup: forward %d status = %d, want 0 (paused)", forwardID, got)
	}

	body := fmt.Sprintf(`{"userId":%d,"scope":"all"}`, userID)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/quota/reset", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.userQuotaReset(rec, req)
	var env struct {
		Code int `json:"code"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("decode reset response: %v (body %q)", err, rec.Body.String())
	}
	if env.Code != 0 {
		t.Fatalf("reset returned code %d, want 0 (body %q)", env.Code, rec.Body.String())
	}
	if got := c05ForwardStatus(t, r, forwardID); got != 1 {
		t.Fatalf("forward %d status = %d after reset, want 1", forwardID, got)
	}
	if got := c05PausedList(t, r, userID); got != "" {
		t.Fatalf("C-(1): PausedForwardIDs = %q after reset, want empty", got)
	}
	wh.waitDeliveries(t, 1, 5*time.Second)
	if titles := wh.titles(); len(titles) != 1 || titles[0] != "用户配额已恢复" {
		t.Fatalf("C-(1): titles = %v, want [用户配额已恢复]", titles)
	}
}

// F2b：流停转发在重置时遇隧道流量门否决，回执须带 warning 与
// failedForwardIds 点名（C-05 F3 同口径），转发保持暂停、清单
// 保留、不发“已恢复”通知。
func TestC1ResetNamesFlowStoppedOnGateVeto(t *testing.T) {
	wh := newR171B5CountingWebhook(t)
	h, r := r171b5SetupQuotaRecoveryHandler(t, wh)
	now := time.Now()
	userID, forwardID := c05SeedBlockedUser(t, r, now, true)
	if err := r.DB().Exec(`UPDATE user_quota SET daily_used_bytes = 0, monthly_used_bytes = 0, disabled_by_quota = 0, disabled_at = 0 WHERE user_id = ?`, userID).Error; err != nil {
		t.Fatalf("seed flow-stop-only quota: %v", err)
	}

	body := fmt.Sprintf(`{"userId":%d,"scope":"all"}`, userID)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/quota/reset", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.userQuotaReset(rec, req)
	var env struct {
		Code    int    `json:"code"`
		Warning string `json:"warning"`
		Data    struct {
			FailedForwardIDs []int64               `json:"failedForwardIds"`
			Failed           []quotaReleaseFailure `json:"failed"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("decode reset response: %v (body %q)", err, rec.Body.String())
	}
	if env.Code != 0 {
		t.Fatalf("reset returned code %d, want 0 (body %q)", env.Code, rec.Body.String())
	}
	if env.Warning == "" {
		t.Fatalf("C-(1): vetoed flow-stop release must carry a warning, got none (body %q)", rec.Body.String())
	}
	if len(env.Data.FailedForwardIDs) != 1 || env.Data.FailedForwardIDs[0] != forwardID {
		t.Fatalf("C-(1): failedForwardIds = %v, want [%d]", env.Data.FailedForwardIDs, forwardID)
	}
	if len(env.Data.Failed) != 1 || env.Data.Failed[0].Reason != "tunnel_traffic_gate" {
		t.Fatalf("C-(1): failed = %+v, want one tunnel_traffic_gate entry", env.Data.Failed)
	}
	if got := c05ForwardStatus(t, r, forwardID); got != 0 {
		t.Fatalf("forward %d status = %d after vetoed reset, want 0 (still paused)", forwardID, got)
	}
	if got := c05PausedList(t, r, userID); got != fmt.Sprintf("%d", forwardID) {
		t.Fatalf("C-(1): PausedForwardIDs = %q after vetoed reset, want %q", got, fmt.Sprintf("%d", forwardID))
	}
	if got := wh.count(); got != 0 {
		t.Fatalf("C-(1): no recovery notice may be delivered on vetoed release, got %d", got)
	}
}

// F3：backstop 覆盖 disabled=0 的待恢复行——流停转发即使无人手动
// 重置，也在分钟级维护轮里恢复。
func TestC1BackstopRetriesFlowStoppedOnlyUser(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now()
	userID, forwardID := c05SeedBlockedUser(t, r, now, false)
	if err := r.DB().Exec(`UPDATE user_quota SET daily_used_bytes = 0, monthly_used_bytes = 0, disabled_by_quota = 0, disabled_at = 0 WHERE user_id = ?`, userID).Error; err != nil {
		t.Fatalf("seed flow-stop-only quota: %v", err)
	}

	h.reconcilePendingQuotaReleases(now)
	if got := c05ForwardStatus(t, r, forwardID); got != 1 {
		t.Fatalf("forward %d status = %d after backstop, want 1", forwardID, got)
	}
	if got := c05PausedList(t, r, userID); got != "" {
		t.Fatalf("C-(1): PausedForwardIDs = %q after backstop, want empty", got)
	}
}

// F4：归因门——只有流量超限导致的暂停才进恢复清单；停用/过期
// 账号的暂停不进（否则 backstop 会对已过期账号每分钟重试恢复）。
func TestC1FlowStopAttribution(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now()
	userID, _ := c05SeedBlockedUser(t, r, now, false)
	nowMs := now.UnixMilli()

	// 用户行 flow=0（不限流，夹具默认值）→ 不归因。
	if h.userFlowStopAttributable(userID, nowMs) {
		t.Fatalf("user with no flow limit must not be attributable")
	}
	// 限额 10GB、用量 11GB → 归因。
	if err := r.DB().Exec(`UPDATE user SET flow = 10, in_flow = ? WHERE id = ?`, int64(11)<<30, userID).Error; err != nil {
		t.Fatalf("exhaust user flow: %v", err)
	}
	if !h.userFlowStopAttributable(userID, nowMs) {
		t.Fatalf("user over flow limit must be attributable")
	}
	// 停用 → 不归因。
	if err := r.DB().Exec(`UPDATE user SET status = 0 WHERE id = ?`, userID).Error; err != nil {
		t.Fatalf("disable user: %v", err)
	}
	if h.userFlowStopAttributable(userID, nowMs) {
		t.Fatalf("disabled user must not be attributable")
	}
	// 过期 → 不归因。
	if err := r.DB().Exec(`UPDATE user SET status = 1, exp_time = ? WHERE id = ?`, nowMs-3600000, userID).Error; err != nil {
		t.Fatalf("expire user: %v", err)
	}
	if h.userFlowStopAttributable(userID, nowMs) {
		t.Fatalf("expired user must not be attributable")
	}

	// 隧道授权半：超限且启用未过期 → 归因；任一条件翻转 → 不归因。
	base := &userTunnelPolicy{UserID: userID, TunnelID: 41, Status: 1, ExpTime: 0, Flow: 1, FlowMiB: 0, InFlow: int64(1) << 30, OutFlow: 0}
	if !userTunnelFlowStopAttributable(base, nowMs) {
		t.Fatalf("tunnel grant over flow limit must be attributable")
	}
	under := *base
	under.InFlow = 0
	if userTunnelFlowStopAttributable(&under, nowMs) {
		t.Fatalf("tunnel grant under flow limit must not be attributable")
	}
	disabled := *base
	disabled.Status = 0
	if userTunnelFlowStopAttributable(&disabled, nowMs) {
		t.Fatalf("disabled tunnel grant must not be attributable")
	}
	expired := *base
	expired.ExpTime = nowMs - 3600000
	if userTunnelFlowStopAttributable(&expired, nowMs) {
		t.Fatalf("expired tunnel grant must not be attributable")
	}
}
