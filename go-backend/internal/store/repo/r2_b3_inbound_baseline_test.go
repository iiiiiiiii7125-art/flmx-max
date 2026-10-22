package repo

import (
	"path/filepath"
	"testing"

	"go-backend/internal/store/model"
)

// R2X-033 (R2-F-04): 入站计数基线语义与订阅管线统一——无基线 /
// epoch 变化两分支只推进基线、delta=0，不得把累计值全量计入；
// 同 epoch 正常差分与同 epoch 回退全量计保持既有语义。
// 修复前：无基线 1000 → 入账 1000；epoch 变 +500 → 再入账 500。
func TestR2B3_InboundBaselineSemantics(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "r2b3-inbound.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	inbound := model.NodeInbound{Name: "r2b3-inbound", Protocol: "vless", Port: 443}
	if err := r.DB().Create(&inbound).Error; err != nil {
		t.Fatalf("create inbound: %v", err)
	}
	upload := func() int64 {
		t.Helper()
		var got model.NodeInbound
		if err := r.DB().First(&got, inbound.ID).Error; err != nil {
			t.Fatal(err)
		}
		return got.UploadBytes
	}
	apply := func(epoch string, bytes int64, now int64) {
		t.Helper()
		if err := r.ApplyInboundCounterSamples([]InboundCounterSample{
			{InboundID: inbound.ID, Protocol: "vless", Direction: "in", Epoch: epoch, Bytes: bytes},
		}, now); err != nil {
			t.Fatalf("apply epoch=%s bytes=%d: %v", epoch, bytes, err)
		}
	}

	// 1) 无基线首采 1000：只建基线，入账 0。
	apply("e1", 1000, 1000)
	if got := upload(); got != 0 {
		t.Fatalf("no-baseline sample must only establish baseline: upload=%d, want 0", got)
	}
	// 2) epoch 变化（e1→e2）样本 500：只推进基线，总量不变。
	apply("e2", 500, 2000)
	if got := upload(); got != 0 {
		t.Fatalf("epoch-change sample must only advance baseline: upload=%d, want 0", got)
	}
	// 3) 同 epoch 正常差分：800-500=300 入账。
	apply("e2", 800, 3000)
	if got := upload(); got != 300 {
		t.Fatalf("same-epoch delta: upload=%d, want 300", got)
	}
	// 4) 同 epoch 回退（计数器同代重置）：当前值全量计，+200。
	apply("e2", 200, 4000)
	if got := upload(); got != 500 {
		t.Fatalf("same-epoch rollback full count: upload=%d, want 500", got)
	}
}
