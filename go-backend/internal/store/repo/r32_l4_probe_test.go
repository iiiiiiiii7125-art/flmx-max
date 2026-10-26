package repo

import (
	"database/sql"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R32-L4-E02（产品 P3）：备份恢复 importTunnels 把隧道 in_ip 原值落库——
// 含 :port 的 in_ip（如 "203.0.113.7:6366"）应剥离后入库，ListTunnels
// 回显裸值；多值列表逐项剥离。先红后绿探针：修复前三格全红（见
// R32-L4-probe-restore-inip.log），修复后三格全绿。
// 凭据 hygiene：本探针只断言 in_ip 字段值与计数，绝不打印备份载荷。

func TestR32L4RestorePortedTunnelInIPStripped(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "r32l4-restore-inip.db")
	backup := &model.BackupData{
		Version: "1.0",
		Tunnels: []model.TunnelBackup{
			{ID: 1, Name: "r32l4-t1", Flow: 1, TrafficRatio: 1, Type: 1, Status: 1, CreatedTime: now, InIP: "203.0.113.7:6366"},
			{ID: 2, Name: "r32l4-t2", Flow: 1, TrafficRatio: 1, Type: 1, Status: 1, CreatedTime: now, InIP: "198.51.100.77:8443,2001:db8::1"},
		},
	}
	result, err := dest.Import(backup, []string{"tunnels"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.TunnelsImported != 2 {
		t.Fatalf("TunnelsImported = %d, want 2", result.TunnelsImported)
	}

	readStoredInIP := func(id int64) string {
		t.Helper()
		var inIP string
		if err := dest.DB().Raw("SELECT in_ip FROM tunnel WHERE id = ?", id).Scan(&inIP).Error; err != nil {
			t.Fatalf("read stored in_ip id=%d: %v", id, err)
		}
		return inIP
	}

	// CELL-T1：单值含端口入库须剥离。
	if got := readStoredInIP(1); got != "203.0.113.7" {
		t.Errorf("CELL-T1 tunnel.in_ip after restore = %q, want %q (ported value stored verbatim)", got, "203.0.113.7")
	}
	// CELL-T2：多值列表逐项剥离（裸 IPv6 项原样保留）。
	if got := readStoredInIP(2); got != "198.51.100.77,2001:db8::1" {
		t.Errorf("CELL-T2 tunnel.in_ip after restore = %q, want %q (multi-value list must strip per item)", got, "198.51.100.77,2001:db8::1")
	}
	// CELL-L1：ListTunnels 回显须为裸值。
	items, err := dest.ListTunnels()
	if err != nil {
		t.Fatalf("ListTunnels: %v", err)
	}
	found := false
	for _, item := range items {
		if id, _ := item["id"].(int64); id == 1 {
			found = true
			if inIP, _ := item["inIp"].(string); inIP != "203.0.113.7" {
				t.Errorf("CELL-L1 ListTunnels inIp = %q, want %q", inIP, "203.0.113.7")
			}
		}
	}
	if !found {
		t.Fatalf("CELL-L1 tunnel id=1 not found in ListTunnels output %+v", items)
	}
	// CELL-L2：存量残留（≤1.5.1 备份已落库的含端口 in_ip，绕过
	// Import 直接落库模拟）——ListTunnels 渲染侧剥离，展示裸值；
	// 存储值本身不改写（R31 F-01 存量不改写口径）。
	createRow(t, dest, &model.Tunnel{ID: 3, Name: "r32l4-legacy", Flow: 1, TrafficRatio: 1, Type: 1, Status: 1, CreatedTime: now, UpdatedTime: now, InIP: sql.NullString{String: "203.0.113.9:6366", Valid: true}})
	if got := readStoredInIP(3); got != "203.0.113.9:6366" {
		t.Fatalf("CELL-L2 setup: legacy residue must stay stored verbatim, got %q", got)
	}
	items, err = dest.ListTunnels()
	if err != nil {
		t.Fatalf("ListTunnels: %v", err)
	}
	found = false
	for _, item := range items {
		if id, _ := item["id"].(int64); id == 3 {
			found = true
			if inIP, _ := item["inIp"].(string); inIP != "203.0.113.9" {
				t.Errorf("CELL-L2 ListTunnels inIp (legacy residue) = %q, want %q", inIP, "203.0.113.9")
			}
		}
	}
	if !found {
		t.Fatalf("CELL-L2 tunnel id=3 not found in ListTunnels output %+v", items)
	}
}
