package repo

import (
	"path/filepath"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R7-G-02 (restore face): importUserTunnels is the third write path for
// user_tunnel status / flowResetTime. It persisted out-of-set values
// verbatim while importUsers already gates user.status {0,1} with
// skip + named warning (R5X-009). Same caliber here: a user_tunnel row
// whose status is outside {0,1} or whose flowResetTime is outside
// 0-31 is skipped with a warning, never persisted; in-set rows restore
// unchanged.

func TestR7G02ImportUserTunnelsLegalSetGate(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "r7-g02-import.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()
	now := time.Now().UnixMilli()

	backup := &model.BackupData{
		Version: "1.0",
		Users: []model.UserBackup{{
			ID: 55, User: "r7g02-import-user", Pwd: "3c85cdebade1c51cf64ca9f3c09d182d",
			RoleID: 1, ExpTime: 2727251700000, Flow: 100, FlowResetTime: 1, Num: 10,
			CreatedTime: now, UpdatedTime: now, Status: 1,
		}},
		Tunnels: []model.TunnelBackup{
			{ID: 66, Name: "r7g02-tunnel-a", TrafficRatio: 1, Type: 1, Protocol: "tls",
				Flow: 1, CreatedTime: now, UpdatedTime: now, Status: 1},
			{ID: 67, Name: "r7g02-tunnel-b", TrafficRatio: 1, Type: 1, Protocol: "tls",
				Flow: 1, CreatedTime: now, UpdatedTime: now, Status: 1},
			{ID: 68, Name: "r7g02-tunnel-c", TrafficRatio: 1, Type: 1, Protocol: "tls",
				Flow: 1, CreatedTime: now, UpdatedTime: now, Status: 1},
		},
		UserTunnels: []model.UserTunnelBackup{
			{ID: 71, UserID: 55, TunnelID: 66, Num: 1, Flow: 10, FlowResetTime: 99, ExpTime: 0, Status: 1},
			{ID: 72, UserID: 55, TunnelID: 67, Num: 1, Flow: 10, FlowResetTime: 1, ExpTime: 0, Status: 99},
			{ID: 73, UserID: 55, TunnelID: 68, Num: 1, Flow: 10, FlowResetTime: 31, ExpTime: 0, Status: 0},
		},
	}
	result, err := r.Import(backup, []string{"users", "tunnels", "userTunnels"})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if result.UserTunnelsImported != 1 {
		t.Fatalf("UserTunnelsImported = %d, want 1 (only the in-set row)", result.UserTunnelsImported)
	}
	if got := result.SkippedRows["userTunnels"]; got != 2 {
		t.Fatalf("SkippedRows[userTunnels] = %d, want 2", got)
	}
	var count int
	if err := r.DB().Raw(`SELECT COUNT(*) FROM user_tunnel WHERE id IN (71, 72)`).Scan(&count).Error; err != nil {
		t.Fatalf("count skipped rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("out-of-set user_tunnel rows persisted: %d", count)
	}
	row, err := r.GetUserTunnelByID(73)
	if err != nil || row == nil {
		t.Fatalf("in-set row 73 missing: %v / %v", row, err)
	}
	if row.Status != 0 || row.FlowResetTime != 31 {
		t.Fatalf("in-set row landed status=%d flowResetTime=%d, want 0/31", row.Status, row.FlowResetTime)
	}
}
