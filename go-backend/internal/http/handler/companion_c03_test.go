package handler

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// ---- C-03: companion-instance split (panel half) ----
//
// The agent half (companion adapters entry + instance isolation) is
// covered by go-gost/x/kernel/companion_c03_test.go. These tests pin
// the panel half: the payload partition, the one-time migration, the
// UI hiding, and the companion-creation routing.

// TestC03AgentSupportsCompanionSplit pins the version gate: the
// companion instance needs an agent that knows the "singbox-companion"
// type (first shipped in 2.1.14); older agents keep companions on the
// main kernel, unchanged.
func TestC03AgentSupportsCompanionSplit(t *testing.T) {
	cases := []struct {
		version string
		want    bool
	}{
		{"2.1.14", true},
		{"2.1.15", true},
		{"2.2.0", true},
		{"2.1.13", false},
		{"2.1.12", false},
		{"", false},
		{"dev", false},
	}
	for _, c := range cases {
		if got := agentSupportsCompanionSplit(c.version); got != c.want {
			t.Errorf("agentSupportsCompanionSplit(%q) = %v, want %v", c.version, got, c.want)
		}
	}
}

// c03PayloadConfig concatenates the rendered config files of a payload
// (empty when the kernel renders nothing).
func c03PayloadConfig(t *testing.T, payload map[string]interface{}) string {
	t.Helper()
	files, _ := payload["files"].([]kernel.File)
	var sb strings.Builder
	for _, f := range files {
		sb.WriteString(f.Content)
	}
	return sb.String()
}

// c03SplitFixture seeds a node + sing-box main kernel + SS2022 origin +
// companion (still on the main kernel, pre-split), with the node
// reporting agentVersion.
func c03SplitFixture(t *testing.T, agentVersion string) (*repo.Repository, *Handler, int64, *model.NodeKernel, *model.NodeInbound, *model.NodeInbound) {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/c03-split.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	node := seedRelayTargetNode(t, r, "c03-split", "203.0.113.31")
	if agentVersion != "2.1.13" {
		if err := r.DB().Exec("UPDATE node SET version=? WHERE id=?", agentVersion, node).Error; err != nil {
			t.Fatal(err)
		}
	}
	main := seedChainKernel(t, r, node, kernel.TypeSingbox)
	origin := seedChainInbound(t, r, main, "c03-ss", kernel.Inbound{Protocol: kernel.ProtocolShadowsocks, Port: 8388,
		Settings: kernel.Settings{Method: meteringMethod, Password: meteringSharedPW}}, 0)
	if !h.createCompanion(origin, time.Now().UnixMilli()) {
		t.Fatal("createCompanion must succeed")
	}
	comp, err := r.GetCompanionInbound(origin.ID)
	if err != nil || comp == nil {
		t.Fatalf("companion must be materialized: %v %v", comp, err)
	}
	return r, h, node, main, origin, comp
}

// TestC03CompanionPayloadPartition is the core C-03 panel proof: after
// the split, the main kernel's config serves the origin only and the
// companion kernel's config serves the companion only — a main-kernel
// Apply (e.g. a subscription cutoff touching the origin) no longer
// carries the companion, so its sessions are not restarted with it.
// And the companion renders only while its origin is enabled node-wide
// (the gate must not be per-kernel: the companion kernel's own rows
// are companions only, so a same-kernel map would be empty and every
// companion would be skipped).
func TestC03CompanionPayloadPartition(t *testing.T) {
	r, h, node, main, origin, comp := c03SplitFixture(t, "2.1.14")
	compKernel := seedChainKernel(t, r, node, kernel.TypeSingboxCompanion)
	comp.KernelID = compKernel.ID
	comp.UpdatedTime = time.Now().UnixMilli()
	if err := r.SaveNodeInbound(comp); err != nil {
		t.Fatal(err)
	}
	compPort := strconv.Itoa(int(comp.Port))

	mainPayload, _, err := h.buildKernelApplyPayload(main)
	if err != nil {
		t.Fatal(err)
	}
	mainCfg := c03PayloadConfig(t, mainPayload)
	if !strings.Contains(mainCfg, "8388") {
		t.Fatalf("main config must serve the origin port 8388:\n%s", mainCfg)
	}
	if strings.Contains(mainCfg, compPort) {
		t.Fatalf("main config must not contain the companion port %s:\n%s", compPort, mainCfg)
	}

	compPayload, _, err := h.buildKernelApplyPayload(compKernel)
	if err != nil {
		t.Fatal(err)
	}
	if compPayload["type"] != kernel.TypeSingboxCompanion {
		t.Fatalf("payload type must stay the raw kernel type (agent instance key), got %v", compPayload["type"])
	}
	compCfg := c03PayloadConfig(t, compPayload)
	if !strings.Contains(compCfg, compPort) {
		t.Fatalf("companion config must serve the companion port %s:\n%s", compPort, compCfg)
	}
	if strings.Contains(compCfg, "8388") {
		t.Fatalf("companion config must not contain the origin port 8388:\n%s", compCfg)
	}

	// Origin disabled -> the companion must not render (node-wide gate).
	origin.Enabled = 0
	origin.UpdatedTime = time.Now().UnixMilli()
	if err := r.SaveNodeInbound(origin); err != nil {
		t.Fatal(err)
	}
	offPayload, _, err := h.buildKernelApplyPayload(compKernel)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(c03PayloadConfig(t, offPayload), compPort) {
		t.Fatal("disabled origin must gate the companion off")
	}
}

// TestC03EnsureCompanionKernelSplitsOnNewAgent pins the one-time
// migration: on a 2.1.14+ node the companion kernel row is created
// (version tracks the main sing-box kernel), every companion moves
// onto it, the origin stays on main, and a second call is a no-op.
func TestC03EnsureCompanionKernelSplitsOnNewAgent(t *testing.T) {
	r, h, node, main, origin, comp := c03SplitFixture(t, "2.1.14")
	h.ensureCompanionKernel(node)

	compKernel, err := r.GetNodeKernelByType(node, kernel.TypeSingboxCompanion)
	if err != nil || compKernel == nil {
		t.Fatalf("companion kernel row must exist: %v", compKernel)
	}
	if compKernel.Version != main.Version {
		t.Fatalf("companion kernel version must track main (%q), got %q", main.Version, compKernel.Version)
	}
	after, err := r.GetCompanionInbound(origin.ID)
	if err != nil || after == nil {
		t.Fatalf("companion must survive the split: %v", err)
	}
	if after.KernelID != compKernel.ID {
		t.Fatalf("companion must move to the companion kernel %d, got %d", compKernel.ID, after.KernelID)
	}
	originAfter, err := r.GetNodeInbound(origin.ID)
	if err != nil || originAfter == nil {
		t.Fatalf("origin must survive the split: %v", err)
	}
	if originAfter.KernelID != main.ID {
		t.Fatalf("origin must stay on the main kernel %d, got %d", main.ID, originAfter.KernelID)
	}

	// Idempotent: a second call creates no second row, and a straggler
	// companion handed back onto main is moved again.
	h.ensureCompanionKernel(node)
	kernels, err := r.ListNodeKernels(node)
	if err != nil {
		t.Fatal(err)
	}
	if len(kernels) != 2 {
		t.Fatalf("split must be idempotent, got %d kernel rows", len(kernels))
	}
	after.KernelID = main.ID
	after.UpdatedTime = time.Now().UnixMilli()
	if err := r.SaveNodeInbound(after); err != nil {
		t.Fatal(err)
	}
	_ = comp
	h.ensureCompanionKernel(node)
	moved, err := r.GetCompanionInbound(origin.ID)
	if err != nil || moved == nil || moved.KernelID != compKernel.ID {
		t.Fatalf("straggler companion must be moved back to the companion kernel: %v %+v", err, moved)
	}
}

// TestC03EnsureCompanionKernelSyncsVersionOnMainUpgrade：主核换版后
// 伴生行须同升——只在创建时快照主核版本会留一个跑旧二进制（旧队列
// 64）的伴生实例；行已存在时 ensure 同样同步版本登记。
func TestC03EnsureCompanionKernelSyncsVersionOnMainUpgrade(t *testing.T) {
	r, h, node, main, _, _ := c03SplitFixture(t, "2.1.14")
	h.ensureCompanionKernel(node)
	compKernel, err := r.GetNodeKernelByType(node, kernel.TypeSingboxCompanion)
	if err != nil || compKernel == nil {
		t.Fatalf("companion kernel row must exist: %v", compKernel)
	}
	// 主核换到 fork 版、伴生行仍为旧版，ensure 须把伴生行同升。
	if err := r.UpdateNodeKernelFields(main.ID, map[string]interface{}{
		"version": kernel.SingboxForkVersion, "updated_time": time.Now().UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.UpdateNodeKernelFields(compKernel.ID, map[string]interface{}{
		"version": "1.14.3", "updated_time": time.Now().UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	h.ensureCompanionKernel(node)
	after, err := r.GetNodeKernelByType(node, kernel.TypeSingboxCompanion)
	if err != nil || after == nil {
		t.Fatalf("companion kernel row must survive: %v", err)
	}
	if after.Version != kernel.SingboxForkVersion {
		t.Fatalf("companion kernel version must track the upgraded main (%q), got %q", kernel.SingboxForkVersion, after.Version)
	}
	kernels, err := r.ListNodeKernels(node)
	if err != nil {
		t.Fatal(err)
	}
	if len(kernels) != 2 {
		t.Fatalf("sync must not create a second companion row, got %d kernel rows", len(kernels))
	}
}

// TestC03EnsureCompanionKernelSkipsOldAgent pins the fallback: on a
// pre-2.1.14 agent no companion row is created and the companion stays
// on the main kernel, behaving exactly as before the split.
func TestC03EnsureCompanionKernelSkipsOldAgent(t *testing.T) {
	r, h, node, main, origin, _ := c03SplitFixture(t, "2.1.13")
	h.ensureCompanionKernel(node)
	if k, _ := r.GetNodeKernelByType(node, kernel.TypeSingboxCompanion); k != nil {
		t.Fatalf("old agent must not get a companion kernel row, got %+v", k)
	}
	after, err := r.GetCompanionInbound(origin.ID)
	if err != nil || after == nil {
		t.Fatalf("companion must survive: %v", err)
	}
	if after.KernelID != main.ID {
		t.Fatalf("companion must stay on the main kernel %d, got %d", main.ID, after.KernelID)
	}
}

// TestC03KernelViewsHidesCompanion pins that the companion kernel is
// managed, never user-facing: the kernel list shows the main kernel
// only.
func TestC03KernelViewsHidesCompanion(t *testing.T) {
	_, h, node, _, _, _ := c03SplitFixture(t, "2.1.14")
	h.ensureCompanionKernel(node)
	views, err := h.kernelViews(node)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 {
		t.Fatalf("companion kernel must be hidden from the UI list, got %d views", len(views))
	}
	if views[0].KernelType != kernel.TypeSingbox {
		t.Fatalf("the visible kernel must be the main sing-box kernel, got %q", views[0].KernelType)
	}
}

// TestC03CreateCompanionUsesCompanionKernelWhenSplit pins that a
// companion created while the split is active lands on the companion
// kernel, not the origin's.
func TestC03CreateCompanionUsesCompanionKernelWhenSplit(t *testing.T) {
	r, h, _, origin, _ := seedMeteringFixture(t)
	compKernel := seedChainKernel(t, r, origin.NodeID, kernel.TypeSingboxCompanion)
	h.reconcileSSMetering()
	comp, err := r.GetCompanionInbound(origin.ID)
	if err != nil || comp == nil {
		t.Fatalf("companion must be materialized: %v %v", comp, err)
	}
	if comp.KernelID != compKernel.ID {
		t.Fatalf("companion must land on the companion kernel %d, got %d", compKernel.ID, comp.KernelID)
	}
}
