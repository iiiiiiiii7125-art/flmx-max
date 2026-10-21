package repo

import (
	"path/filepath"
	"testing"

	"go-backend/internal/store/model"
)

// FIX132-C（A3-04/X2）：订阅计数基线缺失时只建基线、不计增量。
// X2 探针 D 坐实旧语义：恢复态订阅（累计已含 1000/2000）+ 基线缺失
// + 同 epoch 首样本 1000/2000 → 累计变 2000/4000（整段重复计入，
// 可把正常订阅顶过流量上限被停用）。nft 转发路径对基线缺失一直是
// 「只建基线」口径（buildNftCounterDeltas），订阅侧与之对齐。
func TestFix132SubscriptionCounterMissingBaselineOnlyEstablishesBaseline(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "sub-counter-baseline.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	sub := model.Subscription{Name: "restored", Token: "tok-restored", Kind: "share", Enabled: 1,
		UploadBytes: 1000, DownloadBytes: 2000}
	if err := r.DB().Create(&sub).Error; err != nil {
		t.Fatalf("create subscription: %v", err)
	}

	// 基线缺失 + 同 epoch 首样本：只建基线，累计不得跳增。
	if err := r.ApplySubscriptionCounterSamples(7, "e1", []SubscriptionCounterSample{
		{SubscriptionID: sub.ID, Direction: "up", Bytes: 1000},
		{SubscriptionID: sub.ID, Direction: "down", Bytes: 2000},
	}, 1700000000000); err != nil {
		t.Fatalf("apply first sample: %v", err)
	}
	var got model.Subscription
	if err := r.DB().First(&got, sub.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.UploadBytes != 1000 || got.DownloadBytes != 2000 {
		t.Fatalf("A3-04: missing baseline must not double-count, cumulative = %d/%d, want 1000/2000",
			got.UploadBytes, got.DownloadBytes)
	}
	// 基线行必须已落库（后续样本按差分计）。
	var counter model.SubscriptionCounter
	if err := r.DB().Where("node_id = ? AND subscription_id = ? AND direction = ?", 7, sub.ID, "up").First(&counter).Error; err != nil {
		t.Fatalf("baseline row must exist: %v", err)
	}
	if counter.Epoch != "e1" || counter.Bytes != 1000 {
		t.Fatalf("baseline = %+v, want epoch e1 bytes 1000", counter)
	}

	// 基线建立后增量照常计：up 1000→1500 只计 +500。
	if err := r.ApplySubscriptionCounterSamples(7, "e1", []SubscriptionCounterSample{
		{SubscriptionID: sub.ID, Direction: "up", Bytes: 1500},
	}, 1700000060000); err != nil {
		t.Fatalf("apply second sample: %v", err)
	}
	if err := r.DB().First(&got, sub.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.UploadBytes != 1500 {
		t.Fatalf("incremental sample must count delta only, upload = %d, want 1500", got.UploadBytes)
	}

	// epoch 变化（agent 重建计数表）语义不变：新 epoch 累计从零起，
	// 首样本全量即新 epoch 的真实新增流量，照旧全量计。
	if err := r.ApplySubscriptionCounterSamples(7, "e2", []SubscriptionCounterSample{
		{SubscriptionID: sub.ID, Direction: "up", Bytes: 300},
	}, 1700000120000); err != nil {
		t.Fatalf("apply new epoch sample: %v", err)
	}
	if err := r.DB().First(&got, sub.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.UploadBytes != 1800 {
		t.Fatalf("epoch rollover must keep full-count semantics, upload = %d, want 1800", got.UploadBytes)
	}
}
