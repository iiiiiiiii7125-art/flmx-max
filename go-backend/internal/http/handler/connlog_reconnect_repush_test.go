package handler

import (
	"path/filepath"
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
// failure, so the existing 30s retry (scheduleNodeLocalRuntimeRetry)
// runs and the failure is logged with the node id.

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
// 节点无 live 连接时重推须报失败（基线红：直接回 true，静默成功）。
func TestConnlogReconnectRepush_OfflineSkipIsNotSuccess(t *testing.T) {
	a := newOfflineRedeployAgent(t)
	seedR19F03Forward(t, a, 7, 0)

	if ok := a.h.redeployLocalNodeRuntime(1); ok {
		t.Fatalf("redeployLocalNodeRuntime must report failure when the node's own forward is skipped offline, got success")
	}
	if got := len(a.commandsOfType("UpdateService")); got != 0 {
		t.Fatalf("expected no UpdateService to reach an offline node, got %d", got)
	}
}

// TestConnlogReconnectRepush_OnlineRedeployStillSucceeds —— 反向守卫。
// 在线节点重推成功时仍须报成功（警告匹配不得把正常下发误判为跳过）。
func TestConnlogReconnectRepush_OnlineRedeployStillSucceeds(t *testing.T) {
	a := newCleanupAgent(t)
	seedR19F03Forward(t, a, 7, 0)

	if ok := a.h.redeployLocalNodeRuntime(1); !ok {
		t.Fatalf("redeployLocalNodeRuntime should succeed for an online node, got false")
	}
	if got := len(a.commandsOfType("UpdateService")); got == 0 {
		t.Fatalf("expected UpdateService to be sent on redeploy")
	}
}
