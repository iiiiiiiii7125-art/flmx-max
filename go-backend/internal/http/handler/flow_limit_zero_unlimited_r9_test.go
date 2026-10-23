package handler

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// ─── R9X-001（批 1，限额语义收口）：总量限额 limit=0 必须读作「不限」───
// 裁定正本 audit-out-132/r9/R9-X-verdicts.md §0.1：写入面把 0 作合法值
// 接纳、同字段通知面以 limit<=0 读作无限额、同函数兄弟字段 Num/ExpTime
// 均以 >0 守卫（0=不限）；执法四点此前无守卫，flow=0/flowMiB=0 恒判超额。
// 本文件按「修复前失败、修复后通过」纪律编写，并含 limit>0 域正向对照
// （边界行为逐字不变）。

// 种子用量 2^40 B（1 TiB）：远大于耗尽对照的 1 GiB 限额、远小于放行
// 对照的 100000 GiB 限额；对 0 限额（不限）则无论多大都不应判超额。
const r9ZeroLimitHugeUsage = int64(1) << 40

// setupZeroLimitHandler 建一个带真实 repo 的 Handler；user 的总量限额由
// flow/flowMiB 指定，用量直接写库为 huge（in=huge, out=huge）。
func setupZeroLimitHandler(t *testing.T, username string, flow, flowMiB int64) (*Handler, *repo.Repository, int64) {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	nowMs := time.Now().UnixMilli()
	userID, err := r.CreateUser(username, "hash", 1, nowMs+86400000, flow, 1, 10, 1, 0, nowMs, flowMiB)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := r.DB().Exec(`UPDATE "user" SET in_flow = ?, out_flow = ? WHERE id = ?`,
		r9ZeroLimitHugeUsage, r9ZeroLimitHugeUsage, userID).Error; err != nil {
		t.Fatalf("seed user usage: %v", err)
	}
	return h, r, userID
}

func seedZeroLimitTunnel(t *testing.T, r *repo.Repository, tunnelID, userID, utFlow, utFlowMiB int64) {
	t.Helper()
	nowMs := time.Now().UnixMilli()
	if err := r.DB().Exec(`INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, inx)
		VALUES(?, 'r9-zero-limit-tunnel', 1.0, 1, 'tls', 2, ?, ?, 1, 0)`, tunnelID, nowMs, nowMs).Error; err != nil {
		t.Fatalf("insert tunnel: %v", err)
	}
	if err := r.DB().Exec(`INSERT INTO user_tunnel(user_id, tunnel_id, num, flow, flow_mib, in_flow, out_flow, flow_reset_time, exp_time, status)
		VALUES(?, ?, 10, ?, ?, ?, ?, 1, ?, 1)`, userID, tunnelID, utFlow, utFlowMiB,
		r9ZeroLimitHugeUsage, r9ZeroLimitHugeUsage, nowMs+86400000).Error; err != nil {
		t.Fatalf("insert user_tunnel: %v", err)
	}
}

// TestFlowLimitZeroUserNotPausedR9X001: shouldPauseUser 对 flow=0 的用户
// 在任意用量下不得判暂停（修复前 flowLimitBytes(0,0)=0 <= current 恒真）。
func TestFlowLimitZeroUserNotPausedR9X001(t *testing.T) {
	h, _, userID := setupZeroLimitHandler(t, "r9zero-user", 0, 0)
	if h.shouldPauseUser(userID, time.Now().UnixMilli()) {
		t.Fatal("R9X-001: user with flow=0/flowMiB=0 must not be paused (0 = unlimited)")
	}
}

// TestFlowLimitZeroUserTunnelNotPausedR9X001: shouldPauseUserTunnel 纯函数面，
// UT flow=0/flowMiB=0 + 任意用量不得暂停；limit>0 域对照保持 fail-closed。
func TestFlowLimitZeroUserTunnelNotPausedR9X001(t *testing.T) {
	now := time.Now().UnixMilli()
	policy := &userTunnelPolicy{
		Flow: 0, FlowMiB: 0,
		InFlow: r9ZeroLimitHugeUsage, OutFlow: r9ZeroLimitHugeUsage,
		Status: 1,
	}
	if shouldPauseUserTunnel(policy, now) {
		t.Fatal("R9X-001: user tunnel with flow=0/flowMiB=0 must not be paused (0 = unlimited)")
	}
	// 正向对照（limit>0 域逐字不变）：达到上限仍暂停、未达不暂停。
	limited := &userTunnelPolicy{Flow: 1, InFlow: bytesPerGB - 1, Status: 1}
	if shouldPauseUserTunnel(limited, now) {
		t.Fatal("limited policy paused before reaching 1 GiB")
	}
	limited.InFlow = bytesPerGB
	if !shouldPauseUserTunnel(limited, now) {
		t.Fatal("limited policy did not pause at 1 GiB (limit>0 boundary regressed)")
	}
}

// TestFlowLimitZeroForwardAllowedR9X001: ensureUserTunnelForwardAllowed 四格——
// user 段 0 / UT 段 0 / 两段皆 0 时均须放行且回执不得谎称超额；
// limit>0 且已用满时仍须以超额文案拒绝（正向对照）。
func TestFlowLimitZeroForwardAllowedR9X001(t *testing.T) {
	now := time.Now().UnixMilli()
	cases := []struct {
		name      string
		userFlow  int64
		utFlow    int64
		wantErr   bool
		wantQuota bool // 期望错误文案为流量超额类
	}{
		{"user zero, ut limited-large", 0, 100000, false, false},
		{"user limited-large, ut zero", 100000, 0, false, false},
		{"both zero", 0, 0, false, false},
		{"user limited and exhausted", 1, 100000, true, true},
		{"ut limited and exhausted", 100000, 1, true, true},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, r, userID := setupZeroLimitHandler(t, "r9zero-case", tc.userFlow, 0)
			// setup 助手把用户用量写为 huge；正向对照的「limited-large」段
			// 用量 huge 仍远小于 100000 GiB，不受影响；「exhausted」段
			// limit=1 GiB << huge 必超额。
			tunnelID := int64(900 + i)
			seedZeroLimitTunnel(t, r, tunnelID, userID, tc.utFlow, 0)
			err := h.ensureUserTunnelForwardAllowed(userID, tunnelID, now)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected rejection for exhausted limit, got nil")
				}
				if tc.wantQuota && !strings.Contains(err.Error(), "超额") {
					t.Fatalf("expected quota-exceeded message, got %q", err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("R9X-001: forward must be allowed (0 = unlimited), got %q", err.Error())
			}
		})
	}
}

// TestParseTrafficLimitAcceptsZeroR9X001: 写入面保留显式接纳 0——
// parseTrafficLimit 对 flow=0 / flowMiB=0 不得报错（不得以拒 0 代替修执法面）。
func TestParseTrafficLimitAcceptsZeroR9X001(t *testing.T) {
	flowGB, flowMiB, err := parseTrafficLimit(map[string]interface{}{"flow": float64(0)}, 100)
	if err != nil || flowGB != 0 || flowMiB != 0 {
		t.Fatalf("parseTrafficLimit(flow=0) = (%d, %d, %v), want (0, 0, nil)", flowGB, flowMiB, err)
	}
	flowGB, flowMiB, err = parseTrafficLimit(map[string]interface{}{"flowMiB": float64(0)}, 100)
	if err != nil || flowMiB != 0 {
		t.Fatalf("parseTrafficLimit(flowMiB=0) = (%d, %d, %v), want flowMiB=0, nil error", flowGB, flowMiB, err)
	}
	if got := flowLimitBytes(0, 0); got != 0 {
		t.Fatalf("flowLimitBytes(0,0) = %d, want 0 (helper must stay unchanged)", got)
	}
}
