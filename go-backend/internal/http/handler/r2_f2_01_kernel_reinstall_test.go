package handler

import (
	"encoding/json"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
)

// F2-01 回归：换版（存量内核行）此前无条件写 enabled:1——禁用
// 的内核换版即静默启用，且安装最终失败也已启用（副作用与成败
// 无关）。现存量分支只写 version＋updated_time，enabled 保持
// 原值；新建分支默认启用不变。
func TestR2F201KernelReinstallKeepsDisabled(t *testing.T) {
	a := newCleanupAgent(t)
	k := &model.NodeKernel{NodeID: 1, KernelType: kernel.TypeXray, Version: "26.10.20", Status: "pending", Enabled: 0,
		CreatedTime: time.Now().UnixMilli()}
	if err := a.h.repo.CreateNodeKernel(k); err != nil {
		t.Fatal(err)
	}

	code, _ := postKernelInstall(t, a.h, 1, kernel.TypeXray, "26.10.21")
	if code != 0 {
		t.Fatalf("install must succeed, code=%d", code)
	}
	after, err := a.h.repo.GetNodeKernel(k.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != "26.10.21" {
		t.Fatalf("version not updated: %q", after.Version)
	}
	if after.Enabled != 0 {
		t.Fatalf("disabled kernel must stay disabled after reinstall, enabled=%d", after.Enabled)
	}
	// agent 收到的 KernelApply 载荷同样须为 enabled=false（apply
	// 按落库行重读渲染）。
	cmds := a.commandsOfType("KernelApply")
	if len(cmds) == 0 {
		t.Fatalf("KernelApply command was never sent")
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(cmds[0].Data, &payload); err != nil {
		t.Fatalf("decode KernelApply payload: %v", err)
	}
	if enabled, _ := payload["enabled"].(bool); enabled {
		t.Fatalf("KernelApply payload must carry enabled=false, got %v", payload["enabled"])
	}
}
