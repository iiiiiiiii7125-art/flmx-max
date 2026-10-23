package repo

import (
	"path/filepath"
	"sync"
	"testing"

	"go-backend/internal/store/model"
)

// R4-FIX-S3（F-S01）SQLite 侧并发探针：与 PG 契约用例
// （tests/contract/postgres_counter_concurrent_contract_test.go）
// 同形——基线后多 goroutine 并发提交同一样本，总增量只许入账
// 一次。SQLite 的 MaxOpenConns(1) 本就串行化事务体，本组在修复
// 前后都应绿：作用是机理探针 + 防回归（若 SQLite 连接配置或管线
// 结构日后变化导致串行化失效，本组先红）。真 PG 上的红→绿由
// PG 契约用例承担（门上实证双计，见 R4-RELEASE-GATE 项 11）。

func fireConcurrentApplies(t *testing.T, n int, apply func() error) {
	t.Helper()
	start := make(chan struct{})
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = apply()
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent apply %d: %v", i, err)
		}
	}
}

func TestApplySubscriptionCounterSamplesConcurrentSameSampleCountsOnce(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "s3-sub-concurrent.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	sub := model.Subscription{Name: "s3-sub", Token: "s3-token", Kind: "managed", Enabled: 1}
	if err := r.DB().Create(&sub).Error; err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	if err := r.ApplySubscriptionCounterSamples(7, "e1", []SubscriptionCounterSample{
		{SubscriptionID: sub.ID, Direction: "up", Bytes: 1000},
	}, 1000); err != nil {
		t.Fatalf("apply baseline: %v", err)
	}
	fireConcurrentApplies(t, 8, func() error {
		return r.ApplySubscriptionCounterSamples(7, "e1", []SubscriptionCounterSample{
			{SubscriptionID: sub.ID, Direction: "up", Bytes: 1500},
		}, 2000)
	})
	var got model.Subscription
	if err := r.DB().First(&got, sub.ID).Error; err != nil {
		t.Fatalf("reload subscription: %v", err)
	}
	if got.UploadBytes != 500 {
		t.Fatalf("concurrent same sample counted %d times worth: upload_bytes=%d, want 500", got.UploadBytes/500, got.UploadBytes)
	}
}

func TestApplyInboundCounterSamplesConcurrentSameSampleCountsOnce(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "s3-inbound-concurrent.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	inbound := model.NodeInbound{Name: "s3-inbound", Protocol: "vless", Port: 443}
	if err := r.DB().Create(&inbound).Error; err != nil {
		t.Fatalf("create inbound: %v", err)
	}
	if err := r.ApplyInboundCounterSamples([]InboundCounterSample{
		{InboundID: inbound.ID, Protocol: "vless", Direction: "in", Epoch: "e1", Bytes: 1000},
	}, 1000); err != nil {
		t.Fatalf("apply baseline: %v", err)
	}
	fireConcurrentApplies(t, 8, func() error {
		return r.ApplyInboundCounterSamples([]InboundCounterSample{
			{InboundID: inbound.ID, Protocol: "vless", Direction: "in", Epoch: "e1", Bytes: 1500},
		}, 2000)
	})
	var got model.NodeInbound
	if err := r.DB().First(&got, inbound.ID).Error; err != nil {
		t.Fatalf("reload inbound: %v", err)
	}
	if got.UploadBytes != 500 {
		t.Fatalf("concurrent same sample counted %d times worth: upload_bytes=%d, want 500", got.UploadBytes/500, got.UploadBytes)
	}
}
