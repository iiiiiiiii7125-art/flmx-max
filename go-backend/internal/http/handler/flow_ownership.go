package handler

import (
	"log"
	"math"
	"strings"

	"go-backend/internal/store/repo"
)

// Classify ownership before building local counters or enforcing local quotas.
// Numeric IDs from a consuming panel are not IDs in the provider's database.
func (h *Handler) buildNodeFlowUploadBatch(nodeID int64, items []flowItem, metas map[int64]repo.FlowUploadForwardMeta) flowUploadBatch {
	if h == nil || h.repo == nil || nodeID <= 0 {
		return flowUploadBatch{}
	}
	runtimes, err := h.repo.ListActiveForwardPeerShareRuntimesByNode(nodeID)
	if err != nil {
		log.Printf("flow ownership lookup failed node_id=%d err=%v", nodeID, err)
		return flowUploadBatch{}
	}
	resources, err := h.repo.ListPeerShareResourcesByNode(nodeID)
	if err != nil {
		log.Printf("flow resource lookup failed node_id=%d err=%v", nodeID, err)
		return flowUploadBatch{}
	}
	forwardIDs, err := h.repo.ListForwardIDsByNode(nodeID)
	if err != nil {
		log.Printf("flow node ownership lookup failed node_id=%d err=%v", nodeID, err)
		return flowUploadBatch{}
	}
	nodeForwards := make(map[int64]struct{}, len(forwardIDs))
	for _, forwardID := range forwardIDs {
		nodeForwards[forwardID] = struct{}{}
	}
	legacyOwners := make(map[string]map[int64]struct{})
	addLegacyOwner := func(name string, shareID int64) {
		name = normalizeForwardRuntimeServiceName(name)
		if name == "" {
			return
		}
		if legacyOwners[name] == nil {
			legacyOwners[name] = make(map[int64]struct{})
		}
		legacyOwners[name][shareID] = struct{}{}
	}
	for _, runtime := range runtimes {
		addLegacyOwner(runtime.ServiceName, runtime.ShareID)
	}
	resourceOwners := make(map[string]int64)
	for _, resource := range resources {
		// A migrated TCP service may still own a legacy UDP sibling. This alias
		// remains authoritative until the whole legacy family is acknowledged gone.
		addLegacyOwner(resource.LegacyServiceBase, resource.ShareID)
		// A deletion intent does not prove the listener is gone. Failed or
		// timed-out commands retain ownership until the node acknowledges it.
		if resource.Kind == "service" && (resource.DesiredState != "deleted" || resource.Applied == 0) {
			resourceOwners[resource.RuntimeName] = resource.ShareID
		}
	}
	localItems := make([]flowItem, 0, len(items))
	sharedUsage := make(map[int64]int64)
	for _, item := range items {
		name := strings.TrimSpace(item.N)
		if strings.HasPrefix(name, "peer-share-") {
			shareID, _, ok := parsePeerShareServiceName(name)
			if ok && resourceOwners[name] == shareID && item.U >= 0 && item.D >= 0 {
				// X-P3-18（L-05-1）：U+D 与累计溢出整条丢弃（对齐
				// buildFlowUploadBatch 口径），不得回绕为负后落账。
				if item.U > math.MaxInt64-item.D || sharedUsage[shareID] > math.MaxInt64-(item.U+item.D) {
					log.Printf("gost overflow discard peer-share service=%s u=%d d=%d", name, item.U, item.D)
				} else {
					sharedUsage[shareID] += item.U + item.D
				}
			}
			// Unknown or confirmed-deleted scoped names must never become local IDs.
			continue
		}
		if owners := legacyOwners[normalizeForwardRuntimeServiceName(name)]; len(owners) > 0 {
			forwardID, userID, userTunnelID, parsed := parseFlowServiceIDs(name)
			meta, local := metas[forwardID]
			_, onNode := nodeForwards[forwardID]
			if parsed && local && onNode && meta.UserID == userID && meta.UserTunnelID == userTunnelID {
				// Legacy names can be genuinely ambiguous. Preserve the listener
				// but do not debit either owner based on an ID guess.
				log.Printf("ambiguous legacy flow ownership node_id=%d service=%s", nodeID, name)
				continue
			}
			if len(owners) == 1 && item.U >= 0 && item.D >= 0 {
				for shareID := range owners {
					// OBS-R7-F-02（随 R7-F-01 批同口径）：legacy-owner
					// 分支与上方 peer-share 分支同一溢出守卫——U+D
					// 本身或累计和溢出即整条丢弃，不得回绕为负后
					// 落入 sharedUsage。
					if item.U > math.MaxInt64-item.D || sharedUsage[shareID] > math.MaxInt64-(item.U+item.D) {
						log.Printf("gost overflow discard legacy peer-share service=%s u=%d d=%d", name, item.U, item.D)
					} else {
						sharedUsage[shareID] += item.U + item.D
					}
				}
			}
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
	batch.peerShareUsage = sharedUsage
	return batch
}

// addPeerShareFlow 是 peer share 流量的唯一汇总点（四入口：
// processPeerShareFlow / processPeerShareFlowFromForward /
// processPeerShareFlowByServiceName 的 U+D，以及 buildNodeFlowUploadBatch
// 的 peerShareUsage）。
// 口径（F-077 核查后写明）：share 的 current_flow 记的是**提供方
// 节点**被该 share 实际消耗的原始字节（U+D，不缩放）。不套用本地
// 隧道计费口径（TrafficRatio/Flow 方向选择）的理由：这些流量在
// 提供方没有对应的本地隧道/转发计量对象——消费方的 user/tunnel ID
// 在提供方库中不存在（见 buildNodeFlowUploadBatch 头注释），ratio
// 与 Flow 是消费方面板对其自有用户计费的参数，提供方无从取得；
// share 上限（MaxBandwidth，isPeerShareFlowExceeded 按字节比较）
// 的语义即「经本节点共享出去的物理流量上限」。四入口经核查口径
// 一致；若产品日后定义为与消费方计费一致，改此一处汇总口径即可，
// 各入口不得再自行缩放（防二次缩放）。
func (h *Handler) addPeerShareFlow(nodeID, shareID, delta int64) {
	if nodeID <= 0 || shareID <= 0 || delta <= 0 {
		return
	}
	share, err := h.repo.GetPeerShare(shareID)
	if err != nil || share == nil || share.NodeID != nodeID {
		return
	}
	if err := h.repo.AddPeerShareCurrentFlow(shareID, delta); err != nil {
		return
	}
	share, err = h.repo.GetPeerShare(shareID)
	if err == nil && isPeerShareFlowExceeded(share) {
		h.enforcePeerShareFlowLimit(shareID)
	}
}
