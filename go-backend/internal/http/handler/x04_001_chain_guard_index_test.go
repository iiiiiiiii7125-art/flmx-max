package handler

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// X04-001 回归：链式引用守卫的单次快照索引（chainRefIndex）。
//
// 旧实现每查一个目标就 ListNodeInbounds 全表重载并逐行解析
// outboundTargets：删节点 = 按其入站数重载，inboundIDsDialedByNode
// 更按全表逐行经 chainDialerNodeIDs 重载，T=1000/k=10 实测 55s、
// T=33000 时 1446s 未完成。本文件的断言分两半：
//   1. 等价性——索引对每个目标的引用方/拨号节点结果与旧逐行扫描
//      逐项一致（顺序、去重、外部行过滤口径全对齐）；
//   2. 规模——T=1000/k=10 的删节点守卫整轮（跨节点守卫 + 受影响
//      收集，含 Θ(T) 的 dialed 扫描）必须在宽松上限内完成。

// bruteForceReferrers replicates the pre-fix chainReferrers scan.
func bruteForceReferrers(rows []model.NodeInbound, targetID int64) []int64 {
	var out []int64
	for _, row := range rows {
		for _, id := range outboundTargets(row) {
			if id == targetID {
				out = append(out, row.ID)
				break
			}
		}
	}
	return out
}

// bruteForceDialerNodeIDs replicates the pre-fix chainDialerNodeIDs.
func bruteForceDialerNodeIDs(rows []model.NodeInbound, inboundID int64) []int64 {
	var out []int64
	for _, refID := range bruteForceReferrers(rows, inboundID) {
		for _, row := range rows {
			if row.ID == refID && !isExternalInbound(&row) {
				out = append(out, row.NodeID)
			}
		}
	}
	return out
}

func x04IDs(rows []model.NodeInbound) []int64 {
	out := make([]int64, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.ID)
	}
	return out
}

func x04EqualIDs(t *testing.T, what string, got, want []int64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %v, want %v", what, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s: got %v, want %v", what, got, want)
		}
	}
}

func x04Split(t *testing.T, rules []kernel.SplitRule) string {
	t.Helper()
	raw, err := json.Marshal(rules)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func x04Row(t *testing.T, r *repo.Repository, row model.NodeInbound) model.NodeInbound {
	t.Helper()
	row.Enabled = 1
	row.CreatedTime = time.Now().UnixMilli()
	if err := r.DB().Create(&row).Error; err != nil {
		t.Fatalf("create inbound %q: %v", row.Name, err)
	}
	return row
}

func TestX04ChainRefIndexEquivalence(t *testing.T) {
	r, h := r3b9Open(t, "x04-index.db")
	nodeA := seedRelayTargetNode(t, r, "a", "198.51.100.1")
	nodeB := seedRelayTargetNode(t, r, "b", "198.51.100.2")
	nodeC := seedRelayTargetNode(t, r, "c", "198.51.100.3")
	kA := seedChainKernel(t, r, nodeA, kernel.TypeXray)
	kB := seedChainKernel(t, r, nodeB, kernel.TypeSingbox)
	kC := seedChainKernel(t, r, nodeC, kernel.TypeXray)

	target1 := x04Row(t, r, model.NodeInbound{KernelID: kA.ID, NodeID: nodeA, Name: "t1", Port: 1001})
	target2 := x04Row(t, r, model.NodeInbound{KernelID: kB.ID, NodeID: nodeB, Name: "t2", Port: 1002})
	target3 := x04Row(t, r, model.NodeInbound{KernelID: kA.ID, NodeID: nodeA, Name: "t3", Port: 1003})
	ref1 := x04Row(t, r, model.NodeInbound{KernelID: kB.ID, NodeID: nodeB, Name: "r1", Port: 2001, OutboundID: target1.ID})
	ref2 := x04Row(t, r, model.NodeInbound{KernelID: kC.ID, NodeID: nodeC, Name: "r2", Port: 2002, SplitRules: x04Split(t, []kernel.SplitRule{
		{Action: kernel.SplitProxy, OutboundID: target1.ID},
		{Action: kernel.SplitProxy, OutboundID: target2.ID},
		{Action: kernel.SplitDirect, OutboundID: target1.ID},
	})})
	// Same target via OutboundID AND a split rule: must appear once.
	ref3 := x04Row(t, r, model.NodeInbound{KernelID: kC.ID, NodeID: nodeC, Name: "r3", Port: 2003, OutboundID: target1.ID, SplitRules: x04Split(t, []kernel.SplitRule{
		{Action: kernel.SplitProxy, OutboundID: target1.ID},
	})})
	// External referrer (KernelID=0): counts as a referrer, never as a dialer node.
	ext := x04Row(t, r, model.NodeInbound{KernelID: 0, NodeID: nodeB, Name: "ext", Port: 2004, OutboundID: target1.ID})
	_ = x04Row(t, r, model.NodeInbound{KernelID: kA.ID, NodeID: nodeA, Name: "r5", Port: 2005, OutboundID: target2.ID})
	_ = x04Row(t, r, model.NodeInbound{KernelID: kA.ID, NodeID: nodeA, Name: "plain", Port: 2006})

	rows, err := r.ListNodeInbounds()
	if err != nil {
		t.Fatal(err)
	}
	ix := chainRefIndexFromRows(rows)

	for _, targetID := range []int64{target1.ID, target2.ID, target3.ID, ext.ID, ref1.ID, 99999} {
		x04EqualIDs(t, fmt.Sprintf("referrers(%d)", targetID), x04IDs(ix.referrers(targetID)), bruteForceReferrers(rows, targetID))
		x04EqualIDs(t, fmt.Sprintf("dialerNodeIDs(%d)", targetID), ix.dialerNodeIDs(targetID), bruteForceDialerNodeIDs(rows, targetID))
	}
	// Public single-call surface agrees with the brute-force scan.
	refs, err := h.chainReferrers(target1.ID)
	if err != nil {
		t.Fatal(err)
	}
	x04EqualIDs(t, "chainReferrers(t1)", x04IDs(refs), []int64{ref1.ID, ref2.ID, ref3.ID, ext.ID})
	dialers, err := h.chainDialerNodeIDs(target1.ID)
	if err != nil {
		t.Fatal(err)
	}
	x04EqualIDs(t, "chainDialerNodeIDs(t1)", dialers, []int64{nodeB, nodeC, nodeC})

	// Kernel uninstall guard over the snapshot: kernel A's inbounds are
	// target1/target3/r5/plain; only target1 has referrers, all from
	// outside the kernel, deduplicated by referrer ID.
	var kernelARows []model.NodeInbound
	for _, row := range rows {
		if row.KernelID == kA.ID {
			kernelARows = append(kernelARows, row)
		}
	}
	names, msg := h.kernelChainGuardIx(kernelARows, ix)
	if len(names) != 4 {
		t.Fatalf("kernelChainGuardIx names = %v, want 4 unique referrers", names)
	}
	if msg == "" {
		t.Fatal("kernelChainGuardIx message must not be empty")
	}
	names2, msg2, gerr := h.kernelChainGuard(kA.ID)
	if gerr != nil || len(names2) != 4 || msg2 != msg {
		t.Fatalf("kernelChainGuard wrapper mismatch: %v %q %v", names2, msg2, gerr)
	}

	// Cross-node guard for node A over the snapshot.
	var nodeARows []model.NodeInbound
	for _, row := range rows {
		if row.NodeID == nodeA {
			nodeARows = append(nodeARows, row)
		}
	}
	cross := h.nodeCrossReferrersIx(nodeARows, nodeA, ix)
	var crossIDs []int64
	for _, c := range cross {
		crossIDs = append(crossIDs, c.ID)
	}
	x04EqualIDs(t, "nodeCrossReferrersIx(A)", crossIDs, []int64{ref1.ID, ref2.ID, ref3.ID, ext.ID})
	cross2, cerr := h.nodeCrossReferrers(nodeA)
	if cerr != nil || len(cross2) != len(cross) {
		t.Fatalf("nodeCrossReferrers wrapper mismatch: %v %v", cross2, cerr)
	}
}

func TestX04NodeDeleteGuardRoundScales(t *testing.T) {
	r, h := r3b9Open(t, "x04-scale.db")
	victim := seedRelayTargetNode(t, r, "victim", "198.51.100.10")
	other := seedRelayTargetNode(t, r, "other", "198.51.100.11")

	const totalOther = 990
	const victimCount = 10
	now := time.Now().UnixMilli()
	victimRows := make([]model.NodeInbound, 0, victimCount)
	for i := 0; i < victimCount; i++ {
		victimRows = append(victimRows, model.NodeInbound{
			KernelID: 1, NodeID: victim, Name: fmt.Sprintf("v-%d", i),
			Port: 3000 + i, Enabled: 1, CreatedTime: now,
		})
	}
	if err := r.DB().CreateInBatches(&victimRows, 100).Error; err != nil {
		t.Fatal(err)
	}
	otherRows := make([]model.NodeInbound, 0, totalOther)
	for i := 0; i < totalOther; i++ {
		row := model.NodeInbound{
			KernelID: 1, NodeID: other, Name: fmt.Sprintf("o-%d", i),
			Port: 5000 + i, Enabled: 1, CreatedTime: now,
		}
		if i < 50 {
			// 50 cross referrers chained through victim's first inbound.
			row.OutboundID = victimRows[0].ID
		}
		otherRows = append(otherRows, row)
	}
	if err := r.DB().CreateInBatches(&otherRows, 200).Error; err != nil {
		t.Fatal(err)
	}
	// One victim inbound chains through otherRows[100]: that row is
	// dialed by the victim node (the Θ(T) dialed scan must find it).
	if err := r.DB().Model(&model.NodeInbound{}).Where("id = ?", victimRows[1].ID).
		Update("outbound_id", otherRows[100].ID).Error; err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	ix, err := h.loadChainRefIndex()
	if err != nil {
		t.Fatal(err)
	}
	targets, err := r.ListNodeInboundsByNode(victim)
	if err != nil {
		t.Fatal(err)
	}
	cross := h.nodeCrossReferrersIx(targets, victim, ix)
	affected, err := h.inboundIDsAffectedByNodeDeleteIx(victim, ix)
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)

	if len(cross) != 50 {
		t.Fatalf("cross referrers = %d, want 50", len(cross))
	}
	// affected = 50 cross referrers + otherRows[100] (dialed by victim).
	if len(affected) != 51 {
		t.Fatalf("affected inbounds = %d, want 51 (%v)", len(affected), affected)
	}
	// Pre-fix this round measured ~55s at T=1000/k=10 (V6 re-test); the
	// bound is deliberately loose so slow CI still passes, while the
	// quadratic shape (tens of seconds) cannot.
	if elapsed > 10*time.Second {
		t.Fatalf("X04-001 regression: node-delete guard round at T=1000/k=10 took %v, want < 10s", elapsed)
	}
	t.Logf("guard round at T=1000/k=10: %v", elapsed)
}
