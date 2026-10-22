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
//
// X-P3-20（L-08-2）：锁表改引用计数。旧 CleanupPortAllocLock 无条件
// 删表项：与并发 Acquire 交错时，持锁者仍在临界区内，第二个 Acquire
// 会拿到全新 mutex 与之双持有同 nodeID 临界区。现 Acquire 计引用、
// Release 归还引用；Cleanup 在引用未归零时只打 zombie 标记（条目
// 保留、后续 Acquire 复用同一 mutex），引用归零后才真正删除。
type portAllocLockEntry struct {
	nodeID int64
	mu     *sync.Mutex
	refs   int
	zombie bool
}

type portAllocLocks struct {
	mu      sync.Mutex
	locks   map[int64]*portAllocLockEntry
	byMutex map[*sync.Mutex]*portAllocLockEntry
}

var globalPortAllocLocks = &portAllocLocks{
	locks:   make(map[int64]*portAllocLockEntry),
	byMutex: make(map[*sync.Mutex]*portAllocLockEntry),
}

// AcquirePortAllocLock 获取 node_id 的端口分配锁。
// 调用方必须在事务之前调用，事务结束后调用 ReleasePortAllocLock。
// 锁内不得进行网络调用。
func AcquirePortAllocLock(nodeID int64) *sync.Mutex {
	globalPortAllocLocks.mu.Lock()
	e, ok := globalPortAllocLocks.locks[nodeID]
	if !ok {
		e = &portAllocLockEntry{nodeID: nodeID, mu: &sync.Mutex{}}
		globalPortAllocLocks.locks[nodeID] = e
		globalPortAllocLocks.byMutex[e.mu] = e
	}
	e.refs++
	mu := e.mu
	globalPortAllocLocks.mu.Unlock()
	mu.Lock()
	return mu
}

// ReleasePortAllocLock 释放端口分配锁并归还引用；若该条目已被
// Cleanup 标记且引用归零，在此真正删除表项。
func ReleasePortAllocLock(mu *sync.Mutex) {
	if mu == nil {
		return
	}
	mu.Unlock()
	globalPortAllocLocks.mu.Lock()
	defer globalPortAllocLocks.mu.Unlock()
	e, ok := globalPortAllocLocks.byMutex[mu]
	if !ok {
		return
	}
	e.refs--
	if e.refs <= 0 && e.zombie {
		delete(globalPortAllocLocks.locks, e.nodeID)
		delete(globalPortAllocLocks.byMutex, mu)
	}
}

// CleanupPortAllocLock 清理 node_id 的锁表条目（节点删除时调用）。
// 仍有未释放引用时只打 zombie 标记，不删条目（防双持有竞态）。
func CleanupPortAllocLock(nodeID int64) {
	globalPortAllocLocks.mu.Lock()
	defer globalPortAllocLocks.mu.Unlock()
	e, ok := globalPortAllocLocks.locks[nodeID]
	if !ok {
		return
	}
	if e.refs <= 0 {
		delete(globalPortAllocLocks.locks, nodeID)
		delete(globalPortAllocLocks.byMutex, e.mu)
		return
	}
	e.zombie = true
}
