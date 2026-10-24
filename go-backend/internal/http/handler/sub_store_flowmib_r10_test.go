package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"go-backend/internal/security"
	"go-backend/internal/store/repo"
)

// ─── R10X-005（批 5，T1）：openAPI sub_store 订阅头 total 必须经 FlowMiB ───
// 裁定正本 audit-out-132/r10/R10-X-verdicts.md §3.1 + R10-G-report §1.2
// 六格表：执法面 flowLimitBytes(Flow, FlowMiB) vs 订阅头旧读法 Flow×1GiB。
// 修复前亚 GB / 非整 GB / 备份可达对三格在 user 面（handler.go:910）与
// UT 面（:932）均分歧（红）；整 GB / 哨兵 / (0,0) 三格修复前后逐字不变
// （防过修对照）。(0,0) total=0 保留：与分享订阅面 TrafficLimit
// 0=不限量（kernel_relay.go 注记）同读法。

func parseSubscriptionHeaderTotal(t *testing.T, header string) int64 {
	t.Helper()
	for _, part := range strings.Split(header, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "total=") {
			v, err := strconv.ParseInt(strings.TrimPrefix(part, "total="), 10, 64)
			if err != nil {
				t.Fatalf("parse total from header %q: %v", header, err)
			}
			return v
		}
	}
	t.Fatalf("header %q has no total", header)
	return 0
}

func TestSubStoreHeaderTotalUsesFlowMiBR10X005(t *testing.T) {
	resetSubStoreLimiterForTest()
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	const password = "r10x005-probe-pass"
	hash, err := security.HashPassword(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	now := time.Now().UnixMilli()

	cases := []struct {
		name    string
		flow    int64
		flowMiB int64
	}{
		{"sub-GB (1,512)", 1, 512},
		{"non-integer-GB (2,1536)", 2, 1536},
		{"backup-reachable (0,512)", 0, 512},
		{"integer-GB (100,0)", 100, 0},
		{"sentinel (99999,0)", 99999, 0},
		{"zero (0,0)", 0, 0},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			username := fmt.Sprintf("r10x005user%d", i)
			userID, err := r.CreateUser(username, hash, 1, now+86400000, tc.flow, 1, 10, 1, 0, now, tc.flowMiB)
			if err != nil {
				t.Fatalf("create user: %v", err)
			}
			want := flowLimitBytes(tc.flow, tc.flowMiB)

			call := func(tunnel string, ipOctet int) string {
				t.Helper()
				req := httptest.NewRequest(http.MethodPost, "/api/v1/open_api/sub_store",
					strings.NewReader(`{"user":`+jsonQuote(username)+`,"password":`+jsonQuote(password)+`,"tunnel":`+jsonQuote(tunnel)+`}`))
				req.Header.Set("Content-Type", "application/json")
				req.RemoteAddr = fmt.Sprintf("198.51.100.%d:4000", ipOctet)
				w := httptest.NewRecorder()
				h.openAPISubStore(w, req)
				header := w.Header().Get("subscription-userinfo")
				if header == "" {
					t.Fatalf("sub_store tunnel=%s: no subscription-userinfo header, body=%q", tunnel, w.Body.String())
				}
				return header
			}

			// user 面（tunnel="-1"，原 handler.go:910）
			if got := parseSubscriptionHeaderTotal(t, call("-1", 10+i)); got != want {
				t.Errorf("user face total = %d, want flowLimitBytes(%d,%d) = %d", got, tc.flow, tc.flowMiB, want)
			}

			// UT 面（原 handler.go:932）：同 (Flow,FlowMiB) 的 user_tunnel 行
			tunnelID := int64(7000 + i)
			if err := r.DB().Exec(`INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, inx)
				VALUES(?, 'r10x005-tunnel', 1.0, 1, 'tls', 2, ?, ?, 1, 0)`, tunnelID, now, now).Error; err != nil {
				t.Fatalf("insert tunnel: %v", err)
			}
			if err := r.DB().Exec(`INSERT INTO user_tunnel(user_id, tunnel_id, num, flow, flow_mib, in_flow, out_flow, flow_reset_time, exp_time, status)
				VALUES(?, ?, 10, ?, ?, 0, 0, 1, ?, 1)`, userID, tunnelID, tc.flow, tc.flowMiB, now+86400000).Error; err != nil {
				t.Fatalf("insert user_tunnel: %v", err)
			}
			var utID int64
			if err := r.DB().Raw(`SELECT id FROM user_tunnel WHERE user_id = ? AND tunnel_id = ?`, userID, tunnelID).Scan(&utID).Error; err != nil || utID <= 0 {
				t.Fatalf("lookup user_tunnel id: id=%d err=%v", utID, err)
			}
			if got := parseSubscriptionHeaderTotal(t, call(strconv.FormatInt(utID, 10), 50+i)); got != want {
				t.Errorf("UT face total = %d, want flowLimitBytes(%d,%d) = %d", got, tc.flow, tc.flowMiB, want)
			}
		})
	}
}
