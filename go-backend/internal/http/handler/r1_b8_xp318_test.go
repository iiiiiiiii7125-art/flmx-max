package handler

import (
	"math"
	"testing"

	"go-backend/internal/store/repo"
)

// X-P3-18 (L-05-1): gost 流量合并与缩放补溢出检查（对齐 nft 路径：
// 溢出整条丢弃并记日志）。修复前两条各 ≈9.2e18 的上报合并回绕为
// 负数、负增量落账。
func TestR1B8_XP318_GostOverflowDiscardsWholeForward(t *testing.T) {
	h := &Handler{}
	metas := map[int64]repo.FlowUploadForwardMeta{
		20: {ForwardID: 20, UserID: 2, UserTunnelID: 10, TunnelID: 1, TrafficRatio: 1, TunnelFlow: 3},
	}

	batch := h.buildFlowUploadBatch([]flowItem{
		{N: "20_2_10", U: math.MaxInt64 - 100, D: 10},
		{N: "20_2_10_tcp", U: 200, D: 10},
	}, metas)
	if len(batch.flowDeltas) != 0 {
		t.Fatalf("溢出转发应整条丢弃, got deltas=%+v", batch.flowDeltas)
	}
	if batch.quotaUsage[2] != 0 {
		t.Fatalf("溢出转发不应计 quota, got %d", batch.quotaUsage[2])
	}
	if _, ok := batch.forwardTraffic[20]; ok {
		t.Fatalf("溢出转发不应进入隧道指标桶: %+v", batch.forwardTraffic[20])
	}

	// 防过修：正常大流量仍入账。
	batch = h.buildFlowUploadBatch([]flowItem{
		{N: "20_2_10", U: 1 << 40, D: 1 << 39},
	}, metas)
	if len(batch.flowDeltas) != 1 {
		t.Fatalf("正常大流量应入账, got %+v", batch.flowDeltas)
	}
	if batch.flowDeltas[0].OutFlow != 1<<40 || batch.flowDeltas[0].InFlow != 1<<39 {
		t.Fatalf("正常大流量数值错误: %+v", batch.flowDeltas[0])
	}
}
