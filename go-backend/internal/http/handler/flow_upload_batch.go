package handler

import (
	"log"
	"math"
	"sort"
	"strings"
	"time"

	"go-backend/internal/store/repo"
)

type flowPolicyTarget struct {
	UserID       int64
	UserTunnelID int64
}

type flowUploadBatch struct {
	flowDeltas            []repo.FlowUploadCounterDelta
	quotaUsage            map[int64]int64
	policyTargets         []flowPolicyTarget
	forwardTraffic        map[int64]tunnelTrafficDelta
	orphanServices        map[string]struct{}
	peerShareForwardItems map[string]flowItem
	peerShareRuntimeItems map[int64]flowItem
	peerShareUsage        map[int64]int64
	// seqAdvance X-P3-31：agent 上报路径设置——入账单事务提交时同
	// tx 推进 seq；非上报路径（策略单条/内部调用）为 nil。
	seqAdvance *repo.FlowSeqAdvance
}

func (h *Handler) buildFlowUploadBatch(items []flowItem, metas map[int64]repo.FlowUploadForwardMeta) flowUploadBatch {
	batch := flowUploadBatch{
		quotaUsage:            make(map[int64]int64),
		forwardTraffic:        make(map[int64]tunnelTrafficDelta),
		orphanServices:        make(map[string]struct{}),
		peerShareForwardItems: make(map[string]flowItem),
		peerShareRuntimeItems: make(map[int64]flowItem),
	}
	policySeen := map[flowPolicyTarget]struct{}{}

	// X-P3-18（L-05-1）：gost 合并/缩放溢出检查（对齐 nft 路径：溢出
	// 整条丢弃并记日志）。旧实现逐项直接累加，两条各 ≈9.2e18 的上报
	// 合并回绕为负、负增量落账。现本地转发先聚合到 pending（任一加法
	// 溢出即整条转发丢弃），最后统一落 batch；peer 合并同口径丢弃。
	type pendingLocal struct {
		userID, userTunnelID int64
		rawIn, rawOut        int64
		scaledIn, scaledOut  int64
	}
	pending := make(map[int64]*pendingLocal)
	pendingOrder := make([]int64, 0)
	droppedLocal := make(map[int64]struct{})
	droppedPeerRuntime := make(map[int64]struct{})
	droppedPeerForward := make(map[string]struct{})
	addOverflows := func(a, b int64) bool {
		return (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b)
	}

	for _, item := range items {
		// H-17: 丢弃负流量值。/flow/upload 仅凭节点 secret 鉴权，攻击者可伪造
		// {"d":-N} 经 gorm.Expr("in_flow + ?", 负数) 直接扣减计数器，绕过配额。
		if item.U < 0 || item.D < 0 {
			continue
		}
		serviceName := strings.TrimSpace(item.N)
		if serviceName == "" || serviceName == "web_api" {
			continue
		}
		if runtimeID, ok := parsePeerShareRuntimeServiceID(serviceName); ok {
			if _, dropped := droppedPeerRuntime[runtimeID]; dropped {
				continue
			}
			merged := batch.peerShareRuntimeItems[runtimeID]
			if addOverflows(merged.U, item.U) || addOverflows(merged.D, item.D) {
				log.Printf("gost overflow discard peer runtime_id=%d service=%s u=%d d=%d", runtimeID, serviceName, item.U, item.D)
				delete(batch.peerShareRuntimeItems, runtimeID)
				droppedPeerRuntime[runtimeID] = struct{}{}
				continue
			}
			merged.N = serviceName
			merged.U += item.U
			merged.D += item.D
			batch.peerShareRuntimeItems[runtimeID] = merged
			continue
		}
		forwardID, userID, userTunnelID, ok := parseFlowServiceIDs(serviceName)
		if !ok {
			continue
		}
		normalized := normalizeForwardRuntimeServiceName(serviceName)
		if _, dropped := droppedPeerForward[normalized]; !dropped {
			merged := batch.peerShareForwardItems[normalized]
			if addOverflows(merged.U, item.U) || addOverflows(merged.D, item.D) {
				log.Printf("gost overflow discard peer service=%s u=%d d=%d", normalized, item.U, item.D)
				delete(batch.peerShareForwardItems, normalized)
				droppedPeerForward[normalized] = struct{}{}
			} else {
				merged.N = normalized
				merged.U += item.U
				merged.D += item.D
				batch.peerShareForwardItems[normalized] = merged
			}
		}

		meta, exists := metas[forwardID]
		if !exists {
			batch.orphanServices[serviceName] = struct{}{}
			continue
		}
		// Local accounting uses database ownership, not foreign IDs embedded in
		// a service name (including stale user-tunnel IDs after reassignment).
		userID, userTunnelID = meta.UserID, meta.UserTunnelID

		if _, dropped := droppedLocal[forwardID]; dropped {
			continue
		}
		// N-01（方向钉死，勿再按字面交换）：agent 上报线格式的 U/D 与
		// 「上行/下行」字面相反。agent 在 go-gost/x/service/service.go 以
		// AddTraffic(name, outputBytes, inputBytes) 填充：U=gost
		// OutputBytes=远端→客户端（出），D=gost InputBytes=客户端→远端
		// （入）。真机实证（w10-run/N-01）：下载 12,582,912 字节时
		// U=12,583,527、D=267。故 InFlow（入）=item.D、OutFlow（出）
		// =item.U，与 nftables/realm 路径 BytesIn=to-target（renderer
		// 按目标地址计数）完全一致；raw.bytesIn/bytesOut 同此映射，
		// 隧道指标桶三条路径同口径。
		// 此前 O6-002 按「U=入」的字面假设交换过 scaledIn/scaledOut：
		// 双向计费时总额不变，但 TunnelFlow=1（单向，仅计入方向）时
		// gost 实际按出方向扣配额，与 realm/nftables 取入方向正好
		// 相反，且 in_flow/out_flow 归属与前端 ↑/↓ 展示颠倒。本次订正。
		// Flow=1 单向：仅入方向（InFlow）；Flow!=1 双向：入+出。
		// 历史 bug：曾把 Flow 当乘数用，导致 Flow=2 双倍扣费（1.0.7
		// M-001 已修复）；quotaUsage 始终为 scaledIn+scaledOut。
		// R7-F-01：缩放统一走共用守卫 scaleTrafficBytes，与 nft 路径
		// 同一语义（ratio==1 直通不走 float64 往返、乘积 >=2^63 /
		// NaN / Inf / 负值整条丢弃）。旧判据 `scaledF > float64
		// (MaxInt64)` 中 float64(MaxInt64) 本身即 2^63，乘积恰为
		// 2^63 时判据为假、int64(2^63) 回绕 MinInt64 并落入
		// user/user_tunnel 总量；本路径不得再自写缩放判据。
		scaledIn, scaleOK := scaleTrafficBytes(item.D, meta.TrafficRatio)
		var scaledOut int64
		if scaleOK && meta.TunnelFlow != 1 {
			scaledOut, scaleOK = scaleTrafficBytes(item.U, meta.TrafficRatio)
		}
		if !scaleOK {
			log.Printf("gost scale overflow discard forward_id=%d u=%d d=%d ratio=%v", forwardID, item.U, item.D, meta.TrafficRatio)
			delete(pending, forwardID)
			droppedLocal[forwardID] = struct{}{}
			continue
		}

		pl, seen := pending[forwardID]
		if !seen {
			pl = &pendingLocal{userID: userID, userTunnelID: userTunnelID}
			pending[forwardID] = pl
			pendingOrder = append(pendingOrder, forwardID)
		}
		if addOverflows(pl.rawIn, item.D) || addOverflows(pl.rawOut, item.U) ||
			addOverflows(pl.scaledIn, scaledIn) || addOverflows(pl.scaledOut, scaledOut) {
			log.Printf("gost merge overflow discard forward_id=%d u=%d d=%d", forwardID, item.U, item.D)
			delete(pending, forwardID)
			droppedLocal[forwardID] = struct{}{}
			continue
		}
		pl.rawIn += item.D
		pl.rawOut += item.U
		pl.scaledIn += scaledIn
		pl.scaledOut += scaledOut
	}

	// 落账：按首见顺序把 pending 写进 batch（quota 合计溢出同样整条丢弃）。
	for _, forwardID := range pendingOrder {
		pl, ok := pending[forwardID]
		if !ok {
			continue // 已因溢出丢弃
		}
		if addOverflows(pl.scaledIn, pl.scaledOut) || addOverflows(batch.quotaUsage[pl.userID], pl.scaledIn+pl.scaledOut) {
			log.Printf("gost quota overflow discard forward_id=%d scaled_in=%d scaled_out=%d", forwardID, pl.scaledIn, pl.scaledOut)
			continue
		}
		batch.forwardTraffic[forwardID] = tunnelTrafficDelta{bytesIn: pl.rawIn, bytesOut: pl.rawOut}
		// R5X-013：delta 拆 raw/scaled 两组——forward 行落 raw
		// （pl.rawIn/rawOut，与 forwardTraffic 指标桶同源），
		// user/user_tunnel 行与 quota 落 scaled。
		batch.flowDeltas = append(batch.flowDeltas, repo.FlowUploadCounterDelta{
			ForwardID:    forwardID,
			UserID:       pl.userID,
			UserTunnelID: pl.userTunnelID,
			InFlow:       pl.scaledIn,
			OutFlow:      pl.scaledOut,
			RawInFlow:    pl.rawIn,
			RawOutFlow:   pl.rawOut,
			HasRaw:       true,
		})
		batch.quotaUsage[pl.userID] += pl.scaledIn + pl.scaledOut
		target := flowPolicyTarget{UserID: pl.userID, UserTunnelID: pl.userTunnelID}
		if _, seen := policySeen[target]; !seen {
			policySeen[target] = struct{}{}
			batch.policyTargets = append(batch.policyTargets, target)
		}
	}

	sort.Slice(batch.policyTargets, func(i, j int) bool {
		if batch.policyTargets[i].UserID == batch.policyTargets[j].UserID {
			return batch.policyTargets[i].UserTunnelID < batch.policyTargets[j].UserTunnelID
		}
		return batch.policyTargets[i].UserID < batch.policyTargets[j].UserID
	})

	return batch
}

// applyFlowUploadBatch 返回入账错误（X-P3-31/V-08）：总量+quota 由
// repo 单事务原子入账（seqAdvance 非 nil 时 seq 同 tx 推进）；失败时
// 整批未入账，策略执行/孤儿清理/peer-share 结算一律不进行，调用方
// （flowUpload）据此整批拒收返非 ok，agent 同 seq 重报重新入账。
// 旧「两段分开写、一段失败另一段继续 + 逐条 fallback」取舍已按
// V-08 裁定废弃（断言更新登记见 flow_upload_batch_test.go 三测试）。
func (h *Handler) applyFlowUploadBatch(nodeID int64, batch flowUploadBatch, now time.Time) error {
	if h == nil || h.repo == nil {
		return nil
	}
	quotaViews, err := h.repo.ApplyFlowUploadAccounting(batch.flowDeltas, batch.quotaUsage, now, batch.seqAdvance)
	if err != nil {
		log.Printf("flow upload accounting failed node_id=%d err=%v", nodeID, err)
		return err
	}
	for userID, quota := range quotaViews {
		h.enforceUserQuotaIfNeeded(userID, quota)
	}
	for _, target := range batch.policyTargets {
		if target.UserID <= 0 || target.UserTunnelID <= 0 {
			continue
		}
		h.enforceFlowPolicies(target.UserID, target.UserTunnelID)
	}
	if len(batch.orphanServices) > 0 {
		serviceNames := make([]string, 0, len(batch.orphanServices))
		for serviceName := range batch.orphanServices {
			serviceNames = append(serviceNames, serviceName)
		}
		h.sendDeleteOrphanedForwardServices(nodeID, serviceNames)
	}
	for serviceName, item := range batch.peerShareForwardItems {
		forwardID, _, _, ok := parseFlowServiceIDs(serviceName)
		if ok {
			h.processPeerShareFlowFromForward(forwardID, nodeID, serviceName, item)
		}
	}
	for runtimeID, item := range batch.peerShareRuntimeItems {
		h.processPeerShareFlow(nodeID, runtimeID, item)
	}
	for shareID, delta := range batch.peerShareUsage {
		h.addPeerShareFlow(nodeID, shareID, delta)
	}
	return nil
}
