package handler

import (
	"encoding/json"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
)

// connIdle "不限" (sentinel -1): the value must survive the same
// persist -> list -> render chain R20-X-02 pinned for ordinary values,
// and the SS metering companion mirror must propagate -1 <-> 0
// switches (the dirty-check compares ints, so no new code path is
// needed — but the switch itself is pinned here).

func TestConnIdleUnlimitedPersistsAndRenders(t *testing.T) {
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
		Security: string(security), Enabled: 1, ConnIdleSeconds: -1, CreatedTime: now, UpdatedTime: now}
	if err := r.CreateNodeInbound(row); err != nil {
		t.Fatal(err)
	}

	// List leg: the list view must show -1 (unlimited), not 0 or blank.
	listed, err := r.ListNodeInboundsByNode(node.ID)
	if err != nil || len(listed) != 1 {
		t.Fatalf("list: %v len=%d", err, len(listed))
	}
	if listed[0].ConnIdleSeconds != -1 {
		t.Fatalf("list connIdleSeconds = %d, want -1 (unlimited)", listed[0].ConnIdleSeconds)
	}

	// Render leg: the stored row must render policy {connIdle:31536000}.
	in, err := inboundFromModel(listed[0])
	if err != nil {
		t.Fatal(err)
	}
	out, err := kernel.Render(kernel.TypeXray, []kernel.Inbound{in})
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
	if l1["connIdle"] != float64(kernel.UnlimitedConnIdleSeconds) {
		t.Fatalf("rendered policy.levels[1].connIdle = %v, want %d", l1["connIdle"], kernel.UnlimitedConnIdleSeconds)
	}

	// Edit leg: -1 -> 10 through the same applyInboundToModel +
	// SaveNodeInbound path inboundSave uses, then back to -1.
	for _, want := range []int{10, -1} {
		stored, err := r.GetNodeInbound(row.ID)
		if err != nil || stored == nil {
			t.Fatalf("reload row: %v", err)
		}
		in, err := inboundFromModel(*stored)
		if err != nil {
			t.Fatal(err)
		}
		in.ConnIdleSeconds = want
		if err := applyInboundToModel(in, stored); err != nil {
			t.Fatal(err)
		}
		stored.UpdatedTime = time.Now().UnixMilli()
		if err := r.SaveNodeInbound(stored); err != nil {
			t.Fatal(err)
		}
		got, err := r.GetNodeInbound(row.ID)
		if err != nil || got == nil {
			t.Fatalf("reload after edit to %d: %v", want, err)
		}
		if got.ConnIdleSeconds != want {
			t.Fatalf("edited connIdleSeconds = %d, want %d", got.ConnIdleSeconds, want)
		}
	}
}

func TestConnIdleUnlimitedCompanionSyncSwitches(t *testing.T) {
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

	// -1 -> 0 and 0 -> -1 switches on the origin must both propagate.
	for _, want := range []int{-1, 0, -1} {
		if err := r.DB().Model(&model.NodeInbound{}).Where("id = ?", origin.ID).
			Update("conn_idle_seconds", want).Error; err != nil {
			t.Fatal(err)
		}
		origin, err := r.GetNodeInbound(origin.ID)
		if err != nil || origin == nil {
			t.Fatalf("reload origin: %v", err)
		}
		comp, err := r.GetNodeInbound(comp.ID)
		if err != nil || comp == nil {
			t.Fatalf("reload companion: %v", err)
		}
		if !h.syncCompanion(origin, comp, time.Now().UnixMilli()) {
			t.Fatalf("syncCompanion must propagate the origin timeout switch to %d", want)
		}
		synced, err := r.GetNodeInbound(comp.ID)
		if err != nil || synced == nil {
			t.Fatalf("reload synced companion: %v", err)
		}
		if synced.ConnIdleSeconds != want {
			t.Fatalf("synced companion connIdleSeconds = %d, want %d", synced.ConnIdleSeconds, want)
		}
	}
}
