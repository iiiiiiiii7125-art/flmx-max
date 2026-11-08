package repo

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// FIX2-B 回归批（V3/V4/V5 复审发现）：
//   - V4-N2 两格（importPeerShares / 联邦 binding 恢复）已随
//     DEV-RM-SHARE 联邦删除一并摘除。
//   - D4-11：importUsers 在备份无配额信号时删目标库现存配额行。

// D4-05 + V3-02：node_kernel.InstalledVersion 必须进备份并往返；
// 且同实例重复导入不得把已有值清空（旧 DoUpdates 含该列而 item
// 恒空，二次导入即主动清零）。
func TestFix2BKernelInstalledVersionRoundTripAndReimport(t *testing.T) {
	now := time.Now().UnixMilli()
	source := openBackupRepo(t, "fix2b-kernel-src.db")
	createRow(t, source, &model.Node{ID: 3, Name: "nft-node", Secret: "node-secret", ServerIP: "10.0.0.3", Port: "10000-10100",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}, ForwardMode: "nftables"})
	createRow(t, source, &model.NodeKernel{ID: 21, NodeID: 3, KernelType: "xray", Version: "v1.2.3",
		InstalledVersion: "v1.2.3-inst", Status: "applied", Enabled: 1, CreatedTime: now, UpdatedTime: now})

	dest := openBackupRepo(t, "fix2b-kernel-dst.db")
	roundTripBackup(t, source, dest)
	var k model.NodeKernel
	if err := dest.DB().Where("id = ?", 21).First(&k).Error; err != nil {
		t.Fatalf("kernel must be restored: %v", err)
	}
	if k.InstalledVersion != "v1.2.3-inst" {
		t.Fatalf("D4-05: InstalledVersion = %q, want %q", k.InstalledVersion, "v1.2.3-inst")
	}

	roundTripBackup(t, source, dest)
	if err := dest.DB().Where("id = ?", 21).First(&k).Error; err != nil {
		t.Fatalf("kernel must survive reimport: %v", err)
	}
	if k.InstalledVersion != "v1.2.3-inst" {
		t.Fatalf("V3-02: reimport cleared InstalledVersion to %q, want %q", k.InstalledVersion, "v1.2.3-inst")
	}
}

// D4-04：remote_source.Userinfo（机场套餐流量/到期）必须往返；
// file 源无刷新自愈，丢失即永久。
func TestFix2BRemoteSourceUserinfoRoundTrip(t *testing.T) {
	now := time.Now().UnixMilli()
	source := openBackupRepo(t, "fix2b-srcinfo-src.db")
	createRow(t, source, &model.RemoteSource{ID: 5, Name: "airport", URL: "https://example.com/sub",
		Kind: "file", Userinfo: "upload=1; download=2", Enabled: 1, CreatedTime: now, UpdatedTime: now})

	dest := openBackupRepo(t, "fix2b-srcinfo-dst.db")
	roundTripBackup(t, source, dest)

	var src model.RemoteSource
	if err := dest.DB().Where("id = ?", 5).First(&src).Error; err != nil {
		t.Fatalf("remote source must be restored: %v", err)
	}
	if src.Userinfo != "upload=1; download=2" {
		t.Fatalf("D4-04: Userinfo = %q, want %q", src.Userinfo, "upload=1; download=2")
	}
}

// D4-06：PendingDelete 待删订阅恢复后必须保持待删语义，不得复活
// 成可拉取的正常订阅。
func TestFix2BSubscriptionPendingDeleteRoundTrip(t *testing.T) {
	now := time.Now().UnixMilli()
	source := openBackupRepo(t, "fix2b-pendel-src.db")
	token := strings.Repeat("c", 32)
	createRow(t, source, &model.Subscription{ID: 11, Name: "pending-del", Kind: model.SubscriptionShare,
		Token: token, UUID: "u-1", Password: "p", Enabled: 0, PendingDelete: 1,
		CreatedTime: now, UpdatedTime: now})

	dest := openBackupRepo(t, "fix2b-pendel-dst.db")
	roundTripBackup(t, source, dest)

	var sub model.Subscription
	if err := dest.DB().Where("token = ?", token).First(&sub).Error; err != nil {
		t.Fatalf("subscription must be restored: %v", err)
	}
	if sub.PendingDelete != 1 {
		t.Fatalf("D4-06: PendingDelete = %d, want 1 (pending-delete subscription must not revive)", sub.PendingDelete)
	}
}

// D4-11：备份用户无任何配额信号（旧备份形态）时，导入不得删除目标
// 实例现存的配额行——同实例重导入会静默剥掉用户的日/月限额。
func TestFix2BImportUsersWithoutQuotaSignalPreservesQuotaRow(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "fix2b-quota-dst.db")
	createRow(t, dest, &model.User{ID: 7, User: "quota-keeper", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, dest, &model.UserQuota{
		UserID: 7, DailyLimitGB: 5, MonthlyLimitGB: 50,
		DayKey: 20261022, MonthKey: 202610, CreatedTime: now, UpdatedTime: now,
	})

	backup := &model.BackupData{Version: "1.0", Users: []model.UserBackup{{
		ID: 7, User: "quota-keeper", Pwd: "$2a$10$abcdefghijklmnopqrstuv", RoleID: 1, Status: 1, CreatedTime: now,
	}}}
	if _, err := dest.Import(backup, []string{"users"}); err != nil {
		t.Fatalf("import: %v", err)
	}
	var q model.UserQuota
	if err := dest.DB().Where("user_id = ?", 7).First(&q).Error; err != nil {
		t.Fatalf("D4-11: existing quota row must survive a quota-less backup import: %v", err)
	}
	if q.DailyLimitGB != 5 || q.MonthlyLimitGB != 50 {
		t.Fatalf("D4-11: quota limits = %d/%d, want 5/50", q.DailyLimitGB, q.MonthlyLimitGB)
	}
}
