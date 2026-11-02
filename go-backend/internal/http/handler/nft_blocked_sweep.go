package handler

import (
	"context"
	"log"
	"sort"

	"go-backend/internal/store/model"
)

// R2-038 实例②：转发在暂停态被删除时，其入口端口留在节点
// flmx_quota 的 blocked_ports 集里——sync 的解除扫描只遍历当前
// plan 的端口，已删转发的端口不在 plan 内，只剩「端口被新转发
// 复用」或「整节点删除删整表」两条回收路径。本文件的周期清扫
// 按节点当前转发全集对账：封禁集中不属于任何现存转发的端口
// （无主端口）才解除。
//
// 不误伤在用封禁的谓词基础：暂停中的转发仍持有 forward_port
// 行，其端口属于「有主」集，一律不碰——现存转发的封禁状态由
// sync 的期望集（DB 已暂停 ∪ 操作意图）全权管理，清扫不做
// 第二套期望推导。
//
// 竞态与自愈：归属集在读完节点封禁集之后再查 DB，窗口已尽量
// 小；残余窗口（复核后、解除前恰有新转发复用该端口并被暂停）
// 的后果有界——该转发的下一次 sync 会按 DB 期望集重新封禁，
// 且窗口内该端口本就无 rule 在 plan 中（转发刚建），无流量
// 误放行面。

// orphanBlockedPorts 是清扫的纯决策：blocked 中不在 ownedPorts
// 内的端口（升序）。ownedPorts 是节点当前转发全集占用的端口。
func orphanBlockedPorts(blocked []int, ownedPorts []int) []int {
	owned := make(map[int]bool, len(ownedPorts))
	for _, port := range ownedPorts {
		owned[port] = true
	}
	var orphans []int
	for _, port := range blocked {
		if !owned[port] {
			orphans = append(orphans, port)
		}
	}
	sort.Ints(orphans)
	return orphans
}

// sweepOrphanedBlockedPorts 逐 nftables 节点清扫无主封禁端口。
// fail-soft（卫生面，见 state_sweep.go 的口径说明）：单节点失败
// 只记日志，残留下轮再清，不影响其他节点与维护轮。
func (h *Handler) sweepOrphanedBlockedPorts() {
	if h == nil || h.repo == nil || h.nftablesManager == nil {
		return
	}
	nodes, err := h.repo.ListNftablesNodesForCollection()
	if err != nil {
		log.Printf("nftables blocked-port sweep failed op=list_nodes err=%v", err)
		return
	}
	for i := range nodes {
		node := &nodes[i]
		if err := h.sweepNodeOrphanedBlockedPorts(context.Background(), node.NodeID, &node.Config); err != nil {
			log.Printf("nftables blocked-port sweep failed node_id=%d err=%v", node.NodeID, err)
		}
	}
}

func (h *Handler) sweepNodeOrphanedBlockedPorts(ctx context.Context, nodeID int64, cfgModel *model.NodeSSHConfig) error {
	sshCfg, err := sshConfigFromModel(cfgModel)
	if err != nil {
		return err
	}
	blocked, err := h.nftablesManager.ListBlockedPorts(ctx, nodeID, sshCfg)
	if err != nil {
		return err
	}
	if len(blocked) == 0 {
		return nil
	}
	ownedPorts, err := h.repo.ListForwardPortsByNode(nodeID)
	if err != nil {
		return err
	}
	for _, port := range orphanBlockedPorts(blocked, ownedPorts) {
		if err := h.nftablesManager.SetBlocked(ctx, nodeID, sshCfg, port, false); err != nil {
			return err
		}
		log.Printf("nftables blocked-port sweep removed orphan node_id=%d port=%d", nodeID, port)
	}
	return nil
}
