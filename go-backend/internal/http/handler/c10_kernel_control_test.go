package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go-backend/internal/kernel"
	"go-backend/internal/store/repo"
)

// C10 簇回归（B11-002 / B11-004）：
//   - B11-002：kernel/control 对离线节点先落库 enabled 再下发，下发失败时
//     回执必须与 kernelInstall 的部分成功口径一致（明示状态已更新、仅下发
//     失败），不得只回原始错误（如「节点不在线」）让管理员误判未生效。
//   - B11-004：kernel/capabilities 与兄弟 kernel 端点统一 POST-only，
//     非 POST 返回信封化「请求失败」，POST 正常返回目录。

type c10Envelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

func c10CallControl(t *testing.T, h *Handler, kernelID int64, action string) c10Envelope {
	t.Helper()
	res := httptest.NewRecorder()
	h.kernelControl(res, newAuthenticatedJSONRequest(t, map[string]any{"id": kernelID, "action": action}))
	var env c10Envelope
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode kernel control response: %v body=%s", err, res.Body.String())
	}
	return env
}

func TestC10KernelControlOfflineReportsStateSaved(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/c10-kernel-control.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	node := seedRelayTargetNode(t, r, "offline-node", "198.51.100.7")
	k := seedChainKernel(t, r, node, kernel.TypeXray) // Enabled=1，节点从未上线 → apply 必然失败

	// stop：下发失败，但回执须明示启停状态已更新，且 enabled 已落库为 0。
	env := c10CallControl(t, h, k.ID, "stop")
	if env.Code == 0 {
		t.Fatalf("offline control stop must report the apply failure, got success")
	}
	if !strings.Contains(env.Msg, "已更新启停状态") || !strings.Contains(env.Msg, "下发失败") {
		t.Fatalf("stop receipt must state the enabled flag was saved and only the apply failed, got msg=%q", env.Msg)
	}
	row, err := h.repo.GetNodeKernel(k.ID)
	if err != nil || row == nil {
		t.Fatalf("kernel row missing after stop: %v", err)
	}
	if row.Enabled != 0 {
		t.Fatalf("stop must persist enabled=0 despite apply failure, got enabled=%d", row.Enabled)
	}

	// start：同口径——回执明示已更新，enabled 落库为 1。
	env = c10CallControl(t, h, k.ID, "start")
	if env.Code == 0 {
		t.Fatalf("offline control start must report the apply failure, got success")
	}
	if !strings.Contains(env.Msg, "已更新启停状态") || !strings.Contains(env.Msg, "下发失败") {
		t.Fatalf("start receipt must state the enabled flag was saved and only the apply failed, got msg=%q", env.Msg)
	}
	row, err = h.repo.GetNodeKernel(k.ID)
	if err != nil || row == nil {
		t.Fatalf("kernel row missing after start: %v", err)
	}
	if row.Enabled != 1 {
		t.Fatalf("start must persist enabled=1 despite apply failure, got enabled=%d", row.Enabled)
	}
}

func TestC10KernelCapabilitiesPostOnly(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/c10-kernel-capabilities.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	// GET（及其余非 POST 方法）与兄弟端点同口径：信封化「请求失败」。
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		res := httptest.NewRecorder()
		h.kernelCapabilities(res, httptest.NewRequest(method, "/api/v1/kernel/capabilities", nil))
		var env c10Envelope
		if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
			t.Fatalf("%s: decode response: %v body=%s", method, err, res.Body.String())
		}
		if env.Code == 0 {
			t.Fatalf("%s capabilities must be rejected (POST-only), got success body=%s", method, res.Body.String())
		}
		if env.Msg != "请求失败" {
			t.Fatalf("%s capabilities rejection must use the sibling wording 请求失败, got msg=%q", method, env.Msg)
		}
	}

	// POST 正常返回静态目录。
	res := httptest.NewRecorder()
	h.kernelCapabilities(res, httptest.NewRequest(http.MethodPost, "/api/v1/kernel/capabilities", nil))
	var env c10Envelope
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode POST capabilities response: %v body=%s", err, res.Body.String())
	}
	if env.Code != 0 {
		t.Fatalf("POST capabilities must succeed, got code=%d msg=%q", env.Code, env.Msg)
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("decode capabilities data: %v", err)
	}
	if _, ok := data["kernels"]; !ok {
		t.Fatalf("POST capabilities data must contain the kernel catalog, got %s", env.Data)
	}
}
