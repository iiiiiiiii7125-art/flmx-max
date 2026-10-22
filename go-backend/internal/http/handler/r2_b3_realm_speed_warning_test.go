package handler

import (
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R2X-034 (R2-F-05): realm 限速警告的转发级分支必须与 UT 级分支
// 同谓词——SpeedID/IPSpeedID 引用 status=0（已禁用）或 speed=0
// 的规则时该限速本就不生效，不得触发「限速不生效」警告；引用
// 有效规则仍警告；仅 MaxConn 仍警告（连接数是转发自有字段，
// 保持裸值判定）。
func TestR2B3_RealmForwardSpeedWarningUsesValidityPredicate(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/realm-fwd-warning.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	nodeID := seedRelayTargetNode(t, r, "entry", "198.51.100.22")
	tunnelID := seedTunnelForNftables(t, h, "realm-fwd-tunnel", nodeID)
	if err := r.DB().Exec(`UPDATE tunnel SET engine = 'realm' WHERE id = ?`, tunnelID).Error; err != nil {
		t.Fatal(err)
	}

	mkLimit := func(name string, speed, status int) int64 {
		t.Helper()
		sl := &model.SpeedLimit{Name: name, Speed: speed, CreatedTime: time.Now().UnixMilli(), Status: status}
		if err := r.DB().Create(sl).Error; err != nil {
			t.Fatalf("seed speed_limit %s: %v", name, err)
		}
		return sl.ID
	}
	disabledID := mkLimit("disabled-limit", 100, 0)
	zeroID := mkLimit("zero-limit", 0, 1)
	validID := mkLimit("valid-limit", 100, 1)

	hasLimitWarning := func(warnings []string) bool {
		for _, w := range warnings {
			if strings.Contains(w, "限速") && strings.Contains(w, "不生效") {
				return true
			}
		}
		return false
	}
	warnFor := func(forward *forwardRecord) bool {
		t.Helper()
		warnings, err := h.syncRealmForward(forward, nil)
		if err != nil {
			t.Fatalf("syncRealmForward: %v", err)
		}
		return hasLimitWarning(warnings)
	}
	setSpeedIDs := func(forwardID int64, speedID, ipSpeedID any) *forwardRecord {
		t.Helper()
		if err := r.DB().Exec(`UPDATE forward SET speed_id = ?, ip_speed_id = ? WHERE id = ?`, speedID, ipSpeedID, forwardID).Error; err != nil {
			t.Fatal(err)
		}
		fwd, err := h.getForwardRecord(forwardID)
		if err != nil || fwd == nil {
			t.Fatalf("reload forward %d: %v", forwardID, err)
		}
		return fwd
	}

	// 引用 status=0 与 speed=0 规则：无警告（修复前裸 ID 判定误报）。
	fDisabled := seedForwardForNftables(t, h, tunnelID, nodeID, "1.1.1.1:443", 20011)
	if warnFor(setSpeedIDs(fDisabled.ID, disabledID, zeroID)) {
		t.Fatalf("disabled/zero forward speed limits must not produce realm limit warning")
	}
	// 引用有效规则：仍警告。
	fValid := seedForwardForNftables(t, h, tunnelID, nodeID, "1.1.1.1:443", 20012)
	if !warnFor(setSpeedIDs(fValid.ID, validID, nil)) {
		t.Fatalf("valid forward speed limit must produce realm limit warning")
	}
	// 仅 MaxConn：仍警告。
	fConn := seedForwardForNftables(t, h, tunnelID, nodeID, "1.1.1.1:443", 20013)
	fConn.MaxConn = 3
	if !warnFor(fConn) {
		t.Fatalf("MaxConn-only forward must produce realm limit warning")
	}
}
