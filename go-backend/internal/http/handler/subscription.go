package handler

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"go-backend/internal/http/response"
	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

var subscriptionCheckInterval = time.Minute

// subSyncMu keeps the loop and the traffic-triggered syncs from pushing the
// same kernels at once.
var subSyncMu sync.Mutex

// subPipeline is the JSON form of kernel.SubOptions stored per subscription.
type subPipeline struct {
	Lines             string   `json:"lines"`
	Regions           []string `json:"regions"`
	Protocols         []string `json:"protocols"`
	Include           string   `json:"include"`
	Exclude           string   `json:"exclude"`
	Rename            string   `json:"rename"`
	AddFlag           bool     `json:"addFlag"`
	Prefix            string   `json:"prefix"`
	Dedupe            bool     `json:"dedupe"`
	Sort              string   `json:"sort"`
	RegionGroups      bool     `json:"regionGroups"`
	ChainLanding      string   `json:"chainLanding"`
	ChainRelay        string   `json:"chainRelay"`
	Script            string   `json:"script"`
	Rules             bool     `json:"rules"`
	RuleGroups        []string `json:"ruleGroups"`
	ChainLandingNames []string `json:"chainLandingNames"`
	ChainRelayNames   []string `json:"chainRelayNames"`
	// ChainLandingSources / ChainRelaySources take whole remote sources
	// (their nodes join the subscription), ChainLandingOwn the panel's own
	// nodes; ChainNoneNames are nodes set to 不用 that those would take.
	ChainLandingSources []int64  `json:"chainLandingSources"`
	ChainRelaySources   []int64  `json:"chainRelaySources"`
	ChainLandingOwn     bool     `json:"chainLandingOwn"`
	ChainNoneNames      []string `json:"chainNoneNames"`
	ChainHideRelays     bool     `json:"chainHideRelays"`
	// ChainSourceFilters narrows a chain source to the nodes whose name
	// matches; ChainDropUnused leaves the rest of its nodes out.
	ChainSourceFilters map[int64]string `json:"chainSourceFilters,omitempty"`
	ChainDropUnused    bool             `json:"chainDropUnused"`
	// RelayIDs 是端点白名单（1.0.12 中转范围过滤）：空/nil = 不过滤（旧行为）；
	// 非空时仅保留 RelayID 命中的端点，0 表示直连端点，N>0 表示该中转的端点。
	RelayIDs []int64 `json:"relayIds,omitempty"`
}

func (p subPipeline) options() kernel.SubOptions {
	return kernel.SubOptions{
		Lines: p.Lines, Regions: p.Regions, Protocols: p.Protocols, Include: p.Include, Exclude: p.Exclude,
		Rename: p.Rename, AddFlag: p.AddFlag, Prefix: p.Prefix, Dedupe: p.Dedupe, Sort: p.Sort,
		RegionGroups: p.RegionGroups, ChainLanding: p.ChainLanding, ChainRelay: p.ChainRelay,
		Script: p.Script, Rules: p.Rules, RuleGroups: p.RuleGroups,
		ChainLandingNames: p.ChainLandingNames, ChainRelayNames: p.ChainRelayNames,
		ChainLandingSources: p.ChainLandingSources, ChainRelaySources: p.ChainRelaySources,
		ChainLandingOwn: p.ChainLandingOwn, ChainNoneNames: p.ChainNoneNames, ChainHideRelays: p.ChainHideRelays,
		ChainSourceFilters: p.ChainSourceFilters, ChainDropUnused: p.ChainDropUnused,
	}
}

func (p *subPipeline) normalize() error {
	p.Rename = strings.TrimSpace(strings.ReplaceAll(p.Rename, "\r\n", "\n"))
	p.Include = strings.TrimSpace(p.Include)
	p.Exclude = strings.TrimSpace(p.Exclude)
	p.ChainLanding = strings.TrimSpace(p.ChainLanding)
	p.ChainRelay = strings.TrimSpace(p.ChainRelay)
	if len(p.Rename) > 10000 || len(p.Include) > 1000 || len(p.Exclude) > 1000 ||
		len(p.ChainLanding) > 1000 || len(p.ChainRelay) > 1000 {
		return errText("规则太长")
	}
	if len([]rune(p.Prefix)) > 50 {
		return errText("前缀最多 50 个字")
	}
	if p.Sort != "name" {
		p.Sort = ""
	}
	if p.Lines != "relay" && p.Lines != "direct" {
		p.Lines = ""
	}
	// R2X-025（B2-04）：Regions / Protocols 此前仅 nil→[]，带空格或
	// 未知值原样落库后过滤恒不命中、拉取静默为空。与 RuleGroups 同
	// 口径：按已知集修剪、去重、过滤未知值（正常前端数据不受影响）。
	knownRegions := make(map[string]bool)
	for _, name := range append(kernel.RegionNames(), kernel.RegionOther) {
		knownRegions[name] = true
	}
	regions := []string{}
	seenRegions := map[string]bool{}
	for _, name := range p.Regions {
		name = strings.TrimSpace(name)
		if name == "" || seenRegions[name] || !knownRegions[name] {
			continue
		}
		seenRegions[name] = true
		regions = append(regions, name)
	}
	p.Regions = regions
	// 已知协议集 = 自建协议常量 + importTypes 值域（kernel 侧统一导出）。
	knownProtocols := make(map[string]bool)
	for _, proto := range kernel.SubscriptionProtocols() {
		knownProtocols[proto] = true
	}
	protocols := []string{}
	seenProtocols := map[string]bool{}
	for _, proto := range p.Protocols {
		proto = strings.ToLower(strings.TrimSpace(proto))
		if proto == "" || seenProtocols[proto] || !knownProtocols[proto] {
			continue
		}
		seenProtocols[proto] = true
		protocols = append(protocols, proto)
	}
	p.Protocols = protocols
	if p.RuleGroups == nil {
		p.RuleGroups = []string{}
	}
	// O4-005/H-003：保存时过滤非法规则组 key（此前只在渲染侧 fail-safe，
	// 非法值会原样落库）。NormalizeRuleGroups 只保留已知 key 并按展示顺序排列。
	p.RuleGroups = kernel.NormalizeRuleGroups(p.RuleGroups)
	if p.ChainLandingNames == nil {
		p.ChainLandingNames = []string{}
	}
	if p.ChainRelayNames == nil {
		p.ChainRelayNames = []string{}
	}
	if p.ChainNoneNames == nil {
		p.ChainNoneNames = []string{}
	}
	// 链式按源（3.3.29 #87）：新字段归一化——None 名单修剪去重、来源 ID
	// 去零去重；筛选表达式只保留链内来源的（其余静默丢弃）。
	noneNames, err := normalizeNames(p.ChainNoneNames)
	if err != nil {
		return err
	}
	p.ChainNoneNames = noneNames
	// X-P3-1（B-F1）：ChainLandingNames/ChainRelayNames 此前唯独未过
	// normalizeNames，带空格的名字落库后与 Original 精确匹配永不命中。
	// 与 None 名单同口径（修剪/去重/条数与长度上限、同一文案）。
	landingNames, err := normalizeNames(p.ChainLandingNames)
	if err != nil {
		return err
	}
	p.ChainLandingNames = landingNames
	relayNames, err := normalizeNames(p.ChainRelayNames)
	if err != nil {
		return err
	}
	p.ChainRelayNames = relayNames
	landingSources, err := normalizeIDs(p.ChainLandingSources)
	if err != nil {
		return err
	}
	p.ChainLandingSources = landingSources
	relaySources, err := normalizeIDs(p.ChainRelaySources)
	if err != nil {
		return err
	}
	p.ChainRelaySources = relaySources
	// Filters are kept for the sources in the chain only.
	inChain := map[int64]bool{}
	for _, id := range append(append([]int64{}, p.ChainLandingSources...), p.ChainRelaySources...) {
		inChain[id] = true
	}
	filters := map[int64]string{}
	for id, expr := range p.ChainSourceFilters {
		if expr = strings.TrimSpace(expr); expr != "" && inChain[id] {
			if len(expr) > 1000 {
				return errText("规则太长")
			}
			filters[id] = expr
		}
	}
	p.ChainSourceFilters = filters
	// 脚本长度限制（与执行时的 20000 一致，保存时提前拦截）
	// X-P3-3（B-F3/BC-10）：按字符计数，与执行侧 ValidateSubScript 同口径。
	if utf8.RuneCountInString(p.Script) > 20000 {
		return errText("脚本过长（最多 20000 字符）")
	}
	// 中转范围白名单归一化：去重、剔除负数（0=直连合法）。
	// R2X-025：补条数上限 1000（与名称名单同口径，原始条数计），
	// 超限报错而非静默截断。
	if len(p.RelayIDs) > 1000 {
		return errText("选的中转太多（最多 1000 个）")
	}
	if len(p.RelayIDs) > 0 {
		seen := make(map[int64]bool, len(p.RelayIDs))
		clean := make([]int64, 0, len(p.RelayIDs))
		for _, id := range p.RelayIDs {
			if id < 0 || seen[id] {
				continue
			}
			seen[id] = true
			clean = append(clean, id)
		}
		p.RelayIDs = clean
	}
	return p.options().Validate()
}

func parsePipeline(raw string) subPipeline {
	var p subPipeline
	_ = json.Unmarshal([]byte(raw), &p)
	if p.Regions == nil {
		p.Regions = []string{}
	}
	if p.Protocols == nil {
		p.Protocols = []string{}
	}
	// 新字段 nil→[] 默认值，防止前端收到 null 崩溃（H-001）
	if p.RuleGroups == nil {
		p.RuleGroups = []string{}
	}
	if p.ChainLandingNames == nil {
		p.ChainLandingNames = []string{}
	}
	if p.ChainRelayNames == nil {
		p.ChainRelayNames = []string{}
	}
	if p.ChainNoneNames == nil {
		p.ChainNoneNames = []string{}
	}
	if p.ChainLandingSources == nil {
		p.ChainLandingSources = []int64{}
	}
	if p.ChainRelaySources == nil {
		p.ChainRelaySources = []int64{}
	}
	return p
}

// normalizeIDs drops zero and repeated ids.
func normalizeIDs(ids []int64) ([]int64, error) {
	if len(ids) > 100 {
		return nil, errText("选的订阅源太多")
	}
	out := []int64{}
	seen := map[int64]bool{}
	for _, id := range ids {
		if id > 0 && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

// normalizeNames trims and dedupes picked node names; a node picked as
// both landing and relay counts as landing (see markChain).
func normalizeNames(names []string) ([]string, error) {
	if len(names) > 1000 {
		return nil, errText("选的节点太多")
	}
	out := []string{}
	seen := map[string]bool{}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" || seen[n] {
			continue
		}
		if len([]rune(n)) > 200 {
			return nil, errText("节点名称太长")
		}
		seen[n] = true
		out = append(out, n)
	}
	return out, nil
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
		return "", fmt.Errorf("生成随机凭据失败: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func newUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := cryptoRandRead(b); err != nil {
		return "", fmt.Errorf("生成随机凭据失败: %w", err)
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

func subUsed(s *model.Subscription) int64 { return s.UploadBytes + s.DownloadBytes }

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
	samples := make([]repo.SubscriptionCounterSample, 0, len(counters))
	for _, c := range counters {
		samples = append(samples, repo.SubscriptionCounterSample{SubscriptionID: c.ID, Direction: c.Dir, Bytes: c.Bytes})
	}
	if err := h.repo.ApplySubscriptionCounterSamples(nodeID, epoch, samples, now); err != nil {
		log.Printf("subscription traffic apply failed node_id=%d err=%v", nodeID, err)
		return
	}
	h.syncSubscriptions(time.UnixMilli(now))
}

func isShare(s *model.Subscription) bool { return s.Kind == model.SubscriptionShare }

func kernelUser(s *model.Subscription) kernel.User {
	return kernel.User{ID: s.ID, UUID: s.UUID, Password: s.Password}
}

// subInboundIDs resolves the subscription's sources against the inbounds.
func subInboundIDs(s *model.Subscription, rows []model.NodeInbound) []int64 {
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
	pipeline := parsePipeline(s.Options)
	// The chain's sources come with their nodes.
	for _, id := range append(append(parseIDList(s.SourceIDs), pipeline.ChainLandingSources...), pipeline.ChainRelaySources...) {
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
// credentials the kernels must currently accept.
func (h *Handler) activeUsersByInbound(now time.Time) map[int64][]kernel.User {
	subs, err := h.repo.ListSubscriptions()
	if err != nil {
		log.Printf("subscription lookup failed: %v", err)
		return nil
	}
	var rows []model.NodeInbound
	out := map[int64][]kernel.User{}
	for i := range subs {
		s := &subs[i]
		if !isShare(s) || !subActive(s, now) {
			continue
		}
		if rows == nil {
			if rows, err = h.repo.ListNodeInbounds(); err != nil {
				return nil
			}
		}
		for _, id := range subInboundIDs(s, rows) {
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

// syncSubscriptions pushes credential changes for share subscriptions
// whose active state differs from what the kernels last received.
func (h *Handler) syncSubscriptions(now time.Time) {
	subSyncMu.Lock()
	defer subSyncMu.Unlock()
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
		if err := h.applyKernelsForInbounds(subInboundIDs(s, rows)); err != nil {
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
	want := map[int64]bool{}
	for _, id := range subInboundIDs(s, rows) {
		want[id] = true
	}
	selected := make([]model.NodeInbound, 0, len(want))
	for _, row := range rows {
		if want[row.ID] {
			selected = append(selected, row)
		}
	}
	endpoints, _ := h.inboundEndpoints(selected)
	if isShare(s) {
		ku := kernelUser(s)
		for i := range endpoints {
			if endpoints[i].Proxy == nil {
				endpoints[i].Inbound = endpoints[i].Inbound.ForUser(ku)
			}
		}
	}
	// 1.0.12: 中转范围过滤（Options.relayIds）。空 = 不过滤，保持旧行为。
	return filterEndpointsByRelayIDs(endpoints, parsePipeline(s.Options).RelayIDs)
}

// filterInboundRowsByUserAssignments 保留用户被分配的入站行
// （openSubscription ?user= 拉取侧第一段过滤；FIX2-B/V5-1 起与
// mine 计数共用同一实现，保证两侧口径永不分叉）。
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
		} else if e.Direct && directAllowed[e.Inbound.ID] {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

// filterEndpointsByRelayIDs 按 relayIds 白名单过滤端点：
// relayIDs 为空时原样返回；端点的 RelayID==0 视为直连（含导入端点），
// 仅当 0 在白名单中才保留；RelayID>0 的中转端点同理按号命中。
func filterEndpointsByRelayIDs(endpoints []kernel.Endpoint, relayIDs []int64) []kernel.Endpoint {
	if len(relayIDs) == 0 {
		return endpoints
	}
	allowed := make(map[int64]bool, len(relayIDs))
	for _, id := range relayIDs {
		allowed[id] = true
	}
	out := endpoints[:0]
	for _, e := range endpoints {
		if allowed[e.RelayID] {
			out = append(out, e)
		}
	}
	return out
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
	// carry per-user credentials (Snell, Shadowsocks, imported nodes).
	SharedIDs  []int64     `json:"sharedIds"`
	Options    subPipeline `json:"options"`
	ExpireTime int64       `json:"expireTime"`
	Enabled    bool        `json:"enabled"`
	Active     bool        `json:"active"`
	Remark     string      `json:"remark"`
	NodeCount  int         `json:"nodeCount"`
	// Traffic (share subscriptions; bytes).
	UploadBytes   int64 `json:"uploadBytes"`
	DownloadBytes int64 `json:"downloadBytes"`
	TrafficLimit  int64 `json:"trafficLimit"`
	ResetDay      int   `json:"resetDay"`
	// TrafficSince is when the used-traffic counters started counting
	// (creation or last reset), resolved via subTrafficSince.
	TrafficSince int64 `json:"trafficSince"`
	OverQuota    bool  `json:"overQuota"`
	// RelayIDs 是该订阅的中转范围白名单（与 options.relayIds 同源，顶层暴露便于前端回显）。
	RelayIDs []int64 `json:"relayIds"`
	// IsDefault 标记"默认订阅"（个人订阅链接的载体）：排序第一条，由 subscriptionList 回填。
	IsDefault bool `json:"isDefault"`
}

// subFormatAlias resolves format aliases shared by the pull side
// (openSubscription) and the count side (nodeCountFormat) — X-P3-2
// (B-F2): the two used to resolve aliases independently and the count
// side missed stash, silently falling back to base64 counts for a
// format clients actually receive as Clash. Add new aliases here only.
func subFormatAlias(format string) string {
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "stash" {
		return kernel.FormatClash // Stash reads Clash profiles
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
	case kernel.FormatClash, kernel.FormatSingbox, kernel.FormatSurge,
		kernel.FormatLoon, kernel.FormatQX, kernel.FormatShadowrocket, kernel.FormatBase64:
		return format
	}
	return kernel.FormatBase64
}

func (h *Handler) subscriptionView(s *model.Subscription, rows []model.NodeInbound, now time.Time, format string) subscriptionView {
	return h.subscriptionViewForAssignments(s, rows, now, format, nil)
}

// subscriptionViewForAssignments 在 subscriptionView 基础上按用户入站
// 分配收紧 NodeCount（FIX2-B/V5-1）：assignments 为 nil 表示管理员
// 全量口径；非 nil（含空集）时先按拉取侧同一组过滤函数收紧行与
// 端点，使「我的订阅」显示数与该用户经 ?user=&sig= 个性化链接实际
// 拉到的节点数恒等。空集 = 该用户个性化链接拉到 0 个节点。
func (h *Handler) subscriptionViewForAssignments(s *model.Subscription, rows []model.NodeInbound, now time.Time, format string, assignments []repo.UserInboundAssignment) subscriptionView {
	if assignments != nil {
		rows = filterInboundRowsByUserAssignments(rows, assignments)
	}
	ids := subInboundIDs(s, rows)
	byID := make(map[int64]model.NodeInbound, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}
	shared := []int64{}
	if isShare(s) {
		for _, id := range ids {
			row := byID[id]
			if isExternalInbound(&row) {
				shared = append(shared, id)
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
	relayIDs := pipe.RelayIDs
	if relayIDs == nil {
		relayIDs = []int64{}
	}
	// A5-02/BC-11：NodeCount 按目标格式的实际渲染数计（与该格式的客户
	// 端实得一致），不再是格式过滤前的终结点数。sing-box 口径按现行
	// 客户端（1.14+，含 Snell）计，与真机实测对齐。
	format = nodeCountFormat(format)
	countOpts := pipe.options()
	if format == kernel.FormatSingbox {
		countOpts.SingboxSnell = true
	}
	endpoints := h.subEndpoints(s, rows)
	if len(assignments) > 0 {
		endpoints = filterEndpointsByUserAssignments(endpoints, assignments)
	}
	return subscriptionView{
		ID: s.ID, Name: s.Name, Kind: s.Kind, Token: s.Token, Path: "/api/v1/open_api/sub/" + s.Token,
		SourceAll: s.SourceAll == 1, InboundIDs: selected, SourceIDs: sourceIDs, SharedIDs: shared, Options: pipe,
		ExpireTime: s.ExpireTime, Enabled: s.Enabled == 1, Active: subActive(s, now), Remark: s.Remark,
		NodeCount:   kernel.SubscriptionNodeCount(format, endpoints, countOpts),
		UploadBytes: s.UploadBytes, DownloadBytes: s.DownloadBytes, TrafficLimit: s.TrafficLimit,
		ResetDay: s.ResetDay, TrafficSince: subTrafficSince(s), OverQuota: subOverQuota(s), RelayIDs: relayIDs,
	}
}

func (h *Handler) subscriptionList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	subs, err := h.repo.ListSubscriptions()
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if len(subs) == 0 {
		// Start everyone off with one; deleting it later is the user's call.
		def, err := h.repo.DefaultSubscription(time.Now().UnixMilli())
		if err != nil {
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
		subs = append(subs, *def)
	}
	rows, err := h.repo.ListNodeInbounds()
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	// 请求体可选：format 指定 NodeCount 的目标格式口径（A5-02/BC-11）。
	var listReq struct {
		Format string `json:"format"`
	}
	_ = decodeJSON(r.Body, &listReq)
	now := time.Now()
	items := make([]subscriptionView, 0, len(subs))
	for i := range subs {
		items = append(items, h.subscriptionView(&subs[i], rows, now, listReq.Format))
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
		"items":      items,
		"regions":    append(kernel.RegionNames(), kernel.RegionOther),
		"ruleGroups": kernel.RuleGroups(),
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
func (req *subscriptionRequest) validate(rows []model.NodeInbound) ([]int64, error) {
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len([]rune(req.Name)) > 100 {
		return nil, errText("请填写名称（最多 100 个字）")
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
		return nil, errText("每月重置日只能是 1–28 号")
	}
	if err := req.Options.normalize(); err != nil {
		return nil, err
	}
	known := make(map[int64]bool, len(rows))
	for _, r := range rows {
		known[r.ID] = true
	}
	var ids []int64
	for _, id := range req.InboundIDs {
		if known[id] {
			ids = append(ids, id)
		}
	}
	var sources []int64
	for _, id := range req.SourceIDs {
		if id > 0 {
			sources = append(sources, id)
		}
	}
	req.SourceIDs = sources
	chainSources := len(req.Options.ChainLandingSources) + len(req.Options.ChainRelaySources)
	if !req.SourceAll && len(ids) == 0 && len(sources) == 0 && chainSources == 0 {
		return nil, errText("请至少选择一个入站或订阅源，或选择全部")
	}
	return ids, nil
}

func (h *Handler) subscriptionSave(w http.ResponseWriter, r *http.Request) {
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
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	ids, err := req.validate(rows)
	if err != nil {
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
		if isShare(s) {
			before = subInboundIDs(s, rows)
		}
	} else {
		token, tokenErr := randomHex(16)
		if tokenErr != nil {
			response.WriteJSON(w, response.Err(-2, tokenErr.Error()))
			return
		}
		s = &model.Subscription{Token: token, Enabled: 1, CreatedTime: now.UnixMilli(), TrafficSince: now.UnixMilli()}
	}
	s.Name = req.Name
	s.Kind = req.Kind
	if isShare(s) && (s.UUID == "" || s.Password == "") {
		uuid, uuidErr := newUUID()
		if uuidErr != nil {
			response.WriteJSON(w, response.Err(-2, uuidErr.Error()))
			return
		}
		pw, pwErr := randomHex(16)
		if pwErr != nil {
			response.WriteJSON(w, response.Err(-2, pwErr.Error()))
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
		response.WriteJSON(w, response.Err(-2, saveErr.Error()))
		return
	}
	push := before
	if isShare(s) {
		push = append(push, subInboundIDs(s, rows)...)
	}
	if err := h.applyKernelsForInbounds(push); err != nil {
		// 下发失败：不更新 AppliedActive，循环会重试；仅提示用户。
		response.WriteJSON(w, response.R{Code: 0, Msg: "已保存，但下发到节点失败: " + err.Error(), Data: h.subscriptionView(s, rows, now, req.Format), TS: time.Now().UnixMilli()})
		return
	}
	// 下发成功：更新 AppliedActive。
	_ = h.repo.UpdateSubscriptionApplied(s.ID, newAppliedActive)
	s.AppliedActive = newAppliedActive
	response.WriteJSON(w, response.R{Code: 0, Msg: "操作成功", Data: h.subscriptionView(s, rows, now, req.Format), TS: time.Now().UnixMilli()})
}

func (h *Handler) respondAfterSubPush(w http.ResponseWriter, inboundIDs []int64, data interface{}) {
	msg := "操作成功"
	if err := h.applyKernelsForInbounds(inboundIDs); err != nil {
		msg = "已保存，但下发到节点失败: " + err.Error()
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
		response.WriteJSON(w, response.Err(-2, err.Error()))
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
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	s.PendingDelete = 1
	s.Enabled = 0
	if err := h.completePendingSubscriptionDelete(s); err != nil {
		log.Printf("subscription %d: delete push failed, tombstone kept for retry: %v", id, err)
		response.WriteJSON(w, response.R{Code: 0, Msg: "已标记删除，但下发到节点失败，后台将自动重试: " + err.Error(), Data: nil, TS: time.Now().UnixMilli()})
		return
	}
	response.WriteJSON(w, response.R{Code: 0, Msg: "操作成功", Data: nil, TS: time.Now().UnixMilli()})
}

// completePendingSubscriptionDelete pushes the credential removal for a
// tombstoned subscription and, only on success, hard-deletes the row
// together with its assignments in one transaction. The push list is
// computed from the subscription's (former) inbounds; the payload itself
// is rendered from current DB state, in which the tombstone is disabled
// and therefore already excluded.
func (h *Handler) completePendingSubscriptionDelete(s *model.Subscription) error {
	var push []int64
	if isShare(s) {
		rows, err := h.repo.ListNodeInbounds()
		if err != nil {
			return err
		}
		push = subInboundIDs(s, rows)
	}
	if err := h.applyKernelsForInbounds(push); err != nil {
		return err
	}
	return h.repo.DeleteSubscriptionWithAssignments(s.ID)
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
		return nil, nil, err
	}
	rows, _ = h.repo.ListNodeInbounds()
	var push []int64
	if isShare(s) {
		push = subInboundIDs(s, rows)
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
	id := idFromBody(r, w)
	if id <= 0 {
		return
	}
	s, err := h.repo.GetSubscription(id)
	if err != nil || s == nil {
		response.WriteJSON(w, response.ErrDefault("订阅不存在"))
		return
	}
	rows, applyErr, err := h.rotateSubscriptionCredentials(s)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if applyErr != nil {
		response.WriteJSON(w, response.R{Code: 0, Msg: "已重置，但下发到节点失败: " + applyErr.Error(), Data: h.subscriptionView(s, rows, time.Now(), ""), TS: time.Now().UnixMilli()})
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
	now := time.Now()
	if err := h.repo.ResetSubscriptionTraffic(id, now.UnixMilli()); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if h.notifier != nil {
		h.notifier.clearAlerts(fmt.Sprintf("subflow:%d:", id))
	}
	h.syncSubscriptions(now)
	s, _ = h.repo.GetSubscription(id)
	rows, _ := h.repo.ListNodeInbounds()
	response.WriteJSON(w, response.OK(h.subscriptionView(s, rows, now, "")))
}

type subPreviewItem struct {
	Name     string `json:"name"`
	Original string `json:"original"`
	Region   string `json:"region"`
	Protocol string `json:"protocol"`
	Line     string `json:"line"` // "direct" or "relay"
	// Chain is "landing" or "relay" when the subscription chains proxies.
	Chain string `json:"chain,omitempty"`
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
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	ids, err := req.validate(rows)
	if err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	options, _ := json.Marshal(req.Options)
	draft := &model.Subscription{SourceAll: boolToInt(req.SourceAll), InboundIDs: formatIDList(ids), SourceIDs: formatIDList(req.SourceIDs),
		Options: string(options)}
	source := h.subEndpoints(draft, rows)
	opts := req.Options.options()
	// F-024（O4-001 的另一半）：脚本出错时预览必须保留其他步骤的节点并
	// 附带 scriptError——此前实现直接返回空 items + dropped=全部，与
	// guide「脚本执行失败不影响其他步骤的结果」相悖，还误导用户以为
	// 节点全被过滤。单次流水线同时拿结果与脚本错误，不再重跑。
	result, scriptErr := kernel.ApplySubOptionsReport(source, opts)
	items := make([]subPreviewItem, 0, len(result))
	for _, e := range result {
		line := "relay"
		if e.Direct {
			line = "direct"
		}
		items = append(items, subPreviewItem{Name: e.Name, Original: e.Original, Region: kernel.RegionOf(e.Name),
			Protocol: kernel.EndpointProtocol(e), Line: line, Chain: e.ChainRole, SourceID: e.SourceID, Own: e.Proxy == nil && e.SourceID == 0})
	}
	resp := map[string]interface{}{
		"items":   items,
		"total":   len(source),
		"dropped": len(source) - len(items),
	}
	if scriptErr != nil {
		resp["scriptError"] = scriptErr.Error()
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
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	userID, roleID, authErr := userRoleFromRequest(r)
	if req.Reset {
		// 换 token 是管理员操作（F-001 回归点：reset 仍仅管理员）。
		if authErr != nil || roleID != 0 {
			response.WriteJSON(w, response.Err(403, "无权限，仅管理员可操作"))
			return
		}
		// X-P3-13（L-03-2）：与主路径共用全轮换（token+UUID/密码+下发
		// 内核），不再只换 token——否则经此路重置的旧配置仍能连接。
		_, applyErr, err := h.rotateSubscriptionCredentials(s)
		if err != nil {
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
		if applyErr != nil {
			response.WriteJSON(w, response.R{Code: 0, Msg: "已重置，但下发到节点失败: " + applyErr.Error(), Data: map[string]interface{}{
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
			allowed, err := h.canReadDefaultSubscription(userID, s.ID)
			if err != nil {
				response.WriteJSON(w, response.Err(-2, err.Error()))
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
// 拼链接，分配为空集时 openSubscription 本就过滤为空，见 X1 §7）。
func (h *Handler) canReadDefaultSubscription(userID, subscriptionID int64) (bool, error) {
	ids, err := h.repo.ListUserSubscriptionIDs(userID)
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		if id == subscriptionID {
			return true, nil
		}
	}
	legacy, err := h.repo.ListUserInboundIDs(userID)
	if err != nil {
		return false, err
	}
	return len(legacy) > 0, nil
}
