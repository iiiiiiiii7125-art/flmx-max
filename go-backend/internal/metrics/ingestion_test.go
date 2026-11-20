package metrics

import (
	"context"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

func TestRecordNodeMetric(t *testing.T) {
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	svc := NewIngestionService(r)

	info := SystemInfo{
		Uptime:           86400,
		BytesReceived:    1024000,
		BytesTransmitted: 2048000,
		CPUUsage:         45.5,
		MemoryUsage:      60.2,
		DiskUsage:        30.1,
		Load1:            1.5,
		Load5:            1.2,
		Load15:           0.9,
		TCPConns:         100,
		UDPConns:         50,
		NetInSpeed:       51200,
		NetOutSpeed:      102400,
	}

	svc.RecordNodeMetric(1, info)
	svc.flushNodeMetrics()

	metrics, err := r.GetNodeMetrics(1, time.Now().UnixMilli()-60000, time.Now().UnixMilli()+1000)
	if err != nil {
		t.Fatalf("get metrics: %v", err)
	}
	if len(metrics) != 1 {
		t.Fatalf("expected 1 metric, got %d", len(metrics))
	}

	m := metrics[0]
	if m.CPUUsage != 45.5 {
		t.Fatalf("expected CPUUsage 45.5, got %f", m.CPUUsage)
	}
	if m.MemUsage != 60.2 {
		t.Fatalf("expected MemUsage 60.2, got %f", m.MemUsage)
	}
	if m.DiskUsage != 30.1 {
		t.Fatalf("expected DiskUsage 30.1, got %f", m.DiskUsage)
	}
	if m.Load1 != 1.5 {
		t.Fatalf("expected Load1 1.5, got %f", m.Load1)
	}
	if m.TCPConns != 100 {
		t.Fatalf("expected TCPConns 100, got %d", m.TCPConns)
	}
	if m.UDPConns != 50 {
		t.Fatalf("expected UDPConns 50, got %d", m.UDPConns)
	}
}

func TestRecordNodeMetricAutoFlush(t *testing.T) {
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	svc := NewIngestionService(r)

	info := SystemInfo{
		CPUUsage:    50.0,
		MemoryUsage: 60.0,
		DiskUsage:   30.0,
	}

	// R18-C4-02：逐节点最小接受间隔 1 秒——同一节点连发 250 帧
	// 只会被接受 1 帧。本测试测的是“缓冲满 200 即自动 flush”，
	// 故用 250 个不同节点各发一帧（每节点首帧恒被接受）。
	for i := 0; i < 250; i++ {
		svc.RecordNodeMetric(int64(i+1), info)
	}

	time.Sleep(100 * time.Millisecond)

	total := 0
	for i := 1; i <= 250; i++ {
		metrics, err := r.GetNodeMetrics(int64(i), time.Now().UnixMilli()-60000, time.Now().UnixMilli()+1000)
		if err != nil {
			t.Fatalf("get metrics: %v", err)
		}
		total += len(metrics)
	}
	if total < 200 {
		t.Fatalf("expected at least 200 metrics after auto-flush, got %d", total)
	}
}

func TestIngestionServiceStart(t *testing.T) {
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	svc := NewIngestionService(r)
	svc.flushInterval = 100 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	info := SystemInfo{
		CPUUsage:    45.0,
		MemoryUsage: 55.0,
		DiskUsage:   35.0,
	}

	go svc.Start(ctx)

	for i := 0; i < 10; i++ {
		svc.RecordNodeMetric(1, info)
		time.Sleep(50 * time.Millisecond)
	}

	<-ctx.Done()

	metrics, err := r.GetNodeMetrics(1, time.Now().UnixMilli()-60000, time.Now().UnixMilli()+1000)
	if err != nil {
		t.Fatalf("get metrics: %v", err)
	}
	if len(metrics) == 0 {
		t.Fatalf("expected metrics after service run")
	}
}

func TestGetLatestMetric(t *testing.T) {
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	svc := NewIngestionService(r)
	// R18-C4-02：本测试模拟同节点快速序列（两次上报只隔 5ms），
	// 不测限流本身，故把最小接受间隔置 0；限流由
	// TestRecordNodeMetricPerNodeRateLimit 以默认值覆盖。
	svc.minMetricIntervalMs = 0

	now := time.Now().UnixMilli()

	info1 := SystemInfo{CPUUsage: 40.0, MemoryUsage: 50.0, DiskUsage: 30.0}
	svc.RecordNodeMetric(1, info1)

	time.Sleep(5 * time.Millisecond)

	info2 := SystemInfo{CPUUsage: 60.0, MemoryUsage: 70.0, DiskUsage: 40.0}
	svc.RecordNodeMetric(1, info2)

	svc.flushNodeMetrics()

	latest, err := svc.GetLatestMetric(1)
	if err != nil {
		t.Fatalf("get latest: %v", err)
	}
	if latest == nil {
		t.Fatalf("expected latest metric")
	}
	if latest.CPUUsage != 60.0 {
		t.Fatalf("expected latest CPUUsage 60.0, got %f", latest.CPUUsage)
	}

	_ = now

	latestNone, err := svc.GetLatestMetric(999)
	if err != nil {
		t.Fatalf("get latest for non-existent: %v", err)
	}
	if latestNone != nil {
		t.Fatalf("expected nil for non-existent node")
	}
}

func TestGetMetricsWithTimeRange(t *testing.T) {
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	svc := NewIngestionService(r)
	// R18-C4-02：同上，同节点快速序列不测限流，间隔置 0。
	svc.minMetricIntervalMs = 0

	now := time.Now().UnixMilli()

	for i := 0; i < 5; i++ {
		info := SystemInfo{
			CPUUsage:    float64(40 + i*5),
			MemoryUsage: 50.0,
			DiskUsage:   30.0,
		}
		svc.RecordNodeMetric(1, info)
		time.Sleep(10 * time.Millisecond)
	}

	svc.flushNodeMetrics()

	metrics, err := svc.GetMetrics(1, now-60000, now+1000)
	if err != nil {
		t.Fatalf("get metrics: %v", err)
	}
	if len(metrics) != 5 {
		t.Fatalf("expected 5 metrics, got %d", len(metrics))
	}
}

func TestPruneMetrics(t *testing.T) {
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	svc := NewIngestionService(r)

	info := SystemInfo{CPUUsage: 50.0, MemoryUsage: 60.0, DiskUsage: 30.0}

	svc.RecordNodeMetric(1, info)
	svc.flushNodeMetrics()

	svc.pruneMetrics()

	metrics, err := r.GetNodeMetrics(1, time.Now().UnixMilli()-60000, time.Now().UnixMilli()+1000)
	if err != nil {
		t.Fatalf("get metrics: %v", err)
	}
	if len(metrics) != 1 {
		t.Fatalf("expected 1 metric (not pruned), got %d", len(metrics))
	}
}

func TestPruneMetricsUsesConfiguredRetentionDays(t *testing.T) {
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	now := time.Now().UnixMilli()
	if err := r.UpsertConfig("monitor_retention_days", "2", now); err != nil {
		t.Fatalf("upsert retention config: %v", err)
	}

	oldMetric := &model.NodeMetric{NodeID: 1, Timestamp: now - int64(3*24*time.Hour/time.Millisecond), CPUUsage: 10}
	newMetric := &model.NodeMetric{NodeID: 1, Timestamp: now - int64(1*24*time.Hour/time.Millisecond), CPUUsage: 20}
	if err := r.InsertNodeMetric(oldMetric); err != nil {
		t.Fatalf("insert old metric: %v", err)
	}
	if err := r.InsertNodeMetric(newMetric); err != nil {
		t.Fatalf("insert new metric: %v", err)
	}

	svc := NewIngestionService(r)
	svc.pruneMetricsAt(time.UnixMilli(now))

	metrics, err := r.GetNodeMetrics(1, now-int64(4*24*time.Hour/time.Millisecond), now+1000)
	if err != nil {
		t.Fatalf("get node metrics: %v", err)
	}
	if len(metrics) != 1 || metrics[0].CPUUsage != 20 {
		t.Fatalf("expected only newer metric to remain, got %#v", metrics)
	}
}

func TestMultipleNodes(t *testing.T) {
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	svc := NewIngestionService(r)

	info := SystemInfo{
		CPUUsage:    50.0,
		MemoryUsage: 60.0,
		DiskUsage:   30.0,
	}

	svc.RecordNodeMetric(1, info)
	svc.RecordNodeMetric(2, info)
	svc.RecordNodeMetric(3, info)

	svc.flushNodeMetrics()

	for nodeID := int64(1); nodeID <= 3; nodeID++ {
		metrics, err := r.GetNodeMetrics(nodeID, time.Now().UnixMilli()-60000, time.Now().UnixMilli()+1000)
		if err != nil {
			t.Fatalf("get metrics for node %d: %v", nodeID, err)
		}
		if len(metrics) != 1 {
			t.Fatalf("expected 1 metric for node %d, got %d", nodeID, len(metrics))
		}
	}
}

func TestZeroValues(t *testing.T) {
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	svc := NewIngestionService(r)

	info := SystemInfo{}

	svc.RecordNodeMetric(1, info)
	svc.flushNodeMetrics()

	metrics, err := r.GetNodeMetrics(1, time.Now().UnixMilli()-60000, time.Now().UnixMilli()+1000)
	if err != nil {
		t.Fatalf("get metrics: %v", err)
	}
	if len(metrics) != 1 {
		t.Fatalf("expected 1 metric, got %d", len(metrics))
	}

	m := metrics[0]
	if m.CPUUsage != 0 || m.MemUsage != 0 || m.DiskUsage != 0 {
		t.Fatalf("expected zero values, got CPU=%f Mem=%f Disk=%f", m.CPUUsage, m.MemUsage, m.DiskUsage)
	}
}

// Traffic is what the byte counters grew by; the first report is only
// a baseline. A regression WITHOUT a boot id (an old agent) counts 0
// since the R3/S4 fix: an unproven regression is indistinguishable
// from an interface-set change, and counting it in full double-counts
// the machine's whole counter history (the pre-fix assertion here
// expected the third report to add its full 100/50 — that legacy
// "reboot counts in full" reading is exactly the phantom-traffic bug;
// proven reboots are covered by TestNodeTrafficBootKeyedDeltas).
func TestNodeTrafficFromCounters(t *testing.T) {
	s := NewIngestionService(nil)
	// R18-C4-02：同节点计数器序列模拟，间隔置 0（不测限流）。
	s.minMetricIntervalMs = 0
	s.RecordNodeMetric(7, SystemInfo{BytesReceived: 1000, BytesTransmitted: 500})
	s.RecordNodeMetric(7, SystemInfo{BytesReceived: 1600, BytesTransmitted: 900})
	s.RecordNodeMetric(7, SystemInfo{BytesReceived: 100, BytesTransmitted: 50})
	var in, out int64
	for k, v := range s.traffic {
		if k.node == 7 {
			in += v[0]
			out += v[1]
		}
	}
	if in != 600 || out != 400 {
		t.Fatalf("traffic in=%d out=%d", in, out)
	}
}

// R3/S4 (P2): NIC counter regressions are keyed by the agent's boot
// id. Same boot + regression = the interface set changed mid-boot
// (veth/tailscale churn): count 0, never a full recount — twice in a
// row included. Proven boot change + regression = a real reboot:
// the new counter counts in full, as before. Growth is plain
// subtraction under every boot state.
func TestNodeTrafficBootKeyedDeltas(t *testing.T) {
	s := NewIngestionService(nil)
	// R18-C4-02：同节点计数器序列模拟，间隔置 0（不测限流）。
	s.minMetricIntervalMs = 0
	rec := func(node int64, boot string, in, out uint64) {
		s.RecordNodeMetric(node, SystemInfo{BootID: boot, BytesReceived: in, BytesTransmitted: out})
	}
	// Node 7 stays on boot-a: growth, regression, growth, regression.
	rec(7, "boot-a", 1000, 500) // baseline
	rec(7, "boot-a", 1600, 900) // +600/+400
	rec(7, "boot-a", 100, 50)   // same-boot regression: +0
	rec(7, "boot-a", 150, 80)   // +50/+30
	rec(7, "boot-a", 60, 40)    // second same-boot regression: +0
	// Node 8 reboots between reports: the regression is proven.
	// A-低-5：8 号须用与 7 号不同的 BootID——同 BootID 即同机，
	// 去重后 8 号的增量会被 7 号（更低 ID）吞掉。
	rec(8, "boot-c", 1000, 500) // baseline
	rec(8, "boot-c", 1600, 900) // +600/+400
	rec(8, "boot-d", 100, 50)   // boot changed: counts in full +100/+50
	rec(8, "boot-d", 200, 90)   // +100/+40
	totals := map[int64][2]int64{}
	for k, v := range s.traffic {
		t := totals[k.node]
		t[0] += v[0]
		t[1] += v[1]
		totals[k.node] = t
	}
	if got := totals[7]; got != [2]int64{650, 430} {
		t.Fatalf("same-boot traffic = %v, want [650 430]", got)
	}
	if got := totals[8]; got != [2]int64{800, 490} {
		t.Fatalf("reboot traffic = %v, want [800 490]", got)
	}
}

// A-低-5: two agents on one machine (same BootID) each report the
// machine's whole NIC counters; only the lowest node ID's delta
// counts. A loser still advances its baseline, so when the winner
// goes stale (>5 min without a report) the loser takes over without
// double-counting or losing more than one window. Empty BootID (old
// agent) never dedupes.
func TestNodeTrafficSharedBootIDDedupes(t *testing.T) {
	s := NewIngestionService(nil)
	// R18-C4-02：同节点计数器序列模拟，间隔置 0（不测限流）。
	s.minMetricIntervalMs = 0
	rec := func(node int64, boot string, in, out uint64) {
		s.RecordNodeMetric(node, SystemInfo{BootID: boot, BytesReceived: in, BytesTransmitted: out})
	}
	totals := func() map[int64][2]int64 {
		out := map[int64][2]int64{}
		for k, v := range s.traffic {
			cur := out[k.node]
			cur[0] += v[0]
			cur[1] += v[1]
			out[k.node] = cur
		}
		return out
	}

	rec(1, "boot-x", 1000, 500)  // baseline
	rec(2, "boot-x", 1000, 500)  // baseline (same machine)
	rec(1, "boot-x", 3000, 1500) // winner: +2000/+1000
	rec(2, "boot-x", 3000, 1500) // loser: +0 (same bytes, other agent)
	if got := totals(); got[1] != [2]int64{2000, 1000} || got[2] != [2]int64{0, 0} {
		t.Fatalf("shared-boot traffic = %v, want node1=[2000 1000] node2=[0 0]", got)
	}

	// Winner goes stale: force its lastSeen 10 min back; the loser
	// takes over on its next report.
	s.lastSeen[1] = time.Now().Add(-10 * time.Minute).UnixMilli()
	rec(2, "boot-x", 4000, 2000) // +1000/+500 counted now
	if got := totals(); got[2] != [2]int64{1000, 500} {
		t.Fatalf("after handover node2 traffic = %v, want [1000 500]", got[2])
	}

	// Empty BootID never dedupes: nodes 5 and 6 both count in full.
	rec(5, "", 1000, 500)
	rec(6, "", 1000, 500)
	rec(5, "", 2000, 1000)
	rec(6, "", 2000, 1000)
	if got := totals(); got[5] != [2]int64{1000, 500} || got[6] != [2]int64{1000, 500} {
		t.Fatalf("empty-boot traffic = %v, want node5=[1000 500] node6=[1000 500]", got)
	}
}
