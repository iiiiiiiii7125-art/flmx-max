package handler

import (
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go-backend/internal/http/response"
)

// 节点连接日志就绪指示 + 一键重新下发（connlog 自愈 lane B）。
//
// 背景：用户在 gost-only 隧道入口节点上开了连接日志开关却看不到
// 行；开关翻转、升级、重启 agent 都不治本——agent 的转发服务活
// 在本机 gost.json 里，升级/重启只是重载同一份陈旧 gost.json；
// 面板只在开关翻转时走 repushNodeRuntimeAfterSettingChange 重推
// （异步，离线/nftables 节点静默跳过），且没有任何持久记录能
// 证明某次运行配置重推是否发生、是否成功。
//
// 本文件只补「看得见 + 能重推」两件事，不改采集机制本身：
//   - GET /api/v1/node/connlog-readiness：读面。agent 版本支
//     持度（>= 2.1.5 带 U1 hook）、节点开关、总开关、在线态、
//     转发模式，以及最近一条连接记录的时刻（agent 正在采集的
//     唯一面板侧实证）。
//   - POST /api/v1/node/connlog-repush：写面。同步调用与开关翻
//     转同一条重推漏斗（redeployLocalNodeRuntime，
//     upgrade.go:1469）并原样返回成功/失败，绝不静默 fire-and-
//     forget；成功后补一次与开关翻转同形的内核重下发
//     （reconcileNodeKernelsCollect），失败点名进 warning。
//
// 诚实口径——上次下发状态：全仓没有任何持久记录保存运行配置重
// 推的结果（RestartPushStatus/RestartPushError/RestartPushedAt
// 三列只记定时重启下发的尝试，与运行配置重推无关；重推本身只
// 有日志与内存里的重试队列）。readiness 的 lastPush 因此恒报
// unknown（source "none"），绝不拿重启下发的记录冒充运行配置
// 的下发记录，也不编造时间戳。readiness=="ready" 的唯一依据是
// 最近已上报的连接记录——那是 agent 正在采集的实证，而非一次
// 下发的回执。

// connlogAgentMinVersion 是连接日志 U1 hook 落地的首个 agent
// 版本：低于它 agent 根本不采集，再怎么重推也不会出记录。
const connlogAgentMinVersion = "2.1.5"

// connlogReadinessRowFreshMs 是“已生效”的实证窗口：最近一条
// 连接记录只在此时长内才算 agent 当前正在采集的证据——三天前
// 的一行证明不了今天的配置还在 agent 手里。超窗即 unknown
// （无法确认当前已生效），不冒充 ready；安静但健康的节点会
// 显示 unknown 而非误报 not_ready。
const connlogReadinessRowFreshMs = int64(24 * 60 * 60 * 1000)

// parseConnlogAgentVersion 把 agent 上报的版本串归一成严格
// semver（供 compareVersions）。上报串是 go-gost/main.go 的
// fullVersion 形，如 "2.1.8-cand (debian/amd64)" 或 "2.1.5"：
// 取首个空白前的段，剥可选 "v" 前缀，再剥 "-" / "+" 后的预发
// 布/构建后缀（"2.1.8-cand" 即 2.1.8 的候选版，比较时按
// 2.1.8 计）。空串、"dev"、无数字段等不可解析形态一律返回
// ok=false——调用方按 unknown 处理，绝不按支持处理。
func parseConnlogAgentVersion(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "dev" {
		return "", false
	}
	head := raw
	if i := strings.IndexByte(head, ' '); i >= 0 {
		head = head[:i]
	}
	head = strings.TrimPrefix(head, "v")
	if head == "" || head == "dev" {
		return "", false
	}
	if i := strings.IndexAny(head, "-+"); i >= 0 {
		head = head[:i]
	}
	parts, err := parseVersionParts(head)
	if err != nil {
		return "", false
	}
	segs := make([]string, len(parts))
	for i, n := range parts {
		segs[i] = strconv.Itoa(n)
	}
	return strings.Join(segs, "."), true
}

// connlogAgentSupport 判定 agent 版本对连接日志的支持度：
// supported（>= 2.1.5）/ unsupported（可解析但过旧）/
// unknown（缺失或不可解析——绝不按支持处理）。
func connlogAgentSupport(raw string) string {
	normalised, ok := parseConnlogAgentVersion(raw)
	if !ok {
		return "unknown"
	}
	cmp, err := compareVersions(normalised, connlogAgentMinVersion)
	if err != nil {
		return "unknown"
	}
	if cmp < 0 {
		return "unsupported"
	}
	return "supported"
}

// nodeConnlogReadiness 是节点连接日志就绪指示读面：
// GET /api/v1/node/connlog-readiness?nodeId=。管理员专属
// （ensureAdminAccess，与 nodeConnlog 同门）。
// 响应 data：
//   - nodeId / agentVersion（上报原串，缺失为空串）/
//     agentSupport（supported/unsupported/unknown）/
//     agentMinVersion（"2.1.5"）；
//   - masterEnabled（总开关）/ connLogEnabled（节点开关，
//     int 0/1 原样下发，前端须按 === 1 读）；
//   - online（node.Status==1）/ forwardMode；
//   - lastPush：{status:"unknown", pushedAt:null,
//     source:"none", note}——面板未持久化运行配置重推结果，
//     故恒为 unknown，见文件头诚实口径；
//   - lastRowAt：最近一条连接记录的 startTime（毫秒），无记
//     录为 null——agent 正在采集的唯一面板侧实证；
//   - readiness：ready（已开启且 24 小时内有记录上报，即
//     agent 当前已生效）/ off（总开关或节点开关未开）/
//     not_ready（有确定性阻断：nftables/版本过旧/离线）/
//     unknown（版本未知、无记录、或最近 24 小时无记录，无法
//     确认当前是否生效）；reason 为首要原因，reasons 列出全
//     部适用原因。
func (h *Handler) nodeConnlogReadiness(w http.ResponseWriter, r *http.Request) {
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
		log.Printf("connlog readiness query failed node_id=%d err=%v", nodeID, err)
		response.WriteJSON(w, response.ErrDefault("查询节点状态失败"))
		return
	}
	if node == nil {
		response.WriteJSON(w, response.ErrDefault("节点不存在"))
		return
	}
	masterOn, err := h.connlogMasterEnabled()
	if err != nil {
		log.Printf("connlog readiness master query failed node_id=%d err=%v", nodeID, err)
		response.WriteJSON(w, response.ErrDefault("查询连接日志状态失败"))
		return
	}
	agentVersion := ""
	if node.Version.Valid {
		agentVersion = node.Version.String
	}
	support := connlogAgentSupport(agentVersion)
	online := node.Status == 1
	forwardMode := defaultNodeForwardMode(node.ForwardMode)
	// 最近一条记录（start_time 倒序首行）：读错不拖垮主查询，
	// 按无记录处理并记日志（与 nodeConnlog 页脚 dropped 的回
	// 落口径同形）。
	var lastRowAt *int64
	if rows, rerr := h.repo.ListNodeConnLogs(nodeID, "", 0, 1); rerr != nil {
		log.Printf("connlog readiness last-row query failed node_id=%d err=%v (lastRowAt falls back to null)", nodeID, rerr)
	} else if len(rows) > 0 {
		at := rows[0].StartTime
		lastRowAt = &at
	}

	var reasons []string
	if !masterOn {
		reasons = append(reasons, "连接日志总开关未开启")
	}
	if node.ConnLogEnabled != 1 {
		reasons = append(reasons, "节点连接日志开关未开启")
	}
	if forwardMode == "nftables" {
		reasons = append(reasons, "nftables 模式无 agent 采集通道")
	}
	switch support {
	case "unsupported":
		reasons = append(reasons, "agent 版本过旧（当前 "+agentVersion+"，需 "+connlogAgentMinVersion+" 或更高）")
	case "unknown":
		reasons = append(reasons, "agent 版本未知，无法确认是否支持连接日志")
	}
	if !online {
		reasons = append(reasons, "节点离线：配置可能未下发，节点重连时将按最新配置自动重推")
	}
	readiness := "ready"
	reason := ""
	switch {
	case !masterOn || node.ConnLogEnabled != 1:
		readiness = "off"
		reason = reasons[0]
	case forwardMode == "nftables" || support == "unsupported" || !online:
		readiness = "not_ready"
		// 首要原因按阻断确定性排序：模式 > 版本 > 在线。
		switch {
		case forwardMode == "nftables":
			reason = "nftables 模式无 agent 采集通道"
		case support == "unsupported":
			reason = "agent 版本过旧（当前 " + agentVersion + "，需 " + connlogAgentMinVersion + " 或更高）"
		default:
			reason = "节点离线：配置可能未下发，节点重连时将按最新配置自动重推"
		}
	case support == "unknown":
		readiness = "unknown"
		reason = "agent 版本未知，无法确认是否支持连接日志"
	case lastRowAt == nil:
		readiness = "unknown"
		reason = "尚无连接记录，无法确认 agent 已生效：可能暂无客户端连入，也可能配置未生效，可点“重新下发配置”验证"
	case time.Now().UnixMilli()-*lastRowAt > connlogReadinessRowFreshMs:
		readiness = "unknown"
		reason = "最近 24 小时内无连接记录，无法确认 agent 当前已生效：可能暂无客户端连入，也可能配置未生效，可点“重新下发配置”验证"
	}
	if readiness == "ready" {
		reasons = nil
	}

	response.WriteJSON(w, response.OK(map[string]interface{}{
		"nodeId":          nodeID,
		"agentVersion":    agentVersion,
		"agentSupport":    support,
		"agentMinVersion": connlogAgentMinVersion,
		"masterEnabled":   masterOn,
		"connLogEnabled":  node.ConnLogEnabled,
		"online":          online,
		"forwardMode":     forwardMode,
		"lastPush": map[string]interface{}{
			"status":   "unknown",
			"pushedAt": nil,
			"source":   "none",
			"note":     "面板未持久化记录运行配置下发结果（定时重启下发的 RestartPushStatus 与本次重推无关），故上次下发状态未知；“已生效”以最近上报的连接记录为准",
		},
		"lastRowAt": lastRowAt,
		"readiness": readiness,
		"reason":    reason,
		"reasons":   reasons,
	}))
}

// nodeConnlogRepush 是节点运行配置一键重新下发：
// POST /api/v1/node/connlog-repush {"nodeId": N}。管理员专属。
// 与开关翻转同一条重推漏斗（redeployLocalNodeRuntime，
// upgrade.go:1469：按参与关系枚举本节点全部活跃隧道整隧道重
// 布 redeployTunnelAndForwards＋逐转发 syncForwardServices，
// 生成面按 DB 现值重算，含 connlog 入口标记），但与
// repushNodeRuntimeAfterSettingChange 的异步静默口径不同：
// 本端点同步等重推完成并原样返回成功/失败——重推返回 false
// 即回 code -1，绝不报成功。离线/nftables 节点在重推前即显式
// 拒绝（不碰 agent、不报成功，离线节点重连时 onNodeOnline
// 按 DB 新值自动重布）。成功后补一次与开关翻转同形的内核重
// 下发（reconcileNodeKernelsCollect），其失败只进 warning、
// 不翻转已成功的重推回执；无内核的节点此步为空操作。
// 注意：本端点不走 startNodeOnlineRedeploy 的 30 秒冷却门——
// 那是重连风暴的节流门，手动重推是用户显式动作，冷却内点第
// 二次也应如实执行一次（重推本身幂等，与并发的 onNodeOnline
// 重推重叠亦无害：同为 UpdateService 全量下发）。
func (h *Handler) nodeConnlogRepush(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	if !h.ensureAdminAccess(w, r) {
		return
	}
	var req struct {
		NodeID int64 `json:"nodeId"`
	}
	if err := decodeJSON(r.Body, &req); err != nil || req.NodeID <= 0 {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	node, err := h.repo.GetNodeByID(req.NodeID)
	if err != nil {
		log.Printf("connlog repush node lookup failed node_id=%d err=%v", req.NodeID, err)
		response.WriteJSON(w, response.ErrDefault("查询节点失败"))
		return
	}
	if node == nil {
		response.WriteJSON(w, response.ErrDefault("节点不存在"))
		return
	}
	if defaultNodeForwardMode(node.ForwardMode) == "nftables" {
		response.WriteJSON(w, response.ErrDefault("nftables 节点没有 agent 运行配置可下发"))
		return
	}
	if node.Status != 1 {
		response.WriteJSON(w, response.ErrDefault("节点离线：本次下发已跳过，未向节点发送任何配置；节点重连时将按最新配置自动重推"))
		return
	}
	// 计数只作回执富集（重推成败由 redeployLocalNodeRuntime 的
	// bool 定，不由计数定）：读错记日志、计数回 0，不挡重推、
	// 不冒充——此处须查错（R3-MR-01 登记门：读错静默吞即红）。
	tunnelIDs, err := h.repo.ListActiveTunnelIDsByNode(req.NodeID)
	if err != nil {
		log.Printf("connlog repush tunnel count query failed node_id=%d err=%v (tunnelCount falls back to 0)", req.NodeID, err)
		tunnelIDs = nil
	}
	forwardIDs, err := h.repo.ListForwardIDsByNode(req.NodeID)
	if err != nil {
		log.Printf("connlog repush forward count query failed node_id=%d err=%v (forwardCount falls back to 0)", req.NodeID, err)
		forwardIDs = nil
	}
	log.Printf("node connlog repush requested node_id=%d tunnels=%d forwards=%d operator_id=%d",
		req.NodeID, len(tunnelIDs), len(forwardIDs), idemUserID(r))
	if ok := h.redeployLocalNodeRuntime(req.NodeID); !ok {
		// 与 onNodeOnline 同口径：失败挂重试队列（30 秒后
		// retryNodeLocalRuntime 再试），但本次回执如实报失败。
		h.scheduleNodeLocalRuntimeRetry(req.NodeID)
		log.Printf("node connlog repush failed node_id=%d (runtime redeploy returned false; retry queued)", req.NodeID)
		response.WriteJSON(w, response.ErrDefault("重新下发失败：节点未确认全部配置，请检查节点在线状态与面板日志，稍后可重试"))
		return
	}
	h.clearNodeLocalRuntimeRetry(req.NodeID)
	log.Printf("node connlog repush succeeded node_id=%d tunnels=%d forwards=%d", req.NodeID, len(tunnelIDs), len(forwardIDs))
	failures := h.reconcileNodeKernelsCollect(req.NodeID)
	data := map[string]interface{}{
		"nodeId":       req.NodeID,
		"redeployed":   true,
		"tunnelCount":  len(tunnelIDs),
		"forwardCount": len(forwardIDs),
	}
	if note := connlogReconcileFailureNote(failures); note != "" {
		response.WriteJSON(w, response.OKWithWarning(data, "运行配置已下发；"+note))
		return
	}
	response.WriteJSON(w, response.OK(data))
}
