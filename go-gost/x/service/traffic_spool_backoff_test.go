package service

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// CPU-S1 回归：流量 spool 退避＋死信＋TTL。
//
// 口径（与 connlog reporter 同源）：失败退避 2s 起翻倍、上限 30s；
// 连续失败达 trafficMaxConsecutiveFailures 次即死信隔离留证并释
// 放 Pending；待确认批次超 trafficPendingTTL 未确认即死信（兜
// 底拒收自愈循环）。死信字节计入 DroppedBytes 随下批上行。

func newBackoffTestManager(t *testing.T, spoolPath string) *GlobalTrafficManager {
	t.Helper()
	spool, err := openTrafficSpool(spoolPath)
	if err != nil {
		t.Fatal(err)
	}
	return &GlobalTrafficManager{
		serviceTraffic: map[string]*ServiceTraffic{
			"svc-a": {ServiceName: "svc-a", UpBytes: 1111, DownBytes: 2222},
		},
		spool: spool,
		ctx:   context.Background(),
	}
}

func TestTrafficReportBackoffIncreasesAndCaps(t *testing.T) {
	origReportDo, origReportURL, origAESCrypto := reportDo, httpReportURL, httpAESCrypto
	origMaxFailures := trafficMaxConsecutiveFailures
	defer func() {
		reportDo, httpReportURL, httpAESCrypto = origReportDo, origReportURL, origAESCrypto
		trafficMaxConsecutiveFailures = origMaxFailures
	}()
	trafficMaxConsecutiveFailures = 100 // 本测试只验退避，不触死信

	captured := stubV2Panel(t, "test-secret", func(env trafficEnvelope) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})
	m := newBackoffTestManager(t, filepath.Join(t.TempDir(), "flmx_traffic_backoff.json"))

	expected := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second}
	for i, want := range expected {
		m.collectAndReport()
		if m.failCount != i+1 {
			t.Fatalf("attempt %d: failCount=%d, want %d", i+1, m.failCount, i+1)
		}
		remain := time.Until(m.retryAt)
		if remain < want-2*time.Second || remain > want+2*time.Second {
			t.Fatalf("attempt %d: retry in %v, want ~%v", i+1, remain, want)
		}
		if len(*captured) != i+1 {
			t.Fatalf("attempt %d: sent %d times, want %d", i+1, len(*captured), i+1)
		}
		// 退避门内再次触发不发送：无退避的旧行为每 5s 必发一次。
		m.collectAndReport()
		if len(*captured) != i+1 {
			t.Fatalf("attempt %d: sent during backoff window, got %d sends", i+1, len(*captured))
		}
		if m.failCount != i+1 {
			t.Fatalf("attempt %d: failCount moved during backoff: %d", i+1, m.failCount)
		}
		m.retryAt = time.Time{} // 测试驱动：放行下一窗口
	}
}

func TestTrafficDeadLetterAfterMaxFailuresReleasesPending(t *testing.T) {
	origReportDo, origReportURL, origAESCrypto := reportDo, httpReportURL, httpAESCrypto
	origMaxFailures := trafficMaxConsecutiveFailures
	defer func() {
		reportDo, httpReportURL, httpAESCrypto = origReportDo, origReportURL, origAESCrypto
		trafficMaxConsecutiveFailures = origMaxFailures
	}()
	trafficMaxConsecutiveFailures = 3

	dir := t.TempDir()
	fail := true
	captured := stubV2Panel(t, "test-secret", func(env trafficEnvelope) (*http.Response, error) {
		if fail {
			return nil, errors.New("connection refused")
		}
		return v2AckResponse(env)
	})
	m := newBackoffTestManager(t, filepath.Join(dir, "flmx_traffic_dead.json"))

	m.collectAndReport() // 失败 1 次：svc-a 批次冻结为 Pending
	if m.spool.state.Pending == nil {
		t.Fatal("expected Pending after first failed report")
	}
	deadSeq := m.spool.state.Pending.Sequence
	// 期间新流量只进 Buffered（头堵的另一半：不污染待确认批次）。
	m.serviceTraffic["svc-b"] = &ServiceTraffic{ServiceName: "svc-b", UpBytes: 333, DownBytes: 444}
	m.retryAt = time.Time{}
	m.collectAndReport() // 失败 2 次
	if len(m.spool.state.Buffered) != 1 || m.spool.state.Buffered[0].N != "svc-b" {
		t.Fatalf("expected svc-b buffered behind Pending, got %+v", m.spool.state.Buffered)
	}
	m.retryAt = time.Time{}
	m.collectAndReport() // 失败 3 次 → 死信
	if m.spool.state.Pending != nil {
		t.Fatal("expected Pending released after max consecutive failures")
	}
	if m.failCount != 0 || !m.retryAt.IsZero() {
		t.Fatalf("expected fail state reset after dead-letter, got failCount=%d retryAt=%v", m.failCount, m.retryAt)
	}
	if m.spool.state.DroppedBytes != 3333 {
		t.Fatalf("DroppedBytes=%d, want 3333 (dead-lettered svc-a bytes)", m.spool.state.DroppedBytes)
	}
	archives, _ := filepath.Glob(filepath.Join(dir, "*.rejected-*.json"))
	if len(archives) != 1 {
		t.Fatalf("expected 1 dead-letter archive, got %v", archives)
	}
	archived, err := os.ReadFile(archives[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(archived), "svc-a") || !strings.Contains(string(archived), "连续上报失败") {
		t.Fatalf("dead-letter archive lacks batch/reason evidence: %s", archived)
	}

	// 恢复：下周期 Buffered 晋升为 Pending、沿用同一序号、带死信计数上行。
	fail = false
	m.collectAndReport()
	if len(*captured) != 4 {
		t.Fatalf("expected recovery attempt after dead-letter, got %d sends", len(*captured))
	}
	last := (*captured)[len(*captured)-1].env
	if len(last.Items) != 1 || last.Items[0].N != "svc-b" {
		t.Fatalf("expected promoted svc-b batch, got %+v", last.Items)
	}
	if last.Sequence != deadSeq {
		t.Fatalf("promoted batch sequence=%d, want reused %d", last.Sequence, deadSeq)
	}
	if last.DroppedBytes != 3333 {
		t.Fatalf("promoted batch DroppedBytes=%d, want 3333", last.DroppedBytes)
	}
	if m.spool.state.Pending != nil || m.spool.state.DroppedBytes != 0 {
		t.Fatalf("expected Pending cleared and DroppedBytes settled after ack, got pending=%v dropped=%d",
			m.spool.state.Pending, m.spool.state.DroppedBytes)
	}
	if m.spool.state.Next != deadSeq+1 {
		t.Fatalf("Next=%d, want %d after ack", m.spool.state.Next, deadSeq+1)
	}
}

func TestTrafficPendingTTLDeadLettersEvenDuringBackoff(t *testing.T) {
	origReportDo, origReportURL, origAESCrypto := reportDo, httpReportURL, httpAESCrypto
	origMaxFailures, origTTL := trafficMaxConsecutiveFailures, trafficPendingTTL
	defer func() {
		reportDo, httpReportURL, httpAESCrypto = origReportDo, origReportURL, origAESCrypto
		trafficMaxConsecutiveFailures, trafficPendingTTL = origMaxFailures, origTTL
	}()
	trafficMaxConsecutiveFailures = 100 // 失败计数够不到上限：只验 TTL 兜底
	trafficPendingTTL = time.Minute

	dir := t.TempDir()
	captured := stubV2Panel(t, "test-secret", func(env trafficEnvelope) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})
	m := newBackoffTestManager(t, filepath.Join(dir, "flmx_traffic_ttl.json"))

	m.collectAndReport() // 失败 1 次，Pending 建成
	if m.spool.state.Pending == nil {
		t.Fatal("expected Pending after first failed report")
	}
	// 批次已超龄，且正处退避窗口内：TTL 必须先于退避门死信，不等发送。
	m.spool.state.PendingSince = time.Now().Add(-2 * time.Hour).UnixMilli()
	m.collectAndReport()
	if m.spool.state.Pending != nil {
		t.Fatal("expected stale Pending dead-lettered by TTL")
	}
	if len(*captured) != 1 {
		t.Fatalf("TTL dead-letter must not send again, got %d sends", len(*captured))
	}
	archives, _ := filepath.Glob(filepath.Join(dir, "*.rejected-*.json"))
	if len(archives) != 1 {
		t.Fatalf("expected 1 TTL dead-letter archive, got %v", archives)
	}
	archived, _ := os.ReadFile(archives[0])
	if !strings.Contains(string(archived), "超龄") {
		t.Fatalf("TTL archive lacks reason evidence: %s", archived)
	}
}

func TestTrafficRecoveryAfterTransientFailureResumes(t *testing.T) {
	origReportDo, origReportURL, origAESCrypto := reportDo, httpReportURL, httpAESCrypto
	origMaxFailures := trafficMaxConsecutiveFailures
	defer func() {
		reportDo, httpReportURL, httpAESCrypto = origReportDo, origReportURL, origAESCrypto
		trafficMaxConsecutiveFailures = origMaxFailures
	}()
	trafficMaxConsecutiveFailures = 8

	fail := true
	captured := stubV2Panel(t, "test-secret", func(env trafficEnvelope) (*http.Response, error) {
		if fail {
			return nil, errors.New("connection refused")
		}
		return v2AckResponse(env)
	})
	m := newBackoffTestManager(t, filepath.Join(t.TempDir(), "flmx_traffic_recover.json"))

	m.collectAndReport()
	m.retryAt = time.Time{}
	m.collectAndReport()
	if m.failCount != 2 || len(*captured) != 2 {
		t.Fatalf("expected 2 failures, got failCount=%d sends=%d", m.failCount, len(*captured))
	}

	// 面板恢复：同一批次原样确认，计数清零、序号推进。
	fail = false
	m.retryAt = time.Time{}
	m.collectAndReport()
	if m.failCount != 0 || !m.retryAt.IsZero() {
		t.Fatalf("expected fail state reset after recovery, got failCount=%d retryAt=%v", m.failCount, m.retryAt)
	}
	if m.spool.state.Pending != nil || m.spool.state.Next != 2 {
		t.Fatalf("expected Pending cleared and Next=2, got pending=%v next=%d", m.spool.state.Pending, m.spool.state.Next)
	}

	// 后续新流量照常上报、序号续上。
	m.serviceTraffic["svc-a"] = &ServiceTraffic{ServiceName: "svc-a", UpBytes: 7, DownBytes: 8}
	m.collectAndReport()
	if len(*captured) != 4 {
		t.Fatalf("expected resumed report after recovery, got %d sends", len(*captured))
	}
	if (*captured)[3].env.Sequence != 2 {
		t.Fatalf("resumed batch sequence=%d, want 2", (*captured)[3].env.Sequence)
	}
}

func TestTrafficLegacyPendingGetsTimestamp(t *testing.T) {
	origReportDo, origReportURL, origAESCrypto := reportDo, httpReportURL, httpAESCrypto
	defer func() {
		reportDo, httpReportURL, httpAESCrypto = origReportDo, origReportURL, origAESCrypto
	}()
	stubV2Panel(t, "test-secret", func(env trafficEnvelope) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})
	m := newBackoffTestManager(t, filepath.Join(t.TempDir(), "flmx_traffic_legacy.json"))

	m.collectAndReport()
	if m.spool.state.Pending == nil || m.spool.state.PendingSince == 0 {
		t.Fatal("expected stamped Pending after first report")
	}
	// 模拟升级前的旧 spool：Pending 在、时间戳缺失。
	m.spool.state.PendingSince = 0
	m.retryAt = time.Time{}
	m.collectAndReport()
	if m.spool.state.PendingSince == 0 {
		t.Fatal("expected legacy Pending to be stamped on first post-upgrade cycle")
	}
	// 落盘亦补戳：重开同一文件仍带时间戳。
	reopened, err := openTrafficSpool(m.spool.path)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.state.PendingSince == 0 {
		t.Fatal("expected stamped PendingSince to survive spool reload")
	}
}
