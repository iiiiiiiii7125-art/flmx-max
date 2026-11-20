package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"go-backend/internal/security"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
	"go-backend/internal/ws"
)

// R18-R2-F1-01: best-exit reorder must not downgrade a verified
// dialer to legacy. applyBestExitChainOrder used to pass
// dialerGroup=nil into buildTunnelChainConfig, whose dialer gate
// requires a non-empty group — so every reorder re-rendered the
// dialer without a "tls" block and pushed it over the running
// verified chain, while the peer listener stayed verified
// (RequireAndVerifyClientCert): the legacy dialer has no client
// certificate and the leg breaks until the next full redeploy.
// The fix passes the full dialer group (the owner's own group:
// the entry nodes when there are no middle hops, the last hop
// otherwise) into the reorder path, and the reorder refuses to
// push when it cannot preserve verification (unknown group, ready
// set unreadable, or a previously-issued leg member not ready).

// bestExitReorderAgent is a fake-agent harness for the reorder
// tests: it connects the given nodes over the real ws transport
// and records every command the panel pushes, so a best-exit
// reorder's pushed chain config can be inspected. connected nodes
// get a live agent connection; dbOnly nodes exist only as DB rows
// (they are dial targets, never push recipients).
type bestExitReorderAgent struct {
	h        *Handler
	mu       sync.Mutex
	commands []cleanupAgentCommand
}

func newBestExitReorderAgent(t *testing.T, connected, dbOnly []int64) *bestExitReorderAgent {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "best-exit-reorder.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	all := append(append([]int64{}, connected...), dbOnly...)
	for _, id := range all {
		secret := fmt.Sprintf("reorder-node-%d-secret", id)
		if err := r.DB().Create(&model.Node{ID: id, Name: fmt.Sprintf("node-%d", id), Secret: secret, ServerIP: fmt.Sprintf("192.0.2.%d", id), Port: "30000", Status: 1}).Error; err != nil {
			t.Fatal(err)
		}
	}
	server := ws.NewServer(r, "test-jwt-secret")
	online := make(chan struct{})
	var onlineOnce sync.Once
	var onlineMu sync.Mutex
	onlineCount := 0
	server.SetNodeOnlineHook(func(int64) {
		onlineMu.Lock()
		onlineCount++
		if onlineCount == len(connected) {
			onlineOnce.Do(func() { close(online) })
		}
		onlineMu.Unlock()
	})
	var serverDoneOnce sync.Once
	serverDone := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		defer serverDoneOnce.Do(func() { close(serverDone) })
		server.ServeHTTP(w, req)
	}))
	t.Cleanup(ts.Close)
	a := &bestExitReorderAgent{h: &Handler{repo: r, wsServer: server, jwtSecret: "unit-test-secret", bestExit: newBestExitManager()}}
	for _, id := range connected {
		secret := fmt.Sprintf("reorder-node-%d-secret", id)
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+fmt.Sprintf("?type=1&secret=%s", secret), nil)
		if err != nil {
			t.Fatal(err)
		}
		crypto, err := security.NewAESCrypto(secret)
		if err != nil {
			t.Fatal(err)
		}
		readerDone := make(chan struct{})
		go func(conn *websocket.Conn, crypto *security.AESCrypto) {
			defer close(readerDone)
			for {
				_, payload, err := conn.ReadMessage()
				if err != nil {
					return
				}
				var envelope struct {
					Encrypted bool   `json:"encrypted"`
					Data      string `json:"data"`
				}
				if err := json.Unmarshal(payload, &envelope); err != nil {
					t.Errorf("decode command envelope: %v", err)
					return
				}
				if envelope.Encrypted {
					payload, err = crypto.Decrypt(envelope.Data)
					if err != nil {
						t.Errorf("decrypt command: %v", err)
						return
					}
				}
				var command cleanupAgentCommand
				if err := json.Unmarshal(payload, &command); err != nil {
					t.Errorf("decode command: %v", err)
					return
				}
				a.mu.Lock()
				a.commands = append(a.commands, command)
				a.mu.Unlock()
				if err := conn.WriteJSON(map[string]interface{}{"type": command.Type, "requestId": command.RequestID, "success": true}); err != nil {
					return
				}
			}
		}(conn, crypto)
		t.Cleanup(func() {
			_ = conn.Close()
			<-readerDone
		})
	}
	select {
	case <-online:
	case <-time.After(3 * time.Second):
		t.Fatal("mock nodes did not come online")
	}
	for _, id := range connected {
		if _, err := a.h.wsServer.SendCommand(id, "CleanupTestProbe", nil, time.Second); err != nil {
			t.Fatalf("mock agent transport unavailable for node %d: %v", id, err)
		}
	}
	return a
}

func (a *bestExitReorderAgent) commandsOfType(commandType string) []cleanupAgentCommand {
	a.mu.Lock()
	defer a.mu.Unlock()
	var commands []cleanupAgentCommand
	for _, command := range a.commands {
		if command.Type == commandType {
			commands = append(commands, command)
		}
	}
	return commands
}

func seedBestExitReorderTunnel(t *testing.T, a *bestExitReorderAgent, tunnelID int64) {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := a.h.repo.DB().Exec(`
		INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, inx, ip_preference, probe_target_host, probe_target_port)
		VALUES(?, ?, 1, 2, 'tls', 1, ?, ?, 1, ?, '', '', 0)
	`, tunnelID, fmt.Sprintf("r1-f1-01-%d", tunnelID), now, now, tunnelID).Error; err != nil {
		t.Fatalf("insert tunnel: %v", err)
	}
}

func markBestExitReorderReady(t *testing.T, a *bestExitReorderAgent, ids ...int64) {
	t.Helper()
	for _, id := range ids {
		if err := a.h.repo.MarkNodeTunnelTLSReady(id, "ca-"+fmt.Sprint(id), "cert-"+fmt.Sprint(id), fmt.Sprintf("node-%d.flmx.internal", id), time.Now().Unix()); err != nil {
			t.Fatalf("mark node %d ready: %v", id, err)
		}
	}
}

// fakeBestExitReorderPinger scores exit 2 far ahead of exit 4
// (owner→exit 10ms vs 100ms, public leg 10ms each), so a switch
// from the initial applied exit 4 to exit 2 is confirmed after
// three rounds.
func fakeBestExitReorderPinger(nodeID int64, ip string, port int, options diagnosisExecOptions) (float64, float64, error) {
	switch ip {
	case "192.0.2.2":
		return 10, 0, nil
	case "192.0.2.4":
		return 100, 0, nil
	default:
		return 10, 0, nil
	}
}

// driveBestExitReorderSwitch runs three probe rounds for the given
// owners (pre-seeded as applied to exit 4 long enough ago that the
// 30s switch cooldown has expired) and returns the UpdateChains
// commands the panel pushed.
func driveBestExitReorderSwitch(t *testing.T, a *bestExitReorderAgent, tunnelID int64, inNodes []chainNodeRecord, chainHops [][]chainNodeRecord, outNodes []chainNodeRecord, owners []chainNodeRecord) []cleanupAgentCommand {
	t.Helper()
	for _, owner := range owners {
		a.h.bestExit.ensureApplied(bestExitOwnerKey{TunnelID: tunnelID, OwnerNodeID: owner.NodeID}, 4, time.Now().Add(-31*time.Second))
	}
	p := &tunnelQualityProber{handler: a.h}
	for i := 0; i < 3; i++ {
		p.probeBestExitOwners(tunnelID, inNodes, chainHops, outNodes, "", diagnosisExecOptions{}, defaultTunnelProbeTarget(), fakeBestExitReorderPinger, "tls")
	}
	return a.commandsOfType("UpdateChains")
}

// reorderChainDataOf extracts the pushed chain config from one
// UpdateChains command payload ({"chain": ..., "data": {...}}).
func reorderChainDataOf(t *testing.T, cmd cleanupAgentCommand) map[string]interface{} {
	t.Helper()
	var payload struct {
		Chain string                 `json:"chain"`
		Data  map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(cmd.Data, &payload); err != nil {
		t.Fatalf("decode UpdateChains payload: %v", err)
	}
	if payload.Data == nil {
		t.Fatalf("UpdateChains payload has no chain data: %s", string(cmd.Data))
	}
	return payload.Data
}

// reorderDialerTLSBlockOf extracts the "tls" block of one dialer
// leg from a chain config that round-tripped through JSON (the
// pushed UpdateChains payload: hops/nodes decode as []interface{},
// unlike the in-memory shape the D-3 dialerTLSBlockOf helper
// expects). Nil when the leg renders legacy (no "tls" key).
func reorderDialerTLSBlockOf(t *testing.T, chainCfg map[string]interface{}, idx int) map[string]interface{} {
	t.Helper()
	hops, ok := chainCfg["hops"].([]interface{})
	if !ok || len(hops) == 0 {
		t.Fatalf("chain has no hops: %#v", chainCfg)
	}
	hop, _ := hops[0].(map[string]interface{})
	if hop == nil {
		t.Fatalf("chain hop 0 is not an object: %#v", chainCfg)
	}
	nodes, ok := hop["nodes"].([]interface{})
	if !ok || idx < 0 || idx >= len(nodes) {
		t.Fatalf("chain has no node %d: %#v", idx, chainCfg)
	}
	node, _ := nodes[idx].(map[string]interface{})
	if node == nil {
		t.Fatalf("chain node %d is not an object: %#v", idx, chainCfg)
	}
	dialer, _ := node["dialer"].(map[string]interface{})
	if dialer == nil {
		t.Fatalf("node %d has no dialer: %#v", idx, node)
	}
	tls, _ := dialer["tls"].(map[string]interface{})
	return tls
}

// assertReorderedLegsVerified checks that every leg of a pushed
// reorder chain still carries verified TLS for the expected exit
// order (best first).
func assertReorderedLegsVerified(t *testing.T, chainData map[string]interface{}, wantExits []int64) {
	t.Helper()
	for idx, wantNode := range wantExits {
		tlsBlock := reorderDialerTLSBlockOf(t, chainData, idx)
		if tlsBlock == nil {
			t.Fatalf("reordered chain leg %d (exit %d) must keep verified TLS, got legacy (no tls block)", idx, wantNode)
		}
		if tlsBlock["secure"] != true || tlsBlock["serverName"] != fmt.Sprintf("node-%d.flmx.internal", wantNode) {
			t.Fatalf("reordered chain leg %d tls block = %v, want secure=true serverName=node-%d.flmx.internal", idx, tlsBlock, wantNode)
		}
	}
}

// TestBestExitReorderKeepsVerifiedDialer (R18-R2-F1-01): a verified
// TLS tunnel (2 entries, 2 best exits, all nodes TLS-ready) whose
// best exit switches must keep verified TLS on the reordered
// dialer — both legs — and the exit listener rendered from the
// same ready set must agree (segment ends consistent).
func TestBestExitReorderKeepsVerifiedDialer(t *testing.T) {
	a := newBestExitReorderAgent(t, []int64{1, 3}, []int64{2, 4})
	seedBestExitReorderTunnel(t, a, 78)
	markBestExitReorderReady(t, a, 1, 2, 3, 4)
	inNodes := []chainNodeRecord{
		{NodeID: 1, Protocol: "tls", Strategy: "best"},
		{NodeID: 3, Protocol: "tls", Strategy: "best"},
	}
	outNodes := []chainNodeRecord{
		{NodeID: 4, Protocol: "tls", Strategy: "best", Port: 24002},
		{NodeID: 2, Protocol: "tls", Strategy: "best", Port: 24001},
	}
	cmds := driveBestExitReorderSwitch(t, a, 78, inNodes, nil, outNodes, inNodes)
	if len(cmds) != 2 {
		t.Fatalf("expected 2 UpdateChains (one per entry owner), got %d", len(cmds))
	}
	for _, cmd := range cmds {
		assertReorderedLegsVerified(t, reorderChainDataOf(t, cmd), []int64{2, 4})
	}
	// Segment ends agree: the exit-2 listener rendered from the
	// same ready set (dialers = entries 1, 3) must also be verified.
	ready := a.h.tunnelTLSReadyMap([]int64{1, 2, 3, 4})
	node2, err := a.h.getNodeRecord(2)
	if err != nil {
		t.Fatalf("get exit node 2: %v", err)
	}
	svcs := a.h.buildTunnelChainServiceConfig(78, tunnelRuntimeNode{NodeID: 2, Protocol: "tls", Port: 24001, ChainType: 2}, node2, 1, ready, []int64{1, 3}, "tls")
	if got := listenerTLSBlockOf(t, svcs); got == nil {
		t.Fatal("exit listener must render verified TLS to match the reordered dialer")
	}
}

// TestBestExitReorderKeepsVerifiedDialerWithMiddleHop
// (R18-R2-F1-01): with a middle hop, the reorder owner is the
// last-hop node and its dialer group is that hop — the reordered
// chain must still keep verified TLS on both legs.
func TestBestExitReorderKeepsVerifiedDialerWithMiddleHop(t *testing.T) {
	a := newBestExitReorderAgent(t, []int64{1, 5}, []int64{2, 4})
	seedBestExitReorderTunnel(t, a, 78)
	markBestExitReorderReady(t, a, 1, 2, 4, 5)
	inNodes := []chainNodeRecord{{NodeID: 1, Protocol: "tls", Strategy: "best"}}
	chainHops := [][]chainNodeRecord{{{NodeID: 5, Protocol: "tls", Strategy: "best", Port: 24005}}}
	outNodes := []chainNodeRecord{
		{NodeID: 4, Protocol: "tls", Strategy: "best", Port: 24002},
		{NodeID: 2, Protocol: "tls", Strategy: "best", Port: 24001},
	}
	owners := []chainNodeRecord{{NodeID: 5, Protocol: "tls", Strategy: "best"}}
	cmds := driveBestExitReorderSwitch(t, a, 78, inNodes, chainHops, outNodes, owners)
	if len(cmds) != 1 {
		t.Fatalf("expected 1 UpdateChains (last-hop owner only), got %d", len(cmds))
	}
	assertReorderedLegsVerified(t, reorderChainDataOf(t, cmds[0]), []int64{2, 4})
	// The exit-2 listener's dialers are the last-hop nodes ([5]).
	ready := a.h.tunnelTLSReadyMap([]int64{1, 2, 4, 5})
	node2, err := a.h.getNodeRecord(2)
	if err != nil {
		t.Fatalf("get exit node 2: %v", err)
	}
	svcs := a.h.buildTunnelChainServiceConfig(78, tunnelRuntimeNode{NodeID: 2, Protocol: "tls", Port: 24001, ChainType: 2}, node2, 1, ready, []int64{5}, "tls")
	if got := listenerTLSBlockOf(t, svcs); got == nil {
		t.Fatal("exit listener must render verified TLS to match the reordered dialer")
	}
}

// TestBestExitReorderRefusesDowngradeWhenDialerNotReady
// (R18-R2-F1-01, fail-closed): when a previously-issued dialer
// group member is no longer ready, the reorder must refuse to
// push anything — a legacy dialer pushed while the peer listener
// is still verified would be rejected for lack of a client
// certificate. The switch decision must still fire (the prober
// records the apply failure and retries later); only the push is
// refused.
func TestBestExitReorderRefusesDowngradeWhenDialerNotReady(t *testing.T) {
	a := newBestExitReorderAgent(t, []int64{1, 3}, []int64{2, 4})
	seedBestExitReorderTunnel(t, a, 78)
	markBestExitReorderReady(t, a, 1, 2, 3, 4)
	if err := a.h.repo.ClearNodeTunnelTLSReady(3); err != nil {
		t.Fatalf("clear node 3 ready: %v", err)
	}
	inNodes := []chainNodeRecord{
		{NodeID: 1, Protocol: "tls", Strategy: "best"},
		{NodeID: 3, Protocol: "tls", Strategy: "best"},
	}
	outNodes := []chainNodeRecord{
		{NodeID: 4, Protocol: "tls", Strategy: "best", Port: 24002},
		{NodeID: 2, Protocol: "tls", Strategy: "best", Port: 24001},
	}
	cmds := driveBestExitReorderSwitch(t, a, 78, inNodes, nil, outNodes, inNodes)
	if len(cmds) != 0 {
		t.Fatalf("refused reorder must push nothing, got %d UpdateChains", len(cmds))
	}
	snap, ok := a.h.bestExit.snapshot(bestExitOwnerKey{TunnelID: 78, OwnerNodeID: 1})
	if !ok {
		t.Fatal("best-exit decision missing for owner 1 (the switch never fired)")
	}
	if snap.Reason != "apply retry cooldown" {
		t.Fatalf("snapshot reason = %q, want %q (switch fired, apply refused and recorded)", snap.Reason, "apply retry cooldown")
	}
}
