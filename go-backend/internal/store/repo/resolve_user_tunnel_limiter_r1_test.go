package repo

import (
	"path/filepath"
	"testing"
)

// R1 X-P2-5 (F-01 = L-01-1): a disabled (status=0) speed_limit bound at the
// user-tunnel level must not resolve as an effective limiter, while the
// user-tunnel row itself must still be returned (LEFT JOIN semantics).
// Positive control: the same rule with status=1 resolves with its values.
func TestResolveUserTunnelAndLimiterDisabledSpeedLimit_R1XP25(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "r1-xp25.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer r.Close()

	if err := r.DB().Exec(`
		INSERT INTO speed_limit(id, name, speed, tunnel_id, tunnel_name, created_time, updated_time, status)
		VALUES(77, 'r1-disabled', 12345, NULL, NULL, 1, NULL, 0)
	`).Error; err != nil {
		t.Fatalf("seed speed_limit: %v", err)
	}
	if err := r.DB().Exec(`
		INSERT INTO user_tunnel(user_id, tunnel_id, speed_id, num, flow, in_flow, out_flow, flow_reset_time, exp_time, status)
		VALUES(11, 22, 77, 1, 0, 0, 0, 1, 2727251700000, 1)
	`).Error; err != nil {
		t.Fatalf("seed user_tunnel: %v", err)
	}
	var utID int64
	if err := r.DB().Raw(`SELECT id FROM user_tunnel WHERE user_id = 11 AND tunnel_id = 22`).Scan(&utID).Error; err != nil {
		t.Fatalf("load user_tunnel id: %v", err)
	}

	info, err := r.ResolveUserTunnelAndLimiter(11, 22)
	if err != nil {
		t.Fatalf("ResolveUserTunnelAndLimiter() error = %v", err)
	}
	if info == nil {
		t.Fatal("ResolveUserTunnelAndLimiter() returned nil info")
	}
	if info.UserTunnelID != utID {
		t.Fatalf("UserTunnelID = %d, want %d (user-tunnel row must still be returned)", info.UserTunnelID, utID)
	}
	if info.LimiterID != nil || info.Speed != nil {
		t.Fatalf("disabled speed_limit must not resolve: LimiterID=%v Speed=%v, want both nil", info.LimiterID, info.Speed)
	}

	// Positive control: enabling the same rule returns its original values.
	if err := r.DB().Exec(`UPDATE speed_limit SET status = 1 WHERE id = 77`).Error; err != nil {
		t.Fatalf("enable speed_limit: %v", err)
	}
	info, err = r.ResolveUserTunnelAndLimiter(11, 22)
	if err != nil {
		t.Fatalf("ResolveUserTunnelAndLimiter() after enable error = %v", err)
	}
	if info == nil || info.LimiterID == nil || *info.LimiterID != 77 || info.Speed == nil || *info.Speed != 12345 {
		t.Fatalf("enabled speed_limit must resolve to limiter 77 / speed 12345, got %+v", info)
	}
	if info.UserTunnelID != utID {
		t.Fatalf("UserTunnelID after enable = %d, want %d", info.UserTunnelID, utID)
	}
}
