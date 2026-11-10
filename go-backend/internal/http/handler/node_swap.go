package handler

import (
	"log"
	"net/http"
	"strings"
	"time"

	"go-backend/internal/http/response"
	"go-backend/internal/store/model"
)

// --- 节点一键设置 Swap ---
//
// 设计口径（用户 2026-11-10 亲令）：节点页每节点提供「设置
// Swap」——弹窗先拉现状（swap-info），用户选大小确认后下发执行
// （swap-set）。起因：存量升级上来的小内存节点无 swap，内存高峰
// 卡顿；装机脚本对 ≤512MB 机器自动建 swap，升级路径不补建，面板
// 此前也没有任何 swap 操作入口。
//
// 执行面在 agent：命令 SwapInfo / SwapSet 走现有节点命令通道
// （go-gost/x/socket/swap.go）。agent 只管理 /swapfile——既存的
// 分区/他文件 swap 只报告不动；SwapSet 含磁盘容量校验与 swapoff
// 压死风险拒绝，失败尽力恢复执行前状态，错误文案是给用户看的
// 处置信息，面板原文透传（与仓储/内部错误区别对待：后者走
// respondInternalError，不许 err 原文直出）。
//
// nftables 节点没有 agent 服务，在两个接口都点名拒绝（与定时
// 重启口径一致）。超时三层：SwapInfo 只读快查给 30s；SwapSet 要
// 在慢盘上 fallocate/dd 写满数 GB、mkswap、swapon，给 10 分钟命令
// 界（登记于 doc/audit/timeout-budgets.tsv P006），写截止由
// nodeSwapSetWriteDeadline 在其外层加余量。

const (
	nodeSwapInfoCommand = "SwapInfo"
	nodeSwapSetCommand  = "SwapSet"

	nodeSwapInfoTimeout = 30 * time.Second
	nodeSwapSetTimeout  = 10 * time.Minute

	nodeSwapMinSizeGB = 1
	nodeSwapMaxSizeGB = 32
)

// nodeSwapLookup 取节点并做两个 swap 接口共用的前置判定：仓储
// 错误走内部错误口径，节点不存在与 nftables 无 agent 分别点名。
// handled=true 时回执已写，调用方直接返回。
func (h *Handler) nodeSwapLookup(w http.ResponseWriter, nodeID int64) (node *model.Node, handled bool) {
	node, repoErr := h.repo.GetNodeByID(nodeID)
	if repoErr != nil {
		respondInternalError(w, "nodeSwap lookup", repoErr)
		return nil, true
	}
	if node == nil {
		response.WriteJSON(w, response.ErrDefault("节点不存在"))
		return nil, true
	}
	if defaultNodeForwardMode(node.ForwardMode) == "nftables" {
		response.WriteJSON(w, response.ErrDefault("nftables 节点没有 agent 服务，不支持设置 Swap"))
		return nil, true
	}
	return node, false
}

// nodeSwapCommandError 把命令腿错误翻成回执：agent 版本过旧（命令
// 未知）给升级指引；其余（节点不在线、等待超时、agent 结构化业务
// 错误如空间不足/风险拒绝）原文透传——这些文案是给用户看的处置
// 信息，不是内部错误。
func nodeSwapCommandError(w http.ResponseWriter, cmdErr error) {
	msg := cmdErr.Error()
	if strings.Contains(msg, "未知命令类型") {
		msg = "节点 agent 版本过旧，不支持一键设置 Swap，请先升级节点 Agent"
	}
	response.WriteJSON(w, response.Err(-2, msg))
}

// nodeSwapInfo 回节点当前 swap 现状（全机口径总量＋逐条目＋磁盘
// 可用＋内存总量＋托管文件在场与否），agent 数据原样透传。
// POST /api/v1/node/swap-info {nodeId}
func (h *Handler) nodeSwapInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req struct {
		NodeID int64 `json:"nodeId"`
	}
	if err := decodeJSON(r.Body, &req); err != nil || req.NodeID <= 0 {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	if _, handled := h.nodeSwapLookup(w, req.NodeID); handled {
		return
	}
	res, cmdErr := h.sendNodeCommandWithTimeout(req.NodeID, nodeSwapInfoCommand, nil, nodeSwapInfoTimeout, false, false)
	if cmdErr != nil {
		nodeSwapCommandError(w, cmdErr)
		return
	}
	response.WriteJSON(w, response.OK(res.Data))
}

// nodeSwapSet 下发 SwapSet 在节点上建/改 /swapfile 大小并收敛
// fstab，回 agent 的 before/after 对照原样透传。面板先判大小
// 1–32GB，agent 再复判。执行留痕（与本仓设置面同口径）。
// POST /api/v1/node/swap-set {nodeId, sizeGB}
func (h *Handler) nodeSwapSet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	extendWriteDeadline(w, nodeSwapSetWriteDeadline)
	var req struct {
		NodeID int64 `json:"nodeId"`
		SizeGB int   `json:"sizeGB"`
	}
	if err := decodeJSON(r.Body, &req); err != nil || req.NodeID <= 0 {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	if req.SizeGB < nodeSwapMinSizeGB || req.SizeGB > nodeSwapMaxSizeGB {
		response.WriteJSON(w, response.ErrDefault("Swap 大小须在 1–32 GB 之间"))
		return
	}
	if _, handled := h.nodeSwapLookup(w, req.NodeID); handled {
		return
	}
	log.Printf("node swap set requested node_id=%d size_gb=%d operator_id=%d",
		req.NodeID, req.SizeGB, idemUserID(r))
	res, cmdErr := h.sendNodeCommandWithTimeout(req.NodeID, nodeSwapSetCommand,
		map[string]interface{}{"sizeGB": req.SizeGB}, nodeSwapSetTimeout, false, false)
	if cmdErr != nil {
		nodeSwapCommandError(w, cmdErr)
		return
	}
	response.WriteJSON(w, response.OK(res.Data))
}
