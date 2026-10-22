package handler

import (
	"testing"

	"go-backend/internal/store/repo"
)

// X-P3-34 (X-FO5, V-09): pendingRealmSettle 标记落库——面板重启后
// 未完成标记须恢复，重连仍先结算后 redeploy（顺序逻辑不变，标记
// 不再随进程内存丢失）。修复前标记纯内存态，新 Handler 读不到。
func TestR1B8_XP334_PendingRealmSettleSurvivesRestart(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/xp334.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })

	h1 := New(r, "test-secret")
	h1.markPendingRealmSettle(424242)
	if !h1.hasPendingRealmSettle(424242) {
		t.Fatal("标记后应可读")
	}

	// 模拟面板重启：同库新建 Handler，标记应从 DB 恢复。
	h2 := New(r, "test-secret")
	if !h2.hasPendingRealmSettle(424242) {
		t.Fatalf("重启后 pendingRealmSettle 标记丢失（内存态未落库）")
	}
	if !h2.consumePendingRealmSettle(424242) {
		t.Fatalf("恢复的标记应可被消费")
	}

	// 消费后落库标记同步清除，再重启不应复活。
	h3 := New(r, "test-secret")
	if h3.hasPendingRealmSettle(424242) {
		t.Fatalf("已消费的标记不应在重启后复活")
	}
}
