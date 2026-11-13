package handler

import (
	"context"
	"fmt"
	"log"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
)

// relayProxyProtocolVersion is what relay forwards send when the inbound
// wants the real client address. v1 is accepted by every kernel we support.
const relayProxyProtocolVersion = 1

var loopbackAllow = []string{"127.0.0.0/8", "::1/128"}

// inboundAllowList computes the sources allowed to reach a whitelisted
// inbound: loopback (tunnel exits dial locally), every address of the nodes
// that dial the landing node (entry nodes of port forwards, exit nodes of
// tunnel forwards), the nodes of kernels chained through it, and the user's
// extra entries.
// R3X-015: 链式拨号节点查询（chainDialerNodeIDs）失败时上抛 error，
// 下发面中止整单而非下发少算拨号节点的白名单（其余既有查询的
// log-and-continue 口径不在本条范围）。
func (h *Handler) inboundAllowList(row model.NodeInbound) ([]string, error) {
	return h.inboundAllowListIx(row, nil)
}

// inboundAllowListIx is inboundAllowList with the chain-dialer lookup
// effectiveInboundWhitelist 返回入站的生效白名单（节点级覆盖
// 层）：节点级白名单开 → 该节点全部入站生效 relayWhitelist=1、
// whitelistExtra=节点级列表（逐入站存量字段不动，新入站渲染
// 时合并即自动继承）；关 → 逐入站设置照旧。中转节点与本机照
// 既有机制自动放行（见 inboundAllowListIx）。节点查询失败返
// 回错误（fail-closed：半知状态下发白名单可能错放或错拦）。
func (h *Handler) effectiveInboundWhitelist(row model.NodeInbound) (bool, string, error) {
	node, err := h.repo.GetNodeByID(row.NodeID)
	if err != nil {
		return false, "", fmt.Errorf("查询节点 %d 白名单失败: %w", row.NodeID, err)
	}
	if node != nil && node.WhitelistEnabled == 1 {
		return true, node.WhitelistExtra, nil
	}
	return row.RelayWhitelist == 1, row.WhitelistExtra, nil
}

// served from a shared snapshot when ix is non-nil (X04-001: kernel
// apply rounds compute one allow list per whitelisted inbound and used
// to reload the whole inbound table for each); a nil ix keeps the
// single-call behaviour.
func (h *Handler) inboundAllowListIx(row model.NodeInbound, ix *chainRefIndex) ([]string, error) {
	set := make(map[string]bool)
	for _, c := range loopbackAllow {
		set[c] = true
	}
	// T-002：附加名单解析失败不得静默丢弃——记错留痕（方向仍为
	// fail-closed：解析失败的附加条目不进 allow，只记日志不放行）。
	if extra, err := kernel.ParseAllowList(row.WhitelistExtra); err == nil {
		for _, c := range extra {
			set[c] = true
		}
	} else {
		log.Printf("whitelist extra allowlist parse failed inbound_id=%d err=%v (extra entries dropped)", row.ID, err)
	}

	relays, err := h.repo.ListInboundRelays(row.ID)
	if err != nil {
		log.Printf("whitelist relay lookup failed inbound_id=%d err=%v", row.ID, err)
	}
	seenNodes := make(map[int64]bool)
	for _, rel := range relays {
		if !rel.ForwardExists {
			continue
		}
		dialers, err := h.relayDialerNodeIDs(rel.TunnelID, rel.TunnelType)
		if err != nil {
			// T-002：拨号节点查询失败不得静默跳过——记错留痕（方向仍
			// 为 fail-closed：查不到的拨号节点不进 allow，只记日志）。
			log.Printf("whitelist relay dialer lookup failed inbound_id=%d tunnel_id=%d err=%v (dialer nodes dropped)", row.ID, rel.TunnelID, err)
			continue
		}
		for _, nodeID := range dialers {
			if seenNodes[nodeID] {
				continue
			}
			seenNodes[nodeID] = true
			node, err := h.repo.GetNodeByID(nodeID)
			if err != nil || node == nil {
				// T-002：同上，拨号节点行查不到时记错留痕，不进 allow。
				log.Printf("whitelist relay dialer node lookup failed inbound_id=%d node_id=%d err=%v (node dropped)", row.ID, nodeID, err)
				continue
			}
			for _, host := range nodeAddresses(node) {
				for _, c := range resolveAllowHost(host) {
					set[c] = true
				}
			}
		}
	}

	// Kernels chained through the inbound dial it from their own node.
	var chainDialers []int64
	if ix != nil {
		chainDialers = ix.dialerNodeIDs(row.ID)
	} else {
		var err error
		chainDialers, err = h.chainDialerNodeIDs(row.ID)
		if err != nil {
			return nil, err
		}
	}
	for _, nodeID := range chainDialers {
		if seenNodes[nodeID] {
			continue
		}
		seenNodes[nodeID] = true
		node, err := h.repo.GetNodeByID(nodeID)
		if err != nil || node == nil {
			// T-002：链式拨号节点行查不到时记错留痕，不进 allow
			//（fail-closed；链式查询本身失败仍按 R3X-015 上抛）。
			log.Printf("whitelist chain dialer node lookup failed inbound_id=%d node_id=%d err=%v (node dropped)", row.ID, nodeID, err)
			continue
		}
		for _, host := range nodeAddresses(node) {
			for _, c := range resolveAllowHost(host) {
				set[c] = true
			}
		}
	}

	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Strings(out)
	return out, nil
}

// relayDialerNodeIDs returns the nodes whose connections reach the landing
// node: a port-forward tunnel dials from its entry nodes, a tunnel forward
// from its exit nodes.
func (h *Handler) relayDialerNodeIDs(tunnelID int64, tunnelType int) ([]int64, error) {
	if tunnelType == 2 {
		return h.repo.TunnelExitNodeIDs(tunnelID)
	}
	return h.tunnelEntryNodeIDs(tunnelID)
}

func nodeAddresses(node *model.Node) []string {
	raw := []string{node.ServerIP, node.ServerIPV4.String, node.ServerIPV6.String, node.ExtraIPs.String}
	var out []string
	for _, r := range raw {
		for _, tok := range strings.FieldsFunc(r, func(c rune) bool {
			return c == ',' || c == ';' || c == ' ' || c == '\n' || c == '\t'
		}) {
			// R28-FIX-F01（sweep）：ServerIP 可含 :port；原值进
			// resolveAllowHost 既过不了 NormalizeAllowEntry、DNS
			// 解析亦失败，白名单静默丢拨号节点。剥离后裸 host
			// 才能进 allow 集（裸 IP/括号 v6 时为 no-op）。
			if tok = stripShareHostPort(tok); tok != "" {
				out = append(out, tok)
			}
		}
	}
	return out
}

// resolveAllowHost turns an IP or hostname into CIDRs; hostnames are resolved
// with a short timeout so an unreachable DNS server cannot stall an apply.
func resolveAllowHost(host string) []string {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" {
		return nil
	}
	if c, err := kernel.NormalizeAllowEntry(host); err == nil {
		return []string{c}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		log.Printf("whitelist: cannot resolve %q: %v", host, err)
		return nil
	}
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		if c, err := kernel.NormalizeAllowEntry(a.IP.String()); err == nil {
			out = append(out, c)
		}
	}
	return out
}

// syncRelayProxyProtocol makes every managed relay forward of an inbound send
// (or stop sending) PROXY protocol to match the inbound setting.
func (h *Handler) syncRelayProxyProtocol(row model.NodeInbound) []string {
	want := 0
	if row.AcceptProxyProtocol == 1 {
		want = relayProxyProtocolVersion
	}
	relays, err := h.repo.ListInboundRelays(row.ID)
	if err != nil {
		return []string{err.Error()}
	}
	var problems []string
	for _, rel := range relays {
		if !rel.ForwardExists || rel.Managed != 1 {
			continue
		}
		fwd, err := h.getForwardRecord(rel.ForwardID)
		if err != nil || fwd == nil {
			continue
		}
		_, current := normalizeForwardProxyProtocol(fwd.ProxyProtocol, fwd.ProxyProtocolReceive, fwd.ProxyProtocolSend)
		if current == want {
			continue
		}
		if err := h.repo.UpdateForward(fwd.ID, fwd.Name, fwd.TunnelID, fwd.RemoteAddr, fwd.Strategy, time.Now().UnixMilli(),
			fwd.SpeedID, fwd.MaxConn, fwd.IPMaxConn, fwd.IPSpeedID, 0, fwd.ProxyProtocolReceive, want); err != nil {
			problems = append(problems, rel.Name+": "+err.Error())
			continue
		}
		updated, err := h.getForwardRecord(fwd.ID)
		if err != nil || updated == nil {
			continue
		}
		if err := h.syncForwardServices(updated, "UpdateService", true); err != nil {
			problems = append(problems, rel.Name+": "+err.Error())
		}
	}
	return problems
}

// inboundIDsForForward returns the inbounds relayed through a forward
// (X-P2-3 收集逻辑：forwardID → NodeInboundRelay.InboundID)。
func (h *Handler) inboundIDsForForward(forwardID int64) []int64 {
	rels, err := h.repo.ListNodeInboundRelays()
	if err != nil {
		return nil
	}
	var out []int64
	seen := make(map[int64]struct{})
	for _, rel := range rels {
		if rel.ForwardID != forwardID {
			continue
		}
		if _, ok := seen[rel.InboundID]; ok {
			continue
		}
		seen[rel.InboundID] = struct{}{}
		out = append(out, rel.InboundID)
	}
	return out
}

// inboundIDsForForwards is the collection half of the shared whitelist
// funnel (R3 批 4 / verdicts §8 #1): the union of inboundIDsForForward
// over a forward set, deduplicated. Every write path that removes or
// re-homes forwards must collect through this helper BEFORE the
// mutation lands (the relay rows that map forwards to inbounds are
// cascade-deleted with the forwards) and flush through
// refreshInboundWhitelists AFTER it commits — the exact orchestration
// forwardDelete has always used, now shared by forwardUpdate tunnel
// switches, cleanupForwardsForUserTunnel, node deletion and the
// pending-delete job instead of being re-implemented (or forgotten)
// per call site.
func (h *Handler) inboundIDsForForwards(forwardIDs []int64) []int64 {
	var out []int64
	seen := make(map[int64]struct{})
	for _, forwardID := range forwardIDs {
		for _, inboundID := range h.inboundIDsForForward(forwardID) {
			if _, ok := seen[inboundID]; ok {
				continue
			}
			seen[inboundID] = struct{}{}
			out = append(out, inboundID)
		}
	}
	return out
}

// inboundIDsAffectedByNodeDelete collects, before a node is deleted,
// the surviving inbounds whose allow list embeds the node's addresses
// (R3X-017): inbounds dialed via a ChainTunnel row referencing the
// node or chained through by one of the node's inbounds
// (inboundIDsDialedByNode), plus the inbounds on other nodes that use
// one of the node's inbounds as their chained outbound target
// (chainReferrers of the node's inbounds). The node's own inbounds
// are excluded — they are deleted with the node.
// 集成合并注记（R3 批4×批9）：本收集器位于删节点路径上，其数据源
// inboundIDsDialedByNode / chainReferrers 已按 R3X-015 改为错误上抛，
// 故本函数同步上抛——收集失败时调用方中止删除（fail-closed），不得
// 以不完整清单照删后漏刷新白名单。
func (h *Handler) inboundIDsAffectedByNodeDelete(nodeID int64) ([]int64, error) {
	ix, err := h.loadChainRefIndex()
	if err != nil {
		return nil, err
	}
	return h.inboundIDsAffectedByNodeDeleteIx(nodeID, ix)
}

// inboundIDsAffectedByNodeDeleteIx is inboundIDsAffectedByNodeDelete
// over a shared snapshot (X04-001): the dialed-set scan and the
// per-target referrer pass both read the one table load.
func (h *Handler) inboundIDsAffectedByNodeDeleteIx(nodeID int64, ix *chainRefIndex) ([]int64, error) {
	own := make(map[int64]struct{})
	if targets, err := h.repo.ListNodeInboundsByNode(nodeID); err == nil {
		for _, target := range targets {
			own[target.ID] = struct{}{}
		}
	}
	var out []int64
	seen := make(map[int64]struct{})
	add := func(inboundID int64) {
		if _, isOwn := own[inboundID]; isOwn {
			return
		}
		if _, dup := seen[inboundID]; dup {
			return
		}
		seen[inboundID] = struct{}{}
		out = append(out, inboundID)
	}
	dialedIDs, err := h.inboundIDsDialedByNodeIx(nodeID, ix)
	if err != nil {
		return nil, err
	}
	for _, inboundID := range dialedIDs {
		add(inboundID)
	}
	if targets, err := h.repo.ListNodeInboundsByNode(nodeID); err == nil {
		for _, target := range targets {
			for _, ref := range ix.referrers(target.ID) {
				if ref.NodeID != nodeID {
					add(ref.ID)
				}
			}
		}
	}
	return out, nil
}

// inboundIDsForTunnel returns the inbounds relayed through any forward of
// a tunnel（隧道名下转发 → NodeInboundRelay → inboundID，与 tunnelDelete
// 的 O3-001 收集逻辑同一口径）。
func (h *Handler) inboundIDsForTunnel(tunnelID int64) []int64 {
	forwards, err := h.repo.ListForwardsByTunnel(tunnelID)
	if err != nil {
		return nil
	}
	var out []int64
	seen := make(map[int64]struct{})
	for _, fwd := range forwards {
		for _, inboundID := range h.inboundIDsForForward(fwd.ID) {
			if _, ok := seen[inboundID]; ok {
				continue
			}
			seen[inboundID] = struct{}{}
			out = append(out, inboundID)
		}
	}
	return out
}

// inboundIDsDialedByNode returns the inbounds whose dialer set includes
// the given node (relay dialers of their forwards' tunnels, plus kernels
// chained through them) — the set whose allow list embeds the node's
// addresses (R2X-008 / E S-1: 节点改址后须刷新这些入站）。
// R3X-015: 顶层列表查询与 chainDialerNodeIDs 的失败上抛（旧实现
// 返回 nil 被调用方读作「无入站经本节点拨号」跳过刷新）。
func (h *Handler) inboundIDsDialedByNode(nodeID int64) ([]int64, error) {
	ix, err := h.loadChainRefIndex()
	if err != nil {
		return nil, err
	}
	return h.inboundIDsDialedByNodeIx(nodeID, ix)
}

// inboundIDsDialedByNodeIx is inboundIDsDialedByNode over a shared
// snapshot (X04-001). This scan is the Θ(T) half of the old blowup: it
// walks every row of the table and used to call chainDialerNodeIDs —
// a full table reload plus parse — for each one.
func (h *Handler) inboundIDsDialedByNodeIx(nodeID int64, ix *chainRefIndex) ([]int64, error) {
	var out []int64
	for _, row := range ix.rows {
		hit := false
		if relays, err := h.repo.ListInboundRelays(row.ID); err == nil {
			for _, rel := range relays {
				if !rel.ForwardExists {
					continue
				}
				dialers, derr := h.relayDialerNodeIDs(rel.TunnelID, rel.TunnelType)
				if derr != nil {
					continue
				}
				for _, d := range dialers {
					if d == nodeID {
						hit = true
						break
					}
				}
				if hit {
					break
				}
			}
		}
		if !hit {
			for _, d := range ix.dialerNodeIDs(row.ID) {
				if d == nodeID {
					hit = true
					break
				}
			}
		}
		if hit {
			out = append(out, row.ID)
		}
	}
	return out, nil
}

// refreshInboundWhitelists refreshes each affected inbound once and
// returns the IDs whose refresh failed (T-001): callers must surface
// the failure in the triggering operation's receipt — a success receipt
// issued while the kernel still enforces the pre-change allow list is
// a false receipt (new relays cannot connect, deleted relays stay
// allowed). Background callers without a receipt log the IDs instead.
func (h *Handler) refreshInboundWhitelists(inboundIDs []int64) []int64 {
	seen := make(map[int64]struct{}, len(inboundIDs))
	var failed []int64
	for _, inboundID := range inboundIDs {
		if _, ok := seen[inboundID]; ok {
			continue
		}
		seen[inboundID] = struct{}{}
		if err := h.refreshInboundWhitelist(inboundID); err != nil {
			failed = append(failed, inboundID)
		}
	}
	return failed
}

// refreshInboundWhitelist re-applies the kernel when the relay set of a
// whitelisted inbound changed, so the allowed sources follow the relays.
// It returns nil when the inbound is missing or its effective whitelist
// is off (no-op) or the re-apply succeeded, and the apply error
// otherwise; the failure is also logged server-side (T-001).
func (h *Handler) refreshInboundWhitelist(inboundID int64) error {
	row, err := h.repo.GetNodeInbound(inboundID)
	if err != nil || row == nil {
		return nil
	}
	effective, _, werr := h.effectiveInboundWhitelist(*row)
	if werr != nil {
		log.Printf("whitelist refresh failed inbound_id=%d err=%v", inboundID, werr)
		return werr
	}
	if !effective {
		return nil
	}
	if err := h.applyKernel(row.KernelID, false); err != nil {
		log.Printf("whitelist refresh failed inbound_id=%d err=%v", inboundID, err)
		return err
	}
	return nil
}

// whitelistRefreshFailureNote renders the operator-facing note for failed
// whitelist refreshes (T-001): the triggering operation itself succeeded,
// but the kernel still enforces the pre-change allow list.
func whitelistRefreshFailureNote(failed []int64) string {
	if len(failed) == 0 {
		return ""
	}
	ids := make([]string, 0, len(failed))
	for _, id := range failed {
		ids = append(ids, strconv.FormatInt(id, 10))
	}
	return "白名单刷新失败（入站 " + strings.Join(ids, "、") + "），内核仍按旧名单放行"
}
