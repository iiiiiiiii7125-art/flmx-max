package metrics

import (
	"context"
	"log"
	"math"
	"sync"
	"sync/atomic"
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
	// ConntrackCount/ConntrackMax（包 K）：宿主机连接跟踪表用
	// 量。加法字段；0 表示该节点无此面（旧 agent/未加载），落
	// 库为 0 且面板不据此告警。
	ConntrackCount int64 `json:"conntrack_count"`
	ConntrackMax   int64 `json:"conntrack_max"`
	AgentRSSBytes       int64 `json:"agent_rss_bytes"`
	MemTotalBytes       int64 `json:"mem_total_bytes"`
	ActiveSessions      int64 `json:"active_sessions"`
	BudgetRejectedTotal int64 `json:"budget_rejected_total"`
	BudgetEvictedTotal  int64 `json:"budget_evicted_total"`
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
	// lastSeen 是每节点上一条指标上报的面板时刻：入站连接日志的
	// boot 结清以其为「最后已知时刻」（设计 §4.6①）。
	lastSeen map[int64]int64
	traffic  map[trafficKey][2]int64

	// lastFullVacuum gates the package K SQLite full-VACUUM path
	// (legacy auto_vacuum=NONE databases) to at most one run per
	// sqliteFullVacuumMinInterval; see reclaimSQLiteSpace.
	lastFullVacuum time.Time
}

func NewIngestionService(repo *repo.Repository) *IngestionService {
	return &IngestionService{
		repo:          repo,
		nodeBuffer:    make([]*model.NodeMetric, 0, 500),
		flushInterval: 30 * time.Second,
		lastBytes:     map[int64][2]int64{},
		lastBoot:      map[int64]string{},
		lastSeen:      map[int64]int64{},
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

// lastInvalidMetricLogNano 是 C3-05 无效样本日志的限速点
// （全进程至多每分钟一条——伪造 agent 可高频上报，日志不得
// 随之放大）。
var lastInvalidMetricLogNano atomic.Int64

func logInvalidMetricThrottled(nodeID int64, reason string) {
	now := time.Now().UnixNano()
	last := lastInvalidMetricLogNano.Load()
	if now-last < int64(time.Minute) || !lastInvalidMetricLogNano.CompareAndSwap(last, now) {
		return
	}
	log.Printf("monitoring ingest rejected node_id=%d reason=%s", nodeID, reason)
}

// clampPercent 把百分比量钳到 [0,100]；NaN 归 0（agent 上报的
// float 不可信，NaN 落库后在展示/聚合面还会污染同批统计）。
func clampPercent(v float64) float64 {
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func clampNonNegativeF(v float64) float64 {
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	return v
}

func clampNonNegativeI(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

func (s *IngestionService) RecordNodeMetric(nodeID int64, info SystemInfo) {
	// C3-05：agent 上报值先消毒再落库。此前 uint64→int64 在
	// ≥2⁶³ 处直接回绕为负、百分比/速度/连接数原样存储——伪造
	// 仅限本节点，但回绕值会进 NodeMetric 行与 lastBytes 基线，
	// 污染展示与后续差分。字节计数器/uptime 超 int64 的样本整条
	// 拒记（不存回绕值、不推进基线，下一样本自恢复）；其余量做
	// 钳制。
	if info.BytesReceived > math.MaxInt64 || info.BytesTransmitted > math.MaxInt64 || info.Uptime > math.MaxInt64 {
		logInvalidMetricThrottled(nodeID, "counter exceeds int64 range")
		return
	}
	m := &model.NodeMetric{
		NodeID:         nodeID,
		Timestamp:      time.Now().UnixMilli(),
		CPUUsage:       clampPercent(info.CPUUsage),
		MemUsage:       clampPercent(info.MemoryUsage),
		DiskUsage:      clampPercent(info.DiskUsage),
		NetInBytes:     int64(info.BytesReceived),
		NetOutBytes:    int64(info.BytesTransmitted),
		NetInSpeed:     clampNonNegativeI(info.NetInSpeed),
		NetOutSpeed:    clampNonNegativeI(info.NetOutSpeed),
		Load1:          clampNonNegativeF(info.Load1),
		Load5:          clampNonNegativeF(info.Load5),
		Load15:         clampNonNegativeF(info.Load15),
		TCPConns:       clampNonNegativeI(info.TCPConns),
		UDPConns:       clampNonNegativeI(info.UDPConns),
		ConntrackCount: clampNonNegativeI(info.ConntrackCount),
		ConntrackMax:   clampNonNegativeI(info.ConntrackMax),
		AgentRSSBytes:       clampNonNegativeI(info.AgentRSSBytes),
		MemTotalBytes:       clampNonNegativeI(info.MemTotalBytes),
		ActiveSessions:      clampNonNegativeI(info.ActiveSessions),
		BudgetRejectedTotal: clampNonNegativeI(info.BudgetRejectedTotal),
		BudgetEvictedTotal:  clampNonNegativeI(info.BudgetEvictedTotal),
		Uptime:         int64(info.Uptime),
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
	prevBoot := s.lastBoot[nodeID]
	prevSeen := s.lastSeen[nodeID]
	s.lastBytes[nodeID] = [2]int64{m.NetInBytes, m.NetOutBytes}
	s.lastBoot[nodeID] = info.BootID
	s.lastSeen[nodeID] = m.Timestamp
	s.nodeBufferMu.Unlock()

	// 入站连接日志的僵尸结清（设计 §4.6①）：boot_id 被证明变化
	// （两侧都带 id 且不同，与 counterDeltaForBoot 的证明口径一
	// 致）即该节点 agent 已重启，其名下仍 open 的连接行批量置
	// interrupted，end_time 记面板最后收到旧 boot 消息的时刻
	// （prevSeen，最后已知时刻，不冒充真实断开）。未证明的变化
	// （任一侧缺 id）不动行，靠 24h 清道夫兜底。
	if prevBoot != "" && info.BootID != "" && prevBoot != info.BootID && s.repo != nil {
		if n, err := s.repo.InterruptOpenInboundConnLogs(nodeID, prevSeen); err != nil {
			log.Printf("connlog boot interrupt failed node_id=%d err=%v", nodeID, err)
		} else if n > 0 {
			log.Printf("connlog boot interrupt node_id=%d interrupted=%d last_known=%d", nodeID, n, prevSeen)
		}
	}

	if shouldFlush {
		go s.flushNodeMetrics()
	}
}

// maxRequeuedMetrics 是 flush 失败回填的缓冲上限：DB 长时间不可
// 用时不得无限积压内存，超出部分丢最旧并计数点名（C3-06）。
const maxRequeuedMetrics = 5000

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
	// C3-06：先写成功才算消费。此前 swap 后写失败仅记日志，
	// buffer 与 traffic 一并丢弃——一次瞬时 DB 失败（如隧道写
	// 入突发期的 SQLite busy）即永久丢失该窗口的节点指标与 NIC
	// 流量增量（lastBytes 基线已推进，丢失的差分永不重算）。现
	// 写失败把未落库部分回填缓冲（指标回填保序并设上限，流量
	// 按键饱和合并），下一轮 flush 重报。
	if err := s.repo.InsertNodeMetricBatch(buffer); err != nil {
		log.Printf("monitoring write failed op=node_metric.flush count=%d err=%v (requeued)", len(buffer), err)
		s.nodeBufferMu.Lock()
		merged := append(buffer, s.nodeBuffer...)
		if len(merged) > maxRequeuedMetrics {
			dropped := len(merged) - maxRequeuedMetrics
			merged = merged[dropped:]
			log.Printf("monitoring requeue overflow op=node_metric.flush dropped_oldest=%d cap=%d", dropped, maxRequeuedMetrics)
		}
		s.nodeBuffer = merged
		s.nodeBufferMu.Unlock()
	}
	rows := make([]model.NodeTraffic, 0, len(traffic))
	for k, v := range traffic {
		if v[0] > 0 || v[1] > 0 {
			rows = append(rows, model.NodeTraffic{NodeID: k.node, Hour: k.hour, InBytes: v[0], OutBytes: v[1]})
		}
	}
	if err := s.repo.AddNodeTraffic(rows); err != nil {
		log.Printf("monitoring write failed op=node_traffic.flush count=%d err=%v (requeued)", len(rows), err)
		s.nodeBufferMu.Lock()
		for k, v := range traffic {
			cur := s.traffic[k]
			cur[0] = repo.SaturatingAddInt64(cur[0], v[0])
			cur[1] = repo.SaturatingAddInt64(cur[1], v[1])
			s.traffic[k] = cur
		}
		s.nodeBufferMu.Unlock()
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

func (s *IngestionService) connlogRetentionDaysFromConfig() int {
	if s == nil || s.repo == nil {
		return monitoring.DefaultConnlogRetentionDays
	}
	cfg, err := s.repo.GetConfigsByNames([]string{monitoring.ConfigConnlogRetentionDays})
	if err != nil {
		return monitoring.DefaultConnlogRetentionDays
	}
	return monitoring.ConnlogRetentionDaysFromConfigMap(cfg)
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
	// 入站连接日志（设计 §5.2/§4.6③）：独立保留期（不随监控保留
	// 放大）＋每入站行数帽双限，另加 24h 僵尸 open 行清道夫。
	connlogCutoff := now.Add(-time.Duration(s.connlogRetentionDaysFromConfig()) * 24 * time.Hour).UnixMilli()
	if _, err := s.repo.PruneInboundConnLogsBefore(connlogCutoff); err != nil {
		log.Printf("connlog prune failed op=inbound_conn_log.time err=%v", err)
	}
	if _, err := s.repo.PruneInboundConnLogsOverCap(repo.ConnlogPerInboundRowCap); err != nil {
		log.Printf("connlog prune failed op=inbound_conn_log.cap err=%v", err)
	}
	if _, err := s.repo.InterruptStaleOpenInboundConnLogs(now.Add(-24 * time.Hour).UnixMilli()); err != nil {
		log.Printf("connlog prune failed op=inbound_conn_log.stale_open err=%v", err)
	}
	s.reclaimSQLiteSpace(now)
}

// SQLite file reclaim gates (包 K). Prunes bound row counts, but
// SQLite keeps deleted pages on its freelist: the gost.db file
// only ever grows to its high-water mark unless the space is
// vacuumed back. On the small disks this panel targets, that
// turns bounded tables into an unbounded file.
const (
	// sqliteFullVacuumMinReclaim: a full VACUUM rewrites the
	// whole file over the single connection, so legacy
	// (auto_vacuum=NONE) databases only pay it when at least
	// this much space comes back.
	sqliteFullVacuumMinReclaim = 128 << 20
	// sqliteFullVacuumMinInterval: and at most once a day, no
	// matter how much churn the prunes produce.
	sqliteFullVacuumMinInterval = 24 * time.Hour
)

// reclaimSQLiteSpace returns prune-freed pages to the OS after
// each hourly prune cycle. INCREMENTAL databases (everything
// created since this change) take the cheap incremental path;
// FULL needs nothing (space returns at each commit); legacy NONE
// databases take the gated full VACUUM, which also migrates the
// file to the DSN-requested INCREMENTAL mode. Every action is
// logged — reclaiming space is an identifiable event, never a
// silent one. Postgres is skipped (its own autovacuum owns this).
func (s *IngestionService) reclaimSQLiteSpace(now time.Time) {
	if s == nil || s.repo == nil {
		return
	}
	stats, err := s.repo.SQLiteSpaceStats()
	if err != nil {
		log.Printf("monitoring sqlite reclaim check failed err=%v", err)
		return
	}
	if !stats.IsSQLite || stats.ReclaimableBytes() <= 0 {
		return
	}
	switch stats.AutoVacuum {
	case 2: // INCREMENTAL
		if err := s.repo.IncrementalVacuumSQLite(stats.FreelistCount); err != nil {
			log.Printf("monitoring sqlite reclaim failed mode=incremental_vacuum err=%v", err)
			return
		}
		after, err := s.repo.SQLiteSpaceStats()
		if err != nil {
			log.Printf("monitoring sqlite reclaim mode=incremental_vacuum reclaimed_bytes<=%d (post-check failed err=%v)", stats.ReclaimableBytes(), err)
			return
		}
		log.Printf("monitoring sqlite reclaim mode=incremental_vacuum reclaimed_bytes=%d freelist_pages_remaining=%d",
			stats.ReclaimableBytes()-after.ReclaimableBytes(), after.FreelistCount)
	case 0: // NONE (legacy database)
		if stats.ReclaimableBytes() < sqliteFullVacuumMinReclaim {
			return
		}
		if !s.lastFullVacuum.IsZero() && now.Sub(s.lastFullVacuum) < sqliteFullVacuumMinInterval {
			return
		}
		if err := s.repo.VacuumSQLite(); err != nil {
			log.Printf("monitoring sqlite reclaim failed mode=vacuum err=%v", err)
			return
		}
		s.lastFullVacuum = now
		log.Printf("monitoring sqlite reclaim mode=vacuum reclaimed_bytes>=%d freelist_pages=%d", stats.ReclaimableBytes(), stats.FreelistCount)
	}
}

func (s *IngestionService) GetLatestMetric(nodeID int64) (*model.NodeMetric, error) {
	return s.repo.GetLatestNodeMetric(nodeID)
}

func (s *IngestionService) GetMetrics(nodeID int64, startMs, endMs int64) ([]model.NodeMetric, error) {
	return s.repo.GetNodeMetrics(nodeID, startMs, endMs)
}
