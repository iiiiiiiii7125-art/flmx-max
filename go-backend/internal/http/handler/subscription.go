package handler

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go-backend/internal/http/response"
	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

var subscriptionCheckInterval = time.Minute

// subSyncMu keeps the loop and the traffic-triggered syncs from pushing the
// same kernels at once.
var subSyncMu sync.Mutex

// subPipeline is the JSON form of the stored subscription options.
// Since 2.1.2 the only stored processing-era options are the relay
// scope (Lines + RelayIDs); the processing block (regions/protocols/
// include/exclude/rename/flag/prefix/dedupe/sort/region groups/info
// nodes), the subscription chain options and rules/scripts were
// removed, and their stored keys are ignored on read and dropped on
// the next save.
type subPipeline struct {
	// Lines is the relay scope （中转范围三档）: "" keeps every
	// endpoint, "direct" keeps direct endpoints only, "relay" keeps
	// relayed endpoints only. It keeps the historical "lines" JSON
	// key on purpose: before 2.1.2 the same key carried the same
	// three-state scope inside the removed processing block, so a
	// stored value keeps its exact meaning. It applies only when no
	// RelayIDs whitelist is present.
	Lines string `json:"lines"`
	// RelayIDs is the exact endpoint whitelist (0 = direct, >0 =
	// relay id): when non-empty it is the operative scope filter and
	// takes precedence over Lines (see kernel.FilterEndpoints). It is
	// carried verbatim on parse and on save — never folded into
	// Lines. (2.1.2 folded it into Lines at parse time, which widened
	// a partial whitelist to its whole tier; that fold is removed so
	// stored pre-2.1.2 whitelists mean their exact set again.)
	RelayIDs []int64 `json:"relayIds,omitempty"`
	// Templates 按客户端槽位选订阅模板（kernel.TemplateClash /
	// Templates 按客户端槽位选订阅模板（kernel.TemplateClash /
	// TemplateSingbox / TemplateSurge / TemplateStash /
	// TemplateShadowrocket → 模板 ID）。选了模板的格式渲染进模板骨架；
	// 模板正文在渲染时按 ID 加载（见 Handler.subOptions），不落订阅行。
	Templates map[string]int64 `json:"templates,omitempty"`
}

// UnmarshalJSON decodes the options object leniently. Request
// decoding runs with DisallowUnknownFields, but the options object
// must keep tolerating keys removed in 2.1.2: old clients still send
// them, and they are ignored here and dropped on the next save.
// relayIds is captured verbatim — never folded into Lines.
func (p *subPipeline) UnmarshalJSON(data []byte) error {
	type alias subPipeline
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*p = subPipeline(a)
	return nil
}

func (p subPipeline) options() kernel.SubOptions {
	return kernel.SubOptions{Lines: p.Lines, RelayIDs: p.RelayIDs}
}

func (p *subPipeline) normalize() error {
	if p.Lines != "relay" && p.Lines != "direct" {
		p.Lines = ""
	}
	// 模板槽位归一化：只保留已知槽位与正 ID，未知槽位静默丢弃；
	// 模板存在性与格式匹配在保存时由 checkPipelineTemplates 校验。
	templates := map[string]int64{}
	for _, slot := range kernel.TemplateSlots {
		if id := p.Templates[slot]; id > 0 {
			templates[slot] = id
		}
	}
	p.Templates = templates
	return p.options().Validate()
}

func parsePipeline(raw string) subPipeline {
	var p subPipeline
	_ = json.Unmarshal([]byte(raw), &p)
	if p.Templates == nil {
		p.Templates = map[string]int64{}
	}
	return p
}

type errText string

func (e errText) Error() string { return string(e) }

func parseIDList(s string) []int64 {
	var out []int64
	seen := map[int64]bool{}
	for _, part := range strings.Split(s, ",") {
		id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err == nil && id > 0 && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func formatIDList(ids []int64) string {
	sorted := append([]int64(nil), ids...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	parts := make([]string, len(sorted))
	for i, id := range sorted {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, ",")
}

// randomHex / newUUID：X-P3-14（L-03-3）随机源失败 fail-closed。
// 旧实现忽略 rand 错误、落全零 token/UUID（可预测凭据）。
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := cryptoRandRead(b); err != nil {
		// RAW291：随机源错误只含操作系统熵源失败，不含任何
		// 内部状态——但仍以固定文案回执，不拼 err 原文。
		return "", errText("生成随机凭据失败")
	}
	return hex.EncodeToString(b), nil
}

func newUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := cryptoRandRead(b); err != nil {
		// RAW291：同 randomHex，固定文案。
		return "", errText("生成随机凭据失败")
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32], nil
}

// subActive reports whether the link works (and, for share subscriptions,
// whether its credentials must be on the kernels).
func subActive(s *model.Subscription, now time.Time) bool {
	return s.Enabled == 1 && (s.ExpireTime == 0 || s.ExpireTime > now.UnixMilli()) && !subOverQuota(s)
}

// subInactiveReason is why the link no longer works, as the name of
// the placeholder node served instead (see openSubscription); "" while
// it is active.
func subInactiveReason(s *model.Subscription, now time.Time) string {
	switch {
	case subActive(s, now):
		return ""
	case s.Enabled != 1:
		return "⚠️ 订阅已停用，请联系管理员"
	case subOverQuota(s):
		return "⚠️ 流量已用完，请联系管理员"
	}
	return "⚠️ 订阅已过期，请联系管理员"
}

// subUsed F2-04: the sum must saturate — two restored near-MaxInt64
// counters summed with a plain add wrap negative, which read as
// "no usage" and kept subOverQuota false forever (live-d scenario 3:
// MaxInt64 + MaxInt64 wrapped to -2).
func subUsed(s *model.Subscription) int64 {
	return repo.SaturatingAddInt64(s.UploadBytes, s.DownloadBytes)
}

func subOverQuota(s *model.Subscription) bool {
	return s.TrafficLimit > 0 && subUsed(s) >= s.TrafficLimit
}

// lastResetAt is the most recent monthly reset moment at or before now for
// reset day d (local time), clamped to the month's length.
func lastResetAt(d int, now time.Time) time.Time {
	at := func(y int, m time.Month) time.Time {
		last := time.Date(y, m+1, 0, 0, 0, 0, 0, now.Location()).Day()
		day := d
		if day > last {
			day = last
		}
		return time.Date(y, m, day, 0, 0, 0, 0, now.Location())
	}
	t := at(now.Year(), now.Month())
	if t.After(now) {
		t = at(now.Year(), now.Month()-1)
	}
	return t
}

// subTrafficSince is when the subscription's current used-traffic
// counters started accumulating (creation or the last traffic reset).
// Rows predating the traffic_since column fall back to the reset time,
// then to creation time, so no data backfill is needed.
func subTrafficSince(s *model.Subscription) int64 {
	if s.TrafficSince > 0 {
		return s.TrafficSince
	}
	if s.TrafficResetTime > 0 {
		return s.TrafficResetTime
	}
	return s.CreatedTime
}

// subDueReset reports whether the monthly reset is due.
func subDueReset(s *model.Subscription, now time.Time) bool {
	if s.ResetDay <= 0 || !isShare(s) {
		return false
	}
	return s.TrafficResetTime < lastResetAt(s.ResetDay, now).UnixMilli()
}

// applyUserCounters adds a node's per-user traffic to the subscriptions
// and cuts off the ones that reached their limit right away.
func (h *Handler) applyUserCounters(nodeID int64, epoch string, counters []agentUserCounter, now int64) {
	if err := h.applyUserCounterSamples(nodeID, epoch, counters, now); err != nil {
		log.Printf("subscription traffic apply failed node_id=%d err=%v", nodeID, err)
		return
	}
	h.syncSubscriptions(time.UnixMilli(now))
}

// applyUserCounterSamples 只把用户计数样本入账（基线推进＋订阅累
// 计＋小时桶），不做凭据同步/限额切断。供下发后 settle 在
// per-kernel apply 锁内调用：syncSubscriptions 会经
// applyKernelsForInbounds 回头取同一 kernel 的 apply 锁（不可重
// 入），锁内同步必自锁；同步与切断由周期轮询的 applyUserCounters
// 全量面在 ≤60s 内补做，切断判据读已入账累计值，不漏不重。
func (h *Handler) applyUserCounterSamples(nodeID int64, epoch string, counters []agentUserCounter, now int64) error {
	samples := h.scopedSubscriptionSamples(nodeID, counters)
	if len(samples) == 0 {
		return nil
	}
	return h.repo.ApplySubscriptionCounterSamples(nodeID, epoch, samples, now)
}

// scopedSubscriptionSamples F2-01: a node's per-user samples book only
// against subscriptions that node actually serves. The serving set is
// derived mechanically from the same source the pull/mount faces use —
// subInboundIDs (SourceAll / explicit list / source-derived) resolved
// to node_inbound.node_id — plus the nodes hosting metering companions
// of those inbounds (the companion's kernel carries the users and is
// the one reporting). Foreign or unknown subscription IDs are dropped
// with an aggregated warn (mirroring the unaccounted-inbound pattern
// in applyInboundCounterChunk): baselines are keyed per reporting
// node, so an unchecked foreign sample would mint a baseline and book
// every later delta against a subscription the node never served,
// forcing its quota cutoff at the sync that follows. A lookup failure
// drops the whole poll cycle fail-closed — counters are cumulative,
// so the next successful poll books the full delta against the old
// baseline and nothing is lost.
func (h *Handler) scopedSubscriptionSamples(nodeID int64, counters []agentUserCounter) []repo.SubscriptionCounterSample {
	if len(counters) == 0 {
		return nil
	}
	subs, err := h.repo.ListSubscriptions()
	if err != nil {
		log.Printf("subscription traffic scope lookup failed op=list_subscriptions node_id=%d err=%v action=dropped_all", nodeID, err)
		return nil
	}
	rows, err := h.repo.ListNodeInbounds()
	if err != nil {
		log.Printf("subscription traffic scope lookup failed op=list_inbounds node_id=%d err=%v action=dropped_all", nodeID, err)
		return nil
	}
	byID := make(map[int64]*model.Subscription, len(subs))
	for i := range subs {
		byID[subs[i].ID] = &subs[i]
	}
	nodeOfInbound := make(map[int64]int64, len(rows))
	for _, r := range rows {
		nodeOfInbound[r.ID] = r.NodeID
	}
	serving := make(map[int64]map[int64]bool, len(counters))
	resolve := func(s *model.Subscription) map[int64]bool {
		if set, ok := serving[s.ID]; ok {
			return set
		}
		set := map[int64]bool{}
		ids := subInboundIDs(s, rows)
		idSet := make(map[int64]bool, len(ids))
		for _, id := range ids {
			idSet[id] = true
			if n, ok := nodeOfInbound[id]; ok {
				set[n] = true
			}
		}
		for _, r := range rows {
			if r.CompanionOf != 0 && idSet[r.CompanionOf] {
				set[r.NodeID] = true
			}
		}
		serving[s.ID] = set
		return set
	}
	out := make([]repo.SubscriptionCounterSample, 0, len(counters))
	dropped := make(map[int64]int)
	for _, c := range counters {
		s, ok := byID[c.ID]
		if !ok || !resolve(s)[nodeID] {
			dropped[c.ID]++
			continue
		}
		out = append(out, repo.SubscriptionCounterSample{SubscriptionID: c.ID, Direction: c.Dir, Bytes: c.Bytes})
	}
	for id, n := range dropped {
		log.Printf("subscription traffic sample foreign op=foreign_subscription node_id=%d subscription_id=%d samples=%d action=dropped", nodeID, id, n)
	}
	return out
}

func isShare(s *model.Subscription) bool { return s.Kind == model.SubscriptionShare }

func kernelUser(s *model.Subscription) kernel.User {
	return kernel.User{ID: s.ID, UUID: s.UUID, Password: s.Password}
}

// subInboundIDs resolves the subscription's sources against the inbounds.
func subInboundIDs(s *model.Subscription, rows []model.NodeInbound) []int64 {
	// Companion inbounds are never subscription sources: every caller
	// of this choke point (user mounts, endpoint building, push sets,
	// list views) speaks about origins only. Metering retargeting
	// happens later, at endpoint construction.
	filtered := make([]model.NodeInbound, 0, len(rows))
	for _, r := range rows {
		if r.CompanionOf == 0 {
			filtered = append(filtered, r)
		}
	}
	rows = filtered
	if s.SourceAll == 1 {
		ids := make([]int64, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, r.ID)
		}
		return ids
	}
	ids := subExplicitIDs(s, rows)
	picked := make(map[int64]bool, len(ids))
	for _, id := range ids {
		picked[id] = true
	}
	sources := map[int64]bool{}
	for _, id := range parseIDList(s.SourceIDs) {
		sources[id] = true
	}
	for _, r := range rows {
		if r.SourceID != 0 && sources[r.SourceID] && !picked[r.ID] {
			ids = append(ids, r.ID)
		}
	}
	return ids
}

// subExplicitIDs are the inbounds picked one by one (still existing).
func subExplicitIDs(s *model.Subscription, rows []model.NodeInbound) []int64 {
	known := make(map[int64]bool, len(rows))
	for _, r := range rows {
		known[r.ID] = true
	}
	var ids []int64
	for _, id := range parseIDList(s.InboundIDs) {
		if known[id] {
			ids = append(ids, id)
		}
	}
	return ids
}

// activeUsersByInbound maps inbound ids to the share subscriptions whose
// credentials the kernels must currently accept. SS origins with a
// metering companion are remapped to the companion's id: the origin
// stays shared (it would ignore the users anyway) and the companion
// carries them as SS2022 multi-user clients.
func (h *Handler) activeUsersByInbound(now time.Time) map[int64][]kernel.User {
	out := h.rawActiveUsersByInbound(now)
	if len(out) == 0 {
		return out
	}
	comps, err := h.repo.ListCompanionInbounds()
	if err != nil || len(comps) == 0 {
		return out
	}
	compByOrigin := make(map[int64]int64, len(comps))
	for _, c := range comps {
		if _, ok := compByOrigin[c.CompanionOf]; !ok {
			compByOrigin[c.CompanionOf] = c.ID
		}
	}
	for originID, users := range out {
		compID, ok := compByOrigin[originID]
		if !ok || compID == originID {
			continue
		}
		out[compID] = append(out[compID], users...)
		delete(out, originID)
	}
	return out
}

// uncountableSubWarned dedupes the L4-08 over-capacity warning:
// rawActiveUsersByInbound runs on every kernel push / sync cycle, so
// the warning fires once per subscription id per process, not per push.
var uncountableSubWarned sync.Map

// warnUncountableSubscription logs loudly, once per subscription,
// when an active share subscription's id exceeds the socket-mark
// metering capacity (kernel.MaxUserMarkID): markedUsers drops it, so
// its traffic is never metered and its TrafficLimit never triggers.
// Before L4-08 this drop was silent apart from a code comment.
func warnUncountableSubscription(s *model.Subscription) {
	if s == nil || s.ID <= kernel.MaxUserMarkID {
		return
	}
	if _, loaded := uncountableSubWarned.LoadOrStore(s.ID, struct{}{}); loaded {
		return
	}
	log.Printf("subscription metering capacity exceeded: subscription_id=%d name=%q exceeds the per-user mark limit %d; this subscription's traffic will NOT be metered and its traffic limit will NOT trigger (L4-08)", s.ID, s.Name, kernel.MaxUserMarkID)
}

// rawActiveUsersByInbound is activeUsersByInbound before the SS
// companion remap: users are keyed by the inbound the subscription
// sources (the origin). Metering reconcile reads demand from this form.
func (h *Handler) rawActiveUsersByInbound(now time.Time) map[int64][]kernel.User {
	subs, err := h.repo.ListSubscriptions()
	if err != nil {
		log.Printf("subscription lookup failed: %v", err)
		return nil
	}
	var rows []model.NodeInbound
	memo := &companionGateMemo{}
	out := map[int64][]kernel.User{}
	for i := range subs {
		s := &subs[i]
		// R10-02（I-F3-01）纵深防御：删除墓碑永不进 payload——即使
		// 墓碑行被任何写路径改回 Enabled=1∧PendingDelete=1 的状态机
		// 外组合，凭据也不下发，使 completePendingSubscriptionDelete
		// 的「墓碑已排除」前提由构造保证、而非靠各入口约定。
		if s.PendingDelete == 1 {
			continue
		}
		if !isShare(s) || !subActive(s, now) {
			continue
		}
		warnUncountableSubscription(s)
		if rows == nil {
			if rows, err = h.repo.ListNodeInbounds(); err != nil {
				return nil
			}
		}
		for _, id := range h.subAllInboundIDs(s, rows, memo) {
			out[id] = append(out[id], kernelUser(s))
		}
	}
	return out
}

// applyKernelsForInbounds pushes the kernels serving the given inbounds,
// once each. Imported nodes have no kernel and are skipped.
func (h *Handler) applyKernelsForInbounds(ids []int64) error {
	seen := map[int64]bool{}
	var firstErr error
	for _, id := range ids {
		row, err := h.repo.GetNodeInbound(id)
		if err != nil || row == nil || isExternalInbound(row) || seen[row.KernelID] {
			continue
		}
		seen[row.KernelID] = true
		if err := h.applyKernel(row.KernelID, false); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// pushFailureMsg 把下发失败拼进成功回执 Msg 前的净化（RAW291）：
// 仓储错误（repoError 标记，如 applyKernel 的 GetNodeKernel 读失败）
// 只留固定前缀、原文只记日志；数据面下发错误（节点不在线、agent
// 拒收）原样附后——那是管理员排查所需的诊断信息。
func pushFailureMsg(prefix string, err error) string {
	if isRepoError(err) {
		log.Printf("push failure (storage): %v", err)
		return prefix
	}
	return prefix + ": " + err.Error()
}

// syncSubscriptions pushes credential changes for share subscriptions
// whose active state differs from what the kernels last received.
func (h *Handler) syncSubscriptions(now time.Time) {
	subSyncMu.Lock()
	defer subSyncMu.Unlock()
	// SS 计量伴生对账（节流）：物化/回收/同步伴生入站，使其与原入站
	// 开关和活跃分享订阅需求一致。失败只记日志，下轮重试。
	h.maybeReconcileSSMetering()
	// B4/F-058: 先重试未完成的订阅删除（墓碑）。下发成功才硬删行+分配；
	// 失败留待下一轮。与下方凭据轮换/状态同步的 AppliedActive 机制相互
	// 独立，本块不读写 AppliedActive。
	if pending, err := h.repo.ListPendingDeleteSubscriptions(); err == nil {
		for i := range pending {
			if err := h.completePendingSubscriptionDelete(&pending[i]); err != nil {
				log.Printf("subscription %d: pending delete push failed, will retry: %v", pending[i].ID, err)
			}
		}
	}
	subs, err := h.repo.ListSubscriptions()
	if err != nil {
		return
	}
	var rows []model.NodeInbound
	groupMemo := &companionGateMemo{}
	for i := range subs {
		s := &subs[i]
		if s.PendingDelete == 1 {
			continue // 墓碑由上方 F-058 删除块独占处理
		}
		if subDueReset(s, now) {
			if err := h.repo.ResetSubscriptionTraffic(s.ID, now.UnixMilli()); err == nil {
				s.UploadBytes, s.DownloadBytes = 0, 0
				if h.notifier != nil {
					h.notifier.clearAlerts(fmt.Sprintf("subflow:%d:", s.ID))
					// R4X-014：重置即恢复——与回落两处成对口径对齐，
					// 连带清该订阅流量子型的指纹，否则重置后同文案
					// 再越界被 30 分钟指纹窗抑制（R3X-030 同根变体）。
					h.notifier.forgetSubQuotaFlow(s.Name)
				}
			}
		}
		active := boolToInt(isShare(s) && subActive(s, now))
		if active == s.AppliedActive {
			continue
		}
		if rows == nil {
			if rows, err = h.repo.ListNodeInbounds(); err != nil {
				return
			}
		}
		// AUDIT-R8 (F7X-001): 下发失败时不更新 AppliedActive，留待下轮循环重试。
		// 旧代码无条件更新，导致失败后永不重试（凭据撤销失效）。
		if err := h.applyKernelsForInbounds(h.subAllInboundIDs(s, rows, groupMemo)); err != nil {
			log.Printf("subscription %d kernel push failed: %v", s.ID, err)
		} else {
			_ = h.repo.UpdateSubscriptionApplied(s.ID, active)
		}
	}
}

func (h *Handler) runSubscriptionLoop(ctx context.Context) {
	defer h.jobsWG.Done()
	ticker := time.NewTicker(subscriptionCheckInterval)
	defer ticker.Stop()
	for {
		h.syncSubscriptions(time.Now())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// subEndpoints are the subscription's endpoints before its pipeline: the
// selected inbounds and their relays, with the share credentials where
// the protocol allows.
func (h *Handler) subEndpoints(s *model.Subscription, rows []model.NodeInbound) []kernel.Endpoint {
	return h.subEndpointsMemo(s, rows, nil)
}

// subEndpointsMemo is subEndpoints with an F4 gate memo (Y-01): the
// caller passes the memo of the enclosing view build / request so the
// companion map — and with it the per-kernel payload rebuilds behind
// the applied gate — is evaluated once, not once per consultation.
func (h *Handler) subEndpointsMemo(s *model.Subscription, rows []model.NodeInbound, memo *companionGateMemo) []kernel.Endpoint {
	return h.subEndpointsForIDs(s, rows, memo, subInboundIDs(s, rows))
}

// subAllInboundIDs are every inbound a subscription draws from: its own
// sources, resolved by subInboundIDs. Credential pushes, membership
// checks and traffic attribution must use this set.
func (h *Handler) subAllInboundIDs(s *model.Subscription, rows []model.NodeInbound, memo *companionGateMemo) []int64 {
	return subInboundIDs(s, rows)
}

// subRenderEndpointsMemo is what a subscription renders: its own
// sources (which the caller passes through the subscription's pipeline
// at render time). Subscriptions render exactly subEndpointsMemo.
func (h *Handler) subRenderEndpointsMemo(s *model.Subscription, rows []model.NodeInbound, memo *companionGateMemo) []kernel.Endpoint {
	return h.subEndpointsMemo(s, rows, memo)
}

func (h *Handler) subRenderEndpoints(s *model.Subscription, rows []model.NodeInbound) []kernel.Endpoint {
	return h.subRenderEndpointsMemo(s, rows, nil)
}

// subEndpointsForIDs is subEndpointsMemo for an explicit inbound id set:
// same endpoint building, credential stamping and relay scope, driven
// by ids instead of the subscription's own sources.
func (h *Handler) subEndpointsForIDs(s *model.Subscription, rows []model.NodeInbound, memo *companionGateMemo, ids []int64) []kernel.Endpoint {
	want := map[int64]bool{}
	for _, id := range ids {
		want[id] = true
	}
	selected := make([]model.NodeInbound, 0, len(want))
	for _, row := range rows {
		// Companion inbounds are never subscription sources: they only
		// serve origins through the retargeting below.
		if want[row.ID] && row.CompanionOf == 0 {
			selected = append(selected, row)
		}
	}
	endpoints, _ := h.inboundEndpoints(selected)
	if s != nil && isShare(s) {
		ku := kernelUser(s)
		companions := h.companionsByOrigin(memo)
		kept := endpoints[:0]
		for i := range endpoints {
			e := endpoints[i]
			// SS metering: retarget direct SS endpoints whose origin
			// has a live companion to the companion (its port and
			// identity PSK); ForUser below then stamps the combined
			// iPSK:uPSK password. Relay endpoints keep pointing at
			// the origin — the relay forwards land on the origin's
			// port, with the shared password, as before.
			if len(companions) > 0 && e.Direct && e.Proxy == nil && e.RelayID == 0 &&
				e.Inbound.Protocol == kernel.ProtocolShadowsocks {
				if comp, ok := companions[e.Inbound.ID]; ok {
					e.CompanionOf = e.Inbound.ID
					e.Inbound = comp
					e.Port = comp.Port
				}
			}
			if e.Proxy == nil {
				e.Inbound = e.Inbound.ForUser(ku)
			}
			// F5 (C-03): fail closed when the per-subscription
			// credential cannot be derived for a companion endpoint
			// (ForUser leaves the bare identity PSK in place — no ':'
			// separator; SS2022 PSKs are base64 and never contain
			// one). The bare iPSK is a working single-user credential
			// and must never be served; the render side likewise
			// skips underivable users.
			if e.CompanionOf > 0 && !strings.Contains(e.Inbound.Settings.Password, ":") {
				log.Printf("subscription %d: drop companion endpoint of inbound %d: per-subscription credential underivable", s.ID, e.Inbound.ID)
				continue
			}
			kept = append(kept, e)
		}
		endpoints = kept
	}
	if s == nil {
		return endpoints
	}
	// 中转范围在装配层先过滤一遍（计数/预览/拉取共用此口径）；
	// 渲染流水线里 kernel.FilterEndpoints 还会再过一次，幂等。
	// relayIds 白名单非空时是精确端点集过滤并优先于 lines 三档，
	// 无白名单时才按 lines 收紧（见 kernel.FilterEndpoints）。
	return kernel.FilterEndpoints(endpoints, parsePipeline(s.Options).options())
}

// filterInboundRowsByUserAssignments 保留用户被分配的入站行
// （openSubscription ?user= 拉取侧第一段过滤，FIX2-B/V5-1）。
// B10-001 起 mine 计数不再共用本过滤：mine 的卡片是独立链接
// （无 ?user=），按全量口径计数，见 userSubscriptionMine。
func filterInboundRowsByUserAssignments(rows []model.NodeInbound, assignments []repo.UserInboundAssignment) []model.NodeInbound {
	allowedInbound := make(map[int64]bool, len(assignments))
	for _, a := range assignments {
		allowedInbound[a.InboundID] = true
	}
	filtered := make([]model.NodeInbound, 0, len(rows))
	for _, row := range rows {
		if allowedInbound[row.ID] {
			filtered = append(filtered, row)
		}
	}
	return filtered
}

// filterEndpointsByUserAssignments 按用户分配收紧端点（拉取侧第二段
// 过滤）：中转端点需该 relay 的分配，直连端点需 relayID=0 的分配。
func filterEndpointsByUserAssignments(endpoints []kernel.Endpoint, assignments []repo.UserInboundAssignment) []kernel.Endpoint {
	directAllowed := make(map[int64]bool)
	relayAllowed := make(map[int64]bool)
	for _, a := range assignments {
		if a.RelayID == 0 {
			directAllowed[a.InboundID] = true
		} else {
			relayAllowed[a.RelayID] = true
		}
	}
	filtered := make([]kernel.Endpoint, 0, len(endpoints))
	for _, e := range endpoints {
		if e.RelayID > 0 {
			if relayAllowed[e.RelayID] {
				filtered = append(filtered, e)
			}
		} else if e.Direct {
			// Assignments are written against the origin inbound; an
			// SS endpoint retargeted to its metering companion still
			// answers to the origin's id.
			id := e.Inbound.ID
			if e.CompanionOf > 0 {
				id = e.CompanionOf
			}
			if directAllowed[id] {
				filtered = append(filtered, e)
			}
		}
	}
	return filtered
}

// ---- API ----

type subscriptionView struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	Kind       string  `json:"kind"`
	Token      string  `json:"token"`
	Path       string  `json:"path"`
	SourceAll  bool    `json:"sourceAll"`
	InboundIDs []int64 `json:"inboundIds"`
	SourceIDs  []int64 `json:"sourceIds"`
	// SharedIDs are selected inbounds of a share subscription that can't
	// carry per-user credentials (Shadowsocks, imported nodes).
	SharedIDs  []int64     `json:"sharedIds"`
	Options    subPipeline `json:"options"`
	ExpireTime int64       `json:"expireTime"`
	Enabled    bool        `json:"enabled"`
	Active     bool        `json:"active"`
	// PendingDelete（R10-02/I-F3-01）：删除墓碑行对管理员可见——
	// 此前墓碑与普通已禁用订阅在列表中同形、带完整编辑入口，
	// 管理员把残留行重新启用即打穿删除完成路径的前提。前端据此
	// 呈「删除中」态并禁用开关；后端 save/reset/resetTraffic 对
	// 墓碑行一律拒绝（见各入口守卫）。
	PendingDelete bool   `json:"pendingDelete"`
	Remark        string `json:"remark"`
	NodeCount     int    `json:"nodeCount"`
	// Traffic (share subscriptions; bytes).
	UploadBytes   int64 `json:"uploadBytes"`
	DownloadBytes int64 `json:"downloadBytes"`
	TrafficLimit  int64 `json:"trafficLimit"`
	ResetDay      int   `json:"resetDay"`
	// TrafficSince is when the used-traffic counters started counting
	// (creation or last reset), resolved via subTrafficSince.
	TrafficSince int64 `json:"trafficSince"`
	OverQuota    bool  `json:"overQuota"`
	// IsDefault 标记"默认订阅"（个人订阅链接的载体）：排序第一条，由 subscriptionList 回填。
	IsDefault bool `json:"isDefault"`
	// ExcludedCompanionIDs 是本次 save 校验时被排除的伴生入站 ID
	// （B-06：伴生入站不能作订阅源，B10-002：排除须对管理员可见）。
	// 仅 subscriptionSave 的响应回填；列表等其他视图恒空、omitempty 不出现。
	ExcludedCompanionIDs []int64 `json:"excludedCompanionIds,omitempty"`
	// MeteringUnsupported（L4-08）：订阅 ID 超出 socket mark 计量
	// 编码上限（kernel.MaxUserMarkID）时为 true——该订阅的流量永不
	// 计量、TrafficLimit 永不触发，须对管理员显式可见，不许静默。
	// 视图构建单点回填，列表/保存/详情同口径。
	MeteringUnsupported bool `json:"meteringUnsupported,omitempty"`
}

// subFormatAlias resolves format aliases shared by the pull side
// (openSubscription) and the count side (nodeCountFormat) — X-P3-2
// (B-F2): the two used to resolve aliases independently and the count
// side missed stash, silently falling back to base64 counts for a
// format clients actually receive as Clash. Add new aliases here only.
//
// 1.4.0 (DA-7): stash is no longer aliased to Clash — it resolves to
// the first-class kernel.FormatStash on BOTH sides (pull renders the
// Stash variant; SubscriptionNodeCount counts it through the same
// ClashProxy gate), keeping the count/actual equality X-P3-2 restored.
func subFormatAlias(format string) string {
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "stash" {
		return kernel.FormatStash
	}
	return format
}

// nodeCountFormat resolves the format a subscription view's NodeCount is
// computed for (A5-02/BC-11): the request's explicit format when it names
// a supported one, else the format a bare UA-less subscription request
// yields — base64. An unknown value falls back instead of failing the
// whole view.
func nodeCountFormat(format string) string {
	// FIX2-B（V5-2）：与拉取侧（openSubscription 先 ToLower）同口径
	// 大小写不敏感；此前 "Clash" 在计数侧静默回落 base64、与实得
	// 格式分叉且无任何提示。
	format = subFormatAlias(format)
	switch format {
	case kernel.FormatClash, kernel.FormatStash, kernel.FormatSingbox, kernel.FormatSurge,
		kernel.FormatLoon, kernel.FormatQX, kernel.FormatShadowrocket, kernel.FormatBase64:
		return format
	}
	return kernel.FormatBase64
}

func (h *Handler) subscriptionView(s *model.Subscription, rows []model.NodeInbound, now time.Time, format string) subscriptionView {
	return h.subscriptionViewMemo(s, rows, now, format, nil)
}

// subscriptionViewMemo is subscriptionView with an F4 gate memo
// (Y-01): list handlers pass one memo for the whole request so the
// applied gate is evaluated once per kernel across all views in the
// response, instead of twice per view.
func (h *Handler) subscriptionViewMemo(s *model.Subscription, rows []model.NodeInbound, now time.Time, format string, memo *companionGateMemo) subscriptionView {
	return h.subscriptionViewForAssignmentsMemo(s, rows, now, format, nil, memo)
}

// subscriptionViewForAssignments 在 subscriptionView 基础上按用户入站
// 分配收紧 NodeCount（FIX2-B/V5-1）：assignments 为 nil 表示全量
// 口径；非 nil（含空集）时先按拉取侧同一组过滤函数收紧行与端点，
// 使显示数与该用户经 ?user=&sig= 个性化链接实际拉到的节点数恒等。
// 空集 = 该用户个性化链接拉到 0 个节点。注意「我的订阅」（mine）
// 呈现的是独立链接而非个性化链接，B10-001 起 mine 恒传 nil 按
// 全量口径计数；本收紧口径只对个性化链接视图成立。
func (h *Handler) subscriptionViewForAssignments(s *model.Subscription, rows []model.NodeInbound, now time.Time, format string, assignments []repo.UserInboundAssignment) subscriptionView {
	return h.subscriptionViewForAssignmentsMemo(s, rows, now, format, assignments, nil)
}

func (h *Handler) subscriptionViewForAssignmentsMemo(s *model.Subscription, rows []model.NodeInbound, now time.Time, format string, assignments []repo.UserInboundAssignment, memo *companionGateMemo) subscriptionView {
	if memo == nil {
		memo = &companionGateMemo{}
	}
	if assignments != nil {
		rows = filterInboundRowsByUserAssignments(rows, assignments)
	}
	ids := h.subAllInboundIDs(s, rows, memo)
	byID := make(map[int64]model.NodeInbound, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}
	shared := []int64{}
	if isShare(s) {
		companions := h.companionsByOrigin(memo)
		for _, id := range ids {
			row := byID[id]
			if isExternalInbound(&row) {
				shared = append(shared, id)
			} else if _, metered := companions[id]; metered {
				// SS origin with a live metering companion: new pulls
				// carry per-subscription credentials and are counted,
				// so the inbound no longer counts as shared for this
				// subscription.
				continue
			} else if in, err := inboundFromModel(row); err == nil && !in.SupportsUsers() {
				shared = append(shared, id)
			}
		}
	}
	selected := []int64{}
	sourceIDs := []int64{}
	if s.SourceAll != 1 {
		selected = append(selected, subExplicitIDs(s, rows)...)
		sourceIDs = append(sourceIDs, parseIDList(s.SourceIDs)...)
	}
	pipe := parsePipeline(s.Options)
	// A5-02/BC-11：NodeCount 按目标格式的实际渲染数计（与该格式的客户
	// 端实得一致），不再是格式过滤前的终结点数。sing-box 口径按现行
	// 客户端（1.14+，含导入 Snell）计，与真机实测对齐。
	format = nodeCountFormat(format)
	countOpts := pipe.options()
	if format == kernel.FormatSingbox {
		countOpts.SingboxSnell = true
		// 1.14+ 口径含 Naive（≥1.13），与 SingboxSnell 同置 true。
		countOpts.SingboxNaive = true
	}
	endpoints := h.subRenderEndpointsMemo(s, rows, memo)
	if len(assignments) > 0 {
		endpoints = filterEndpointsByUserAssignments(endpoints, assignments)
	}
	return subscriptionView{
		ID: s.ID, Name: s.Name, Kind: s.Kind, Token: s.Token, Path: "/api/v1/open_api/sub/" + s.Token,
		SourceAll: s.SourceAll == 1, InboundIDs: selected, SourceIDs: sourceIDs, SharedIDs: shared, Options: pipe,
		ExpireTime: s.ExpireTime, Enabled: s.Enabled == 1, Active: subActive(s, now), PendingDelete: s.PendingDelete == 1, Remark: s.Remark,
		NodeCount:   kernel.SubscriptionNodeCount(format, endpoints, countOpts),
		UploadBytes: s.UploadBytes, DownloadBytes: s.DownloadBytes, TrafficLimit: s.TrafficLimit,
		ResetDay: s.ResetDay, TrafficSince: subTrafficSince(s), OverQuota: subOverQuota(s),
		MeteringUnsupported: isShare(s) && s.ID > kernel.MaxUserMarkID,
	}
}

func (h *Handler) subscriptionList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	subs, err := h.repo.ListSubscriptions()
	if err != nil {
		respondInternalError(w, "subscriptionList", err)
		return
	}
	if len(subs) == 0 {
		// Start everyone off with one; deleting it later is the user's call.
		def, err := h.repo.DefaultSubscription(time.Now().UnixMilli())
		if err != nil {
			respondInternalError(w, "subscriptionList", err)
			return
		}
		subs = append(subs, *def)
	}
	rows, err := h.repo.ListNodeInbounds()
	if err != nil {
		respondInternalError(w, "subscriptionList", err)
		return
	}
	// 请求体可选：format 指定 NodeCount 的目标格式口径（A5-02/BC-11）。
	var listReq struct {
		Format string `json:"format"`
	}
	_ = decodeJSON(r.Body, &listReq)
	now := time.Now()
	// One F4 gate memo for the whole list request (Y-01): the applied
	// gate is evaluated once per kernel across all views rendered here.
	gateMemo := &companionGateMemo{}
	items := make([]subscriptionView, 0, len(subs))
	for i := range subs {
		items = append(items, h.subscriptionViewMemo(&subs[i], rows, now, listReq.Format, gateMemo))
	}
	// 回填默认订阅标记（个人订阅链接的载体 = 排序第一条）。DefaultSubscription
	// 在无订阅时会新建一条，与上方空列表分支的既有行为一致；此处失败不阻断
	// 列表返回，标记保持 false。
	if def, derr := h.repo.DefaultSubscription(now.UnixMilli()); derr == nil && def != nil {
		for i := range items {
			items[i].IsDefault = items[i].ID == def.ID
		}
	}
	response.WriteJSON(w, response.OK(map[string]interface{}{
		"items": items,
	}))
}

type subscriptionRequest struct {
	ID         int64       `json:"id"`
	Name       string      `json:"name"`
	Kind       string      `json:"kind"`
	SourceAll  bool        `json:"sourceAll"`
	InboundIDs []int64     `json:"inboundIds"`
	SourceIDs  []int64     `json:"sourceIds"`
	Options    subPipeline `json:"options"`
	ExpireTime int64       `json:"expireTime"`
	Enabled    *bool       `json:"enabled"`
	Remark     string      `json:"remark"`
	// TrafficLimit is in bytes (0 = unlimited); ResetDay 1-28 (0 = never).
	TrafficLimit int64 `json:"trafficLimit"`
	ResetDay     int   `json:"resetDay"`
	// Format optionally names the target format for the returned view's
	// NodeCount (A5-02/BC-11); it is not persisted.
	Format string `json:"format"`
}

// validate checks the request and returns the inbound ids it selects.
// It discards the excluded-companion detail validateDetailed reports;
// callers that render a response use validateDetailed so the exclusion
// is visible to the caller (B10-002).
func (req *subscriptionRequest) validate(rows []model.NodeInbound) ([]int64, error) {
	ids, _, err := req.validateDetailed(rows)
	return ids, err
}

// validateDetailed is validate plus the companion inbound ids it
// excluded. Inbound ids that do not exist at all are a hard error
// (B10-002: silently dropping them left admins believing a mistyped
// or deleted inbound was selected — the assignment surface
// userSubscriptionSet refuses unknown ids outright, and save now
// matches that stance). Companion ids keep the B-06 exclusion (they can
// never be subscription sources), but are returned so the save/preview
// responses can name them instead of dropping them silently.
func (req *subscriptionRequest) validateDetailed(rows []model.NodeInbound) ([]int64, []int64, error) {
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len([]rune(req.Name)) > 100 {
		return nil, nil, errText("请填写名称（最多 100 个字）")
	}
	// Every subscription has its own credentials.
	req.Kind = model.SubscriptionShare
	if req.ExpireTime < 0 {
		req.ExpireTime = 0
	}
	if req.TrafficLimit < 0 {
		req.TrafficLimit = 0
	}
	if req.ResetDay < 0 || req.ResetDay > 28 {
		return nil, nil, errText("每月重置日只能是 1–28 号")
	}
	if err := req.Options.normalize(); err != nil {
		return nil, nil, err
	}
	// Companion inbounds are never subscription sources (the same
	// choke point subInboundIDs enforces at read time): exclude them
	// here so a crafted save cannot store an id every reader would
	// then silently drop (B-06; the assignment surface refuses them
	// outright). Ids that match no inbound row at all are a different
	// case — a typo or a since-deleted inbound — and are refused
	// outright (B10-002), never silently dropped.
	known := make(map[int64]bool, len(rows))
	companion := make(map[int64]bool, len(rows))
	for _, r := range rows {
		if r.CompanionOf == 0 {
			known[r.ID] = true
		} else {
			companion[r.ID] = true
		}
	}
	var ids []int64
	var excluded []int64
	var unknown []int64
	seenExcluded := make(map[int64]bool)
	seenUnknown := make(map[int64]bool)
	for _, id := range req.InboundIDs {
		switch {
		case known[id]:
			ids = append(ids, id)
		case companion[id]:
			if !seenExcluded[id] {
				seenExcluded[id] = true
				excluded = append(excluded, id)
			}
		case !seenUnknown[id]:
			seenUnknown[id] = true
			unknown = append(unknown, id)
		}
	}
	if len(unknown) > 0 {
		return nil, nil, errText(fmt.Sprintf("入站不存在: %v", unknown))
	}
	var sources []int64
	for _, id := range req.SourceIDs {
		if id > 0 {
			sources = append(sources, id)
		}
	}
	req.SourceIDs = sources
	if !req.SourceAll && len(ids) == 0 && len(sources) == 0 {
		return nil, nil, errText("请至少选择一个入站或订阅源，或选择全部")
	}
	return ids, excluded, nil
}

// subscriptionSourceRefError 是订阅源引用校验的定型失败：文案由
// 未知源 ID 列表唯一确定，不含仓储原文。调用方以 errors.As 分流
// 后原样回执（msg 字段访问），仓储故障仍走通用文案。R2-A01：
// TestR4F1SubscriptionSourceRefIntegrity 的定型恢复（取代此前
// errText 形态——errText 在本函数外仍用于纯校验面，见 validateDetailed）。
type subscriptionSourceRefError struct {
	msg string
}

func (e *subscriptionSourceRefError) Error() string { return e.msg }

// checkSubscriptionSourceRefs（R4-F1-01）：订阅对订阅源的引用在保存
// 时验存在——SourceIDs 此前仅过滤正数，未验的幽灵 ID 被计入「至少
// 选一个」的非空判定后落库、渲染为空且无人知晓。与入站（B10-002
// 「入站不存在」）同口径：未知源 ID 硬拒并点名。删除面的对称处置
// （删源时清理引用并回执点名）见 remoteSourceDelete。（2.1.2 起
// Options 内的链式按源引用已随订阅链式删除，本检查只剩 SourceIDs。）
func (h *Handler) checkSubscriptionSourceRefs(req *subscriptionRequest) error {
	if len(req.SourceIDs) == 0 {
		return nil
	}
	sources, err := h.repo.ListRemoteSources()
	if err != nil {
		// R2-F4-01：仓储直通，标记后由调用方守卫泛化。
		return markRepoError(err)
	}
	known := make(map[int64]bool, len(sources))
	for _, s := range sources {
		known[s.ID] = true
	}
	var unknown []int64
	seen := make(map[int64]bool)
	for _, id := range req.SourceIDs {
		if id > 0 && !known[id] && !seen[id] {
			seen[id] = true
			unknown = append(unknown, id)
		}
	}
	if len(unknown) > 0 {
		return &subscriptionSourceRefError{msg: fmt.Sprintf("订阅源不存在: %v", unknown)}
	}
	return nil
}

// relayOnlySelectedWarning is the save receipt's naming of the
// selected inbounds that can never appear in the subscription
// (R2-02 防静默面 ①, user decision 2026-11-11: warn only, never
// materialize endpoints or change scope semantics). A relay-only
// inbound serves no direct endpoint (see inboundEndpoints); without
// a live relay endpoint of its own it is absent under every scope —
// before this warning the save answered 操作成功， the pulls came
// back empty and nothing anywhere said why. Inbounds that do have a
// live relay endpoint are NOT named: they appear through it, so the
// warning's claim stays exact. "" when nothing is lost.
func (h *Handler) relayOnlySelectedWarning(s *model.Subscription, rows []model.NodeInbound) string {
	ids := h.subAllInboundIDs(s, rows, nil)
	if len(ids) == 0 {
		return ""
	}
	live := make(map[int64]bool)
	relays, err := h.relayViews(0)
	if err != nil {
		// 与装配面同口径：中转视图查询失败已由 inboundEndpoints
		// 记日志（M-003）；此处按无在册中转端点继续点名——回执宁可
		// 多点一个实际经坏视图丢失的节点，不可再静默。
		log.Printf("relayOnlySelectedWarning: relayViews failed for subscription %d: %v", s.ID, err)
	} else {
		for _, rel := range relays {
			if rel.ForwardExists && rel.ForwardStatus == 1 && len(rel.Entries) > 0 {
				live[rel.InboundID] = true
			}
		}
	}
	byID := make(map[int64]model.NodeInbound, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}
	var names []string
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		if row, ok := byID[id]; ok && row.RelayOnly == 1 && !live[id] {
			names = append(names, row.Name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return fmt.Sprintf("以下节点为纯中转导入节点，不可订阅、不会出现在订阅中：%s", strings.Join(names, "、"))
}

// customEmptyRelayFallbackWarning names the A-01 (AUDIT-R215) fallback
// on the save receipt: the request carried relayIds as an explicit
// empty set (custom mode, zero endpoints checked), so FilterEndpoints
// falls back to the three-tier Lines scope by established semantics
// (user_subscription_test.go:190 pins the fallback; it is not changed
// here). Key-in-field vs absent is what tells the two apart: a
// three-tier save sends no relayIds key (nil), a custom-empty save
// sends relayIds: [] (non-nil, length 0). "" when no fallback happened.
func customEmptyRelayFallbackWarning(opts subPipeline) string {
	if opts.RelayIDs == nil || len(opts.RelayIDs) > 0 {
		return ""
	}
	return fmt.Sprintf("自定义中转范围已选 0 个端点，将按三档「%s」回落下发", relayScopeTierName(opts.Lines))
}

func relayScopeTierName(lines string) string {
	switch lines {
	case "relay":
		return "只要中转"
	case "direct":
		return "只要直连"
	default:
		return "全部"
	}
}

func (h *Handler) subscriptionSave(w http.ResponseWriter, r *http.Request) {
	// R2-K2 family closure (B11-001): the response awaits
	// applyKernelsForInbounds for the saved subscription's kernels,
	// each with the conditional kernelApplyTimeout bound; arm the
	// write relief.
	extendWriteDeadline(w, kernelApplyWriteDeadline)
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req subscriptionRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	rows, err := h.repo.ListNodeInbounds()
	if err != nil {
		respondInternalError(w, "subscriptionSave", err)
		return
	}
	ids, excludedCompanions, err := req.validateDetailed(rows)
	if err != nil {
		// RAW291 Type C：validateDetailed 只产 errText 固定校验
		// 文案（名称/重置日/入站不存在等用户输入校验），原样回。
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	if err := h.checkSubscriptionSourceRefs(&req); err != nil {
		// R2-A01：未知订阅源定型文案原样回（TestR4F1），仓储故障才泛化。
		var sre *subscriptionSourceRefError
		if errors.As(err, &sre) {
			response.WriteJSON(w, response.ErrDefault(sre.msg))
			return
		}
		respondInternalError(w, "subscriptionSave", err)
		return
	}
	if err := h.checkPipelineTemplates(req.Options); err != nil {
		// RAW291 Type C：checkPipelineTemplates 只产 errText
		// 固定校验文案（模板不存在），原样回。
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	// X-P3-27（L-11-2）：新建分支接入 Idempotency-Key 机制（更新分支
	// req.ID>0 不参与，更新本身按 ID 定位、无防重语义变化）。
	if req.ID == 0 {
		var finish func()
		var replayed bool
		w, finish, replayed = h.idemBegin(w, r, idemUserID(r), "subscription:create")
		defer finish()
		if replayed {
			return
		}
	}

	now := time.Now()
	var s *model.Subscription
	var before []int64
	if req.ID > 0 {
		if s, err = h.repo.GetSubscription(req.ID); err != nil || s == nil {
			response.WriteJSON(w, response.ErrDefault("订阅不存在"))
			return
		}
		// R10-02（I-F3-01）：墓碑是吸收态——删除完成前拒绝一切保存，
		// 不许把墓碑改回启用（Enabled=1∧PendingDelete=1 会让凭据重入
		// payload、删除完成后成无人撤销的孤儿）。
		if s.PendingDelete == 1 {
			response.WriteJSON(w, response.ErrDefault("订阅删除中，无法保存"))
			return
		}
		if isShare(s) {
			before = h.subAllInboundIDs(s, rows, nil)
		}
	} else {
		token, tokenErr := randomHex(16)
		if tokenErr != nil {
			// RAW291：随机源失败为内部错误，原文只记日志。
			respondInternalError(w, "subscriptionSave: randomHex failed", tokenErr)
			return
		}
		s = &model.Subscription{Token: token, Enabled: 1, CreatedTime: now.UnixMilli(), TrafficSince: now.UnixMilli()}
	}
	s.Name = req.Name
	s.Kind = req.Kind
	if isShare(s) && (s.UUID == "" || s.Password == "") {
		uuid, uuidErr := newUUID()
		if uuidErr != nil {
			// RAW291：随机源失败为内部错误，原文只记日志。
			respondInternalError(w, "subscriptionSave: newUUID failed", uuidErr)
			return
		}
		pw, pwErr := randomHex(16)
		if pwErr != nil {
			// RAW291：同上。
			respondInternalError(w, "subscriptionSave: randomHex failed", pwErr)
			return
		}
		s.UUID, s.Password = uuid, pw
	}
	s.SourceAll = boolToInt(req.SourceAll)
	s.InboundIDs = formatIDList(ids)
	s.SourceIDs = formatIDList(req.SourceIDs)
	opts, _ := json.Marshal(req.Options)
	s.Options = string(opts)
	s.ExpireTime = req.ExpireTime
	s.Remark = strings.TrimSpace(req.Remark)
	s.TrafficLimit = req.TrafficLimit
	// X-P3-24（L-09-6）：重置时间与订阅本体同事务落库（见
	// SaveSubscriptionWithResetTime），不再先 Mark 后 Save 分两步。
	resetDayChanged := req.ResetDay != s.ResetDay && req.ResetDay > 0
	if resetDayChanged {
		// Count from now: a newly set reset day must not wipe this month.
		s.TrafficResetTime = now.UnixMilli()
		s.TrafficSince = now.UnixMilli()
	}
	s.ResetDay = req.ResetDay
	if req.Enabled != nil {
		s.Enabled = boolToInt(*req.Enabled)
	}
	s.UpdatedTime = now.UnixMilli()
	// AUDIT-R8 (F7X-001): 不再乐观标记 AppliedActive。先保存（保持旧值），
	// 下发成功后直接更新 AppliedActive；失败则不更新，留待 syncSubscriptions 循环重试。
	newAppliedActive := boolToInt(isShare(s) && subActive(s, now))
	var saveErr error
	if resetDayChanged && s.ID > 0 {
		saveErr = h.repo.SaveSubscriptionWithResetTime(s, s.TrafficResetTime)
	} else {
		saveErr = h.repo.SaveSubscription(s)
	}
	if saveErr != nil {
		respondInternalError(w, "subscriptionSave", saveErr)
		return
	}
	push := before
	if isShare(s) {
		push = append(push, h.subAllInboundIDs(s, rows, nil)...)
	}
	// B10-002：响应视图回填本次被排除的伴生入站，管理员可见。
	view := h.subscriptionView(s, rows, now, req.Format)
	view.ExcludedCompanionIDs = excludedCompanions
	// R2-02（防静默面 ①）：所选节点含无在册中转端点的纯中转导入
	// 节点时，回执 warning 逐个点名——它们在任何范围档位下都不
	// 会出现在订阅中，保存面是管理员当刻唯一能得知的面。下发成
	// 功/失败两条出口同带（warning 为 omitempty，无点名时信封与
	// 此前逐字一致）。
	warning := h.relayOnlySelectedWarning(s, rows)
	// A-01 (AUDIT-R215): 自定义空勾选回落须在保存回执点名——界面
	// 当刻显示「已选 0 个端点」，拉取却按三档出端点；不点名即静默。
	// 两条 warning 同属一个回执，以「；」并成一条（前端单 toast 透出）。
	if fb := customEmptyRelayFallbackWarning(req.Options); fb != "" {
		if warning != "" {
			warning += "；"
		}
		warning += fb
	}
	if err := h.applyKernelsForInbounds(push); err != nil {
		// 下发失败：不更新 AppliedActive，循环会重试；仅提示用户。
		// RAW291：仓储原文经 pushFailureMsg 换成固定文案。
		response.WriteJSON(w, response.R{Code: 0, Msg: pushFailureMsg("已保存，但下发到节点失败", err), Data: view, TS: time.Now().UnixMilli(), Warning: warning})
		return
	}
	// 下发成功：更新 AppliedActive。
	_ = h.repo.UpdateSubscriptionApplied(s.ID, newAppliedActive)
	s.AppliedActive = newAppliedActive
	response.WriteJSON(w, response.R{Code: 0, Msg: "操作成功", Data: view, TS: time.Now().UnixMilli(), Warning: warning})
}

func (h *Handler) respondAfterSubPush(w http.ResponseWriter, inboundIDs []int64, data interface{}) {
	msg := "操作成功"
	if err := h.applyKernelsForInbounds(inboundIDs); err != nil {
		// RAW291：仓储原文经 pushFailureMsg 换成固定文案。
		msg = pushFailureMsg("已保存，但下发到节点失败", err)
	}
	response.WriteJSON(w, response.R{Code: 0, Msg: msg, Data: data, TS: time.Now().UnixMilli()})
}

func (h *Handler) subscriptionDelete(w http.ResponseWriter, r *http.Request) {
	id := idFromBody(r, w)
	if id <= 0 {
		return
	}
	s, err := h.repo.GetSubscription(id)
	if err != nil {
		respondInternalError(w, "subscriptionDelete", err)
		return
	}
	if s == nil {
		// X-P3-28（L-11-3，V-12）：不存在幂等成功。
		response.WriteJSON(w, response.OKEmpty())
		return
	}
	// B4/F-058: 删除改为两阶段。此前行先删、下发失败仅改响应文案，行已
	// 不在，60s 同步循环无从重试，节点侧凭据成为孤儿。现在先落墓碑
	// （pending_delete + 禁用，链接立即失效、凭据退出 payload），下发
	// 成功后才硬删；失败则墓碑保留，由 syncSubscriptions 循环重试完成。
	// B4/F-136: 硬删与清分配在同一 repo 事务内完成（旧两步第二步失败
	// 仅打日志、分配行残留）。
	if err := h.repo.MarkSubscriptionPendingDelete(id); err != nil {
		respondInternalError(w, "subscriptionDelete", err)
		return
	}
	s.PendingDelete = 1
	s.Enabled = 0
	if err := h.completePendingSubscriptionDelete(s); err != nil {
		log.Printf("subscription %d: delete push failed, tombstone kept for retry: %v", id, err)
		// RAW291：仓储原文经 pushFailureMsg 换成固定文案。
		response.WriteJSON(w, response.R{Code: 0, Msg: pushFailureMsg("已标记删除，但下发到节点失败，后台将自动重试", err), Data: nil, TS: time.Now().UnixMilli()})
		return
	}
	response.WriteJSON(w, response.R{Code: 0, Msg: "操作成功", Data: nil, TS: time.Now().UnixMilli()})
}

// completePendingSubscriptionDelete pushes the credential removal for a
// tombstoned subscription and, only on success, hard-deletes the row
// together with its assignments in one transaction. The push list is
// computed from the subscription's (former) inbounds; the payload itself
// is rendered from current DB state, in which the tombstone is excluded
// by construction: rawActiveUsersByInbound always skips pending-delete
// rows (R10-02), regardless of their enabled flag.
func (h *Handler) completePendingSubscriptionDelete(s *model.Subscription) error {
	var push []int64
	if isShare(s) {
		rows, err := h.repo.ListNodeInbounds()
		if err != nil {
			// RAW291：仓储直通错误标 repoError，调用点凭此标记
			// 把仓储原文换成固定文案；下发错误不经此标记。
			return markRepoError(err)
		}
		push = h.subAllInboundIDs(s, rows, nil)
	}
	if err := h.applyKernelsForInbounds(push); err != nil {
		return err
	}
	if err := h.repo.DeleteSubscriptionWithAssignments(s.ID); err != nil {
		// RAW291：同上。
		return markRepoError(err)
	}
	// P1-07: the row is gone, so its one-shot warning dedupe entry can
	// be reclaimed (a future subscription reusing the id must warn
	// again rather than inherit the silence).
	uncountableSubWarned.Delete(s.ID)
	return nil
}

// rotateSubscriptionCredentials is the one full reset rotation: new
// token, new UUID/password for share subscriptions, persisted, then
// pushed to the kernels. X-P3-13（L-03-2）：主路径 subscriptionReset
// 与 legacy inboundSubInfo reset 共用此实现——legacy 此前只换 token、
// 不下发，经它重置的旧配置其实仍有效；不另写第二套轮换。
// Returns the inbound rows (for view rendering), a push error (the
// rotation itself is already persisted; AppliedActive is parked at -1
// so the sync loop retries the push), or a persistence error.
func (h *Handler) rotateSubscriptionCredentials(s *model.Subscription) (rows []model.NodeInbound, applyErr error, err error) {
	// R10-02（I-F3-01）：轮换咽喉点同守墓碑——主路径 subscriptionReset
	// 与 legacy inboundSubInfo reset 共用此实现，任一面都不得对墓碑
	// 轮换凭据（行将被硬删、轮换产物指向 404）。
	if s.PendingDelete == 1 {
		return nil, nil, errText("订阅删除中，无法重置")
	}
	// 集成收口：随机源失败 fail-closed（批次 8 X-P3-14 的新签名）与
	// 共享全轮换（批次 7 X-P3-13）在此合并——生成失败直接报错返回，
	// 不落库、不下发。
	token, tokenErr := randomHex(16)
	if tokenErr != nil {
		return nil, nil, tokenErr
	}
	s.Token = token
	if isShare(s) {
		uuid, uuidErr := newUUID()
		if uuidErr != nil {
			return nil, nil, uuidErr
		}
		pw, pwErr := randomHex(16)
		if pwErr != nil {
			return nil, nil, pwErr
		}
		s.UUID, s.Password = uuid, pw
	}
	s.UpdatedTime = time.Now().UnixMilli()
	if err := h.repo.SaveSubscription(s); err != nil {
		// RAW291：仓储直通错误标 repoError，调用点 guard 后
		// 改走通用文案；随机源失败为固定文案不经此标记。
		return nil, nil, markRepoError(err)
	}
	rows, listErr := h.repo.ListNodeInbounds()
	if listErr != nil {
		// R3-MR-01：入站读失败时下发集不可知——按下方 push 失败同
		// 漏斗处置：AppliedActive 停在 -1 待 sync 循环重试、把错误
		// 作为 applyErr 回执披露（pushFailureMsg 对仓储错误只留固定
		// 文案）。禁止拿空集继续：那会让 applyKernels 空转「成功」
		// 并记成已下发，新凭据永不下发、旧凭据继续有效却报成功。
		_ = h.repo.UpdateSubscriptionApplied(s.ID, -1)
		return nil, markRepoError(listErr), nil
	}
	var push []int64
	if isShare(s) {
		push = h.subAllInboundIDs(s, rows, nil)
	}
	// AUDIT-R8 (R5R8-001): 凭据轮换后下发失败时，强制标记 AppliedActive=-1（需同步），
	// 使 syncSubscriptions 循环的 active != AppliedActive 恒成立从而重试。
	// 成功后恢复正常值。否则旧凭据永不失效（F7.14 承诺被违背）。
	if err := h.applyKernelsForInbounds(push); err != nil {
		_ = h.repo.UpdateSubscriptionApplied(s.ID, -1)
		return rows, err, nil
	}
	_ = h.repo.UpdateSubscriptionApplied(s.ID, boolToInt(isShare(s) && subActive(s, time.Now())))
	return rows, nil, nil
}

// subscriptionReset issues a new link; a share subscription also gets new
// credentials, so a leaked link or imported config stops working.
func (h *Handler) subscriptionReset(w http.ResponseWriter, r *http.Request) {
	// C1-06：凭据轮换接入幂等——同键重试回放首轮回执（含首轮新
	// token），不再二次轮换使刚分发的链接失效（scope 登记见
	// idempotency.go）。
	var finish func()
	var replayed bool
	w, finish, replayed = h.idemBegin(w, r, idemUserID(r), "subscription:reset")
	defer finish()
	if replayed {
		return
	}
	id := idFromBody(r, w)
	if id <= 0 {
		return
	}
	s, err := h.repo.GetSubscription(id)
	if err != nil || s == nil {
		response.WriteJSON(w, response.ErrDefault("订阅不存在"))
		return
	}
	// R10-02（I-F3-01）：墓碑行拒绝轮换——对即将硬删的行换凭据只会
	// 产出一枚指向 404 的新 token，且轮换下发面同属墓碑可达操作面。
	if s.PendingDelete == 1 {
		response.WriteJSON(w, response.ErrDefault("订阅删除中，无法重置"))
		return
	}
	rows, applyErr, err := h.rotateSubscriptionCredentials(s)
	if err != nil {
		respondInternalError(w, "subscriptionReset", err)
		return
	}
	if applyErr != nil {
		// RAW291：仓储原文经 pushFailureMsg 换成固定文案。
		response.WriteJSON(w, response.R{Code: 0, Msg: pushFailureMsg("已重置，但下发到节点失败", applyErr), Data: h.subscriptionView(s, rows, time.Now(), ""), TS: time.Now().UnixMilli()})
		return
	}
	response.WriteJSON(w, response.R{Code: 0, Msg: "操作成功", Data: h.subscriptionView(s, rows, time.Now(), ""), TS: time.Now().UnixMilli()})
}

// subscriptionResetTraffic zeroes a share subscription's traffic and puts
// it back on the nodes if the limit had cut it off.
func (h *Handler) subscriptionResetTraffic(w http.ResponseWriter, r *http.Request) {
	id := idFromBody(r, w)
	if id <= 0 {
		return
	}
	s, err := h.repo.GetSubscription(id)
	if err != nil || s == nil {
		response.WriteJSON(w, response.ErrDefault("订阅不存在"))
		return
	}
	// R10-02（I-F3-01）：墓碑行拒绝重置流量（同 save/reset 口径）。
	if s.PendingDelete == 1 {
		response.WriteJSON(w, response.ErrDefault("订阅删除中，无法重置流量"))
		return
	}
	now := time.Now()
	if err := h.repo.ResetSubscriptionTraffic(id, now.UnixMilli()); err != nil {
		respondInternalError(w, "subscriptionResetTraffic", err)
		return
	}
	if h.notifier != nil {
		h.notifier.clearAlerts(fmt.Sprintf("subflow:%d:", id))
		// R4X-014：同周期重置面——连带清指纹（s 为重置前已取的订阅行，
		// 名称即指纹消息前缀所需）。
		h.notifier.forgetSubQuotaFlow(s.Name)
	}
	h.syncSubscriptions(now)
	s, _ = h.repo.GetSubscription(id)
	if s == nil {
		// R10-03（I-F3-02）兜底：请求内的 syncSubscriptions 可能已完成
		// 该订阅的删除（入口读取后被标记墓碑并当场完成），回读为空
		// 属正常收口——按已删除回执返回，不对 nil 解引用渲染视图
		// （基线此处 panic、生产经 recover 变 500 且回执丢失）。
		response.WriteJSON(w, response.R{Code: 0, Msg: "操作成功，订阅已删除", Data: nil, TS: time.Now().UnixMilli()})
		return
	}
	rows, err := h.repo.ListNodeInbounds()
	if err != nil {
		// R3-MR-01：视图渲染读失败不得装空集回「已重置」——流量
		// 重置虽已落库，回执视图须诚实失败（与本函数其余读错同口径）。
		respondInternalError(w, "subscriptionResetTraffic", err)
		return
	}
	response.WriteJSON(w, response.OK(h.subscriptionView(s, rows, now, "")))
}

type subPreviewItem struct {
	Name     string `json:"name"`
	Original string `json:"original"`
	Protocol string `json:"protocol"`
	Line     string `json:"line"` // "direct" or "relay"
	// SourceID is the remote source of an imported node; Own marks the
	// panel's own nodes.
	SourceID int64 `json:"sourceId,omitempty"`
	Own      bool  `json:"own,omitempty"`
}

// subscriptionPreview runs an (unsaved) subscription's pipeline and lists
// the resulting nodes, plus how many the filters dropped.
func (h *Handler) subscriptionPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req subscriptionRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	if req.Name == "" {
		req.Name = "preview"
	}
	rows, err := h.repo.ListNodeInbounds()
	if err != nil {
		respondInternalError(w, "subscriptionPreview", err)
		return
	}
	ids, excludedCompanions, err := req.validateDetailed(rows)
	if err != nil {
		// RAW291 Type C：validateDetailed 只产 errText 固定校验
		// 文案（名称/重置日/入站不存在等用户输入校验），原样回。
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	options, _ := json.Marshal(req.Options)
	draft := &model.Subscription{SourceAll: boolToInt(req.SourceAll), InboundIDs: formatIDList(ids), SourceIDs: formatIDList(req.SourceIDs),
		Options: string(options)}
	source := h.subRenderEndpoints(draft, rows)
	opts := req.Options.options()
	result := kernel.ApplySubOptions(source, opts)
	items := make([]subPreviewItem, 0, len(result))
	for _, e := range result {
		line := "relay"
		if e.Direct {
			line = "direct"
		}
		items = append(items, subPreviewItem{Name: e.Name, Original: e.Original,
			Protocol: kernel.EndpointProtocol(e), Line: line, SourceID: e.SourceID, Own: e.Proxy == nil && e.SourceID == 0})
	}
	resp := map[string]interface{}{
		"items":   items,
		"total":   len(source),
		"dropped": len(source) - len(items),
	}
	// W3/P-003：按格式点名略过——某些协议/格式组合在某些投递格式里
	// 无表达（如 Snell 进不了 Surge），此前列表 NodeCount 静默变小、
	// 预览零提示，管理员无从知道少了谁。此处对全部格式用与计数同源
	// 的谓词（SubscriptionFormatSkipped）逐格式列出被略过的节点名，
	// 前端在预览处呈现「N 个节点不被该格式支持：…」。
	formatSkipped := map[string][]string{}
	for _, format := range kernel.SubscriptionFormats() {
		if names := kernel.SubscriptionFormatSkipped(format, source, opts); len(names) > 0 {
			formatSkipped[format] = names
		}
	}
	if len(formatSkipped) > 0 {
		resp["formatSkipped"] = formatSkipped
	}
	if len(excludedCompanions) > 0 {
		// B10-002：与 save 同口径，预览也标注被排除的伴生入站。
		resp["excludedCompanionIds"] = excludedCompanions
	}
	response.WriteJSON(w, response.OK(resp))
}

// inboundSubInfo is the pre-3.1 single-subscription API: the default
// subscription's link; reset rotates it.
func (h *Handler) inboundSubInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req struct {
		Reset bool `json:"reset"`
	}
	_ = decodeJSON(r.Body, &req)
	s, err := h.repo.DefaultSubscription(time.Now().UnixMilli())
	if err != nil {
		respondInternalError(w, "inboundSubInfo: DefaultSubscription failed", err)
		return
	}
	userID, roleID, authErr := userRoleFromRequest(r)
	if req.Reset {
		// 换 token 是管理员操作（F-001 回归点：reset 仍仅管理员）。
		if authErr != nil || roleID != 0 {
			response.WriteJSON(w, response.Err(403, "无权限，仅管理员可操作"))
			return
		}
		// C1-06：reset 分支与 subscriptionReset 同为凭据轮换，接入
		// 幂等（管理员校验通过后才占位：403 不落缓存、未授权者
		// 不能预占键）。回放命中时首轮回执（含新 token）原样返回，
		// 函数尾部的共用 OK 回执不会再执行。
		var finish func()
		var replayed bool
		w, finish, replayed = h.idemBegin(w, r, idemUserID(r), "inbound-sub-info:reset")
		defer finish()
		if replayed {
			return
		}
		// X-P3-13（L-03-2）：与主路径共用全轮换（token+UUID/密码+下发
		// 内核），不再只换 token——否则经此路重置的旧配置仍能连接。
		_, applyErr, err := h.rotateSubscriptionCredentials(s)
		if err != nil {
			// RAW291 Type B：混合错误——SaveSubscription 仓储错误
			// （已标 repoError）改走通用文案；随机源失败与墓碑拒绝
			// 为固定文案，原样回。
			if respondInternalIfRepoError(w, "inboundSubInfo: rotateSubscriptionCredentials failed", err) {
				return
			}
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
		if applyErr != nil {
			// RAW291：仓储原文经 pushFailureMsg 换成固定文案。
			response.WriteJSON(w, response.R{Code: 0, Msg: pushFailureMsg("已重置，但下发到节点失败", applyErr), Data: map[string]interface{}{
				"token": s.Token,
				"path":  "/api/v1/open_api/sub/" + s.Token,
			}, TS: time.Now().UnixMilli()})
			return
		}
	} else {
		// F-001：读分支只给被分配的用户（用户 2026-10-20 裁决：默认订阅
		// 只给被分配的用户）。未分配用户拿到 token 即可绕过订阅分配、
		// 不带 ?user= 拉全量节点，故响应中不得包含 token。
		// 注意 authErr 必须先判：claims 解析失败时 roleID 恒为 0，
		// 若先看 roleID 会把未认证请求误当管理员放行。
		if authErr != nil {
			response.WriteJSON(w, response.Err(401, "未登录或token已过期"))
			return
		}
		if roleID != 0 {
			allowed, err := h.canReadDefaultSubscription(userID, s)
			if err != nil {
				respondInternalError(w, "inboundSubInfo: canReadDefaultSubscription failed", err)
				return
			}
			if !allowed {
				response.WriteJSON(w, response.Err(403, "无权限，默认订阅未分配给当前用户"))
				return
			}
		}
	}
	response.WriteJSON(w, response.OK(map[string]interface{}{
		"token": s.Token,
		"path":  "/api/v1/open_api/sub/" + s.Token,
	}))
}

// canReadDefaultSubscription 判断普通用户是否有权读取默认订阅 token：
// 被分配了该订阅（1.0.12 user_subscription），或持有旧入站分配
// （user_inbound——「我的订阅」存量 ?user=+sig 回退链靠本端点的 path
// 拼链接，见 X1 §7）且至少一条分配入站属于该订阅。
//
// L1-F01（Round 5）：旧实现第二条腿只看「有任意一条 user_inbound 行」，
// 不问该入站属不属于默认订阅——仅在其他订阅下有分配（错位分配）、或
// 分配行指向不存在入站的用户，都能拿到默认订阅裸 token，不带 ?user=
// 拉全量，绕过 ?user=&sig= 按分配过滤面。现收紧为与拉取侧同一成员
// 口径：分配入站 ∩ subInboundIDs(订阅) 非空才放行（subInboundIDs 是
// 拉取侧成员判定的唯一收口——SourceAll 全量、否则显式列表＋来源派生、
// 伴生行不计；分配键与 filterInboundRowsByUserAssignments 同为 origin
// 入站 id，含中转分配行）。
func (h *Handler) canReadDefaultSubscription(userID int64, s *model.Subscription) (bool, error) {
	ids, err := h.repo.ListUserSubscriptionIDs(userID)
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		if id == s.ID {
			return true, nil
		}
	}
	assigned, err := h.repo.ListUserInboundIDs(userID)
	if err != nil {
		return false, err
	}
	if len(assigned) == 0 {
		return false, nil
	}
	rows, err := h.repo.ListNodeInbounds()
	if err != nil {
		return false, err
	}
	member := make(map[int64]bool, len(assigned))
	for _, id := range h.subAllInboundIDs(s, rows, nil) {
		member[id] = true
	}
	for _, id := range assigned {
		if member[id] {
			return true, nil
		}
	}
	return false, nil
}
