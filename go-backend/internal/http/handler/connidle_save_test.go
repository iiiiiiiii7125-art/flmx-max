package handler

import (
	"encoding/json"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
)

// R20-X-02 (F-02): inbound edit silently dropped connIdleSeconds.
// SaveNodeInbound's Updates whitelist lacked "conn_idle_seconds", so an
// edit reported code 0 while the stored timeout — and the rendered
// policy — kept the old value. These tests pin the full chain:
// create with 5 -> edit to 10 through the same applyInboundToModel +
// SaveNodeInbound path inboundSave uses -> list shows 10 -> Xray render
// emits policy.levels["1"].connIdle = 10; and the companion mirror leg
// (syncCompanion must propagate an origin timeout change to its SS
// metering companion instead of no-oping on the dirty-check).

func TestConnIdleEditPersistsAndRenders(t *testing.T) {
	r, h := r2b2Open(t)
	_ = h
	now := time.Now().UnixMilli()
	node := model.Node{Name: "n1", Secret: "s", ServerIP: "198.51.100.1", Port: "26000-26100",
		Status: 1, CreatedTime: now}
	if err := r.DB().Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	k := &model.NodeKernel{NodeID: node.ID, KernelType: "xray", Version: "latest", Enabled: 1,
		CreatedTime: now, UpdatedTime: now}
	if err := r.CreateNodeKernel(k); err != nil {
		t.Fatal(err)
	}
	settings, _ := json.Marshal(kernel.Settings{UUID: "0b6e4c38-8f0e-4a3e-9d2d-7a1c2b3c4d5e"})
	transport, _ := json.Marshal(kernel.Transport{Type: "tcp"})
	security, _ := json.Marshal(kernel.Security{Type: "none"})
	row := &model.NodeInbound{KernelID: k.ID, NodeID: node.ID, Name: "vless-1", Protocol: "vless",
		Listen: "::", Port: 21001, Settings: string(settings), Transport: string(transport),
		Security: string(security), Enabled: 1, ConnIdleSeconds: 5, CreatedTime: now, UpdatedTime: now}
	if err := r.CreateNodeInbound(row); err != nil {
		t.Fatal(err)
	}

	// Edit leg: exactly what inboundSave does — load, convert, change the
	// timeout, convert back, save through the whitelist.
	stored, err := r.GetNodeInbound(row.ID)
	if err != nil || stored == nil {
		t.Fatalf("load created row: %v", err)
	}
	if stored.ConnIdleSeconds != 5 {
		t.Fatalf("created connIdleSeconds = %d, want 5", stored.ConnIdleSeconds)
	}
	in, err := inboundFromModel(*stored)
	if err != nil {
		t.Fatal(err)
	}
	in.ConnIdleSeconds = 10
	if err := applyInboundToModel(in, stored); err != nil {
		t.Fatal(err)
	}
	stored.UpdatedTime = time.Now().UnixMilli()
	if err := r.SaveNodeInbound(stored); err != nil {
		t.Fatal(err)
	}

	// List leg: the list view must show the edited value.
	listed, err := r.ListNodeInboundsByNode(node.ID)
	if err != nil || len(listed) != 1 {
		t.Fatalf("list: %v len=%d", err, len(listed))
	}
	if listed[0].ConnIdleSeconds != 10 {
		t.Fatalf("list connIdleSeconds = %d, want 10 (edit silently dropped)", listed[0].ConnIdleSeconds)
	}

	// Render leg: the stored row must render policy {connIdle:10}.
	in2, err := inboundFromModel(listed[0])
	if err != nil {
		t.Fatal(err)
	}
	out, err := kernel.Render(kernel.TypeXray, []kernel.Inbound{in2})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(out.Files[0].Content), &parsed); err != nil {
		t.Fatalf("invalid rendered json: %v", err)
	}
	policy, _ := parsed["policy"].(map[string]interface{})
	levels, _ := policy["levels"].(map[string]interface{})
	l1, _ := levels["1"].(map[string]interface{})
	if l1["connIdle"] != float64(10) {
		t.Fatalf("rendered policy.levels[1].connIdle = %v, want 10", l1["connIdle"])
	}

	// Explicit 0 clears the timeout (pointer semantics in inboundSave).
	in.ConnIdleSeconds = 0
	if err := applyInboundToModel(in, stored); err != nil {
		t.Fatal(err)
	}
	stored.UpdatedTime = time.Now().UnixMilli()
	if err := r.SaveNodeInbound(stored); err != nil {
		t.Fatal(err)
	}
	cleared, err := r.GetNodeInbound(row.ID)
	if err != nil || cleared == nil {
		t.Fatalf("reload cleared row: %v", err)
	}
	if cleared.ConnIdleSeconds != 0 {
		t.Fatalf("cleared connIdleSeconds = %d, want 0", cleared.ConnIdleSeconds)
	}
}

func TestConnIdleCompanionSyncMirrorsOrigin(t *testing.T) {
	r, h := r2b2Open(t)
	now := time.Now().UnixMilli()
	node := model.Node{Name: "n1", Secret: "s", ServerIP: "198.51.100.1", Port: "26000-26100",
		Status: 1, CreatedTime: now}
	if err := r.DB().Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	k := &model.NodeKernel{NodeID: node.ID, KernelType: "xray", Version: "latest",
		Enabled: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.CreateNodeKernel(k); err != nil {
		t.Fatal(err)
	}
	originSettings, _ := json.Marshal(kernel.Settings{Method: "2022-blake3-aes-128-gcm", Password: "origin-password-0123456789abcdef"})
	compSettings, _ := json.Marshal(kernel.Settings{Method: "2022-blake3-aes-128-gcm", Password: "companion-psk-0123456789abcdef"})
	transport, _ := json.Marshal(kernel.Transport{Type: "tcp"})
	security, _ := json.Marshal(kernel.Security{Type: "none"})
	origin := &model.NodeInbound{KernelID: k.ID, NodeID: node.ID, Name: "ss-origin",
		Protocol: "shadowsocks", Listen: "::", Port: 22001,
		Settings: string(originSettings), Transport: string(transport), Security: string(security),
		ShareAddressMode: "auto", Enabled: 1, ConnIdleSeconds: 10, CreatedTime: now, UpdatedTime: now}
	if err := r.CreateNodeInbound(origin); err != nil {
		t.Fatal(err)
	}
	comp := &model.NodeInbound{KernelID: k.ID, NodeID: node.ID, Name: companionName(origin.Name),
		Protocol: "shadowsocks", Listen: "::", Port: 22002,
		Settings: string(compSettings), Transport: string(transport), Security: string(security),
		ShareAddressMode: "auto", Enabled: 1, ConnIdleSeconds: 10, CompanionOf: origin.ID,
		CreatedTime: now, UpdatedTime: now}
	if err := r.CreateNodeInbound(comp); err != nil {
		t.Fatal(err)
	}
	origin, err := r.GetNodeInbound(origin.ID)
	if err != nil || origin == nil {
		t.Fatalf("reload origin: %v", err)
	}
	comp, err = r.GetNodeInbound(comp.ID)
	if err != nil || comp == nil {
		t.Fatalf("reload companion: %v", err)
	}

	// Baseline: identical rows must be a no-op (validates the fixture
	// has no spurious diffs that would mask the connIdle leg).
	if h.syncCompanion(origin, comp, now) {
		t.Fatal("syncCompanion must no-op when origin and companion already agree")
	}

	// Drift only the companion's stored timeout (as if the origin was
	// edited 10 while the companion still holds the old value).
	if err := r.DB().Model(&model.NodeInbound{}).Where("id = ?", comp.ID).
		Update("conn_idle_seconds", 5).Error; err != nil {
		t.Fatal(err)
	}
	comp, err = r.GetNodeInbound(comp.ID)
	if err != nil || comp == nil {
		t.Fatalf("reload drifted companion: %v", err)
	}
	if comp.ConnIdleSeconds != 5 {
		t.Fatalf("drifted companion connIdleSeconds = %d, want 5", comp.ConnIdleSeconds)
	}
	if !h.syncCompanion(origin, comp, time.Now().UnixMilli()) {
		t.Fatal("syncCompanion must propagate the origin timeout change to the companion")
	}
	synced, err := r.GetNodeInbound(comp.ID)
	if err != nil || synced == nil {
		t.Fatalf("reload synced companion: %v", err)
	}
	if synced.ConnIdleSeconds != 10 {
		t.Fatalf("synced companion connIdleSeconds = %d, want 10", synced.ConnIdleSeconds)
	}
}
