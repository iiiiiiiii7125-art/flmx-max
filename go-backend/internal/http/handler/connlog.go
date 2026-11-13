package handler

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go-backend/internal/http/response"
	"go-backend/internal/monitoring"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// 入站连接日志的面板面（设计 §5.3）：
//
//   - 摄入 POST /flow/connlog/v2：与 flow v2 同鉴权（节点 secret＋
//     AES 信封）、同 stream+sequence+digest 游标骨架。事件分
//     open/close 两类，面板按 (entry_node_id, conn_id) upsert；
//     entry_node_id 只取自 secret 命中的节点行，绝不采信载荷。
//     归属在摄入时按拓扑解析（repo.ResolveConnlogDisplayKeys），
//     解析不出落未归属（0/0），不猜。
//   - 查询 GET /api/v1/node/connlog：管理员专属
//     （ensureAdminAccess，含真实客户端 IP 不对普通用户开放），
//     按入口节点（entry_node_id，即“连到这台服务器”的口径）
//     过滤、start_time 倒序、offset/limit 分页；ip 非空时按
//     client_ip 精确匹配。响应为页对象 {rows, total, enabled}：
//     rows 附解析出的本地端口 localPort 与入口名 entryName，
//     total 为总数，enabled 为该节点的采集开关状态。不提供
//     任何跨机合并参数。

const connlogMaxEventsPerBatch = 1000

type connlogEvent struct {
	Type        string `json:"type"`
	ConnID      string `json:"connId"`
	EntryKind   string `json:"entryKind"`
	EntryID     string `json:"entryId"`
	ClientIP    string `json:"clientIp"`
	PeerIP      string `json:"peerIp"`
	ClientPort  int    `json:"clientPort"`
	Network     string `json:"network"`
	SessionKind string `json:"sessionKind"`
	StartTime   int64  `json:"startTime"`
	EndTime     int64  `json:"endTime"`
	// Status 是 close 事件的终态（closed/interrupted）：agent 侧
	// 代末结清（内核进程换代、agent 重启）产出的 close 带
	// interrupted 且 endTime 为最后已知时刻——面板必须原样落
	// interrupted，绝不把结清当成真实断开（设计 §4.6）。
	Status string `json:"status"`
}

type connlogEnvelope struct {
	Stream   string         `json:"stream"`
	Sequence int64          `json:"sequence"`
	Dropped  int64          `json:"dropped"`
	Events   []connlogEvent `json:"events"`
}

func (h *Handler) flowConnlogV2(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	// 与 flow v2 共用同一鉴权失败限流器（R8-060 同族口径）：未知
	// secret 的尝试先记账再查库，达上限的来源回包与未知 secret
	// 同形（401 纯文本、非确认回执）。
	failKey, throttled := h.flowAuthThrottleKey(r)
	if throttled {
		log.Printf("connlog upload auth throttled")
		http.Error(w, "invalid node", http.StatusUnauthorized)
		return
	}
	secret := r.URL.Query().Get("secret")
	node, err := h.repo.GetNodeBySecret(secret)
	if err != nil {
		h.recordFlowAuthFailure(failKey)
		log.Printf("connlog upload node lookup failed err=%v", err)
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	if node == nil {
		h.recordFlowAuthFailure(failKey)
		http.Error(w, "invalid node", http.StatusUnauthorized)
		return
	}
	raw, err := readAndDecryptFlowBody(r.Body, secret)
	if err != nil {
		http.Error(w, "invalid connlog body", http.StatusBadRequest)
		return
	}
	var envelope connlogEnvelope
	if json.Unmarshal([]byte(raw), &envelope) != nil || len(envelope.Events) == 0 ||
		len(envelope.Events) > connlogMaxEventsPerBatch ||
		envelope.Sequence <= 0 || !validFlowStreamID(envelope.Stream) {
		http.Error(w, "invalid connlog upload", http.StatusBadRequest)
		return
	}
	reject := func(status int, reason string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"stream": envelope.Stream, "sequence": envelope.Sequence, "error": reason,
		})
	}
	if err := validateConnlogEvents(envelope.Events); err != nil {
		log.Printf("connlog upload invalid batch node_id=%d err=%v", node.ID, err)
		reject(http.StatusBadRequest, "invalid_batch")
		return
	}

	// 归属解析（只读、按批缓存）：同一批内同一入口只查一次拓扑。
	type displayKeys struct{ nodeID, inboundID int64 }
	resolved := make(map[string]displayKeys)
	events := make([]repo.ConnlogEvent, 0, len(envelope.Events))
	for _, item := range envelope.Events {
		cacheKey := item.EntryKind + "\x00" + item.EntryID
		keys, ok := resolved[cacheKey]
		if !ok {
			keys.nodeID, keys.inboundID = h.repo.ResolveConnlogDisplayKeys(node.ID, item.EntryKind, item.EntryID)
			resolved[cacheKey] = keys
		}
		events = append(events, repo.ConnlogEvent{
			Type:             item.Type,
			ConnID:           item.ConnID,
			EntryNodeID:      node.ID,
			EntryKind:        item.EntryKind,
			EntryID:          item.EntryID,
			DisplayNodeID:    keys.nodeID,
			DisplayInboundID: keys.inboundID,
			ClientIP:         item.ClientIP,
			PeerIP:           item.PeerIP,
			ClientPort:       item.ClientPort,
			Network:          item.Network,
			SessionKind:      item.SessionKind,
			StartTime:        item.StartTime,
			EndTime:          item.EndTime,
			Status:           item.Status,
		})
	}

	eventsJSON, _ := json.Marshal(envelope.Events)
	digest := fmt.Sprintf("%x", sha256.Sum256(eventsJSON))
	scope := fmt.Sprintf("connlog:%d:%s", node.ID, envelope.Stream)
	ack, err := h.repo.ApplyConnlogStreamBatch(scope, envelope.Sequence, digest, time.Now(), events)
	if err != nil {
		var gap *repo.TrafficStreamGap
		switch {
		case errors.As(err, &gap):
			log.Printf("connlog upload rejected node_id=%d stream=%s sequence=%d cursor=%d events=%d class=reset_stream",
				node.ID, envelope.Stream, envelope.Sequence, gap.Expected-1, len(events))
			reject(http.StatusConflict, "reset_stream")
		case errors.Is(err, repo.ErrTrafficStreamConflict):
			log.Printf("connlog upload rejected node_id=%d stream=%s sequence=%d events=%d class=identity_conflict",
				node.ID, envelope.Stream, envelope.Sequence, len(events))
			reject(http.StatusConflict, "identity_conflict")
		default:
			log.Printf("connlog upload failed node_id=%d err=%v", node.ID, err)
			http.Error(w, "connlog upload not committed", http.StatusServiceUnavailable)
		}
		return
	}
	switch {
	case ack.Behind:
		log.Printf("connlog upload ack not booked node_id=%d stream=%s sequence=%d cursor=%d events=%d class=agent_state_behind",
			node.ID, envelope.Stream, envelope.Sequence, ack.Cursor, len(events))
	case ack.Replay:
		log.Printf("connlog upload ack not booked node_id=%d stream=%s sequence=%d cursor=%d events=%d class=replay_exact",
			node.ID, envelope.Stream, envelope.Sequence, ack.Cursor, len(events))
	}
	if envelope.Dropped > 0 {
		// agent 侧采集丢弃计数（设计 §4.2）：随批报上行的是
		// 「尚未被面板确认」的丢弃快照——同一数值会随后续批次
		// 反复出现，直到某批被真正应用、agent 才把它从待确认
		// 计数里扣掉。故面板只在 ack.Applied 时把本批数值作增
		// 量入账（connlog_node_dropped 按节点累计），重放/落后
		// 批次不重复入账；弹窗页脚据此汇总展示。入账失败只记
		// 日志：批报本身已提交，不能因页脚簿记失败判整批失败。
		log.Printf("connlog upload dropped_report node_id=%d dropped_pending=%d applied=%v", node.ID, envelope.Dropped, ack.Applied)
		if ack.Applied {
			if err := h.repo.AddConnlogNodeDropped(node.ID, envelope.Dropped, time.Now().UnixMilli()); err != nil {
				log.Printf("connlog dropped persist failed node_id=%d err=%v", node.ID, err)
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Stream   string `json:"stream"`
		Sequence int64  `json:"sequence"`
		Cursor   int64  `json:"cursor"`
	}{envelope.Stream, envelope.Sequence, ack.Cursor})
}

// validateConnlogEvents 是整批合法性门（任一项非法整批拒收、回
// invalid_batch 供 agent 二分定位，与 flow v2 同口径）。每条事件
// 必须自带全字段：close 事件在 open 丢失时要能独立建行，故字段
// 校验不因 type 放宽（仅 open 强制 startTime/clientIp，close 强
// 制 endTime）。
func validateConnlogEvents(events []connlogEvent) error {
	for i, ev := range events {
		if len(ev.ConnID) == 0 || len(ev.ConnID) > 64 {
			return fmt.Errorf("event %d: connId length out of range", i)
		}
		if ev.EntryKind != repo.ConnlogEntryForward && ev.EntryKind != repo.ConnlogEntryInbound {
			return fmt.Errorf("event %d: unknown entryKind %q", i, ev.EntryKind)
		}
		if len(ev.EntryID) > 100 {
			return fmt.Errorf("event %d: entryId too long", i)
		}
		if ev.ClientPort < 0 || ev.ClientPort > 65535 {
			return fmt.Errorf("event %d: clientPort out of range", i)
		}
		if ev.ClientIP != "" && net.ParseIP(ev.ClientIP) == nil {
			return fmt.Errorf("event %d: clientIp not an IP address", i)
		}
		if ev.PeerIP != "" && net.ParseIP(ev.PeerIP) == nil {
			return fmt.Errorf("event %d: peerIp not an IP address", i)
		}
		switch ev.Type {
		case "open":
			if ev.Status != "" {
				return fmt.Errorf("event %d: open carries status %q", i, ev.Status)
			}
			if ev.StartTime <= 0 {
				return fmt.Errorf("event %d: open without startTime", i)
			}
			if ev.ClientIP == "" {
				return fmt.Errorf("event %d: open without clientIp", i)
			}
			if ev.Network != "tcp" && ev.Network != "udp" {
				return fmt.Errorf("event %d: unknown network %q", i, ev.Network)
			}
			switch ev.SessionKind {
			case "tcp_conn", "udp_session", "kernel_session", "event_only":
			default:
				return fmt.Errorf("event %d: unknown sessionKind %q", i, ev.SessionKind)
			}
		case "close":
			if ev.Status != "" && ev.Status != repo.ConnlogStatusClosed && ev.Status != repo.ConnlogStatusInterrupted {
				return fmt.Errorf("event %d: unknown close status %q", i, ev.Status)
			}
			if ev.EndTime <= 0 {
				return fmt.Errorf("event %d: close without endTime", i)
			}
			if ev.StartTime > 0 && ev.EndTime < ev.StartTime {
				return fmt.Errorf("event %d: endTime before startTime", i)
			}
		default:
			return fmt.Errorf("event %d: unknown type %q", i, ev.Type)
		}
	}
	return nil
}

type connlogRowView struct {
	ID            int64   `json:"id"`
	EntryNodeID   int64   `json:"entryNodeId"`
	EntryKind     string  `json:"entryKind"`
	EntryID       string  `json:"entryId"`
	DisplayNodeID int64   `json:"displayNodeId"`
	ClientIP      string  `json:"clientIp"`
	PeerIP        *string `json:"peerIp"`
	ClientPort    int     `json:"clientPort"`
	Network       string  `json:"network"`
	SessionKind   string  `json:"sessionKind"`
	StartTime     int64   `json:"startTime"`
	EndTime       *int64  `json:"endTime"`
	DurationMs    *int64  `json:"durationMs"`
	Status        string  `json:"status"`
	// LocalPort 是客户端实际连到的本地端口（直连取入站端口，中转
	// 取转发入口端口）；EntryName 是入口名（入站名／转发名）。
	// 解析不出时 LocalPort=0、EntryName=原始 EntryID，绝不编造。
	LocalPort int    `json:"localPort"`
	EntryName string `json:"entryName"`
}

// nodeConnlog 是节点连接日志查询：
// GET /api/v1/node/connlog?nodeId=&ip=&offset=&limit=。
// 管理员专属（ensureAdminAccess，含真实客户端 IP 不对普通用户
// 开放）。按入口节点（entry_node_id，即“连到这台服务器”的口
// 径）查、start_time 倒序、offset/limit 分页；ip 非空时按
// client_ip 精确匹配。响应为页对象 {rows, total, enabled, dropped,
// retentionDays, markedForwards, markMismatch, markWarning}：rows 为本页行（每行附解析出的 localPort 与 entryName），
// total 为该节点（同 ip 过滤）的总行数，enabled 为该节点的采集开关状态
// （只控采集，不删历史行）；dropped 为该节点的累计采集丢弃数（入口机
// 采集缓冲溢出时丢事件，页脚据此提示日志可能不完整）；retentionDays
// 为当前保留期（面板配置 connlog_retention_days，默认 7 天）；
// markedForwards 为 agent 上报的 live 打标转发数（null＝从未上报），
// markMismatch 为开关开、名下有 forward、且 agent 正面报 0 的失配
// verdict（名下无 forward 时报 0 是真空态，不判失配），markWarning
// 在失配时为“配置未下发到 agent”、开关开但未上报时为未知提示、
// 其余为空。页脚取数失败不拖垮主查询：dropped 回 0、保留期回默认
// （旧逐入站端点同口径）。
func (h *Handler) nodeConnlog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	if !h.ensureAdminAccess(w, r) {
		return
	}
	q := r.URL.Query()
	nodeID, err := strconv.ParseInt(strings.TrimSpace(q.Get("nodeId")), 10, 64)
	if err != nil || nodeID <= 0 {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	node, err := h.repo.GetNodeByID(nodeID)
	if err != nil {
		// R2-F4-01 原始错误门：仓储错误原文只记服务端日志，
		// 客户端回通用文案，不外泄存储层细节。
		log.Printf("connlog query failed node_id=%d err=%v", nodeID, err)
		response.WriteJSON(w, response.ErrDefault("查询连接日志失败"))
		return
	}
	if node == nil {
		response.WriteJSON(w, response.ErrDefault("节点不存在"))
		return
	}
	offset, _ := strconv.Atoi(strings.TrimSpace(q.Get("offset")))
	if offset < 0 {
		offset = 0
	}
	limit, _ := strconv.Atoi(strings.TrimSpace(q.Get("limit")))
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	ip := strings.TrimSpace(q.Get("ip"))
	rows, err := h.repo.ListNodeConnLogs(nodeID, ip, offset, limit)
	if err != nil {
		log.Printf("connlog query failed node_id=%d err=%v", nodeID, err)
		response.WriteJSON(w, response.ErrDefault("查询连接日志失败"))
		return
	}
	total, err := h.repo.CountNodeConnLogs(nodeID, ip)
	if err != nil {
		log.Printf("connlog count failed node_id=%d err=%v", nodeID, err)
		response.WriteJSON(w, response.ErrDefault("查询连接日志失败"))
		return
	}
	views := h.connlogNodeRowViews(rows)
	// 页脚字段：dropped 为该节点的累计采集丢弃数、retentionDays
	// 为当前保留期；取数失败不拖垮主查询（dropped 回 0、保留期
	// 回默认，旧逐入站端点同口径）。
	dropped := int64(0)
	if total, err := h.repo.SumConnlogDroppedByNodes([]int64{nodeID}); err == nil {
		dropped = total
	} else {
		log.Printf("connlog dropped sum failed node_id=%d err=%v (footer falls back to 0)", nodeID, err)
	}
	retentionDays := monitoring.DefaultConnlogRetentionDays
	if cfg, err := h.repo.GetConfigsByNames([]string{monitoring.ConfigConnlogRetentionDays}); err == nil {
		retentionDays = monitoring.ConnlogRetentionDaysFromConfigMap(cfg)
	} else {
		log.Printf("connlog retention read failed node_id=%d err=%v (footer falls back to default %d)", nodeID, err, retentionDays)
	}
	// 打标对账（lane C）：desired（本节点开关）vs actual（agent
	// 上报的 live 打标转发数）。markedForwards 为 null＝agent
	// 从未上报（旧 agent／未连接），此时 markMismatch 恒 false、
	// markWarning 诚实报未知；只有开关开、名下有 forward、且
	// agent 正面报 0 时才判「配置未下发到 agent」，UI（lane B）
	// 据此提示重推。名下无 forward 的节点报 0 是真空态而非失配
	// （无物可标，重推亦无从改变），不判 mismatch、不告警。
	// forward 查询失败时门保持敞开（沿用门前口径）并记日志：
	// DB 错不许藏起一次真实失配。
	marked, markReported := h.connlogMark(nodeID)
	enabled := node.ConnLogEnabled == 1
	hasForwards := true
	if ports, err := h.repo.ListForwardPortsByNode(nodeID); err != nil {
		log.Printf("connlog mark forward check failed node_id=%d err=%v (mismatch verdict falls back to ungated)", nodeID, err)
	} else {
		hasForwards = len(ports) > 0
	}
	var markedForwards interface{}
	if markReported {
		markedForwards = marked
	}
	response.WriteJSON(w, response.OK(map[string]interface{}{
		"rows":           views,
		"total":          total,
		"enabled":        enabled,
		"dropped":        dropped,
		"retentionDays":  retentionDays,
		"markedForwards": markedForwards,
		"markMismatch":   connlogMarkMismatch(enabled, marked, markReported, hasForwards),
		"markWarning":    connlogMarkWarning(enabled, marked, markReported, hasForwards),
	}))
}

// connlogNodeRowViews 把仓储行转成节点视图，逐行附 localPort 与
// entryName。入站与转发端口按页缓存（同页同入口只查一次库）。
func (h *Handler) connlogNodeRowViews(rows []model.InboundConnLog) []connlogRowView {
	views := make([]connlogRowView, 0, len(rows))
	inboundCache := make(map[int64]*model.NodeInbound)
	forwardPortCache := make(map[int64][]model.ForwardPortRecord)
	forwardNameCache := make(map[int64]string)
	for i := range rows {
		row := &rows[i]
		view := connlogRowView{
			ID:            row.ID,
			EntryNodeID:   row.EntryNodeID,
			EntryKind:     row.EntryKind,
			EntryID:       row.EntryID,
			DisplayNodeID: row.DisplayNodeID,
			ClientIP:      row.ClientIP,
			ClientPort:    row.ClientPort,
			Network:       row.Network,
			SessionKind:   row.SessionKind,
			StartTime:     row.StartTime,
			Status:        row.Status,
		}
		if row.PeerIP != "" {
			peer := row.PeerIP
			view.PeerIP = &peer
		}
		// endTime 只在行已离开 open 态时给出（closed=真实断开、
		// interrupted=最后已知时刻，读法由 status 区分）；
		// durationMs 只在 closed 时给出——event_only 与
		// interrupted 恒为 null，前端据此显“—”，不冒充时长。
		if row.Status != repo.ConnlogStatusOpen && row.EndTime > 0 {
			end := row.EndTime
			view.EndTime = &end
		}
		if row.Status == repo.ConnlogStatusClosed {
			duration := row.DurationMs
			view.DurationMs = &duration
		}
		view.LocalPort, view.EntryName = h.connlogRowLocalPort(row, inboundCache, forwardPortCache, forwardNameCache)
		views = append(views, view)
	}
	return views
}

// connlogRowLocalPort 解析一行的本地端口与入口名：直连入站取入站
// 端口＋入站名，中转转发取转发在该入口节点上的端口＋转发名。端口
// 与名字独立回落——任一解析不出时该项回落（0／原始 EntryID），
// 另一项仍如实返回，绝不编造。
func (h *Handler) connlogRowLocalPort(row *model.InboundConnLog, inboundCache map[int64]*model.NodeInbound, forwardPortCache map[int64][]model.ForwardPortRecord, forwardNameCache map[int64]string) (int, string) {
	switch row.EntryKind {
	case repo.ConnlogEntryInbound:
		idText, ok := strings.CutPrefix(strings.TrimSpace(row.EntryID), "in-")
		if !ok {
			return 0, row.EntryID
		}
		inboundID, err := strconv.ParseInt(idText, 10, 64)
		if err != nil || inboundID <= 0 {
			return 0, row.EntryID
		}
		inbound, cached := inboundCache[inboundID]
		if !cached {
			var err error
			inbound, err = h.repo.GetNodeInbound(inboundID)
			if err != nil {
				// R3-MR-01：读错记日志、不静默吞——回落仍是
				// (0, 原始 EntryID)，行照常列出、不编造。
				log.Printf("connlog enrichment failed inbound_id=%d err=%v (localPort/entryName fall back)", inboundID, err)
			}
			inboundCache[inboundID] = inbound
		}
		if inbound == nil {
			return 0, row.EntryID
		}
		return inbound.Port, inbound.Name
	case repo.ConnlogEntryForward:
		forwardID, ok := repo.ParseConnlogForwardServiceID(row.EntryID)
		if !ok {
			return 0, row.EntryID
		}
		ports, cached := forwardPortCache[forwardID]
		if !cached {
			var err error
			ports, err = h.repo.ListForwardPorts(forwardID)
			if err != nil {
				// R3-MR-01：读错记日志、不静默吞——端口回落 0，
				// 名字仍按转发记录解析，不编造。
				log.Printf("connlog enrichment failed forward_id=%d err=%v (localPort falls back to 0)", forwardID, err)
			}
			forwardPortCache[forwardID] = ports
		}
		port := 0
		for _, p := range ports {
			if p.NodeID == row.EntryNodeID {
				port = p.Port
				break
			}
		}
		name, cached := forwardNameCache[forwardID]
		if !cached {
			if fwd, err := h.getForwardRecord(forwardID); err == nil && fwd != nil {
				name = fwd.Name
			} else {
				name = row.EntryID
			}
			forwardNameCache[forwardID] = name
		}
		return port, name
	default:
		return 0, row.EntryID
	}
}
