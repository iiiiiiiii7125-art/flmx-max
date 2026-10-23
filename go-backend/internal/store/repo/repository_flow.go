package repo

import (
	"errors"
	"log"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"go-backend/internal/store/model"
)

type FlowUploadForwardMeta struct {
	ForwardID    int64
	UserID       int64
	UserTunnelID int64
	TunnelID     int64
	TrafficRatio float64
	TunnelFlow   int64
}

const flowUploadForwardMetaChunkSize = 500

func chunkFlowUploadForwardIDs(ids []int64) [][]int64 {
	if len(ids) == 0 {
		return nil
	}
	chunks := make([][]int64, 0, (len(ids)+flowUploadForwardMetaChunkSize-1)/flowUploadForwardMetaChunkSize)
	for start := 0; start < len(ids); start += flowUploadForwardMetaChunkSize {
		end := start + flowUploadForwardMetaChunkSize
		if end > len(ids) {
			end = len(ids)
		}
		chunks = append(chunks, ids[start:end])
	}
	return chunks
}

func (r *Repository) GetFlowUploadForwardMetas(forwardIDs []int64) (map[int64]FlowUploadForwardMeta, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	if len(forwardIDs) == 0 {
		return map[int64]FlowUploadForwardMeta{}, nil
	}

	ids := make([]int64, 0, len(forwardIDs))
	seen := make(map[int64]struct{}, len(forwardIDs))
	for _, id := range forwardIDs {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return map[int64]FlowUploadForwardMeta{}, nil
	}

	type row struct {
		ForwardID     int64   `gorm:"column:forward_id"`
		UserID        int64   `gorm:"column:user_id"`
		UserTunnelID  int64   `gorm:"column:user_tunnel_id"`
		TunnelID      int64   `gorm:"column:tunnel_id"`
		TrafficRatio  float64 `gorm:"column:traffic_ratio"`
		TunnelFlow    int64   `gorm:"column:tunnel_flow"`
		TunnelMissing int64   `gorm:"column:tunnel_missing"`
	}

	out := make(map[int64]FlowUploadForwardMeta, len(ids))
	for _, chunk := range chunkFlowUploadForwardIDs(ids) {
		var rows []row
		err := r.db.Table("forward AS f").
			Select("f.id AS forward_id, f.user_id AS user_id, COALESCE(ut.id, 0) AS user_tunnel_id, f.tunnel_id AS tunnel_id, t.traffic_ratio AS traffic_ratio, t.flow AS tunnel_flow, CASE WHEN t.id IS NULL THEN 1 ELSE 0 END AS tunnel_missing").
			Joins("LEFT JOIN tunnel t ON t.id = f.tunnel_id").
			Joins("LEFT JOIN user_tunnel ut ON ut.user_id = f.user_id AND ut.tunnel_id = f.tunnel_id").
			Where("f.id IN ?", chunk).
			Scan(&rows).Error
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row.TunnelMissing == 1 {
				// F-S01 硬化（不改入账语义）：forward 指向的 tunnel
				// 行真缺失属数据不一致——此前静默按默认 ratio/flow
				// 归一化入账、无任何痕迹，现打 warn 日志留痕；归一
				// 化与入账口径保持不变。
				log.Printf("flow upload meta warn op=tunnel_row_missing forward_id=%d tunnel_id=%d action=default_ratio_flow", row.ForwardID, row.TunnelID)
			}
			if row.TunnelFlow <= 0 {
				row.TunnelFlow = 1
			}
			// R3X-003：TrafficRatio 的兜底归一化只许发生在隧道行
			// 缺失时（TunnelMissing，此时 ratio/flow 本就无值）。
			// 行存在时 0 是有意配置（BC-R2-03 零计费），必须保留 0
			// 使消费层 quotaUsage=0、O6-005 警告分支可达；负值无合
			// 法计费语义，钳为 0 并 warn，不产生负计费。
			if row.TunnelMissing == 1 {
				if row.TrafficRatio <= 0 {
					row.TrafficRatio = 1
				}
			} else if row.TrafficRatio < 0 {
				log.Printf("flow upload meta warn op=traffic_ratio_negative_clamped forward_id=%d tunnel_id=%d ratio=%v action=clamp_zero", row.ForwardID, row.TunnelID, row.TrafficRatio)
				row.TrafficRatio = 0
			}
			out[row.ForwardID] = FlowUploadForwardMeta{
				ForwardID:    row.ForwardID,
				UserID:       row.UserID,
				UserTunnelID: row.UserTunnelID,
				TunnelID:     row.TunnelID,
				TrafficRatio: row.TrafficRatio,
				TunnelFlow:   row.TunnelFlow,
			}
		}
	}
	return out, nil
}

func (r *Repository) UpdateForwardStatus(forwardID int64, status int, now int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Model(&model.Forward{}).Where("id = ?", forwardID).Updates(map[string]interface{}{
		"status": status, "updated_time": now,
	}).Error
}

func (r *Repository) ListActiveForwardsByUser(userID int64) ([]model.ForwardRecord, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var forwards []model.Forward
	err := r.db.Where("user_id = ? AND status = 1", userID).Order("id ASC").Find(&forwards).Error
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

func (r *Repository) ListActiveForwardsByUserTunnel(userID, tunnelID int64) ([]model.ForwardRecord, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var forwards []model.Forward
	err := r.db.Where("user_id = ? AND tunnel_id = ? AND status = 1", userID, tunnelID).Order("id ASC").Find(&forwards).Error
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

// ListForwardsByUser returns every forward owned by a user across all
// tunnels (B4/F-057: user deletion must clean each forward's data plane
// before the cascade removes the rows).
func (r *Repository) ListForwardsByUser(userID int64) ([]model.ForwardRecord, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var forwards []model.Forward
	err := r.db.Where("user_id = ?", userID).Order("id ASC").Find(&forwards).Error
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

func (r *Repository) ListForwardsByUserAndTunnel(userID, tunnelID int64) ([]model.ForwardRecord, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var forwards []model.Forward
	err := r.db.Where("user_id = ? AND tunnel_id = ?", userID, tunnelID).Order("id ASC").Find(&forwards).Error
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

func (r *Repository) GetForwardRecord(forwardID int64) (*model.ForwardRecord, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var f model.Forward
	err := r.db.Where("id = ?", forwardID).First(&f).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	proxyProtocolReceive, proxyProtocolSend := normalizeForwardProxyProtocol(f.ProxyProtocol, f.ProxyProtocolReceive, f.ProxyProtocolSend)
	fr := model.ForwardRecord{
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
	}
	if strings.TrimSpace(fr.Strategy) == "" {
		fr.Strategy = "fifo"
	}
	return &fr, nil
}

// GetTunnelRecord returns the tunnel record with read-side
// normalization applied (Flow/TrafficRatio <= 0 are presented as 1).
//
// R4X-004 口径拆分：本函数仅限只读消费（运行时渲染/判定/诊断），
// 禁止作写回源——归一化后的假值经 presence 回填写回会把存量合法
// 零值（如 traffic_ratio=0 的零计费隧道）静默改成 1 并持久化。
// 任何「读旧值 → 回填 → 写库」的链路必须改走 GetTunnelRecordRaw。
func (r *Repository) GetTunnelRecord(tunnelID int64) (*model.TunnelRecord, error) {
	tr, err := r.GetTunnelRecordRaw(tunnelID)
	if err != nil || tr == nil {
		return tr, err
	}
	if tr.Flow <= 0 {
		tr.Flow = 1
	}
	if tr.TrafficRatio <= 0 {
		tr.TrafficRatio = 1
	}
	return tr, nil
}

// GetTunnelRecordRaw returns the tunnel record exactly as stored,
// without the Flow/TrafficRatio normalization of GetTunnelRecord.
// It is the only approved old-value source for write-back paths
// (presence backfill in tunnelUpdate, R4X-004).
func (r *Repository) GetTunnelRecordRaw(tunnelID int64) (*model.TunnelRecord, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var t model.Tunnel
	err := r.db.Where("id = ?", tunnelID).First(&t).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &model.TunnelRecord{
		ID:              t.ID,
		Type:            t.Type,
		Status:          t.Status,
		Flow:            t.Flow,
		TrafficRatio:    t.TrafficRatio,
		Protocol:        t.Protocol,
		ProbeTargetHost: t.ProbeTargetHost,
		ProbeTargetPort: t.ProbeTargetPort,
		Engine:          t.Engine,
	}, nil
}

func (r *Repository) TunnelExists(tunnelID int64) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("repository not initialized")
	}
	var count int64
	err := r.db.Model(&model.Tunnel{}).Where("id = ?", tunnelID).Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *Repository) ForwardExists(forwardID int64) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("repository not initialized")
	}
	var count int64
	err := r.db.Model(&model.Forward{}).Where("id = ?", forwardID).Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// MapForwardIDsToTunnelIDs returns a mapping from forward.id to forward.tunnel_id.
// Missing forward IDs are omitted from the returned map.
func (r *Repository) MapForwardIDsToTunnelIDs(forwardIDs []int64) (map[int64]int64, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	if len(forwardIDs) == 0 {
		return map[int64]int64{}, nil
	}

	// Deduplicate and filter invalid IDs.
	ids := make([]int64, 0, len(forwardIDs))
	seen := make(map[int64]struct{}, len(forwardIDs))
	for _, id := range forwardIDs {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return map[int64]int64{}, nil
	}

	type row struct {
		ID       int64 `gorm:"column:id"`
		TunnelID int64 `gorm:"column:tunnel_id"`
	}

	out := make(map[int64]int64, len(ids))
	const chunkSize = 500
	for start := 0; start < len(ids); start += chunkSize {
		end := start + chunkSize
		if end > len(ids) {
			end = len(ids)
		}

		var rows []row
		if err := r.db.Model(&model.Forward{}).
			Select("id", "tunnel_id").
			Where("id IN ?", ids[start:end]).
			Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, r := range rows {
			if r.ID <= 0 || r.TunnelID <= 0 {
				continue
			}
			out[r.ID] = r.TunnelID
		}
	}

	return out, nil
}

func (r *Repository) SpeedLimitExists(id int64) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("repository not initialized")
	}
	return speedLimitExists(r.db, id)
}

func (r *Repository) SpeedLimitExistsTx(tx *gorm.DB, id int64) (bool, error) {
	if tx == nil {
		return false, errors.New("database unavailable")
	}
	return speedLimitExists(tx, id)
}

func speedLimitExists(db *gorm.DB, id int64) (bool, error) {
	var count int64
	// O2-005：只查询已启用的限速规则，已禁用的不得被引用生效。
	err := db.Model(&model.SpeedLimit{}).Where("id = ? AND status = 1", id).Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *Repository) CountActiveForwardsByUser(userID int64) (int64, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("repository not initialized")
	}
	var count int64
	err := r.db.Model(&model.Forward{}).Where("user_id = ? AND status = 1", userID).Count(&count).Error
	return count, err
}

func (r *Repository) CountActiveForwardsByUserTunnel(userID, tunnelID int64) (int64, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("repository not initialized")
	}
	var count int64
	err := r.db.Model(&model.Forward{}).Where("user_id = ? AND tunnel_id = ? AND status = 1", userID, tunnelID).Count(&count).Error
	return count, err
}

// GetSpeedLimit returns the full stored speed-limit row (any status), or
// nil when it does not exist. R4X-019: speedLimitUpdate needs the stored
// row as its presence backfill source; GetSpeedLimitSpeed cannot serve
// (it filters status=1 and selects only the speed column).
func (r *Repository) GetSpeedLimit(id int64) (*model.SpeedLimit, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var sl model.SpeedLimit
	err := r.db.Where("id = ?", id).First(&sl).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &sl, nil
}

// SpeedLimitAffectedForwardIDs 返回引用某限速规则的全部转发 ID
// （R6X-017 引用集正本，去重升序）：转发直引两列（forward.speed_id /
// forward.ip_speed_id）+ user_tunnel.speed_id 间接引（该 UT 的
// user_id+tunnel_id 名下全部转发，其 limiter 在同步时经
// ResolveUserTunnelAndLimiter 解析到该规则）。规则编辑/删除/禁用后
// 的重下发面以此集为准；删除前枚举与删除后按悬空 id 反查同集。
func (r *Repository) SpeedLimitAffectedForwardIDs(id int64) ([]int64, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	seen := make(map[int64]bool)
	var ids []int64
	add := func(list []int64) {
		for _, fid := range list {
			if fid > 0 && !seen[fid] {
				seen[fid] = true
				ids = append(ids, fid)
			}
		}
	}
	var direct []int64
	if err := r.db.Model(&model.Forward{}).
		Where("speed_id = ? OR ip_speed_id = ?", id, id).
		Order("id ASC").Pluck("id", &direct).Error; err != nil {
		return nil, err
	}
	add(direct)
	type utPair struct {
		UserID   int64 `gorm:"column:user_id"`
		TunnelID int64 `gorm:"column:tunnel_id"`
	}
	var pairs []utPair
	if err := r.db.Model(&model.UserTunnel{}).
		Select("user_id, tunnel_id").
		Where("speed_id = ?", id).
		Find(&pairs).Error; err != nil {
		return nil, err
	}
	for _, pair := range pairs {
		var viaUT []int64
		if err := r.db.Model(&model.Forward{}).
			Where("user_id = ? AND tunnel_id = ?", pair.UserID, pair.TunnelID).
			Order("id ASC").Pluck("id", &viaUT).Error; err != nil {
			return nil, err
		}
		add(viaUT)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}

func (r *Repository) GetSpeedLimitSpeed(id int64) (int, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("repository not initialized")
	}
	var sl model.SpeedLimit
	// O2-005：只读取已启用的限速规则，已禁用的不得被引用生效。
	err := r.db.Select("speed").Where("id = ? AND status = 1", id).First(&sl).Error
	if err != nil {
		return 0, err
	}
	return sl.Speed, nil
}

// FlowSeqAdvance 描述一次入账成功须同事务推进的上报 seq。
type FlowSeqAdvance struct {
	NodeID int64
	Seq    *int64
}

// ApplyFlowUploadAccounting X-P3-31（V-08）：gost 入账单事务——
// 总量增量与 quota 用量同 tx 写入，任一段失败整批回滚（旧实现两段
// 分开写 + 逐条 fallback，一段失败另一段继续，总量与 quota 可分叉）。
// seqAdv 非 nil 时 seq 推进同 tx 落地：入账失败 seq 不动，agent 以
// 同 seq 重报可正常入账（推进后 CheckSeq 判重放防重复入账）。
func (r *Repository) ApplyFlowUploadAccounting(deltas []FlowUploadCounterDelta, quotaUsage map[int64]int64, now time.Time, seqAdv *FlowSeqAdvance) (map[int64]*model.UserQuotaView, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	quotaViews := map[int64]*model.UserQuotaView{}
	err := r.db.Transaction(func(tx *gorm.DB) error {
		// R2X-011：seq 推进提到 deltas 之前——推进失败（ErrSeqReplay）
		// 时整 tx 在写入任何账目前即回滚，重放判定不依赖写入顺序的
		// 副作用残留；且推进本身是本 tx 的第一道门。
		if seqAdv != nil && seqAdv.NodeID > 0 {
			if err := advanceSeqTx(tx, seqAdv.NodeID, seqAdv.Seq, now.UnixMilli()); err != nil {
				return err
			}
		}
		if err := applyFlowUploadDeltasTx(tx, deltas); err != nil {
			return err
		}
		var err error
		quotaViews, err = r.addUserQuotaUsageBatchTx(tx, quotaUsage, now)
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return quotaViews, nil
}
