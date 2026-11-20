package metrics

import (
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

func metricWithBytes(uptime, in, out uint64, boot string) SystemInfo {
	return SystemInfo{
		Uptime:           uptime,
		BytesReceived:    in,
		BytesTransmitted: out,
		BootID:           boot,
	}
}

// L3-05：ForgetNode 必须清掉六张表（lastBytes/lastBoot/lastSeen/
// traffic/lastMetricAt/metricDropped，R18-C4-02 补后两张）在该节点
// 上的全部残留键，且不动其他节点。
func TestForgetNodeDropsAllFourMaps(t *testing.T) {
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()
	svc := NewIngestionService(r)
	// R18-C4-02：同节点快速序列模拟，间隔置 0（不测限流）。
	svc.minMetricIntervalMs = 0

	// 两条上报才建 traffic 桶（首条只建基线）。
	// A-低-5：两节点必须用不同 BootID——同 BootID 即同机，去重后
	// 高 ID 节点不建桶，本测试的「两节点各有桶」前置即不成立。
	svc.RecordNodeMetric(7, metricWithBytes(10, 1000, 2000, "boot-a"))
	svc.RecordNodeMetric(7, metricWithBytes(20, 1500, 2600, "boot-a"))
	svc.RecordNodeMetric(8, metricWithBytes(10, 1000, 2000, "boot-b"))
	svc.RecordNodeMetric(8, metricWithBytes(20, 1500, 2600, "boot-b"))
	if len(svc.traffic) == 0 {
		t.Fatal("setup: expected traffic buckets for both nodes")
	}

	svc.ForgetNode(7)

	if _, ok := svc.lastBytes[7]; ok {
		t.Fatal("ForgetNode left lastBytes[7]")
	}
	if _, ok := svc.lastBoot[7]; ok {
		t.Fatal("ForgetNode left lastBoot[7]")
	}
	if _, ok := svc.lastSeen[7]; ok {
		t.Fatal("ForgetNode left lastSeen[7]")
	}
	if _, ok := svc.lastMetricAt[7]; ok {
		t.Fatal("ForgetNode left lastMetricAt[7]")
	}
	if _, ok := svc.metricDropped[7]; ok {
		t.Fatal("ForgetNode left metricDropped[7]")
	}
	for k := range svc.traffic {
		if k.node == 7 {
			t.Fatalf("ForgetNode left traffic bucket %+v", k)
		}
	}
	if _, ok := svc.lastBytes[8]; !ok {
		t.Fatal("ForgetNode must not touch other nodes' lastBytes")
	}
	if _, ok := svc.lastBoot[8]; !ok {
		t.Fatal("ForgetNode must not touch other nodes' lastBoot")
	}
	if _, ok := svc.lastSeen[8]; !ok {
		t.Fatal("ForgetNode must not touch other nodes' lastSeen")
	}
	kept := 0
	for k := range svc.traffic {
		if k.node == 8 {
			kept++
		}
	}
	if kept == 0 {
		t.Fatal("ForgetNode must not touch other nodes' traffic buckets")
	}
}

// L3-05 兜底：小时级 prune 按 DB 现存节点集逐出已删节点的残留键，
// 现存节点保留。
func TestEvictDeletedNodeKeysDropsStaleNodes(t *testing.T) {
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()
	svc := NewIngestionService(r)
	// R18-C4-02：同节点快速序列模拟，间隔置 0（不测限流）。
	svc.minMetricIntervalMs = 0

	now := time.Now().UnixMilli()
	liveID, err := r.CreateNode("keep-node", "secret-keep", "198.51.100.10", nil, nil, "1000-65535", nil, nil, nil, nil, nil, 0, 0, 0, now, 1, "", "", 1, nil, "")
	if err != nil {
		t.Fatalf("create live node: %v", err)
	}
	svc.RecordNodeMetric(liveID, metricWithBytes(10, 1000, 2000, "boot-a"))
	svc.RecordNodeMetric(liveID, metricWithBytes(20, 1500, 2600, "boot-a"))
	// 7 号节点在 DB 中不存在：模拟已被删、键残留。
	// A-低-5：7 号须用不同 BootID，否则与 live 节点同机去重后
	// 根本不建 traffic 桶，扫掉桶的断言即空洞。
	svc.RecordNodeMetric(7, metricWithBytes(10, 1000, 2000, "boot-b"))
	svc.RecordNodeMetric(7, metricWithBytes(20, 1500, 2600, "boot-b"))

	svc.evictDeletedNodeKeys()

	if _, ok := svc.lastBytes[7]; ok {
		t.Fatal("sweep left lastBytes[7] for a deleted node")
	}
	if _, ok := svc.lastBoot[7]; ok {
		t.Fatal("sweep left lastBoot[7] for a deleted node")
	}
	if _, ok := svc.lastSeen[7]; ok {
		t.Fatal("sweep left lastSeen[7] for a deleted node")
	}
	for k := range svc.traffic {
		if k.node == 7 {
			t.Fatalf("sweep left traffic bucket %+v for a deleted node", k)
		}
	}
	if _, ok := svc.lastBytes[liveID]; !ok {
		t.Fatal("sweep must keep the live node's keys")
	}
}
