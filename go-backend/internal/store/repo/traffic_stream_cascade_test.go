package repo_test

import (
	"path/filepath"
	"strings"
	"testing"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// 流量可靠上报 v2 的流游标按 scope 前缀归属节点、无 node_id 列：
// DeleteNodeCascade 必须按前缀补删，否则节点每次换 stream 都在
// traffic_stream_cursor 留一行永久孤儿。前缀含结尾冒号——节点 12
// 的级联不得误伤节点 123。
func TestDeleteNodeCascadeClearsTrafficStreamCursors(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "traffic-stream-cascade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()

	for _, id := range []int64{12, 123} {
		if err := r.DB().Create(&model.Node{ID: id, Name: "node"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	digest := strings.Repeat("a", 64)
	scopes := []string{
		"flow:12:stream-a",
		"flow:12:stream-b",
		"flow:123:stream-a",
		"flow:999:stream-other-node",
	}
	for _, scope := range scopes {
		if err := r.DB().Create(&repo.TrafficStreamCursor{Scope: scope, Sequence: 1, Digest: digest}).Error; err != nil {
			t.Fatal(err)
		}
	}
	countScope := func(pattern string) int64 {
		var c int64
		if err := r.DB().Model(&repo.TrafficStreamCursor{}).Where("scope LIKE ?", pattern).Count(&c).Error; err != nil {
			t.Fatal(err)
		}
		return c
	}

	if err := r.DeleteNodeCascade(12); err != nil {
		t.Fatal(err)
	}
	if c := countScope("flow:12:%"); c != 0 {
		t.Fatalf("node 12 cursors orphaned after cascade: %d", c)
	}
	if c := countScope("flow:123:%"); c != 1 {
		t.Fatalf("node 123 cursors must survive node 12 cascade, got %d", c)
	}
	if c := countScope("flow:999:%"); c != 1 {
		t.Fatalf("unrelated node cursors must survive, got %d", c)
	}

	if err := r.DeleteNodeCascade(123); err != nil {
		t.Fatal(err)
	}
	if c := countScope("flow:123:%"); c != 0 {
		t.Fatalf("node 123 cursors orphaned after cascade: %d", c)
	}
	if c := countScope("flow:999:%"); c != 1 {
		t.Fatalf("unrelated node cursors must survive, got %d", c)
	}
}
