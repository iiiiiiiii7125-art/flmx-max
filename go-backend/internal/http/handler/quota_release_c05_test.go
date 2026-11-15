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

// C-05 (= D-01): 重置用户配额转发不恢复还报“已恢复”。
// 旧实现在 ResetUserQuotaUsage 同事务清 PausedForwardIDs，释放时
// ensureUserTunnelForwardAllowed 被隧道流量门否决又静默 continue，
// agent 侧 ResumeService 命令数为 0，回执与“已恢复”通知照发，且失败
// 释放永无重试（reconcile 只管仍超限用户）。

// c05SeedBlockedUser creates a quota-blocked user with one paused forward
// on tunnel 41. When gateExhausted is true the user_tunnel flow is
// exhausted (1GB limit, 1GiB used) so ensureUserTunnelForwardAllowed
// vetoes the release (V4).
func c05SeedBlockedUser(t *testing.T, r *repo.Repository, now time.Time, gateExhausted bool) (userID, forwardID int64) {
	t.Helper()
	nowMs := now.UnixMilli()
	userID, err := r.CreateUser("c05-user", "hash", 1, 0, 0, 0, 10, 1, 0, nowMs)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := r.DB().Exec(`INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, inx)
		VALUES(41, 'c05-tunnel', 1.0, 1, 'tls', 2, ?, ?, 1, 0)`, nowMs, nowMs).Error; err != nil {
		t.Fatalf("insert tunnel: %v", err)
	}
	utInFlow := int64(0)
	utFlow := int64(100000)
	if gateExhausted {
		utFlow = 1
		utInFlow = int64(1) << 30
	}
	if err := r.DB().Exec(`INSERT INTO user_tunnel(user_id, tunnel_id, num, flow, flow_mib, in_flow, out_flow, flow_reset_time, exp_time, status)
		VALUES(?, 41, 10, ?, 0, ?, 0, 1, ?, 1)`, userID, utFlow, utInFlow, nowMs+86400000).Error; err != nil {
		t.Fatalf("insert user_tunnel: %v", err)
	}
	if err := r.DB().Exec(`INSERT INTO forward(user_id, user_name, name, tunnel_id, remote_addr, strategy, in_flow, out_flow, created_time, updated_time, status, inx)
		VALUES(?, 'c05-user', 'c05-fwd', 41, '203.0.113.1:443', 'fifo', 0, 0, ?, ?, 0, 0)`, userID, nowMs, nowMs).Error; err != nil {
		t.Fatalf("insert forward: %v", err)
	}
	forwardID = mustLastInsertID(t, r, "c05-fwd")
	if _, err := r.AddUserQuotaUsage(userID, 0, now); err != nil {
		t.Fatalf("init quota row: %v", err)
	}
	if err := r.DB().Exec(`UPDATE user_quota SET daily_limit_gb = 1, monthly_limit_gb = 0, daily_used_bytes = ?, monthly_used_bytes = 0, disabled_by_quota = 1, disabled_at = ?, paused_forward_ids = ?
		WHERE user_id = ?`, int64(3)<<30, nowMs, fmt.Sprintf("%d", forwardID), userID).Error; err != nil {
		t.Fatalf("seed blocked quota: %v", err)
	}
	return userID, forwardID
}

func c05ForwardStatus(t *testing.T, r *repo.Repository, id int64) int {
	t.Helper()
	return mustQueryInt(t, r, `SELECT status FROM forward WHERE id = ?`, id)
}

func c05PausedList(t *testing.T, r *repo.Repository, userID int64) string {
	t.Helper()
	view, err := r.GetUserQuotaView(userID, time.Now())
	if err != nil || view == nil {
		t.Fatalf("quota view: %v / %v", view, err)
	}
	return view.PausedForwardIDs
}

// F1: 重置只清用量与禁用标志，恢复依据（PausedForwardIDs）在成功恢复前不得清除。
func TestC05ResetKeepsPausedForwardIDs(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	now := time.Now()
	userID, forwardID := c05SeedBlockedUser(t, r, now, false)

	release, err := r.ResetUserQuotaUsage(userID, "all", now)
	if err != nil {
		t.Fatalf("reset quota: %v", err)
	}
	if release == nil || !release.UnblockUser {
		t.Fatalf("expected unblocking release, got %+v", release)
	}
	if len(release.ForwardIDs) != 1 || release.ForwardIDs[0] != forwardID {
		t.Fatalf("release forward IDs = %v, want [%d]", release.ForwardIDs, forwardID)
	}
	view, err := r.GetUserQuotaView(userID, now)
	if err != nil || view == nil {
		t.Fatalf("quota view after reset: %v / %v", view, err)
	}
	if view.DisabledByQuota != 0 {
		t.Fatalf("disabled_by_quota = %d after reset, want 0", view.DisabledByQuota)
	}
	if view.PausedForwardIDs != fmt.Sprintf("%d", forwardID) {
		t.Fatalf("C-05 F1: PausedForwardIDs = %q after reset, want %q (restore basis must survive the reset)", view.PausedForwardIDs, fmt.Sprintf("%d", forwardID))
	}
}

// F2 (V4): 隧道流量门否决须记日志、进失败集、转发保持暂停且留在待恢复清单。
func TestC05ReleaseTunnelTrafficGateFailsLoudAndStaysPending(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now()
	userID, forwardID := c05SeedBlockedUser(t, r, now, true)

	release, err := r.ResetUserQuotaUsage(userID, "all", now)
	if err != nil {
		t.Fatalf("reset quota: %v", err)
	}
	result := h.applyUserQuotaRelease(release, now.UnixMilli())
	if len(result.Failed) != 1 || result.Failed[0].Reason != "tunnel_traffic_gate" || result.Failed[0].ForwardID != forwardID {
		t.Fatalf("C-05 F2 V4: release result = %+v, want one tunnel_traffic_gate failure for forward %d", result, forwardID)
	}
	if len(result.Resumed) != 0 {
		t.Fatalf("C-05 F2 V4: resumed = %v, want none", result.Resumed)
	}
	if got := c05ForwardStatus(t, r, forwardID); got != 0 {
		t.Fatalf("forward %d status = %d after vetoed release, want 0 (still paused)", forwardID, got)
	}
	if got := c05PausedList(t, r, userID); got != fmt.Sprintf("%d", forwardID) {
		t.Fatalf("C-05 F2 V4: PausedForwardIDs = %q after vetoed release, want %q", got, fmt.Sprintf("%d", forwardID))
	}
}

// F2 (V1): 已不存在的转发行是终态——记日志并从待恢复清单摘除，不作失败、不永久挂起。
func TestC05ReleaseDropsGoneForward(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now()
	userID, forwardID := c05SeedBlockedUser(t, r, now, false)

	// 先走一次真实重置（清用量、解禁、保留待恢复清单），再在释放清单
	// 里混入一个重置后被删的转发行 ID，模拟重置与释放之间的删除竞态。
	release, err := r.ResetUserQuotaUsage(userID, "all", now)
	if err != nil {
		t.Fatalf("reset quota: %v", err)
	}
	release.ForwardIDs = append([]int64{999999}, release.ForwardIDs...)
	result := h.applyUserQuotaRelease(release, now.UnixMilli())
	if len(result.Dropped) != 1 || result.Dropped[0] != 999999 {
		t.Fatalf("C-05 F2 V1: dropped = %v, want [999999]", result.Dropped)
	}
	if len(result.Resumed) != 1 || result.Resumed[0] != forwardID {
		t.Fatalf("C-05 F2 V1: resumed = %v, want [%d]", result.Resumed, forwardID)
	}
	if len(result.Failed) != 0 {
		t.Fatalf("C-05 F2 V1: failed = %+v, want none", result.Failed)
	}
	if got := c05ForwardStatus(t, r, forwardID); got != 1 {
		t.Fatalf("forward %d status = %d after release, want 1", forwardID, got)
	}
	if got := c05PausedList(t, r, userID); got != "" {
		t.Fatalf("C-05 F2 V1: PausedForwardIDs = %q after full release, want empty", got)
	}
}

// F4: 解禁后释放失败的待恢复清单由分钟级 backstop 重试；门一清除即恢复，
// 且 ListPendingQuotaReleases 只列未再超限的待恢复用户。
func TestC05BackstopRetriesAfterGateClears(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now()
	userID, forwardID := c05SeedBlockedUser(t, r, now, true)

	release, err := r.ResetUserQuotaUsage(userID, "all", now)
	if err != nil {
		t.Fatalf("reset quota: %v", err)
	}
	if result := h.applyUserQuotaRelease(release, now.UnixMilli()); len(result.Failed) != 1 {
		t.Fatalf("setup: expected one failed release, got %+v", result)
	}
	pending, err := r.ListPendingQuotaReleases(now)
	if err != nil {
		t.Fatalf("list pending releases: %v", err)
	}
	if len(pending) != 1 || pending[0].UserID != userID {
		t.Fatalf("C-05 F4: pending releases = %+v, want one for user %d", pending, userID)
	}
	// 门清除后 backstop 应恢复转发并清空清单。
	if err := r.DB().Exec(`UPDATE user_tunnel SET in_flow = 0 WHERE user_id = ? AND tunnel_id = 41`, userID).Error; err != nil {
		t.Fatalf("clear gate: %v", err)
	}
	h.reconcilePendingQuotaReleases(now)
	if got := c05ForwardStatus(t, r, forwardID); got != 1 {
		t.Fatalf("forward %d status = %d after backstop retry, want 1", forwardID, got)
	}
	if got := c05PausedList(t, r, userID); got != "" {
		t.Fatalf("C-05 F4: PausedForwardIDs = %q after backstop retry, want empty", got)
	}
	pending, err = r.ListPendingQuotaReleases(now)
	if err != nil {
		t.Fatalf("list pending releases: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("C-05 F4: pending releases = %+v after retry, want none", pending)
	}
}

// F3: 手动重置遇部分恢复失败时，回执 code 仍为 0（重置本身完成），但须带
// warning 与 failedForwardIds 点名仍暂停的转发，且不得发“已恢复”通知；
// 门清除后 backstop 补齐恢复并补发通知。
func TestC05ResetReceiptNamesStillPausedForwards(t *testing.T) {
	wh := newR171B5CountingWebhook(t)
	h, r := r171b5SetupQuotaRecoveryHandler(t, wh)
	now := time.Now()
	userID, forwardID := c05SeedBlockedUser(t, r, now, true)

	body := fmt.Sprintf(`{"userId":%d,"scope":"all"}`, userID)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/quota/reset", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.userQuotaReset(rec, req)
	var env struct {
		Code    int    `json:"code"`
		Msg     string `json:"msg"`
		Warning string `json:"warning"`
		Data    struct {
			ResumedForwardIDs []int64                `json:"resumedForwardIds"`
			FailedForwardIDs  []int64                `json:"failedForwardIds"`
			Failed            []quotaReleaseFailure  `json:"failed"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("decode reset response: %v (body %q)", err, rec.Body.String())
	}
	if env.Code != 0 {
		t.Fatalf("reset returned code %d (msg %q), want 0 (reset itself succeeded)", env.Code, env.Msg)
	}
	if env.Warning == "" {
		t.Fatalf("C-05 F3: partial release must carry a warning, got none (body %q)", rec.Body.String())
	}
	if len(env.Data.FailedForwardIDs) != 1 || env.Data.FailedForwardIDs[0] != forwardID {
		t.Fatalf("C-05 F3: failedForwardIds = %v, want [%d]", env.Data.FailedForwardIDs, forwardID)
	}
	if len(env.Data.Failed) != 1 || env.Data.Failed[0].Reason != "tunnel_traffic_gate" {
		t.Fatalf("C-05 F3: failed = %+v, want one tunnel_traffic_gate entry", env.Data.Failed)
	}
	if ids := r171b5PendingRecoveryUserIDs(t, h); len(ids) != 0 {
		t.Fatalf("C-05 F3: recovery notice must not be marked on partial release, pending = %v", ids)
	}
	if got := wh.count(); got != 0 {
		t.Fatalf("C-05 F3: no recovery notice may be delivered on partial release, got %d", got)
	}
	// 门清除后 backstop 补齐：转发恢复、清单清空、“已恢复”通知补发。
	if err := r.DB().Exec(`UPDATE user_tunnel SET in_flow = 0 WHERE user_id = ? AND tunnel_id = 41`, userID).Error; err != nil {
		t.Fatalf("clear gate: %v", err)
	}
	h.reconcilePendingQuotaReleases(now)
	wh.waitDeliveries(t, 1, 5*time.Second)
	if titles := wh.titles(); len(titles) != 1 || titles[0] != "用户配额已恢复" {
		t.Fatalf("C-05 F4: titles = %v, want [用户配额已恢复]", titles)
	}
	if got := c05ForwardStatus(t, r, forwardID); got != 1 {
		t.Fatalf("forward %d status = %d after backstop retry, want 1", forwardID, got)
	}
}
