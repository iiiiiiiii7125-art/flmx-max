package repo

import (
	"fmt"
	"sort"
	"sync"

	"go-backend/internal/store/model"
)

// B4/F-055: 备份恢复此前无任何端口预检/锁，与正常创建入口完全脱节：
// 跨表同口（入站 vs 转发 vs 链路）在无唯一索引兜底时静默恢复出坏
// 配置，有索引时整单事务裸冲突失败且报错不指名。现在 Import 在写
// 入前先做预检，并在预检+事务全程持有相关节点的 per-node 端口锁：
//   - 占用判重走统一四源谓词 NodePortOccupantTx（P01-001 收敛；
//     此前本文件自拼三表全景，漏联邦有效预留——恢复落口撞现行预留
//     会放行、落库后跨表重复），排除将被本次恢复按 ID 替换的行；
//   - 备份内的声明逐条与 DB 占用及彼此核对，冲突即以「节点+端口+
//     占用方」报错中止，什么都不写。
// external 入站（node_id=0）的端口在远端主机上，不参与本地节点
// 端口空间，跳过。

type backupPortClaim struct {
	nodeID int64
	port   int
	owner  string
}

func backupClaims(backup *model.BackupData, typeSet map[string]bool) []backupPortClaim {
	claims := make([]backupPortClaim, 0)
	if typeSet["forwards"] {
		for _, f := range backup.Forwards {
			if f.ForwardPorts == nil {
				continue
			}
			for _, fp := range *f.ForwardPorts {
				if fp.NodeID > 0 && fp.Port > 0 {
					claims = append(claims, backupPortClaim{fp.NodeID, fp.Port, fmt.Sprintf("转发「%s」", f.Name)})
				}
			}
		}
	}
	if typeSet["tunnels"] {
		for _, t := range backup.Tunnels {
			for _, ct := range t.ChainTunnels {
				if ct.NodeID > 0 && ct.Port != nil && *ct.Port > 0 {
					claims = append(claims, backupPortClaim{ct.NodeID, *ct.Port, fmt.Sprintf("隧道「%s」的链路", t.Name)})
				}
			}
		}
	}
	if typeSet["inbounds"] && backup.Inbounds != nil {
		for _, ib := range backup.Inbounds.Inbounds {
			if ib.NodeID > 0 && ib.Port > 0 {
				claims = append(claims, backupPortClaim{ib.NodeID, ib.Port, fmt.Sprintf("入站「%s」", ib.Name)})
			}
		}
	}
	return claims
}

// lockBackupPortNodes takes the per-node port-allocation locks for
// every node a backup restore will claim ports on, in ascending order.
func lockBackupPortNodes(claims []backupPortClaim) func() {
	nodeSet := make(map[int64]bool)
	for _, c := range claims {
		nodeSet[c.nodeID] = true
	}
	ids := make([]int64, 0, len(nodeSet))
	for id := range nodeSet {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	locks := make([]*sync.Mutex, 0, len(ids))
	for _, id := range ids {
		locks = append(locks, AcquirePortAllocLock(id))
	}
	return func() {
		for i := len(locks) - 1; i >= 0; i-- {
			ReleasePortAllocLock(locks[i])
		}
	}
}

// backupPortSourceLabel 把统一谓词的来源标识译成预检报错里的占用方
// 文案（与收敛前的三表全景文案保持一致，联邦预留为新增第四源）。
func backupPortSourceLabel(source string) string {
	switch source {
	case PortSourceForward:
		return "已有转发"
	case PortSourceChain:
		return "已有隧道链路"
	case PortSourceInbound:
		return "已有入站"
	case PortSourcePeer:
		return "联邦分享预留"
	}
	return "其他服务"
}

func (r *Repository) precheckBackupPortConflicts(backup *model.BackupData, typeSet map[string]bool, claims []backupPortClaim) error {
	if len(claims) == 0 {
		return nil
	}
	// 将被本次恢复按 ID 替换的行不算冲突（重复恢复同一备份须幂等）。
	// 排除集按恢复语义组装：转发按 forward_id、入站按入站 id、链路
	// 按所属隧道 id（统一谓词的链路排除粒度是隧道——同一
	// (node, port) 上属于该隧道的行只可能是将被替换的那一行，与
	// 此前按链路行 id 排除等价）。联邦有效预留不在备份内、无排除。
	var excl PortExclusions
	if typeSet["forwards"] {
		for _, f := range backup.Forwards {
			excl.ForwardIDs = append(excl.ForwardIDs, f.ID)
		}
	}
	if typeSet["tunnels"] {
		seen := make(map[int64]bool)
		for _, t := range backup.Tunnels {
			if !seen[t.ID] {
				seen[t.ID] = true
				excl.TunnelIDs = append(excl.TunnelIDs, t.ID)
			}
		}
	}
	if typeSet["inbounds"] && backup.Inbounds != nil {
		for _, ib := range backup.Inbounds.Inbounds {
			excl.InboundIDs = append(excl.InboundIDs, ib.ID)
		}
	}

	type portKey struct {
		nodeID int64
		port   int
	}
	claimed := make(map[portKey]string)
	for _, claim := range claims {
		key := portKey{claim.nodeID, claim.port}
		if owner, ok := claimed[key]; ok {
			return fmt.Errorf("备份恢复端口冲突：节点 %d 端口 %d 已被%s占用（备份中的%s需要该端口），已中止恢复，未写入任何数据",
				claim.nodeID, claim.port, owner, claim.owner)
		}
		source, err := r.NodePortOccupantTx(r.db, claim.nodeID, claim.port, excl)
		if err != nil {
			return err
		}
		if source != "" {
			return fmt.Errorf("备份恢复端口冲突：节点 %d 端口 %d 已被%s占用（备份中的%s需要该端口），已中止恢复，未写入任何数据",
				claim.nodeID, claim.port, backupPortSourceLabel(source), claim.owner)
		}
		claimed[key] = "备份中的" + claim.owner
	}
	return nil
}
