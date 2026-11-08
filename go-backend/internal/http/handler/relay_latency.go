package handler

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"go-backend/internal/http/response"
	"go-backend/internal/store/repo"
)

// Relay line latency: what a user behind a relay line sees, estimated as the
// entry node's latency to each reference point plus every hop the line
// takes to the landing (entry → tunnel hops → exit → landing). TCP connect
// times are one round trip each, so they add up to the line's round trip.

const relayPathPointName = "中转路径"

// relayHop is one leg of a relay line, measured from its first node.
type relayHop struct {
	From      string  `json:"from"`
	To        string  `json:"to"`
	OK        bool    `json:"ok"`
	LatencyMs int64   `json:"latencyMs,omitempty"`
	LossPct   float64 `json:"lossPct,omitempty"`
	Error     string  `json:"error,omitempty"`
	// Untested（R7-006）：目标段为 UDP 基协议段时本腿未实测——
	// 段服务在该口只绑 UDP、无 TCP 监听，TcpPing 恒失败并不代表
	// 线路不通。与质量探测 TunnelQualityHop.Untested 同口径：
	// 不探测、不判不通、不进 pathOK/延迟/丢包聚合，呈现层据此
	// 走中性「未测」形态。
	Untested bool `json:"untested,omitempty"`
}

// relayLatency is a relay line's estimated latency to each reference point
// (Points), with the legs it is made of.
type relayLatency struct {
	externalProbe
	Entry       string     `json:"entry,omitempty"`
	EntryNodeID int64      `json:"entryNodeId,omitempty"`
	Hops        []relayHop `json:"hops"`
	PathMs      int64      `json:"pathMs,omitempty"`
	// PartialUntested（R8-024）：PathMs 只累加实测腿、且至少一腿
	// 未测时为真——PathMs 是部分和，不是完整路径值，呈现层据此
	// 与全实测总值区分（点位级同名标记同步置位）。
	PartialUntested bool `json:"partialUntested,omitempty"`
}

type relayLatencyStore struct {
	mu      sync.RWMutex
	results map[int64]relayLatency
	rounds  probeRounds
}

func newRelayLatencyStore() *relayLatencyStore {
	return &relayLatencyStore{results: make(map[int64]relayLatency)}
}

func (s *relayLatencyStore) get(relayID int64) (relayLatency, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.results[relayID]
	return r, ok
}

// replace stores fresh results; a full round also drops deleted relays.
func (s *relayLatencyStore) replace(results map[int64]relayLatency, all bool) {
	s.replaceRound(results, all, time.Now().UnixNano())
}

// replaceRound is replace gated per entry by probeRounds（DB-4）。
func (s *relayLatencyStore) replaceRound(results map[int64]relayLatency, all bool, started int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if all {
		for id := range s.results {
			if _, ok := results[id]; ok {
				continue
			}
			if !s.rounds.take(id, started) {
				continue
			}
			delete(s.results, id)
			delete(s.rounds, id)
		}
	}
	for id, r := range results {
		if s.rounds.take(id, started) {
			s.results[id] = r
		}
	}
}

// relayLeg is one hop to measure: from a node to an address.
type relayLeg struct {
	fromID   int64
	fromName string
	toName   string
	host     string
	port     int
	// err is set when the leg cannot be measured (shared node, no address).
	err string
	// untested marks a leg whose target segment is a UDP-based
	// protocol segment (R7-006): it is never probed and never
	// judged down. Panel-side facts (offline/shared/resolve
	// failure) still land in err and take precedence, mirroring
	// the quality prober's ordering.
	untested bool
}

type relayPlan struct {
	entry chainNodeRecord
	legs  []relayLeg
	err   string
}

// planRelayPath lists the legs of a relay line: the tunnel's entry node to
// each hop (first node of each group) and its exit, then on to the
// forward's target unless the tunnel exits on the landing itself.
func (h *Handler) planRelayPath(rw repo.InboundRelayView, landing string, cache map[int64]*nodeRecord) relayPlan {
	if !rw.ForwardExists || rw.TunnelID <= 0 {
		return relayPlan{err: "中转转发不存在"}
	}
	tunnel, err := h.getTunnelRecord(rw.TunnelID)
	if err != nil {
		return relayPlan{err: err.Error()}
	}
	rows, err := h.listChainNodesForTunnel(rw.TunnelID)
	if err != nil {
		return relayPlan{err: err.Error()}
	}
	inNodes, hopGroups, outNodes := splitChainNodeGroups(rows)
	if len(inNodes) == 0 {
		return relayPlan{err: "隧道没有入口节点"}
	}
	plan := relayPlan{entry: inNodes[0]}
	prev := inNodes[0]
	leg := func(to chainNodeRecord) relayLeg {
		l := relayLeg{fromID: prev.NodeID, fromName: prev.NodeName, toName: to.NodeName}
		// R7-006：段协议判定与质量探测/诊断面同族同口径（段行协议
		// 优先、回退隧道协议，见 chainSegmentProtocol）；UDP 基
		// 协议段腿按未测处置，不以 TcpPing 恒测冒充不通。
		l.untested = isUDPBasedProtocol(chainSegmentProtocol(to.Protocol, tunnel.Protocol))
		fromNode, _ := h.cachedNode(cache, prev.NodeID)
		toNode, err := h.cachedNode(cache, to.NodeID)
		if err != nil {
			l.err = err.Error()
			return l
		}
		l.host, l.port, err = resolveChainProbeTarget(fromNode, toNode, to.Port, h.repo.GetTunnelIPPreference(rw.TunnelID), to.ConnectIP)
		if err != nil {
			l.err = err.Error()
		}
		return l
	}
	if tunnel.Type == 2 {
		var seq []chainNodeRecord
		for _, g := range hopGroups {
			if len(g) > 0 {
				seq = append(seq, g[0])
			}
		}
		if len(outNodes) > 0 {
			seq = append(seq, outNodes[0])
		}
		for _, n := range seq {
			plan.legs = append(plan.legs, leg(n))
			prev = n
		}
	}
	targets := splitRemoteTargets(rw.RemoteAddr)
	if len(targets) == 0 {
		plan.err = "中转转发没有目标地址"
		return plan
	}
	host, port, err := parseTargetAddress(targets[0])
	if err != nil {
		plan.err = "中转转发目标地址无效"
		return plan
	}
	// A tunnel exiting on the landing dials the inbound locally: no leg.
	if ip := net.ParseIP(host); tunnel.Type == 2 && ip != nil && ip.IsLoopback() {
		return plan
	}
	// R7-006 末腿裁定（出口→落地目标）：目标协议不带入本腿是数据
	// 本身决定的——转发记录（forward 表）没有协议字段，同一转发
	// 的目标口可同时承载 TCP 与 UDP（nft 绑定默认 tcp,udp 双协
	// 议），目标协议是逐连接属性而非线路属性，plan 侧无任何可信
	// 数据源可判其是否为 UDP 服务。为一条低危呈现问题给转发加
	// 协议字段/改产品语义成本不合理，故末腿维持 TcpPing 实测；
	// 链路段腿（目标行 Protocol 可信）已按未测处置。
	plan.legs = append(plan.legs, relayLeg{fromID: prev.NodeID, fromName: prev.NodeName, toName: landing, host: host, port: port})
	return plan
}

// combineLoss is the loss of two legs in a row, in percent.
func combineLoss(a, b float64) float64 {
	return float64(int((1-(1-a/100)*(1-b/100))*100 + 0.5))
}

// relayLatencyFrom adds the measured legs to the entry node's latency to
// each reference point. Without reference points the path alone is shown.
func relayLatencyFrom(plan relayPlan, hops []relayHop, entry *externalProbe, now int64) relayLatency {
	out := relayLatency{Hops: hops, Entry: plan.entry.NodeName, EntryNodeID: plan.entry.NodeID}
	out.CheckedAt = now
	if plan.err != "" {
		out.Error = plan.err
		return out
	}
	pathOK, pathLoss, pathErr := true, 0.0, ""
	measuredHops, untestedHops := 0, 0
	for _, hop := range hops {
		// R7-006：未测腿（UDP 协议段）不判不通、不进 pathOK 判定、
		// 不贡献延迟/丢包——与质量探测聚合口径一致。带 Error 的
		// 腿是面板侧事实（离线/共享/解析失败），不被未测豁免。
		if hop.Untested && hop.Error == "" {
			untestedHops++
			continue
		}
		if !hop.OK {
			pathOK = false
			if pathErr == "" {
				pathErr = fmt.Sprintf("%s → %s %s", hop.From, hop.To, firstNonEmpty(hop.Error, "不通"))
			}
			continue
		}
		measuredHops++
		out.PathMs += hop.LatencyMs
		pathLoss = combineLoss(pathLoss, hop.LossPct)
	}
	// R8-024：部分未测——总值（PathMs 与点位值）只含实测腿，必须
	// 带可机读的不完整标记，不得与全实测总值同形呈现。
	partial := measuredHops > 0 && untestedHops > 0
	out.PartialUntested = partial
	var points []latencyPoint
	if entry != nil && len(entry.Points) > 0 {
		// RB3-01：全腿未测时参考点分支同守卫——路径贡献恒 0，
		// 入口点自身的实测值不是路径延迟，不得以 OK 点位冒充
		// 实测（与下方无参考点分支的全未测守卫同口径）。
		allUntested := pathOK && measuredHops == 0 && untestedHops > 0
		for _, pt := range entry.Points {
			p := latencyPoint{Name: pt.Name, Pinned: pt.Pinned, Route: pt.Route}
			switch {
			case !pt.OK:
				p.Error = pt.Error
			case !pathOK:
				p.Error = "线路不通"
			case allUntested:
				p.Untested = true
			default:
				p.OK = true
				p.LatencyMs = pt.LatencyMs + out.PathMs
				p.LossPct = combineLoss(pt.LossPct, pathLoss)
				if partial {
					p.PartialUntested = true
				}
			}
			points = append(points, p)
		}
	} else {
		p := latencyPoint{Name: relayPathPointName, OK: pathOK, Error: pathErr}
		switch {
		case pathOK && measuredHops == 0:
			// 无任何实测腿：全腿未测（untestedHops>0）或零腿
			// 路径（untestedHops==0，如 type-2 回环落地＋零链路
			// 段，R8-048）。两者都没有实测延迟可报，不得以
			// PathMs=0 冒充 1ms 完美样本（与质量探测对「段不
			// 存在/未测」用哨兵区分的同仓纪律一致，R7-006）。
			// 点位记未测、数值留空，呈现层走中性「未测」形态。
			p.OK = false
			p.Untested = true
			p.Error = ""
		case pathOK:
			p.LatencyMs, p.LossPct = out.PathMs, pathLoss
			if p.LatencyMs < 1 {
				p.LatencyMs = 1
			}
			if partial {
				p.PartialUntested = true
			}
		}
		points = []latencyPoint{p}
	}
	probe := latencyProbe(points, now)
	if !pathOK {
		probe.Error = pathErr
	} else if allPointsUntested(points) {
		// latencyProbe 对无 OK 点位填的默认误差文案不适用于未测。
		// R8-020：清除条件按参考点数量泛化——全部点位未测即清
		// （RB3-01 的清理原先只匹配单点形态，多参考点时顶层
		// 残留「测试点都连不上」与点位未测语义自相矛盾）。点位
		// 自身实测失败（Error 非空、Untested 为假）不被豁免。
		probe.Error = ""
	}
	out.externalProbe = probe
	return out
}

// allPointsUntested reports whether every point was deliberately left
// unmeasured: no point answered and no point carries a real failure.
func allPointsUntested(points []latencyPoint) bool {
	if len(points) == 0 {
		return false
	}
	for _, p := range points {
		if !p.Untested {
			return false
		}
	}
	return true
}

// probeRelayLatency measures the given relay lines (all when ids is
// empty). Entry nodes' reference point latency comes from the last
// node round.
//
// fullReplace states the round's cache semantics explicitly — the
// caller declares it, it is never inferred from len(ids) inside
// (R3-B-02: the two streak-carrying probers dropped that inference in
// R2 for probeRoundKind; this store has no streak, but the same
// latent trap would arm itself the day relay alerts learn to count).
// Whole-set rounds (the scheduled loop, the config-save refresh, the
// probe endpoint's empty-ids "all lines" contract) pass true; the
// reconnect retest, which measures only the affected lines, passes
// false so untouched lines keep their cached results.
func (h *Handler) probeRelayLatency(ctx context.Context, ids []int64, fullReplace bool) (map[int64]relayLatency, error) {
	started := time.Now().UnixNano()
	rows, err := h.repo.ListInboundRelays(0)
	if err != nil {
		return nil, err
	}
	inbounds, err := h.repo.ListNodeInbounds()
	if err != nil {
		return nil, err
	}
	landing := make(map[int64]string, len(inbounds))
	for _, in := range inbounds {
		landing[in.ID] = in.Name
	}
	want := make(map[int64]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	cache := map[int64]*nodeRecord{}
	plans := make(map[int64]relayPlan)
	hops := make(map[int64][]relayHop)
	for _, rw := range rows {
		if len(ids) > 0 && !want[rw.ID] {
			continue
		}
		plan := h.planRelayPath(rw, firstNonEmpty(landing[rw.InboundID], "落地"), cache)
		for i := range plan.legs {
			l := &plan.legs[i]
			if l.err != "" {
				continue
			}
			if n, err := h.cachedNode(cache, l.fromID); err != nil {
				l.err = err.Error()
			} else if n.Status != 1 {
				l.err = "节点离线"
			}
		}
		plans[rw.ID] = plan
		hops[rw.ID] = make([]relayHop, len(plan.legs))
	}

	// DA-6：与节点延迟轮同口径，派发前按腿的出发节点交错。
	type relayJob struct {
		id  int64
		i   int
		leg relayLeg
		hop relayHop
	}
	jobs := make([]relayJob, 0, len(plans))
	for id, plan := range plans {
		for i, l := range plan.legs {
			hop := relayHop{From: l.fromName, To: l.toName, Error: l.err}
			if l.err != "" {
				hops[id][i] = hop
				continue
			}
			// R7-006：UDP 协议段腿不派发 TcpPing 探测，直接记未测。
			if l.untested {
				hop.Untested = true
				hops[id][i] = hop
				continue
			}
			jobs = append(jobs, relayJob{id, i, l, hop})
		}
	}
	jobs = interleaveByNode(jobs, func(j relayJob) int64 { return j.leg.fromID })

	var wg sync.WaitGroup
	sem := make(chan struct{}, latencyParallel)
	for _, j := range jobs {
		if ctx.Err() != nil {
			hops[j.id][j.i] = j.hop
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(j relayJob) {
			defer wg.Done()
			defer func() { <-sem }()
			data, err := nodeLatencyPing(h, j.leg.fromID, j.leg.host, j.leg.port)
			p := latencyPointFrom(latencyTarget{}, data, err)
			j.hop.OK, j.hop.LatencyMs, j.hop.LossPct, j.hop.Error = p.OK, p.LatencyMs, p.LossPct, p.Error
			// Each goroutine writes its own slot.
			hops[j.id][j.i] = j.hop
		}(j)
	}
	wg.Wait()

	now := time.Now().UnixMilli()
	results := make(map[int64]relayLatency, len(plans))
	for id, plan := range plans {
		var entry *externalProbe
		if plan.entry.NodeID > 0 {
			if p, ok := h.nodeLatency.get(plan.entry.NodeID); ok {
				p = h.withRoutes(plan.entry.NodeID, p)
				entry = &p
			}
		}
		results[id] = relayLatencyFrom(plan, hops[id], entry, now)
	}
	if ctx.Err() == nil {
		h.relayLatency.replaceRound(results, fullReplace, started)
	}
	return results, nil
}

// inboundRelayProbe measures relay lines now: the given ids, or all of them.
// Their entry nodes are measured first so the totals are current.
func (h *Handler) inboundRelayProbe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	if len(h.latencyPool()) > 0 {
		// R1-B-02：空 ids 在本入口是「全部中转线」，不是 probeNodeLatency
		// 的「全部节点全量轮」——展开为这些线各自的入口节点显式列表，
		// 预刷保持定向（DB-4：手动点击不计延迟告警 streak）且范围只限
		// 本页展示的中转入口，不再顺带全量探测无关节点。
		rows, err := h.repo.ListInboundRelays(0)
		if err != nil {
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
		want := make(map[int64]bool, len(req.IDs))
		for _, id := range req.IDs {
			want[id] = true
		}
		seen := make(map[int64]bool)
		var entries []int64
		for _, rw := range rows {
			if (len(req.IDs) > 0 && !want[rw.ID]) || rw.TunnelID <= 0 {
				continue
			}
			if nodes, err := h.listChainNodesForTunnel(rw.TunnelID); err == nil {
				if in, _, _ := splitChainNodeGroups(nodes); len(in) > 0 && !seen[in[0].NodeID] {
					seen[in[0].NodeID] = true
					entries = append(entries, in[0].NodeID)
				}
			}
		}
		if len(entries) > 0 {
			if _, err := h.probeNodeLatency(r.Context(), entries, probeRoundTargeted); err != nil {
				response.WriteJSON(w, response.Err(-2, err.Error()))
				return
			}
		}
	}
	// 本入口的 API 契约：空 ids = 全部中转线（整表替换），非空 =
	// 定向刷新（合并）。轮次语义在入口按契约显式求值后传参，
	// prober 内部不再以 len 推断（R3-B-02）。
	results, err := h.probeRelayLatency(r.Context(), req.IDs, len(req.IDs) == 0)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	out := make(map[string]relayLatency, len(results))
	for id, res := range results {
		out[strconv.FormatInt(id, 10)] = res
	}
	response.WriteJSON(w, response.OK(out))
}

// relayLatencyView is a relay's last measurement for the relay list.
func (h *Handler) relayLatencyView(relayID int64) *relayLatency {
	if r, ok := h.relayLatency.get(relayID); ok {
		return &r
	}
	return nil
}
