package handler

import (
	"context"
	"sync"
	"time"

	runtimenft "go-backend/internal/runtime/nftables"
)

// R5-B5 D14: single-flight 下沉到 manager 层。
//
// 问题：批次 3 把 per-node 锁放在 syncNftablesNodeWithOverrides 内，
// clearNftablesNode 直接调 nftablesManager.Clear() 绕过锁。
//
// 新设计：serializedNftManager 包装 nftablesRuntimeManager，写方法
// （Reconcile/Clear/Test）都必须经 per-node 锁，并置 applying+递增
// generation（D18）。CollectCounters 为只读采集，按 D18 例外不取锁
// （F-134 订正：旧注释称其同样经锁，与实现不符），靠 handler 层
// generation/applying 检查丢弃脏读。调用方必须传 nodeID，不依赖
// 自觉。锁表清理策略保留（ReleaseNode）。
// 锁内不得持有 DB 事务（本层只做 SSH，无 DB 操作）。
type serializedNftManager struct {
	inner nftablesRuntimeManager
	mu    sync.Mutex
	locks map[int64]*sync.Mutex
	// X-P3-20（L-08-2）：锁表引用计数（与 repo 端口锁同型）。ReleaseNode
	// 在引用未归零时只打 zombie 标记，条目待最后一次 unlockFor 后删除，
	// 避免持锁 Reconcile/Clear 期间新 lockFor 生成第二把锁并发进入。
	lockRefs   map[int64]int
	lockZombie map[int64]bool
	// R5-B5B D18: applying 标记（nodeID -> 置位时间戳）与 generation 计数器
	// （nodeID -> 单调递增）。gen 独立于 applying，用于检测采集期间的脏读。
	applying map[int64]time.Time
	gen      map[int64]uint64
}

// applyingTimeout: applying 标记超时，超过即视为 stale（防 reconcile 崩溃
// 后永久阻塞采集）。
const applyingTimeout = 60 * time.Second

func newSerializedNftManager(inner nftablesRuntimeManager) *serializedNftManager {
	return &serializedNftManager{
		inner:      inner,
		locks:      make(map[int64]*sync.Mutex),
		lockRefs:   make(map[int64]int),
		lockZombie: make(map[int64]bool),
		applying:   make(map[int64]time.Time),
		gen:        make(map[int64]uint64),
	}
}

func (m *serializedNftManager) lockFor(nodeID int64) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lockRefs == nil {
		m.lockRefs = make(map[int64]int)
	}
	if m.lockZombie == nil {
		m.lockZombie = make(map[int64]bool)
	}
	mu, ok := m.locks[nodeID]
	if !ok {
		mu = &sync.Mutex{}
		m.locks[nodeID] = mu
	}
	m.lockRefs[nodeID]++
	return mu
}

// unlockFor 释放 lockFor 返回的锁并归还引用；条目已被 ReleaseNode
// 标记且引用归零时在此真正删除（含 applying/gen 状态）。
func (m *serializedNftManager) unlockFor(nodeID int64, mu *sync.Mutex) {
	if mu != nil {
		mu.Unlock()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lockRefs[nodeID]--
	if m.lockRefs[nodeID] <= 0 {
		delete(m.lockRefs, nodeID)
		if m.lockZombie[nodeID] {
			delete(m.lockZombie, nodeID)
			delete(m.locks, nodeID)
			delete(m.applying, nodeID)
			delete(m.gen, nodeID)
		}
	}
}

// ReleaseNode 释放 node_id 的锁表条目（节点删除时调用，防泄漏）。
// 同时清理 applying/gen 条目（D18）。X-P3-20：仍有未释放引用时只打
// zombie 标记，锁条目与 D18 状态待引用归零后由 unlockFor 一并删除。
func (m *serializedNftManager) ReleaseNode(nodeID int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lockRefs[nodeID] > 0 {
		m.lockZombie[nodeID] = true
		return
	}
	delete(m.locks, nodeID)
	delete(m.applying, nodeID)
	delete(m.gen, nodeID)
}

// GetGeneration 返回节点级 generation 计数器（D18：独立于 applying）。
func (m *serializedNftManager) GetGeneration(nodeID int64) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.gen[nodeID]
}

// IsApplying 检查节点是否处于 apply 窗口（D18）。
// 超时（60s）即视为 stale，返回 false。
func (m *serializedNftManager) IsApplying(nodeID int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	ts, ok := m.applying[nodeID]
	if !ok {
		return false
	}
	// D18 修正：超时判断用 now - ts >= 60s。
	if time.Since(ts) >= applyingTimeout {
		return false
	}
	return true
}

// Unwrap 返回内层 manager（仅测试用，用于断言 fake 的状态）。
func (m *serializedNftManager) Unwrap() nftablesRuntimeManager {
	return m.inner
}

func (m *serializedNftManager) Reconcile(ctx context.Context, nodeID int64, cfg runtimenft.SSHConfig, plan runtimenft.NodePlan) (runtimenft.ApplyResult, error) {
	mu := m.lockFor(nodeID)
	mu.Lock()
	defer m.unlockFor(nodeID, mu)
	// R5-B5B D18: 写路径。drain 已在 handler 层、Reconcile 调用之前完成。
	// 置位 applying + 递增 gen（finally 清除，保证异常路径）。
	m.mu.Lock()
	m.gen[nodeID]++
	m.applying[nodeID] = time.Now()
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.applying, nodeID)
		m.mu.Unlock()
	}()
	return m.inner.Reconcile(ctx, cfg, plan)
}

func (m *serializedNftManager) Clear(ctx context.Context, nodeID int64, cfg runtimenft.SSHConfig) error {
	mu := m.lockFor(nodeID)
	mu.Lock()
	defer m.unlockFor(nodeID, mu)
	// F-020（A4-003/X1-202）：Clear 与 Reconcile 同为删表重建型写
	// 操作，必须同样置 applying + 递增 gen。旧实现只有锁：采集侧的
	// D18 脏读防护（查 IsApplying/GetGeneration）对 Clear 全程失明，
	// 在途采集可把清表前的累计样本当 delta 落库并把旧累计值写回
	// nft_counter_state 基线，清表后一段流量被静默漏计。
	m.mu.Lock()
	m.gen[nodeID]++
	m.applying[nodeID] = time.Now()
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.applying, nodeID)
		m.mu.Unlock()
	}()
	return m.inner.Clear(ctx, cfg)
}

func (m *serializedNftManager) CollectCounters(ctx context.Context, nodeID int64, cfg runtimenft.SSHConfig) ([]runtimenft.CounterSample, error) {
	// R5-B5B D18: 采集不获取写锁，允许与 reconcile 并发读。
	// 脏读由 handler 层的 generation 检查兜底（见 collectNftablesNodeTraffic）。
	return m.inner.CollectCounters(ctx, cfg)
}

func (m *serializedNftManager) Test(ctx context.Context, nodeID int64, cfg runtimenft.SSHConfig) error {
	mu := m.lockFor(nodeID)
	mu.Lock()
	defer m.unlockFor(nodeID, mu)
	return m.inner.Test(ctx, cfg)
}
