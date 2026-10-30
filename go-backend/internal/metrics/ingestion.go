package metrics

import (
	"context"
	"log"
	"sync"
	"time"

	"go-backend/internal/monitoring"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

type SystemInfo struct {
	Uptime           uint64 `json:"uptime"`
	BytesReceived    uint64 `json:"bytes_received"`
	BytesTransmitted uint64 `json:"bytes_transmitted"`
	// BootID identifies the node's current OS boot (the agent's
	// machineBootID); it keys the NIC counter regression semantics
	// in RecordNodeMetric. Empty from old agents.
	BootID      string  `json:"boot_id"`
	CPUUsage    float64 `json:"cpu_usage"`
	MemoryUsage float64 `json:"memory_usage"`
	DiskUsage   float64 `json:"disk_usage"`
	Load1       float64 `json:"load1"`
	Load5       float64 `json:"load5"`
	Load15      float64 `json:"load15"`
	TCPConns    int64   `json:"tcp_conns"`
	UDPConns    int64   `json:"udp_conns"`
	NetInSpeed  int64   `json:"net_in_speed"`
	NetOutSpeed int64   `json:"net_out_speed"`
}

// NodeTrafficRetention keeps a little over a year of hourly node NIC
// traffic buckets. Fixed on purpose: it is independent of the
// configurable monitoring retention, which can be far shorter.
const NodeTrafficRetention = 400 * 24 * time.Hour

type trafficKey struct {
	node, hour int64
}

type IngestionService struct {
	repo          *repo.Repository
	nodeBuffer    []*model.NodeMetric
	nodeBufferMu  sync.Mutex
	flushInterval time.Duration

	// lastBytes is each node's last NIC byte counters (in, out);
	// node_traffic accumulates what they grew by, per hour, until the
	// next flush. These are whole-machine NIC counters — not tunnel
	// billing counters. lastBoot is the boot id that accompanied
	// lastBytes; it decides what a counter regression means.
	lastBytes map[int64][2]int64
	lastBoot  map[int64]string
	traffic   map[trafficKey][2]int64
}

func NewIngestionService(repo *repo.Repository) *IngestionService {
	return &IngestionService{
		repo:          repo,
		nodeBuffer:    make([]*model.NodeMetric, 0, 500),
		flushInterval: 30 * time.Second,
		lastBytes:     map[int64][2]int64{},
		lastBoot:      map[int64]string{},
		traffic:       map[trafficKey][2]int64{},
	}
}

// counterDeltaForBoot is what a byte counter grew by between two
// reports, keyed by the boot id each report carried. Growth is plain
// subtraction under every boot state.
//
// A regression has two very different causes. A reboot restarts the
// counters: everything in the new counter is new traffic. But the
// agent reports the SUM over all non-loopback interfaces, so the sum
// also falls mid-boot whenever an interface vanishes (a veth or
// tailscale interface going away) — and those bytes were already
// counted, so treating the new total as fresh double-counts the
// machine's whole counter history (real-machine S4: +4.4GB of phantom
// traffic in 13 minutes on a Docker node, the month total passing the
// physical NIC's since-boot cumulative; R3 item 9, P2). Only a PROVEN
// boot change — both reports carry a boot id and they differ — keeps
// the legacy count-in-full semantics. An unproven regression (same
// boot, or either side missing the id, i.e. an old agent) counts 0:
// at most one sampling window is understated, never a full recount.
func counterDeltaForBoot(prev, cur int64, prevBoot, curBoot string) int64 {
	if cur >= prev {
		return cur - prev
	}
	if prevBoot != "" && curBoot != "" && prevBoot != curBoot {
		return cur
	}
	return 0
}

func (s *IngestionService) Start(ctx context.Context) {
	flushTicker := time.NewTicker(s.flushInterval)
	defer flushTicker.Stop()

	pruneTicker := time.NewTicker(1 * time.Hour)
	defer pruneTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.flushNodeMetrics()
			return
		case <-flushTicker.C:
			s.flushNodeMetrics()
		case <-pruneTicker.C:
			s.pruneMetrics()
		}
	}
}

func (s *IngestionService) RecordNodeMetric(nodeID int64, info SystemInfo) {
	m := &model.NodeMetric{
		NodeID:      nodeID,
		Timestamp:   time.Now().UnixMilli(),
		CPUUsage:    info.CPUUsage,
		MemUsage:    info.MemoryUsage,
		DiskUsage:   info.DiskUsage,
		NetInBytes:  int64(info.BytesReceived),
		NetOutBytes: int64(info.BytesTransmitted),
		NetInSpeed:  info.NetInSpeed,
		NetOutSpeed: info.NetOutSpeed,
		Load1:       info.Load1,
		Load5:       info.Load5,
		Load15:      info.Load15,
		TCPConns:    info.TCPConns,
		UDPConns:    info.UDPConns,
		Uptime:      int64(info.Uptime),
	}

	s.nodeBufferMu.Lock()
	s.nodeBuffer = append(s.nodeBuffer, m)
	shouldFlush := len(s.nodeBuffer) >= 200
	// The first report after a panel restart only sets the baseline.
	if prev, ok := s.lastBytes[nodeID]; ok {
		k := trafficKey{nodeID, m.Timestamp - m.Timestamp%int64(time.Hour/time.Millisecond)}
		t := s.traffic[k]
		// SAT2: flush 窗口内的缓冲合并是 AddNodeTraffic 落库前的合并
		// 点；增量虽为计数器差分，伪造的计数器序列（如 boot id 交替 +
		// 近 MaxInt64 读数）可造出多个近上限差分，裸 += 会先在内存回
		// 绕再绕过 SQL 饱和守卫——与 repo 批内合同口径走饱和加。
		t[0] = repo.SaturatingAddInt64(t[0], counterDeltaForBoot(prev[0], m.NetInBytes, s.lastBoot[nodeID], info.BootID))
		t[1] = repo.SaturatingAddInt64(t[1], counterDeltaForBoot(prev[1], m.NetOutBytes, s.lastBoot[nodeID], info.BootID))
		s.traffic[k] = t
	}
	s.lastBytes[nodeID] = [2]int64{m.NetInBytes, m.NetOutBytes}
	s.lastBoot[nodeID] = info.BootID
	s.nodeBufferMu.Unlock()

	if shouldFlush {
		go s.flushNodeMetrics()
	}
}

func (s *IngestionService) flushNodeMetrics() {
	s.nodeBufferMu.Lock()
	if len(s.nodeBuffer) == 0 {
		s.nodeBufferMu.Unlock()
		return
	}
	buffer := s.nodeBuffer
	s.nodeBuffer = make([]*model.NodeMetric, 0, 500)
	traffic := s.traffic
	s.traffic = map[trafficKey][2]int64{}
	s.nodeBufferMu.Unlock()

	if s.repo == nil {
		return
	}
	if err := s.repo.InsertNodeMetricBatch(buffer); err != nil {
		log.Printf("monitoring write failed op=node_metric.flush count=%d err=%v", len(buffer), err)
	}
	rows := make([]model.NodeTraffic, 0, len(traffic))
	for k, v := range traffic {
		if v[0] > 0 || v[1] > 0 {
			rows = append(rows, model.NodeTraffic{NodeID: k.node, Hour: k.hour, InBytes: v[0], OutBytes: v[1]})
		}
	}
	if err := s.repo.AddNodeTraffic(rows); err != nil {
		log.Printf("monitoring write failed op=node_traffic.flush count=%d err=%v", len(rows), err)
	}
}

func (s *IngestionService) pruneMetrics() {
	s.pruneMetricsAt(time.Now())
}

func (s *IngestionService) retentionDaysFromConfig() int {
	if s == nil || s.repo == nil {
		return monitoring.DefaultMonitorRetentionDays
	}
	cfg, err := s.repo.GetConfigsByNames([]string{monitoring.ConfigMonitorRetentionDays})
	if err != nil {
		return monitoring.DefaultMonitorRetentionDays
	}
	return monitoring.MonitoringRetentionDaysFromConfigMap(cfg)
}

func (s *IngestionService) pruneMetricsAt(now time.Time) {
	cutoff := now.Add(-time.Duration(s.retentionDaysFromConfig()) * 24 * time.Hour).UnixMilli()
	if s.repo == nil {
		return
	}
	if err := s.repo.PruneNodeMetrics(cutoff); err != nil {
		log.Printf("monitoring prune failed op=node_metric cutoff=%d err=%v", cutoff, err)
	}
	if err := s.repo.PruneTunnelMetrics(cutoff); err != nil {
		log.Printf("monitoring prune failed op=tunnel_metric cutoff=%d err=%v", cutoff, err)
	}
	if err := s.repo.PruneServiceMonitorResults(cutoff); err != nil {
		log.Printf("monitoring prune failed op=service_monitor_result cutoff=%d err=%v", cutoff, err)
	}
	// Latency samples grow by one row per node/point/round (3 min), so
	// their retention is capped at 7 days even when the monitoring
	// retention is configured longer.
	latencyCutoff := now.Add(-7 * 24 * time.Hour).UnixMilli()
	if latencyCutoff < cutoff {
		latencyCutoff = cutoff
	}
	if err := s.repo.PruneNodeLatencySamples(latencyCutoff); err != nil {
		log.Printf("monitoring prune failed op=node_latency_sample cutoff=%d err=%v", latencyCutoff, err)
	}
	if err := s.repo.PruneNodeTraffic(now.Add(-NodeTrafficRetention).UnixMilli()); err != nil {
		log.Printf("monitoring prune failed op=node_traffic err=%v", err)
	}
}

func (s *IngestionService) GetLatestMetric(nodeID int64) (*model.NodeMetric, error) {
	return s.repo.GetLatestNodeMetric(nodeID)
}

func (s *IngestionService) GetMetrics(nodeID int64, startMs, endMs int64) ([]model.NodeMetric, error) {
	return s.repo.GetNodeMetrics(nodeID, startMs, endMs)
}
