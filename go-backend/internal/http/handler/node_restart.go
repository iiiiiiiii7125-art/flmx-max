package handler

import (
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"go-backend/internal/http/response"
)

// --- 节点定时重启 agent 服务（保险丝） ---
//
// 设计口径（用户 2026-11-08 拍板）：
//   - 重启对象是节点上的 agent 服务（flmx_agent systemd 服务），
//     不是整机重启；
//   - 面板按节点提供开关＋时刻＋随机抖动，是设置的权威源；
//   - agent 收到设置后落成 systemd timer 持久化，面板离线照常
//     触发，代码侧不依赖面板心跳；
//   - 默认关（新列默认值 restart_enabled=0）。
//
// 下发面与 SetProtocol / Tune 同构：sendNodeCommand 发送
// "SetRestartSchedule" 命令，载荷 {enabled, time, jitterMinutes}
// （interval 模式再加 {mode, intervalMinutes}；daily 模式载荷与
// 引入模式前逐字一致），agent 侧再校验一遍同样的边界（面板判＋
// agent 复判）。节点离线时
// 设置只落库，节点重连走 onNodeOnline 的 pushNodeRestartSchedule
// 补发——所以离线改设置不拒绝、不丢失，与协议三开关的
// R4B03-002 口径不同：那三项会被 agent 握手值覆回，本设置在
// agent 侧没有反向覆写面，面板恒为权威源。
//
// 远端（联邦）节点与 nftables 节点不适用：前者的 agent 归所属
// 面板管理，后者没有 agent 服务，两者均在写面点名拒绝。

const (
	restartScheduleDefaultTime   = "04:00"
	restartScheduleDefaultJitter = 30
	restartScheduleMaxJitterMins = 720 // 12h：抖动窗口上限
	restartScheduleCommand       = "SetRestartSchedule"
	restartSchedulePushTimeout   = 30 * time.Second
)

// 重启模式（fix/restart-interval）：daily 为存量每天定点模式；
// interval 为每 N 分钟一次的间隔模式（小内存节点攒积保险丝，
// 用户 2026-11-12 令）。间隔下限 15 分钟防误设成重启风暴，上限
// 10080 分钟（一周）与 daily 的语义边界对齐。
const (
	restartModeDaily              = "daily"
	restartModeInterval           = "interval"
	restartIntervalMinMinutes     = 15
	restartIntervalMaxMinutes     = 10080
	restartIntervalDefaultMinutes = 60
)

// normalizeRestartMode maps the wire/stored mode onto the legal
// set: empty means a pre-mode record and reads as daily.
func normalizeRestartMode(mode string) string {
	if mode == restartModeInterval {
		return restartModeInterval
	}
	return restartModeDaily
}

// 定时重启下发结果持久状态（W8-R-C601）：pending 已保存未下发、
// applied agent 已确认、failed 下发失败（原因见 RestartPushError）。
const (
	restartPushPending = "pending"
	restartPushApplied = "applied"
	restartPushFailed  = "failed"
)

// recordRestartPushResult 落一次下发尝试的结果（尽力而为：落库
// 失败只记日志，不挡回执）。errMsg 按列宽 500 截断（PG varchar
// 上限），空原因的失败不落地（调用方须给原因）。
func (h *Handler) recordRestartPushResult(nodeID int64, status, errMsg string) {
	if h == nil || h.repo == nil || nodeID <= 0 {
		return
	}
	if r := []rune(errMsg); len(r) > 500 {
		errMsg = string(r[:500])
	}
	if err := h.repo.SetNodeRestartPushResult(nodeID, status, errMsg, time.Now().UnixMilli()); err != nil {
		log.Printf("node restart schedule push result persist failed node_id=%d status=%s err=%v", nodeID, status, err)
	}
}

// restartPushDeterministicFailure 判下发失败是否确定性失败（重连
// 补发亦然）：agent 明确回「未知命令类型」（版本过旧）或「无
// systemd」（容器形态无持久化能力）时，重连不会改变结果，不得
// 再承诺「节点重连时自动补发」。
func restartPushDeterministicFailure(msg string) bool {
	return strings.Contains(msg, "未知命令类型") || strings.Contains(msg, "无 systemd")
}

var restartTimePattern = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// validateRestartSchedule judges the full setting. The agent
// re-validates the identical rules before writing any unit file,
// so a forged or stale payload can never widen them. The time is
// validated in both modes so the stored setting stays well-formed
// whichever mode is active; the interval is judged only in
// interval mode.
func validateRestartSchedule(enabled int, mode string, timeStr string, jitterMinutes int, intervalMinutes int) error {
	if enabled != 0 && enabled != 1 {
		return fmt.Errorf("定时重启开关值无效")
	}
	if mode != restartModeDaily && mode != restartModeInterval {
		return fmt.Errorf("重启模式须为 daily（每天定时）或 interval（按间隔）")
	}
	if !restartTimePattern.MatchString(timeStr) {
		return fmt.Errorf("重启时刻须为 HH:MM 格式（00:00–23:59）")
	}
	if jitterMinutes < 0 || jitterMinutes > restartScheduleMaxJitterMins {
		return fmt.Errorf("随机抖动须在 0–%d 分钟之间", restartScheduleMaxJitterMins)
	}
	if mode == restartModeInterval &&
		(intervalMinutes < restartIntervalMinMinutes || intervalMinutes > restartIntervalMaxMinutes) {
		return fmt.Errorf("重启间隔须在 %d–%d 分钟之间", restartIntervalMinMinutes, restartIntervalMaxMinutes)
	}
	return nil
}

// restartSchedulePayload renders the wire payload for one node's
// stored setting. Shared by the save path and the reconnect re-push
// so the two can never drift apart. Daily mode renders exactly the
// pre-mode payload {enabled, time, jitterMinutes}; interval mode
// adds {mode, intervalMinutes} so an agent that predates modes
// still sees a well-formed daily-shaped setting it re-validates.
func restartSchedulePayload(enabled int, mode string, timeStr string, jitterMinutes int, intervalMinutes int) map[string]interface{} {
	payload := map[string]interface{}{
		"enabled":       enabled == 1,
		"time":          timeStr,
		"jitterMinutes": jitterMinutes,
	}
	if normalizeRestartMode(mode) == restartModeInterval {
		payload["mode"] = restartModeInterval
		payload["intervalMinutes"] = intervalMinutes
	}
	return payload
}

// pushNodeRestartSchedule sends the node's stored setting to its
// agent. It returns (true, nil) only when the agent acknowledged.
// nftables nodes are skipped silently (false, nil): they have no
// agent service for this panel to configure. (The remote-node skip
// from the original implementation is gone with DEV-RM-SHARE: the
// federation removal deletes remote nodes and the IsRemote column,
// so every remaining node is this panel's own.)
func (h *Handler) pushNodeRestartSchedule(nodeID int64) (bool, error) {
	node, err := h.repo.GetNodeByID(nodeID)
	if err != nil || node == nil {
		return false, err
	}
	if defaultNodeForwardMode(node.ForwardMode) == "nftables" {
		return false, nil
	}
	// 下发超时 30s：agent 侧要跑 daemon-reload + enable --now，
	// 在慢节点上可能超过默认命令超时（6s）；与 agent 侧 apply 的
	// 30s 上下文对齐，前端 60s 在其外层，排序不倒挂。
	if _, err := h.sendNodeCommandWithTimeout(nodeID, restartScheduleCommand,
		restartSchedulePayload(node.RestartEnabled, node.RestartMode, node.RestartTime, node.RestartJitterMinutes, node.RestartIntervalMinutes),
		restartSchedulePushTimeout, false, false); err != nil {
		return false, err
	}
	return true, nil
}

// nodeRestartScheduleSet stores one node's scheduled agent-restart
// setting and pushes it to the agent when the node is online.
// POST /api/v1/node/restart-schedule {id, enabled, time, jitterMinutes, mode, intervalMinutes}
// mode/intervalMinutes are optional on the wire: a pre-mode client
// sends neither and gets the daily behavior unchanged.
func (h *Handler) nodeRestartScheduleSet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req struct {
		ID              int64  `json:"id"`
		Enabled         int    `json:"enabled"`
		Time            string `json:"time"`
		JitterMinutes   int    `json:"jitterMinutes"`
		Mode            string `json:"mode"`
		IntervalMinutes int    `json:"intervalMinutes"`
	}
	if err := decodeJSON(r.Body, &req); err != nil || req.ID <= 0 {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	req.Time = strings.TrimSpace(req.Time)
	if req.Time == "" {
		req.Time = restartScheduleDefaultTime
	}
	// 只有空 mode（引入模式前的旧客户端）按 daily 回落；其余未知值
	// 由 validateRestartSchedule 点名拒绝，不静默改义。
	req.Mode = strings.TrimSpace(req.Mode)
	if req.Mode == "" {
		req.Mode = restartModeDaily
	}
	if req.IntervalMinutes == 0 {
		req.IntervalMinutes = restartIntervalDefaultMinutes
	}
	if err := validateRestartSchedule(req.Enabled, req.Mode, req.Time, req.JitterMinutes, req.IntervalMinutes); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	node, err := h.repo.GetNodeByID(req.ID)
	if err != nil || node == nil {
		response.WriteJSON(w, response.ErrDefault("节点不存在"))
		return
	}
	if defaultNodeForwardMode(node.ForwardMode) == "nftables" {
		response.WriteJSON(w, response.ErrDefault("nftables 节点没有 agent 服务，不支持定时重启"))
		return
	}
	if err := h.repo.SetNodeRestartSchedule(req.ID, req.Enabled, req.Time, req.JitterMinutes, req.Mode, req.IntervalMinutes); err != nil {
		respondInternalError(w, "nodeRestartScheduleSet save", err)
		return
	}
	// 设置变更留痕（与本仓设置面同一口径：结构化 log 行，含操作者）。
	log.Printf("node restart schedule updated node_id=%d enabled=%d mode=%s time=%s jitter_minutes=%d interval_minutes=%d operator_id=%d",
		req.ID, req.Enabled, req.Mode, req.Time, req.JitterMinutes, req.IntervalMinutes, idemUserID(r))

	data := map[string]interface{}{
		"enabled":         req.Enabled,
		"time":            req.Time,
		"jitterMinutes":   req.JitterMinutes,
		"mode":            req.Mode,
		"intervalMinutes": req.IntervalMinutes,
		"pushed":          false,
	}
	if node.Status != 1 {
		// 离线：已落库，onNodeOnline 重连时补发（见 upgrade.go）。
		// W8-R-C601：下发结果持久化——离线保存记 pending，刷新
		// 列表行可见，不再只靠当次回执。
		h.recordRestartPushResult(req.ID, restartPushPending, "")
		data["pushStatus"] = restartPushPending
		response.WriteJSON(w, response.OK(data))
		return
	}
	pushed, perr := h.pushNodeRestartSchedule(req.ID)
	if perr != nil {
		msg := perr.Error()
		// W8-R-C601：确定性失败（版本过旧／无 systemd）不得再承
		// 诺「节点重连时自动补发」——重连亦然；瞬时失败才保留该
		// 承诺（onNodeOnline 确会补发）。结果一律持久化。
		h.recordRestartPushResult(req.ID, restartPushFailed, msg)
		data["pushStatus"] = restartPushFailed
		if strings.Contains(msg, "未知命令类型") {
			msg = "节点 agent 版本过旧，不支持定时重启；设置已保存，升级 agent 后将自动生效"
		} else if restartPushDeterministicFailure(msg) {
			msg = "设置已保存，但节点无法持久化定时重启（节点无 systemd，容器形态不支持）；重连不会改变结果"
		} else {
			msg = "设置已保存，但下发 agent 失败（节点重连时自动补发）: " + msg
		}
		data["pushError"] = msg
		response.WriteJSON(w, response.OK(data))
		return
	}
	h.recordRestartPushResult(req.ID, restartPushApplied, "")
	data["pushStatus"] = restartPushApplied
	data["pushed"] = pushed
	response.WriteJSON(w, response.OK(data))
}
