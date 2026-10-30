package repo

import (
	"errors"

	"go-backend/internal/store/model"

	"gorm.io/gorm"
)

// P01-001（Round 2 K9）：节点端口占用知识的唯一实现。
//
// 节点端口空间由四张表共享：forward_port（转发入口口）、node_inbound
// （内核入站口）、chain_tunnel（隧道链路段/出口段口，port>0 才算）、
// peer_share_runtime（联邦分享有效预留/运行时口，status=1 才算）。
// X-P2-13 当年只对齐了转发面与联邦面，隧道选口（pickNodePortTx）、
// 隧道显式口谓词（NodePortOccupiedForTunnelTx）、入站 occupant
// （NodePortRangeOccupant）、备份预检四面仍各写一份三源集合，漏掉
// peer_share_runtime：本地隧道/入站可与分享运行时同节点同口双分配，
// 后下发一方在节点上绑口失败，且按三表排查不到占用方。
//
// 现在全部选口/判重点位一律走本文件的入口：
//   - 点判重：NodePortOccupantTx / NodePortOccupiedTx
//   - 已用集：UsedPortsOnNodeTx
//   - 区间占用：NodePortRangeOccupancyTx
//
// 新增占用源只改这里。port_occupancy_sentinel_test.go 以枚举清单锁定
// 每个点位必须引用本文件函数，防同族再漏。

// 端口占用来源标识（与 FindOtherForwardOnNodePortTx 的
// ForwardPortConflict.Source 取值一致，勿改字符串）。
const (
	PortSourceForward = "forward"
	PortSourceInbound = "inbound"
	PortSourceChain   = "chain_tunnel"
	PortSourcePeer    = "peer_share_runtime"
)

// PortExclusions 描述判重时要排除的「自己人」行：转发按 forward_id、
// 链路按 tunnel_id、入站按入站 id 排除（可多值，备份预检按整单排除）。
// 联邦预留无排除面：备份不含运行时预留、本地任何分配都不得占用
// 有效预留口。
type PortExclusions struct {
	ForwardIDs []int64
	TunnelIDs  []int64
	InboundIDs []int64
}

// NodePortOccupantTx 返回占用 (nodeID, port) 的来源（"" 表示空闲）。
// 检查顺序 forward → inbound → chain → peer，与
// FindOtherForwardOnNodePortTx 的报告优先级一致；多源同占（降级态）
// 时按此序报第一源。
func (r *Repository) NodePortOccupantTx(tx *gorm.DB, nodeID int64, port int, excl PortExclusions) (string, error) {
	if tx == nil {
		return "", errors.New("database unavailable")
	}
	if nodeID <= 0 || port <= 0 {
		return "", nil
	}
	var count int64

	forwardQuery := tx.Model(&model.ForwardPort{}).Where("node_id = ? AND port = ?", nodeID, port)
	if len(excl.ForwardIDs) > 0 {
		forwardQuery = forwardQuery.Where("forward_id NOT IN ?", excl.ForwardIDs)
	}
	if err := forwardQuery.Count(&count).Error; err != nil {
		return "", err
	}
	if count > 0 {
		return PortSourceForward, nil
	}

	inboundQuery := tx.Model(&model.NodeInbound{}).Where("node_id = ? AND port = ?", nodeID, port)
	if len(excl.InboundIDs) > 0 {
		inboundQuery = inboundQuery.Where("id NOT IN ?", excl.InboundIDs)
	}
	if err := inboundQuery.Count(&count).Error; err != nil {
		return "", err
	}
	if count > 0 {
		return PortSourceInbound, nil
	}

	chainQuery := tx.Model(&model.ChainTunnel{}).Where("node_id = ? AND port = ? AND port > 0", nodeID, port)
	if len(excl.TunnelIDs) > 0 {
		chainQuery = chainQuery.Where("tunnel_id NOT IN ?", excl.TunnelIDs)
	}
	if err := chainQuery.Count(&count).Error; err != nil {
		return "", err
	}
	if count > 0 {
		return PortSourceChain, nil
	}

	if err := tx.Model(&model.PeerShareRuntime{}).
		Where("node_id = ? AND port = ? AND port > 0 AND status = 1", nodeID, port).
		Count(&count).Error; err != nil {
		return "", err
	}
	if count > 0 {
		return PortSourcePeer, nil
	}
	return "", nil
}

// NodePortOccupiedTx 是 NodePortOccupantTx 的布尔形。
func (r *Repository) NodePortOccupiedTx(tx *gorm.DB, nodeID int64, port int, excl PortExclusions) (bool, error) {
	source, err := r.NodePortOccupantTx(tx, nodeID, port, excl)
	if err != nil {
		return false, err
	}
	return source != "", nil
}

// UsedPortsOnNodeTx 返回节点上全部已占用端口的集合（四源，含排除）。
// 全部自动选口（隧道 pick、转发 pick、联邦 pick）都以本集合为判重
// 依据，不再各自拼装。
func (r *Repository) UsedPortsOnNodeTx(tx *gorm.DB, nodeID int64, excl PortExclusions) (map[int]struct{}, error) {
	if tx == nil {
		return nil, errors.New("database unavailable")
	}
	used := make(map[int]struct{})
	add := func(ports []int) {
		for _, p := range ports {
			if p > 0 {
				used[p] = struct{}{}
			}
		}
	}

	forwardQuery := tx.Model(&model.ForwardPort{}).Where("node_id = ? AND port > 0", nodeID)
	if len(excl.ForwardIDs) > 0 {
		forwardQuery = forwardQuery.Where("forward_id NOT IN ?", excl.ForwardIDs)
	}
	var forwardPorts []int
	if err := forwardQuery.Pluck("port", &forwardPorts).Error; err != nil {
		return nil, err
	}
	add(forwardPorts)

	inboundPorts, err := listNodeInboundPortsExclTx(tx, nodeID, excl.InboundIDs)
	if err != nil {
		return nil, err
	}
	add(inboundPorts)

	chainQuery := tx.Model(&model.ChainTunnel{}).Where("node_id = ? AND port > 0", nodeID)
	if len(excl.TunnelIDs) > 0 {
		chainQuery = chainQuery.Where("tunnel_id NOT IN ?", excl.TunnelIDs)
	}
	var chainPorts []int
	if err := chainQuery.Pluck("port", &chainPorts).Error; err != nil {
		return nil, err
	}
	add(chainPorts)

	var peerPorts []int
	if err := tx.Model(&model.PeerShareRuntime{}).
		Where("node_id = ? AND port > 0 AND status = 1", nodeID).
		Pluck("port", &peerPorts).Error; err != nil {
		return nil, err
	}
	add(peerPorts)

	return used, nil
}

// listNodeInboundPortsExclTx 是已用集/判重共用的入站口清单（多值
// 排除形）；单值版 listNodeInboundPortsTx 在 repository_kernel.go
// 转调本函数。
func listNodeInboundPortsExclTx(tx *gorm.DB, nodeID int64, excludeInboundIDs []int64) ([]int, error) {
	var ports []int
	q := tx.Model(&model.NodeInbound{}).Where("node_id = ? AND port > 0", nodeID)
	if len(excludeInboundIDs) > 0 {
		q = q.Where("id NOT IN ?", excludeInboundIDs)
	}
	return ports, q.Pluck("port", &ports).Error
}

// PortRangeOccupancy 汇总 [lo, hi] 区间内的占用来源集合。
type PortRangeOccupancy struct {
	Sources map[string]bool
	// InboundCompanion 表示区间内存在订阅计量伴生入站
	// （companion_of > 0）——它不在任何用户列表里，occupant 命名
	// 须单独点名（F7/B-05 口径）。
	InboundCompanion bool
}

// NodePortRangeOccupancyTx 返回区间内各来源的占用情况（四源）。
func (r *Repository) NodePortRangeOccupancyTx(tx *gorm.DB, nodeID int64, lo, hi int, excl PortExclusions) (*PortRangeOccupancy, error) {
	if tx == nil {
		return nil, errors.New("database unavailable")
	}
	occ := &PortRangeOccupancy{Sources: map[string]bool{}}
	var count int64

	inboundQuery := tx.Model(&model.NodeInbound{}).Where("node_id = ? AND port BETWEEN ? AND ?", nodeID, lo, hi)
	if len(excl.InboundIDs) > 0 {
		inboundQuery = inboundQuery.Where("id NOT IN ?", excl.InboundIDs)
	}
	if err := inboundQuery.Count(&count).Error; err != nil {
		return nil, err
	}
	if count > 0 {
		occ.Sources[PortSourceInbound] = true
		// F7 (B-05)：伴生入站命名需要单独确认；原实现对该查询的
		// 错误是吞掉的（err == nil 才采信），保持同口径。
		companionQuery := tx.Model(&model.NodeInbound{}).Where("node_id = ? AND port BETWEEN ? AND ? AND companion_of > 0", nodeID, lo, hi)
		if len(excl.InboundIDs) > 0 {
			companionQuery = companionQuery.Where("id NOT IN ?", excl.InboundIDs)
		}
		var compCount int64
		if err := companionQuery.Count(&compCount).Error; err == nil && compCount > 0 {
			occ.InboundCompanion = true
		}
	}

	forwardQuery := tx.Model(&model.ForwardPort{}).Where("node_id = ? AND port BETWEEN ? AND ?", nodeID, lo, hi)
	if len(excl.ForwardIDs) > 0 {
		forwardQuery = forwardQuery.Where("forward_id NOT IN ?", excl.ForwardIDs)
	}
	if err := forwardQuery.Count(&count).Error; err != nil {
		return nil, err
	}
	if count > 0 {
		occ.Sources[PortSourceForward] = true
	}

	chainQuery := tx.Model(&model.ChainTunnel{}).Where("node_id = ? AND port BETWEEN ? AND ?", nodeID, lo, hi)
	if len(excl.TunnelIDs) > 0 {
		chainQuery = chainQuery.Where("tunnel_id NOT IN ?", excl.TunnelIDs)
	}
	if err := chainQuery.Count(&count).Error; err != nil {
		return nil, err
	}
	if count > 0 {
		occ.Sources[PortSourceChain] = true
	}

	if err := tx.Model(&model.PeerShareRuntime{}).
		Where("node_id = ? AND port BETWEEN ? AND ? AND port > 0 AND status = 1", nodeID, lo, hi).
		Count(&count).Error; err != nil {
		return nil, err
	}
	if count > 0 {
		occ.Sources[PortSourcePeer] = true
	}
	return occ, nil
}

// TunnelChainPortReusableTx 判断隧道现有非入口段口在更新时能否沿用
// （P01-002）：口仍落在节点端口规格内（与 pickNodePortTx 的候选集
// 同口径），且统一四源判重（排除本隧道自身链路行）下未被他人占用。
// 节点不存在按不可沿用处理（后续流程会以「节点不存在」报错）。
func (r *Repository) TunnelChainPortReusableTx(tx *gorm.DB, nodeID int64, port int, tunnelID int64) (bool, error) {
	if tx == nil {
		return false, errors.New("database unavailable")
	}
	if nodeID <= 0 || port <= 0 {
		return false, nil
	}
	var node model.Node
	err := tx.Select("port").Where("id = ?", nodeID).First(&node).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	inSpec := false
	for _, candidate := range parsePortRangeSpec(node.Port) {
		if candidate == port {
			inSpec = true
			break
		}
	}
	if !inSpec {
		return false, nil
	}
	var excl PortExclusions
	if tunnelID > 0 {
		excl.TunnelIDs = []int64{tunnelID}
	}
	occupied, err := r.NodePortOccupiedTx(tx, nodeID, port, excl)
	if err != nil {
		return false, err
	}
	return !occupied, nil
}
