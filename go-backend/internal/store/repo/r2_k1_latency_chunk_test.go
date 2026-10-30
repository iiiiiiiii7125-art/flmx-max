package repo

import (
	"path/filepath"
	"testing"

	"go-backend/internal/store/model"
)

// R2 X01-001（硬化）：SetNodesLatencyTargets 被调层分块——40000 个 id
// 不得再撞 SQLite 变量上限（32766），已存在的节点行须全部更新。
func TestR2K1SetNodesLatencyTargetsChunked(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	var wantIDs []int64
	for i := 0; i < 3; i++ {
		node := &model.Node{Name: "k1-node", ServerIP: "192.0.2.1"}
		if err := r.db.Create(node).Error; err != nil {
			t.Fatalf("create node: %v", err)
		}
		wantIDs = append(wantIDs, node.ID)
	}
	ids := make([]int64, 0, 40000)
	for i := int64(1); i <= 40000; i++ {
		ids = append(ids, i)
	}
	if err := r.SetNodesLatencyTargets(ids, `["a.example"]`); err != nil {
		t.Fatalf("SetNodesLatencyTargets(40000 ids): %v", err)
	}
	targets, err := r.ListNodeLatencyTargets()
	if err != nil {
		t.Fatalf("list targets: %v", err)
	}
	for _, id := range wantIDs {
		if targets[id] != `["a.example"]` {
			t.Fatalf("node %d latency_targets = %q, want set", id, targets[id])
		}
	}
}
