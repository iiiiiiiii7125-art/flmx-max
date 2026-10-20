package handler

import (
	"context"
	"errors"
	"log"
	"math"
	"sort"
	"strings"
	"time"

	runtimenft "go-backend/internal/runtime/nftables"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// errDiscardedDueToApplying: 采集轮因处于 apply 窗口或 generation 变化而丢弃。
// 调用方不得写 nft_counter_state、不推进基线。
var errDiscardedDueToApplying = errors.New("discarded: applying window or generation changed")

type nftTrafficDelta struct {
	ForwardID int64
	BytesIn   int64
	BytesOut  int64
}

type nftCounterStateKey struct {
	forwardID int64
	protocol  string
	direction string
}

func (h *Handler) runNftablesTrafficCollectJob(now time.Time) {
	if h == nil || h.repo == nil {
		return
	}
	nodes, err := h.repo.ListNftablesNodesForCollection()
	if err != nil {
		log.Printf("nftables traffic collection failed op=list_nodes err=%v", err)
		return
	}
	for i := range nodes {
		node := &nodes[i]
		h.collectNftablesNodeTraffic(node.NodeID, &node.Config, now)
	}
}

func (h *Handler) collectNftablesNodeTraffic(nodeID int64, cfgModel *model.NodeSSHConfig, now time.Time) {
	batch, metas, nowMs, quotaViews, err := h.accountNftablesNodeTraffic(nodeID, cfgModel, now)
	if err != nil || batch == nil {
		return
	}
	h.recordTunnelMetricsFromForwardBatch(nodeID, batch.forwardTraffic, metas, nowMs)
	for userID, quota := range quotaViews {
		h.enforceUserQuotaIfNeeded(userID, quota)
	}
	for _, target := range batch.policyTargets {
		if target.UserID <= 0 || target.UserTunnelID <= 0 {
			continue
		}
		h.enforceFlowPolicies(target.UserID, target.UserTunnelID)
	}
}

// accountNftablesNodeTraffic 执行采集→增量→落库（不含执法），供采集循环与
// reconcile 前 drain（R5-B3 D1-004/D8）复用。返回 batch+metas+nowMs+quotaViews 供调用方
// 执法；无增量时返回 nil batch（基线已推进）。
func (h *Handler) accountNftablesNodeTraffic(nodeID int64, cfgModel *model.NodeSSHConfig, now time.Time) (*flowUploadBatch, map[int64]repo.FlowUploadForwardMeta, int64, map[int64]*model.UserQuotaView, error) {
	if h == nil || h.repo == nil {
		return nil, nil, 0, nil, errors.New("handler not initialized")
	}
	if h.nftablesManager == nil {
		return nil, nil, 0, nil, errors.New("nftables manager not initialized")
	}
	sshCfg, err := sshConfigFromModel(cfgModel)
	if err != nil {
		log.Printf("nftables traffic collection failed op=ssh_config node_id=%d err=%v", nodeID, err)
		return nil, nil, 0, nil, err
	}
	samples, err := func() ([]runtimenft.CounterSample, error) {
		// R5-B5B D18: 采集前记录 generation（节点级单调计数器，独立于 applying）。
		gen0 := h.nftablesManager.GetGeneration(nodeID)
		samples, err := h.nftablesManager.CollectCounters(context.Background(), nodeID, sshCfg)
		if err != nil {
			return nil, err
		}
		// D18: 若处于 apply 窗口（未超时）→ 丢弃本轮，不阻塞。
		if h.nftablesManager.IsApplying(nodeID) {
			log.Printf("nftables traffic discarded op=applying_window node_id=%d", nodeID)
			return nil, errDiscardedDueToApplying
		}
		// D18: 若 generation 变化（期间发生过完整 apply）→ 脏读，丢弃。
		if h.nftablesManager.GetGeneration(nodeID) != gen0 {
			log.Printf("nftables traffic discarded op=generation_changed node_id=%d", nodeID)
			return nil, errDiscardedDueToApplying
		}
		return samples, nil
	}()
	if err != nil {
		if err == errDiscardedDueToApplying {
			// 丢弃轮：不写 nft_counter_state，不推进基线，下轮补回。
			// （nft 计数器是累计值，跳过区间在下轮 delta 中补回；
			// 若期间 apply 重建表，负 delta 走既有 reset 逻辑。）
			return nil, nil, 0, nil, nil
		}
		// R5-B3 D1-003/D9(a): 表缺失（节点重启）→ 仅投递异步任务，由独立
		// worker 执行；采集循环立即返回（R2：不在事务内做 SSH+DB）。
		if isNftTableMissingError(err) {
			h.enqueueNftAutoReconcile(nodeID, "table_missing")
		}
		log.Printf("nftables traffic collection failed op=collect node_id=%d err=%v", nodeID, err)
		return nil, nil, 0, nil, err
	}
	// R5-B3 D1-003/D9(b): 采集成功而最近一次 apply 失败过 → 节点已恢复可达，
	// 触发自动 reconcile（投递后由 worker 异步执行）。
	h.checkNftNodeRecovered(nodeID)
	oldStates, err := h.repo.GetNftCounterStatesByNode(nodeID)
	if err != nil {
		log.Printf("nftables traffic collection failed op=list_states node_id=%d err=%v", nodeID, err)
		return nil, nil, 0, nil, err
	}
	bindings, err := h.repo.ListNftRuleBindingsByNode(nodeID)
	if err != nil {
		log.Printf("nftables traffic collection failed op=list_bindings node_id=%d err=%v", nodeID, err)
		return nil, nil, 0, nil, err
	}
	hashes := make(map[int64]string, len(bindings))
	for _, binding := range bindings {
		if strings.ToLower(strings.TrimSpace(binding.Status)) != runtimenft.StatusApplied {
			continue
		}
		ruleHash := strings.TrimSpace(binding.RuleHash)
		if ruleHash == "" {
			continue
		}
		hashes[binding.ForwardID] = ruleHash
	}

	nowMs := now.UnixMilli()
	boundSamples := filterNftCounterSamplesWithBinding(samples, hashes)
	deltas, newStates := buildNftCounterDeltas(nodeID, boundSamples, oldStates, hashes, nowMs)
	if len(newStates) == 0 {
		if len(deltas) != 0 {
			log.Printf("nftables traffic collection skipped suspicious deltas without states node_id=%d deltas=%d", nodeID, len(deltas))
		}
		return nil, nil, nowMs, nil, nil
	}

	// R5-B3 D1-001 矩阵 #9 / 补充 A: pending-delete 行的 delta 丢弃（不产生
	// 任何计费增量），但 newStates 照常落库使基线推进，避免攒出巨大 delta。
	// 注意：newStates 不在此处过滤，下游的 Upsert/Accounting 会推进其基线。
	deltas = h.filterOutPendingDeleteDeltas(nodeID, deltas)

	var metas map[int64]repo.FlowUploadForwardMeta
	forwardIDs := make([]int64, 0, len(deltas))
	for _, delta := range deltas {
		if delta.ForwardID > 0 {
			forwardIDs = append(forwardIDs, delta.ForwardID)
		}
	}
	if len(deltas) != 0 {
		metas, err = h.repo.GetFlowUploadForwardMetas(forwardIDs)
		if err != nil {
			log.Printf("nftables traffic collection failed op=load_flow_metas node_id=%d err=%v", nodeID, err)
			return nil, nil, nowMs, nil, err
		}
		// R5-B3 D1-001(poison-scope)/D10: 缺 meta 的 forward 仅跳过其 delta
		// （不计费），保留其旧基线（newStates 中剔除，下周期重试），warn 日志
		// 带 forward_id；其他 forward 正常累积。不再整节点 return。
		deltas, newStates = filterOutNftDeltasMissingMeta(deltas, newStates, metas, nodeID)
	}
	if len(deltas) == 0 {
		if err := h.repo.UpsertNftCounterStates(newStates, nowMs); err != nil {
			log.Printf("nftables traffic collection failed op=upsert_states node_id=%d err=%v", nodeID, err)
			return nil, nil, nowMs, nil, err
		}
		return nil, nil, nowMs, nil, nil
	}

	batch := buildNftFlowUploadBatch(deltas, metas)
	if missingForwardID, ok := firstNftBatchMissingDelta(deltas, batch); ok {
		log.Printf("nftables traffic collection skipped state advance op=unaccounted_delta node_id=%d forward_id=%d", nodeID, missingForwardID)
		return nil, nil, nowMs, nil, nil
	}
	quotaViews, err := h.repo.ApplyNftTrafficAccounting(batch.flowDeltas, batch.quotaUsage, newStates, now)
	if err != nil {
		log.Printf("nftables traffic collection failed op=accounting node_id=%d err=%v", nodeID, err)
		return nil, nil, nowMs, nil, err
	}
	return &batch, metas, nowMs, quotaViews, nil
}

// drainNftCountersBeforeReconcile 是 D8：reconcile 删表重建前 best-effort
// drain 计数器。成功返回 true；失败返回 false（调用方记 gap，
// 不得阻塞 apply）。drain 与重建必须在同一 single-flight 内（D1-003）。
func (h *Handler) drainNftCountersBeforeReconcile(nodeID int64, cfgModel *model.NodeSSHConfig, now time.Time) bool {
	_, _, _, _, err := h.accountNftablesNodeTraffic(nodeID, cfgModel, now)
	if err != nil {
		// M-005：告警级 gap：删表重建前 drain 失败，本轮到重建之间的流量将漏计（计数器已知局限）
		log.Printf("nftables reconcile drain gap node_id=%d err=%v action=apply_continues_baseline_reset ALERT=traffic_may_be_lost", nodeID, err)
		return false
	}
	return true
}

func firstNftBatchMissingDelta(deltas []nftTrafficDelta, batch flowUploadBatch) (int64, bool) {
	flowSeen := make(map[int64]struct{}, len(batch.flowDeltas))
	for _, delta := range batch.flowDeltas {
		flowSeen[delta.ForwardID] = struct{}{}
	}

	expectedRaw := make(map[int64]tunnelTrafficDelta, len(batch.forwardTraffic))
	for _, delta := range deltas {
		if delta.ForwardID <= 0 || (delta.BytesIn == 0 && delta.BytesOut == 0) {
			continue
		}
		if delta.BytesIn < 0 || delta.BytesOut < 0 {
			return delta.ForwardID, true
		}
		raw := expectedRaw[delta.ForwardID]
		if raw.bytesIn > math.MaxInt64-delta.BytesIn || raw.bytesOut > math.MaxInt64-delta.BytesOut {
			return delta.ForwardID, true
		}
		raw.bytesIn += delta.BytesIn
		raw.bytesOut += delta.BytesOut
		expectedRaw[delta.ForwardID] = raw
	}

	for forwardID, expected := range expectedRaw {
		actual, ok := batch.forwardTraffic[forwardID]
		if !ok || actual.bytesIn != expected.bytesIn || actual.bytesOut != expected.bytesOut {
			return forwardID, true
		}
		if expected.bytesIn != 0 || expected.bytesOut != 0 {
			if _, ok := flowSeen[forwardID]; !ok {
				return forwardID, true
			}
		}
	}
	return 0, false
}

// filterOutPendingDeleteDeltas 丢弃 pending-delete 行的 delta（R5-B3 D1-001
// 矩阵 #9 / 补充 A）。被丢弃的 delta 不产生计费增量；其 newStates 由调用方
// 照常落库，基线继续推进。
func (h *Handler) filterOutPendingDeleteDeltas(nodeID int64, deltas []nftTrafficDelta) []nftTrafficDelta {
	if len(deltas) == 0 || h == nil || h.repo == nil {
		return deltas
	}
	ids := make([]int64, 0, len(deltas))
	for _, d := range deltas {
		if d.ForwardID > 0 {
			ids = append(ids, d.ForwardID)
		}
	}
	statusMap, err := h.repo.GetForwardStatusMap(ids)
	if err != nil {
		log.Printf("nftables traffic collection failed op=pending_status node_id=%d err=%v", nodeID, err)
		return deltas
	}
	kept := deltas[:0]
	for _, d := range deltas {
		if d.ForwardID > 0 && statusMap[d.ForwardID] == model.ForwardStatusPendingDelete {
			continue
		}
		kept = append(kept, d)
	}
	if len(kept) != len(deltas) {
		log.Printf("nftables traffic collection dropped pending-delete deltas node_id=%d dropped=%d", nodeID, len(deltas)-len(kept))
	}
	return kept
}

// filterOutNftDeltasMissingMeta 是 D10：缺 meta 的 forward 仅跳过其 delta
// （不产生计费），并从 newStates 剔除其基线更新（保留旧基线，下周期重试），
// warn 日志带 forward_id；其他 forward 正常累积。
// 注意：旧的整节点 return 逻辑（firstNftDeltaMissingMeta）已删除。
func filterOutNftDeltasMissingMeta(deltas []nftTrafficDelta, newStates []repo.NftCounterStateInput, metas map[int64]repo.FlowUploadForwardMeta, nodeID int64) ([]nftTrafficDelta, []repo.NftCounterStateInput) {
	if len(deltas) == 0 {
		return deltas, newStates
	}
	skip := make(map[int64]bool)
	for _, delta := range deltas {
		if delta.ForwardID <= 0 {
			continue
		}
		if _, ok := metas[delta.ForwardID]; !ok {
			if !skip[delta.ForwardID] {
				skip[delta.ForwardID] = true
				log.Printf("nftables traffic skipped op=missing_flow_meta node_id=%d forward_id=%d action=delta_dropped_baseline_kept", nodeID, delta.ForwardID)
			}
		}
	}
	if len(skip) == 0 {
		return deltas, newStates
	}
	keptDeltas := deltas[:0]
	for _, delta := range deltas {
		if !skip[delta.ForwardID] {
			keptDeltas = append(keptDeltas, delta)
		}
	}
	keptStates := newStates[:0]
	for _, st := range newStates {
		if !skip[st.ForwardID] {
			keptStates = append(keptStates, st)
		}
	}
	return keptDeltas, keptStates
}

func filterNftCounterSamplesWithBinding(samples []runtimenft.CounterSample, hashes map[int64]string) []runtimenft.CounterSample {
	if len(samples) == 0 || len(hashes) == 0 {
		return nil
	}
	filtered := make([]runtimenft.CounterSample, 0, len(samples))
	for _, sample := range samples {
		if _, ok := hashes[sample.ForwardID]; !ok {
			continue
		}
		filtered = append(filtered, sample)
	}
	return filtered
}

func nftCounterKey(forwardID int64, protocol, direction string) nftCounterStateKey {
	return nftCounterStateKey{
		forwardID: forwardID,
		protocol:  strings.ToLower(strings.TrimSpace(protocol)),
		direction: strings.ToLower(strings.TrimSpace(direction)),
	}
}

func buildNftCounterDeltas(nodeID int64, samples []runtimenft.CounterSample, oldStates []model.NftCounterState, hashes map[int64]string, nowMs int64) ([]nftTrafficDelta, []repo.NftCounterStateInput) {
	oldByKey := make(map[nftCounterStateKey]model.NftCounterState, len(oldStates))
	for _, old := range oldStates {
		if old.NodeID != nodeID {
			continue
		}
		oldByKey[nftCounterKey(old.ForwardID, old.Protocol, old.Direction)] = old
	}

	stateInputs := make([]repo.NftCounterStateInput, 0, len(samples))
	deltaByForward := make(map[int64]nftTrafficDelta)
	for _, sample := range samples {
		direction := strings.ToLower(strings.TrimSpace(sample.Direction))
		if direction != runtimenft.CounterDirectionToTarget && direction != runtimenft.CounterDirectionFromTarget {
			continue
		}

		protocol := strings.ToLower(strings.TrimSpace(sample.Protocol))
		if protocol != "tcp" && protocol != "udp" {
			continue
		}
		if sample.Bytes > uint64(math.MaxInt64) || sample.Packets > uint64(math.MaxInt64) {
			continue
		}
		ruleHash := strings.TrimSpace(hashes[sample.ForwardID])
		stateInput := repo.NftCounterStateInput{
			NodeID:        nodeID,
			ForwardID:     sample.ForwardID,
			Protocol:      protocol,
			Direction:     direction,
			RuleHash:      ruleHash,
			Bytes:         sample.Bytes,
			Packets:       sample.Packets,
			CollectedTime: nowMs,
		}

		old, exists := oldByKey[nftCounterKey(sample.ForwardID, protocol, direction)]
		if !exists || old.RuleHash != ruleHash {
			stateInputs = append(stateInputs, stateInput)
			continue
		}
		if old.Bytes < 0 {
			stateInputs = append(stateInputs, stateInput)
			continue
		}
		oldBytes := uint64(old.Bytes)
		if sample.Bytes < oldBytes {
			// M-004/O6-003：已知局限——计数器重置（节点重启/规则重建）时，
			// 上次采样到重置之间的流量无法回收，直接以新值更新基线、不计费（漏计）。
			// 计数器方案天然无法感知重置前未采集的增量，无法完美解决，此处只更新基线。
			stateInputs = append(stateInputs, stateInput)
			continue
		}
		rawDelta := sample.Bytes - oldBytes
		if rawDelta == 0 {
			stateInputs = append(stateInputs, stateInput)
			continue
		}

		delta := deltaByForward[sample.ForwardID]
		delta.ForwardID = sample.ForwardID
		rawDeltaInt := int64(rawDelta)
		if direction == runtimenft.CounterDirectionToTarget {
			if delta.BytesIn > math.MaxInt64-rawDeltaInt {
				// M-006：溢出保护触发，丢弃本次增量（极低频），记录以便排查
				log.Printf("nftables overflow discard forward_id=%d direction=in dropped_bytes=%d", sample.ForwardID, rawDeltaInt)
				continue
			}
			delta.BytesIn += rawDeltaInt
		} else {
			if delta.BytesOut > math.MaxInt64-rawDeltaInt {
				// M-006：溢出保护触发，丢弃本次增量（极低频），记录以便排查
				log.Printf("nftables overflow discard forward_id=%d direction=out dropped_bytes=%d", sample.ForwardID, rawDeltaInt)
				continue
			}
			delta.BytesOut += rawDeltaInt
		}
		stateInputs = append(stateInputs, stateInput)
		deltaByForward[sample.ForwardID] = delta
	}

	forwardIDs := make([]int64, 0, len(deltaByForward))
	for forwardID := range deltaByForward {
		forwardIDs = append(forwardIDs, forwardID)
	}
	sort.Slice(forwardIDs, func(i, j int) bool { return forwardIDs[i] < forwardIDs[j] })

	deltas := make([]nftTrafficDelta, 0, len(forwardIDs))
	for _, forwardID := range forwardIDs {
		delta := deltaByForward[forwardID]
		if delta.BytesIn == 0 && delta.BytesOut == 0 {
			continue
		}
		deltas = append(deltas, delta)
	}
	return deltas, stateInputs
}

func buildNftFlowUploadBatch(deltas []nftTrafficDelta, metas map[int64]repo.FlowUploadForwardMeta) flowUploadBatch {
	batch := flowUploadBatch{
		quotaUsage:            make(map[int64]int64),
		forwardTraffic:        make(map[int64]tunnelTrafficDelta),
		orphanServices:        make(map[string]struct{}),
		peerShareForwardItems: make(map[string]flowItem),
		peerShareRuntimeItems: make(map[int64]flowItem),
	}
	policySeen := map[flowPolicyTarget]struct{}{}
	flowSeen := map[int64]int{}

	for _, delta := range deltas {
		meta, exists := metas[delta.ForwardID]
		if !exists {
			continue
		}

		// O6-005：TrafficRatio=0 会产生零计费（不拒绝，可能为有意免费配置），记录警告以便管理员发现误配置
		if meta.TrafficRatio == 0 && (delta.BytesIn > 0 || delta.BytesOut > 0) {
			log.Printf("nftables zero billing forward_id=%d traffic_bytes=%d zero_ratio_config_confirm_intended", delta.ForwardID, delta.BytesIn+delta.BytesOut)
		}

		raw := batch.forwardTraffic[delta.ForwardID]
		if delta.BytesIn < 0 || delta.BytesOut < 0 || raw.bytesIn > math.MaxInt64-delta.BytesIn || raw.bytesOut > math.MaxInt64-delta.BytesOut {
			// M-006：负值/溢出保护触发，丢弃本条增量（极低频），记录以便排查
			log.Printf("nftables invalid delta discard forward_id=%d bytes_in=%d bytes_out=%d", delta.ForwardID, delta.BytesIn, delta.BytesOut)
			continue
		}

		scaledIn, ok := scaleNftTrafficBytes(delta.BytesIn, meta.TrafficRatio)
		if !ok {
			// M-006：倍率缩放溢出（极低频），丢弃本条增量并记录
			log.Printf("nftables scale overflow discard forward_id=%d bytes=%d ratio=%v", delta.ForwardID, delta.BytesIn, meta.TrafficRatio)
			continue
		}
		// Flow=1 单向：只计上传（BytesIn）；Flow=2 双向：计上传+下载
		// 历史 bug：曾把 Flow 当乘数用，导致 Flow=2 双倍扣费
		var quotaDelta int64
		var scaledOut int64
		if meta.TunnelFlow == 1 {
			quotaDelta = scaledIn
			// scaledOut 保持 0
		} else {
			var ok bool
			scaledOut, ok = scaleNftTrafficBytes(delta.BytesOut, meta.TrafficRatio)
			if !ok {
				// M-006：倍率缩放溢出（极低频），丢弃本条增量并记录
				log.Printf("nftables scale overflow discard forward_id=%d bytes=%d ratio=%v", delta.ForwardID, delta.BytesOut, meta.TrafficRatio)
				continue
			}
			if scaledIn > math.MaxInt64-scaledOut {
				continue
			}
			quotaDelta = scaledIn + scaledOut
		}
		if batch.quotaUsage[meta.UserID] > math.MaxInt64-quotaDelta {
			continue
		}

		flowIdx, flowExists := flowSeen[delta.ForwardID]
		if flowExists && (batch.flowDeltas[flowIdx].InFlow > math.MaxInt64-scaledIn || batch.flowDeltas[flowIdx].OutFlow > math.MaxInt64-scaledOut) {
			continue
		}

		raw.bytesIn += delta.BytesIn
		raw.bytesOut += delta.BytesOut
		batch.forwardTraffic[delta.ForwardID] = raw

		if flowExists {
			batch.flowDeltas[flowIdx].InFlow += scaledIn
			batch.flowDeltas[flowIdx].OutFlow += scaledOut
		} else {
			flowSeen[delta.ForwardID] = len(batch.flowDeltas)
			batch.flowDeltas = append(batch.flowDeltas, repo.FlowUploadCounterDelta{
				ForwardID:    delta.ForwardID,
				UserID:       meta.UserID,
				UserTunnelID: meta.UserTunnelID,
				InFlow:       scaledIn,
				OutFlow:      scaledOut,
			})
		}
		batch.quotaUsage[meta.UserID] += quotaDelta

		target := flowPolicyTarget{UserID: meta.UserID, UserTunnelID: meta.UserTunnelID}
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

func scaleNftTrafficBytes(bytes int64, ratio float64) (int64, bool) {
	if bytes < 0 || ratio < 0 {
		return 0, false
	}
	var scaled int64
	if ratio == 1 {
		scaled = bytes
	} else {
		scaledFloat := float64(bytes) * ratio
		if math.IsNaN(scaledFloat) || math.IsInf(scaledFloat, 0) || scaledFloat < 0 || scaledFloat >= math.Pow(2, 63) {
			return 0, false
		}
		scaled = int64(scaledFloat)
	}
	return scaled, true
}
