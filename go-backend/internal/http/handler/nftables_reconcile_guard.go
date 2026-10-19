package handler

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"
)

// R5-B3 D1-003: reconcile 并发与自动触发。
//
// §3 顺序与并发不变量（写在 syncNftablesNodeWithOverrides 入口注释，
// 此处为实现）：
//  1. status=1 的转发，其规则应存在，或处于可观测的待 reconcile 状态。
//  2. 不允许“DB 已删、规则仍在”且无 pending 记录。
//  3. 任何 reconcile 都必须经 per-node single-flight。
//  4. 数据面失败时 DB 不前进（delete/pause/resume 一致）。

// nftAutoFailState 是自动 reconcile 的退避状态（D9）。
type nftAutoFailState struct {
	count       int   // 连续失败次数
	nextAllowed int64 // unix milli，下次允许时间
	gaveUp      bool  // 达到上限，转人工
}

const (
	nftAutoReconcileMaxFailures = 10
	nftAutoReconcileBaseBackoff = 30 * time.Second
	nftAutoReconcileMaxBackoff  = 30 * time.Minute
)

// nftReconcileLockFor 返回 node_id 的 single-flight 锁（R3：按 node_id，
/// 不用全局锁）。调用方必须 Lock/Unlock 配对使用。
func (h *Handler) nftReconcileLockFor(nodeID int64) *sync.Mutex {
	h.nftReconcileMu.Lock()
	defer h.nftReconcileMu.Unlock()
	if h.nftReconcileLocks == nil {
		h.nftReconcileLocks = make(map[int64]*sync.Mutex)
	}
	mu, ok := h.nftReconcileLocks[nodeID]
	if !ok {
		mu = &sync.Mutex{}
		h.nftReconcileLocks[nodeID] = mu
	}
	return mu
}

// releaseNftReconcileLock 释放 node_id 的锁表条目（R3：节点删除时调用，
// 避免长期运行后 map 增长）。仅在确认无进行中 reconcile 时调用。
func (h *Handler) releaseNftReconcileLock(nodeID int64) {
	h.nftReconcileMu.Lock()
	defer h.nftReconcileMu.Unlock()
	delete(h.nftReconcileLocks, nodeID)
}

// isNftTableMissingError 判断是否为“表不存在”（节点重启后表丢失，D9(a)）。
// nft 缺表时的典型错误含 "No such file or directory"。
func isNftTableMissingError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such file or directory") ||
		strings.Contains(msg, "no such table") ||
		strings.Contains(msg, "table not found")
}

// enqueueNftAutoReconcile 投递异步自动 reconcile 任务（R2）。
// 去重：同一 node 在队列中只保留一个，避免 30s 周期堆积。
// 调用方（采集循环）在事务外调用，立即返回，不阻塞。
func (h *Handler) enqueueNftAutoReconcile(nodeID int64, reason string) {
	if h == nil || nodeID <= 0 {
		return
	}
	h.nftAutoMu.Lock()
	if h.nftAutoPending[nodeID] {
		h.nftAutoMu.Unlock()
		return
	}
	// 已放弃的节点不再投递（转人工）。
	if st := h.nftAutoFail[nodeID]; st != nil && st.gaveUp {
		h.nftAutoMu.Unlock()
		return
	}
	if h.nftAutoPending == nil {
		h.nftAutoPending = make(map[int64]bool)
	}
	if h.nftAutoQueue == nil {
		h.nftAutoQueue = make(chan int64, 64)
	}
	h.nftAutoPending[nodeID] = true
	h.nftAutoMu.Unlock()

	select {
	case h.nftAutoQueue <- nodeID:
		log.Printf("nftables auto-reconcile enqueued node_id=%d reason=%s", nodeID, reason)
	default:
		// 队列满时丢弃（下次采集周期会重新投递），避免阻塞采集循环。
		h.nftAutoMu.Lock()
		delete(h.nftAutoPending, nodeID)
		h.nftAutoMu.Unlock()
		log.Printf("nftables auto-reconcile queue full node_id=%d reason=%s action=dropped", nodeID, reason)
	}
}

// markNftApplyFailed 标记节点最近一次 apply 失败（D9(b)）。
func (h *Handler) markNftApplyFailed(nodeID int64, failed bool) {
	if h == nil || nodeID <= 0 {
		return
	}
	h.nftApplyFailedMu.Lock()
	defer h.nftApplyFailedMu.Unlock()
	if h.nftApplyFailed == nil {
		h.nftApplyFailed = make(map[int64]bool)
	}
	if failed {
		h.nftApplyFailed[nodeID] = true
	} else {
		delete(h.nftApplyFailed, nodeID)
	}
}

// checkNftNodeRecovered 供采集循环调用：若节点曾 apply 失败而现在采集成功，
// 说明节点恢复可达，触发自动 reconcile（D9(b)），返回 true。
func (h *Handler) checkNftNodeRecovered(nodeID int64) bool {
	if h == nil || nodeID <= 0 {
		return false
	}
	h.nftApplyFailedMu.Lock()
	failed := h.nftApplyFailed[nodeID]
	if failed {
		delete(h.nftApplyFailed, nodeID)
	}
	h.nftApplyFailedMu.Unlock()
	if failed {
		h.enqueueNftAutoReconcile(nodeID, "apply_failed_node_recovered")
		return true
	}
	return false
}

// runNftAutoReconcileWorker 是自动 reconcile 的独立 worker（R2）。
// 从队列取任务，带指数退避与失败上限，超限后停止并告警转人工。
func (h *Handler) runNftAutoReconcileWorker(ctx context.Context) {
	defer h.jobsWG.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case nodeID := <-h.nftAutoQueue:
			h.nftAutoMu.Lock()
			delete(h.nftAutoPending, nodeID)
			st := h.nftAutoFail[nodeID]
			nowMs := time.Now().UnixMilli()
			if st != nil && (st.gaveUp || nowMs < st.nextAllowed) {
				h.nftAutoMu.Unlock()
				continue
			}
			h.nftAutoMu.Unlock()

			if err := h.syncNftablesNode(nodeID); err != nil {
				h.recordNftAutoReconcileFailure(nodeID, err)
				continue
			}
			// 成功：清退避状态。
			h.nftAutoMu.Lock()
			delete(h.nftAutoFail, nodeID)
			h.nftAutoMu.Unlock()
			log.Printf("nftables auto-reconcile succeeded node_id=%d", nodeID)
		}
	}
}

// recordNftAutoReconcileFailure 记录自动 reconcile 失败，指数退避；
// 达到上限后停止重试并告警（可观测信号，含 node_id/失败次数/最后错误）。
func (h *Handler) recordNftAutoReconcileFailure(nodeID int64, err error) {
	h.nftAutoMu.Lock()
	defer h.nftAutoMu.Unlock()
	if h.nftAutoFail == nil {
		h.nftAutoFail = make(map[int64]*nftAutoFailState)
	}
	st, ok := h.nftAutoFail[nodeID]
	if !ok {
		st = &nftAutoFailState{}
		h.nftAutoFail[nodeID] = st
	}
	st.count++
	if st.count >= nftAutoReconcileMaxFailures {
		st.gaveUp = true
		log.Printf("nftables auto-reconcile exhausted node_id=%d failures=%d last_error=%q action=manual_reconcile_required",
			nodeID, st.count, err.Error())
		return
	}
	backoff := nftAutoReconcileBaseBackoff << (st.count - 1)
	if backoff > nftAutoReconcileMaxBackoff {
		backoff = nftAutoReconcileMaxBackoff
	}
	st.nextAllowed = time.Now().Add(backoff).UnixMilli()
	log.Printf("nftables auto-reconcile failed node_id=%d failures=%d backoff=%s err=%v",
		nodeID, st.count, backoff, err)
	// 退避后由下次采集周期重新投递（表仍缺失时会再次检测到）。
}
