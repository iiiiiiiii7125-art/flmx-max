package repo

import (
	"path/filepath"
	"testing"

	"go-backend/internal/store/model"
)

// FIX-B D6-03：ApplyInboundCounterSamples 原为单个巨型事务（一批
// 1200–2300 条 upsert + 数百 UPDATE 串行占库 0.25–0.9s）。改为按
// 200 条分片提交：每片内「基线推进 + 总量入账」同事务，片间键不重叠
// （同批重复样本已先合并），结果与单事务等价；且某片失败时已提交片
// 的基线与总量同步前进，重试同批样本不会重复计入。
func TestApplyInboundCounterSamplesChunkedPartialProgressAndRetry(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "d603.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	const n = 250
	ids := make([]int64, 0, n)
	for i := 0; i < n; i++ {
		in := model.NodeInbound{Name: "d603", Protocol: "vless", Port: 10000 + i}
		if err := r.DB().Create(&in).Error; err != nil {
			t.Fatalf("create inbound %d: %v", i, err)
		}
		ids = append(ids, in.ID)
	}
	samples := make([]InboundCounterSample, 0, n)
	for i, id := range ids {
		b := int64(1000 + i)
		if i == 230 {
			b = 666666 // 哨兵：触发器让这一条 upsert 失败（落在第二片）
		}
		samples = append(samples, InboundCounterSample{InboundID: id, Protocol: "tcp", Direction: "in", Epoch: "e1", Bytes: b})
	}
	if err := r.DB().Exec(`CREATE TRIGGER d603_fail BEFORE INSERT ON node_inbound_counter
		WHEN NEW.bytes = 666666 BEGIN SELECT RAISE(ABORT, 'd603 boom'); END`).Error; err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	if err := r.ApplyInboundCounterSamples(samples, 1000); err == nil {
		t.Fatalf("expected apply to fail on sentinel sample")
	}
	// 第一片（200 条）应已提交：基线与总量同步前进。
	var first model.NodeInbound
	if err := r.DB().First(&first, ids[0]).Error; err != nil {
		t.Fatal(err)
	}
	if first.UploadBytes != 1000 {
		t.Fatalf("D6-03: first chunk not committed independently, upload=%d want 1000", first.UploadBytes)
	}
	var sentinel model.NodeInbound
	if err := r.DB().First(&sentinel, ids[230]).Error; err != nil {
		t.Fatal(err)
	}
	if sentinel.UploadBytes != 0 {
		t.Fatalf("sentinel inbound must be untouched, upload=%d", sentinel.UploadBytes)
	}

	// 去掉故障后以同批样本重试：已提交片 delta=0（基线已推进），
	// 剩余片补齐，最终每条恰好计入一次。
	if err := r.DB().Exec(`DROP TRIGGER d603_fail`).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.ApplyInboundCounterSamples(samples, 2000); err != nil {
		t.Fatalf("retry apply: %v", err)
	}
	for i, id := range ids {
		var in model.NodeInbound
		if err := r.DB().First(&in, id).Error; err != nil {
			t.Fatal(err)
		}
		want := int64(1000 + i)
		if i == 230 {
			want = 666666
		}
		if in.UploadBytes != want {
			t.Fatalf("inbound %d upload=%d want %d (double count or loss after chunked retry)", i, in.UploadBytes, want)
		}
	}
	var stateCount int64
	if err := r.DB().Model(&model.NodeInboundCounter{}).Count(&stateCount).Error; err != nil {
		t.Fatal(err)
	}
	if stateCount != n {
		t.Fatalf("counter states=%d want %d", stateCount, n)
	}
}
