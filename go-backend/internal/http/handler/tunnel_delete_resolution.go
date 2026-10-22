package handler

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"go-backend/internal/http/response"
	"go-backend/internal/store/model"
)

const tunnelDeletePreviewSampleLimit = 5

const (
	tunnelDeleteActionReplace        = "replace"
	tunnelDeleteActionDeleteForwards = "delete_forwards"
)

var (
	errInvalidTunnelDeleteTarget = errors.New("invalid tunnel delete target")
)

type tunnelDeleteForwardPreviewItem struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	UserID   int64  `json:"userId"`
	UserName string `json:"userName"`
	InPort   int    `json:"inPort"`
}

type tunnelDeletePreviewData struct {
	TunnelID       int64                            `json:"tunnelId"`
	TunnelName     string                           `json:"tunnelName"`
	ForwardCount   int                              `json:"forwardCount"`
	SampleForwards []tunnelDeleteForwardPreviewItem `json:"sampleForwards"`
}

type tunnelBatchDeletePreviewData struct {
	TunnelCount       int                       `json:"tunnelCount"`
	TotalForwardCount int                       `json:"totalForwardCount"`
	Items             []tunnelDeletePreviewData `json:"items"`
}

type tunnelDeleteWithForwardsRequest struct {
	ID             int64  `json:"id"`
	Action         string `json:"action"`
	TargetTunnelID int64  `json:"targetTunnelId"`
}

type tunnelBatchDeleteWithForwardsRequest struct {
	IDs            []int64 `json:"ids"`
	Action         string  `json:"action"`
	TargetTunnelID int64   `json:"targetTunnelId"`
}

type tunnelDeleteWithForwardsResult struct {
	ForwardCount        int      `json:"forwardCount"`
	MigratedCount       int      `json:"migratedCount"`
	DeletedForwardCount int      `json:"deletedForwardCount"`
	PortAdjustedCount   int      `json:"portAdjustedCount"`
	Warnings            []string `json:"warnings,omitempty"`
}

type tunnelBatchDeleteWithForwardsResult struct {
	SuccessCount        int                  `json:"successCount"`
	FailCount           int                  `json:"failCount"`
	Failures            []batchFailureDetail `json:"failures,omitempty"`
	DeletedForwardCount int                  `json:"deletedForwardCount"`
	MigratedCount       int                  `json:"migratedCount"`
	PortAdjustedCount   int                  `json:"portAdjustedCount"`
	Warnings            []string             `json:"warnings,omitempty"`
}

type tunnelForwardMigrationPlan struct {
	forward        *forwardRecord
	oldPorts       []forwardPortRecord
	targetTunnelID int64
	targetPort     int
	keptNodeIDs    []int64
	removedNodeIDs []int64
	portAdjusted   bool
	// X-P2-4 ②：托管中转转发按目标隧道出口集重算的 RemoteAddr
	// （remoteAddrChanged=false 时沿用现址）。
	newRemoteAddr     string
	remoteAddrChanged bool
}

func (h *Handler) tunnelDeletePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}

	id := idFromBody(r, w)
	if id <= 0 {
		return
	}

	preview, err := h.buildTunnelDeletePreview(id)
	if err != nil {
		if strings.Contains(err.Error(), "不存在") {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}

	response.WriteJSON(w, response.OK(preview))
}

func (h *Handler) tunnelBatchDeletePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}

	var req struct {
		IDs []int64 `json:"ids"`
	}
	if err := decodeJSON(r.Body, &req); err != nil || len(req.IDs) == 0 {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}

	preview, err := h.buildTunnelBatchDeletePreview(req.IDs)
	if err != nil {
		if strings.Contains(err.Error(), "不存在") {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}

	response.WriteJSON(w, response.OK(preview))
}

func (h *Handler) tunnelDeleteWithForwards(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}

	var req tunnelDeleteWithForwardsRequest
	if err := decodeJSON(r.Body, &req); err != nil || req.ID <= 0 {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}

	action, err := normalizeTunnelDeleteAction(req.Action)
	if err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	if action == tunnelDeleteActionReplace {
		if _, _, authErr := userRoleFromRequest(r); authErr != nil {
			response.WriteJSON(w, response.Err(401, "无效的token或token已过期"))
			return
		}
	}

	result, failures, err := h.processTunnelDeleteWithForwards(req.ID, action, req.TargetTunnelID)
	if err != nil {
		if err == errInvalidTunnelDeleteTarget {
			response.WriteJSON(w, response.ErrDefault("目标隧道不能为空"))
			return
		}
		if strings.Contains(err.Error(), "目标隧道不能与当前隧道相同") || strings.Contains(err.Error(), "目标隧道不存在") || strings.Contains(err.Error(), "目标隧道已禁用") || strings.Contains(err.Error(), "隧道不存在") {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if len(failures) > 0 {
		response.WriteJSON(w, response.R{
			Code: -2,
			Msg:  "部分规则迁移失败",
			TS:   time.Now().UnixMilli(),
			Data: batchOperationResult{SuccessCount: 0, FailCount: len(failures), Failures: failures},
		})
		return
	}

	response.WriteJSON(w, response.OK(result))
}

func (h *Handler) tunnelBatchDeleteWithForwards(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}

	var req tunnelBatchDeleteWithForwardsRequest
	if err := decodeJSON(r.Body, &req); err != nil || len(req.IDs) == 0 {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}

	action, err := normalizeTunnelDeleteAction(req.Action)
	if err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	if action == tunnelDeleteActionReplace {
		if _, _, authErr := userRoleFromRequest(r); authErr != nil {
			response.WriteJSON(w, response.Err(401, "无效的token或token已过期"))
			return
		}
	}

	normalizedIDs := normalizeTunnelIDs(req.IDs)
	if len(normalizedIDs) == 0 {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}

	if action == tunnelDeleteActionReplace {
		if req.TargetTunnelID <= 0 {
			response.WriteJSON(w, response.ErrDefault("目标隧道不能为空"))
			return
		}
		for _, id := range normalizedIDs {
			if id == req.TargetTunnelID {
				response.WriteJSON(w, response.ErrDefault("目标隧道不能包含在删除列表中"))
				return
			}
		}
	}

	result := tunnelBatchDeleteWithForwardsResult{}
	for _, tunnelID := range normalizedIDs {
		tunnelName, _ := h.repo.GetTunnelName(tunnelID)
		singleResult, failures, processErr := h.processTunnelDeleteWithForwards(tunnelID, action, req.TargetTunnelID)
		if processErr != nil {
			result.FailCount++
			result.Failures = appendBatchFailure(result.Failures, tunnelID, tunnelName, processErr)
			continue
		}
		if len(failures) > 0 {
			result.FailCount++
			result.Failures = appendBatchFailureReason(
				result.Failures,
				tunnelID,
				tunnelName,
				summarizeTunnelDeleteRuleFailures(failures),
			)
			continue
		}

		result.SuccessCount++
		result.DeletedForwardCount += singleResult.DeletedForwardCount
		result.MigratedCount += singleResult.MigratedCount
		result.PortAdjustedCount += singleResult.PortAdjustedCount
		if len(singleResult.Warnings) > 0 {
			result.Warnings = append(result.Warnings, singleResult.Warnings...)
		}
	}

	response.WriteJSON(w, response.OK(result))
}

func (h *Handler) buildTunnelDeletePreview(tunnelID int64) (*tunnelDeletePreviewData, error) {
	if _, err := h.getTunnelRecord(tunnelID); err != nil {
		return nil, err
	}

	tunnelName, err := h.repo.GetTunnelName(tunnelID)
	if err != nil {
		return nil, err
	}

	forwards, err := h.listForwardsByTunnel(tunnelID)
	if err != nil {
		return nil, err
	}

	samples := make([]tunnelDeleteForwardPreviewItem, 0, minInt(len(forwards), tunnelDeletePreviewSampleLimit))
	for i, forward := range forwards {
		if i >= tunnelDeletePreviewSampleLimit {
			break
		}
		ports, portsErr := h.listForwardPorts(forward.ID)
		if portsErr != nil {
			return nil, portsErr
		}
		inPort := 0
		if len(ports) > 0 {
			inPort = ports[0].Port
		}
		samples = append(samples, tunnelDeleteForwardPreviewItem{
			ID:       forward.ID,
			Name:     forward.Name,
			UserID:   forward.UserID,
			UserName: forward.UserName,
			InPort:   inPort,
		})
	}

	return &tunnelDeletePreviewData{
		TunnelID:       tunnelID,
		TunnelName:     tunnelName,
		ForwardCount:   len(forwards),
		SampleForwards: samples,
	}, nil
}

func (h *Handler) buildTunnelBatchDeletePreview(ids []int64) (*tunnelBatchDeletePreviewData, error) {
	normalizedIDs := normalizeTunnelIDs(ids)
	items := make([]tunnelDeletePreviewData, 0, len(normalizedIDs))
	totalForwardCount := 0
	for _, id := range normalizedIDs {
		preview, err := h.buildTunnelDeletePreview(id)
		if err != nil {
			return nil, err
		}
		items = append(items, *preview)
		totalForwardCount += preview.ForwardCount
	}
	return &tunnelBatchDeletePreviewData{
		TunnelCount:       len(items),
		TotalForwardCount: totalForwardCount,
		Items:             items,
	}, nil
}

func normalizeTunnelDeleteAction(action string) (string, error) {
	normalized := strings.TrimSpace(action)
	if normalized == "" {
		return tunnelDeleteActionDeleteForwards, nil
	}
	if normalized != tunnelDeleteActionReplace && normalized != tunnelDeleteActionDeleteForwards {
		return "", errors.New("invalid tunnel delete action")
	}
	return normalized, nil
}

func normalizeTunnelIDs(ids []int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func summarizeTunnelDeleteRuleFailures(failures []batchFailureDetail) string {
	if len(failures) == 0 {
		return "未知错误"
	}
	parts := make([]string, 0, minInt(len(failures), 3))
	for i, failure := range failures {
		if i >= 3 {
			break
		}
		name := strings.TrimSpace(failure.Name)
		if name == "" {
			name = fmt.Sprintf("规则 #%d", failure.ID)
		}
		parts = append(parts, fmt.Sprintf("%s: %s", name, strings.TrimSpace(failure.Reason)))
	}
	if len(failures) > 3 {
		parts = append(parts, fmt.Sprintf("另有 %d 条规则失败", len(failures)-3))
	}
	return strings.Join(parts, "；")
}

func (h *Handler) processTunnelDeleteWithForwards(tunnelID int64, action string, targetTunnelID int64) (tunnelDeleteWithForwardsResult, []batchFailureDetail, error) {
	preview, err := h.buildTunnelDeletePreview(tunnelID)
	if err != nil {
		return tunnelDeleteWithForwardsResult{}, nil, err
	}

	result := tunnelDeleteWithForwardsResult{ForwardCount: preview.ForwardCount}
	if preview.ForwardCount == 0 {
		if err := h.deleteTunnelAndCleanup(tunnelID); err != nil {
			return tunnelDeleteWithForwardsResult{}, nil, err
		}
		return result, nil, nil
	}

	if action == tunnelDeleteActionDeleteForwards {
		result.DeletedForwardCount = preview.ForwardCount
		if err := h.deleteTunnelAndCleanup(tunnelID); err != nil {
			return tunnelDeleteWithForwardsResult{}, nil, err
		}
		return result, nil, nil
	}

	if targetTunnelID <= 0 {
		return tunnelDeleteWithForwardsResult{}, nil, errInvalidTunnelDeleteTarget
	}
	if targetTunnelID == tunnelID {
		return tunnelDeleteWithForwardsResult{}, nil, errors.New("目标隧道不能与当前隧道相同")
	}
	return h.processTunnelDeleteReplaceAction(tunnelID, targetTunnelID, result)
}

func (h *Handler) processTunnelDeleteReplaceAction(tunnelID, targetTunnelID int64, result tunnelDeleteWithForwardsResult) (tunnelDeleteWithForwardsResult, []batchFailureDetail, error) {
	targetTunnel, err := h.getTunnelRecord(targetTunnelID)
	if err != nil {
		return tunnelDeleteWithForwardsResult{}, nil, errors.New("目标隧道不存在")
	}
	if targetTunnel.Status != 1 {
		return tunnelDeleteWithForwardsResult{}, nil, errors.New("目标隧道已禁用")
	}

	plans, failures, err := h.planTunnelDeleteForwardMigrations(tunnelID, targetTunnelID)
	if err != nil {
		return tunnelDeleteWithForwardsResult{}, nil, err
	}
	if len(failures) > 0 {
		return tunnelDeleteWithForwardsResult{}, failures, nil
	}

	portAdjustedCount := 0
	warnings, execErr, execFailure := h.executeTunnelDeleteForwardMigrations(plans)
	for _, plan := range plans {
		if plan.portAdjusted {
			portAdjustedCount++
		}
	}
	if execErr != nil {
		failures = append(failures, execFailure)
		return tunnelDeleteWithForwardsResult{}, failures, nil
	}

	if err := h.deleteTunnelAndCleanup(tunnelID); err != nil {
		h.rollbackTunnelForwardMigrationPlans(plans)
		_ = h.redeployTunnelAndForwards(tunnelID)
		return tunnelDeleteWithForwardsResult{}, nil, err
	}

	result.MigratedCount = len(plans)
	result.PortAdjustedCount = portAdjustedCount
	if len(warnings) > 0 {
		result.Warnings = warnings
	}
	return result, nil, nil
}

func (h *Handler) planTunnelDeleteForwardMigrations(sourceTunnelID, targetTunnelID int64) ([]tunnelForwardMigrationPlan, []batchFailureDetail, error) {
	forwards, err := h.listForwardsByTunnel(sourceTunnelID)
	if err != nil {
		return nil, nil, err
	}

	entryNodes, err := h.tunnelEntryNodeIDs(targetTunnelID)
	if err != nil {
		return nil, nil, err
	}
	if len(entryNodes) == 0 {
		return nil, nil, errors.New("目标隧道缺少入口节点")
	}

	plans := make([]tunnelForwardMigrationPlan, 0, len(forwards))
	failures := make([]batchFailureDetail, 0)
	reservedPorts := make(map[int64]map[int]bool)

	for _, forward := range forwards {
		plan, planErr := h.planSingleTunnelDeleteForwardMigration(&forward, targetTunnelID, entryNodes, reservedPorts)
		if planErr != nil {
			failures = appendBatchFailure(failures, forward.ID, forward.Name, planErr)
			continue
		}
		plans = append(plans, plan)
	}

	return plans, failures, nil
}

func (h *Handler) planSingleTunnelDeleteForwardMigration(forward *forwardRecord, targetTunnelID int64, targetEntryNodes []int64, reservedPorts map[int64]map[int]bool) (tunnelForwardMigrationPlan, error) {
	if forward == nil {
		return tunnelForwardMigrationPlan{}, errors.New("转发不存在")
	}

	oldPorts, err := h.listForwardPorts(forward.ID)
	if err != nil {
		return tunnelForwardMigrationPlan{}, err
	}
	if len(oldPorts) == 0 {
		return tunnelForwardMigrationPlan{}, errors.New("转发入口端口不存在")
	}

	minPort := h.repo.GetMinForwardPort(forward.ID)
	targetPort := 0
	if minPort.Valid {
		targetPort = int(minPort.Int64)
	}
	if targetPort <= 0 {
		picked, pickErr := h.pickTunnelPort(targetTunnelID)
		if pickErr != nil {
			return tunnelForwardMigrationPlan{}, pickErr
		}
		targetPort = picked
	}

	hasCustomInIP := false
	for _, oldPort := range oldPorts {
		if strings.TrimSpace(oldPort.InIP) != "" {
			hasCustomInIP = true
			break
		}
	}
	if hasCustomInIP && len(targetEntryNodes) > 1 {
		return tunnelForwardMigrationPlan{}, errors.New("多入口隧道的转发不支持保留自定义监听IP，请先手动调整该规则")
	}

	for _, nodeID := range targetEntryNodes {
		node, nodeErr := h.getNodeRecord(nodeID)
		if nodeErr != nil {
			return tunnelForwardMigrationPlan{}, nodeErr
		}
		if err := validateRemoteNodePort(node, targetPort); err != nil {
			return tunnelForwardMigrationPlan{}, err
		}
		if err := validateLocalNodePort(node, targetPort); err != nil {
			return tunnelForwardMigrationPlan{}, err
		}
		if err := h.validateForwardPortAvailability(node, targetPort, forward.ID); err != nil {
			return tunnelForwardMigrationPlan{}, err
		}
		if reservedOnNode, ok := reservedPorts[nodeID]; ok && reservedOnNode[targetPort] {
			return tunnelForwardMigrationPlan{}, fmt.Errorf("目标隧道入口节点端口 %d 已被本次迁移中的其他规则占用", targetPort)
		}
	}

	for _, nodeID := range targetEntryNodes {
		reservedOnNode := reservedPorts[nodeID]
		if reservedOnNode == nil {
			reservedOnNode = make(map[int]bool)
			reservedPorts[nodeID] = reservedOnNode
		}
		reservedOnNode[targetPort] = true
	}

	oldNodeIDs := forwardPortNodeIDs(oldPorts)
	newNodeIDs := uniqueInt64s(targetEntryNodes)
	removedNodeIDs := diffInt64s(oldNodeIDs, newNodeIDs)
	keptNodeIDs := diffInt64s(oldNodeIDs, removedNodeIDs)

	previousPort := 0
	if len(oldPorts) > 0 {
		previousPort = oldPorts[0].Port
	}

	// X-P2-4 ②：托管中转转发按目标隧道出口集重算 RemoteAddr，随迁移
	// 落库并经 sync 重下发，不再只改端口沿用旧址；重算失败（如目标
	// 隧道出口集违反落地单出口规则）该转发迁移失败。
	targetTunnel, terr := h.getTunnelRecord(targetTunnelID)
	if terr != nil {
		return tunnelForwardMigrationPlan{}, terr
	}
	newRemoteAddr, remoteAddrChanged, retargetErr := h.recomputedRelayRemoteAddr(forward, targetTunnel)
	if retargetErr != nil {
		return tunnelForwardMigrationPlan{}, retargetErr
	}

	return tunnelForwardMigrationPlan{
		forward:           forward,
		oldPorts:          oldPorts,
		targetTunnelID:    targetTunnelID,
		targetPort:        targetPort,
		keptNodeIDs:       keptNodeIDs,
		removedNodeIDs:    removedNodeIDs,
		portAdjusted:      previousPort > 0 && previousPort != targetPort,
		newRemoteAddr:     newRemoteAddr,
		remoteAddrChanged: remoteAddrChanged,
	}, nil
}

func (h *Handler) executeTunnelDeleteForwardMigrations(plans []tunnelForwardMigrationPlan) ([]string, error, batchFailureDetail) {
	warnings := make([]string, 0)
	completed := make([]tunnelForwardMigrationPlan, 0, len(plans))

	for _, plan := range plans {
		migrationWarnings, err := h.applyTunnelDeleteForwardMigration(plan)
		if err != nil {
			h.rollbackTunnelForwardMigrationPlans(completed)
			return warnings, err, batchFailureDetail{ID: plan.forward.ID, Name: plan.forward.Name, Reason: normalizeBatchFailureReason(errString(err))}
		}
		warnings = append(warnings, migrationWarnings...)
		completed = append(completed, plan)
	}

	return warnings, nil, batchFailureDetail{}
}

func (h *Handler) applyTunnelDeleteForwardMigration(plan tunnelForwardMigrationPlan) ([]string, error) {
	if plan.forward == nil {
		return nil, errors.New("转发不存在")
	}

	if err := h.repo.UpdateForwardTunnel(plan.forward.ID, plan.targetTunnelID, time.Now().UnixMilli()); err != nil {
		return nil, err
	}
	if plan.remoteAddrChanged {
		if err := h.repo.UpdateForward(plan.forward.ID, plan.forward.Name, plan.targetTunnelID, plan.newRemoteAddr, plan.forward.Strategy, time.Now().UnixMilli(), plan.forward.SpeedID, plan.forward.MaxConn, plan.forward.IPMaxConn, plan.forward.IPSpeedID, 0, plan.forward.ProxyProtocolReceive, plan.forward.ProxyProtocolSend); err != nil {
			h.rollbackForwardMutation(plan.forward, plan.oldPorts)
			return nil, err
		}
	}
	if err := h.replaceForwardPorts(plan.forward.ID, plan.targetTunnelID, plan.targetPort, ""); err != nil {
		h.rollbackForwardMutation(plan.forward, plan.oldPorts)
		return nil, err
	}

	updatedForward, err := h.getForwardRecord(plan.forward.ID)
	if err != nil {
		h.rollbackForwardMutation(plan.forward, plan.oldPorts)
		return nil, err
	}

	warnings := make([]string, 0)
	if len(plan.keptNodeIDs) > 0 {
		for _, nodeID := range plan.keptNodeIDs {
			if delErr := h.deleteForwardServicesOnNodeBatch(plan.forward, nodeID); delErr != nil {
				nodeLabel := fmt.Sprintf("%d", nodeID)
				if n, nErr := h.getNodeRecord(nodeID); nErr == nil && n != nil && strings.TrimSpace(n.Name) != "" {
					nodeLabel = strings.TrimSpace(n.Name)
				}
				warnings = append(warnings, fmt.Sprintf("节点 %s 清理旧转发监听失败: %v", nodeLabel, delErr))
			}
		}
		time.Sleep(tunnelServiceBindRetryDelay)
	}

	syncWarnings, err := h.syncForwardServicesWithWarnings(updatedForward, "UpdateService", true)
	if err != nil {
		h.rollbackForwardMutation(plan.forward, plan.oldPorts)
		return nil, err
	}
	warnings = append(warnings, syncWarnings...)

	if len(plan.removedNodeIDs) > 0 {
		for _, nodeID := range plan.removedNodeIDs {
			if delErr := h.deleteForwardServicesOnNodeBatch(plan.forward, nodeID); delErr != nil {
				nodeLabel := fmt.Sprintf("%d", nodeID)
				if n, nErr := h.getNodeRecord(nodeID); nErr == nil && n != nil && strings.TrimSpace(n.Name) != "" {
					nodeLabel = strings.TrimSpace(n.Name)
				}
				warnings = append(warnings, fmt.Sprintf("节点 %s 清理旧隧道残留服务失败: %v", nodeLabel, delErr))
			}
		}
	}

	return warnings, nil
}

func (h *Handler) rollbackTunnelForwardMigrationPlans(plans []tunnelForwardMigrationPlan) {
	for i := len(plans) - 1; i >= 0; i-- {
		plan := plans[i]
		h.rollbackForwardMutation(plan.forward, plan.oldPorts)
	}
}

// cleanupTunnelForwardsDataPlane 是 F-002 的修复核心：删隧道前先枚举其
// 全部转发，逐条走与单条 forwardDelete 相同的数据面清理，再允许删库。
//
// 此前所有删隧道入口都只清隧道自身的 chain/service 并直接 DB 级联：
// gost 转发服务收不到 DeleteService，孤儿监听继续转发且脱离计数/配额；
// nft 转发连 pending 台账被级联一并删除，孤儿规则永不重试。
//
// R1 X-P1-1（E-01，BC-01，按 V-02）起区分失败类别（relay.md 承诺口径）：
//   - 节点离线/不可达（isNodeUnreachableError）：删库继续、接口成功。
//     gost 转发删行即登记待清理——节点重连后 agent 上报的配置经
//     cleanNodeConfigs 孤儿对账（DB 中已无此转发）撤掉残留监听；
//     nft 转发留在 pending-delete 台账（转发行为墓碑，DeleteTunnelCascade
//     保留之），由后台退避重试（runNftPendingDeleteRetryLoop）收敛。
//   - 在线真实失败（节点明确回失败等非离线错误）：仍中止删隧道，
//     F-002 防孤儿目标不丢。
//   - nft：先把全部转发标 pending 并登记台账，再对入口节点 reconcile
//     一次；成功后逐条删转发行（DeleteForwardCascade 会一并清台账/
//     计数/绑定）。
func (h *Handler) cleanupTunnelForwardsDataPlane(tunnelID int64) error {
	forwards, err := h.repo.ListForwardsByTunnel(tunnelID)
	if err != nil {
		return err
	}
	return h.cleanupForwardsDataPlane(tunnelID, forwards, "隧道", true)
}

// cleanupForwardsDataPlane cleans the data plane for an explicit list of
// forwards that all belong to one tunnel, then deletes their DB rows.
// scope names the owning object in abort messages ("隧道" / "用户").
// offlineTolerant 为 true（仅删隧道路径）时，离线/不可达类失败按
// X-P1-1 待清理语义放行删库；为 false（删用户路径）时维持 F-002 硬门。
func (h *Handler) cleanupForwardsDataPlane(tunnelID int64, forwards []model.ForwardRecord, scope string, offlineTolerant bool) error {
	if len(forwards) == 0 {
		return nil
	}
	nftMode, entryNodeIDs, err := h.tunnelUsesNftables(tunnelID)
	if err != nil {
		return err
	}
	if nftMode {
		if len(entryNodeIDs) == 0 {
			return errors.New("nftables 转发缺少入口节点")
		}
		nodeID := entryNodeIDs[0]
		now := time.Now()
		for i := range forwards {
			if err := h.repo.MarkForwardPendingDelete(forwards[i].ID, nodeID, now); err != nil {
				return err
			}
		}
		if err := h.reconcileNftablesNodeByRequest(nodeID); err != nil {
			if offlineTolerant && isNodeUnreachableError(err) {
				// X-P1-1：转发已入 pending-delete 台账，删库继续；
				// 转发墓碑行由台账重试收敛，隧道级联不得清台账。
				log.Printf("tunnel delete: node unreachable, %d nft forwards left in pending-delete ledger tunnel_id=%d node_id=%d err=%v", len(forwards), tunnelID, nodeID, err)
				return nil
			}
			return fmt.Errorf("节点不可达，%d 条转发已加入待清理队列，后台将自动重试，%s未删除", len(forwards), scope)
		}
		for i := range forwards {
			if err := h.deleteForwardByID(forwards[i].ID); err != nil {
				// 同 forwardDelete 的 D6：数据面已变更（规则已下线），
				// DB 删除失败记日志，靠后续对账收敛。
				log.Printf("forward delete: db delete after nft reconcile failed tunnel_id=%d forward_id=%d err=%v", tunnelID, forwards[i].ID, err)
				return err
			}
		}
		return nil
	}
	for i := range forwards {
		fwd := forwards[i]
		if err := h.controlForwardServices(&fwd, "DeleteService", true); err != nil {
			if offlineTolerant && isNodeUnreachableError(err) {
				// X-P1-1：节点离线时删行即完成面板侧删除；残留监听由
				// 节点重连后的配置上报孤儿对账（cleanNodeConfigs）撤销。
				log.Printf("tunnel delete: node offline, forward marked for reconnect cleanup tunnel_id=%d forward_id=%d err=%v", tunnelID, fwd.ID, err)
				if delErr := h.deleteForwardByID(fwd.ID); delErr != nil {
					return delErr
				}
				continue
			}
			return fmt.Errorf("转发 %s 数据面清理失败，%s未删除: %w", fwd.Name, scope, err)
		}
		if err := h.deleteForwardByID(fwd.ID); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handler) deleteTunnelAndCleanup(tunnelID int64) error {
	if err := h.cleanupTunnelForwardsDataPlane(tunnelID); err != nil {
		return err
	}
	h.cleanupTunnelRuntime(tunnelID)
	h.cleanupFederationRuntime(tunnelID)
	if err := h.deleteTunnelByID(tunnelID); err != nil {
		return err
	}
	return nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
