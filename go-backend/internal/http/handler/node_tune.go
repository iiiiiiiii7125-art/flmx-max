package handler

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"go-backend/internal/http/response"
)

const nodeTuneTimeout = 30 * time.Second

// nodeTune reads (action "status") or changes (action "apply" with the
// chosen groups, or "restore") a node's sysctl network tuning. The agent
// does the work; see go-gost/x/socket/tuning.go.
// J-005：handler 侧白名单校验（agent 侧已兜底返回"未知调优项"，此处前置拦截更早反馈）
var nodeTuneGroupAllowlist = map[string]bool{
	"bbr":     true, // BBR 拥塞控制
	"buffer":  true, // 按带宽和延迟设缓冲区
	"tcp":     true, // TCP 连接优化
	"backlog": true, // 高并发队列
	"forward": true, // IPv4 转发
	"files":   true, // 文件句柄上限
}

// bdpTuneMinAgentVersion 是首个支持按带宽/延迟算缓冲区的 agent 版本。
// 上游 3.3.42 的「agent ≥3.3.42」门按上游版本线算，与我方版本线不对齐，
// 不得照抄：此处按我方版本线重做——面板与 agent 同仓同版发布（与
// flmxNoPrivateMinAgentVersion 的 1.3.4 门同一口径），本功能随 1.4.5
// 首发，故门限为 1.4.5。若最终发版号变动，打 tag 前必须同步改此常量。
// 比较复用 compareWithReportedVersion（上报型 fullVersion 口径）：版本
// 未知、不可解析或为 dev 一律按不支持处理（fail-closed，见
// agentSupportsNoPrivateBypass 的同款注释）。
const bdpTuneMinAgentVersion = "1.4.5"

// agentSupportsBdpTuning 判断节点 agent 是否支持按带宽/延迟算缓冲区。
func agentSupportsBdpTuning(agentVersion string) bool {
	cmp, err := compareWithReportedVersion(bdpTuneMinAgentVersion, agentVersion)
	return err == nil && cmp <= 0
}

func (h *Handler) nodeTune(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req struct {
		ID     int64    `json:"id"`
		Action string   `json:"action"`
		Groups []string `json:"groups"`
		// The line the buffers are sized for (manual input, not a
		// measurement); 0/0 lets the agent use the last applied values.
		BandwidthMbps int `json:"bandwidthMbps"`
		RTTMs         int `json:"rttMs"`
	}
	if err := decodeJSON(r.Body, &req); err != nil || req.ID <= 0 {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	// 面板侧范围校验（与 agent 侧 valid() 对齐；只填其一也拒绝，
	// 避免半条线路参数被当成完整参数下发）。
	lineRequested := req.BandwidthMbps != 0 || req.RTTMs != 0
	if lineRequested && (req.BandwidthMbps < 1 || req.BandwidthMbps > 100000 || req.RTTMs < 1 || req.RTTMs > 2000) {
		response.WriteJSON(w, response.ErrDefault("带宽需在 1–100000 Mbps，延迟需在 1–2000 ms"))
		return
	}
	// 版本门：仅当 agent ≥1.4.5 才下发带宽/延迟。旧 agent 沿用自身
	// 固定值组（基线 64 MiB 缓冲区组），面板不下发 BDP 值——即使门
	// 失效（版本未知），旧 agent 侧 encoding/json 也会忽略未知字段，
	// 双层保障，见报告。版本读取失败按不支持处理。
	line := map[string]interface{}{}
	if lineRequested {
		var agentVersion string
		var versionErr error
		if h != nil && h.repo != nil {
			agentVersion, versionErr = h.repo.GetNodeAgentVersion(req.ID)
		}
		if versionErr == nil && agentSupportsBdpTuning(agentVersion) {
			line = map[string]interface{}{"bandwidthMbps": req.BandwidthMbps, "rttMs": req.RTTMs}
		}
	}

	command, data := "TuneStatus", map[string]interface{}{}
	for k, v := range line {
		data[k] = v
	}
	switch req.Action {
	case "", "status":
	case "apply":
		if len(req.Groups) == 0 {
			response.WriteJSON(w, response.ErrDefault("请至少选择一项"))
			return
		}
		// J-005：白名单校验，未知 group 直接拒绝（与 agent 侧 tuneGroups 对齐）
		for _, g := range req.Groups {
			if !nodeTuneGroupAllowlist[g] {
				response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("未知调优项：%s", g)))
				return
			}
		}
		command, data = "TuneApply", map[string]interface{}{"groups": req.Groups}
		for k, v := range line {
			data[k] = v
		}
	case "restore":
		command, data = "TuneApply", map[string]interface{}{"restore": true}
	default:
		response.WriteJSON(w, response.ErrDefault("未知操作"))
		return
	}

	res, err := h.sendNodeCommandWithTimeout(req.ID, command, data, nodeTuneTimeout, false, false)
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "未知命令类型") {
			msg = "节点 agent 版本过旧，不支持调优，请先升级节点"
		}
		response.WriteJSON(w, response.Err(-2, fmt.Sprintf("调优失败: %s", msg)))
		return
	}
	response.WriteJSON(w, response.OK(res.Data))
}
