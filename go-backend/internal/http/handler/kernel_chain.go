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
	e.Host = strings.TrimSpace(target.ShareHost)
	if e.Host == "" {
		if node, err := h.repo.GetNodeByID(target.NodeID); err == nil && node != nil {
			e.Host = strings.TrimSpace(node.ServerIP)
		}
	}
	if e.Host == "" {
		return e, fmt.Errorf("出站节点「%s」没有可用的地址", target.Name)
	}
	return e, nil
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
		if row.ID != 0 && h.chainReaches(target, row.ID) {
			return fmt.Errorf("%s形成了循环，请检查链路", what)
		}
		e, err := h.chainEndpoint(target, row.NodeID)
		if err != nil {
			return fmt.Errorf("%s：%v", what, err)
		}
		// 出站 IP 策略是入站级的：链式/分流目标若是与策略族冲突的字面
		// IP，保存期即拒绝（域名放行，由内核按同策略解析，S4）。
		if err := kernel.ValidateOutboundIPServer(row.OutboundIPStrategy, e.Host); err != nil {
			return fmt.Errorf("%s：%w", what, err)
		}
		if err := kernel.CheckChainWith(kernelType, e, kernel.RenderOptions{SingboxSnell: !versionBelow(kernelVersion, 1, 14)}); err != nil {
			return fmt.Errorf("%s：%v", what, err)
		}
		return nil
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
	if kernelType == kernel.TypeSnell && strings.TrimSpace(row.SplitRules) != "" {
		return errors.New("Snell 内核不支持分流")
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

// chainReferrers returns the inbounds that send traffic through the given
// one, as their chained outbound or a 分流 target.
func (h *Handler) chainReferrers(targetID int64) []model.NodeInbound {
	rows, err := h.repo.ListNodeInbounds()
	if err != nil {
		log.Printf("chain referrer lookup failed inbound_id=%d err=%v", targetID, err)
		return nil
	}
	var out []model.NodeInbound
	for _, row := range rows {
		for _, id := range outboundTargets(row) {
			if id == targetID {
				out = append(out, row)
				break
			}
		}
	}
	return out
}

// chainInUseMessage explains why an inbound cannot be removed, or "" when
// nothing is chained through it.
func (h *Handler) chainInUseMessage(target *model.NodeInbound) string {
	refs := h.chainReferrers(target.ID)
	if len(refs) == 0 {
		return ""
	}
	names := make([]string, 0, len(refs))
	for _, r := range refs {
		names = append(names, "「"+r.Name+"」")
	}
	return fmt.Sprintf("「%s」正被 %s 用作出站代理或分流出口，请先解除", target.Name, strings.Join(names, "、"))
}

// kernelChainGuard 是 R1 X-P2-2（D-R1-02，按 V-03）的卸载守卫：对内核的
// 全部入站逐个跑 chainReferrers，收集「内核之外」的引用方（同内核入站
// 互相引用不算被断——它们随卸载一并删除，与 inboundBatchDelete 的
// batchDeletesReferrers 口径一致）。返回去重后的引用方入站名与按
// chainInUseMessage 口径拼装的说明（无外部引用时说明为空）。
func (h *Handler) kernelChainGuard(kernelID int64) (referrerNames []string, message string) {
	rows, err := h.repo.ListNodeInboundsByKernel(kernelID)
	if err != nil {
		log.Printf("kernel chain guard lookup failed kernel_id=%d err=%v", kernelID, err)
		return nil, ""
	}
	own := make(map[int64]struct{}, len(rows))
	for _, row := range rows {
		own[row.ID] = struct{}{}
	}
	seen := make(map[string]struct{})
	var parts []string
	for _, row := range rows {
		var refNames []string
		for _, ref := range h.chainReferrers(row.ID) {
			if _, isOwn := own[ref.ID]; isOwn {
				continue
			}
			refNames = append(refNames, "「"+ref.Name+"」")
			if _, dup := seen[ref.Name]; !dup {
				seen[ref.Name] = struct{}{}
				referrerNames = append(referrerNames, ref.Name)
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
func (h *Handler) applyChainReferrers(targetID int64) []string {
	var warnings []string
	seen := map[int64]bool{}
	for _, ref := range h.chainReferrers(targetID) {
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
// outbound, for its whitelist.
func (h *Handler) chainDialerNodeIDs(inboundID int64) []int64 {
	var ids []int64
	for _, ref := range h.chainReferrers(inboundID) {
		if !isExternalInbound(&ref) {
			ids = append(ids, ref.NodeID)
		}
	}
	return ids
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
	row.UpdatedTime = time.Now().UnixMilli()
	if err := h.repo.SaveNodeInbound(row); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
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
