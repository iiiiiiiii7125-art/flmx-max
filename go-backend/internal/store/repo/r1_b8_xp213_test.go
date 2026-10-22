package repo

import (
	"path/filepath"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// X-P2-13 (L-08-1): 联邦 peer_share_runtime 预留口必须进入本地端口占用
// 谓词与已用端口集合（双向对齐），且联邦分配须与本地共用 per-node
// 端口锁。修复前：本地谓词对 peer 预留口返回未占用，自动选口可挑中
// 预留口，联邦与本地同节点同口双分配。
func TestR1B8_XP213_PeerShareRuntimePortBlocksLocalAlloc(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "xp213.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	now := time.Now().UnixMilli()
	rt := &model.PeerShareRuntime{
		ShareID: 1, NodeID: 7, ReservationID: "res-xp213", ResourceKey: "rk-xp213",
		Port: 31001, Status: 1, CreatedTime: now, UpdatedTime: now,
	}
	if err := r.DB().Create(rt).Error; err != nil {
		t.Fatalf("seed runtime: %v", err)
	}

	occupied, err := r.HasOtherForwardOnNodePort(7, 31001, 0)
	if err != nil {
		t.Fatalf("predicate: %v", err)
	}
	if !occupied {
		t.Fatalf("peer_share_runtime 预留口 31001 未被本地谓词视为占用（双分配窗口）")
	}

	conflict, err := r.FindOtherForwardOnNodePort(7, 31001, 0)
	if err != nil {
		t.Fatalf("find conflict: %v", err)
	}
	if conflict == nil {
		t.Fatalf("FindOtherForwardOnNodePort 未报告 peer 预留口冲突")
	}

	used, err := r.GetUsedPortsOnNodeAsMap(7)
	if err != nil {
		t.Fatalf("used map: %v", err)
	}
	if !used[31001] {
		t.Fatalf("GetUsedPortsOnNodeAsMap 未含 peer 预留口，自动选口可挑中")
	}

	listed, err := r.ListUsedPortsOnNode(7)
	if err != nil {
		t.Fatalf("list used: %v", err)
	}
	found := false
	for _, p := range listed {
		if p == 31001 {
			found = true
		}
	}
	if !found {
		t.Fatalf("ListUsedPortsOnNode 未含 peer 预留口（联邦侧对齐缺失）")
	}

	// 释放中的预留（status=0）不应再占用。
	if err := r.DB().Model(&model.PeerShareRuntime{}).Where("id = ?", rt.ID).Update("status", 0).Error; err != nil {
		t.Fatalf("release runtime: %v", err)
	}
	occupied, err = r.HasOtherForwardOnNodePort(7, 31001, 0)
	if err != nil {
		t.Fatalf("predicate after release: %v", err)
	}
	if occupied {
		t.Fatalf("status=0 的 peer runtime 不应继续占用端口")
	}
}
