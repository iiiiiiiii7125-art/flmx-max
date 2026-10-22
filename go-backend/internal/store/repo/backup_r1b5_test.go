package repo

import (
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R1 X-P2-7 (H1-01, BC-08): importUserSubscriptions 此前只校验订阅
// 存在、不校验用户存在，悬空分配落库且回执假报 imported。本探针：
// 订阅在、用户不在 → skipped=1 / imported=0 / DB 零行；用户在的
// 对照行正常导入（防过修正）。

func TestR1B5ImportUserSubscriptionsSkipsMissingUser(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "r1b5-us-missing-user.db")
	if err := dest.DB().Exec(`
		INSERT INTO subscription(id, name, kind, token, uuid, password, source_all, inbound_ids, source_ids, options, expire_time, enabled, remark, inx, created_time, updated_time)
		VALUES(31, 'b5-sub', 'share', 'tok-b5', 'uuid-b5', 'pw', 0, '', '', '{}', 0, 1, '', 0, ?, ?)
	`, now, now).Error; err != nil {
		t.Fatalf("seed subscription: %v", err)
	}
	// 对照用户 41 真实存在。
	if err := dest.DB().Exec(`
		INSERT INTO user(id, user, pwd, role_id, exp_time, flow, in_flow, out_flow, flow_reset_time, num, created_time, updated_time, status)
		VALUES(41, 'b5user', 'pwd', 1, 0, 0, 0, 0, 1, 0, ?, ?, 1)
	`, now, now).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}

	backup := &model.BackupData{
		Version: "1.0",
		UserSubscriptions: []model.UserSubscriptionBackup{
			{ID: 51, UserID: 999, SubscriptionID: 31, CreatedTime: now}, // 用户不存在
			{ID: 52, UserID: 41, SubscriptionID: 31, CreatedTime: now},  // 正常对照
		},
	}
	result, err := dest.Import(backup, []string{"userSubscriptions"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.UserSubscriptionsImported != 1 || result.UserSubscriptionsSkipped != 1 {
		t.Fatalf("expected imported=1 skipped=1, got imported=%d skipped=%d",
			result.UserSubscriptionsImported, result.UserSubscriptionsSkipped)
	}
	if n := countRaw(t, dest, `SELECT COUNT(1) FROM user_subscription WHERE user_id = 999`); n != 0 {
		t.Fatalf("dangling assignment for missing user must not be written, got %d rows", n)
	}
	if n := countRaw(t, dest, `SELECT COUNT(1) FROM user_subscription WHERE user_id = 41 AND subscription_id = 31`); n != 1 {
		t.Fatalf("valid assignment must be imported (防过修正), got %d rows", n)
	}
}

// R1 H-O2（随 X-P2-7 同批顺手项，V-23）：importInbounds 中空 token
// 订阅被静默 continue，不计入 SkippedRows，回执看不见丢弃。
// 修复后必须计入 SkippedRows["subscriptions"]。

func TestR1B5ImportEmptyTokenSubscriptionCountedSkipped(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "r1b5-empty-token.db")
	backup := &model.BackupData{
		Version: "1.0",
		Inbounds: &model.InboundsBackup{
			Subscriptions: []model.SubscriptionBackup{
				{ID: 61, Name: "no-token", Kind: "share", Token: "", CreatedTime: now},
				{ID: 62, Name: "with-token", Kind: "share", Token: "tok-b5-ok", UUID: "u", Password: "p", CreatedTime: now},
			},
		},
	}
	result, err := dest.Import(backup, []string{"inbounds"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.SkippedRows["subscriptions"] != 1 {
		t.Fatalf("empty-token subscription skip must be receipted, SkippedRows = %v", result.SkippedRows)
	}
	if n := countRaw(t, dest, `SELECT COUNT(1) FROM subscription WHERE token = 'tok-b5-ok'`); n != 1 {
		t.Fatalf("valid subscription must be restored (防过修正), got %d rows", n)
	}
	// 空 token 订阅不得以任何形态落库（新库自带 1 条种子默认订阅，
	// 故按 token='' 计数而非总数）。
	if n := countRaw(t, dest, `SELECT COUNT(1) FROM subscription WHERE token = ''`); n != 0 {
		t.Fatalf("empty-token subscription must not be restored, got %d rows", n)
	}
}
