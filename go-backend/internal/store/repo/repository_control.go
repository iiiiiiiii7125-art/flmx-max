package repo

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"gorm.io/gorm"

	"go-backend/internal/store/model"
)

// userTunnelGrantExistsTx 是 (user,tunnel) 授权行存在性的唯一查询
// 口径：仅 status=1 的行构成授权（停用授权不算，R4B02-001 与执行
// 点同义）。运行时与导入收口共用，不许再各写一份计数。
func userTunnelGrantExistsTx(db *gorm.DB, userID, tunnelID int64) (bool, error) {
	var count int64
	err := db.Model(&model.UserTunnel{}).
		Where("user_id = ? AND tunnel_id = ? AND status = 1", userID, tunnelID).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *Repository) UserTunnelExistsByUserAndTunnel(userID, tunnelID int64) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("repository not initialized")
	}
	return userTunnelGrantExistsTx(r.db, userID, tunnelID)
}

// tunnelUseAuthorizedTx 是转发隧道授权判定的唯一实现：运行时
// ensureTunnelPermission 与备份导入尾 sweepImportedAuthorizationTx
// 共用同一判定来源，不许再各写一份。roleID==0（管理员）恒授权——
// 管理员自有转发从不落 user_tunnel 授权行、运行时合法存在，恢复
// 语义必须同源承认，否则整库恢复会把管理员中转线整批删除
// （R3-D-01）；其余角色须有 status=1 的 (user,tunnel) 授权行。
func tunnelUseAuthorizedTx(db *gorm.DB, roleID int, userID, tunnelID int64) (bool, error) {
	if roleID == 0 {
		return true, nil
	}
	return userTunnelGrantExistsTx(db, userID, tunnelID)
}

// UserTunnelUseAuthorized 是运行时授权判定的仓库入口，与导入
// sweep 的 tunnelUseAuthorizedTx 同一实现（判定同源，R3-D-01）。
func (r *Repository) UserTunnelUseAuthorized(userID int64, roleID int, tunnelID int64) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("repository not initialized")
	}
	return tunnelUseAuthorizedTx(r.db, roleID, userID, tunnelID)
}

func (r *Repository) ListForwardsByTunnel(tunnelID int64) ([]model.ForwardRecord, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	return r.ListForwardsByTunnelTx(r.db, tunnelID)
}

func (r *Repository) ListForwardsByTunnelTx(tx *gorm.DB, tunnelID int64) ([]model.ForwardRecord, error) {
	if tx == nil {
		return nil, errors.New("database unavailable")
	}
	var forwards []model.Forward
	err := tx.Where("tunnel_id = ?", tunnelID).Order("id ASC").Find(&forwards).Error
	if err != nil {
		return nil, err
	}
	rows := make([]model.ForwardRecord, 0, len(forwards))
	for _, f := range forwards {
		proxyProtocolReceive, proxyProtocolSend := normalizeForwardProxyProtocol(f.ProxyProtocol, f.ProxyProtocolReceive, f.ProxyProtocolSend)
		rows = append(rows, model.ForwardRecord{
			ID:                   f.ID,
			UserID:               f.UserID,
			UserName:             f.UserName,
			Name:                 f.Name,
			TunnelID:             f.TunnelID,
			RemoteAddr:           f.RemoteAddr,
			Strategy:             f.Strategy,
			Status:               f.Status,
			SpeedID:              f.SpeedID,
			MaxConn:              f.MaxConn,
			IPMaxConn:            f.IPMaxConn,
			IPSpeedID:            f.IPSpeedID,
			ProxyProtocol:        f.ProxyProtocol,
			ProxyProtocolReceive: proxyProtocolReceive,
			ProxyProtocolSend:    proxyProtocolSend,
		})
	}
	for i := range rows {
		if strings.TrimSpace(rows[i].Strategy) == "" {
			rows[i].Strategy = "fifo"
		}
	}
	return rows, nil
}

func (r *Repository) ListActiveTunnelIDsByNode(nodeID int64) ([]int64, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var ids []int64
	err := r.db.Model(&model.ChainTunnel{}).
		Joins("JOIN tunnel ON tunnel.id = chain_tunnel.tunnel_id").
		Where("chain_tunnel.node_id = ? AND tunnel.status = 1", nodeID).
		Select("DISTINCT chain_tunnel.tunnel_id").
		Order("chain_tunnel.tunnel_id ASC").
		Pluck("chain_tunnel.tunnel_id", &ids).Error
	if err != nil {
		return nil, err
	}
	return ids, nil
}

func (r *Repository) ListActiveForwardIDsByNode(nodeID int64) ([]int64, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var ids []int64
	err := r.db.Model(&model.ForwardPort{}).
		Joins("JOIN forward ON forward.id = forward_port.forward_id").
		Where("forward_port.node_id = ? AND forward.status = 1", nodeID).
		Select("DISTINCT forward_port.forward_id").
		Order("forward_port.forward_id ASC").
		Pluck("forward_port.forward_id", &ids).Error
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// ListForwardNodeIDs returns every node that serves at least one
// forward port (R13X-001): the affected-node set for the post-import
// forward repush. A node with no forward ports has no gost/nft
// forward state to repush, so it is excluded by construction.
func (r *Repository) ListForwardNodeIDs() ([]int64, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var ids []int64
	err := r.db.Model(&model.ForwardPort{}).
		Select("DISTINCT node_id").
		Order("node_id ASC").
		Pluck("node_id", &ids).Error
	if err != nil {
		return nil, err
	}
	return ids, nil
}

func (r *Repository) ListForwardIDsByNode(nodeID int64) ([]int64, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var ids []int64
	err := r.db.Model(&model.ForwardPort{}).
		Where("forward_port.node_id = ?", nodeID).
		Select("DISTINCT forward_port.forward_id").
		Order("forward_port.forward_id ASC").
		Pluck("forward_port.forward_id", &ids).Error
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// ListForwardIDs 返回全部现存转发的 ID 全集（任意状态，含暂停：
// 暂停转发仍持有 forward_port 行）。连接日志入口集以此为
// “全部 forward”面：用户自建 forward 无托管中转行，仅凭此面
// 才进采集（D-P3-1）。
func (r *Repository) ListForwardIDs() ([]int64, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var ids []int64
	err := r.db.Model(&model.ForwardPort{}).
		Select("DISTINCT forward_id").
		Order("forward_id ASC").
		Pluck("forward_id", &ids).Error
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// ListForwardPortsByNode 返回节点上全部现存转发占用的入口端口
// （任意状态，含暂停：暂停转发仍持有 forward_port 行）。R2-038
// 封禁集对账清扫以此为「有主端口」全集。
func (r *Repository) ListForwardPortsByNode(nodeID int64) ([]int, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var ports []int
	err := r.db.Model(&model.ForwardPort{}).
		Where("forward_port.node_id = ?", nodeID).
		Select("DISTINCT forward_port.port").
		Order("forward_port.port ASC").
		Pluck("forward_port.port", &ports).Error
	if err != nil {
		return nil, err
	}
	if ports == nil {
		ports = make([]int, 0)
	}
	return ports, nil
}

func (r *Repository) ListForwardPorts(forwardID int64) ([]model.ForwardPortRecord, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	return r.ListForwardPortsTx(r.db, forwardID)
}

func (r *Repository) ListForwardPortsTx(tx *gorm.DB, forwardID int64) ([]model.ForwardPortRecord, error) {
	if tx == nil {
		return nil, errors.New("database unavailable")
	}
	var ports []model.ForwardPort
	err := tx.Where("forward_id = ?", forwardID).Order("id ASC").Find(&ports).Error
	if err != nil {
		return nil, err
	}
	rows := make([]model.ForwardPortRecord, 0, len(ports))
	for _, p := range ports {
		inIP := ""
		if p.InIP.Valid {
			inIP = p.InIP.String
		}
		rows = append(rows, model.ForwardPortRecord{NodeID: p.NodeID, Port: p.Port, InIP: inIP})
	}
	return rows, nil
}

func (r *Repository) HasOtherForwardOnNodePort(nodeID int64, port int, currentForwardID int64) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("repository not initialized")
	}
	return r.HasOtherForwardOnNodePortTx(r.db, nodeID, port, currentForwardID)
}

func (r *Repository) HasOtherForwardOnNodePortTx(tx *gorm.DB, nodeID int64, port int, currentForwardID int64) (bool, error) {
	// P01-001：三源占用知识收敛到统一谓词 NodePortOccupiedTx
	// （repository_port_occupancy.go）。本函数只做 forward 视角的
	// 排除适配：排除自己这条转发，其余三源（入站/链路/联邦有效
	// 预留）全部计入——O2-003 与 X-P2-13 的纳入结论由统一谓词承载。
	var excl PortExclusions
	if currentForwardID != 0 {
		excl.ForwardIDs = []int64{currentForwardID}
	}
	return r.NodePortOccupiedTx(tx, nodeID, port, excl)
}

// NodePortOccupiedForTunnelTx reports whether (nodeID, port) is occupied
// for tunnel-port purposes. B4/F-015: prepareTunnelCreateState validates
// explicit exit/chain ports with it — HasOtherForwardOnNodePortTx cannot
// be reused because its chain branch does not exclude the tunnel being
// updated (keeping the same port during an update would false-positive).
// P01-001: the source set is the unified four-source predicate
// (NodePortOccupiedTx) — this wrapper only adapts the exclusion to
// "chain rows of a DIFFERENT tunnel" (excludeTunnelID is the tunnel
// being created/updated; 0 excludes nothing).
func (r *Repository) NodePortOccupiedForTunnelTx(tx *gorm.DB, nodeID int64, port int, excludeTunnelID int64) (bool, error) {
	var excl PortExclusions
	if excludeTunnelID > 0 {
		excl.TunnelIDs = []int64{excludeTunnelID}
	}
	return r.NodePortOccupiedTx(tx, nodeID, port, excl)
}

func (r *Repository) GetTunnelOutProtocol(tunnelID int64) (string, error) {
	if r == nil || r.db == nil {
		return "", errors.New("repository not initialized")
	}
	var ct model.ChainTunnel
	err := r.db.Select("protocol").
		Where("tunnel_id = ? AND chain_type = ?", tunnelID, "3").
		Order("id ASC").
		Take(&ct).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", nil
		}
		return "", err
	}
	if ct.Protocol.Valid {
		return ct.Protocol.String, nil
	}
	return "", nil
}

func (r *Repository) GetNodeRecord(nodeID int64) (*model.NodeRecord, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var n model.Node
	err := r.db.Where("id = ?", nodeID).First(&n).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return nodeRecordFromModel(&n), nil
}

func (r *Repository) GetNodeRecordTx(tx *gorm.DB, nodeID int64) (*model.NodeRecord, error) {
	if tx == nil {
		return nil, errors.New("database unavailable")
	}
	var n model.Node
	err := tx.Where("id = ?", nodeID).First(&n).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return nodeRecordFromModel(&n), nil
}

func nodeRecordFromModel(n *model.Node) *model.NodeRecord {
	if n == nil {
		return nil
	}
	rec := &model.NodeRecord{
		ID:            n.ID,
		Name:          n.Name,
		ServerIP:      n.ServerIP,
		Status:        n.Status,
		PortRange:     n.Port,
		TCPListenAddr: n.TCPListenAddr, UDPListenAddr: n.UDPListenAddr,
		ForwardMode:                defaultNodeForwardMode(n.ForwardMode),
		IdleTimeoutSeconds:         n.IdleTimeoutSeconds,
		EdgeIdleTimeoutSeconds:     n.EdgeIdleTimeoutSeconds,
		BackstopIdleTimeoutSeconds: n.BackstopIdleTimeoutSeconds,
		BackstopTrickleBps:         n.BackstopTrickleBps,
		ConnLogEnabled:             n.ConnLogEnabled,
		WhitelistEnabled:           n.WhitelistEnabled,
		WhitelistExtra:             n.WhitelistExtra,
	}
	if n.ServerIPV4.Valid {
		rec.ServerIPv4 = strings.TrimSpace(n.ServerIPV4.String)
	}
	if n.ServerIPV6.Valid {
		rec.ServerIPv6 = strings.TrimSpace(n.ServerIPV6.String)
	}
	if n.ExtraIPs.Valid {
		rec.ExtraIPs = strings.TrimSpace(n.ExtraIPs.String)
	}
	if n.InterfaceName.Valid {
		rec.InterfaceName = strings.TrimSpace(n.InterfaceName.String)
	}
	if rec.TCPListenAddr == "" {
		rec.TCPListenAddr = "[::]"
	}
	if rec.UDPListenAddr == "" {
		rec.UDPListenAddr = "[::]"
	}
	if strings.TrimSpace(rec.Name) == "" {
		rec.Name = fmt.Sprintf("node_%d", rec.ID)
	}
	return rec
}

func (r *Repository) ResolveUserTunnelAndLimiter(userID, tunnelID int64) (*model.UserTunnelLimiterInfo, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	type row struct {
		UserTunnelID int64         `gorm:"column:user_tunnel_id"`
		LimiterID    sql.NullInt64 `gorm:"column:limiter_id"`
		Speed        sql.NullInt64 `gorm:"column:speed"`
	}
	var rec row
	err := r.db.Model(&model.UserTunnel{}).
		Select("user_tunnel.id AS user_tunnel_id, speed_limit.id AS limiter_id, speed_limit.speed AS speed").
		Joins("LEFT JOIN speed_limit ON speed_limit.id = user_tunnel.speed_id AND speed_limit.status = 1 AND speed_limit.speed > 0").
		Where("user_tunnel.user_id = ? AND user_tunnel.tunnel_id = ?", userID, tunnelID).
		Order("user_tunnel.id ASC").
		Limit(1).
		Take(&rec).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &model.UserTunnelLimiterInfo{}, nil
		}
		return nil, err
	}
	info := &model.UserTunnelLimiterInfo{UserTunnelID: rec.UserTunnelID}
	if rec.LimiterID.Valid && rec.LimiterID.Int64 > 0 {
		v := rec.LimiterID.Int64
		info.LimiterID = &v
		s := int(rec.Speed.Int64)
		info.Speed = &s
	}
	return info, nil
}

func (r *Repository) ListUserTunnelIDs(userID, tunnelID int64) ([]int64, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var ids []int64
	err := r.db.Model(&model.UserTunnel{}).
		Where("user_id = ? AND tunnel_id = ?", userID, tunnelID).
		Order("id ASC").Pluck("id", &ids).Error
	if err != nil {
		return nil, err
	}
	return ids, nil
}

func (r *Repository) ListUserTunnelIDsByUser(userID int64) ([]int64, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var ids []int64
	err := r.db.Model(&model.UserTunnel{}).
		Where("user_id = ?", userID).
		Order("id ASC").Pluck("id", &ids).Error
	if err != nil {
		return nil, err
	}
	return ids, nil
}

func (r *Repository) GetTunnelName(tunnelID int64) (string, error) {
	if r == nil || r.db == nil {
		return "", errors.New("repository not initialized")
	}
	var name string
	err := r.db.Model(&model.Tunnel{}).Where("id = ?", tunnelID).Pluck("name", &name).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", nil
		}
		return "", err
	}
	return name, nil
}

// GetTunnelNameTx is GetTunnelName inside a caller transaction. The
// R5-FGRP scope gate calls it on the batch-assignment path, where the
// pool handle would deadlock against the open transaction (SQLite
// MaxOpenConns(1)).
func (r *Repository) GetTunnelNameTx(tx *gorm.DB, tunnelID int64) (string, error) {
	if tx == nil {
		return r.GetTunnelName(tunnelID)
	}
	var name string
	err := tx.Model(&model.Tunnel{}).Where("id = ?", tunnelID).Pluck("name", &name).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", nil
		}
		return "", err
	}
	return name, nil
}

func (r *Repository) ListChainNodesForTunnel(tunnelID int64) ([]model.ChainNodeRecord, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	return r.ListChainNodesForTunnelTx(r.db, tunnelID)
}

// ListChainNodesForTunnelTx is the transactional form of
// ListChainNodesForTunnel. P01-002: prepareTunnelCreateState reads the
// tunnel's existing chain rows inside the update transaction (the pool
// form would take a second connection and deadlock SQLite's single
// connection — the Round 1 F1 lesson).
func (r *Repository) ListChainNodesForTunnelTx(tx *gorm.DB, tunnelID int64) ([]model.ChainNodeRecord, error) {
	if tx == nil {
		return nil, errors.New("database unavailable")
	}
	type row struct {
		ChainType string
		Inx       sql.NullInt64
		NodeID    int64
		Port      sql.NullInt64
		Name      sql.NullString
		Protocol  sql.NullString
		Strategy  sql.NullString
		ConnectIP sql.NullString
	}
	var rows []row
	err := tx.Model(&model.ChainTunnel{}).
		Select("chain_tunnel.chain_type, chain_tunnel.inx, chain_tunnel.node_id, chain_tunnel.port, node.name, chain_tunnel.protocol, chain_tunnel.strategy, chain_tunnel.connect_ip").
		Joins("LEFT JOIN node ON node.id = chain_tunnel.node_id").
		Where("chain_tunnel.tunnel_id = ?", tunnelID).
		Order("chain_tunnel.chain_type ASC, chain_tunnel.inx ASC, chain_tunnel.id ASC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	result := make([]model.ChainNodeRecord, 0, len(rows))
	for _, row := range rows {
		chainType := 0
		if v := strings.TrimSpace(row.ChainType); v != "" {
			if parsed, parseErr := strconv.Atoi(v); parseErr == nil {
				chainType = parsed
			}
		}
		inx := int64(0)
		if row.Inx.Valid {
			inx = row.Inx.Int64
		}
		port := 0
		if row.Port.Valid {
			port = int(row.Port.Int64)
		}
		item := model.ChainNodeRecord{
			ChainType: chainType,
			Inx:       inx,
			NodeID:    row.NodeID,
			Port:      port,
		}
		if strings.TrimSpace(row.Name.String) == "" {
			item.NodeName = fmt.Sprintf("node_%d", row.NodeID)
		} else {
			item.NodeName = row.Name.String
		}
		if strings.TrimSpace(row.Protocol.String) == "" {
			item.Protocol = "tls"
		} else {
			item.Protocol = row.Protocol.String
		}
		if strings.TrimSpace(row.Strategy.String) == "" {
			item.Strategy = "round"
		} else {
			item.Strategy = row.Strategy.String
		}
		if row.ConnectIP.Valid {
			item.ConnectIP = row.ConnectIP.String
		}
		result = append(result, item)
	}
	return result, nil
}
