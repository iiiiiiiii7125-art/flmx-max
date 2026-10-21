package repo

import (
	"path/filepath"
	"testing"

	"go-backend/internal/store/model"
)

// #88（3.3.29）：计数样本落账的同一事务按小时记桶；桶取 F-075 合并
// 后的增量（批内重复样本不得在桶里重复计），桶合计与订阅累计值同源。
func TestApplySubscriptionCounterSamplesWritesHourlyTraffic(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "sub-traffic.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	sub := model.Subscription{Name: "t-sub", Token: "t-token", Kind: "share", Enabled: 1}
	if err := r.DB().Create(&sub).Error; err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	const hourMs = int64(3600000)
	now1 := int64(1700000000000)
	hour1 := now1 - now1%hourMs

	// 第一批：基线 up 200 / down 500。
	if err := r.ApplySubscriptionCounterSamples(7, "e1", []SubscriptionCounterSample{
		{SubscriptionID: sub.ID, Direction: "up", Bytes: 200},
		{SubscriptionID: sub.ID, Direction: "down", Bytes: 500},
	}, now1); err != nil {
		t.Fatalf("apply batch 1: %v", err)
	}
	// 第二批（同一小时）：up 批内重复 210/260 —— F-075 合并后增量只有
	// 260−200=60；down 900 → 增量 400。桶不得出现 210 的重复计入。
	if err := r.ApplySubscriptionCounterSamples(7, "e1", []SubscriptionCounterSample{
		{SubscriptionID: sub.ID, Direction: "up", Bytes: 210},
		{SubscriptionID: sub.ID, Direction: "up", Bytes: 260},
		{SubscriptionID: sub.ID, Direction: "down", Bytes: 900},
	}, now1+60000); err != nil {
		t.Fatalf("apply batch 2: %v", err)
	}

	rows, err := r.ListSubscriptionTraffic(sub.ID, 0)
	if err != nil {
		t.Fatalf("list traffic: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 hourly bucket so far, got %+v", rows)
	}
	if rows[0].Hour != hour1 || rows[0].UploadBytes != 260 || rows[0].DownloadBytes != 900 {
		t.Fatalf("bucket = %+v, want hour=%d up=260 down=900", rows[0], hour1)
	}
	// 计数等价：桶合计与订阅累计值逐值一致。
	var got model.Subscription
	if err := r.DB().First(&got, sub.ID).Error; err != nil {
		t.Fatalf("reload subscription: %v", err)
	}
	if got.UploadBytes != rows[0].UploadBytes || got.DownloadBytes != rows[0].DownloadBytes {
		t.Fatalf("cumulative (%d/%d) != bucket sums (%d/%d)",
			got.UploadBytes, got.DownloadBytes, rows[0].UploadBytes, rows[0].DownloadBytes)
	}

	// 第三批（+2 小时）：新桶单独成行，List 按小时升序。
	now3 := now1 + 2*hourMs
	if err := r.ApplySubscriptionCounterSamples(7, "e1", []SubscriptionCounterSample{
		{SubscriptionID: sub.ID, Direction: "up", Bytes: 300},
	}, now3); err != nil {
		t.Fatalf("apply batch 3: %v", err)
	}
	rows, err = r.ListSubscriptionTraffic(sub.ID, 0)
	if err != nil {
		t.Fatalf("list traffic: %v", err)
	}
	if len(rows) != 2 || rows[1].Hour != now3-now3%hourMs || rows[1].UploadBytes != 40 || rows[1].DownloadBytes != 0 {
		t.Fatalf("buckets after batch 3: %+v", rows)
	}
	// fromMs 过滤：只看第二个桶起。
	rows, err = r.ListSubscriptionTraffic(sub.ID, rows[1].Hour)
	if err != nil || len(rows) != 1 || rows[0].UploadBytes != 40 {
		t.Fatalf("list from second hour: rows=%+v err=%v", rows, err)
	}
}

// #88：删订阅必须同事务清桶行——repo 直删与生产硬删路径
// （DeleteSubscriptionWithAssignments）两条都要清。
func TestDeleteSubscriptionClearsTrafficBuckets(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "sub-traffic-del.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	mk := func(token string) model.Subscription {
		sub := model.Subscription{Name: "t", Token: token, Kind: "share", Enabled: 1}
		if err := r.DB().Create(&sub).Error; err != nil {
			t.Fatalf("create subscription: %v", err)
		}
		if err := r.ApplySubscriptionCounterSamples(7, "e1", []SubscriptionCounterSample{
			{SubscriptionID: sub.ID, Direction: "up", Bytes: 10},
		}, 1700000000000); err != nil {
			t.Fatalf("seed traffic: %v", err)
		}
		return sub
	}
	a := mk("tok-a")
	b := mk("tok-b")

	if err := r.DeleteSubscription(a.ID); err != nil {
		t.Fatalf("delete a: %v", err)
	}
	if rows, _ := r.ListSubscriptionTraffic(a.ID, 0); len(rows) != 0 {
		t.Fatalf("DeleteSubscription left %d traffic rows", len(rows))
	}
	if err := r.DeleteSubscriptionWithAssignments(b.ID); err != nil {
		t.Fatalf("delete b: %v", err)
	}
	if rows, _ := r.ListSubscriptionTraffic(b.ID, 0); len(rows) != 0 {
		t.Fatalf("DeleteSubscriptionWithAssignments left %d traffic rows", len(rows))
	}
}

// #88：PurgeSubscriptionTraffic 只删保留期之前的桶。
func TestPurgeSubscriptionTraffic(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "sub-traffic-purge.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	sub := model.Subscription{Name: "t", Token: "tok-p", Kind: "share", Enabled: 1}
	if err := r.DB().Create(&sub).Error; err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	const hourMs = int64(3600000)
	base := int64(1700000000000)
	base -= base % hourMs
	for i, at := range []int64{base, base + 10*hourMs} {
		if err := r.DB().Create(&model.SubscriptionTraffic{
			SubscriptionID: sub.ID, Hour: at, UploadBytes: int64(i + 1),
		}).Error; err != nil {
			t.Fatalf("seed bucket: %v", err)
		}
	}
	if err := r.PurgeSubscriptionTraffic(base + 5*hourMs); err != nil {
		t.Fatalf("purge: %v", err)
	}
	rows, err := r.ListSubscriptionTraffic(sub.ID, 0)
	if err != nil || len(rows) != 1 || rows[0].Hour != base+10*hourMs {
		t.Fatalf("after purge: rows=%+v err=%v", rows, err)
	}
}
