package handler

import (
	"context"
	"sync"

	runtimenft "go-backend/internal/runtime/nftables"
)

// R5-B5 D14: single-flight 下沉到 manager 层。
//
// 问题：批次 3 把 per-node 锁放在 syncNftablesNodeWithOverrides 内，
// clearNftablesNode 直接调 nftablesManager.Clear() 绕过锁。
//
// 新设计：serializedNftManager 包装 nftablesRuntimeManager，所有触碰节点
// nft 状态的方法（Reconcile/Clear/CollectCounters/Test）都必须经 per-node
// 锁。调用方必须传 nodeID，不依赖自觉。锁表清理策略保留（ReleaseNode）。
// 锁内不得持有 DB 事务（本层只做 SSH，无 DB 操作）。
type serializedNftManager struct {
	inner nftablesRuntimeManager
	mu    sync.Mutex
	locks map[int64]*sync.Mutex
}

func newSerializedNftManager(inner nftablesRuntimeManager) *serializedNftManager {
	return &serializedNftManager{
		inner: inner,
		locks: make(map[int64]*sync.Mutex),
	}
}

func (m *serializedNftManager) lockFor(nodeID int64) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	mu, ok := m.locks[nodeID]
	if !ok {
		mu = &sync.Mutex{}
		m.locks[nodeID] = mu
	}
	return mu
}

// ReleaseNode 释放 node_id 的锁表条目（节点删除时调用，防泄漏）。
func (m *serializedNftManager) ReleaseNode(nodeID int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.locks, nodeID)
}

// Unwrap 返回内层 manager（仅测试用，用于断言 fake 的状态）。
func (m *serializedNftManager) Unwrap() nftablesRuntimeManager {
	return m.inner
}

func (m *serializedNftManager) Reconcile(ctx context.Context, nodeID int64, cfg runtimenft.SSHConfig, plan runtimenft.NodePlan) (runtimenft.ApplyResult, error) {
	mu := m.lockFor(nodeID)
	mu.Lock()
	defer mu.Unlock()
	return m.inner.Reconcile(ctx, cfg, plan)
}

func (m *serializedNftManager) Clear(ctx context.Context, nodeID int64, cfg runtimenft.SSHConfig) error {
	mu := m.lockFor(nodeID)
	mu.Lock()
	defer mu.Unlock()
	return m.inner.Clear(ctx, cfg)
}

func (m *serializedNftManager) CollectCounters(ctx context.Context, nodeID int64, cfg runtimenft.SSHConfig) ([]runtimenft.CounterSample, error) {
	mu := m.lockFor(nodeID)
	mu.Lock()
	defer mu.Unlock()
	return m.inner.CollectCounters(ctx, cfg)
}

func (m *serializedNftManager) Test(ctx context.Context, nodeID int64, cfg runtimenft.SSHConfig) error {
	mu := m.lockFor(nodeID)
	mu.Lock()
	defer mu.Unlock()
	return m.inner.Test(ctx, cfg)
}
