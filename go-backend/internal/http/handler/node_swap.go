package handler

import (
	"fmt"
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
// nodeSwapSetWriteDeadline 在其外层加余量。OPEN-7：SwapSet 的 10
// 分钟只是面板等回执的预算——agent 侧 SwapSet 走 mutation 队列、
// 用进程级 ctx，不设超时，面板停止等待后 agent 仍会执行到底；
// 故等回执超时后立即再拉一次 SwapInfo（30s，600+30=630 仍在 660s
// 写截止内）按复查到的实际状态回执，而不是只报超时（见
// reconcileSwapSetTimeout，F2-APPLY-01 同款「超时后核实是否落地」）。

const (
	nodeSwapInfoCommand = "SwapInfo"
	nodeSwapSetCommand  = "SwapSet"

	nodeSwapInfoTimeout = 30 * time.Second
	nodeSwapSetTimeout  = 10 * time.Minute

	nodeSwapMinSizeGB = 1
	nodeSwapMaxSizeGB = 32

	// nodeSwapReserveMB 是目标大小之外的磁盘预留，与 agent
	// swapReserveMB 同值（R18-D2-01-mirror）。
	nodeSwapReserveMB = 256
)

// checkNodeSwapCapacity 是面板侧对 agent checkSwapCapacity
// （go-gost/x/socket/swap.go）的逐字镜像（R18-D2-01-mirror，
// R3-NODE-01）：只认真实磁盘剩余。重建时旧文件只是改名备份、仍占
// 盘，新文件再整块 fallocate，峰值占用是旧+新——旧文件占量绝不能
// 计入可用；existingFileMB 只用于报错文案，不参与可用计算。
//
// 本函数是口径钉子：前端预检与回归测试凭它共用同一组边界数字，
// 执行面的权威校验仍在 agent。nodeSwapSet 下发时不另取 SwapInfo
// 做前置拦截——多一次现状往返会让现状拉取失败时本可执行的设置
// 也被拒，存量用法不断链优先于面板侧二次拦截；agent 复判失败时
// 回执原文透传。
func checkNodeSwapCapacity(diskFreeMB int64, existingFileMB int64, sizeGB int) error {
	needMB := int64(sizeGB)*1024 + nodeSwapReserveMB
	availMB := diskFreeMB
	if availMB < needMB {
		return fmt.Errorf("磁盘空间不足：设置 %d GB Swap 需要至少 %d MB 磁盘剩余（目标 %d MB＋预留 %d MB），当前磁盘剩余 %d MB（现有 /swapfile 占用 %d MB 在重建期间仍占盘，不计入可用）",
			sizeGB, needMB, int64(sizeGB)*1024, nodeSwapReserveMB, availMB, existingFileMB)
	}
	return nil
}

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

// nodeSwapCommandMessage 把命令腿错误翻成给用户看的回执文案：agent
// 版本过旧（命令未知）给升级指引；其余（节点不在线、等待超时、agent
// 结构化业务错误如空间不足/风险拒绝）原文透传——这些文案是给用户看的处置
// 信息，不是内部错误。
func nodeSwapCommandMessage(cmdErr error) string {
	msg := cmdErr.Error()
	if strings.Contains(msg, "未知命令类型") {
		msg = "节点 agent 版本过旧，不支持一键设置 Swap，请先升级节点 Agent"
	}
	return msg
}

// nodeSwapCommandError 把命令腿错误翻成回执（文案见 nodeSwapCommandMessage）。
func nodeSwapCommandError(w http.ResponseWriter, cmdErr error) {
	response.WriteJSON(w, response.Err(-2, nodeSwapCommandMessage(cmdErr)))
}

// isNodeSwapSetTimeoutError 判断 SwapSet 错误是否为「等回执超时」：
// SendCommand 的超时文本是「等待节点响应超时」（与 kernel.go 的
// isKernelApplyTimeoutError 同口径）。只有这个错误才触发超时后复查——
// 命令本身已发出、agent 侧仍在执行，超时不等于失败；其他错误
// （不在线、agent 业务拒绝等）是 agent 或通道自己的 verdict，
// 复查只会读到同一个失败状态，不复查、原文回执。
func isNodeSwapSetTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "等待节点响应超时")
}

// nodeSwapReconcileInfoTimeout 是超时后复查腿（SwapInfo）的预算。
// 包级变量只为测试注入短预算（D2-02 同款包级变量先例）；生产值
// 即 nodeSwapInfoTimeout。
var nodeSwapReconcileInfoTimeout = nodeSwapInfoTimeout

// swapSetReconcileState 是超时后复查读到的最小状态子集：只取判断
// 「是否已按目标启用」所需的字段，其余现状只进回执文案。
type swapSetReconcileState struct {
	totalMB       int64
	fileSizeMB    int64
	managedActive bool
}

func swapInfoNumber(m map[string]interface{}, key string) int64 {
	if v, ok := m[key].(float64); ok {
		return int64(v)
	}
	return 0
}

// parseSwapSetReconcileState 从 SwapInfo 的 data（map 口径）里解析
// 复查状态：缺字段一律按零值读——缺证据时绝不能判「已完成」
// （fail-closed：完成须有文件尺寸＋在用条目双重实证）。
func parseSwapSetReconcileState(data map[string]interface{}) swapSetReconcileState {
	var st swapSetReconcileState
	if data == nil {
		return st
	}
	st.totalMB = swapInfoNumber(data, "totalMB")
	st.fileSizeMB = swapInfoNumber(data, "fileSizeMB")
	if entries, ok := data["entries"].([]interface{}); ok {
		for _, e := range entries {
			em, ok := e.(map[string]interface{})
			if !ok {
				continue
			}
			if name, _ := em["name"].(string); name == "/swapfile" {
				st.managedActive = true
				break
			}
		}
	}
	return st
}

// reconcileSwapSetTimeout 在 SwapSet 等回执超时后复查节点实际状态
// （OPEN-7，F2-APPLY-01 同款「超时后核实是否落地」）：SwapSet 走
// agent mutation 队列、用进程级 ctx，面板 600s 停止等待后 agent
// 仍会执行到底——超时回执丢了，但设置可能已完成、仍在执行或已
// 恢复原状；只报「等待节点响应超时」会让用户把已生效的设置当成
// 失败重做一次（重复 swapoff/重建）。复查腿是 SwapInfo（30s，
// 600+30=630 仍在 660s 写截止内），只读现状、与在途 SwapSet 无锁
// 冲突。返回值是 HTTP 回执二元组（code 0 不出现：复查拿不到
// before/after 对照，成功态也按 -2 回执、文案写明已核实的实际
// 状态——前端 code 0 要求 before/after 齐全，见 R3-NODE-02，
// 绝不为凑形状编造 before）。
func (h *Handler) reconcileSwapSetTimeout(nodeID int64, sizeGB int) (code int, msg string) {
	targetMB := int64(sizeGB) * 1024
	res, cmdErr := h.sendNodeCommandWithTimeout(nodeID, nodeSwapInfoCommand, nil, nodeSwapReconcileInfoTimeout, false, false)
	if cmdErr != nil {
		return -2, fmt.Sprintf("设置 Swap 等待超时，复查节点现状失败（%s），实际状态未知，请稍后用 SwapInfo 刷新确认", nodeSwapCommandMessage(cmdErr))
	}
	st := parseSwapSetReconcileState(res.Data)
	if st.managedActive && st.fileSizeMB == targetMB {
		return -2, fmt.Sprintf("设置 Swap 已完成（复查确认）：/swapfile 已按 %d GB 启用，全机 Swap 总量 %d MB；本次回执等待超时——agent 在面板停止等待后继续执行并完成了设置，超时不代表失败。请刷新现状确认", sizeGB, st.totalMB)
	}
	if st.fileSizeMB == targetMB {
		// 文件已建到目标大小、但 /proc/swaps 里还没有它：在 fallocate/dd
		// 与 swapon 之间的收尾窗口，或 swapon 刚失败、agent 还没恢复。
		return -2, fmt.Sprintf("设置 Swap 等待超时，复查节点现状：/swapfile 文件已建到 %d GB、但尚未启用（可能仍在执行收尾），请稍后用 SwapInfo 刷新确认；当前全机 Swap 总量 %d MB", sizeGB, st.totalMB)
	}
	return -2, fmt.Sprintf("设置 Swap 等待超时，复查节点现状：当前 /swapfile 为 %d MB，未达到目标 %d GB（可能仍在执行，或已恢复原状），请稍后用 SwapInfo 刷新确认；当前全机 Swap 总量 %d MB", st.fileSizeMB, sizeGB, st.totalMB)
}

// executeNodeSwapSet 下发 SwapSet 并返回 HTTP 回执三元组
// （code/msg/data）：成功原样透传 agent 的 before/after；非超时
// 错误按 nodeSwapCommandMessage 回执；等回执超时则走
// reconcileSwapSetTimeout 按复查到的实际状态回执。timeout 参数
// 只为测试注入短预算，生产调用方一律传 nodeSwapSetTimeout。
func (h *Handler) executeNodeSwapSet(nodeID int64, sizeGB int, timeout time.Duration) (code int, msg string, data map[string]interface{}) {
	res, cmdErr := h.sendNodeCommandWithTimeout(nodeID, nodeSwapSetCommand,
		map[string]interface{}{"sizeGB": sizeGB}, timeout, false, false)
	if cmdErr == nil {
		return 0, "", res.Data
	}
	if isNodeSwapSetTimeoutError(cmdErr) {
		code, msg := h.reconcileSwapSetTimeout(nodeID, sizeGB)
		return code, msg, nil
	}
	return -2, nodeSwapCommandMessage(cmdErr), nil
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
	code, msg, data := h.executeNodeSwapSet(req.NodeID, req.SizeGB, nodeSwapSetTimeout)
	if code != 0 {
		response.WriteJSON(w, response.Err(code, msg))
		return
	}
	response.WriteJSON(w, response.OK(data))
}
