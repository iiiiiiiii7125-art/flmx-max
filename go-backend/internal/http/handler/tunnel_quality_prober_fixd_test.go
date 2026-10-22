package handler

import (
	"context"
	"sync/atomic"
	"testing"

	"gorm.io/gorm"
)

// FIX-D / D6-02 证据测试：一轮 probeAll 的 DB 查询数。
// 场景：1 条 type-2 隧道，入口节点 10 + 出口节点 30/31（共 3 个不同
// 节点）。旧实现对同一节点在选主跳/候选链路/公网候选/best-exit 各
// 阶段反复 getNodeRecord（单轮约 13 次 node 表查询），且逐隧道查
// ip_preference、每轮 2 次配置查询。修复后口径：
//   - node 表查询 = 不同节点数（3，轮内缓存）
//   - ip_preference 整轮 1 次批量查询（计入 tunnel 表查询）
//   - 配置三键合读 1 次且 5s 内跨轮缓存（第二轮配置查询 = 0）
func TestFixdProbeRoundQueryBudget(t *testing.T) {
	h := setupProbeTargetTunnelHandler(t)
	seedQualityForwardTunnel(t, h, 90, []int{1, 1})

	var totalQueries, nodeQueries, configQueries, tunnelQueries atomic.Int64
	err := h.repo.DB().Callback().Query().Before("gorm:query").Register("fixd_count_queries", func(tx *gorm.DB) {
		totalQueries.Add(1)
		if tx.Statement == nil {
			return
		}
		switch tx.Statement.Table {
		case "node":
			nodeQueries.Add(1)
		case "vite_config":
			configQueries.Add(1)
		case "tunnel":
			tunnelQueries.Add(1)
		}
	})
	if err != nil {
		t.Fatalf("register query counter: %v", err)
	}

	p := newTunnelQualityProber(h)
	p.ctx = context.Background()
	p.probeNode = func(nodeID int64, ip string, port int, options diagnosisExecOptions) (float64, float64, error) {
		return 10, 0, nil
	}

	p.probeAll()
	t.Logf("round1: total=%d node=%d config=%d tunnel=%d",
		totalQueries.Load(), nodeQueries.Load(), configQueries.Load(), tunnelQueries.Load())
	if got := nodeQueries.Load(); got != 3 {
		t.Fatalf("D6-02: node queries in one round = %d, want 3 (unique nodes via round cache)", got)
	}
	if got := configQueries.Load(); got > 1 {
		t.Fatalf("D6-02: config queries in one round = %d, want <= 1 (batched keys)", got)
	}
	if got := tunnelQueries.Load(); got > 3 {
		t.Fatalf("D6-02: tunnel queries in one round = %d, want <= 3 (record + chain + batched ip prefs)", got)
	}

	// 第二轮紧接第一轮：配置走缓存，config 查询不得再增加；
	// node/ip-pref 仍按轮刷新（节点状态/IP 偏好是轮内语义）。
	configBefore := configQueries.Load()
	p.probeAll()
	if got := configQueries.Load(); got != configBefore {
		t.Fatalf("D6-02: second round re-read configs (%d -> %d), want cache hit", configBefore, got)
	}
	snaps := p.GetAll()
	if len(snaps) != 1 || !snaps[0].Success {
		t.Fatalf("probe semantics changed: %+v", snaps)
	}
}
