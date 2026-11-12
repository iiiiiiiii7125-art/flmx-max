package handler

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// seedU5Inbound 直接落一行入站，供 payload 名单断言用。
func seedU5Inbound(t *testing.T, r *repo.Repository, k *model.NodeKernel, name string, port int, enabled bool, companionOf int64) *model.NodeInbound {
	t.Helper()
	in := kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: port,
		Security: kernel.Security{Type: kernel.SecurityNone}}
	if err := kernel.Normalize(k.KernelType, &in); err != nil {
		t.Fatal(err)
	}
	row := model.NodeInbound{KernelID: k.ID, NodeID: k.NodeID, Name: name,
		CompanionOf: companionOf, CreatedTime: time.Now().UnixMilli()}
	if enabled {
		row.Enabled = 1
	}
	if err := applyInboundToModel(in, &row); err != nil {
		t.Fatal(err)
	}
	if err := r.CreateNodeInbound(&row); err != nil {
		t.Fatal(err)
	}
	return &row
}

// payloadConfigJSON 从 KernelApply 载荷里取渲染出的 config.json 并解析。
func payloadConfigJSON(t *testing.T, payload map[string]interface{}) map[string]interface{} {
	t.Helper()
	files, ok := payload["files"].([]kernel.File)
	if !ok {
		t.Fatalf("files type = %T, want []kernel.File", payload["files"])
	}
	for _, f := range files {
		if f.Name == "config.json" {
			var parsed map[string]interface{}
			if err := json.Unmarshal([]byte(f.Content), &parsed); err != nil {
				t.Fatalf("config.json invalid: %v", err)
			}
			return parsed
		}
	}
	t.Fatal("config.json not in payload files")
	return nil
}

// U5↔U4 契约（节点开关版）：KernelApply 载荷的 connLogInbounds 恰为
// 渲染集中合格的入站——节点开关开时，启用、非伴生的直连入站全入
// 列（逐入站开关已撤，采集门控只剩节点开关）；已停用、伴生两类
// 不得入列；开关关时键整体缺席（agent 解码为空集、采集器停用）。
// connlogEntries（直连入口标记）与日志形态（Xray access /
// sing-box debug）受同一门控。
func TestBuildKernelApplyPayloadConnLogInbounds(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/connlog-u5.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	node := seedRelayTargetNode(t, r, "u5-node", "198.51.100.30")
	k := seedChainKernel(t, r, node, kernel.TypeXray)

	first := seedU5Inbound(t, r, k, "first", 8443, true, 0)
	plain := seedU5Inbound(t, r, k, "plain", 8444, true, 0)
	seedU5Inbound(t, r, k, "disabled", 8445, false, 0)
	seedU5Inbound(t, r, k, "companion", 8446, true, first.ID)

	// 默认关（节点开关默认关）：名单与入口标记皆空，渲染回落。
	payload, _, err := h.buildKernelApplyPayload(k)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["connLogInbounds"]; ok {
		t.Fatalf("connLogInbounds must be absent when the node switch is off, got %v", payload["connLogInbounds"])
	}
	entries, ok := payload["connlogEntries"].([]map[string]interface{})
	if !ok || len(entries) != 0 {
		t.Fatalf("connlogEntries = %v, want empty when the node switch is off", payload["connlogEntries"])
	}
	if _, present := payloadConfigJSON(t, payload)["log"].(map[string]interface{})["access"]; present {
		t.Fatal("node off: log.access must be absent (must not open the access log)")
	}

	// 只开节点开关（逐入站开关不存在）：两启用直连入站入列，
	// 停用与伴生除外；入口标记与日志形态同步抬升。
	if err := r.UpdateNodeConnLogEnabled(node, 1); err != nil {
		t.Fatal(err)
	}
	payload, _, err = h.buildKernelApplyPayload(k)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := payload["connLogInbounds"].([]int64)
	if !ok {
		t.Fatalf("connLogInbounds missing or wrong type: %T %v", payload["connLogInbounds"], payload["connLogInbounds"])
	}
	if want := []int64{first.ID, plain.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("connLogInbounds = %v, want %v", got, want)
	}
	entries, ok = payload["connlogEntries"].([]map[string]interface{})
	if !ok || len(entries) != 2 {
		t.Fatalf("connlogEntries = %v, want the 2 direct entries", payload["connlogEntries"])
	}
	// access 路径是内核包的内部口径：只断言键存在且非空。
	if got := payloadConfigJSON(t, payload)["log"].(map[string]interface{})["access"]; got == nil || got == "" {
		t.Fatalf("node on: log.access = %v, want the access log open", got)
	}

	// sing-box 内核同门控：开关开时入列且升 debug；关时收敛。
	k2 := seedChainKernel(t, r, node, kernel.TypeSingbox)
	sb := seedU5Inbound(t, r, k2, "plain-sb", 9443, true, 0)
	payload2, _, err := h.buildKernelApplyPayload(k2)
	if err != nil {
		t.Fatal(err)
	}
	got2, ok := payload2["connLogInbounds"].([]int64)
	if !ok || !reflect.DeepEqual(got2, []int64{sb.ID}) {
		t.Fatalf("singbox connLogInbounds = %v, want [%d]", payload2["connLogInbounds"], sb.ID)
	}
	if lvl := payloadConfigJSON(t, payload2)["log"].(map[string]interface{})["level"]; lvl != "debug" {
		t.Fatalf("singbox node on: log.level = %v, want debug", lvl)
	}
	if err := r.UpdateNodeConnLogEnabled(node, 0); err != nil {
		t.Fatal(err)
	}
	payload2, _, err = h.buildKernelApplyPayload(k2)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := payload2["connLogInbounds"]; ok {
		t.Fatalf("connLogInbounds must be absent when the node switch is off, got %v", payload2["connLogInbounds"])
	}
	if lvl := payloadConfigJSON(t, payload2)["log"].(map[string]interface{})["level"]; lvl != "warn" {
		t.Fatalf("singbox node off: log.level = %v, want warn", lvl)
	}
}
