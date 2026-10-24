package handler

import (
	"fmt"
	"log"

	"go-backend/internal/store/model"
)

// R13X-001（P2，发布阻断项）：backupImport 成功后对受影响节点集
// 补一次转发重布。此前导入尾钩子只有内核 reconcile / 计量对账 /
// 撤销清转发三条路径，在线节点的 gost 服务与 nft 规则没有任何重推
// 触发——恢复出的转发不监听或按旧参数服役，面板却显示 active、
// 回执报成功（静默不一致，窗口无限期）。
//
// 复用既有路径，不发明新同步机制：
//   - 中转 RemoteAddr 重算：正常写路径的 resyncManagedRelayRemoteAddrs*
//     口径（隧道侧按节点 active 隧道、入站侧按备份内入站行）；
//   - 转发重布：onNodeOnline 同款 redeployLocalNodeRuntime（逐隧道
//     redeployTunnelAndForwards + 逐转发 syncForwardServices，nft 模式
//     转发经 syncForwardServices 内 syncNftablesNode 入队口径覆盖）；
//   - 离线节点不碰：其重连路径 onNodeOnline → redeployLocalNodeRuntime
//     本就全量重布，行为不变（本函数只认 ws live 连接）。
// 同步执行、失败不静默：重推结果（至少失败节点清单）写入回执；
// 重推失败不改写「导入成功」的结论（导入事务已提交），失败节点
// 重连后再次全量重布收敛。

// backupImportNeedsForwardRepush reports whether the import landed rows
// in a segment that can change forward data-plane state. Kernel-only /
// subscription-only / source-only inbounds imports do not touch
// forwards and are excluded; relay rows are counted separately because
// a relays-only inbounds import lands zero inbound rows.
// FINAL-P2-1: speed-limit rows and user-tunnel rows also change the
// limiter the node enforces (forward speed_id/ip_speed_id direct refs
// and user_tunnel.speed_id indirect refs are resolved at sync time, so
// a limiter-only import with no repush leaves the node enforcing the
// stale value indefinitely); both segments trigger the same repush.
func backupImportNeedsForwardRepush(result *model.ImportResult) bool {
	if result == nil {
		return false
	}
	return result.ForwardsImported > 0 || result.TunnelsImported > 0 ||
		result.NodesImported > 0 || result.InboundsImported > 0 ||
		result.InboundRelaysImported > 0 ||
		result.SpeedLimitsImported > 0 || result.UserTunnelsImported > 0
}

type forwardRepushOutcome struct {
	attempted []int64
	failed    []int64
	warnings  []string
}

// repushForwardRuntimeAfterImport replays the current DB forward state
// onto every online forward-serving node. It runs after the import
// transaction and after the revoked-authorization cleanup, so the state
// it pushes is the final post-import state.
func (h *Handler) repushForwardRuntimeAfterImport(result *model.ImportResult, backup *model.BackupData) forwardRepushOutcome {
	var out forwardRepushOutcome
	if h == nil || h.repo == nil || !backupImportNeedsForwardRepush(result) {
		return out
	}
	nodeIDs, err := h.repo.ListForwardNodeIDs()
	if err != nil {
		out.warnings = append(out.warnings, fmt.Sprintf("备份恢复：转发重推未能列出受影响节点（%v），请检查节点转发状态", err))
		return out
	}
	for _, nodeID := range nodeIDs {
		// Offline nodes are left to the reconnect path (onNodeOnline),
		// whose behavior is unchanged; a nil wsServer means no live
		// agent connections at all, so every node takes that path.
		if h.wsServer == nil || !h.wsServer.IsNodeOnline(nodeID) {
			continue
		}
		// 1) 中转 RemoteAddr 重算（写路径口径）：先修 DB，再重布。
		tunnelIDs, err := h.repo.ListActiveTunnelIDsByNode(nodeID)
		if err != nil {
			out.warnings = append(out.warnings, fmt.Sprintf("备份恢复：节点 #%d 的隧道列表读取失败（%v），该节点中转地址重算已跳过", nodeID, err))
		} else {
			for _, tunnelID := range tunnelIDs {
				out.warnings = append(out.warnings, h.resyncManagedRelayRemoteAddrsForTunnel(tunnelID)...)
			}
		}
		if backup != nil && backup.Inbounds != nil {
			for _, inbound := range backup.Inbounds.Inbounds {
				out.warnings = append(out.warnings, h.resyncManagedRelayRemoteAddrsForInbound(inbound.ID)...)
			}
		}
		// 2) 整节点转发重布（onNodeOnline 同款路径）。
		out.attempted = append(out.attempted, nodeID)
		if !h.redeployLocalNodeRuntime(nodeID) {
			out.failed = append(out.failed, nodeID)
			msg := fmt.Sprintf("备份恢复：节点 #%d 的转发重推失败，该节点上的转发数据面可能与数据库不一致，节点重连后将再次全量重推", nodeID)
			out.warnings = append(out.warnings, msg)
			log.Printf("backupImport repush: node %d failed", nodeID)
		}
	}
	return out
}
