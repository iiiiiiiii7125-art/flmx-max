package handler

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"go-backend/internal/http/response"
)

const nodeTuneTimeout = 30 * time.Second

// nodeTuneProbeTimeout is the panel->agent timeout for probe only.
// Frontend allows 120s for the probe call; the agent's own caps bound
// a real probe to ~25s worst case (10s speed test + 3x2s reference
// dials + 5s sock_diag read timeout + overhead), so 90s is headroom
// for loaded nodes / WS queueing, not the binding constraint, and it
// stays below the frontend 120s so the frontend never cuts first:
// agent caps (~25s) < panel probe (90s) < frontend (120s).
// Status/apply/restore keep the 30s B2 timeout.
const nodeTuneProbeTimeout = 90 * time.Second

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

// 能力探测（主门，照搬上游 3.3.42 机制，不比较 agent 版本号）：
// 面板以 agent 的 TuneStatus 回包有无 params 判定是否支持按带宽/延迟算
// 缓冲区，与上游前端 lineSupported = Boolean(status?.params) 同款语义。
// 旧 agent 回包天然没有 params 字段 → 不支持，走自身固定值组；新 agent
// 回包带 params → 支持。面板收到回包后把判定结果以 lineSupported 打在
// data 上（仅派生标注，不改 agent 原值），供前端直接用；前端也可自行
// 按 Boolean(status?.params) 再算一次，两者语义一致。
// 1.4.7 只是文档门号（首个提供该能力的我方版本，仅用于提示文案），
// 不参与是否下发的判断：面板不以版本号硬拦截任何调优请求。
// 第二层兜底：旧 agent 对未知 JSON 字段天然忽略（Go encoding/json
// 默认丢弃未知字段），即使带宽/延迟被透传给旧 agent 也不会出错。
func tuneLineSupported(data map[string]interface{}) bool {
	if data == nil {
		return false
	}
	v, ok := data["params"]
	return ok && v != nil
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
		// Probe options (action "probe" only): withSpeed is the
		// contract field (frontend: {action:"probe", withSpeed}) that
		// triggers the agent's short speed test, off by default;
		// measureBandwidth is the same switch under its earlier name
		// and is accepted as an alias. speedtestURL is the download
		// source it measures against, referenceHost is an optional
		// host:port for a TCP-connect RTT reference value (the agent
		// auto-fills the panel address from its WS connection when
		// absent, so the frontend needs no host field).
		WithSpeed        bool   `json:"withSpeed"`
		MeasureBandwidth bool   `json:"measureBandwidth"`
		SpeedtestURL     string `json:"speedtestURL"`
		ReferenceHost    string `json:"referenceHost"`
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
	// 透传，不做版本门：带宽/延迟只要请求就原样下发。旧 agent 忽略
	// 未知字段走固定值，新 agent 按线路算缓冲区；是否支持由调用方
	// 以 status 回包的 params 有无判定（见 tuneLineSupported），
	// 不以 agent 版本号比较决定——硬版本门会把旧 agent 的全部调优
	// 能力一并关掉，比上游降级更差，故不采纳。
	line := map[string]interface{}{}
	if lineRequested {
		line = map[string]interface{}{"bandwidthMbps": req.BandwidthMbps, "rttMs": req.RTTMs}
	}

	command, data := "TuneStatus", map[string]interface{}{}
	for k, v := range line {
		data[k] = v
	}
	switch req.Action {
	case "", "status":
	case "probe":
		// 一键测算探测：只读建议值，不写盘（agent 侧 TuneProbe 与
		// Apply 写盘路径完全分开）。测速地址只认 http(s)，其余
		// scheme 在面板侧先拦，agent 侧再拦一层。
		if req.SpeedtestURL != "" && !strings.HasPrefix(req.SpeedtestURL, "http://") && !strings.HasPrefix(req.SpeedtestURL, "https://") {
			response.WriteJSON(w, response.ErrDefault("测速地址须为 http(s) 链接"))
			return
		}
		if req.ReferenceHost != "" {
			if _, _, err := net.SplitHostPort(req.ReferenceHost); err != nil {
				response.WriteJSON(w, response.ErrDefault("参考地址须为 host:port 格式"))
				return
			}
		}
		command, data = "TuneProbe", map[string]interface{}{"withSpeed": req.WithSpeed || req.MeasureBandwidth}
		if req.SpeedtestURL != "" {
			data["speedtestURL"] = req.SpeedtestURL
		}
		if req.ReferenceHost != "" {
			data["referenceHost"] = req.ReferenceHost
		}
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

	timeout := nodeTuneTimeout
	if req.Action == "probe" {
		timeout = nodeTuneProbeTimeout
	}
	res, err := h.sendNodeCommandWithTimeout(req.ID, command, data, timeout, false, false)
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "未知命令类型") {
			if req.Action == "probe" {
				// 探测是可选能力：旧 agent 不认 TuneProbe 时只提示，
				// 不拦调优本身；用户仍可手动填带宽/延迟后 Apply。
				// 1.4.7 为文档门号（首个提供探测的版本），仅作提示。
				msg = "节点 agent 较旧，不支持一键测算（按带宽/延迟计算自 1.4.7 起提供），请手动填写带宽/延迟"
			} else {
				msg = "节点 agent 版本过旧，不支持调优，请先升级节点"
			}
		}
		response.WriteJSON(w, response.Err(-2, fmt.Sprintf("调优失败: %s", msg)))
		return
	}
	// 能力探测标注：TuneStatus/Apply/Restore 回包是调优状态形，面板
	// 据 params 有无打 lineSupported；TuneProbe 回包是探测形（无
	// params 键），不打标，避免把“探测结果”误读成“不支持”。
	if req.Action != "probe" && res.Data != nil {
		res.Data["lineSupported"] = tuneLineSupported(res.Data)
	}
	response.WriteJSON(w, response.OK(res.Data))
}
