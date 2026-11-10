package handler

import (
	"context"
	"log"
	"time"

	"go-backend/internal/store/model"
)

// R4B03-001：节点在线真值的源头是 ws.Server 的内存会话表，但 DB
// status 只在连接/断开两事件单向同步——面板进程退出时断开 defer
// 不执行，停机期间死亡的节点在 DB 里永久停在 status=1。两腿收口：
// ① 启动装配完成时批量复位本地非 nftables 节点的残留在线标记；
// ② 后台周期对账：DB 在线但 WS 无会话、且持续超过重连宽限的节点
// 复位为离线。nftables 节点创建即 status=1、无 agent 会话语义，
// 远程节点不经本地会话表，两腿都不碰。

// nodeStatusReconcileInterval 是周期对账的扫描间隔。
var nodeStatusReconcileInterval = 30 * time.Second

// nodeStatusReconcileGrace 是复位前的持续离线宽限：必须大于 WS
// pong 窗口（wsPongWait=45s）量级，覆盖 agent 断线重连与面板侧
// 会话表抖动的正常窗口，避免把正在重连的节点误清为离线。
var nodeStatusReconcileGrace = 90 * time.Second

// resetLocalNodeStatusesAtStartup 执行启动清扫腿。装配期同步调用：
// 此刻 WS 会话表必然为空，DB 中 status=1 的本地非 nftables 节点
// 全部是上一个进程周期残留的僵尸在线；agent 重连会经握手路径
// 立即置回 1，不会误伤真实在线节点。
func (h *Handler) resetLocalNodeStatusesAtStartup() {
	if h == nil || h.repo == nil {
		return
	}
	// F2-02：复位前先快照在线集——复位后 checkStartupOfflineNodes
	// 以快照判定「重启前在线、重启后仍未回连」，不再依赖已被
	// 复位清零的 DB status（旧实现下本地节点结构性永不告警）。
	if ids, err := h.repo.ListOnlineLocalNodeIDs(); err == nil {
		snapshot := make(map[int64]struct{}, len(ids))
		for _, id := range ids {
			snapshot[id] = struct{}{}
		}
		h.startupOnlineSnapshot = snapshot
	} else {
		log.Printf("node status startup snapshot failed: %v", err)
	}
	reset, err := h.repo.ResetLocalNodeStatusesAtStartup()
	if err != nil {
		log.Printf("node status startup reset failed: %v", err)
		return
	}
	if reset > 0 {
		log.Printf("node status startup reset: %d stale online node(s) marked offline", reset)
	}
}

func (h *Handler) runNodeStatusReconcileLoop(ctx context.Context) {
	defer h.jobsWG.Done()
	// offlineSince 记录每个候选节点首次被观察到「DB 在线但 WS 无
	// 会话」的時刻；仅由本循环 goroutine 读写，无需加锁。
	offlineSince := make(map[int64]time.Time)
	ticker := time.NewTicker(nodeStatusReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			h.reconcileNodeOnlineStatuses(now, offlineSince)
		}
	}
}

// reconcileNodeOnlineStatuses 执行一轮对账。now 与 offlineSince 由
// 调用方传入（循环传真实时钟与自有状态），便于回归测试以受控时
// 刻钉住宽限语义。
func (h *Handler) reconcileNodeOnlineStatuses(now time.Time, offlineSince map[int64]time.Time) {
	if h == nil || h.repo == nil {
		return
	}
	nodes, err := h.repo.ListOnlineLocalNodes()
	if err != nil {
		log.Printf("node status reconcile: list online nodes failed: %v", err)
		return
	}
	candidates := make(map[int64]struct{}, len(nodes))
	for _, node := range nodes {
		if !nodeStatusReconcileCandidate(node) {
			continue
		}
		candidates[node.ID] = struct{}{}
		if h.wsServer != nil && h.wsServer.IsNodeOnline(node.ID) {
			// 有实时会话即真值在线，清掉离线计时（重连节点不误清）。
			delete(offlineSince, node.ID)
			continue
		}
		firstSeen, ok := offlineSince[node.ID]
		if !ok {
			offlineSince[node.ID] = now
			continue
		}
		if now.Sub(firstSeen) < nodeStatusReconcileGrace {
			continue
		}
		if err := h.repo.UpdateNodeStatus(node.ID, 0); err != nil {
			log.Printf("node status reconcile: reset node_id=%d failed: %v", node.ID, err)
			continue
		}
		// F2-02 同族（B1/O6）：对账复位是状态翻转点，须与 WS 断
		// 开路径同口径触发掉线告警登记——断开钩子未达成的翻转
		// （如进程内边缘路径）此前只改 DB 不告警。notifyNodeOffline
		// 内部有已告警去重，正常断开路径不会双发。
		h.notifyNodeOffline(node.ID)
		if h.wsServer != nil {
			h.wsServer.BroadcastNodeStatus(node.ID, 0)
		}
		delete(offlineSince, node.ID)
	}
	// 剪除已不再是候选（已复位/模式变更/被删）的计时残留。
	for id := range offlineSince {
		if _, ok := candidates[id]; !ok {
			delete(offlineSince, id)
		}
	}
}

// nodeStatusReconcileCandidate 判定节点是否在对账范围内：本地
// 且非 nftables 模式。nftables
// 节点 status=1 是创建时的既定口径、无会话语义，永不复位。
func nodeStatusReconcileCandidate(node model.Node) bool {
	return defaultNodeForwardMode(node.ForwardMode) != "nftables"
}
