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
//   - 占用全景 = 当前 DB 三表（forward_port / chain_tunnel /
//     node_inbound），排除将被本次恢复按 ID 替换的行；
//   - 备份内的声明逐条与全景及彼此核对，冲突即以「节点+端口+占用
//     方」报错中止，什么都不写。
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
				if ct.NodeID > 0 && ct.Port > 0 {
					claims = append(claims, backupPortClaim{ct.NodeID, ct.Port, fmt.Sprintf("隧道「%s」的链路", t.Name)})
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

func (r *Repository) precheckBackupPortConflicts(backup *model.BackupData, typeSet map[string]bool, claims []backupPortClaim) error {
	if len(claims) == 0 {
		return nil
	}
	// 将被本次恢复按 ID 替换的行不算冲突（重复恢复同一备份须幂等）。
	replacedForwards := make(map[int64]bool)
	if typeSet["forwards"] {
		for _, f := range backup.Forwards {
			replacedForwards[f.ID] = true
		}
	}
	replacedChains := make(map[int64]bool)
	if typeSet["tunnels"] {
		for _, t := range backup.Tunnels {
			for _, ct := range t.ChainTunnels {
				replacedChains[ct.ID] = true
			}
		}
	}
	replacedInbounds := make(map[int64]bool)
	if typeSet["inbounds"] && backup.Inbounds != nil {
		for _, ib := range backup.Inbounds.Inbounds {
			replacedInbounds[ib.ID] = true
		}
	}

	type portKey struct {
		nodeID int64
		port   int
	}
	occupied := make(map[portKey]string)
	var fpRows []struct {
		ForwardID int64
		NodeID    int64
		Port      int
	}
	if err := r.db.Model(&model.ForwardPort{}).Select("forward_id, node_id, port").
		Where("node_id > 0 AND port > 0").Scan(&fpRows).Error; err != nil {
		return err
	}
	for _, row := range fpRows {
		if replacedForwards[row.ForwardID] {
			continue
		}
		occupied[portKey{row.NodeID, row.Port}] = "已有转发"
	}
	var ctRows []struct {
		ID     int64
		NodeID int64
		Port   int64
	}
	if err := r.db.Model(&model.ChainTunnel{}).Select("id, node_id, port").
		Where("node_id > 0 AND port > 0").Scan(&ctRows).Error; err != nil {
		return err
	}
	for _, row := range ctRows {
		if replacedChains[row.ID] {
			continue
		}
		occupied[portKey{row.NodeID, int(row.Port)}] = "已有隧道链路"
	}
	var ibRows []struct {
		ID     int64
		NodeID int64
		Port   int
	}
	if err := r.db.Model(&model.NodeInbound{}).Select("id, node_id, port").
		Where("node_id > 0 AND port > 0").Scan(&ibRows).Error; err != nil {
		return err
	}
	for _, row := range ibRows {
		if replacedInbounds[row.ID] {
			continue
		}
		occupied[portKey{row.NodeID, row.Port}] = "已有入站"
	}

	for _, claim := range claims {
		key := portKey{claim.nodeID, claim.port}
		if owner, ok := occupied[key]; ok {
			return fmt.Errorf("备份恢复端口冲突：节点 %d 端口 %d 已被%s占用（备份中的%s需要该端口），已中止恢复，未写入任何数据",
				claim.nodeID, claim.port, owner, claim.owner)
		}
		occupied[key] = "备份中的" + claim.owner
	}
	return nil
}
