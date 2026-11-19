package handler

import (
	"fmt"
	"log"
	"sort"
	"time"
)

// F2-PORT-01：live socket 探针（面板侧）。
//
// DB 统一谓词（forward_port / node_inbound / chain_tunnel＋伴生 Clash API 派生口）看不到节点
// 本机上 DB 之外的监听端口（手动服务、旧部署残留、非面板进程）。
// 保存入站/隧道显式端口前，先问 agent 扫一遍 /proc/net
// （CheckPortListening 命令，见 go-gost/x/socket/port_listening.go），
// DB＋live 双源判定占用。
//
// 探针是 best-effort、fail-open：节点离线、agent 未升级（无此命
// 令）、超时、回执异常时一律只记日志、沿用 DB 结论——探针只会多
// 判占用，绝不因探针失败拒单。探针只在锁外/事务外跑：
//   - 入站保存：锁前（R5R3-001：锁内禁网络调用）；
//   - 隧道创建/更新：验证事务回滚后、提交段持锁前的无锁间隙。
// 自动选口的探针口径（F2-PORT-01 P0-2）：
//   - 入站 Port==0：inboundSave 的 pick→probe→锁内复检循环里定口后探活，
//     命中换口重试（上限 5 次）；
//   - 隧道创建自动段口：验证段定型即最终端口（提交段复用定型值＋锁内
//     DB 复检），间隙探活、命中换口重试（上限 5 次）；
//   - 隧道更新自动段口：验证段定型即最终端口，间隙全量探（命中即拒，
//     不重挑——更新面既有口径，本轮不动）。
// 探针只探已定型的口：未定型的口（转发创建前、隧道创建验证段之前）仍只走 DB。

// livePortProbeTarget 是一台节点上待探的端口段集。
type livePortProbeTarget struct {
	NodeID int64
	Spans  []portSpan
}

// checkLivePortsOccupied 逐节点探 live 占用，返回首个占用文案；
// 无占用回 ""。任一节点探针失败只记日志、跳过该节点（fail-open）。
func (h *Handler) checkLivePortsOccupied(targets []livePortProbeTarget) string {
	for _, t := range targets {
		if t.NodeID <= 0 || len(t.Spans) == 0 {
			continue
		}
		ranges := make([]map[string]interface{}, 0, len(t.Spans))
		for _, sp := range t.Spans {
			if sp.lo <= 0 {
				continue
			}
			ranges = append(ranges, map[string]interface{}{"lo": sp.lo, "hi": sp.hi})
		}
		if len(ranges) == 0 {
			continue
		}
		res, err := h.sendNodeCommandWithTimeout(t.NodeID, "CheckPortListening", map[string]interface{}{"ranges": ranges}, defaultNodeCommandTimeout, false, false)
		if err != nil {
			log.Printf("F2-PORT-01 live port probe skipped node_id=%d err=%v", t.NodeID, err)
			continue
		}
		var probe struct {
			Listening []int `json:"listening"`
		}
		if err := remarshal(res.Data, &probe); err != nil {
			log.Printf("F2-PORT-01 live port probe decode failed node_id=%d err=%v", t.NodeID, err)
			continue
		}
		for _, p := range probe.Listening {
			for _, sp := range t.Spans {
				if p < sp.lo || p > sp.hi {
					continue
				}
				if sp.lo == sp.hi {
					return fmt.Sprintf("节点 %s 端口 %d 已被节点上其他进程占用", h.nodeLabel(t.NodeID), p)
				}
				return fmt.Sprintf("节点 %s %s 中的端口 %d 已被节点上其他进程占用", h.nodeLabel(t.NodeID), sp, p)
			}
		}
	}
	return ""
}

// probeForwardPortsOnNode asks the agent for the live state of the given
// ports (C-04): listening sockets + established TCP connections per port.
// Established is a *[]int: nil means the agent predates the `established`
// field (2.1.13 agent) and only the listening state is verifiable;
// non-nil (possibly empty) means the agent reported it.
func (h *Handler) probeForwardPortsOnNode(nodeID int64, ports []int) (listening []int, established *[]int, err error) {
	if len(ports) == 0 {
		return nil, nil, nil
	}
	ranges := make([]map[string]interface{}, 0, len(ports))
	for _, p := range ports {
		if p <= 0 {
			continue
		}
		ranges = append(ranges, map[string]interface{}{"lo": p, "hi": p})
	}
	if len(ranges) == 0 {
		return nil, nil, nil
	}
	res, err := h.sendNodeCommandWithTimeout(nodeID, "CheckPortListening", map[string]interface{}{"ranges": ranges}, defaultNodeCommandTimeout, false, false)
	if err != nil {
		return nil, nil, err
	}
	var probe struct {
		Listening   []int  `json:"listening"`
		Established *[]int `json:"established"`
	}
	if err := remarshal(res.Data, &probe); err != nil {
		return nil, nil, fmt.Errorf("decode port probe response: %w", err)
	}
	return probe.Listening, probe.Established, nil
}

// verifyForwardPortsDeadOnNode is the load-bearing probe for a NotFound
// pause (C-04): the agent knows no service under any candidate name, so
// the port state is the only evidence. Silent (no listener, no
// established connection) => already paused (true). Anything live =>
// an orphan listener holds the port (false). Probe failure is an error
// (fail-closed): nothing may be assumed dead.
func (h *Handler) verifyForwardPortsDeadOnNode(nodeID int64, ports []int) (bool, error) {
	listening, established, err := h.probeForwardPortsOnNode(nodeID, ports)
	if err != nil {
		return false, fmt.Errorf("暂停验证失败：节点 %s 端口探针失败: %w", h.nodeLabel(nodeID), err)
	}
	if len(listening) > 0 {
		return false, nil
	}
	if established != nil && len(*established) > 0 {
		return false, nil
	}
	return true, nil
}

// verifyPausedPortsDead polls (up to ~3s) for the ports to go silent after
// a successful PauseService (C-04): listener gone and no established
// connections left. Probe failure is fail-open (log only): the pause
// itself succeeded and the probe is a safety net, not a gate. An old
// agent (no `established` field) verifies the listener only.
func (h *Handler) verifyPausedPortsDead(nodeID int64, ports []int) error {
	deadline := time.Now().Add(3 * time.Second)
	for {
		listening, established, err := h.probeForwardPortsOnNode(nodeID, ports)
		if err != nil {
			log.Printf("C-04 pause verify skipped node_id=%d err=%v", nodeID, err)
			return nil
		}
		if len(listening) == 0 && (established == nil || len(*established) == 0) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("暂停后验证失败：节点 %s 端口仍在监听 %v / 仍有已建立连接 %v", h.nodeLabel(nodeID), listening, established)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// probeForwardEntryPortLive 探入口端口在各入口节点的 live 监听态
// （PortAlloc Fix B）：转发/中转创建的自动选口在 DB 判重通过后、
// 落库前再问一遍 agent——DB 看不到的本机监听（手动服务、漂移后
// 的伴生口、旧部署残留）在此拦截。只探自动选口：显式口沿用既有
// 口径（DB 预检＋下发绑口为终判），探针误报不得新增拒绝面。
// fail-open：探针失败（离线/旧 agent/超时）回 ""，调用方沿用 DB 结论。
func (h *Handler) probeForwardEntryPortLive(entryNodes []int64, port int) string {
	if port <= 0 || len(entryNodes) == 0 {
		return ""
	}
	byNode := make(map[int64][]portSpan, len(entryNodes))
	for _, nid := range entryNodes {
		byNode[nid] = []portSpan{{lo: port, hi: port, what: "入口端口"}}
	}
	return h.checkLivePortsOccupied(livePortTargetsFromMap(byNode))
}

// subtractPortSpans 从 spans 里减去 exclude 的区间（更新时排除
// 自身的监听段，避免把自己的监听器误判为占用）。
func subtractPortSpans(spans, exclude []portSpan) []portSpan {
	out := spans
	for _, ex := range exclude {
		var next []portSpan
		for _, sp := range out {
			if ex.hi < sp.lo || ex.lo > sp.hi {
				next = append(next, sp)
				continue
			}
			if sp.lo < ex.lo {
				next = append(next, portSpan{lo: sp.lo, hi: ex.lo - 1, what: sp.what})
			}
			if sp.hi > ex.hi {
				next = append(next, portSpan{lo: ex.hi + 1, hi: sp.hi, what: sp.what})
			}
		}
		out = next
	}
	return out
}

func livePortTargetsFromMap(byNode map[int64][]portSpan) []livePortProbeTarget {
	ids := make([]int64, 0, len(byNode))
	for id := range byNode {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]livePortProbeTarget, 0, len(ids))
	for _, id := range ids {
		out = append(out, livePortProbeTarget{NodeID: id, Spans: byNode[id]})
	}
	return out
}

// tunnelLivePortTargetsFromRequest 从隧道请求里取显式端口：入口
// inPort（各入口节点同值）＋出/链段逐项的 port。未指定（0）的不
// 探：自动段口由创建面的 probeTunnelAutoSegmentPortsLive 另行探活
// （命中可换口重挑，本函数只做一次拦截、无重试位）。
// own 是本隧道已占的 (节点→端口) 集，命中的跳过（更新时排除自身）。
func tunnelLivePortTargetsFromRequest(req map[string]interface{}, entryNodeIDs []int64, own map[int64]map[int]bool) []livePortProbeTarget {
	byNode := make(map[int64][]portSpan)
	add := func(nodeID int64, port int, what string) {
		if nodeID <= 0 || port <= 0 {
			return
		}
		if own != nil && own[nodeID] != nil && own[nodeID][port] {
			return
		}
		byNode[nodeID] = append(byNode[nodeID], portSpan{lo: port, hi: port, what: what})
	}
	if inPort := asInt(req["inPort"], 0); inPort > 0 {
		for _, nid := range entryNodeIDs {
			add(nid, inPort, "入口端口")
		}
	}
	for _, item := range asMapSlice(req["outNodeId"]) {
		add(asInt64(item["nodeId"], 0), asInt(item["port"], 0), "出口端口")
	}
	for _, hopRaw := range asAnySlice(req["chainNodes"]) {
		for _, item := range asMapSlice(hopRaw) {
			add(asInt64(item["nodeId"], 0), asInt(item["port"], 0), "链路段端口")
		}
	}
	return livePortTargetsFromMap(byNode)
}

// tunnelLivePortTargetsFromState 取更新提交段的最终端口集：入口
// inPort（请求显式值；未提交即沿用旧口、不探）＋验证段定型的全
// 部段口（OutNodes/ChainHops/SegmentPorts，含重挑口——定型后不再
// 变，探之有效）。游离段口（type-1）不在 OutNodes/ChainHops 里，
// 从 SegmentPorts 补（与提交段 DB 复检同口径）。
func tunnelLivePortTargetsFromState(state *tunnelCreateState, inPort int, own map[int64]map[int]bool) []livePortProbeTarget {
	if state == nil {
		return nil
	}
	byNode := make(map[int64][]portSpan)
	add := func(nodeID int64, port int, what string) {
		if nodeID <= 0 || port <= 0 {
			return
		}
		if own != nil && own[nodeID] != nil && own[nodeID][port] {
			return
		}
		byNode[nodeID] = append(byNode[nodeID], portSpan{lo: port, hi: port, what: what})
	}
	for _, n := range state.InNodes {
		add(n.NodeID, inPort, "入口端口")
	}
	for _, n := range state.OutNodes {
		add(n.NodeID, n.Port, "出口端口")
	}
	for _, hop := range state.ChainHops {
		for _, n := range hop {
			add(n.NodeID, n.Port, "链路段端口")
		}
	}
	for key, port := range state.SegmentPorts {
		add(key.nodeID, port, "链路段端口")
	}
	return livePortTargetsFromMap(byNode)
}

// tunnelOwnLivePorts 取本隧道当前已占的 (节点→端口) 集：链段行
// （chainType 2/3）＋名下转发的入口端口（forward_port）。
// forward 行读失败时回 forwardOK=false——调用方此时跳过入口探针
// （无法排除自身监听器，探了会误报），段口照常探。
func (h *Handler) tunnelOwnLivePorts(tunnelID int64, oldChainRows []chainNodeRecord) (map[int64]map[int]bool, bool) {
	own := make(map[int64]map[int]bool)
	add := func(nodeID int64, port int) {
		if nodeID <= 0 || port <= 0 {
			return
		}
		if own[nodeID] == nil {
			own[nodeID] = make(map[int]bool)
		}
		own[nodeID][port] = true
	}
	for _, row := range oldChainRows {
		if row.ChainType == 2 || row.ChainType == 3 {
			add(row.NodeID, row.Port)
		}
	}
	forwards, err := h.listForwardsByTunnel(tunnelID)
	if err != nil {
		log.Printf("F2-PORT-01 own entry ports unreadable tunnel_id=%d err=%v", tunnelID, err)
		return own, false
	}
	for _, f := range forwards {
		ports, err := h.listForwardPorts(f.ID)
		if err != nil {
			log.Printf("F2-PORT-01 own entry ports unreadable tunnel_id=%d forward_id=%d err=%v", tunnelID, f.ID, err)
			return own, false
		}
		for _, fp := range ports {
			add(fp.NodeID, fp.Port)
		}
	}
	return own, true
}
