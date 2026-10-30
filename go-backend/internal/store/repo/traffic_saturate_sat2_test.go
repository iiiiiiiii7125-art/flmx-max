package repo

import (
	"fmt"
	"math"
	"path/filepath"
	"testing"

	"go-backend/internal/store/model"
)

// SAT2（饱和族终局清扫）：SAT 登记的两处同形残留——AddNodeTraffic
// （node_traffic 小时桶）与 UpsertTunnelMetricBuckets（tunnel_metric
// 分钟桶四列）的 `存量 + excluded.*` 裸加，以及终局枚举新发现的
// node_seq.legacy_count 裸 +1。裸加与 B08-001 同机制：SQLite 溢出
// 回绕/宽化为 REAL 落库、PG int8 溢出报错回滚整批。本文件在 SQLite
// 真库上逐面断言饱和语义（含 typeof 保持 integer）。

func TestAddNodeTrafficSaturates_SAT2(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "sat2-node-traffic.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	const hour = int64(1700000400000)
	seed := model.NodeTraffic{NodeID: 1, Hour: hour, InBytes: math.MaxInt64 - 10, OutBytes: math.MaxInt64 - 10}
	if err := r.DB().Create(&seed).Error; err != nil {
		t.Fatalf("seed node_traffic: %v", err)
	}
	if err := r.AddNodeTraffic([]model.NodeTraffic{{NodeID: 1, Hour: hour, InBytes: 1000, OutBytes: 1000}}); err != nil {
		t.Fatalf("AddNodeTraffic: %v", err)
	}
	var got model.NodeTraffic
	if err := r.DB().Where("node_id = ? AND hour = ?", 1, hour).First(&got).Error; err != nil {
		t.Fatalf("reload node_traffic: %v", err)
	}
	if got.InBytes != math.MaxInt64 || got.OutBytes != math.MaxInt64 {
		t.Fatalf("node_traffic = (%d, %d), want saturation at MaxInt64", got.InBytes, got.OutBytes)
	}
	if typ := mustColumnType(t, r, fmt.Sprintf("SELECT typeof(in_bytes) FROM node_traffic WHERE id = %d", got.ID)); typ != "integer" {
		t.Fatalf("typeof(in_bytes) = %s, want integer (overflow widened the stored value)", typ)
	}
	// 正常量级不受影响：新桶首写原值、存量桶正常相加。
	if err := r.AddNodeTraffic([]model.NodeTraffic{{NodeID: 2, Hour: hour, InBytes: 5, OutBytes: 7}}); err != nil {
		t.Fatalf("AddNodeTraffic fresh bucket: %v", err)
	}
	if err := r.AddNodeTraffic([]model.NodeTraffic{{NodeID: 2, Hour: hour, InBytes: 3, OutBytes: 4}}); err != nil {
		t.Fatalf("AddNodeTraffic normal add: %v", err)
	}
	var normal model.NodeTraffic
	if err := r.DB().Where("node_id = ? AND hour = ?", 2, hour).First(&normal).Error; err != nil {
		t.Fatalf("reload normal bucket: %v", err)
	}
	if normal.InBytes != 8 || normal.OutBytes != 11 {
		t.Fatalf("normal bucket = (%d, %d), want (8, 11)", normal.InBytes, normal.OutBytes)
	}
}

func TestUpsertTunnelMetricBucketsSaturates_SAT2(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "sat2-tunnel-metric.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	const ts = int64(1700000460000)
	seed := model.TunnelMetric{
		TunnelID: 1, NodeID: 1, Timestamp: ts,
		BytesIn: math.MaxInt64 - 10, BytesOut: math.MaxInt64 - 10,
		Connections: math.MaxInt64 - 10, Errors: math.MaxInt64 - 10,
	}
	if err := r.DB().Create(&seed).Error; err != nil {
		t.Fatalf("seed tunnel_metric: %v", err)
	}
	delta := &model.TunnelMetric{
		TunnelID: 1, NodeID: 1, Timestamp: ts,
		BytesIn: 1000, BytesOut: 1000, Connections: 1000, Errors: 1000,
	}
	if err := r.UpsertTunnelMetricBuckets([]*model.TunnelMetric{delta}); err != nil {
		t.Fatalf("UpsertTunnelMetricBuckets: %v", err)
	}
	var got model.TunnelMetric
	if err := r.DB().Where("tunnel_id = ? AND node_id = ? AND timestamp = ?", 1, 1, ts).First(&got).Error; err != nil {
		t.Fatalf("reload tunnel_metric: %v", err)
	}
	if got.BytesIn != math.MaxInt64 || got.BytesOut != math.MaxInt64 || got.Connections != math.MaxInt64 || got.Errors != math.MaxInt64 {
		t.Fatalf("tunnel_metric = (%d, %d, %d, %d), want all saturated at MaxInt64",
			got.BytesIn, got.BytesOut, got.Connections, got.Errors)
	}
	if typ := mustColumnType(t, r, fmt.Sprintf("SELECT typeof(bytes_in) FROM tunnel_metric WHERE id = %d", got.ID)); typ != "integer" {
		t.Fatalf("typeof(bytes_in) = %s, want integer (overflow widened the stored value)", typ)
	}

	// 批内同键合并走饱和加：两条同桶增量（MaxInt64-10 与 100）合并后
	// 必须饱和为 MaxInt64 作为 excluded 值落库，而非先在 Go 层回绕为负。
	const ts2 = int64(1700000520000)
	batch := []*model.TunnelMetric{
		{TunnelID: 2, NodeID: 1, Timestamp: ts2, BytesIn: math.MaxInt64 - 10},
		{TunnelID: 2, NodeID: 1, Timestamp: ts2, BytesIn: 100},
	}
	if err := r.UpsertTunnelMetricBuckets(batch); err != nil {
		t.Fatalf("UpsertTunnelMetricBuckets batch merge: %v", err)
	}
	var merged model.TunnelMetric
	if err := r.DB().Where("tunnel_id = ? AND node_id = ? AND timestamp = ?", 2, 1, ts2).First(&merged).Error; err != nil {
		t.Fatalf("reload merged bucket: %v", err)
	}
	if merged.BytesIn != math.MaxInt64 {
		t.Fatalf("batch-merged bytes_in = %d, want saturation at MaxInt64 (Go merge wrapped first)", merged.BytesIn)
	}

	// 正常量级：新桶原值、同桶再加正常累加。
	const ts3 = int64(1700000580000)
	if err := r.UpsertTunnelMetricBuckets([]*model.TunnelMetric{
		{TunnelID: 3, NodeID: 1, Timestamp: ts3, BytesIn: 5, BytesOut: 7, Connections: 1, Errors: 2},
	}); err != nil {
		t.Fatalf("UpsertTunnelMetricBuckets fresh: %v", err)
	}
	if err := r.UpsertTunnelMetricBuckets([]*model.TunnelMetric{
		{TunnelID: 3, NodeID: 1, Timestamp: ts3, BytesIn: 3, BytesOut: 4, Connections: 1, Errors: 1},
	}); err != nil {
		t.Fatalf("UpsertTunnelMetricBuckets normal add: %v", err)
	}
	var normal model.TunnelMetric
	if err := r.DB().Where("tunnel_id = ? AND node_id = ? AND timestamp = ?", 3, 1, ts3).First(&normal).Error; err != nil {
		t.Fatalf("reload normal bucket: %v", err)
	}
	if normal.BytesIn != 8 || normal.BytesOut != 11 || normal.Connections != 2 || normal.Errors != 3 {
		t.Fatalf("normal bucket = (%d, %d, %d, %d), want (8, 11, 2, 3)",
			normal.BytesIn, normal.BytesOut, normal.Connections, normal.Errors)
	}
}

func TestNodeSeqLegacyCountSaturates_SAT2(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "sat2-node-seq.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	const now = int64(1700000000000)
	seed := NodeSeq{NodeID: 7, LastSeq: 0, LegacyCount: math.MaxInt64, UpdatedTime: now}
	if err := r.DB().Create(&seed).Error; err != nil {
		t.Fatalf("seed node_seq: %v", err)
	}
	// 事务外语句（CheckAndUpdateSeq）：+1 必须饱和而非回绕/宽化。
	accepted, isReplay, isLegacy, err := r.CheckAndUpdateSeq(7, nil, now)
	if err != nil {
		t.Fatalf("CheckAndUpdateSeq legacy at ceiling: %v", err)
	}
	if !accepted || isReplay || !isLegacy {
		t.Fatalf("CheckAndUpdateSeq = (accepted=%v replay=%v legacy=%v), want (true, false, true)", accepted, isReplay, isLegacy)
	}
	assertLegacyCount := func(stage string) {
		t.Helper()
		var got NodeSeq
		if err := r.DB().First(&got, 7).Error; err != nil {
			t.Fatalf("reload node_seq %s: %v", stage, err)
		}
		if got.LegacyCount != math.MaxInt64 {
			t.Fatalf("legacy_count %s = %d, want saturation at MaxInt64", stage, got.LegacyCount)
		}
		if typ := mustColumnType(t, r, "SELECT typeof(legacy_count) FROM node_seq WHERE node_id = 7"); typ != "integer" {
			t.Fatalf("typeof(legacy_count) %s = %s, want integer (overflow widened the stored value)", stage, typ)
		}
	}
	assertLegacyCount("after CheckAndUpdateSeq")
	// 事务内语句（advanceSeqTx）：同一形态、同一断言。
	tx := r.DB().Begin()
	if err := advanceSeqTx(tx, 7, nil, now); err != nil {
		tx.Rollback()
		t.Fatalf("advanceSeqTx legacy at ceiling: %v", err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatalf("commit advanceSeqTx: %v", err)
	}
	assertLegacyCount("after advanceSeqTx")
	// 正常量级 +1 不受影响（新行首插为 1、再 +1 为 2）。
	if _, _, _, err := r.CheckAndUpdateSeq(8, nil, now); err != nil {
		t.Fatalf("CheckAndUpdateSeq fresh node: %v", err)
	}
	if _, _, _, err := r.CheckAndUpdateSeq(8, nil, now); err != nil {
		t.Fatalf("CheckAndUpdateSeq fresh node second: %v", err)
	}
	var fresh NodeSeq
	if err := r.DB().First(&fresh, 8).Error; err != nil {
		t.Fatalf("reload fresh node_seq: %v", err)
	}
	if fresh.LegacyCount != 2 {
		t.Fatalf("fresh legacy_count = %d, want 2", fresh.LegacyCount)
	}
}
