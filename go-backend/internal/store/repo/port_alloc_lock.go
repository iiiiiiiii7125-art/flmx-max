package repo

import (
	"sync"
)

// R5-B4: per-node 端口分配锁。
//
// 背景：端口空间在 forward_port、chain_tunnel、node_inbound 三张表之间共享。
// 三张表各加 (node_id, port) 唯一索引后，同表并发可被 DB 挡住，
// 但跨表并发（建转发 vs 建隧道 vs 建入站）DB 挡不住。
//
// 设计：
//   - 进程内按 node_id 的 mutex，覆盖三个分配入口。
//   - 锁在事务之前获取，事务提交/回滚后释放。
//   - 锁内禁止网络调用（SSH / HTTP / agent 命令）。
//   - 节点删除时清理锁表条目。
//   - 依赖单实例部署（passkey.md 已声明当前应保持单实例）。
//     多实例部署下跨表竞态仍有窗口，需外部协调（本批不实现，登记为限制）。
type portAllocLocks struct {
	mu    sync.Mutex
	locks map[int64]*sync.Mutex
}

var globalPortAllocLocks = &portAllocLocks{
	locks: make(map[int64]*sync.Mutex),
}

// AcquirePortAllocLock 获取 node_id 的端口分配锁。
// 调用方必须在事务之前调用，事务结束后调用 ReleasePortAllocLock。
// 锁内不得进行网络调用。
func AcquirePortAllocLock(nodeID int64) *sync.Mutex {
	globalPortAllocLocks.mu.Lock()
	mu, ok := globalPortAllocLocks.locks[nodeID]
	if !ok {
		mu = &sync.Mutex{}
		globalPortAllocLocks.locks[nodeID] = mu
	}
	globalPortAllocLocks.mu.Unlock()
	mu.Lock()
	return mu
}

// ReleasePortAllocLock 释放端口分配锁。
func ReleasePortAllocLock(mu *sync.Mutex) {
	mu.Unlock()
}

// CleanupPortAllocLock 清理 node_id 的锁表条目（节点删除时调用）。
func CleanupPortAllocLock(nodeID int64) {
	globalPortAllocLocks.mu.Lock()
	defer globalPortAllocLocks.mu.Unlock()
	delete(globalPortAllocLocks.locks, nodeID)
}
