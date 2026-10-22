package handler

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"go-backend/internal/ws"
)

// 移植自 kure29/flyx 3.3.30（DA-6）+ 3.3.33（DB-5）：面板侧每节点 TCP
// 探测并发上限与共享超时预算。探测轮次一轮可向同一节点扇出大量
// TcpPing，不设限会把 agent 与节点连接打满；超限的探测在面板侧排队，
// 并与实际执行共用同一份超时预算——预算先被排队耗尽就不再下发。

const (
	agentTCPPingLimit   = 8
	agentBusyMessage    = "TCP探测任务过多"
	agentBusyRetryDelay = 800 * time.Millisecond
)

var errTCPPingQueueTimeout = errors.New("节点上的 TCP 探测排队超时")

// nodePingSlots bounds concurrent TcpPing commands per node; extra probes
// wait for a slot inside the same timeout budget as the command itself.
type nodePingSlots struct {
	mu    sync.Mutex
	slots map[int64]chan struct{}
}

func newNodePingSlots() *nodePingSlots {
	return &nodePingSlots{slots: make(map[int64]chan struct{})}
}

func (s *nodePingSlots) node(nodeID int64) chan struct{} {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := s.slots[nodeID]
	if ch == nil {
		ch = make(chan struct{}, agentTCPPingLimit)
		s.slots[nodeID] = ch
	}
	return ch
}

func (s *nodePingSlots) acquire(nodeID int64, wait time.Duration) bool {
	ch := s.node(nodeID)
	if ch == nil {
		return true
	}
	if wait <= 0 {
		return false
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case ch <- struct{}{}:
		return true
	case <-timer.C:
		return false
	}
}

func (s *nodePingSlots) release(nodeID int64) {
	ch := s.node(nodeID)
	if ch == nil {
		return
	}
	select {
	case <-ch:
	default:
	}
}

// tcpPingSend is a variable so tests can stub the transport.
var tcpPingSend = func(h *Handler, nodeID int64, data interface{}, timeout time.Duration) (ws.CommandResult, error) {
	return h.sendNodeCommandWithTimeout(nodeID, "TcpPing", data, timeout, false, false)
}

// sendTCPPing sends a TcpPing through the node's probe slot. Queueing and
// execution share the timeout; a busy agent gets one retry inside whatever
// budget remains.
func (h *Handler) sendTCPPing(nodeID int64, data interface{}, timeout time.Duration) (ws.CommandResult, error) {
	if timeout <= 0 {
		timeout = defaultNodeCommandTimeout
	}
	deadline := time.Now().Add(timeout)
	if h.pingSlots != nil && !h.pingSlots.acquire(nodeID, time.Until(deadline)) {
		return ws.CommandResult{}, errTCPPingQueueTimeout
	}
	if h.pingSlots != nil {
		defer h.pingSlots.release(nodeID)
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return ws.CommandResult{}, errTCPPingQueueTimeout
	}
	res, err := tcpPingSend(h, nodeID, data, remaining)
	if err == nil || !strings.Contains(err.Error(), agentBusyMessage) {
		return res, err
	}
	remaining = time.Until(deadline)
	if remaining <= agentBusyRetryDelay {
		return res, err
	}
	time.Sleep(agentBusyRetryDelay)
	remaining = time.Until(deadline)
	if remaining <= 0 {
		return ws.CommandResult{}, errTCPPingQueueTimeout
	}
	return tcpPingSend(h, nodeID, data, remaining)
}

// interleaveByNode spreads jobs across nodes round-robin (stable within a
// node) so a full round does not drain one node's probe slots before the
// others start.
func interleaveByNode[T any](jobs []T, nodeOf func(T) int64) []T {
	if len(jobs) < 2 {
		return jobs
	}
	seen := make(map[int64]int, len(jobs))
	type ranked struct {
		job  T
		rank int
		node int64
	}
	rs := make([]ranked, len(jobs))
	for i, job := range jobs {
		node := nodeOf(job)
		rs[i] = ranked{job: job, rank: seen[node], node: node}
		seen[node]++
	}
	sort.SliceStable(rs, func(i, j int) bool {
		if rs[i].rank != rs[j].rank {
			return rs[i].rank < rs[j].rank
		}
		return rs[i].node < rs[j].node
	})
	out := make([]T, len(jobs))
	for i, r := range rs {
		out[i] = r.job
	}
	return out
}
