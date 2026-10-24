package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"go-backend/internal/http/response"
)

// latencyTargetsConfigKey holds the reference points nodes measure their
// latency to, as a JSON array of latencyTarget. Empty means none.
const latencyTargetsConfigKey = "latency_targets"

const (
	maxLatencyTargets       = 30
	maxPinnedLatencyTargets = 3
	maxLatencyTargetName    = 20
	latencyPingCount        = 4
	latencyPingTimeoutMS    = 2000
	latencyParallel         = 16
	// O1-004/J-003：空 IDs 全量探测时收敛上限，防止大规模节点下无确认的出站风暴
	maxProbeAllNodes = 50
	// DB-5（移植自 kure29/flyx 3.3.33）：单节点一轮探测按 agent 槽位
	// 分波——命令超时须容纳全部波次串行排队的最坏耗时。
	latencyAgentParallel  = agentTCPPingLimit
	latencyPingWaves      = (latencyParallel + latencyAgentParallel - 1) / latencyAgentParallel
	latencyCommandTimeout = time.Duration(latencyPingWaves*latencyPingCount*(latencyPingTimeoutMS+200))*time.Millisecond + 5*time.Second
)

// latencyTarget is one reference point, e.g. a province's carrier CDN node.
// Pinned points are the ones cards show first.
type latencyTarget struct {
	Name    string `json:"name"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
	Enabled bool   `json:"enabled"`
	Pinned  bool   `json:"pinned,omitempty"`
}

// latencyPoint is a node's latency to one reference point.
type latencyPoint struct {
	Name      string  `json:"name"`
	Pinned    bool    `json:"pinned,omitempty"`
	OK        bool    `json:"ok"`
	LatencyMs int64   `json:"latencyMs,omitempty"`
	LossPct   float64 `json:"lossPct,omitempty"`
	Error     string  `json:"error,omitempty"`
	// Route names the return route to this point (node_route.go).
	Route string `json:"route,omitempty"`
	// RouteV6 is a dual-stack node's IPv6 return route.
	RouteV6 string `json:"routeV6,omitempty"`
}

// parseLatencyTargets validates and normalizes the configured reference
// points; an empty value is no points.
func parseLatencyTargets(raw string) ([]latencyTarget, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var targets []latencyTarget
	if err := json.Unmarshal([]byte(raw), &targets); err != nil {
		return nil, errors.New("测试点格式无效")
	}
	if len(targets) > maxLatencyTargets {
		return nil, fmt.Errorf("测试点最多 %d 个", maxLatencyTargets)
	}
	names := make(map[string]bool, len(targets))
	pinned := 0
	for i := range targets {
		t := &targets[i]
		t.Name = strings.TrimSpace(t.Name)
		if t.Name == "" || len([]rune(t.Name)) > maxLatencyTargetName {
			return nil, fmt.Errorf("测试点名称不能为空且不超过 %d 字", maxLatencyTargetName)
		}
		if names[t.Name] {
			return nil, fmt.Errorf("测试点名称重复：%s", t.Name)
		}
		names[t.Name] = true
		target, _, err := normalizeTunnelProbeTarget(t.Host, t.Port)
		if err != nil || strings.TrimSpace(t.Host) == "" {
			return nil, fmt.Errorf("测试点 %s：地址无效", t.Name)
		}
		t.Host, t.Port = target.Host, target.Port
		if t.Pinned {
			pinned++
		}
	}
	if pinned > maxPinnedLatencyTargets {
		return nil, fmt.Errorf("卡片最多固定显示 %d 个测试点", maxPinnedLatencyTargets)
	}
	return targets, nil
}

func normalizeLatencyTargetsConfig(raw string) (string, error) {
	targets, err := parseLatencyTargets(raw)
	if err != nil {
		return "", err
	}
	if len(targets) == 0 {
		return "", nil
	}
	b, err := json.Marshal(targets)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// latencyPool returns every configured reference point; enabled ones are
// the defaults.
func (h *Handler) latencyPool() []latencyTarget {
	raw, err := h.repo.GetViteConfigValue(latencyTargetsConfigKey)
	if err != nil {
		return nil
	}
	targets, err := parseLatencyTargets(raw)
	if err != nil {
		return nil
	}
	return targets
}

// nodeTargetChoice is a node's own pick of reference points, stored on the
// node as JSON. No points means the node measures nothing.
type nodeTargetChoice struct {
	Points []string `json:"points"`
	Pinned []string `json:"pinned"`
}

func parseNodeTargetChoice(raw string) *nodeTargetChoice {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var c nodeTargetChoice
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return nil
	}
	if c.Points == nil {
		c.Points = []string{}
	}
	if c.Pinned == nil {
		c.Pinned = []string{}
	}
	return &c
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// targetsForNode returns the points a node measures, in pool order: its own
// choice (with its own pinned points), or the defaults. An empty choice
// measures nothing; one whose points were all removed from the pool falls
// back to the defaults.
func targetsForNode(pool []latencyTarget, rawChoice string) []latencyTarget {
	if c := parseNodeTargetChoice(rawChoice); c != nil {
		if len(c.Points) == 0 {
			return nil
		}
		var own []latencyTarget
		for _, t := range pool {
			if containsString(c.Points, t.Name) {
				t.Pinned = containsString(c.Pinned, t.Name)
				own = append(own, t)
			}
		}
		if len(own) > 0 {
			return own
		}
	}
	var defaults []latencyTarget
	for _, t := range pool {
		if t.Enabled {
			defaults = append(defaults, t)
		}
	}
	return defaults
}

// nodeLatencyStore keeps each node's last measurement.
type nodeLatencyStore struct {
	mu      sync.RWMutex
	results map[int64]externalProbe
	rounds  probeRounds
}

func newNodeLatencyStore() *nodeLatencyStore {
	return &nodeLatencyStore{results: make(map[int64]externalProbe)}
}

func (s *nodeLatencyStore) get(nodeID int64) (externalProbe, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.results[nodeID]
	return p, ok
}

// replace stores fresh results; a full round also drops nodes it skipped
// (offline, remote, or no reference points configured).
func (s *nodeLatencyStore) replace(results map[int64]externalProbe, all bool) {
	s.replaceRound(results, all, time.Now().UnixNano())
}

// replaceRound is replace gated per entry by probeRounds: stale rounds
// cannot overwrite fresher values（DB-4，移植自 kure29/flyx 3.3.33）。
func (s *nodeLatencyStore) replaceRound(results map[int64]externalProbe, all bool, started int64) {
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
	for id, p := range results {
		if s.rounds.take(id, started) {
			s.results[id] = p
		}
	}
}

// drop forgets nodes that no longer measure anything.
func (s *nodeLatencyStore) drop(ids []int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		delete(s.results, id)
		delete(s.rounds, id)
	}
}

// nodeLatencyPing runs one TcpPing from a node; tests replace it.
var nodeLatencyPing = func(h *Handler, nodeID int64, host string, port int) (map[string]interface{}, error) {
	return h.tcpPingViaNode(nodeID, host, port, diagnosisExecOptions{
		commandTimeout: latencyCommandTimeout,
		pingTimeoutMS:  latencyPingTimeoutMS,
		pingCount:      latencyPingCount,
	})
}

// latencyCommandError turns a failed TcpPing command into the reason the
// operator should see: queueing, a busy or offline node, or a timeout
// （DB-5 失败原因分类，移植自 kure29/flyx 3.3.33）。
func latencyCommandError(err error) string {
	if errors.Is(err, errTCPPingQueueTimeout) {
		return errTCPPingQueueTimeout.Error()
	}
	msg := strings.TrimSpace(err.Error())
	switch {
	case strings.Contains(msg, agentBusyMessage), strings.Contains(msg, "并发过多"):
		return "节点繁忙，请稍后重试"
	case strings.Contains(msg, "队列已满"), strings.Contains(msg, "TCP探测"):
		return msg
	case strings.Contains(msg, "节点不在线"), strings.Contains(msg, "节点连接已断开"):
		// 同一事实（节点掉线）只应有一种显示原因：发起时离线是
		// 「节点不在线」，在途命令被断线终局是 failPendingForNode 的
		// 「节点连接已断开」（ws/server.go）——两者同归「节点离线」
		// （R3-B-01，此前在途断线落默认分支显示原始错误串）。
		return "节点离线"
	case errors.Is(err, context.DeadlineExceeded),
		strings.Contains(msg, "timeout"), strings.Contains(msg, "等待节点响应超时"):
		return "节点响应超时"
	case errors.Is(err, context.Canceled), strings.Contains(msg, "Agent is shutting down"):
		return "探测已取消"
	case msg == "":
		return "探测失败"
	default:
		return "探测失败：" + msg
	}
}

func latencyPointFrom(t latencyTarget, data map[string]interface{}, err error) latencyPoint {
	p := latencyPoint{Name: t.Name, Pinned: t.Pinned}
	if err != nil {
		p.Error = latencyCommandError(err)
		return p
	}
	if !asBool(data["success"], false) {
		msg := asString(data["errorMessage"])
		switch {
		case msg == "" || msg == "所有TCP连接尝试都失败":
			p.Error = "测试点连接失败"
		case strings.Contains(msg, "DNS"):
			p.Error = "域名解析失败"
		default:
			p.Error = msg
		}
		return p
	}
	p.OK = true
	p.LatencyMs = int64(asFloat(data["averageTime"], 0) + 0.5)
	if p.LatencyMs < 1 {
		p.LatencyMs = 1
	}
	p.LossPct = float64(int(asFloat(data["packetLoss"], 0) + 0.5))
	return p
}

// probeRoundKind states what a probe round is for. The recording
// semantics — whether the round replaces the whole cache and whether
// it drives the alert streaks — are declared by the caller through
// this kind, never inferred from the ids slice: the len(ids)==0
// inference let manual actions pose as scheduled rounds (a latency
// config save's refresh round counted alert streaks, R2-α-01; an
// empty expansion racing a create could still reach full-round
// counting, R2-α-02). Both probers (probeNodeLatency and
// probeExternalInbounds) share the kind.
type probeRoundKind int

const (
	// probeRoundTargeted refreshes the cache for the given ids only:
	// no whole-cache replacement, no alert streaks. Manual probes
	// (clicks, target saves, relay pre-measures, reconnect retests)
	// use this kind.
	probeRoundTargeted probeRoundKind = iota
	// probeRoundFull re-measures everything and replaces the whole
	// cache, but is still an operator action: no alert streaks. The
	// latency config save's refresh round uses this kind.
	probeRoundFull
	// probeRoundScheduled is the periodic loop's round: whole-cache
	// replacement plus alert streaks. Only runExternalProbeLoop may
	// pass this kind.
	probeRoundScheduled
)

// countsStreak reports whether the round drives the alert streaks.
func (k probeRoundKind) countsStreak() bool { return k == probeRoundScheduled }

// fullReplace reports whether the round replaces the whole cache
// (entries it did not produce are pruned) instead of merging.
func (k probeRoundKind) fullReplace() bool { return k != probeRoundTargeted }

// probeNodeLatency measures the given local nodes' latency (all online ones
// when ids is empty) to every enabled reference point and records it.
func (h *Handler) probeNodeLatency(ctx context.Context, ids []int64, round probeRoundKind) (map[int64]externalProbe, error) {
	started := time.Now().UnixNano()
	nodes, err := h.repo.ListMonitorNodes()
	if err != nil {
		return nil, err
	}
	pool := h.latencyPool()
	want := make(map[int64]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	targets := make(map[int64][]latencyTarget)
	points := make(map[int64][]latencyPoint)
	for _, n := range nodes {
		if (len(ids) > 0 && !want[n.ID]) || n.Status != 1 {
			continue
		}
		if t := targetsForNode(pool, n.LatencyTargets); len(t) > 0 {
			targets[n.ID] = t
			points[n.ID] = make([]latencyPoint, len(t))
		}
	}

	// DA-6：派发前先收集再按节点交错，避免单节点先占满全局并发与
	// 自身探测槽位、其他节点饿死（interleaveByNode）。
	type latencyJob struct {
		nodeID int64
		i      int
		t      latencyTarget
	}
	jobs := make([]latencyJob, 0, len(targets))
	for nodeID, list := range targets {
		for i, t := range list {
			jobs = append(jobs, latencyJob{nodeID, i, t})
		}
	}
	jobs = interleaveByNode(jobs, func(j latencyJob) int64 { return j.nodeID })

	var wg sync.WaitGroup
	sem := make(chan struct{}, latencyParallel)
	for _, j := range jobs {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(j latencyJob) {
			defer wg.Done()
			defer func() { <-sem }()
			data, err := nodeLatencyPing(h, j.nodeID, j.t.Host, j.t.Port)
			// Each goroutine writes its own slot.
			points[j.nodeID][j.i] = latencyPointFrom(j.t, data, err)
		}(j)
	}
	// L-002：ctx 取消后不等在途探测（单个 TcpPing 最长约 14s），直接返回 ctx 错误；
	// 在途 goroutine 各自写独立 slot，调用方不再读取 points，无数据竞争
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	now := time.Now().UnixMilli()
	results := make(map[int64]externalProbe, len(points))
	for nodeID, list := range points {
		results[nodeID] = latencyProbe(list, now)
	}
	if ctx.Err() == nil {
		h.nodeLatency.replaceRound(results, round.fullReplace(), started)
		h.recordLatencySamples(results)
		// Streaks count scheduled rounds only; the round kind — never
		// the ids slice — decides (R2-α-01/02).
		if round.countsStreak() {
			names := make(map[int64]string, len(nodes))
			for _, n := range nodes {
				names[n.ID] = n.Name
			}
			h.alertLatency(results, names)
		}
	}
	return results, nil
}

// latencyProbe summarizes a node's points: OK when any point answered,
// with the worst answering point's latency.
// O1-006/L-007：此处刻意取「最差（最大）延迟」而非平均值——节点卡片显示的延迟代表
// 到最远测试点的上界，语义为保守估计；勿改为平均值（会掩盖个别测试点的高延迟）。
func latencyProbe(points []latencyPoint, checkedAt int64) externalProbe {
	p := externalProbe{CheckedAt: checkedAt, Points: points}
	for _, pt := range points {
		if pt.OK {
			p.OK = true
			if pt.LatencyMs > p.LatencyMs {
				p.LatencyMs = pt.LatencyMs
			}
		}
	}
	if !p.OK {
		p.Error = "测试点都连不上"
	}
	return p
}

// latencyPoolBefore returns the reference points as they are before a
// config save, so renames can follow; nil for other keys.
func (h *Handler) latencyPoolBefore(key string) []latencyTarget {
	if strings.TrimSpace(key) != latencyTargetsConfigKey {
		return nil
	}
	return h.latencyPool()
}

// onLatencyConfigChanged carries renamed points over to the nodes that
// picked them, then re-measures every node so cards do not keep showing
// removed points until the next round.
func (h *Handler) onLatencyConfigChanged(key string, prev []latencyTarget) {
	if strings.TrimSpace(key) != latencyTargetsConfigKey {
		return
	}
	h.syncNodeTargetChoices(prev, h.latencyPool())
	h.jobsMu.Lock()
	ctx := h.jobsCtx
	h.jobsMu.Unlock()
	if ctx == nil {
		return
	}
	go func() {
		// A full refresh (removed points must disappear from cards),
		// but a config save is an operator action: it must not count
		// toward latency alert streaks (R2-α-01).
		if _, err := h.probeNodeLatency(ctx, nil, probeRoundFull); err != nil && ctx.Err() == nil {
			log.Printf("node latency probe failed: %v", err)
		}
		if _, err := h.probeRelayLatency(ctx, nil, true); err != nil && ctx.Err() == nil {
			log.Printf("relay latency probe failed: %v", err)
		}
		if _, err := h.traceNodeRoutes(ctx, nil, false); err != nil && ctx.Err() == nil {
			log.Printf("node route trace failed: %v", err)
		}
	}()
}

// renamedTargets maps old names to new ones for points whose address stayed
// while the name changed.
func renamedTargets(prev, next []latencyTarget) map[string]string {
	oldNames := make(map[string]bool, len(prev))
	for _, t := range prev {
		oldNames[t.Name] = true
	}
	newNames := make(map[string]bool, len(next))
	byAddr := make(map[string]string, len(next))
	for _, t := range next {
		newNames[t.Name] = true
		if !oldNames[t.Name] {
			byAddr[t.Host+":"+strconv.Itoa(t.Port)] = t.Name
		}
	}
	renames := make(map[string]string)
	for _, t := range prev {
		if newNames[t.Name] {
			continue
		}
		if name, ok := byAddr[t.Host+":"+strconv.Itoa(t.Port)]; ok {
			renames[t.Name] = name
		}
	}
	return renames
}

// syncNodeTargetChoices follows renames in the nodes' own choices and drops
// removed points; a choice left empty follows the defaults again (one that
// was empty on purpose stays so).
func (h *Handler) syncNodeTargetChoices(prev, next []latencyTarget) {
	nodes, err := h.repo.ListMonitorNodes()
	if err != nil {
		return
	}
	renames := renamedTargets(prev, next)
	inPool := make(map[string]bool, len(next))
	for _, t := range next {
		inPool[t.Name] = true
	}
	carry := func(names []string) []string {
		var out []string
		for _, n := range names {
			if r, ok := renames[n]; ok {
				n = r
			}
			if inPool[n] {
				out = append(out, n)
			}
		}
		return out
	}
	for _, n := range nodes {
		c := parseNodeTargetChoice(n.LatencyTargets)
		if c == nil || len(c.Points) == 0 {
			continue
		}
		next := nodeTargetChoice{Points: carry(c.Points), Pinned: carry(c.Pinned)}
		value := ""
		if len(next.Points) > 0 {
			b, _ := json.Marshal(next)
			value = string(b)
		}
		if value != n.LatencyTargets {
			if err := h.repo.SetNodesLatencyTargets([]int64{n.ID}, value); err != nil {
				log.Printf("sync node %d latency targets: %v", n.ID, err)
			}
		}
	}
}

// attachNodeLatency enriches /node/list items with the latest latency
// probe result and the node's explicit test-point choice.
//
// The scheduled probe rounds (runExternalProbeLoop) store results in
// h.nodeLatency, but ListNodes never carried them, so the node page
// could only show latency after a manual re-probe — whose button only
// exists once a result is already rendered (deadlock). Attaching here
// closes the loop: every list load carries the latest result.
//
//   - latency: attached only when the store holds a result with at
//     least one point. The shape is externalProbe, identical to the
//     manual /node/latency/probe response (withRoutes-enriched), which
//     is what the frontend InboundProbe consumes.
//   - latencyTargets: attached only when the node has an explicit
//     stored choice (including an explicit empty choice, which the
//     frontend renders as the "不测延迟" pill). No choice means the
//     node follows the global defaults and the key is omitted — the
//     frontend treats a present field as "自定义", so attaching
//     defaults here would mislabel every node.
//   - No online/remote filtering here: renderLatency on the frontend
//     owns that gate, and this handler must not fork its semantics.
//     A targets/read failure degrades to "attach nothing" rather than
//     failing the whole list, and is logged (148-P3-4) so the outage is
//     observable instead of looking like "no node ever chose targets".
func (h *Handler) attachNodeLatency(items []map[string]interface{}) {
	if h == nil || len(items) == 0 {
		return
	}
	targets, err := h.repo.ListNodeLatencyTargets()
	if err != nil {
		// 148-P3-4: a targets/read failure degrades to "attach nothing"
		// rather than failing the whole list — but it must be logged,
		// or every node's latencyTargets silently vanishes with no
		// clue that the DB (not the configuration) is at fault.
		log.Printf("attach node latency: ListNodeLatencyTargets query failed, latencyTargets omitted: %v", err)
	}
	for _, item := range items {
		id := asInt64(item["id"], 0)
		if id == 0 {
			continue
		}
		if h.nodeLatency != nil {
			if p, ok := h.nodeLatency.get(id); ok && len(p.Points) > 0 {
				if h.nodeRoutes != nil {
					p = h.withRoutes(id, p)
				}
				item["latency"] = p
			}
		}
		if raw, ok := targets[id]; ok {
			if c := parseNodeTargetChoice(raw); c != nil {
				item["latencyTargets"] = map[string]interface{}{
					"points": c.Points,
					"pinned": c.Pinned,
				}
			}
		}
	}
}

// nodeLatencyTargetsSet stores the nodes' own reference points, or clears
// them to follow the defaults, then measures those nodes again.
func (h *Handler) nodeLatencyTargetsSet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req struct {
		IDs    []int64  `json:"ids"`
		Custom bool     `json:"custom"`
		Points []string `json:"points"`
		Pinned []string `json:"pinned"`
	}
	if err := decodeJSON(r.Body, &req); err != nil || len(req.IDs) == 0 {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	// J-004：ids 数组长度上限，防止超大批量请求放大 DB/探测负载
	const maxLatencyTargetSetIDs = 200
	if len(req.IDs) > maxLatencyTargetSetIDs {
		response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("一次最多设置 %d 个节点的测试点", maxLatencyTargetSetIDs)))
		return
	}
	// O1-005/L-008：验证节点 ID 存在性，避免对不存在的节点静默写入/探测无响应
	{
		existing, err := h.repo.ListMonitorNodes()
		if err != nil {
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
		exists := make(map[int64]bool, len(existing))
		for _, n := range existing {
			exists[n.ID] = true
		}
		for _, id := range req.IDs {
			if !exists[id] {
				response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("节点不存在：%d", id)))
				return
			}
		}
	}
	value := ""
	measuresNothing := false
	if req.Custom {
		pool := h.latencyPool()
		inPool := make(map[string]bool, len(pool))
		for _, t := range pool {
			inPool[t.Name] = true
		}
		// No points is allowed: these nodes stop measuring.
		choice := nodeTargetChoice{Points: []string{}, Pinned: []string{}}
		for _, n := range req.Points {
			if !inPool[n] {
				response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("测试点不存在：%s", n)))
				return
			}
			if !containsString(choice.Points, n) {
				choice.Points = append(choice.Points, n)
			}
		}
		measuresNothing = len(choice.Points) == 0
		for _, n := range req.Pinned {
			if containsString(choice.Points, n) && !containsString(choice.Pinned, n) {
				choice.Pinned = append(choice.Pinned, n)
			}
		}
		if len(choice.Pinned) > maxPinnedLatencyTargets {
			response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("卡片最多固定显示 %d 个测试点", maxPinnedLatencyTargets)))
			return
		}
		b, err := json.Marshal(choice)
		if err != nil {
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
		value = string(b)
	}
	if err := h.repo.SetNodesLatencyTargets(req.IDs, value); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if measuresNothing {
		h.nodeLatency.drop(req.IDs)
	}
	// Latency takes seconds, so the list the page reloads already shows the
	// new points; traces take longer and follow in the background.
	if _, err := h.probeNodeLatency(r.Context(), req.IDs, probeRoundTargeted); err != nil {
		log.Printf("node latency probe failed: %v", err)
	}
	h.jobsMu.Lock()
	ctx := h.jobsCtx
	h.jobsMu.Unlock()
	if ctx != nil {
		ids := append([]int64(nil), req.IDs...)
		go func() {
			if _, err := h.traceNodeRoutes(ctx, ids, false); err != nil && ctx.Err() == nil {
				log.Printf("node route trace failed: %v", err)
			}
		}()
	}
	response.WriteJSON(w, response.OKEmpty())
}

// nodeLatencyProbe measures the given nodes now (all of them when empty).
func (h *Handler) nodeLatencyProbe(w http.ResponseWriter, r *http.Request) {
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
	if len(h.latencyPool()) == 0 {
		response.WriteJSON(w, response.ErrDefault("还没有设置延迟测试点，请到网站配置添加"))
		return
	}
	ids := req.IDs
	truncated := false
	if len(ids) == 0 {
		// O1-004/J-003：空 IDs 收敛为前 50 个在线节点，避免全量出站风暴；
		// 定向轮（probeRoundTargeted）也使缓存按合并而非整体替换更新
		nodes, err := h.repo.ListMonitorNodes()
		if err != nil {
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
		for _, n := range nodes {
			if n.Status == 1 {
				ids = append(ids, n.ID)
				if len(ids) >= maxProbeAllNodes {
					truncated = true
					break
				}
			}
		}
	}
	results, err := h.probeNodeLatency(r.Context(), ids, probeRoundTargeted)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	out := make(map[string]externalProbe, len(results))
	for id, p := range results {
		out[strconv.FormatInt(id, 10)] = h.withRoutes(id, p)
	}
	if truncated {
		response.WriteJSON(w, response.R{
			Code: 0,
			Msg:  fmt.Sprintf("节点较多，本次仅探测前 %d 个在线节点，剩余节点将由后台任务轮询", maxProbeAllNodes),
			TS:   time.Now().UnixMilli(),
			Data: out,
		})
		return
	}
	response.WriteJSON(w, response.OK(out))
}
