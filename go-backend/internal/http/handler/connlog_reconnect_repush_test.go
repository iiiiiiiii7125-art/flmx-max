package handler

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
	"go-backend/internal/ws"
)

// Lane A (connlog repush on connect): the reconnect redeploy must not
// report success when the node's own forward was skipped.
//
// Mechanism: syncForwardServicesWithWarnings turns an offline/timeout
// send into a warning ("节点 <name> 不在线，已跳过下发") and returns
// nil error, so redeployLocalNodeRuntime used to treat a fully skipped
// push as success — onNodeOnline then cleared the retry queue and no
// retry, no failure log, ever followed. The agent keeps its stale
// gost.json (no connlog mark) until the next toggle/reconnect. The
// fix: a skip of this node's own forward makes the redeploy report
// pending (R5-B-01: errRedeployPending, not a failure), so the
// existing 30s retry (scheduleNodeLocalRuntimeRetry) runs and the
// skip is logged with the node id.

// newOfflineRedeployAgent builds a handler + ws server with NO agent
// connected for node 1 (DB status stays 1, as it is between the panel
// marking the node online and the agent actually answering commands).
// seedR19F03Forward only touches the repo, so it works on this harness.
func newOfflineRedeployAgent(t *testing.T) *cleanupAgent {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "connlog-repush-offline.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	const secret = "connlog-repush-offline-node"
	if err := r.DB().Create(&model.Node{ID: 1, Name: "relay", Secret: secret, ServerIP: "127.0.0.1", Port: "30000", Status: 1}).Error; err != nil {
		t.Fatal(err)
	}
	server := ws.NewServer(r, "test-jwt-secret")
	return &cleanupAgent{h: &Handler{repo: r, wsServer: server}}
}

// TestConnlogReconnectRepush_OfflineSkipIsNotSuccess —— 主回归。
// 节点无 live 连接时重推须报 pending（已排队、非失败）：下发未送达、
// 30 秒重试已排队、重推幂等——按 F1 的 pending/失败分流口径，
// 「重新下发失败」是误报。旧口径（bool false → 失败）基线红：
// 回执 code -1「重新下发失败」。
func TestConnlogReconnectRepush_OfflineSkipIsNotSuccess(t *testing.T) {
	a := newOfflineRedeployAgent(t)
	seedR19F03Forward(t, a, 7, 0)

	if err := a.h.redeployLocalNodeRuntime(1); !errors.Is(err, errRedeployPending) {
		t.Fatalf("redeployLocalNodeRuntime must report pending when the node's own forward is skipped offline, got %v", err)
	}
	if got := len(a.commandsOfType("UpdateService")); got != 0 {
		t.Fatalf("expected no UpdateService to reach an offline node, got %d", got)
	}
}

// TestConnlogRepushReceipt_OfflineSkipIsPendingNotFailure ——
// R5-B-01 回执面：离线跳过时一键重推回 code 0＋排队提示，绝不
// 缀「下发失败」。
func TestConnlogRepushReceipt_OfflineSkipIsPendingNotFailure(t *testing.T) {
	a := newOfflineRedeployAgent(t)
	seedR19F03Forward(t, a, 7, 0)

	body := r2b2AuthPost(t, a.h.nodeConnlogRepush, "/api/v1/node/connlog-repush", map[string]interface{}{"nodeId": 1}, "")
	var env struct {
		Code    int                    `json:"code"`
		Msg     string                 `json:"msg"`
		Warning string                 `json:"warning"`
		Data    map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("decode receipt: %v (body=%s)", err, body)
	}
	if env.Code != 0 {
		t.Fatalf("pending repush receipt code = %d, want 0 (body=%s)", env.Code, body)
	}
	// 排队提示走信封 warning 字段（OKWithWarning 口径，与
	// config/forward 各面的警告回执一致），不在 msg 里。
	if !strings.Contains(env.Warning, "已排队") || !strings.Contains(env.Warning, "30 秒后自动重试") {
		t.Fatalf("pending repush receipt warning = %q, want it to say queued for retry in 30s (body=%s)", env.Warning, body)
	}
	if strings.Contains(env.Warning, "下发失败") || strings.Contains(env.Msg, "重新下发失败") {
		t.Fatalf("pending repush receipt must never say 下发失败: warning=%q msg=%q", env.Warning, env.Msg)
	}
	if env.Data["redeployed"] != false {
		t.Fatalf("pending repush receipt redeployed = %v, want false (body=%s)", env.Data["redeployed"], body)
	}
}

// TestConnlogReconnectRepush_OnlineRedeployStillSucceeds —— 反向守卫。
// 在线节点重推成功时仍须报成功（警告匹配不得把正常下发误判为跳过）。
func TestConnlogReconnectRepush_OnlineRedeployStillSucceeds(t *testing.T) {
	a := newCleanupAgent(t)
	seedR19F03Forward(t, a, 7, 0)

	if err := a.h.redeployLocalNodeRuntime(1); err != nil {
		t.Fatalf("redeployLocalNodeRuntime should succeed for an online node, got %v", err)
	}
	if got := len(a.commandsOfType("UpdateService")); got == 0 {
		t.Fatalf("expected UpdateService to be sent on redeploy")
	}
}
