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
	// 零值家族收口：NodeKernel.Enabled 带 `default:1`，Create 会吞掉
	// 显式 0。现行两个调用点恒传 1，此处仍按 CreateNodeInbound
	// （BUG-A4-1）范式留意图写回，避免未来调用方踩中同族坑。
	wantEnabled := k.Enabled
	// R4X-023：调用方均为 check-then-insert（GetNodeKernelByType 得 nil
	// 才 Create），两条产品路径并发到达时（WS 重连对账 syncRealmNodes
	// 与同步 switchTunnelEngine / 管理员双击安装）双插同键，败者此前
	// 吃 (node_id, kernel_type) UNIQUE 约束错误并把裸 DB 错误回给调用
	// 方。改冲突安全写法：ON CONFLICT DO NOTHING 单语句插入，未插入
	// 说明胜者行已落库——回读既有行写入 k 并幂等成功（胜者行内容与
	// 本次拟插入行同口径，见批 9 spike 定性）。与 node_seq 的冲突安全
	// 范式同源；非重试语义，不掩盖其他错误。
	res := r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "node_id"}, {Name: "kernel_type"}},
		DoNothing: true,
	}).Create(k)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		existing, err := r.GetNodeKernelByType(k.NodeID, k.KernelType)
		if err != nil {
			return err
		}
		if existing == nil {
			return errors.New("node kernel create conflicted but row not found")
		}
		*k = *existing
		return nil
	}
	if wantEnabled == 0 {
		if err := r.db.Model(&model.NodeKernel{}).Where("id = ?", k.ID).Update("enabled", 0).Error; err != nil {
			return err
		}
		k.Enabled = 0
	}
	return nil
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

// GetCompanionInbound returns the SS metering companion row derived
// from the given origin inbound, or (nil, nil) when none exists.
func (r *Repository) GetCompanionInbound(originID int64) (*model.NodeInbound, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var in model.NodeInbound
	err := r.db.Where("companion_of = ?", originID).Order("id ASC").First(&in).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &in, err
}

// ListCompanionInbounds returns every SS metering companion row
// (companion_of > 0), ordered by origin then id.
func (r *Repository) ListCompanionInbounds() ([]model.NodeInbound, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var rows []model.NodeInbound
	return rows, r.db.Where("companion_of > 0").Order("companion_of ASC, id ASC").Find(&rows).Error
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
		"share_address_mode":    in.ShareAddressMode,
		"remark":                in.Remark,
		"relay_only":            in.RelayOnly,
		"accept_proxy_protocol": in.AcceptProxyProtocol,
		"relay_whitelist":       in.RelayWhitelist,
		"whitelist_extra":       in.WhitelistExtra,
		// F8: syncCompanion mirrors the origin's inx onto companions;
		// without the column here that assignment would be silently
		// dropped (same BUG-A2-1 lesson). Every other caller loads the
		// row first, so this writes back the stored value unchanged.
		"inx":         in.Inx,
		"enabled":     in.Enabled,
		"outbound_id": in.OutboundID,
		"split_rules": in.SplitRules,
		// BUG-A2-1: 此列曾漏在白名单外——inboundSave 与 inboundSplitSave
		// 都赋值 row.OutboundIPStrategy，落库时被静默丢弃，编辑策略永不生效。
		"outbound_ip_strategy": in.OutboundIPStrategy,
		// R20-X-02 (F-02): 同类复发——inboundSave 经 applyInboundToModel
		// 赋值 row.ConnIdleSeconds，落库时被静默丢弃，编辑空闲超时报
		// code 0 却不生效（list 与渲染 policy 均显旧值）。
		"conn_idle_seconds": in.ConnIdleSeconds,
		// SS 计量伴生标记与开关（伴生行由 reconcile 维护，原入站开关由
		// inboundSave 写；同 BUG-A2-1 教训，赋值必须与白名单同步）。
		"companion_of":     in.CompanionOf,
		"metering_enabled": in.MeteringEnabled,
		"updated_time":     in.UpdatedTime,
	}).Error
}

// ClearNodeInboundRouting clears a row's chained exit and 分流 rules
// (R14-A5-08 migration for Snell poisoned rows: a Snell-kernel row
// carrying OutboundID / SplitRules can never render, so the deploy-time
// heal drops exactly those two columns back to direct).
func (r *Repository) ClearNodeInboundRouting(id int64, now int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Model(&model.NodeInbound{}).Where("id = ?", id).Updates(map[string]interface{}{
		"outbound_id":  0,
		"split_rules":  "",
		"updated_time": now,
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
	// P01-001：实现收敛到统一占用文件的多值排除形，本函数只做单值适配。
	var excl []int64
	if excludeInboundID > 0 {
		excl = []int64{excludeInboundID}
	}
	return listNodeInboundPortsExclTx(tx, nodeID, excl)
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
// node: an inbound, a forward, a tunnel or a federation share reservation
// ("" when free). P01-001: the occupancy knowledge lives in the unified
// four-source predicate (NodePortRangeOccupancyTx); this function only
// maps sources to display names, keeping the historical precedence
// (inbound → forward → tunnel, with the companion special case).
func (r *Repository) NodePortRangeOccupant(nodeID int64, lo, hi int, excludeInboundID int64) (string, error) {
	if r == nil || r.db == nil {
		return "", errors.New("repository not initialized")
	}
	var excl PortExclusions
	if excludeInboundID > 0 {
		excl.InboundIDs = []int64{excludeInboundID}
	}
	occ, err := r.NodePortRangeOccupancyTx(r.db, nodeID, lo, hi, excl)
	if err != nil {
		return "", err
	}
	switch {
	case occ.Sources[PortSourceInbound]:
		// F7 (B-05): name a metering companion when one occupies the
		// range — it appears in no user-facing list and cannot be
		// moved or deleted directly, so the generic label would send
		// the user hunting for a service that does not exist for
		// them. The port frees up by disabling subscription metering
		// on the origin inbound.
		if occ.InboundCompanion {
			return "SS 计量伴生入站", nil
		}
		return "其他节点服务", nil
	case occ.Sources[PortSourceForward]:
		return "转发", nil
	case occ.Sources[PortSourceChain]:
		return "隧道", nil
	case occ.Sources[PortSourcePeer]:
		return "联邦分享预留", nil
	}
	return "", nil
}

// ApplyInboundCounterSamples converts cumulative counters into traffic deltas.
// R2X-033：基线语义与订阅管线同形——无基线（首采）或 epoch 变化
// 时只推进基线、delta=0，不把累计值计入（旧语义全量计，干净恢复
// 或内核重启换代后会把历史累计重复计入一遍）；同 epoch 前进取
// 差分；同 epoch 回退（计数器同代重置）当前值全量计，保持既有
// 语义。
func (r *Repository) ApplyInboundCounterSamples(samples []InboundCounterSample, now int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	if len(samples) == 0 {
		return nil
	}

	// F-075（A4-004）：同一批内同 (inbound, protocol, direction)
	// 的重复样本先合并再算增量（同 epoch 取最大 Bytes；epoch
	// 变化说明计数器已重置，取后到样本为最新化身）。旧实现
	// prev 只读 DB 基线、批次内不推进，重复样本会重复计入。
	// 合并是纯函数，先于分片完成：合并后每个键在整批中只出现一次，
	// 这是 D6-03 分片后片间互不依赖的前提。
	merged := make(map[inboundCounterKey]InboundCounterSample, len(samples))
	mergedOrder := make([]inboundCounterKey, 0, len(samples))
	for _, s := range samples {
		k := inboundCounterKey{s.InboundID, s.Protocol, s.Direction}
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

	// D6-03：原实现把整批（实测 1200–2300 条 upsert + 数百条 UPDATE）
	// 塞进单个事务，串行占库 0.25–0.9s。改为按 200 条分片提交：
	// 每片内「基线推进 + 总量入账」仍是同一事务（原子性不变），
	// 片间键互不重叠、总量是可加表达式，故结果与单事务等价；
	// 某片失败时已提交片的基线与总量同步前进，以同批样本重试时
	// 已结算部分 delta=0，不会重复计入。
	const chunkSize = 200
	for start := 0; start < len(ordered); start += chunkSize {
		end := start + chunkSize
		if end > len(ordered) {
			end = len(ordered)
		}
		chunk := ordered[start:end]
		if err := r.db.Transaction(func(tx *gorm.DB) error {
			return applyInboundCounterChunk(tx, chunk, now)
		}); err != nil {
			return err
		}
	}
	return nil
}

type inboundCounterKey struct {
	id        int64
	proto     string
	direction string
}

// applyInboundCounterChunk settles one chunk of merged samples in a
// single transaction: baseline upserts and inbound totals move together.
func applyInboundCounterChunk(tx *gorm.DB, ordered []InboundCounterSample, now int64) error {
	{
		ids := make([]int64, 0, len(ordered))
		for _, s := range ordered {
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

		// F-S01（R4 发版门项 11 升 P3）：基线行以 FOR UPDATE 读，
		// 与订阅管线同修法——PG Read Committed 下两路并发同一样本
		// 各按同一旧基线算 delta 会双计（门上实证入站后两轮双
		// 计）；行锁串行化后后到事务重读已提交基线、delta 归 0。
		// 按唯一键序取锁防锁序交叉；SQLite 方言自动忽略 FOR 子
		// 句，行为不变（MaxOpenConns(1) 本就串行）。
		var states []model.NodeInboundCounter
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("inbound_id IN ?", ids).
			Order("inbound_id ASC, protocol ASC, direction ASC").
			Find(&states).Error; err != nil {
			return err
		}
		prev := make(map[inboundCounterKey]model.NodeInboundCounter, len(states))
		for _, st := range states {
			prev[inboundCounterKey{st.InboundID, st.Protocol, st.Direction}] = st
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
			// R2X-033：与订阅管线同形——无基线 / epoch 变化只
			// 推进基线（delta=0）；同 epoch 前进取差分；同 epoch
			// 回退全量计（既有语义不变）。
			var delta int64
			old, seen := prev[inboundCounterKey{s.InboundID, s.Protocol, s.Direction}]
			switch {
			case !seen:
				delta = 0
			case old.Epoch != s.Epoch:
				delta = 0
			case s.Bytes >= old.Bytes:
				delta = s.Bytes - old.Bytes
			default:
				delta = s.Bytes
			}
			if delta > 0 {
				// B08-001 同族：批内合并走饱和加，与落库面守卫同口径，
				// 防多样本增量先在 Go 层回绕为负再绕过 SQL 守卫。
				if s.Direction == "in" {
					upload[s.InboundID] = saturatingAddInt64(upload[s.InboundID], delta)
				} else {
					download[s.InboundID] = saturatingAddInt64(download[s.InboundID], delta)
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
			// B08-001 同族：入站累计字节是持久累加器，裸加溢出同
			// flow 三面（SQLite 回绕落库 / PG 报错回滚整批），走饱和口径。
			if err := tx.Model(&model.NodeInbound{}).Where("id = ?", id).Updates(map[string]interface{}{
				"upload_bytes":   saturatingCounterIncrExpr("upload_bytes", up),
				"download_bytes": saturatingCounterIncrExpr("download_bytes", down),
			}).Error; err != nil {
				return err
			}
		}
		return nil
	}
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
	_, err := r.SetUserInboundAssignmentsCounted(userID, assignments)
	return err
}

// SetUserInboundAssignmentsCounted is SetUserInboundAssignments plus the
// number of assignments skipped because their relay does not exist or does
// not belong to the inbound (R3X-022: the skip stays silent at the data
// layer by design, but the handler receipt now reports skippedRelays
// instead of claiming an unqualified success).
func (r *Repository) SetUserInboundAssignmentsCounted(userID int64, assignments []UserInboundAssignment) (skippedRelays int, err error) {
	if r == nil || r.db == nil {
		return 0, errors.New("repository not initialized")
	}
	// Validate before touching the DB; drop orphans.
	seen := make(map[UserInboundAssignment]bool, len(assignments))
	valid := make([]UserInboundAssignment, 0, len(assignments))
	for _, a := range assignments {
		if a.InboundID <= 0 || a.RelayID < 0 {
			return 0, errors.New("invalid assignment")
		}
		if seen[a] {
			return 0, errors.New("duplicate assignment")
		}
		seen[a] = true
		var cnt int64
		if err := r.db.Model(&model.NodeInbound{}).Where("id = ?", a.InboundID).Count(&cnt).Error; err != nil {
			return 0, err
		}
		if cnt == 0 {
			continue // orphan: inbound was deleted, skip
		}
		if a.RelayID > 0 {
			var rcnt int64
			// Relays are tunnel forwards bound to the inbound via NodeInboundRelay.
			if err := r.db.Model(&model.NodeInboundRelay{}).Where("id = ? AND inbound_id = ?", a.RelayID, a.InboundID).Count(&rcnt).Error; err != nil {
				return 0, err
			}
			if rcnt == 0 {
				skippedRelays++
				continue // orphan: relay was deleted, skip
			}
		}
		valid = append(valid, a)
	}
	err = r.db.Transaction(func(tx *gorm.DB) error {
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
	if err != nil {
		return 0, err
	}
	return skippedRelays, nil
}
