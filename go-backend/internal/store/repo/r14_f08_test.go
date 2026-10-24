package repo

import (
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R14-F08 / A5-08 导入侧：备份里带出来的 Snell+Via / Snell+分流毒化行
// 在恢复后置校验里即剥离为直连（与悬空引用剥离同口径），不得原样落库
// 后阻塞整内核下发；剥离须进 ImportWarnings 披露。
func TestR14F08ImportStripsSnellPoisonedRouting(t *testing.T) {
	r, err := Open(t.TempDir() + "/r14f08-a508-import.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	now := time.Now().UnixMilli()

	node := model.Node{Name: "n", Secret: "n", ServerIP: "192.0.2.1"}
	if err := r.DB().Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	snellK := model.NodeKernel{NodeID: node.ID, KernelType: "snell", Version: "6.0.0", Enabled: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.DB().Create(&snellK).Error; err != nil {
		t.Fatal(err)
	}
	xrayK := model.NodeKernel{NodeID: node.ID, KernelType: "xray", Version: "25.9.5", Enabled: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.DB().Create(&xrayK).Error; err != nil {
		t.Fatal(err)
	}
	target := model.NodeInbound{KernelID: xrayK.ID, NodeID: node.ID, Name: "xray-target", Protocol: "vless",
		Listen: "::", Port: 24443, Transport: `{"type":"tcp"}`, Security: `{"type":"none"}`,
		Settings: `{"uuid":"0b6e4c38-8f0e-4a3e-9d2d-7a1c2b3c4d5e"}`, Enabled: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.DB().Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	poisoned := model.NodeInbound{KernelID: snellK.ID, NodeID: node.ID, Name: "snell-poisoned", Protocol: "snell",
		Listen: "::", Port: 20001, Transport: `{"type":"tcp"}`, Security: `{"type":"none"}`,
		Settings: `{"password":"test-psk-123456","snellVersion":6}`, Enabled: 1,
		OutboundID: target.ID, SplitRules: `[{"action":"direct","domains":["example.com"]}]`,
		CreatedTime: now, UpdatedTime: now}
	if err := r.DB().Create(&poisoned).Error; err != nil {
		t.Fatal(err)
	}
	// 对照：Xray 内核同款带 Via 行不得被剥离。
	xrayRow := model.NodeInbound{KernelID: xrayK.ID, NodeID: node.ID, Name: "xray-via", Protocol: "vless",
		Listen: "::", Port: 24444, Transport: `{"type":"tcp"}`, Security: `{"type":"none"}`,
		Settings: `{"uuid":"0b6e4c38-8f0e-4a3e-9d2d-7a1c2b3c4d5e"}`, Enabled: 1,
		OutboundID: target.ID, CreatedTime: now, UpdatedTime: now}
	if err := r.DB().Create(&xrayRow).Error; err != nil {
		t.Fatal(err)
	}

	rep := &importReport{skipped: map[string]int{}}
	rows := []model.InboundBackup{{ID: poisoned.ID}, {ID: xrayRow.ID}}
	if err := validateImportedInboundRefsTx(r.DB(), rows, rep); err != nil {
		t.Fatalf("validateImportedInboundRefsTx: %v", err)
	}

	var got model.NodeInbound
	if err := r.DB().Where("id = ?", poisoned.ID).First(&got).Error; err != nil {
		t.Fatal(err)
	}
	if got.OutboundID != 0 || strings.TrimSpace(got.SplitRules) != "" {
		t.Fatalf("snell poisoned row not stripped: outbound_id=%d split_rules=%q", got.OutboundID, got.SplitRules)
	}
	var gotX model.NodeInbound
	if err := r.DB().Where("id = ?", xrayRow.ID).First(&gotX).Error; err != nil {
		t.Fatal(err)
	}
	if gotX.OutboundID != target.ID {
		t.Fatalf("xray+via row must be untouched: outbound_id=%d want %d", gotX.OutboundID, target.ID)
	}
	joined := strings.Join(rep.warnings, "\n")
	if !strings.Contains(joined, "Snell") {
		t.Fatalf("import warnings must disclose the Snell strip, got %q", rep.warnings)
	}
}
