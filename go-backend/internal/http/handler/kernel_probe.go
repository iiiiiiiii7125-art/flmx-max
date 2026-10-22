package handler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"go-backend/internal/http/response"
	"go-backend/internal/kernel"
	"go-backend/internal/netguard"
	"go-backend/internal/store/model"
)

var (
	externalProbeInterval = 3 * time.Minute
	externalProbeTimeout  = 4 * time.Second
)

const externalProbeParallel = 16

// externalProbe is the last TCP reachability check of an imported node, made
// from the panel server. UDP-only protocols (Hysteria2, TUIC) are skipped.
type externalProbe struct {
	OK        bool   `json:"ok"`
	Skipped   bool   `json:"skipped,omitempty"`
	LatencyMs int64  `json:"latencyMs,omitempty"`
	Error     string `json:"error,omitempty"`
	CheckedAt int64  `json:"checkedAt"`
	// Points 是节点到各延迟测试点的延迟（node_latency.go 移植自 kure29/flyx）。
	Points []latencyPoint `json:"points,omitempty"`
}

// probeRounds remembers when each stored result's round started so a
// slower older round cannot overwrite a newer round's values（移植自
// kure29/flyx 3.3.33，DB-4 三个探测存储共用）。
type probeRounds map[int64]int64

// take records started for id and reports whether a write from that round
// may still land: false when a newer round already stored a value.
func (r *probeRounds) take(id, started int64) bool {
	if *r == nil {
		*r = make(probeRounds)
	}
	if (*r)[id] > started {
		return false
	}
	(*r)[id] = started
	return true
}

type externalProbeStore struct {
	mu      sync.RWMutex
	results map[int64]externalProbe
	rounds  probeRounds
	// failStreak counts consecutive failed probes; down marks nodes already
	// reported as unreachable, so alerts fire once per outage.
	failStreak map[int64]int
	down       map[int64]bool
}

func newExternalProbeStore() *externalProbeStore {
	return &externalProbeStore{
		results:    make(map[int64]externalProbe),
		failStreak: make(map[int64]int),
		down:       make(map[int64]bool),
	}
}

type probeTransition struct {
	row  model.NodeInbound
	down bool
	err  string
}

// track updates the outage state of enabled nodes and returns the ones that
// just went down (after notifyInboundDownStreak failures) or came back.
func (s *externalProbeStore) track(rows map[int64]model.NodeInbound, results map[int64]externalProbe) []probeTransition {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []probeTransition
	for id, p := range results {
		row, ok := rows[id]
		if !ok || p.Skipped || row.Enabled != 1 {
			delete(s.failStreak, id)
			delete(s.down, id)
			continue
		}
		if p.OK {
			delete(s.failStreak, id)
			if s.down[id] {
				delete(s.down, id)
				out = append(out, probeTransition{row: row})
			}
			continue
		}
		s.failStreak[id]++
		if s.failStreak[id] >= notifyInboundDownStreak && !s.down[id] {
			s.down[id] = true
			out = append(out, probeTransition{row: row, down: true, err: p.Error})
		}
	}
	return out
}

// unmarkDown 回滚某入站的 down 标记（A7-03：告警发送失败时），failStreak
// 不动——下一轮探测仍失败便立即重新触发告警。
func (s *externalProbeStore) unmarkDown(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.down, id)
}

// remarkDown 是恢复向的对偶回滚（R2X-017）：恢复通知发送失败时把 down
// 标记置回，使下一轮探测（节点仍可达）重新产出恢复跃迁并补发。
func (s *externalProbeStore) remarkDown(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.down == nil {
		s.down = make(map[int64]bool)
	}
	s.down[id] = true
}

func (s *externalProbeStore) get(id int64) (externalProbe, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.results[id]
	return p, ok
}

// replace stores fresh results and drops entries of inbounds that no longer
// exist when the whole set was probed.
func (s *externalProbeStore) replace(results map[int64]externalProbe, all bool) {
	s.replaceRound(results, all, time.Now().UnixNano())
}

// replaceRound is replace gated per entry by the round that produced the
// results: entries from a round that started before the stored one are
// dropped, so a slow full round cannot clobber fresher targeted results.
func (s *externalProbeStore) replaceRound(results map[int64]externalProbe, all bool, started int64) {
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
		for id := range s.failStreak {
			if _, ok := results[id]; !ok {
				delete(s.failStreak, id)
			}
		}
		for id := range s.down {
			if _, ok := results[id]; !ok {
				delete(s.down, id)
			}
		}
	}
	for id, p := range results {
		if s.rounds.take(id, started) {
			s.results[id] = p
		}
	}
}

func externalProbeSkipped(row *model.NodeInbound) bool {
	return row.Protocol == kernel.ProtocolHysteria2 || row.Protocol == kernel.ProtocolTUIC
}

// externalProbeLookupIP 是 probe 的解析入口（var 以便测试注入）。
var externalProbeLookupIP = net.LookupIP

func probeExternalInbound(ctx context.Context, row model.NodeInbound) externalProbe {
	now := time.Now()
	if externalProbeSkipped(&row) {
		return externalProbe{Skipped: true, CheckedAt: now.UnixMilli()}
	}
	// R2X-005/R2X-043：拨号前对解析结果过 netguard——任一命中
	// loopback/硬拒不拨（存量坏行同样受检，命中即跳过并标记）。
	// 判定与拨号共用同一次解析结果（拨已校验 IP，无二次解析窗口）。
	var ips []net.IP
	if ip := net.ParseIP(row.Listen); ip != nil {
		ips = []net.IP{ip}
	} else {
		resolved, err := externalProbeLookupIP(row.Listen)
		if err != nil || len(resolved) == 0 {
			return externalProbe{Error: "域名解析失败", CheckedAt: time.Now().UnixMilli()}
		}
		ips = resolved
	}
	for _, ip := range ips {
		if d := netguard.Judge(ip, netguard.PolicyHardDenyOrLoopback, false); !d.Allowed {
			return externalProbe{Error: "目标地址不允许", CheckedAt: time.Now().UnixMilli()}
		}
	}
	dialer := net.Dialer{Timeout: externalProbeTimeout}
	var conn net.Conn
	var err error
	for _, ip := range ips {
		dialIP := ip
		if ip4 := ip.To4(); ip4 != nil {
			dialIP = ip4
		}
		conn, err = dialer.DialContext(ctx, "tcp", net.JoinHostPort(dialIP.String(), strconv.Itoa(row.Port)))
		if err == nil {
			break
		}
	}
	if err != nil {
		return externalProbe{Error: probeErrorText(err), CheckedAt: time.Now().UnixMilli()}
	}
	_ = conn.Close()
	latency := time.Since(now).Milliseconds()
	if latency < 1 {
		latency = 1
	}
	return externalProbe{OK: true, LatencyMs: latency, CheckedAt: time.Now().UnixMilli()}
}

func probeErrorText(err error) string {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "域名解析失败"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "连接超时"
	}
	return "无法连接"
}

// probeExternalInbounds checks the given imported nodes (all of them when
// ids is empty) in parallel and records the results.
func (h *Handler) probeExternalInbounds(ctx context.Context, ids []int64) (map[int64]externalProbe, error) {
	started := time.Now().UnixNano()
	rows, err := h.repo.ListNodeInbounds()
	if err != nil {
		return nil, err
	}
	want := make(map[int64]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	targets := make([]model.NodeInbound, 0)
	for _, row := range rows {
		if !isExternalInbound(&row) || (len(ids) > 0 && !want[row.ID]) {
			continue
		}
		targets = append(targets, row)
	}

	results := make(map[int64]externalProbe, len(targets))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, externalProbeParallel)
	for _, row := range targets {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(row model.NodeInbound) {
			defer wg.Done()
			defer func() { <-sem }()
			p := probeExternalInbound(ctx, row)
			mu.Lock()
			results[row.ID] = p
			mu.Unlock()
		}(row)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	h.externalProbes.replaceRound(results, len(ids) == 0, started)

	// DB-4（移植自 kure29/flyx 3.3.33）：定向探测（手动检测/节点重连
	// 重测，ids 非空）只刷新缓存，不计入 inbound_down 告警 streak；
	// streak 与跃迁通知只由定时全量轮驱动（track 段）。
	if len(ids) > 0 {
		return results, nil
	}

	byID := make(map[int64]model.NodeInbound, len(targets))
	for _, row := range targets {
		byID[row.ID] = row
	}
	for _, t := range h.externalProbes.track(byID, results) {
		addr := net.JoinHostPort(t.row.Listen, strconv.Itoa(t.row.Port))
		if t.down {
			rowID := t.row.ID
			h.notifyTracked(notifyEventInboundDown, "导入节点连不上",
				fmt.Sprintf("节点：%s\n地址：%s\n原因：%s（从面板服务器检测）", t.row.Name, addr, t.err),
				func(outcome notifyOutcome) {
					// A7-03：发送失败回滚 down 标记，下一轮重新告警。
					if outcome == notifyOutcomeFailed {
						h.externalProbes.unmarkDown(rowID)
					}
				})
		} else {
			name := t.row.Name
			rowID := t.row.ID
			h.notifyTracked(notifyEventInboundDown, "导入节点已恢复",
				fmt.Sprintf("节点：%s\n地址：%s", t.row.Name, addr),
				func(outcome notifyOutcome) {
					// A7-02：恢复送达后清掉该节点的问题指纹，重新武装。
					if outcome == notifyOutcomeSent || outcome == notifyOutcomeSuppressed {
						h.notifier.forgetFingerprintPrefix(
							notifyEventInboundDown + "\x00导入节点连不上\x00节点：" + name + "\n")
					}
					// R2X-017：恢复发送失败置回 down 标记（remarkDown
					// 对偶），下一轮探测重新产出恢复跃迁补发；Disabled
					// 维持 V-16 语义不动。
					if outcome == notifyOutcomeFailed {
						h.externalProbes.remarkDown(rowID)
					}
				})
		}
	}
	return results, nil
}

// runExternalProbeLoop runs one reachability round every
// externalProbeInterval: imported inbounds from the panel, then every
// node's latency to its reference points and its return routes. The
// latency round also records history samples and latency alerts (see
// probeNodeLatency); routes self-throttle to 24h per node.
func (h *Handler) runExternalProbeLoop(ctx context.Context) {
	defer h.jobsWG.Done()
	ticker := time.NewTicker(externalProbeInterval)
	defer ticker.Stop()
	for {
		if _, err := h.probeExternalInbounds(ctx, nil); err != nil && ctx.Err() == nil {
			log.Printf("imported node probe failed: %v", err)
		}
		if _, err := h.probeNodeLatency(ctx, nil); err != nil && ctx.Err() == nil {
			log.Printf("node latency probe failed: %v", err)
		}
		if _, err := h.probeRelayLatency(ctx, nil); err != nil && ctx.Err() == nil {
			log.Printf("relay latency probe failed: %v", err)
		}
		if _, err := h.traceNodeRoutes(ctx, nil, false); err != nil && ctx.Err() == nil {
			log.Printf("node route trace failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// inboundProbe checks imported nodes now: the given ids, or all of them.
func (h *Handler) inboundProbe(w http.ResponseWriter, r *http.Request) {
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
	results, err := h.probeExternalInbounds(r.Context(), req.IDs)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	out := make(map[string]externalProbe, len(results))
	for id, p := range results {
		out[strconv.FormatInt(id, 10)] = p
	}
	response.WriteJSON(w, response.OK(out))
}
