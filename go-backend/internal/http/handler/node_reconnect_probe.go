package handler

import (
	"context"
	"log"
	"sort"
	"sync"
	"time"
)

// 移植自 kure29/flyx 3.3.33（DB-3）：节点重连/内核恢复后，把受影响的
// 探测面排进一个静默合并队列统一重测，免得等下一轮定时探测（3 分钟）
// 才刷新。受影响集按我方探测面改写：节点延迟轮 + 该节点上的外部入站
// 可达 + 经该节点的中转线路延迟 + 经该节点的隧道质量。所有重测都走
// 定向（ids 非空）路径——按 DB-4 口径不计告警 streak。

type nodeReconnectProbeQueue struct {
	mu    sync.Mutex
	nodes map[int64]struct{}
	wake  chan struct{}
}

func newNodeReconnectProbeQueue() *nodeReconnectProbeQueue {
	return &nodeReconnectProbeQueue{
		nodes: make(map[int64]struct{}),
		wake:  make(chan struct{}, 1),
	}
}

func (q *nodeReconnectProbeQueue) add(nodeID int64) {
	if q == nil || nodeID <= 0 {
		return
	}
	q.mu.Lock()
	q.nodes[nodeID] = struct{}{}
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *nodeReconnectProbeQueue) take() []int64 {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	ids := make([]int64, 0, len(q.nodes))
	for id := range q.nodes {
		ids = append(ids, id)
	}
	clear(q.nodes)
	q.mu.Unlock()
	for {
		select {
		case <-q.wake:
		default:
			sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
			return ids
		}
	}
}

// ---- kernel restart detection (R1-B-04) ----
//
// DB-3's original trigger is the node coming back online (WS reconnect).
// A kernel that crashes and is restarted by the agent while the node
// stays connected never passes through onNodeOnline — but the agent's
// KernelStatus reports carry each kernel's Restarts count and StartedAt
// stamp (go-gost/x/kernel manager), so the panel's status poll sees the
// restart within a minute. noteKernelStart compares each report with
// the previous one for that kernel; pollNodeKernels queues the same
// reconnect retest when a running kernel's start identity moved.

type kernelStartKey struct {
	nodeID     int64
	kernelType string
}

type kernelStartMark struct {
	restarts  int
	startedAt int64
}

// noteKernelStart records one kernel's start identity from a status
// report and reports whether the kernel (re)started since the previous
// report: the restart counter grew, or the start time moved (the
// counter resets when the agent process itself restarts; the start
// time still moves). The first report for a kernel only establishes
// the baseline — panel starts and agent upgrades must not fire
// spurious retests. A kernel that is not running never fires; its mark
// still advances, so the eventual start is judged against the stopped
// state rather than an older running one.
func (h *Handler) noteKernelStart(nodeID int64, s agentKernelState) bool {
	h.kernelStartsMu.Lock()
	defer h.kernelStartsMu.Unlock()
	if h.kernelStarts == nil {
		h.kernelStarts = make(map[kernelStartKey]kernelStartMark)
	}
	key := kernelStartKey{nodeID: nodeID, kernelType: s.Type}
	prev, seen := h.kernelStarts[key]
	h.kernelStarts[key] = kernelStartMark{restarts: s.Restarts, startedAt: s.StartedAt}
	if !seen || !s.Running {
		return false
	}
	if s.Restarts > prev.restarts {
		return true
	}
	return prev.startedAt != 0 && s.StartedAt != 0 && s.StartedAt != prev.startedAt
}

// scheduleNodeReconnectProbe queues a re-measurement of everything a node
// feeds. Reconnect bursts inside the quiet window coalesce into one round.
func (h *Handler) scheduleNodeReconnectProbe(nodeID int64) {
	h.jobsMu.Lock()
	started := h.jobsStarted
	queue := h.reconnectProbes
	h.jobsMu.Unlock()
	if !started || queue == nil {
		return
	}
	queue.add(nodeID)
}

func (h *Handler) runNodeReconnectProbes(ctx context.Context, queue *nodeReconnectProbeQueue, quiet time.Duration) {
	defer h.jobsWG.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case <-queue.wake:
		}
		// Coalesce a reconnect burst: wait for a quiet window (bounded),
		// then take everything that accumulated.
		deadline := time.Now().Add(6 * quiet)
		timer := time.NewTimer(quiet)
	merge:
		for {
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-queue.wake:
				if time.Now().After(deadline) {
					break merge
				}
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(quiet)
			case <-timer.C:
				break merge
			}
		}
		timer.Stop()
		nodes := queue.take()
		if len(nodes) == 0 {
			continue
		}
		if err := h.probeReconnectedNodes(ctx, nodes); err != nil && ctx.Err() == nil {
			log.Printf("node reconnect probe failed: %v", err)
		}
	}
}

type reconnectProbeSet struct {
	latencyNodes []int64
	externalIDs  []int64
	relayIDs     []int64
	tunnelIDs    []int64
}

// reconnectProbeTargets resolves what a set of reconnected nodes feeds:
// the nodes themselves plus the entry nodes of affected relay lines (their
// cached latency is a leg of those lines), the external inbounds hosted on
// them, the relay lines landing on or chaining through them, and those
// lines' tunnels (for the tunnel quality prober).
func (h *Handler) reconnectProbeTargets(nodes []int64) (reconnectProbeSet, error) {
	want := make(map[int64]bool, len(nodes))
	out := reconnectProbeSet{}
	for _, id := range nodes {
		if id > 0 && !want[id] {
			want[id] = true
			out.latencyNodes = append(out.latencyNodes, id)
		}
	}
	inbounds, err := h.repo.ListNodeInbounds()
	if err != nil {
		return out, err
	}
	landingNode := make(map[int64]int64, len(inbounds))
	for i := range inbounds {
		row := &inbounds[i]
		landingNode[row.ID] = row.NodeID
		if want[row.NodeID] && isExternalInbound(row) {
			out.externalIDs = append(out.externalIDs, row.ID)
		}
	}
	relays, err := h.repo.ListInboundRelays(0)
	if err != nil {
		return out, err
	}
	measure := make(map[int64]bool, len(nodes))
	for _, id := range nodes {
		measure[id] = true
	}
	tunnels := make(map[int64]bool)
	for _, rw := range relays {
		if !rw.ForwardExists || rw.TunnelID <= 0 {
			continue
		}
		affected := want[landingNode[rw.InboundID]]
		chain, chainErr := h.listChainNodesForTunnel(rw.TunnelID)
		var head int64
		if chainErr == nil && len(chain) > 0 {
			entries, _, _ := splitChainNodeGroups(chain)
			if len(entries) > 0 {
				head = entries[0].NodeID
			}
			if !affected {
				for _, n := range chain {
					if want[n.NodeID] {
						affected = true
						break
					}
				}
			}
		}
		if !affected {
			continue
		}
		out.relayIDs = append(out.relayIDs, rw.ID)
		if !tunnels[rw.TunnelID] {
			tunnels[rw.TunnelID] = true
			out.tunnelIDs = append(out.tunnelIDs, rw.TunnelID)
		}
		if head > 0 && !measure[head] {
			measure[head] = true
			out.latencyNodes = append(out.latencyNodes, head)
		}
	}
	for _, ids := range [][]int64{out.latencyNodes, out.externalIDs, out.relayIDs, out.tunnelIDs} {
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	}
	return out, nil
}

func (h *Handler) probeReconnectedNodes(ctx context.Context, nodes []int64) error {
	if len(nodes) == 0 {
		// An empty ids slice means "all" to the probers; never call them
		// with nothing.
		return nil
	}
	targets, err := h.reconnectProbeTargets(nodes)
	if err != nil {
		return err
	}
	if _, err := h.probeNodeLatency(ctx, targets.latencyNodes, probeRoundTargeted); err != nil {
		return err
	}
	if len(targets.externalIDs) > 0 {
		if _, err := h.probeExternalInbounds(ctx, targets.externalIDs, probeRoundTargeted); err != nil {
			return err
		}
	}
	if len(targets.relayIDs) > 0 {
		if _, err := h.probeRelayLatency(ctx, targets.relayIDs, false); err != nil {
			return err
		}
	}
	if h.qualityProber != nil && h.qualityProber.isEnabled() {
		for _, tunnelID := range targets.tunnelIDs {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			h.qualityProber.probeTunnel(tunnelID)
		}
	}
	log.Printf("node reconnect probe: nodes=%v latency=%v external=%v relays=%v tunnels=%v",
		nodes, targets.latencyNodes, targets.externalIDs, targets.relayIDs, targets.tunnelIDs)
	return nil
}
