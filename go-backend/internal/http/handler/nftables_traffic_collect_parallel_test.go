package handler

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// L3-04：nft 采集轮必须多节点并行（有界并发 8），单节点 SSH 挂死
// 不得拖死整轮。4 节点 × 每节点 1s 延迟：串行约 4s，并行约 1s。

func seedNftablesNodeWithHost(t *testing.T, h *Handler, name, host string) int64 {
	t.Helper()
	now := time.Now().UnixMilli()
	id, err := h.repo.CreateNode(name, "secret-"+name, "198.51.100.10", nil, nil, "1000-65535", nil, nil, nil, nil, nil, 0, 0, 0, now, 1, "", "", 1, nil, "nftables")
	if err != nil {
		t.Fatalf("create node %s: %v", name, err)
	}
	if err := h.repo.UpsertNodeSSHConfig(id, repo.NftSSHConfigInput{
		Host:     host,
		Port:     22,
		Username: "root",
		AuthType: "password",
		Password: "test-password",
		SudoMode: "none",
	}, now); err != nil {
		t.Fatalf("seed ssh config node %d: %v", id, err)
	}
	return id
}

func setupParallelCollectFixture(t *testing.T, hosts []string) (*Handler, *fakeNftablesManager) {
	t.Helper()
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	if len(hosts) == 0 {
		t.Fatal("need at least one host")
	}
	// setupNftablesHandler 建的节点复用为第 1 个采集节点。
	if err := h.repo.UpsertNodeSSHConfig(fixture.nodeID, repo.NftSSHConfigInput{
		Host:     hosts[0],
		Port:     22,
		Username: "root",
		AuthType: "password",
		Password: "test-password",
		SudoMode: "none",
	}, time.Now().UnixMilli()); err != nil {
		t.Fatalf("seed ssh config node %d: %v", fixture.nodeID, err)
	}
	for i := 1; i < len(hosts); i++ {
		seedNftablesNodeWithHost(t, h, fmt.Sprintf("nft-parallel-%d", i), hosts[i])
	}
	manager := &fakeNftablesManager{
		collectDelayByHost: map[string]time.Duration{},
		collectErrByHost:   map[string]error{},
	}
	h.nftablesManager = newSerializedNftManager(manager)
	return h, manager
}

func TestRunNftablesTrafficCollectJobParallelAcrossNodes(t *testing.T) {
	hosts := []string{"203.0.113.11", "203.0.113.12", "203.0.113.13", "203.0.113.14"}
	h, manager := setupParallelCollectFixture(t, hosts)
	for _, host := range hosts {
		manager.collectDelayByHost[host] = time.Second
	}

	start := time.Now()
	h.runNftablesTrafficCollectJob(time.Now())
	elapsed := time.Since(start)

	if got := manager.collectCount(); got != len(hosts) {
		t.Fatalf("expected %d collections, got %d", len(hosts), got)
	}
	if max := manager.collectMaxConcurrency(); max < 2 {
		t.Fatalf("expected parallel collection across nodes, max concurrency=%d", max)
	}
	// 串行要约 4s（4×1s）；并行约 1s。2.5s 分界两侧都有裕量。
	if elapsed >= 2500*time.Millisecond {
		t.Fatalf("4 nodes x 1s serial would take ~4s, parallel must finish near 1s; took %v", elapsed)
	}
}

func TestRunNftablesTrafficCollectJobSlowNodeFailureDoesNotBlockOthers(t *testing.T) {
	hosts := []string{"203.0.113.21", "203.0.113.22", "203.0.113.23", "203.0.113.24"}
	h, manager := setupParallelCollectFixture(t, hosts)
	manager.collectErrByHost[hosts[0]] = errors.New("ssh dial timeout")
	for _, host := range hosts[1:] {
		manager.collectDelayByHost[host] = time.Second
	}

	start := time.Now()
	h.runNftablesTrafficCollectJob(time.Now())
	elapsed := time.Since(start)

	if got := manager.collectCount(); got != len(hosts) {
		t.Fatalf("a failing node must not skip the others: expected %d collection attempts, got %d", len(hosts), got)
	}
	if elapsed >= 2500*time.Millisecond {
		t.Fatalf("failing slow node must not stall the round; took %v", elapsed)
	}
}
