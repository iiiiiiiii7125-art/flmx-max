package handler

import (
	"testing"

	"go-backend/internal/store/repo"
)

// R5-B3 D1-001(poison-scope)/D10: 缺 meta 的 forward 仅跳过其 delta，
// 其他 forward 正常累积。不再是整节点 return。
// 可达性证据：原 firstNftDeltaMissingMeta 触发时整节点 return（
// nftables_traffic.go:139-141），1 个坏 forward 毒化全节点计费。

func TestMissingMetaPerForwardSkip(t *testing.T) {
	deltas := []nftTrafficDelta{
		{ForwardID: 1, BytesIn: 100, BytesOut: 50},
		{ForwardID: 2, BytesIn: 200, BytesOut: 100}, // 缺 meta
		{ForwardID: 3, BytesIn: 300, BytesOut: 150},
	}
	newStates := []repo.NftCounterStateInput{
		{ForwardID: 1, Bytes: 100},
		{ForwardID: 2, Bytes: 200},
		{ForwardID: 3, Bytes: 300},
	}
	metas := map[int64]repo.FlowUploadForwardMeta{
		1: {ForwardID: 1},
		3: {ForwardID: 3},
		// ForwardID 2 缺失。
	}

	keptDeltas, keptStates := filterOutNftDeltasMissingMeta(deltas, newStates, metas, 99)

	// 2 个正常的仍在。
	if len(keptDeltas) != 2 {
		t.Fatalf("expected 2 deltas kept, got %d", len(keptDeltas))
	}
	for _, d := range keptDeltas {
		if d.ForwardID == 2 {
			t.Fatalf("missing-meta forward 2 should be skipped")
		}
	}
	// 缺 meta 的基线不推进（保留旧基线）。
	if len(keptStates) != 2 {
		t.Fatalf("expected 2 states kept, got %d", len(keptStates))
	}
	for _, s := range keptStates {
		if s.ForwardID == 2 {
			t.Fatalf("missing-meta forward 2 baseline should be kept (not advanced)")
		}
	}
}

func TestMissingMetaNoneSkippedWhenAllPresent(t *testing.T) {
	deltas := []nftTrafficDelta{
		{ForwardID: 1, BytesIn: 100},
		{ForwardID: 2, BytesIn: 200},
	}
	newStates := []repo.NftCounterStateInput{
		{ForwardID: 1, Bytes: 100},
		{ForwardID: 2, Bytes: 200},
	}
	metas := map[int64]repo.FlowUploadForwardMeta{
		1: {ForwardID: 1},
		2: {ForwardID: 2},
	}

	keptDeltas, keptStates := filterOutNftDeltasMissingMeta(deltas, newStates, metas, 99)
	if len(keptDeltas) != 2 || len(keptStates) != 2 {
		t.Fatalf("no skip expected, got %d deltas %d states", len(keptDeltas), len(keptStates))
	}
}
