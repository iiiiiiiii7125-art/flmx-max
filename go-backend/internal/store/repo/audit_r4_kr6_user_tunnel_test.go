package repo

import (
	"path/filepath"
	"testing"

	"go-backend/internal/store/model"
)

// R4B02-001: ListUserAccessibleTunnels must apply the same status gate as the
// enforcement point (UserTunnelExistsByUserAndTunnel): a disabled grant
// (user_tunnel.status=0) must not appear in the user's operable tunnel list.
func TestR4KR6_ListUserAccessibleTunnelsFiltersDisabledGrants(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "r4_kr6_user_tunnel.db")
	r, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	mkTunnel := func(name string, status int) int64 {
		tn := &model.Tunnel{Name: name, Type: 1, Flow: 0, Status: status}
		if err := r.db.Create(tn).Error; err != nil {
			t.Fatalf("create tunnel %s: %v", name, err)
		}
		return tn.ID
	}
	enabledTunnel := mkTunnel("enabled-tunnel", 1)
	disabledGrantTunnel := mkTunnel("disabled-grant-tunnel", 1)
	disabledTunnel := mkTunnel("disabled-tunnel", 0)

	const userID = int64(42)
	grants := []model.UserTunnel{
		{UserID: userID, TunnelID: enabledTunnel, Status: 1},
		{UserID: userID, TunnelID: disabledGrantTunnel, Status: 0},
		{UserID: userID, TunnelID: disabledTunnel, Status: 1},
		{UserID: 43, TunnelID: disabledGrantTunnel, Status: 1},
	}
	for i := range grants {
		if err := r.db.Create(&grants[i]).Error; err != nil {
			t.Fatalf("create grant %+v: %v", grants[i], err)
		}
	}

	items, err := r.ListUserAccessibleTunnels(userID)
	if err != nil {
		t.Fatalf("ListUserAccessibleTunnels: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected exactly 1 accessible tunnel, got %d: %v", len(items), items)
	}
	if got, _ := items[0]["id"].(int64); got != enabledTunnel {
		t.Fatalf("expected tunnel %d, got %v", enabledTunnel, items[0]["id"])
	}
	if got, _ := items[0]["name"].(string); got != "enabled-tunnel" {
		t.Fatalf("expected name enabled-tunnel, got %v", items[0]["name"])
	}

	// The other user's enabled grant on the same tunnel is unaffected.
	other, err := r.ListUserAccessibleTunnels(43)
	if err != nil {
		t.Fatalf("ListUserAccessibleTunnels(43): %v", err)
	}
	if len(other) != 1 {
		t.Fatalf("expected 1 accessible tunnel for user 43, got %d: %v", len(other), other)
	}
	if got, _ := other[0]["id"].(int64); got != disabledGrantTunnel {
		t.Fatalf("expected tunnel %d for user 43, got %v", disabledGrantTunnel, other[0]["id"])
	}
}
