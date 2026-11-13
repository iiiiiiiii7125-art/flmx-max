package repo_test

import (
	"path/filepath"
	"strings"
	"testing"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R2-038 实例①：游标行按 updated_time 保留期清扫——过期行删、
// 活跃行留、重复执行幂等。
func TestDeleteStaleTrafficStreamCursors(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "traffic-stream-sweep.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()

	digest := strings.Repeat("a", 64)
	const cutoff = int64(1_000_000)
	rows := map[string]int64{
		"flow:1:stale":  cutoff - 1,
		"flow:1:edge":   cutoff, // 恰在截止线上：保留（删除条件为 <）
		"flow:1:active": cutoff + 5000,
	}
	for scope, updated := range rows {
		if err := r.DB().Create(&repo.TrafficStreamCursor{Scope: scope, Sequence: 3, Digest: digest, UpdatedTime: updated}).Error; err != nil {
			t.Fatal(err)
		}
	}

	deleted, err := r.DeleteStaleTrafficStreamCursors(cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("expected exactly the stale row deleted, got %d", deleted)
	}
	var remaining []string
	if err := r.DB().Model(&repo.TrafficStreamCursor{}).Order("scope").Pluck("scope", &remaining).Error; err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 2 || remaining[0] != "flow:1:active" || remaining[1] != "flow:1:edge" {
		t.Fatalf("unexpected remaining scopes: %v", remaining)
	}

	deleted, err = r.DeleteStaleTrafficStreamCursors(cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 0 {
		t.Fatalf("sweep must be idempotent, second run deleted %d", deleted)
	}
}

// R2-038 实例②的归属全集：节点上全部现存转发占用的端口
// （升序），其他节点的端口不混入。（forward_port 有
// UNIQUE(node_id, port) 索引，同口同节点至多一行，DISTINCT
// 只是防御。）
func TestListForwardPortsByNode(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "forward-ports-by-node.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()

	seed := []model.ForwardPort{
		{ForwardID: 10, NodeID: 1, Port: 3002},
		{ForwardID: 10, NodeID: 1, Port: 3001},
		{ForwardID: 12, NodeID: 2, Port: 3003}, // 其他节点：不混入
	}
	for i := range seed {
		if err := r.DB().Create(&seed[i]).Error; err != nil {
			t.Fatal(err)
		}
	}

	ports, err := r.ListForwardPortsByNode(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(ports) != 2 || ports[0] != 3001 || ports[1] != 3002 {
		t.Fatalf("expected sorted [3001 3002], got %v", ports)
	}

	ports, err = r.ListForwardPortsByNode(99)
	if err != nil {
		t.Fatal(err)
	}
	if len(ports) != 0 {
		t.Fatalf("node without forwards must yield no ports, got %v", ports)
	}
}

// Lane C: the batch form of the same query (nodeList's mark gate):
// one call, per-node sorted ports, nodes without forwards absent
// from the map (not present with an empty slice).
func TestListForwardPortsByNodes(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "forward-ports-by-nodes.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()

	seed := []model.ForwardPort{
		{ForwardID: 10, NodeID: 1, Port: 3002},
		{ForwardID: 10, NodeID: 1, Port: 3001},
		{ForwardID: 12, NodeID: 2, Port: 3003},
	}
	for i := range seed {
		if err := r.DB().Create(&seed[i]).Error; err != nil {
			t.Fatal(err)
		}
	}

	byNode, err := r.ListForwardPortsByNodes([]int64{1, 2, 99})
	if err != nil {
		t.Fatal(err)
	}
	if len(byNode[1]) != 2 || byNode[1][0] != 3001 || byNode[1][1] != 3002 {
		t.Fatalf("node 1 = %v, want sorted [3001 3002]", byNode[1])
	}
	if len(byNode[2]) != 1 || byNode[2][0] != 3003 {
		t.Fatalf("node 2 = %v, want [3003]", byNode[2])
	}
	if _, ok := byNode[99]; ok {
		t.Fatalf("node 99 must be absent from the map, got %v", byNode[99])
	}

	empty, err := r.ListForwardPortsByNodes(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Fatalf("empty input must yield an empty map, got %v", empty)
	}
}
