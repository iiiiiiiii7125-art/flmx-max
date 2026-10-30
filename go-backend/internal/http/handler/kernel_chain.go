package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"go-backend/internal/http/response"
	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
)

// Chained outbounds: an inbound with OutboundID set leaves through another
// inbound (of this panel, or an imported node) instead of from its own node,
// and its 分流 rules can send part of the traffic through others. Each
// kernel dials only the next hop, so A -> B -> C is A chained to B and B
// chained to C.

// maxChainHops bounds the walk that looks for loops.
const maxChainHops = 32

// chainEndpoint is the address a kernel on fromNodeID dials to reach the
// target inbound.
func (h *Handler) chainEndpoint(target *model.NodeInbound, fromNodeID int64) (kernel.Endpoint, error) {
	in, err := inboundFromModel(*target)
	if err != nil {
		return kernel.Endpoint{}, err
	}
	// One port: a chained hop never uses Hysteria2 port hopping.
	e := kernel.Endpoint{Name: target.Name, Port: target.Port, Inbound: in, Proxy: importedProxy(target)}
	if isExternalInbound(target) {
		e.Host = firstNonEmpty(strings.TrimSpace(target.ShareHost), target.Listen)
		return e, nil
	}
	if target.AcceptProxyProtocol == 1 {
		return e, fmt.Errorf("出站节点「%s」开启了获取真实 IP（PROXY protocol），只能经中转连接", target.Name)
	}
	if target.NodeID == fromNodeID {
		// Same machine: dial it locally.
		e.Host = "127.0.0.1"
		if ip := net.ParseIP(target.Listen); ip != nil && !ip.IsUnspecified() {
			e.Host = ip.String()
		}
		return e, nil
	}
	if isLoopbackListen(target.Listen) {
		return e, fmt.Errorf("出站节点「%s」只监听本机，其他节点连不上", target.Name)
	}
	node, _ := h.repo.GetNodeByID(target.NodeID)
	e.Host = inboundShareAddress(target, node)
	if e.Host == "" {
		return e, fmt.Errorf("出站节点「%s」没有可用的地址", target.Name)
	}
	if err := validateShareHostValue(e.Host); err != nil {
		// R35-LC-01：auto/ipv4/ipv6 的地址源是节点 ServerIP /
		// ServerIPV4 / ServerIPV6，与订阅面 inboundEndpoints 同口径
		// （坏值跳过/拒绝）；存量坏分享主机不进链式下一跳配置。
		// 同节点 127.0.0.1 与导入行在上方早返回，不受影响。
		return e, fmt.Errorf("出站节点「%s」的%s不正确", target.Name, chainShareHostLabel(inboundShareMode(target)))
	}
	return e, nil
}

// chainShareHostLabel names the share-address mode in chain errors, so
// auto/ipv4/ipv6 failures are not misreported as a custom-address
// problem (R35-LC-01).
func chainShareHostLabel(mode string) string {
	switch mode {
	case "ipv4":
		return "IPv4 分享地址"
	case "ipv6":
		return "IPv6 分享地址"
	case "custom":
		return "自定义分享地址"
	default:
		return "自动分享地址"
	}
}

// rowSplitRules returns an inbound's stored 分流 rules.
func rowSplitRules(row model.NodeInbound) []kernel.SplitRule {
	if strings.TrimSpace(row.SplitRules) == "" {
		return nil
	}
	var rules []kernel.SplitRule
	if err := json.Unmarshal([]byte(row.SplitRules), &rules); err != nil {
		log.Printf("kernel: inbound %d split rules damaged: %v", row.ID, err)
		return nil
	}
	return rules
}

// outboundTargets lists the inbounds an inbound sends traffic through: its
// chained outbound and the targets of its 分流 rules.
func outboundTargets(row model.NodeInbound) []int64 {
	var ids []int64
	if row.OutboundID != 0 {
		ids = append(ids, row.OutboundID)
	}
	for _, r := range rowSplitRules(row) {
		if r.Action == kernel.SplitProxy && r.OutboundID != 0 {
			ids = append(ids, r.OutboundID)
		}
	}
	return ids
}

// resolveTarget resolves an outbound target of an inbound for rendering. A
// deleted target yields an empty endpoint, which drops the traffic.
func (h *Handler) resolveTarget(row model.NodeInbound, targetID int64, what string) (*kernel.Endpoint, error) {
	target, err := h.repo.GetNodeInbound(targetID)
	if err != nil {
		return nil, err
	}
	if target == nil {
		log.Printf("kernel: inbound %d sends traffic through missing inbound %d, dropping it", row.ID, targetID)
		return &kernel.Endpoint{}, nil
	}
	e, err := h.chainEndpoint(target, row.NodeID)
	if err != nil {
		return nil, fmt.Errorf("入站「%s」的%s: %v", row.Name, what, err)
	}
	return &e, nil
}

// inboundVia resolves the chained outbound of an inbound for rendering.
func (h *Handler) inboundVia(row model.NodeInbound) (*kernel.Endpoint, error) {
	if row.OutboundID == 0 {
		return nil, nil
	}
	return h.resolveTarget(row, row.OutboundID, "出站代理")
}

// inboundSplit returns an inbound's 分流 rules with their targets resolved.
func (h *Handler) inboundSplit(row model.NodeInbound) ([]kernel.SplitRule, error) {
	rules := rowSplitRules(row)
	for i := range rules {
		if rules[i].Action != kernel.SplitProxy {
			continue
		}
		via, err := h.resolveTarget(row, rules[i].OutboundID, fmt.Sprintf("第 %d 条分流", i+1))
		if err != nil {
			return nil, err
		}
		rules[i].Via = via
	}
	return rules, nil
}

// validateChain checks the outbound targets (chained outbound and 分流
// targets) of an inbound about to be saved.
func (h *Handler) validateChain(row model.NodeInbound, kernelType string) error {
	return h.validateChainVersion(row, kernelType, "")
}

// validateChainVersion is validateChain with the serving kernel's version,
// so version-gated targets (Snell via sing-box needs 1.14+) are judged
// against the version that will actually run them. An empty version is
// not treated as too old (see versionBelow).
func (h *Handler) validateChainVersion(row model.NodeInbound, kernelType, kernelVersion string) error {
	check := func(targetID int64, what string) error {
		if targetID == row.ID {
			return fmt.Errorf("%s不能选择自己", what)
		}
		target, err := h.repo.GetNodeInbound(targetID)
		if err != nil {
			return err
		}
		if target == nil {
			return fmt.Errorf("%s所选的节点不存在", what)
		}
		if target.CompanionOf > 0 {
			return fmt.Errorf("%s不能选择计量伴生入站", what)
		}
		if row.ID != 0 && h.chainReaches(target, row.ID) {
			return fmt.Errorf("%s形成了循环，请检查链路", what)
		}
		e, err := h.chainEndpoint(target, row.NodeID)
		if err != nil {
			return fmt.Errorf("%s：%v", what, err)
		}
		// R2X-007：ENC 拨号侧版本门——目标带 VLESS 加密时，拨号方
		// Xray 同样需要 25.9.5+（与入站自有门 kernelVersionGate 同口径）。
		if msg := encDialGate(kernelType, kernelVersion, &e); msg != "" {
			return fmt.Errorf("%s：%s", what, msg)
		}
		// 出站 IP 策略是入站级的：链式/分流目标若是与策略族冲突的字面
		// IP，保存期即拒绝（域名放行，由内核按同策略解析，S4）。
		if err := kernel.ValidateOutboundIPServer(row.OutboundIPStrategy, e.Host); err != nil {
			return fmt.Errorf("%s：%w", what, err)
		}
		if err := kernel.CheckChainWith(kernelType, e, kernel.RenderOptions{SingboxSnell: !versionBelow(kernelVersion, 1, 14), SingboxNaive: !versionBelow(kernelVersion, 1, 13)}); err != nil {
			return fmt.Errorf("%s：%v", what, err)
		}
		return nil
	}
	// R14-A5-08：Snell 内核没有出站概念——链式出站与分流在渲染期
	//（render_snell.go:25-30）均拒绝，保存期须同口径拒绝（与渲染文案逐字
	//一致），不得先落库再由下发失败告警。守卫置于目标校验之前：
	//Snell 行的任何 OutboundID / SplitRules 都是毒化组合，与目标是否
	//存在无关。注：此前 Via 经下方 check() → CheckChainWith 默认分支亦
	//被拒绝（文案带「出站代理：」前缀），本守卫使其与渲染口径逐字对齐。
	if kernelType == kernel.TypeSnell {
		if row.OutboundID != 0 {
			return errors.New("Snell 内核不支持链式出站")
		}
		if strings.TrimSpace(row.SplitRules) != "" {
			return errors.New("Snell 内核不支持分流")
		}
	}
	if row.OutboundID != 0 {
		if err := check(row.OutboundID, "出站代理"); err != nil {
			return err
		}
	}
	for i, r := range rowSplitRules(row) {
		if r.Action != kernel.SplitProxy {
			continue
		}
		if err := check(r.OutboundID, fmt.Sprintf("第 %d 条分流", i+1)); err != nil {
			return err
		}
	}
	return nil
}

// chainReaches reports whether traffic sent through start can arrive at id,
// following chained outbounds and 分流 targets.
func (h *Handler) chainReaches(start *model.NodeInbound, id int64) bool {
	seen := map[int64]bool{}
	queue := []*model.NodeInbound{start}
	for len(queue) > 0 {
		row := queue[0]
		queue = queue[1:]
		if row.ID == id {
			return true
		}
		if seen[row.ID] {
			continue
		}
		seen[row.ID] = true
		if len(seen) > maxChainHops {
			return true
		}
		for _, next := range outboundTargets(*row) {
			n, err := h.repo.GetNodeInbound(next)
			if err != nil {
				return true
			}
			if n != nil {
				queue = append(queue, n)
			}
		}
	}
	return false
}

// chainRefIndex is a single-load snapshot of the whole inbound table,
// indexed by outbound target: targetID -> referrer rows. X04-001: the
// chain-reference guards used to re-run ListNodeInbounds (a full-table
// load plus a per-row outboundTargets parse) once per checked target —
// per inbound of a deleted node, per row of an uninstalled kernel, and
// (via chainDialerNodeIDs) once per row of the whole table — so one
// guard round cost O(rounds x table) full scans and a node delete at
// ~1000 inbounds already took minutes on the single SQLite connection.
// A guard round now loads the table once, builds this index, and every
// lookup in the round is served from it.
//
// Equivalence: rows are indexed in ListNodeInbounds order (inx ASC, id
// ASC) and each row appears at most once per target, exactly matching
// what repeated chainReferrers calls over the same snapshot returned.
type chainRefIndex struct {
	rows     []model.NodeInbound
	byTarget map[int64][]model.NodeInbound
}

// chainRefIndexFromRows builds the index from an already-loaded table
// snapshot (callers that load the table for their own matching logic
// reuse it instead of querying again).
func chainRefIndexFromRows(rows []model.NodeInbound) *chainRefIndex {
	ix := &chainRefIndex{rows: rows, byTarget: make(map[int64][]model.NodeInbound)}
	for _, row := range rows {
		targets := outboundTargets(row)
		for i, targetID := range targets {
			dup := false
			for _, prev := range targets[:i] {
				if prev == targetID {
					dup = true
					break
				}
			}
			if !dup {
				ix.byTarget[targetID] = append(ix.byTarget[targetID], row)
			}
		}
	}
	return ix
}

// loadChainRefIndex loads the inbound table once and indexes it. The
// raw repository error is returned; callers wrap it in the message
// shape their guard has always produced.
func (h *Handler) loadChainRefIndex() (*chainRefIndex, error) {
	rows, err := h.repo.ListNodeInbounds()
	if err != nil {
		return nil, err
	}
	return chainRefIndexFromRows(rows), nil
}

// referrers returns the snapshot rows that send traffic through
// targetID, in table order — chainReferrers over the snapshot.
func (ix *chainRefIndex) referrers(targetID int64) []model.NodeInbound {
	return ix.byTarget[targetID]
}

// dialerNodeIDs returns the nodes that dial the inbound as a chained
// outbound — chainDialerNodeIDs over the snapshot (same order, same
// duplicate behaviour, external referrers skipped).
func (ix *chainRefIndex) dialerNodeIDs(inboundID int64) []int64 {
	var ids []int64
	for _, ref := range ix.byTarget[inboundID] {
		if !isExternalInbound(&ref) {
			ids = append(ids, ref.NodeID)
		}
	}
	return ids
}

// chainReferrers returns the inbounds that send traffic through the given
// one, as their chained outbound or a 分流 target.
//
// R3X-015: 查询失败必须上抛——删除/卸载守卫以本函数为据，旧实现出错
// 返回 nil 被调用方读作「无引用」放行删除（fail-open），与同文件
// chainReaches 的 fail-closed 口径冲突。调用方不得把 error 当空集。
func (h *Handler) chainReferrers(targetID int64) ([]model.NodeInbound, error) {
	ix, err := h.loadChainRefIndex()
	if err != nil {
		return nil, fmt.Errorf("查询入站 %d 的链式引用方失败: %w", targetID, err)
	}
	return ix.referrers(targetID), nil
}

// chainReferrerDisplayName renders a referrer inbound as
// 「名称（节点名）」 (R2X-029): inbound names are only unique per node, so
// a bare name cannot identify a referrer in guard messages or lists.
func (h *Handler) chainReferrerDisplayName(ref model.NodeInbound) string {
	if node, err := h.repo.GetNodeByID(ref.NodeID); err == nil && node != nil && strings.TrimSpace(node.Name) != "" {
		return fmt.Sprintf("%s（%s）", ref.Name, strings.TrimSpace(node.Name))
	}
	return ref.Name
}

// chainInUseMessage explains why an inbound cannot be removed, or "" when
// nothing is chained through it. R3X-015: 守卫查询失败上抛 error，
// 调用方必须中止删除，不得把失败读作 ""（无引用）。
func (h *Handler) chainInUseMessage(target *model.NodeInbound) (string, error) {
	refs, err := h.chainReferrers(target.ID)
	if err != nil {
		return "", err
	}
	return h.chainInUseMessageFromRefs(target, refs), nil
}

// chainInUseMessageFromRefs renders the guard message from an already
// resolved referrer list (single-snapshot guard rounds, X04-001).
func (h *Handler) chainInUseMessageFromRefs(target *model.NodeInbound, refs []model.NodeInbound) string {
	if len(refs) == 0 {
		return ""
	}
	names := make([]string, 0, len(refs))
	for _, r := range refs {
		names = append(names, "「"+h.chainReferrerDisplayName(r)+"」")
	}
	return fmt.Sprintf("「%s」正被 %s 用作出站代理或分流出口，请先解除", target.Name, strings.Join(names, "、"))
}

// nodeCrossReferrer is one entry of the cross-node referrer list: the
// referrer inbound ID — the only sound dedup key for callers aggregating
// across several deleted nodes (R3X-014) — plus its display string
// 「名称（节点名）」. Display strings are NOT unique: two distinct
// referrers on one node can share a name and render identically.
type nodeCrossReferrer struct {
	ID      int64
	Display string
}

// nodeCrossReferrers lists the inbounds on OTHER nodes that chain through
// any inbound of the given node (R2X-045): deleting the node silently
// breaks exactly these referrers, so they are reported back to the
// caller. Same-node referrers are excluded (they are deleted together
// with the node), and entries are deduplicated by referrer inbound ID
// and disambiguated as 「名称（节点名）」 (R2X-029 口径）.
// R3X-015: 任一底层查询失败上抛 error，删节点路径必须中止——清单
// 不完整时静默删除等于守卫失效（旧实现出错返回 nil 冒充无引用）。
func (h *Handler) nodeCrossReferrers(nodeID int64) ([]nodeCrossReferrer, error) {
	targets, err := h.repo.ListNodeInboundsByNode(nodeID)
	if err != nil {
		return nil, fmt.Errorf("查询节点 %d 的入站失败: %w", nodeID, err)
	}
	if len(targets) == 0 {
		return make([]nodeCrossReferrer, 0), nil
	}
	ix, err := h.loadChainRefIndex()
	if err != nil {
		// Same failure shape the per-target chainReferrers loop produced:
		// the table load failed at the first target.
		return nil, fmt.Errorf("查询入站 %d 的链式引用方失败: %w", targets[0].ID, err)
	}
	return h.nodeCrossReferrersIx(targets, nodeID, ix), nil
}

// nodeCrossReferrersIx is nodeCrossReferrers over a shared snapshot
// (X04-001): the per-target lookups no longer reload the whole table.
func (h *Handler) nodeCrossReferrersIx(targets []model.NodeInbound, nodeID int64, ix *chainRefIndex) []nodeCrossReferrer {
	seen := make(map[int64]struct{})
	out := make([]nodeCrossReferrer, 0)
	for _, target := range targets {
		for _, ref := range ix.referrers(target.ID) {
			if ref.NodeID == nodeID {
				continue
			}
			if _, dup := seen[ref.ID]; dup {
				continue
			}
			seen[ref.ID] = struct{}{}
			out = append(out, nodeCrossReferrer{ID: ref.ID, Display: h.chainReferrerDisplayName(ref)})
		}
	}
	return out
}

// kernelChainGuard 是 R1 X-P2-2（D-R1-02，按 V-03）的卸载守卫：对内核的
// 全部入站逐个跑 chainReferrers，收集「内核之外」的引用方（同内核入站
// 互相引用不算被断——它们随卸载一并删除，与 inboundBatchDelete 的
// batchDeletesReferrers 口径一致）。返回去重后的引用方入站名与按
// chainInUseMessage 口径拼装的说明（无外部引用时说明为空）。
func (h *Handler) kernelChainGuard(kernelID int64) (referrerNames []string, message string, err error) {
	rows, err := h.repo.ListNodeInboundsByKernel(kernelID)
	if err != nil {
		return nil, "", fmt.Errorf("查询内核 %d 的入站失败: %w", kernelID, err)
	}
	if len(rows) == 0 {
		return nil, "", nil
	}
	ix, err := h.loadChainRefIndex()
	if err != nil {
		// Same failure shape the per-row chainReferrers loop produced.
		return nil, "", fmt.Errorf("查询入站 %d 的链式引用方失败: %w", rows[0].ID, err)
	}
	names, msg := h.kernelChainGuardIx(rows, ix)
	return names, msg, nil
}

// kernelChainGuardIx is kernelChainGuard over a shared snapshot
// (X04-001): one table load for the whole uninstall guard round.
func (h *Handler) kernelChainGuardIx(rows []model.NodeInbound, ix *chainRefIndex) (referrerNames []string, message string) {
	own := make(map[int64]struct{}, len(rows))
	for _, row := range rows {
		own[row.ID] = struct{}{}
	}
	// R2X-029: 去重以引用方入站 ID 为键（旧口径以名称为键——同名入站
	// 分属不同节点时被少计且不可区分），元素以「名称（节点名）」消歧。
	seen := make(map[int64]struct{})
	var parts []string
	for _, row := range rows {
		var refNames []string
		refs := ix.referrers(row.ID)
		for _, ref := range refs {
			if _, isOwn := own[ref.ID]; isOwn {
				continue
			}
			display := h.chainReferrerDisplayName(ref)
			refNames = append(refNames, "「"+display+"」")
			if _, dup := seen[ref.ID]; !dup {
				seen[ref.ID] = struct{}{}
				referrerNames = append(referrerNames, display)
			}
		}
		if len(refNames) > 0 {
			parts = append(parts, fmt.Sprintf("「%s」正被 %s 用作出站代理或分流出口，请先解除", row.Name, strings.Join(refNames, "、")))
		}
	}
	return referrerNames, strings.Join(parts, "；")
}

// applyChainReferrers re-applies the kernels of inbounds chained through the
// target after it changed, returning a warning per failure.
// R3X-015: 引用方查询失败以告警回传调用方（响应 Msg/Errors 可见），
// 不得静默当作「无引用方」跳过重下发。
func (h *Handler) applyChainReferrers(targetID int64) []string {
	refs, err := h.chainReferrers(targetID)
	if err != nil {
		return []string{err.Error()}
	}
	return h.applyChainReferrersFromRefs(refs)
}

// applyChainReferrersIx is applyChainReferrers over a shared snapshot
// (X04-001) for rounds that re-apply many targets in a row.
func (h *Handler) applyChainReferrersIx(ix *chainRefIndex, targetID int64) []string {
	return h.applyChainReferrersFromRefs(ix.referrers(targetID))
}

func (h *Handler) applyChainReferrersFromRefs(refs []model.NodeInbound) []string {
	var warnings []string
	seen := map[int64]bool{}
	for _, ref := range refs {
		if isExternalInbound(&ref) || seen[ref.KernelID] {
			continue
		}
		seen[ref.KernelID] = true
		if err := h.applyKernel(ref.KernelID, false); err != nil {
			warnings = append(warnings, fmt.Sprintf("经它出站的「%s」下发失败: %v", ref.Name, err))
		}
	}
	return warnings
}

// applyWhitelistedTarget re-applies a whitelisted target's kernel so its
// allow list follows the nodes chained through it.
func (h *Handler) applyWhitelistedTarget(targetID int64) []string {
	if targetID == 0 {
		return nil
	}
	target, err := h.repo.GetNodeInbound(targetID)
	if err != nil || target == nil || isExternalInbound(target) || target.RelayWhitelist != 1 {
		return nil
	}
	if err := h.applyKernel(target.KernelID, false); err != nil {
		return []string{fmt.Sprintf("出站节点「%s」的白名单下发失败: %v", target.Name, err)}
	}
	return nil
}

// chainDialerNodeIDs returns the nodes that dial the inbound as a chained
// outbound, for its whitelist. R3X-015: 查询失败上抛——白名单少算拨号
// 节点即下发不完整配置，调用方必须中止而非照常下发。
func (h *Handler) chainDialerNodeIDs(inboundID int64) ([]int64, error) {
	refs, err := h.chainReferrers(inboundID)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for _, ref := range refs {
		if !isExternalInbound(&ref) {
			ids = append(ids, ref.NodeID)
		}
	}
	return ids, nil
}

// batchDeletesReferrers reports whether a batch delete also removes every
// inbound chained through a target, so the target may go too.
func batchDeletesReferrers(ids []int64, refs []model.NodeInbound) bool {
	deleting := make(map[int64]bool, len(ids))
	for _, id := range ids {
		deleting[id] = true
	}
	for _, r := range refs {
		if !deleting[r.ID] {
			return false
		}
	}
	return true
}

// changedTargets lists the targets in only one of before and after, once
// each.
func changedTargets(before, after []int64) []int64 {
	count := map[int64]int{}
	for _, id := range before {
		count[id] |= 1
	}
	for _, id := range after {
		count[id] |= 2
	}
	var out []int64
	for _, id := range append(append([]int64{}, before...), after...) {
		if c, ok := count[id]; ok && c != 3 {
			out = append(out, id)
			delete(count, id)
		}
	}
	return out
}

// setInboundRouting sets an inbound's default exit and 分流 rules and checks
// that its kernel can reach every target.
func (h *Handler) setInboundRouting(row *model.NodeInbound, kernelType, kernelVersion string, outboundID int64, rules []kernel.SplitRule) error {
	if outboundID < 0 {
		outboundID = 0
	}
	split, err := kernel.NormalizeSplitRules(rules)
	if err != nil {
		return err
	}
	row.OutboundID = outboundID
	row.SplitRules = ""
	if len(split) > 0 {
		raw, _ := json.Marshal(split)
		row.SplitRules = string(raw)
	}
	return h.validateChainVersion(*row, kernelType, kernelVersion)
}

// applyChangedTargets re-applies the whitelists of targets the inbound
// started or stopped using.
func (h *Handler) applyChangedTargets(prevTargets []int64, row model.NodeInbound) []string {
	var warnings []string
	for _, id := range changedTargets(prevTargets, outboundTargets(row)) {
		warnings = append(warnings, h.applyWhitelistedTarget(id)...)
	}
	return warnings
}

// inboundSplitSave saves only an inbound's default exit and 分流 rules.
func (h *Handler) inboundSplitSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req struct {
		ID                 int64              `json:"id"`
		OutboundID         int64              `json:"outboundId"`
		SplitRules         []kernel.SplitRule `json:"splitRules"`
		OutboundIPStrategy *string            `json:"outboundIpStrategy"`
	}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	row, err := h.repo.GetNodeInbound(req.ID)
	if err != nil || row == nil || isExternalInbound(row) {
		response.WriteJSON(w, response.ErrDefault("节点服务不存在"))
		return
	}
	if row.CompanionOf > 0 {
		response.WriteJSON(w, response.ErrDefault("计量伴生入站由系统管理，不能编辑分流"))
		return
	}
	k, err := h.repo.GetNodeKernel(row.KernelID)
	if err != nil || k == nil {
		response.WriteJSON(w, response.ErrDefault("内核不存在"))
		return
	}
	// 出站 IP 策略是入站级字段，分流编辑界面同样提供修改入口（S4）：
	// 归一化/版本门/Extra 三连校验与入站保存同口径。
	if req.OutboundIPStrategy != nil {
		strategy, err := kernel.NormalizeOutboundIPStrategy(k.KernelType, *req.OutboundIPStrategy)
		if err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
		row.OutboundIPStrategy = strategy
	}
	if err := validateOutboundIPVersion(k.KernelType, k.Version, row.OutboundIPStrategy); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	if err := kernel.ValidateOutboundIPExtra(k.KernelType, row.OutboundIPStrategy, json.RawMessage(row.Extra)); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	prevTargets := outboundTargets(*row)
	if err := h.setInboundRouting(row, k.KernelType, k.Version, req.OutboundID, req.SplitRules); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	// F1：已开启订阅计量的入站不能经分流面获得链式出口/分流（伴生不
	// 继承这两者，落库即静默改道计量流量）；清空它们则放行，下方对
	// 账会立即回收/物化伴生（B-08）。
	if row.MeteringEnabled == 1 && (row.OutboundID != 0 || strings.TrimSpace(row.SplitRules) != "") {
		response.WriteJSON(w, response.ErrDefault("该入站已开启订阅计量，不能设置链式出口或分流，请先关闭订阅计量"))
		return
	}
	row.UpdatedTime = time.Now().UnixMilli()
	if err := h.repo.SaveNodeInbound(row); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	// 路由形态变化直接决定计量资格（ssMeteringWanted），与 inboundSave
	// 同口径保存后立即对账，不等订阅同步周期（B-08）。
	h.reconcileSSMetering()
	var warnings []string
	if err := h.applyKernel(k.ID, false); err != nil {
		warnings = append(warnings, "下发到节点失败: "+err.Error())
	}
	warnings = append(warnings, h.applyChangedTargets(prevTargets, *row)...)
	if len(warnings) > 0 {
		response.WriteJSON(w, response.R{Code: 0, Msg: "已保存，但" + strings.Join(warnings, "；"), TS: row.UpdatedTime})
		return
	}
	response.WriteJSON(w, response.OKEmpty())
}
