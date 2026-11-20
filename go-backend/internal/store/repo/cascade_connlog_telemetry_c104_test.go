package repo_test

import (
	"path/filepath"
	"testing"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R18-C1-04 回归：删节点级联漏清连接日志遥测三表——inbound_conn_log
// 行受保留期 prune 兜底终会消失，但 connlog_node_dropped 与
// traffic_node_dropped 是累计计数器、无任何保留期或清扫，每删
// 一个节点永久多两行。修复后三表与既有清单同口径、同事务清理。
func TestDeleteNodeCascadeClearsConnlogTelemetry(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "connlog-cascade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()

	for _, id := range []int64{12, 123} {
		if err := r.DB().Create(&model.Node{ID: id, Name: "node"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	seed := []model.InboundConnLog{
		{EntryNodeID: 12, ConnID: "a", ClientIP: "203.0.113.1", StartTime: 1_700_000_000_000, CreatedTime: 1_700_000_000_000, Status: repo.ConnlogStatusClosed},
		{EntryNodeID: 123, ConnID: "b", ClientIP: "203.0.113.1", StartTime: 1_700_000_000_000, CreatedTime: 1_700_000_000_000, Status: repo.ConnlogStatusClosed},
	}
	for i := range seed {
		if err := r.DB().Create(&seed[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []int64{12, 123} {
		if err := r.DB().Create(&model.ConnlogNodeDropped{NodeID: id, DroppedTotal: 7}).Error; err != nil {
			t.Fatal(err)
		}
		if err := r.DB().Create(&model.TrafficNodeDropped{NodeID: id, DroppedTotal: 9}).Error; err != nil {
			t.Fatal(err)
		}
	}
	count := func(table string, nodeID int64, col string) int64 {
		var c int64
		if err := r.DB().Table(table).Where(col+" = ?", nodeID).Count(&c).Error; err != nil {
			t.Fatal(err)
		}
		return c
	}

	if err := r.DeleteNodeCascade(12); err != nil {
		t.Fatal(err)
	}
	if c := count("inbound_conn_log", 12, "entry_node_id"); c != 0 {
		t.Fatalf("node 12 connlog rows orphaned after cascade: %d", c)
	}
	if c := count("connlog_node_dropped", 12, "node_id"); c != 0 {
		t.Fatalf("node 12 connlog dropped rows orphaned after cascade: %d", c)
	}
	if c := count("traffic_node_dropped", 12, "node_id"); c != 0 {
		t.Fatalf("node 12 traffic dropped rows orphaned after cascade: %d", c)
	}
	if c := count("inbound_conn_log", 123, "entry_node_id"); c != 1 {
		t.Fatalf("node 123 connlog rows must survive node 12 cascade, got %d", c)
	}
	if c := count("connlog_node_dropped", 123, "node_id"); c != 1 {
		t.Fatalf("node 123 connlog dropped rows must survive, got %d", c)
	}
	if c := count("traffic_node_dropped", 123, "node_id"); c != 1 {
		t.Fatalf("node 123 traffic dropped rows must survive, got %d", c)
	}

	if err := r.DeleteNodeCascade(123); err != nil {
		t.Fatal(err)
	}
	if c := count("inbound_conn_log", 123, "entry_node_id"); c != 0 {
		t.Fatalf("node 123 connlog rows orphaned after cascade: %d", c)
	}
	if c := count("connlog_node_dropped", 123, "node_id"); c != 0 {
		t.Fatalf("node 123 connlog dropped rows orphaned after cascade: %d", c)
	}
	if c := count("traffic_node_dropped", 123, "node_id"); c != 0 {
		t.Fatalf("node 123 traffic dropped rows orphaned after cascade: %d", c)
	}
}
