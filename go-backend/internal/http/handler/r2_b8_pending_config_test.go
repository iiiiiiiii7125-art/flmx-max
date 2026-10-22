package handler

import (
	"testing"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R2X-047（handler 面）：旧备份恢复出的 pending_realm_settle 键必须
// 被 hasPendingRealmSettle 惰性读 DB 命中——configs 段排除集只精确
// 命中日报运行时两键，该前缀族是 X-P3-34 的有意落库设计，显式豁免。
func TestR2B8RestoredPendingRealmSettleVisible(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r2b8-pending.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })

	backup := &model.BackupData{
		Version: "1.0",
		Configs: map[string]string{
			"pending_realm_settle:7": `{"nodeId":7,"markedAt":1,"direction":"realm->gost"}`,
			"notify_daily_last":      "2020-01-01",
		},
	}
	if _, err := r.Import(backup, []string{"configs"}); err != nil {
		t.Fatalf("import: %v", err)
	}

	h := New(r, "test-secret")
	if !h.hasPendingRealmSettle(7) {
		t.Fatalf("R2X-047: restored pending_realm_settle:7 must be visible to hasPendingRealmSettle")
	}
	if cfg, err := r.GetConfigByName("notify_daily_last"); err != nil || (cfg != nil && cfg.Value != "") {
		t.Fatalf("R2X-047: notify_daily_last from a backup must not be imported, cfg=%v err=%v", cfg, err)
	}
}
