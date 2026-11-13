package repo

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"go-backend/internal/store/model"

	gsqlite "github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// SWEEP-CPU L3-03 回归：6 张指标表 prune 分批删（每批 1000
// 行、各自提交，与 connlog prune 同口径）。2500 行过期（>2
// 批）＋少量未过期行：过期删净、未过期保留；中途只删了一批
// 即中断时，再跑一次 prune 能从剩余行续删干净（可中断恢复）。

func newPruneTestRepo(t *testing.T) *Repository {
	t.Helper()
	db, err := gorm.Open(gsqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.AutoMigrate(
		&model.NodeMetric{},
		&model.NodeTraffic{},
		&model.TunnelMetric{},
		&model.ServiceMonitorResult{},
		&model.NodeLatencySample{},
		&model.TunnelQuality{},
	); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return &Repository{db: db}
}

func countRows(t *testing.T, r *Repository, dst interface{}, where string, args ...interface{}) int64 {
	t.Helper()
	var n int64
	if err := r.db.Model(dst).Where(where, args...).Count(&n).Error; err != nil {
		t.Fatalf("count %T: %v", dst, err)
	}
	return n
}

func TestPruneMetricsBatchedDeletesAllExpired(t *testing.T) {
	r := newPruneTestRepo(t)
	oldTs, freshTs := int64(1_000_000), int64(9_000_000)
	cutoff := int64(5_000_000)
	const expired, fresh = 2500, 5

	seed := func() {
		metrics := make([]model.NodeMetric, 0, expired+fresh)
		for i := 0; i < expired; i++ {
			metrics = append(metrics, model.NodeMetric{NodeID: 1, Timestamp: oldTs + int64(i)})
		}
		for i := 0; i < fresh; i++ {
			metrics = append(metrics, model.NodeMetric{NodeID: 1, Timestamp: freshTs + int64(i)})
		}
		if err := r.db.CreateInBatches(metrics, 500).Error; err != nil {
			t.Fatalf("seed node_metric: %v", err)
		}
		traffic := make([]model.NodeTraffic, 0, expired+fresh)
		for i := 0; i < expired; i++ {
			traffic = append(traffic, model.NodeTraffic{NodeID: 1, Hour: cutoff - int64(expired-i)*3600_000})
		}
		for i := 0; i < fresh; i++ {
			traffic = append(traffic, model.NodeTraffic{NodeID: 1, Hour: cutoff + int64(i+1)*3600_000})
		}
		if err := r.db.CreateInBatches(traffic, 500).Error; err != nil {
			t.Fatalf("seed node_traffic: %v", err)
		}
		tunnel := make([]model.TunnelMetric, 0, expired+fresh)
		for i := 0; i < expired; i++ {
			tunnel = append(tunnel, model.TunnelMetric{TunnelID: 1, NodeID: 1, Timestamp: oldTs + int64(i)})
		}
		for i := 0; i < fresh; i++ {
			tunnel = append(tunnel, model.TunnelMetric{TunnelID: 1, NodeID: 1, Timestamp: freshTs + int64(i)})
		}
		if err := r.db.CreateInBatches(tunnel, 500).Error; err != nil {
			t.Fatalf("seed tunnel_metric: %v", err)
		}
		results := make([]model.ServiceMonitorResult, 0, expired+fresh)
		for i := 0; i < expired; i++ {
			results = append(results, model.ServiceMonitorResult{MonitorID: 1, NodeID: 1, Timestamp: oldTs + int64(i)})
		}
		for i := 0; i < fresh; i++ {
			results = append(results, model.ServiceMonitorResult{MonitorID: 1, NodeID: 1, Timestamp: freshTs + int64(i)})
		}
		if err := r.db.CreateInBatches(results, 500).Error; err != nil {
			t.Fatalf("seed service_monitor_result: %v", err)
		}
		samples := make([]model.NodeLatencySample, 0, expired+fresh)
		for i := 0; i < expired; i++ {
			samples = append(samples, model.NodeLatencySample{NodeID: 1, CreatedTime: oldTs + int64(i), Point: "p"})
		}
		for i := 0; i < fresh; i++ {
			samples = append(samples, model.NodeLatencySample{NodeID: 1, CreatedTime: freshTs + int64(i), Point: "p"})
		}
		if err := r.db.CreateInBatches(samples, 500).Error; err != nil {
			t.Fatalf("seed node_latency_sample: %v", err)
		}
		quality := make([]model.TunnelQuality, 0, expired+fresh)
		for i := 0; i < expired; i++ {
			quality = append(quality, model.TunnelQuality{TunnelID: 1, Timestamp: oldTs + int64(i)})
		}
		for i := 0; i < fresh; i++ {
			quality = append(quality, model.TunnelQuality{TunnelID: 1, Timestamp: freshTs + int64(i)})
		}
		if err := r.db.CreateInBatches(quality, 500).Error; err != nil {
			t.Fatalf("seed tunnel_quality: %v", err)
		}
	}
	seed()

	cases := []struct {
		name  string
		prune func() error
		dst   interface{}
		where string
	}{
		{"node_metric", func() error { return r.PruneNodeMetrics(cutoff) }, &model.NodeMetric{}, "timestamp < ?"},
		{"node_traffic", func() error { return r.PruneNodeTraffic(cutoff) }, &model.NodeTraffic{}, "hour < ?"},
		{"tunnel_metric", func() error { return r.PruneTunnelMetrics(cutoff) }, &model.TunnelMetric{}, "timestamp < ?"},
		{"service_monitor_result", func() error { return r.PruneServiceMonitorResults(cutoff) }, &model.ServiceMonitorResult{}, "timestamp < ?"},
		{"node_latency_sample", func() error { return r.PruneNodeLatencySamples(cutoff) }, &model.NodeLatencySample{}, "created_time < ?"},
		{"tunnel_quality", func() error { return r.PruneTunnelQualityResults(cutoff) }, &model.TunnelQuality{}, "timestamp < ?"},
	}
	for _, tc := range cases {
		if err := tc.prune(); err != nil {
			t.Fatalf("%s prune: %v", tc.name, err)
		}
		if n := countRows(t, r, tc.dst, tc.where, cutoff); n != 0 {
			t.Fatalf("%s: %d expired rows remain after prune, want 0 (2500 rows span 3 batches of 1000)", tc.name, n)
		}
		if n := countRows(t, r, tc.dst, "1 = 1"); n != fresh {
			t.Fatalf("%s: %d rows remain total, want %d fresh rows kept", tc.name, n, fresh)
		}
		// 幂等：删净后重跑无事发生。
		if err := tc.prune(); err != nil {
			t.Fatalf("%s prune rerun: %v", tc.name, err)
		}
	}
}

// 中途可中断恢复：模拟 prune 只删完第一批（1000 行）即崩溃——
// 剩余 1500 行过期行仍在，再跑一次 prune 必须删净。
func TestPruneNodeMetricsResumesAfterPartialDelete(t *testing.T) {
	r := newPruneTestRepo(t)
	oldTs, freshTs := int64(1_000_000), int64(9_000_000)
	cutoff := int64(5_000_000)
	rows := make([]model.NodeMetric, 0, 2505)
	for i := 0; i < 2500; i++ {
		rows = append(rows, model.NodeMetric{NodeID: 1, Timestamp: oldTs + int64(i)})
	}
	for i := 0; i < 5; i++ {
		rows = append(rows, model.NodeMetric{NodeID: 1, Timestamp: freshTs + int64(i)})
	}
	if err := r.db.CreateInBatches(rows, 500).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	// 模拟崩溃：只删一批（与分批实现同形的单批 DELETE）。
	res := r.db.Exec("DELETE FROM node_metric WHERE id IN (SELECT id FROM node_metric WHERE timestamp < ? LIMIT ?)", cutoff, 1000)
	if res.Error != nil || res.RowsAffected != 1000 {
		t.Fatalf("simulated first batch = (%d, %v), want (1000, nil)", res.RowsAffected, res.Error)
	}
	if n := countRows(t, r, &model.NodeMetric{}, "timestamp < ?", cutoff); n != 1500 {
		t.Fatalf("after simulated crash %d expired rows remain, want 1500", n)
	}
	if err := r.PruneNodeMetrics(cutoff); err != nil {
		t.Fatalf("resume prune: %v", err)
	}
	if n := countRows(t, r, &model.NodeMetric{}, "timestamp < ?", cutoff); n != 0 {
		t.Fatalf("after resume prune %d expired rows remain, want 0", n)
	}
	if n := countRows(t, r, &model.NodeMetric{}, "1 = 1"); n != 5 {
		t.Fatalf("after resume prune %d rows remain total, want 5 fresh", n)
	}
}

// deleteStmtCounter 是 GORM 日志钩子：只数 DELETE 语句（按表），
// 用来判别 prune 是单条 DELETE 还是分批 DELETE——两种实现删
// 净的结果相同，语句数不同（2500 行过期：分批实现发 3 条
// 1000/1000/500 的 raw Exec；旧单条 GORM Delete 不走 Trace
// 路径，计数为 0）。旧代码上本测试变红。
type deleteStmtCounter struct {
	mu     sync.Mutex
	counts map[string]int
}

func (c *deleteStmtCounter) LogMode(logger.LogLevel) logger.Interface      { return c }
func (c *deleteStmtCounter) Info(context.Context, string, ...interface{})  {}
func (c *deleteStmtCounter) Warn(context.Context, string, ...interface{})  {}
func (c *deleteStmtCounter) Error(context.Context, string, ...interface{}) {}
func (c *deleteStmtCounter) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	up := strings.ToUpper(strings.TrimSpace(sql))
	if !strings.HasPrefix(up, "DELETE FROM ") {
		return
	}
	rest := strings.TrimSpace(up[len("DELETE FROM "):])
	table := strings.ToLower(strings.Trim(strings.Fields(rest)[0], `"'`))
	c.mu.Lock()
	c.counts[table]++
	c.mu.Unlock()
}

func (c *deleteStmtCounter) count(table string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[table]
}

func (c *deleteStmtCounter) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts = map[string]int{}
}

func TestPruneNodeMetricsDeletesInBatches(t *testing.T) {
	counter := &deleteStmtCounter{counts: map[string]int{}}
	db, err := gorm.Open(gsqlite.Open(":memory:"), &gorm.Config{Logger: counter})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.AutoMigrate(&model.NodeMetric{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	r := &Repository{db: db}
	rows := make([]model.NodeMetric, 0, 2500)
	for i := 0; i < 2500; i++ {
		rows = append(rows, model.NodeMetric{NodeID: 1, Timestamp: int64(1_000_000 + i)})
	}
	if err := db.CreateInBatches(rows, 500).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	counter.reset()
	if err := r.PruneNodeMetrics(5_000_000); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n := counter.count("node_metric"); n != 3 {
		t.Fatalf("DELETE statements on node_metric = %d, want 3 batched DELETEs (1000/1000/500); old single-DELETE code issues no batched statement", n)
	}
	if n := countRows(t, r, &model.NodeMetric{}, "timestamp < ?", 5_000_000); n != 0 {
		t.Fatalf("%d expired rows remain, want 0", n)
	}
}
