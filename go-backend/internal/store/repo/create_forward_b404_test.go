package repo

import (
	"testing"
	"time"
)

// B4-04: 重复入口行（备份恢复可达：importTunnels 不去重、
// TunnelEntryNodeIDs 无 DISTINCT）喂给 CreateForwardTx 时，旧实现
// 对同一节点连续两次取不可重入的端口锁，创建请求自死锁并楔死
// 该节点的端口分配。修复后入口列表先去重：创建须在限时内完成，
// 且只写一行 forward_port。本用例在独立 goroutine 中调用并设
// 超时——死锁形态下用例判红而非挂死整个测试进程后续用例。
func TestCreateForwardTxDuplicateEntryNodesDoNotDeadlock(t *testing.T) {
	r := setupB4TestDB(t)
	now := time.Now().UnixMilli()

	type result struct {
		id  int64
		err error
	}
	done := make(chan result, 1)
	go func() {
		id, err := r.CreateForwardTx(1, "test", "fwd-dup", 0, "", "fifo", now, 0, []int64{1, 1}, 31000, "", nil, 0, 0, nil, 0, 0, 0)
		done <- result{id, err}
	}()

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("create forward with duplicated entry nodes: %v", res.err)
		}
		var rows int64
		if err := r.db.Raw(`SELECT COUNT(*) FROM forward_port WHERE forward_id = ?`, res.id).Scan(&rows).Error; err != nil {
			t.Fatalf("count forward_port rows: %v", err)
		}
		if rows != 1 {
			t.Fatalf("duplicated entry nodes must land exactly one forward_port row, got %d", rows)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("CreateForwardTx deadlocked on duplicated entry nodes (same port mutex taken twice)")
	}
}
