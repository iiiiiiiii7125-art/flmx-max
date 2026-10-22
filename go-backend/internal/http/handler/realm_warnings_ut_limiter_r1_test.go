package handler

import (
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R1 X-P3-10 (L-01-3): the realm "limits do not take effect" warning must
// consider the user-tunnel level limiter, not only the forward's own
// fields. A forward with no own speed/conn limits whose user-tunnel
// binding carries an effective (enabled, speed > 0) limiter must still
// warn when synced to realm; once that rule is disabled it is no longer
// effective and the warning must disappear.
func TestSyncRealmForwardWarnsForUserTunnelLimiterOnly_R1XP310(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/realm-ut-warning.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	nodeID := seedRelayTargetNode(t, r, "entry", "198.51.100.21")
	tunnelID := seedTunnelForNftables(t, h, "realm-ut-tunnel", nodeID)
	if err := r.DB().Exec(`UPDATE tunnel SET engine = 'realm' WHERE id = ?`, tunnelID).Error; err != nil {
		t.Fatal(err)
	}

	sl := &model.SpeedLimit{Name: "ut-only-limit", Speed: 100, CreatedTime: time.Now().UnixMilli(), Status: 1}
	if err := r.DB().Create(sl).Error; err != nil {
		t.Fatalf("seed speed_limit: %v", err)
	}
	if err := r.DB().Exec(`
		INSERT INTO user_tunnel(user_id, tunnel_id, speed_id, num, flow, in_flow, out_flow, flow_reset_time, exp_time, status)
		VALUES(1, ?, ?, 99999, 99999, 0, 0, 1, 2727251700000, 1)
	`, tunnelID, sl.ID).Error; err != nil {
		t.Fatalf("seed user_tunnel: %v", err)
	}

	forward := seedForwardForNftables(t, h, tunnelID, nodeID, "1.1.1.1:443", 20001)
	// Premise: the forward itself carries no limit settings at all.
	if forward.SpeedID.Valid || forward.IPSpeedID.Valid || forward.MaxConn > 0 || forward.IPMaxConn > 0 {
		t.Fatalf("test premise broken: forward has own limits: %+v", forward)
	}

	hasLimitWarning := func(warnings []string) bool {
		for _, w := range warnings {
			if strings.Contains(w, "限速") && strings.Contains(w, "不生效") {
				return true
			}
		}
		return false
	}

	warnings, err := h.syncRealmForward(forward, nil)
	if err != nil {
		t.Fatalf("syncRealmForward: %v", err)
	}
	if !hasLimitWarning(warnings) {
		t.Fatalf("UT-level limiter must produce realm limit warning, warnings=%v", warnings)
	}

	// The UT rule is no longer effective once disabled: no warning.
	if err := r.DB().Exec(`UPDATE speed_limit SET status = 0 WHERE id = ?`, sl.ID).Error; err != nil {
		t.Fatal(err)
	}
	warnings, err = h.syncRealmForward(forward, nil)
	if err != nil {
		t.Fatalf("syncRealmForward after disable: %v", err)
	}
	if hasLimitWarning(warnings) {
		t.Fatalf("disabled UT-level limiter must not produce realm limit warning, warnings=%v", warnings)
	}
}
