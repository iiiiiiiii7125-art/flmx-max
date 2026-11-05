package repo

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"

	"gorm.io/gorm"
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
//
// B4-02 残面分工（本预检的定位）：预检排除集按备份 ID 全集组装，
// 天然无法区分「将被替换的行」与「被行门跳过后存量存活的行」，
// 故预检只是 fail-fast 粗筛（备份内自撞 + 明显占用先行中止）；
// 与顺序无关的最终收口在事务内三面行内复检——转发面
// （importForwards）、链路面（importTunnels）、入站面
// （importInbounds）落库前各自以四源谓词复检，排除集来自
// predictImportedForwardPortReplacements 的全集预判。预检放行
// 不再等价于落库无冲突，冲突在行内复检处逐行跳过 + 点名。
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

// importedTunnelRowPassesGates 是 importTunnels 前置门循环的纯函数
// 镜像（flow/ratio/probe/名长/type/status——均为备份行纯判，无 DB
// 依赖），供下方预判在 tunnels 段执行前先行判定「该备份隧道是否
// 会落库」。与 importTunnels 的门集是同一集合的两处实现（包内
// 镜像先例同 validateImportedTunnelFlow 的注记）：任一侧增删门，
// 两侧必须同步，否则预判与实际落库分家、B4-02 的顺序无关性失效。
func importedTunnelRowPassesGates(t model.TunnelBackup) bool {
	if validateImportedTunnelFlow(t.Flow) != nil {
		return false
	}
	if validateImportedTunnelTrafficRatio(t.TrafficRatio) != nil {
		return false
	}
	if host := strings.TrimSpace(t.ProbeTargetHost); host != "" && kernel.HasEmptyLabel(host) {
		return false
	}
	if t.ProbeTargetPort < 0 || t.ProbeTargetPort > 65535 ||
		(t.ProbeTargetPort == 0 && strings.TrimSpace(t.ProbeTargetHost) != "") {
		return false
	}
	if len([]rune(t.Name)) > 100 {
		return false
	}
	if validateImportedTunnelType(t.Type) != nil {
		return false
	}
	if validateImportedTunnelStatus(t.Status) != nil {
		return false
	}
	return true
}

// predictImportedForwardPortReplacements 在导入事务内、tunnels 段
// 之前对全部备份转发做一次全集预判：哪些转发会通过 importForwards
// 的全部行门落库、且 ForwardPorts != nil（即其存量 forward_port
// 行将被整表替换）。返回集合即三面行内复检（转发/链路/入站）的
// 转发排除集来源。
//
// 为什么需要预判（B4-02 残面）：R1-03-M2 的行内复检排除集按
// 「尚未处理的备份转发」逐行收缩——被跳转发的存量端口行只有在
// 声明行排在其后时才计占用，声明行排在前面时仍被排除，可与存量
// 行同节点同口双落；链路/入站声明面更没有行内复检。成员资格改由
// 本预判按行门全集先行算定（被门跳过的转发、落库但不替换端口行
// 的转发一律不入集，其存量行在任何声明顺序下都计占用），复检
// 结果与备份内声明顺序无关。
//
// 预判的门集与 importForwards 行门逐项同集（status 集/空标签/零
// 目标/名长/段数/端口格式/用户与隧道存在性/strategy 合法集/realm
// 策略）——同 importedTunnelRowPassesGates 的镜像纪律，两侧必须
// 同步。时点差异只有隧道存在性：tunnels 段尚未执行，「将存在」
// 按 DB 现存 ∪ 备份内过纯门的隧道行判定，引擎同理取备份行归一
// 值（DB 隧道取现存 engine；备份隧道若 engine 归一失败，
// importTunnels 会整单中止，预判结果不会被消费）。用户存在性已
// 是终态（users 段在本预判之前执行完毕）。
func predictImportedForwardPortReplacements(tx *gorm.DB, backup *model.BackupData, typeSet map[string]bool) (map[int64]bool, error) {
	out := make(map[int64]bool)
	backupTunnels := make(map[int64]model.TunnelBackup)
	if typeSet["tunnels"] {
		for _, t := range backup.Tunnels {
			if importedTunnelRowPassesGates(t) {
				backupTunnels[t.ID] = t
			}
		}
	}
	tunnelGate := func(id int64) (exists bool, engine string, err error) {
		if bt, ok := backupTunnels[id]; ok {
			engine, nerr := normalizeBackupTunnelEngine(bt.Engine)
			if nerr != nil {
				return true, "", nil
			}
			return true, engine, nil
		}
		var tun model.Tunnel
		err = tx.Select("engine").Where("id = ?", id).First(&tun).Error
		if err == nil {
			return true, tun.Engine, nil
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, "", nil
		}
		return false, "", err
	}
	for _, f := range backup.Forwards {
		if f.ForwardPorts == nil {
			continue
		}
		switch f.Status {
		case model.ForwardStatusPaused, model.ForwardStatusActive, model.ForwardStatusPendingDelete:
		default:
			continue
		}
		if addrsHaveEmptyLabel(f.RemoteAddr) || remoteAddrHasNoTargets(f.RemoteAddr) {
			continue
		}
		if len([]rune(f.Name)) > 100 {
			continue
		}
		if importRemoteAddrSegmentCount(f.RemoteAddr) > maxImportRemoteAddrSegments {
			continue
		}
		if err := validateImportRemoteAddrPorts(f.RemoteAddr); err != nil {
			continue
		}
		userExists, err := rowExistsTx(tx, &model.User{}, f.UserID)
		if err != nil {
			return nil, err
		}
		if !userExists {
			continue
		}
		exists, engine, err := tunnelGate(f.TunnelID)
		if err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		strategy := strings.TrimSpace(f.Strategy)
		if strategy == "" {
			strategy = "fifo"
		}
		if !kernel.ForwardStrategyLegal(strategy) {
			continue
		}
		if err := validateImportedRealmForwardStrategy(engine, f.RemoteAddr, strategy); err != nil {
			continue
		}
		out[f.ID] = true
	}
	return out, nil
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
	// 排除集为整单规模（无上界）：消费侧 NodePortOccupantTx 对大
	// 集合走候选反连接判定，每条 claim 的 SQL 变量数恒定，不整体
	// 回灌 NOT IN（K8/X01-001，OPT-01 随之消除）。
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
