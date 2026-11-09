package handler

import (
	"path/filepath"
	"testing"

	"go-backend/internal/store/repo"
)

func TestSpeedLimiterExistsPreservesForwardRuleLimiter(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	if err := r.DB().Exec(`
		INSERT INTO speed_limit(id, name, speed, created_time, status)
		VALUES(3, 'ip-limit', 100, 1, 1),
		       (4, 'ip-limit-disabled', 100, 1, 0)
	`).Error; err != nil {
		t.Fatalf("insert speed_limit: %v", err)
	}
	if err := r.DB().Exec(`
		INSERT INTO forward(id, user_id, user_name, name, tunnel_id, remote_addr, strategy, in_flow, out_flow, created_time, updated_time, status, inx, ip_speed_id)
		VALUES(8, 1, 'user', 'forward', 1, '127.0.0.1:80', 'fifo', 0, 0, 1, 1, 1, 0, 3),
		       (9, 1, 'user', 'forward-without-ip-limit', 1, '127.0.0.1:81', 'fifo', 0, 0, 1, 1, 1, 0, NULL),
		       (11, 1, 'user', 'forward-disabled-ip-limit', 1, '127.0.0.1:82', 'fifo', 0, 0, 1, 1, 1, 0, 4)
	`).Error; err != nil {
		t.Fatalf("insert forward: %v", err)
	}

	h := &Handler{repo: r}
	if !h.speedLimiterExists("rule_traffic_limit_8") {
		t.Fatal("expected runtime limiter for existing forward to be preserved")
	}
	// S1-F01：所绑限速规则被禁用（status=0）后条目必须可回收——
	// 前缀分支与数字分支谓词同形（都以 SpeedLimitExists 的
	// status=1 为准），旧实现只看 ip_speed_id 落值、永不回收。
	if h.speedLimiterExists("rule_traffic_limit_11") {
		t.Fatal("expected runtime limiter bound to a disabled speed limit rule to be treated as orphaned")
	}
	if h.speedLimiterExists("rule_traffic_limit_9") {
		t.Fatal("expected runtime limiter for forward without per-IP speed limit to be treated as orphaned")
	}
	if h.speedLimiterExists("rule_traffic_limit_10") {
		t.Fatal("expected runtime limiter for missing forward to be treated as orphaned")
	}
	if h.speedLimiterExists("rule_traffic_limit_invalid") {
		t.Fatal("expected malformed runtime limiter name to be treated as orphaned")
	}
}
