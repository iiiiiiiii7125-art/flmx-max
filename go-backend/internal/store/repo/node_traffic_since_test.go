package repo

import (
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R1-B-03：NodeTrafficSince 在非整点时区少计月初首个小时桶——桶按
// UTC 整点对齐，查看者时区月初时刻（+5:30 等）落在桶中间时，该桶
// 只计 fromMs 之后的分钟份额（订阅侧 BC-12 同法：被计段是该行最后
// 一段，取「整值 − 排除份额向下取整」的余量）。整点时区 fromMs 与
// 桶界对齐，结果必须与旧行为逐值一致。
func TestNodeTrafficSinceProratesBoundaryBucket(t *testing.T) {
	r, err := Open(t.TempDir() + "/node-traffic.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	const hourMs = int64(time.Hour / time.Millisecond)
	bucket := time.Date(2026, 10, 31, 18, 0, 0, 0, time.UTC).UnixMilli()
	rows := []model.NodeTraffic{
		{NodeID: 1, Hour: bucket - hourMs, InBytes: 9999, OutBytes: 9999}, // 跨界桶之前：不计
		{NodeID: 1, Hour: bucket, InBytes: 1000, OutBytes: 500},           // 跨界桶
		{NodeID: 1, Hour: bucket + hourMs, InBytes: 300, OutBytes: 100},   // 整桶
		{NodeID: 2, Hour: bucket, InBytes: 60, OutBytes: 30},              // 只有跨界桶
		{NodeID: 3, Hour: bucket + hourMs, InBytes: 70, OutBytes: 40},     // 只有整桶
	}
	if err := r.AddNodeTraffic(rows); err != nil {
		t.Fatal(err)
	}

	// +5:30 月初：fromMs = 桶起点 +30 分钟，跨界桶计一半份额
	// （1000 − 1000·30/60 = 500，500 − 500·30/60 = 250）。
	from := bucket + 30*60*1000
	got, err := r.NodeTrafficSince(from)
	if err != nil {
		t.Fatal(err)
	}
	if want := [2]int64{500 + 300, 250 + 100}; got[1] != want {
		t.Fatalf("node1 = %v, want %v", got[1], want)
	}
	if want := [2]int64{30, 15}; got[2] != want {
		t.Fatalf("node2 = %v, want %v", got[2], want)
	}
	if want := [2]int64{70, 40}; got[3] != want {
		t.Fatalf("node3 = %v, want %v", got[3], want)
	}

	// 整点时区回归：fromMs 恰在桶界，跨界桶整桶全计，与旧的
	// 「hour >= fromMs 全量求和」逐值一致。
	got, err = r.NodeTrafficSince(bucket)
	if err != nil {
		t.Fatal(err)
	}
	if want := [2]int64{1000 + 300, 500 + 100}; got[1] != want {
		t.Fatalf("aligned node1 = %v, want %v", got[1], want)
	}
	if want := [2]int64{60, 30}; got[2] != want {
		t.Fatalf("aligned node2 = %v, want %v", got[2], want)
	}
	if want := [2]int64{70, 40}; got[3] != want {
		t.Fatalf("aligned node3 = %v, want %v", got[3], want)
	}
}
