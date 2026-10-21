package repo

import (
	"database/sql"
	"errors"
	"log"
	"net"
	"strconv"
	"strings"

	"go-backend/internal/store/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// InboundCounterSample is one cumulative nft counter reported by an agent.
type InboundCounterSample struct {
	InboundID int64
	Protocol  string
	Direction string // "in" (client upload) or "out" (client download)
	Epoch     string
	Bytes     int64
}

func (r *Repository) ListNodeKernels(nodeID int64) ([]model.NodeKernel, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var rows []model.NodeKernel
	q := r.db.Order("node_id ASC, id ASC")
	if nodeID > 0 {
		q = q.Where("node_id = ?", nodeID)
	}
	return rows, q.Find(&rows).Error
}

func (r *Repository) ListKernelNodeIDs() ([]int64, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var ids []int64
	err := r.db.Model(&model.NodeKernel{}).Distinct("node_id").Order("node_id ASC").Pluck("node_id", &ids).Error
	return ids, err
}

func (r *Repository) GetNodeKernel(id int64) (*model.NodeKernel, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var k model.NodeKernel
	err := r.db.Where("id = ?", id).First(&k).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &k, err
}

func (r *Repository) GetNodeKernelByType(nodeID int64, kernelType string) (*model.NodeKernel, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var k model.NodeKernel
	err := r.db.Where("node_id = ? AND kernel_type = ?", nodeID, kernelType).First(&k).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &k, err
}

func (r *Repository) CreateNodeKernel(k *model.NodeKernel) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Create(k).Error
}

// UpdateNodeKernelFields updates selected columns; zero values are written.
func (r *Repository) UpdateNodeKernelFields(id int64, fields map[string]interface{}) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Model(&model.NodeKernel{}).Where("id = ?", id).Updates(fields).Error
}

func (r *Repository) DeleteNodeKernel(id int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		var ids []int64
		if err := tx.Model(&model.NodeInbound{}).Where("kernel_id = ?", id).Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) > 0 {
			relayIDs, err := pluckInboundRelayIDsTx(tx, "inbound_id IN ?", ids)
			if err != nil {
				return err
			}
			if err := tx.Where("inbound_id IN ?", ids).Delete(&model.NodeInboundCounter{}).Error; err != nil {
				return err
			}
			if err := tx.Where("inbound_id IN ?", ids).Delete(&model.NodeInboundRelay{}).Error; err != nil {
				return err
			}
			// B4/F-010（变体）: 单删入站（DeleteNodeInbound）会清 user_inbound，
			// 删内核/删节点级联此前独漏，孤儿分配行可阻断后续订阅分配保存。
			if err := tx.Where("inbound_id IN ?", ids).Delete(&model.UserInbound{}).Error; err != nil {
				return err
			}
			// A3-02/X2-01 统一手法：指向被删 relay 行的授权按物化集合清。
			if err := deleteUserInboundGrantsByRelayIDsTx(tx, relayIDs); err != nil {
				return err
			}
		}
		if err := tx.Where("kernel_id = ?", id).Delete(&model.NodeInbound{}).Error; err != nil {
			return err
		}
		// AUDIT-R1 (TST-002): 删除 R5-B7 误加的行。UserInbound 模型没有 kernel_id 列
		//（仅 user_id/inbound_id/relay_id），该删除恒以 "no such column" 失败并回滚
		// 整个内核删除；NodeInbound 的 kernel_id 已由上一行清理，无悬空可清。
		return tx.Where("id = ?", id).Delete(&model.NodeKernel{}).Error
	})
}

// pluckInboundRelayIDsTx 在 relay 行被删除之前物化其 id 集合。
// user_inbound.relay_id 的键空间是 NodeInboundRelay.id，与 inbound id /
// forward id 是相互独立的自增序列：清理授权行必须用物化的 relay id
// 集合，严禁用其它实体的 id 集合代替（A3-02：旧 deleteNodeKernelsTx
// 用入站 id 集合套 relay_id，数值碰撞时静默误删其它节点中转的授权）。
func pluckInboundRelayIDsTx(tx *gorm.DB, where string, args ...interface{}) ([]int64, error) {
	var relayIDs []int64
	if err := tx.Model(&model.NodeInboundRelay{}).Where(where, args...).Pluck("id", &relayIDs).Error; err != nil {
		return nil, err
	}
	return relayIDs, nil
}

// deleteUserInboundGrantsByRelayIDsTx 按物化的 relay id 集合清理
// user_inbound 授权行（各删除级联统一手法，A3-02/X2-01）。
func deleteUserInboundGrantsByRelayIDsTx(tx *gorm.DB, relayIDs []int64) error {
	if len(relayIDs) == 0 {
		return nil
	}
	return tx.Where("relay_id IN ?", relayIDs).Delete(&model.UserInbound{}).Error
}

func deleteNodeKernelsTx(tx *gorm.DB, nodeID int64) error {
	var ids []int64
	if err := tx.Model(&model.NodeInbound{}).Where("node_id = ?", nodeID).Pluck("id", &ids).Error; err != nil {
		return err
	}
	if len(ids) > 0 {
		relayIDs, err := pluckInboundRelayIDsTx(tx, "inbound_id IN ?", ids)
		if err != nil {
			return err
		}
		if err := tx.Where("inbound_id IN ?", ids).Delete(&model.NodeInboundCounter{}).Error; err != nil {
			return err
		}
		if err := tx.Where("inbound_id IN ?", ids).Delete(&model.NodeInboundRelay{}).Error; err != nil {
			return err
		}
		// B4/F-010: 节点级联删入站时同步清 user_inbound（按 inbound_id），
		// 与 DeleteNodeInbound 的口径一致；指向被删 relay 行的分配行
		// （relay_id）按物化的 relay id 集合一并清理（A3-02 修正：
		// 旧实现误用入站 id 集合，会误删其它节点同号 relay 的授权）。
		if err := tx.Where("inbound_id IN ?", ids).Delete(&model.UserInbound{}).Error; err != nil {
			return err
		}
		if err := deleteUserInboundGrantsByRelayIDsTx(tx, relayIDs); err != nil {
			return err
		}
	}
	if err := tx.Where("node_id = ?", nodeID).Delete(&model.NodeInbound{}).Error; err != nil {
		return err
	}
	return tx.Where("node_id = ?", nodeID).Delete(&model.NodeKernel{}).Error
}

func (r *Repository) ListNodeInbounds() ([]model.NodeInbound, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var rows []model.NodeInbound
	return rows, r.db.Order("inx ASC, id ASC").Find(&rows).Error
}

// ListNodeInboundRelays returns all inbound-relay links.
func (r *Repository) ListNodeInboundRelays() ([]model.NodeInboundRelay, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var rows []model.NodeInboundRelay
	return rows, r.db.Order("id ASC").Find(&rows).Error
}

func (r *Repository) ListNodeInboundsByKernel(kernelID int64) ([]model.NodeInbound, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var rows []model.NodeInbound
	return rows, r.db.Where("kernel_id = ?", kernelID).Order("id ASC").Find(&rows).Error
}

func (r *Repository) GetNodeInbound(id int64) (*model.NodeInbound, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var in model.NodeInbound
	err := r.db.Where("id = ?", id).First(&in).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &in, err
}

func (r *Repository) CreateNodeInbound(in *model.NodeInbound) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	// AUDIT-R3 (F2X-001): per-node 端口分配锁已上移到调用方
	//（inboundSave/remote_source/kernel_external），覆盖"检查→插入"
	// 临界区。此处不再加锁（sync.Mutex 不可重入）。
	// BUG-A4-1: Enabled 带 gorm `default:1`，db.Create 会把显式 0 当未设置
	// 丢弃、由列默认值 1 接管并回填结构体——enabled:false 建出来的入站
	// 直接启用上线。故先留住调用方意图，创建后再写回。所有调用方都显式
	// 给 Enabled 赋值（inboundSave 默认 1、externalRow 恒 1），意图为 0
	// 必是显式禁用。改模型标签去掉 default 会触发存量库 AutoMigrate 的
	// 列变更（SQLite 需重建表），故在仓库层收口。
	wantEnabled := in.Enabled
	// FIX132-C（A3-06）：建行与 enabled 写回包进同一事务。旧实现
	// 两步非原子，写回失败时行已落库且 enabled=1（与调用方显式
	// 禁用意图相反）并已上线。
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(in).Error; err != nil {
			return err
		}
		if wantEnabled == 0 {
			if err := tx.Model(&model.NodeInbound{}).Where("id = ?", in.ID).Update("enabled", 0).Error; err != nil {
				return err
			}
			in.Enabled = 0
		}
		return nil
	})
}

// SaveNodeInbound writes the editable columns; traffic is left untouched so a
// concurrent collection is not overwritten.
func (r *Repository) SaveNodeInbound(in *model.NodeInbound) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Model(&model.NodeInbound{}).Where("id = ?", in.ID).Updates(map[string]interface{}{
		"name":                  in.Name,
		"protocol":              in.Protocol,
		"listen":                in.Listen,
		"port":                  in.Port,
		"transport":             in.Transport,
		"security":              in.Security,
		"settings":              in.Settings,
		"extra":                 in.Extra,
		"share_host":            in.ShareHost,
		"remark":                in.Remark,
		"relay_only":            in.RelayOnly,
		"accept_proxy_protocol": in.AcceptProxyProtocol,
		"relay_whitelist":       in.RelayWhitelist,
		"whitelist_extra":       in.WhitelistExtra,
		"enabled":               in.Enabled,
		"outbound_id":           in.OutboundID,
		"split_rules":           in.SplitRules,
		// BUG-A2-1: 此列曾漏在白名单外——inboundSave 与 inboundSplitSave
		// 都赋值 row.OutboundIPStrategy，落库时被静默丢弃，编辑策略永不生效。
		"outbound_ip_strategy": in.OutboundIPStrategy,
		"updated_time":         in.UpdatedTime,
	}).Error
}

func (r *Repository) DeleteNodeInbound(id int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		relayIDs, err := pluckInboundRelayIDsTx(tx, "inbound_id = ?", id)
		if err != nil {
			return err
		}
		if err := tx.Where("inbound_id = ?", id).Delete(&model.NodeInboundCounter{}).Error; err != nil {
			return err
		}
		if err := tx.Where("inbound_id = ?", id).Delete(&model.NodeInboundRelay{}).Error; err != nil {
			return err
		}
		// Clean up user subscription assignments pointing at this inbound,
		// otherwise they become orphans that block future saves ("inbound not found").
		if err := tx.Where("inbound_id = ?", id).Delete(&model.UserInbound{}).Error; err != nil {
			return err
		}
		// A3-02/X2-01 统一手法：指向被删 relay 行的授权按物化集合清。
		if err := deleteUserInboundGrantsByRelayIDsTx(tx, relayIDs); err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&model.NodeInbound{}).Error
	})
}

func (r *Repository) ResetNodeInboundTraffic(id int64, now int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Model(&model.NodeInbound{}).Where("id = ?", id).Updates(map[string]interface{}{
		"upload_bytes":   0,
		"download_bytes": 0,
		"updated_time":   now,
	}).Error
}

func listNodeInboundPortsTx(tx *gorm.DB, nodeID int64, excludeInboundID int64) ([]int, error) {
	var ports []int
	q := tx.Model(&model.NodeInbound{}).Where("node_id = ? AND port > 0", nodeID)
	if excludeInboundID > 0 {
		q = q.Where("id <> ?", excludeInboundID)
	}
	return ports, q.Pluck("port", &ports).Error
}

// NodePortOccupant names what already uses port on a node, or "" when free.
func (r *Repository) NodePortOccupant(nodeID int64, port int, excludeInboundID int64) (string, error) {
	return r.NodePortRangeOccupant(nodeID, port, port, excludeInboundID)
}

// ListNodeInboundsByNode returns every inbound on a node (all kernels).
func (r *Repository) ListNodeInboundsByNode(nodeID int64) ([]model.NodeInbound, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var rows []model.NodeInbound
	return rows, r.db.Where("node_id = ?", nodeID).Order("id ASC").Find(&rows).Error
}

// NodePortRangeOccupant reports what listens on any port in [lo, hi] of a
// node: an inbound, a forward or a tunnel ("" when free).
func (r *Repository) NodePortRangeOccupant(nodeID int64, lo, hi int, excludeInboundID int64) (string, error) {
	if r == nil || r.db == nil {
		return "", errors.New("repository not initialized")
	}
	var count int64
	q := r.db.Model(&model.NodeInbound{}).Where("node_id = ? AND port BETWEEN ? AND ?", nodeID, lo, hi)
	if excludeInboundID > 0 {
		q = q.Where("id <> ?", excludeInboundID)
	}
	if err := q.Count(&count).Error; err != nil {
		return "", err
	}
	if count > 0 {
		return "其他节点服务", nil
	}
	if err := r.db.Model(&model.ForwardPort{}).Where("node_id = ? AND port BETWEEN ? AND ?", nodeID, lo, hi).Count(&count).Error; err != nil {
		return "", err
	}
	if count > 0 {
		return "转发", nil
	}
	if err := r.db.Model(&model.ChainTunnel{}).Where("node_id = ? AND port BETWEEN ? AND ?", nodeID, lo, hi).Count(&count).Error; err != nil {
		return "", err
	}
	if count > 0 {
		return "隧道", nil
	}
	return "", nil
}

// ApplyInboundCounterSamples converts cumulative counters into traffic deltas.
// A sample whose epoch differs from the stored one, or whose value went
// backwards, belongs to a fresh counter and counts from zero.
func (r *Repository) ApplyInboundCounterSamples(samples []InboundCounterSample, now int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	if len(samples) == 0 {
		return nil
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		ids := make([]int64, 0, len(samples))
		for _, s := range samples {
			ids = append(ids, s.InboundID)
		}
		var existing []int64
		if err := tx.Model(&model.NodeInbound{}).Where("id IN ?", ids).Pluck("id", &existing).Error; err != nil {
			return err
		}
		known := make(map[int64]struct{}, len(existing))
		for _, id := range existing {
			known[id] = struct{}{}
		}

		var states []model.NodeInboundCounter
		if err := tx.Where("inbound_id IN ?", ids).Find(&states).Error; err != nil {
			return err
		}
		type key struct {
			id        int64
			proto     string
			direction string
		}
		prev := make(map[key]model.NodeInboundCounter, len(states))
		for _, st := range states {
			prev[key{st.InboundID, st.Protocol, st.Direction}] = st
		}

		// F-075（A4-004）：同一批内同 (inbound, protocol, direction)
		// 的重复样本先合并再算增量（同 epoch 取最大 Bytes；epoch
		// 变化说明计数器已重置，取后到样本为最新化身）。旧实现
		// prev 只读 DB 基线、批次内不推进，重复样本会重复计入。
		merged := make(map[key]InboundCounterSample, len(samples))
		mergedOrder := make([]key, 0, len(samples))
		for _, s := range samples {
			k := key{s.InboundID, s.Protocol, s.Direction}
			if cur, ok := merged[k]; ok {
				switch {
				case s.Epoch != cur.Epoch:
					merged[k] = s
				case s.Bytes > cur.Bytes:
					merged[k] = s
				}
				continue
			}
			merged[k] = s
			mergedOrder = append(mergedOrder, k)
		}
		ordered := make([]InboundCounterSample, 0, len(mergedOrder))
		for _, k := range mergedOrder {
			ordered = append(ordered, merged[k])
		}

		upload := make(map[int64]int64)
		download := make(map[int64]int64)
		unaccounted := make(map[int64]int64)
		for _, s := range ordered {
			if _, ok := known[s.InboundID]; !ok {
				// 未归属样本：计数 ID 不属于任何现存入站。此前在这里
				// 静默丢弃（与 Type 1 转发计费恒 0 同型的故障面），
				// 现在按 ID 聚合打 warn，绝不静默。
				unaccounted[s.InboundID]++
				continue
			}
			if s.Bytes < 0 {
				continue
			}
			if s.Direction != "in" && s.Direction != "out" {
				continue
			}
			delta := s.Bytes
			if old, ok := prev[key{s.InboundID, s.Protocol, s.Direction}]; ok && old.Epoch == s.Epoch && s.Bytes >= old.Bytes {
				delta = s.Bytes - old.Bytes
			}
			if delta > 0 {
				if s.Direction == "in" {
					upload[s.InboundID] += delta
				} else {
					download[s.InboundID] += delta
				}
			}
			row := model.NodeInboundCounter{
				InboundID:   s.InboundID,
				Protocol:    s.Protocol,
				Direction:   s.Direction,
				Epoch:       s.Epoch,
				Bytes:       s.Bytes,
				UpdatedTime: now,
			}
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "inbound_id"}, {Name: "protocol"}, {Name: "direction"}},
				DoUpdates: clause.AssignmentColumns([]string{"epoch", "bytes", "updated_time"}),
			}).Create(&row).Error; err != nil {
				return err
			}
		}
		for id, n := range unaccounted {
			log.Printf("kernel traffic sample unaccounted op=unknown_inbound inbound_id=%d samples=%d action=dropped", id, n)
		}
		for id := range known {
			up, down := upload[id], download[id]
			if up == 0 && down == 0 {
				continue
			}
			if err := tx.Model(&model.NodeInbound{}).Where("id = ?", id).Updates(map[string]interface{}{
				"upload_bytes":   gorm.Expr("upload_bytes + ?", up),
				"download_bytes": gorm.Expr("download_bytes + ?", down),
			}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// InboundRelayView is a relay joined with the forward and tunnel behind it.
type InboundRelayView struct {
	model.NodeInboundRelay
	ForwardName   string
	ForwardStatus int
	RemoteAddr    string
	TunnelID      int64
	TunnelName    string
	TunnelType    int
	ForwardExists bool
}

// ForwardEntry is one public address a forward listens on.
type ForwardEntry struct {
	Host string
	Port int
}

func (r *Repository) ListInboundRelays(inboundID int64) ([]InboundRelayView, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	type row struct {
		model.NodeInboundRelay
		ForwardName   sql.NullString
		ForwardStatus sql.NullInt64
		RemoteAddr    sql.NullString
		TunnelID      sql.NullInt64
		TunnelName    sql.NullString
		TunnelType    sql.NullInt64
	}
	var rows []row
	q := r.db.Model(&model.NodeInboundRelay{}).
		Select("node_inbound_relay.*, forward.name AS forward_name, forward.status AS forward_status, forward.remote_addr AS remote_addr, forward.tunnel_id AS tunnel_id, tunnel.name AS tunnel_name, tunnel.type AS tunnel_type").
		Joins("LEFT JOIN forward ON forward.id = node_inbound_relay.forward_id").
		Joins("LEFT JOIN tunnel ON tunnel.id = forward.tunnel_id").
		Order("node_inbound_relay.id ASC")
	if inboundID > 0 {
		q = q.Where("node_inbound_relay.inbound_id = ?", inboundID)
	}
	if err := q.Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]InboundRelayView, 0, len(rows))
	for _, rw := range rows {
		out = append(out, InboundRelayView{
			NodeInboundRelay: rw.NodeInboundRelay,
			ForwardName:      rw.ForwardName.String,
			ForwardStatus:    int(rw.ForwardStatus.Int64),
			RemoteAddr:       rw.RemoteAddr.String,
			TunnelID:         rw.TunnelID.Int64,
			TunnelName:       rw.TunnelName.String,
			TunnelType:       int(rw.TunnelType.Int64),
			ForwardExists:    rw.ForwardName.Valid,
		})
	}
	return out, nil
}

func (r *Repository) GetInboundRelay(id int64) (*model.NodeInboundRelay, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var rel model.NodeInboundRelay
	err := r.db.Where("id = ?", id).First(&rel).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &rel, err
}

func (r *Repository) CreateInboundRelay(rel *model.NodeInboundRelay) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Create(rel).Error
}

func (r *Repository) DeleteInboundRelay(id int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		// Clean up user subscription assignments pointing at this relay.
		if err := tx.Where("relay_id = ?", id).Delete(&model.UserInbound{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&model.NodeInboundRelay{}).Error
	})
}

// ForwardEntries lists the public host:port pairs of a forward's entry nodes.
func (r *Repository) ForwardEntries(forwardID, tunnelID int64) ([]ForwardEntry, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	joined, _, err := resolveForwardIngress(r.db, forwardID, tunnelID)
	if err != nil {
		return nil, err
	}
	var out []ForwardEntry
	for _, pair := range strings.Split(joined, ",") {
		host, portStr, err := net.SplitHostPort(strings.TrimSpace(pair))
		if err != nil {
			continue
		}
		port, err := strconv.Atoi(portStr)
		if err != nil || port <= 0 {
			continue
		}
		out = append(out, ForwardEntry{Host: host, Port: port})
	}
	return out, nil
}

func (r *Repository) TunnelExitNodeIDs(tunnelID int64) ([]int64, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var ids []int64
	err := r.db.Model(&model.ChainTunnel{}).
		Where("tunnel_id = ? AND chain_type = ?", tunnelID, "3").
		Pluck("node_id", &ids).Error
	return ids, err
}

func (r *Repository) SetNodeInboundRelayOnly(id int64, relayOnly int) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Model(&model.NodeInbound{}).Where("id = ?", id).Update("relay_only", relayOnly).Error
}

// ---- user inbound permissions ----

func (r *Repository) ListUserInboundIDs(userID int64) ([]int64, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var rows []model.UserInbound
	if err := r.db.Where("user_id = ?", userID).Find(&rows).Error; err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.InboundID)
	}
	return ids, nil
}

func (r *Repository) SetUserInbounds(userID int64, inboundIDs []int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", userID).Delete(&model.UserInbound{}).Error; err != nil {
			return err
		}
		for _, inboundID := range inboundIDs {
			row := model.UserInbound{UserID: userID, InboundID: inboundID}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// UserInboundAssignment is a single inbound/relay grant for a user.
// RelayID = 0 means the inbound itself (direct); > 0 means a specific relay.
type UserInboundAssignment struct {
	InboundID int64
	RelayID   int64
}

// ListUserInboundAssignments returns all inbound/relay grants for a user.
func (r *Repository) ListUserInboundAssignments(userID int64) ([]UserInboundAssignment, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var rows []model.UserInbound
	if err := r.db.Where("user_id = ?", userID).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]UserInboundAssignment, 0, len(rows))
	for _, row := range rows {
		out = append(out, UserInboundAssignment{InboundID: row.InboundID, RelayID: row.RelayID})
	}
	return out, nil
}

// SetUserInboundAssignments replaces all inbound/relay grants for a user.
// Each assignment is validated: inbound must exist; relay must belong to the inbound.
// Assignments pointing at deleted inbounds/relays (orphans from before the
// delete-cascade was added) are silently skipped instead of failing the save.
func (r *Repository) SetUserInboundAssignments(userID int64, assignments []UserInboundAssignment) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	// Validate before touching the DB; drop orphans.
	seen := make(map[UserInboundAssignment]bool, len(assignments))
	valid := make([]UserInboundAssignment, 0, len(assignments))
	for _, a := range assignments {
		if a.InboundID <= 0 || a.RelayID < 0 {
			return errors.New("invalid assignment")
		}
		if seen[a] {
			return errors.New("duplicate assignment")
		}
		seen[a] = true
		var cnt int64
		if err := r.db.Model(&model.NodeInbound{}).Where("id = ?", a.InboundID).Count(&cnt).Error; err != nil {
			return err
		}
		if cnt == 0 {
			continue // orphan: inbound was deleted, skip
		}
		if a.RelayID > 0 {
			var rcnt int64
			// Relays are tunnel forwards bound to the inbound via NodeInboundRelay.
			if err := r.db.Model(&model.NodeInboundRelay{}).Where("id = ? AND inbound_id = ?", a.RelayID, a.InboundID).Count(&rcnt).Error; err != nil {
				return err
			}
			if rcnt == 0 {
				continue // orphan: relay was deleted, skip
			}
		}
		valid = append(valid, a)
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", userID).Delete(&model.UserInbound{}).Error; err != nil {
			return err
		}
		for _, a := range valid {
			row := model.UserInbound{UserID: userID, InboundID: a.InboundID, RelayID: a.RelayID}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
