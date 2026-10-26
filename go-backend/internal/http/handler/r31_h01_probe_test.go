package handler

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R31-L3-H01 (产品 P3): 恢复后首小时 statistics_flow 膨胀。
// statistics_flow 不在任何备份段，全量恢复后表为空；runStatisticsFlowJob
// 取不到基线时把用户终身累计 in+out 记为单小时增量，日报名下 24h 流量与
// 24h 曲线一次性虚增终身总量，下个小时才自愈。
// 探针按审计口径重建：源库 in+out = 3,000,000,000 + 2,000,000,000，
// ExportAll → JSON → 全新库 Import → 首小时增量期望 0。
func TestR31H01StatisticsFlowInflationAfterRestore(t *testing.T) {
	srcPath := filepath.Join(t.TempDir(), "r31-h01-src.db")
	src, err := repo.Open(srcPath)
	if err != nil {
		t.Fatalf("open source sqlite: %v", err)
	}
	t.Cleanup(func() { _ = src.Close() })

	now := time.Date(2026, 2, 7, 12, 0, 0, 0, time.UTC)

	// 源库用户终身累计 in+out = 3e9 + 2e9，并立一条 statistics_flow 基线行。
	if err := src.DB().Exec(`UPDATE user SET in_flow = 3000000000, out_flow = 2000000000 WHERE id = 1`).Error; err != nil {
		t.Fatalf("seed source user flow: %v", err)
	}
	if err := src.DB().Exec(`INSERT INTO statistics_flow(user_id, flow, total_flow, time, created_time) VALUES(1, 5000000000, 5000000000, '11:00', ?)`, now.Add(-time.Hour).UnixMilli()).Error; err != nil {
		t.Fatalf("seed source statistics baseline: %v", err)
	}

	// 全量备份经 JSON（落盘真实形态）恢复进全新库。
	backup, err := src.ExportAll()
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	raw, err := json.Marshal(backup)
	if err != nil {
		t.Fatalf("marshal backup: %v", err)
	}
	var decoded model.BackupData
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal backup: %v", err)
	}
	dstPath := filepath.Join(t.TempDir(), "r31-h01-dst.db")
	dst, err := repo.Open(dstPath)
	if err != nil {
		t.Fatalf("open dest sqlite: %v", err)
	}
	t.Cleanup(func() { _ = dst.Close() })
	if _, err := dst.Import(&decoded, r31H01AllBackupTypes); err != nil {
		t.Fatalf("import: %v", err)
	}

	// statistics_flow 不入备份：恢复库该表须为空。
	if n := mustQueryInt(t, dst, `SELECT COUNT(1) FROM statistics_flow`); n != 0 {
		t.Fatalf("expected restored statistics_flow to be empty, got %d rows", n)
	}
	// 恢复出的用户累计流量须完整（计费/配额读 user 表直值，不受本修复影响）。
	inFlow, outFlow, _ := mustQueryInt64Int64Int(t, dst, `SELECT in_flow, out_flow, status FROM user WHERE id = 1`)
	if inFlow != 3000000000 || outFlow != 2000000000 {
		t.Fatalf("expected restored user flow in=3000000000 out=2000000000, got in=%d out=%d", inFlow, outFlow)
	}

	h := New(dst, "secret")
	h.runStatisticsFlowJob(now)

	flow, total, hour := mustQueryInt64Int64String(t, dst, `SELECT flow, total_flow, time FROM statistics_flow WHERE user_id = 1 ORDER BY id DESC LIMIT 1`)
	if flow != 0 {
		t.Fatalf("H-01: first post-restore hour increment = %d, want 0 (baseline-only); lifetime total is billed as one hour", flow)
	}
	if total != 5000000000 {
		t.Fatalf("expected baseline row total_flow 5000000000, got %d", total)
	}
	if hour != "12:00" {
		t.Fatalf("expected hour mark 12:00, got %s", hour)
	}

	// 下个小时起正常计量：累计再涨 150,000,000，增量即为该差值。
	if err := dst.DB().Exec(`UPDATE user SET in_flow = 3100000000, out_flow = 2050000000 WHERE id = 1`).Error; err != nil {
		t.Fatalf("advance dest user flow: %v", err)
	}
	h.runStatisticsFlowJob(now.Add(time.Hour))
	flow2, total2, _ := mustQueryInt64Int64String(t, dst, `SELECT flow, total_flow, time FROM statistics_flow WHERE user_id = 1 ORDER BY id DESC LIMIT 1`)
	if flow2 != 150000000 {
		t.Fatalf("expected second-hour increment 150000000, got %d", flow2)
	}
	if total2 != 5150000000 {
		t.Fatalf("expected second-hour total_flow 5150000000, got %d", total2)
	}
}

var r31H01AllBackupTypes = []string{
	"users", "nodes", "tunnels", "forwards", "userTunnels", "speedLimits",
	"tunnelGroups", "userGroups", "permissions", "configs", "inbounds",
	"userInbounds", "announcements", "monitorPermissions",
	"userSubscriptions", "serviceMonitors",
}
