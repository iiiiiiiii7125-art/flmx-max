package repo

import (
	"path/filepath"
	"strings"
	"testing"

	"go-backend/internal/store/model"
)

// R18-B9-01：备份导入 users/userTunnels 段把负值限额字段原样落库，
// 写面拒收的值经导入成"无限额"（Flow/FlowMiB/Num/MaxConn、
// 日月配额、FlowResetTime）。以下测试先于修复落盘（先红后绿）。
func TestR18B901ImportNegativeLimitsClamped(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "r18b901.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	backup := &model.BackupData{Version: "1.0",
		Users: []model.UserBackup{
			// 负限额 + 合法重置日：行保留，限额钳 0 + warning 点名原值。
			{ID: 501, User: "neg-limits", RoleID: 1, Status: 1,
				Flow: -1, FlowMiB: -5, Num: -1, MaxConn: -5, FlowResetTime: 1,
				DailyQuotaGB: -10, MonthlyQuotaGB: -20,
				DailyUsedBytes: 100, MonthlyUsedBytes: 200,
				QuotaDayKey: 20261120, QuotaMonthKey: 202611},
			// FlowResetTime 越界：整行跳过 + warning（与
			// importUserTunnels 的 R7-G-02 门同口径）。
			{ID: 502, User: "bad-reset-day", RoleID: 1, Status: 1,
				Flow: 10, Num: 1, MaxConn: 1, FlowResetTime: 99},
			{ID: 503, User: "neg-reset-day", RoleID: 1, Status: 1,
				Flow: 10, Num: 1, MaxConn: 1, FlowResetTime: -1},
			// 正控：合法限额原样恢复。
			{ID: 504, User: "honest", RoleID: 1, Status: 1,
				Flow: 10, FlowMiB: 20, Num: 5, MaxConn: 3, FlowResetTime: 15,
				DailyQuotaGB: 2.5, MonthlyQuotaGB: 7.5,
				DailyUsedBytes: 100, MonthlyUsedBytes: 200,
				QuotaDayKey: 20261120, QuotaMonthKey: 202611},
		},
	}
	res, err := r.Import(backup, []string{"users"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.UsersImported != 2 {
		t.Fatalf("R18-B9-01：UsersImported=%d，期望 2（负限额行保留、正控行保留，越界重置日两行跳过）", res.UsersImported)
	}

	var neg model.User
	if err := r.DB().First(&neg, 501).Error; err != nil {
		t.Fatalf("load neg-limits user: %v", err)
	}
	if neg.Flow != 0 || neg.FlowMiB != 0 || neg.Num != 0 || neg.MaxConn != 0 {
		t.Fatalf("R18-B9-01：负限额原样落库 flow=%d flowMiB=%d num=%d maxConn=%d，期望全为 0", neg.Flow, neg.FlowMiB, neg.Num, neg.MaxConn)
	}
	if neg.FlowResetTime != 1 {
		t.Fatalf("R18-B9-01：合法 FlowResetTime 被改写为 %d，期望 1", neg.FlowResetTime)
	}
	var quota model.UserQuota
	if err := r.DB().Where("user_id = ?", 501).First(&quota).Error; err != nil {
		t.Fatalf("load neg-limits quota: %v", err)
	}
	if quota.DailyLimitGB != 0 || quota.MonthlyLimitGB != 0 {
		t.Fatalf("R18-B9-01：负配额原样落库 daily=%v monthly=%v，期望 0/0", quota.DailyLimitGB, quota.MonthlyLimitGB)
	}
	if quota.DailyUsedBytes != 100 || quota.MonthlyUsedBytes != 200 {
		t.Fatalf("R18-B9-01：配额已用量被改写 daily=%d monthly=%d，期望 100/200", quota.DailyUsedBytes, quota.MonthlyUsedBytes)
	}
	for _, id := range []int64{502, 503} {
		var skipped model.User
		if err := r.DB().First(&skipped, id).Error; err == nil {
			t.Fatalf("R18-B9-01：FlowResetTime 越界用户 #%d 应整行跳过，却已落库 %+v", id, skipped)
		}
	}
	var honest model.User
	if err := r.DB().First(&honest, 504).Error; err != nil {
		t.Fatalf("load honest user: %v", err)
	}
	if honest.Flow != 10 || honest.FlowMiB != 20 || honest.Num != 5 || honest.MaxConn != 3 || honest.FlowResetTime != 15 {
		t.Fatalf("R18-B9-01：合法限额被改写 flow=%d flowMiB=%d num=%d maxConn=%d reset=%d，期望 10/20/5/3/15",
			honest.Flow, honest.FlowMiB, honest.Num, honest.MaxConn, honest.FlowResetTime)
	}
	var honestQuota model.UserQuota
	if err := r.DB().Where("user_id = ?", 504).First(&honestQuota).Error; err != nil {
		t.Fatalf("load honest quota: %v", err)
	}
	if honestQuota.DailyLimitGB != 2.5 || honestQuota.MonthlyLimitGB != 7.5 {
		t.Fatalf("R18-B9-01：合法配额被改写 daily=%v monthly=%v，期望 2.5/7.5", honestQuota.DailyLimitGB, honestQuota.MonthlyLimitGB)
	}

	joined := strings.Join(res.ImportWarnings, "\n")
	for _, want := range []string{"flow=-1", "flowMiB=-5", "num=-1", "maxConn=-5", "dailyQuotaGB=-10", "monthlyQuotaGB=-20", "flowResetTime=99", "flowResetTime=-1"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("R18-B9-01：warning 未点名 %q：%v", want, res.ImportWarnings)
		}
	}
}

func TestR18B901ImportUserTunnelNegativeLimitsClamped(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "r18b901-ut.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	for _, tid := range []int64{601, 602, 603} {
		if err := r.DB().Create(&model.Tunnel{ID: tid, Name: "t601", TrafficRatio: 1, Type: 1, Protocol: "tls", Flow: 1, CreatedTime: 1000, UpdatedTime: 1000, Status: 1}).Error; err != nil {
			t.Fatalf("seed tunnel %d: %v", tid, err)
		}
	}
	backup := &model.BackupData{Version: "1.0",
		Users: []model.UserBackup{
			{ID: 501, User: "owner", RoleID: 1, Status: 1},
		},
		UserTunnels: []model.UserTunnelBackup{
			// 负限额 + 合法重置日：行保留，限额钳 0 + warning。
			{ID: 901, UserID: 501, TunnelID: 601, Status: 1, Num: -2, Flow: -3, FlowMiB: -4, FlowResetTime: 1},
			// FlowResetTime 越界：整行跳过（R7-G-02 既有门，复核）。
			{ID: 902, UserID: 501, TunnelID: 602, Status: 1, Num: 1, Flow: 10, FlowResetTime: 99},
			// 正控。
			{ID: 903, UserID: 501, TunnelID: 603, Status: 1, Num: 2, Flow: 10, FlowMiB: 20, FlowResetTime: 15},
		},
	}
	res, err := r.Import(backup, []string{"users", "userTunnels"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	var neg model.UserTunnel
	if err := r.DB().First(&neg, 901).Error; err != nil {
		t.Fatalf("load neg user tunnel: %v", err)
	}
	if neg.Num != 0 || neg.Flow != 0 || neg.FlowMiB != 0 {
		t.Fatalf("R18-B9-01：用户隧道负限额原样落库 num=%d flow=%d flowMiB=%d，期望全为 0", neg.Num, neg.Flow, neg.FlowMiB)
	}
	var skipped model.UserTunnel
	if err := r.DB().First(&skipped, 902).Error; err == nil {
		t.Fatalf("R18-B9-01：FlowResetTime 越界授权 #902 应整行跳过，却已落库 %+v", skipped)
	}
	var honest model.UserTunnel
	if err := r.DB().First(&honest, 903).Error; err != nil {
		t.Fatalf("load honest user tunnel: %v", err)
	}
	if honest.Num != 2 || honest.Flow != 10 || honest.FlowMiB != 20 || honest.FlowResetTime != 15 {
		t.Fatalf("R18-B9-01：合法授权被改写 num=%d flow=%d flowMiB=%d reset=%d，期望 2/10/20/15",
			honest.Num, honest.Flow, honest.FlowMiB, honest.FlowResetTime)
	}
	joined := strings.Join(res.ImportWarnings, "\n")
	for _, want := range []string{"num=-2", "flow=-3", "flowMiB=-4"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("R18-B9-01：warning 未点名 %q：%v", want, res.ImportWarnings)
		}
	}
}
