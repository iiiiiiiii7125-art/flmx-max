package handler

import (
	"errors"
	"fmt"
	"strings"

	"go-backend/internal/store/repo"
)

// Classify ownership before building local counters or enforcing local quotas.
// Numeric IDs from a consuming panel are not IDs in the provider's database.
//
// R2-019（B-traffic B-T-01）：归属查询失败必须向上传播 error，禁止
// 吞成空批次——旧实现查询出错只记日志返回空批次，调用方
// （v1/v2）照常推进 seq/游标并回确认，agent 随即清掉待确认批次，
// 整批流量静默丢失不可恢复。调用方在 err != nil 时必须按 metas
// 失败同口径整批拒收（不确认、不推进），agent 保留批次重报。
func (h *Handler) buildNodeFlowUploadBatch(nodeID int64, items []flowItem, metas map[int64]repo.FlowUploadForwardMeta) (flowUploadBatch, error) {
	if h == nil || h.repo == nil || nodeID <= 0 {
		return flowUploadBatch{}, errors.New("flow ownership lookup: handler not initialized")
	}
	forwardIDs, err := h.repo.ListForwardIDsByNode(nodeID)
	if err != nil {
		return flowUploadBatch{}, fmt.Errorf("flow node ownership lookup failed node_id=%d: %w", nodeID, err)
	}
	nodeForwards := make(map[int64]struct{}, len(forwardIDs))
	for _, forwardID := range forwardIDs {
		nodeForwards[forwardID] = struct{}{}
	}
	localItems := make([]flowItem, 0, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.N)
		if strings.HasPrefix(name, "peer-share-") {
			// Legacy federation service names are never local forward
			// IDs; they must not be debited to a local owner.
			continue
		}
		if forwardID, userID, _, ok := parseFlowServiceIDs(name); ok {
			if meta, exists := metas[forwardID]; exists {
				if _, onNode := nodeForwards[forwardID]; !onNode || meta.UserID != userID {
					continue
				}
			}
		}
		localItems = append(localItems, item)
	}
	batch := h.buildFlowUploadBatch(localItems, metas)
	return batch, nil
}
