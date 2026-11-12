package repo

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go-backend/internal/store/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 入站连接日志的面板摄入与存储（设计 §5）。事件由入口机 agent 经
// /flow/connlog/v2 批报，面板按 flow v2 同骨架记 stream 游标
// （scope=connlog:节点:stream，与流量游标同表不同 scope、互不干
// 扰）：恰好下一序号才应用事件并同 tx 推进游标；同序号同摘要是
// 真重放（确认不重应用）、同序号不同摘要是身份冲突、序号跳跃是
// 游标回退缺口。事件本身再按 (entry_node_id, conn_id) upsert——
// open 建行、close 补终态——双重幂等：整批重放被游标挡下，跨
// stream 的单事件重报被 upsert 的状态机挡下（已 closed 的行不会
// 被迟到的 open 重开，迟到的 close 可以把 interrupted 行补成真
// closed）。

const (
	ConnlogEntryForward = "forward"
	ConnlogEntryInbound = "inbound"

	ConnlogStatusOpen        = "open"
	ConnlogStatusClosed      = "closed"
	ConnlogStatusInterrupted = "interrupted"

	// ConnlogPerInboundRowCap 是每入站行数帽（设计 §5.2 双限之
	// 一）：与保留天数先到先清，由每小时 prune 执行。
	ConnlogPerInboundRowCap = 50000

	connlogDeleteChunk = 1000
)

// ConnlogEvent 是一条已归一的连接事件：采集键由上报会话定
// （EntryNodeID 不来自载荷），归属键由 ResolveConnlogDisplayKeys
// 在摄入前解析（0/0 表示推导不出、未归属）。
type ConnlogEvent struct {
	Type             string // "open" | "close"
	ConnID           string
	EntryNodeID      int64
	EntryKind        string
	EntryID          string
	DisplayNodeID    int64
	DisplayInboundID int64
	ClientIP         string
	PeerIP           string
	ClientPort       int
	Network          string
	SessionKind      string
	StartTime        int64
	EndTime          int64
	// Status 是 close 事件的 agent 侧终态（"" / "closed" /
	// "interrupted")：interrupted 表示代末结清（EndTime 为最后
	// 已知时刻、无真时长），落库必须原样保留，不得升格为
	// closed（设计 §4.6）。open 事件恒为空。
	Status string
}

// ResolveConnlogDisplayKeys 按拓扑解析一条事件的归属键（设计
// §2）：
//   - forward 入口：entry_id 是 forward 服务名（<forwardID>_... 的
//     N 编码，可带 _tcp/_udp 后缀），经 NodeInboundRelay 的
//     ForwardID → InboundID → 目标入站本行；任一环缺失即未归属。
//   - inbound 入口：entry_id 是入站 tag in-<ID>，归属即入站本行，
//     且入站必须属于上报节点本人（节点 A 无权把记录挂到节点 B 的
//     入站名下——校验不过按未归属落库，不丢弃事件本身）。
//
// 推导不出时返回 (0, 0)：落采集键并标未归属，绝不按地址/端口猜。
func (r *Repository) ResolveConnlogDisplayKeys(entryNodeID int64, entryKind, entryID string) (int64, int64) {
	if r == nil || r.db == nil || entryNodeID <= 0 {
		return 0, 0
	}
	switch entryKind {
	case ConnlogEntryForward:
		forwardID, ok := ParseConnlogForwardServiceID(entryID)
		if !ok {
			return 0, 0
		}
		var relay model.NodeInboundRelay
		if err := r.db.Where("forward_id = ?", forwardID).First(&relay).Error; err != nil {
			return 0, 0
		}
		inbound, err := r.GetNodeInbound(relay.InboundID)
		if err != nil || inbound == nil {
			return 0, 0
		}
		return inbound.NodeID, inbound.ID
	case ConnlogEntryInbound:
		idText, ok := strings.CutPrefix(strings.TrimSpace(entryID), "in-")
		if !ok {
			return 0, 0
		}
		inboundID, err := strconv.ParseInt(idText, 10, 64)
		if err != nil || inboundID <= 0 {
			return 0, 0
		}
		inbound, err := r.GetNodeInbound(inboundID)
		if err != nil || inbound == nil || inbound.NodeID != entryNodeID {
			return 0, 0
		}
		return inbound.NodeID, inbound.ID
	}
	return 0, 0
}

// ParseConnlogForwardServiceID 从 forward 服务名取 forwardID：与
// handler 的 parseFlowServiceIDs 同编码（<forwardID>_<userID>_<
// userTunnelID>[_tcp|_udp]），此处只取首段。服务名是面板生成面
// 的稳定编码，不是启发式猜测。
func ParseConnlogForwardServiceID(serviceName string) (int64, bool) {
	base := strings.TrimSpace(serviceName)
	base = strings.TrimSuffix(strings.TrimSuffix(base, "_tcp"), "_udp")
	parts := strings.Split(base, "_")
	if len(parts) < 3 {
		return 0, false
	}
	forwardID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || forwardID <= 0 {
		return 0, false
	}
	return forwardID, true
}

// ApplyConnlogStreamBatch 在单个事务内完成流游标判定＋事件
// upsert：判定语义与 ApplyTrafficStreamAccounting 同口径（重放/
// 落后返回 ack 无错误、冲突与缺口返回错误），入账内容换成连接
// 事件。digest 是整批事件的摘要（handler 以规范 JSON 计算）。
func (r *Repository) ApplyConnlogStreamBatch(scope string, sequence int64, digest string, now time.Time, events []ConnlogEvent) (TrafficStreamAck, error) {
	if r == nil || r.db == nil {
		return TrafficStreamAck{}, errors.New("repository not initialized")
	}
	if scope == "" || len(scope) > 200 || sequence <= 0 || len(digest) != 64 {
		return TrafficStreamAck{}, ErrTrafficStreamConflict
	}
	ack := TrafficStreamAck{}
	err := r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).
			Create(&TrafficStreamCursor{Scope: scope}).Error; err != nil {
			return err
		}
		var cursor TrafficStreamCursor
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("scope = ?", scope).First(&cursor).Error; err != nil {
			return err
		}
		if sequence <= cursor.Sequence {
			if sequence == cursor.Sequence && digest != cursor.Digest {
				return ErrTrafficStreamConflict
			}
			ack.Cursor = cursor.Sequence
			ack.Replay = sequence == cursor.Sequence
			ack.Behind = sequence < cursor.Sequence
			return nil
		}
		if sequence != cursor.Sequence+1 {
			return &TrafficStreamGap{Expected: cursor.Sequence + 1}
		}
		nowMs := now.UnixMilli()
		for i := range events {
			if err := upsertConnlogEventTx(tx, &events[i], nowMs); err != nil {
				return err
			}
		}
		if err := tx.Model(&TrafficStreamCursor{}).
			Where("scope = ?", scope).
			Updates(map[string]interface{}{
				"sequence":       sequence,
				"digest":         digest,
				"recent_digests": appendRecentDigest(cursor.RecentDigests, sequence, digest),
				"updated_time":   nowMs,
			}).Error; err != nil {
			return err
		}
		ack.Applied = true
		ack.Cursor = sequence
		return nil
	})
	if err != nil {
		return TrafficStreamAck{}, err
	}
	return ack, nil
}

// upsertConnlogEventTx 按 (entry_node_id, conn_id) 应用一条事件。
// 状态机：open 只建行/刷新未终态行；close 补终态（缺 open 时直接
// 建完整行——open 事件丢失不连带丢掉整条记录）；closed 是终态，
// 之后到达的 open/close 一律不再改动该行（首个真 close 为准）。
// close 携 interrupted 时落 interrupted（end 为最后已知时刻、
// 无 duration），真 close 迟到可把 interrupted 补成 closed。
func upsertConnlogEventTx(tx *gorm.DB, ev *ConnlogEvent, nowMs int64) error {
	var row model.InboundConnLog
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("entry_node_id = ? AND conn_id = ?", ev.EntryNodeID, ev.ConnID).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		row = model.InboundConnLog{
			EntryNodeID:      ev.EntryNodeID,
			ConnID:           ev.ConnID,
			EntryKind:        ev.EntryKind,
			EntryID:          ev.EntryID,
			DisplayNodeID:    ev.DisplayNodeID,
			DisplayInboundID: ev.DisplayInboundID,
			ClientIP:         ev.ClientIP,
			PeerIP:           ev.PeerIP,
			ClientPort:       ev.ClientPort,
			Network:          ev.Network,
			SessionKind:      ev.SessionKind,
			StartTime:        ev.StartTime,
			Status:           ConnlogStatusOpen,
			CreatedTime:      nowMs,
		}
		if ev.Type == "close" {
			if row.StartTime <= 0 {
				row.StartTime = ev.EndTime
			}
			if ev.Status == ConnlogStatusInterrupted {
				// 无 open 的结清 close：行以 interrupted 落地，
				// end 为最后已知时刻，不造时长。
				row.Status = ConnlogStatusInterrupted
				row.EndTime = ev.EndTime
			} else {
				row.Status = ConnlogStatusClosed
				row.EndTime = ev.EndTime
				if ev.EndTime >= row.StartTime {
					row.DurationMs = ev.EndTime - row.StartTime
				}
			}
		}
		return tx.Create(&row).Error
	}
	if err != nil {
		return err
	}

	switch ev.Type {
	case "open":
		if row.Status != ConnlogStatusOpen {
			// 已终态/已结清的行不被迟到的 open 重开。
			return nil
		}
		updates := map[string]interface{}{
			"entry_kind":   ev.EntryKind,
			"entry_id":     ev.EntryID,
			"client_ip":    ev.ClientIP,
			"peer_ip":      ev.PeerIP,
			"client_port":  ev.ClientPort,
			"network":      ev.Network,
			"session_kind": ev.SessionKind,
		}
		if ev.StartTime > 0 {
			updates["start_time"] = ev.StartTime
		}
		// 归属只往有值方向补：摄入时 relay 尚未建好而落未归属的
		// 行，后续同连接事件解析出归属时补上；已有归属不覆盖
		// （归属在首事件定版，防拓扑变更后同行漂移）。
		if row.DisplayInboundID == 0 && ev.DisplayInboundID != 0 {
			updates["display_node_id"] = ev.DisplayNodeID
			updates["display_inbound_id"] = ev.DisplayInboundID
		}
		return tx.Model(&model.InboundConnLog{}).Where("id = ?", row.ID).Updates(updates).Error
	case "close":
		if row.Status == ConnlogStatusClosed {
			// closed 是终态：首个真 close 为准，后到的一切 close
			// （含结清）不再改动该行。
			return nil
		}
		if ev.Status == ConnlogStatusInterrupted {
			// 结清 close（agent 代末清扫）：open 行落 interrupted，
			// end_time 取最后已知时刻且永不产生 duration；已
			// interrupted 的行只把最后已知时刻向后刷新。真 close
			// 迟到时走下方分支升格为 closed。
			updates := map[string]interface{}{
				"status": ConnlogStatusInterrupted,
			}
			if ev.EndTime >= row.EndTime {
				updates["end_time"] = ev.EndTime
			}
			if row.ClientIP == "" && ev.ClientIP != "" {
				updates["client_ip"] = ev.ClientIP
			}
			if row.DisplayInboundID == 0 && ev.DisplayInboundID != 0 {
				updates["display_node_id"] = ev.DisplayNodeID
				updates["display_inbound_id"] = ev.DisplayInboundID
			}
			return tx.Model(&model.InboundConnLog{}).Where("id = ?", row.ID).Updates(updates).Error
		}
		updates := map[string]interface{}{
			"status":   ConnlogStatusClosed,
			"end_time": ev.EndTime,
		}
		startTime := row.StartTime
		if startTime <= 0 && ev.StartTime > 0 {
			startTime = ev.StartTime
			updates["start_time"] = startTime
		}
		if startTime > 0 && ev.EndTime >= startTime {
			updates["duration_ms"] = ev.EndTime - startTime
		}
		// close 事件自带全字段：open 行残缺时以 close 的采集面
		// 字段补齐（同一连接的同一事实，不存在口径冲突）。
		if row.ClientIP == "" && ev.ClientIP != "" {
			updates["client_ip"] = ev.ClientIP
		}
		if row.DisplayInboundID == 0 && ev.DisplayInboundID != 0 {
			updates["display_node_id"] = ev.DisplayNodeID
			updates["display_inbound_id"] = ev.DisplayInboundID
		}
		return tx.Model(&model.InboundConnLog{}).Where("id = ?", row.ID).Updates(updates).Error
	}
	return fmt.Errorf("unknown connlog event type %q", ev.Type)
}

// AddConnlogNodeDropped 把一个已应用批次携带的丢弃增量入账到
// 上报节点名下（upsert 累加）。调用方（handler）只在批次 ack
// 为 Applied 时调用一次——agent 的 dropped 是「未确认快照」，
// 重放/落后批次不得重复入账。
func (r *Repository) AddConnlogNodeDropped(nodeID, delta, nowMs int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	if delta <= 0 {
		return nil
	}
	row := model.ConnlogNodeDropped{NodeID: nodeID, DroppedTotal: delta, UpdatedTime: nowMs}
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "node_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"dropped_total": gorm.Expr("connlog_node_dropped.dropped_total + ?", delta),
			"updated_time":  nowMs,
		}),
	}).Create(&row).Error
}

// SumConnlogDroppedByNodes 汇总一组节点的累计采集丢弃（弹窗
// 页脚）。无行按 0 计。
func (r *Repository) SumConnlogDroppedByNodes(nodeIDs []int64) (int64, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("repository not initialized")
	}
	if len(nodeIDs) == 0 {
		return 0, nil
	}
	var total int64
	for _, chunk := range chunkInt64IDs(nodeIDs) {
		var sub int64
		err := r.db.Model(&model.ConnlogNodeDropped{}).
			Where("node_id IN ?", chunk).
			Select("COALESCE(SUM(dropped_total), 0)").
			Scan(&sub).Error
		if err != nil {
			return 0, err
		}
		total += sub
	}
	return total, nil
}

// ConnlogEntryNodeIDs 返回曾为某归属入站供过连接日志行的入口
// 节点集（去重）——页脚 dropped 只汇总真正供数的节点，不把无
// 关节点的丢弃算进来。
func (r *Repository) ConnlogEntryNodeIDs(inboundID int64) ([]int64, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var ids []int64
	err := r.db.Model(&model.InboundConnLog{}).
		Where("display_inbound_id = ?", inboundID).
		Distinct("entry_node_id").
		Pluck("entry_node_id", &ids).Error
	return ids, err
}

// ListNodeConnLogs 是节点连接日志主查询：按入口节点（entry_node_id，
// 即“连到这台服务器”的口径）过滤、start_time 倒序（id 倒序兜底同
// 毫秒稳定序），offset/limit 由 handler 换算。ip 非空时按 client_ip
// 精确匹配。开关只控采集、不删历史行，故查询不过滤开关。
func (r *Repository) ListNodeConnLogs(entryNodeID int64, ip string, offset, limit int) ([]model.InboundConnLog, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	q := r.db.Where("entry_node_id = ?", entryNodeID)
	if ip = strings.TrimSpace(ip); ip != "" {
		q = q.Where("client_ip = ?", ip)
	}
	var rows []model.InboundConnLog
	err := q.Order("start_time DESC, id DESC").Offset(offset).Limit(limit).Find(&rows).Error
	return rows, err
}

// CountNodeConnLogs 是 ListNodeConnLogs 的总数口径（同一过滤条件，
// 供节点端点 total 用）。
func (r *Repository) CountNodeConnLogs(entryNodeID int64, ip string) (int64, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("repository not initialized")
	}
	q := r.db.Model(&model.InboundConnLog{}).Where("entry_node_id = ?", entryNodeID)
	if ip = strings.TrimSpace(ip); ip != "" {
		q = q.Where("client_ip = ?", ip)
	}
	var total int64
	err := q.Count(&total).Error
	return total, err
}

// PruneInboundConnLogsBefore 删除 start_time 早于 cutoffMs 的行
// （保留天数限，设计 §5.2）。分批删防长事务锁表。返回删除行数。
func (r *Repository) PruneInboundConnLogsBefore(cutoffMs int64) (int64, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("repository not initialized")
	}
	var total int64
	for {
		res := r.db.Exec(
			"DELETE FROM inbound_conn_log WHERE id IN (SELECT id FROM inbound_conn_log WHERE start_time < ? LIMIT ?)",
			cutoffMs, connlogDeleteChunk)
		if res.Error != nil {
			return total, res.Error
		}
		total += res.RowsAffected
		if res.RowsAffected < connlogDeleteChunk {
			return total, nil
		}
	}
}

// PruneInboundConnLogsOverCap 执行每入站行数帽（设计 §5.2）：按
// display_inbound_id 分组（含未归属的 0 桶），每组只保留 start_
// time 最新（同刻 id 最大）的 cap 行，最旧先清。单独成函数供
// prune 与测试直调。返回删除行数。
func (r *Repository) PruneInboundConnLogsOverCap(cap int) (int64, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("repository not initialized")
	}
	if cap <= 0 {
		return 0, fmt.Errorf("connlog row cap must be positive, got %d", cap)
	}
	type capGroup struct {
		DisplayInboundID int64
		Cnt              int64
	}
	var groups []capGroup
	if err := r.db.Model(&model.InboundConnLog{}).
		Select("display_inbound_id, COUNT(*) AS cnt").
		Group("display_inbound_id").
		Having("COUNT(*) > ?", cap).
		Scan(&groups).Error; err != nil {
		return 0, err
	}
	var total int64
	for _, g := range groups {
		excess := g.Cnt - int64(cap)
		for excess > 0 {
			chunk := int64(connlogDeleteChunk)
			if excess < chunk {
				chunk = excess
			}
			res := r.db.Exec(
				"DELETE FROM inbound_conn_log WHERE id IN (SELECT id FROM inbound_conn_log WHERE display_inbound_id = ? ORDER BY start_time ASC, id ASC LIMIT ?)",
				g.DisplayInboundID, chunk)
			if res.Error != nil {
				return total, res.Error
			}
			if res.RowsAffected == 0 {
				break
			}
			total += res.RowsAffected
			excess -= res.RowsAffected
		}
	}
	return total, nil
}

// InterruptOpenInboundConnLogs 把某节点仍 open 的行批量置
// interrupted（设计 §4.6①：agent 重启/boot_id 变化时结清）。
// end_time 记 lastKnownMs——面板最后收到该节点旧 boot 消息的时
// 刻，是已知事实，不冒充真实断开时刻；duration 不落（非
// closed）。返回结清行数。
func (r *Repository) InterruptOpenInboundConnLogs(nodeID, lastKnownMs int64) (int64, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("repository not initialized")
	}
	res := r.db.Model(&model.InboundConnLog{}).
		Where("entry_node_id = ? AND status = ?", nodeID, ConnlogStatusOpen).
		Updates(map[string]interface{}{
			"status":   ConnlogStatusInterrupted,
			"end_time": lastKnownMs,
		})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

// InterruptStaleOpenInboundConnLogs 是 24h 兜底清道夫（设计
// §4.6③）：open 超过时限仍无 close 的行置 interrupted，end_time
// 取行自身的 created_time（面板摄入 open 事件的时刻——该连接在
// 那一刻确证存在，是最后已知时刻）。返回结清行数。
func (r *Repository) InterruptStaleOpenInboundConnLogs(startedBeforeMs int64) (int64, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("repository not initialized")
	}
	res := r.db.Model(&model.InboundConnLog{}).
		Where("status = ? AND start_time < ?", ConnlogStatusOpen, startedBeforeMs).
		Updates(map[string]interface{}{
			"status":   ConnlogStatusInterrupted,
			"end_time": gorm.Expr("created_time"),
		})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

// ConnLogEnabledConfigKey 是入站连接日志功能的**总开关**设置键
// （设计 §4.5）：gost 中转入口的采集无日志放大代价，随本开关走、
// **默认开**（键缺失/被清空即开）。内核直连入站的采集另有节点级
// 开关（Node.ConnLogEnabled，默认关）逐节点开启——本键不代管
// 内核面，避免一次关开静默改变全网内核日志形态。
//
// 值域：规范形态 "true"/"false"（保存面
// handler.normalizeAndValidateConfigValue 经
// NormalizeConnLogEnabledValue 收口）；读面
// ConnLogEnabledFromConfigMap 对脏值（绕过保存面直入 DB）按默认
// 开处理，与隧道质量开关的读面口径一致（!= "false" 即开）。
//
// 与 U3 的保留期键 connlog_retention_days 分工：本键只管采不采，
// 保留期/行数帽是摄入面口径，两键不互相代管。
const ConnLogEnabledConfigKey = "connlog_enabled"

// NormalizeConnLogEnabledValue 校验并归一总开关值。空串返回 ""
// （走调用方既有可清空语义：删除该键、回落默认开）；仅接受
// true/false（大小写/首尾空白不敏感），其余整批拒绝并点名。
func NormalizeConnLogEnabledValue(raw string) (string, error) {
	trimmed := strings.TrimSpace(strings.ToLower(raw))
	switch trimmed {
	case "":
		return "", nil
	case "true", "false":
		return trimmed, nil
	default:
		return "", fmt.Errorf("连接日志总开关配置值无效")
	}
}

// ConnLogEnabledFromConfigMap 从配置快照读总开关：仅显式 "false"
// 为关，键缺失、空值与脏值一律按默认开（与
// monitoring/隧道质量开关的读面口径一致）。消费方（U7 入口标记、
// U1 采集下发）统一经此读，不各自解析。
func ConnLogEnabledFromConfigMap(cfg map[string]string) bool {
	if cfg == nil {
		return true
	}
	return strings.TrimSpace(strings.ToLower(cfg[ConnLogEnabledConfigKey])) != "false"
}
