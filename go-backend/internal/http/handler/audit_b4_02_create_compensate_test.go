package handler

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"go-backend/internal/security"
	"go-backend/internal/store/repo"
	"go-backend/internal/ws"
)

// R18-B4-02: forward create over a two-entry gost tunnel where entry A
// accepts UpdateService and entry B hard-fails it must not leave a live
// service on A behind a "create failed" receipt and a deleted DB row.
// The compensation must DeleteService the already-pushed node first
// (and say so when the cleanup itself cannot complete).

type b402Recorder struct {
	mu   sync.Mutex
	cmds []cleanupAgentCommand
}

func (rc *b402Recorder) snapshot() []cleanupAgentCommand {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return append([]cleanupAgentCommand(nil), rc.cmds...)
}

func (rc *b402Recorder) sawCommand(cmdType string) bool {
	for _, c := range rc.snapshot() {
		if c.Type == cmdType {
			return true
		}
	}
	return false
}

// connectB402Agent dials a mock agent for the node with the given secret.
// When failHard is true every command is answered success:false with a
// hard (non-offline, non-timeout) agent error.
func connectB402Agent(t *testing.T, ts *httptest.Server, secret string, rc *b402Recorder, failHard bool) {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"?type=1&secret="+secret, nil)
	if err != nil {
		t.Fatalf("dial mock agent: %v", err)
	}
	crypto, err := security.NewAESCrypto(secret)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
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
				return
			}
			if envelope.Encrypted {
				payload, err = crypto.Decrypt(envelope.Data)
				if err != nil {
					return
				}
			}
			var command cleanupAgentCommand
			if err := json.Unmarshal(payload, &command); err != nil {
				return
			}
			rc.mu.Lock()
			rc.cmds = append(rc.cmds, command)
			rc.mu.Unlock()
			reply := map[string]interface{}{"type": command.Type, "requestId": command.RequestID, "success": true}
			if failHard {
				reply["success"] = false
				reply["message"] = "agent rejected: bind failed"
			}
			if err := conn.WriteJSON(reply); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = conn.Close()
		<-done
	})
}

func TestR18B402CreateSyncFailureCleansPushedService(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/b4-02.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	server := ws.NewServer(r, "test-secret")
	server.SetNodeOnlineHook(h.onNodeOnline)
	server.SetNodeOfflineHook(h.notifyNodeOffline)
	h.wsServer = server
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	now := time.Now().UnixMilli()
	if _, err := r.CreateUser("admin", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now); err != nil {
		t.Fatalf("create user: %v", err)
	}
	nodeA := seedRelayTargetNode(t, r, "b402-a", "198.51.100.21")
	nodeB := seedRelayTargetNode(t, r, "b402-b", "198.51.100.22")
	tunnelID := seedTunnelForNftables(t, h, "b402-tunnel", nodeA)
	if err := r.DB().Exec(`INSERT INTO chain_tunnel(tunnel_id, chain_type, node_id, port, strategy, inx, protocol) VALUES(?, 1, ?, NULL, 'round', 2, 'tls')`, tunnelID, nodeB).Error; err != nil {
		t.Fatalf("seed second entry: %v", err)
	}
	if err := r.DB().Exec(`
		INSERT INTO user_tunnel(user_id, tunnel_id, speed_id, num, flow, in_flow, out_flow, flow_reset_time, exp_time, status)
		VALUES(1, ?, NULL, 99999, 99999, 0, 0, 1, 2727251700000, 1)
	`, tunnelID).Error; err != nil {
		t.Fatalf("seed user_tunnel: %v", err)
	}

	rcA := &b402Recorder{}
	rcB := &b402Recorder{}
	connectB402Agent(t, ts, "b402-a-secret", rcA, false)
	connectB402Agent(t, ts, "b402-b-secret", rcB, true)
	waitForCondition(t, 5*time.Second, func() bool {
		return server.IsNodeOnline(nodeA) && server.IsNodeOnline(nodeB)
	}, "mock agents online")

	res := httptest.NewRecorder()
	h.forwardCreate(res, newAuthenticatedJSONRequest(t, map[string]interface{}{
		"tunnelId":   tunnelID,
		"name":       "b402-forward",
		"remoteAddr": "203.0.113.5:443",
		"inPort":     30005,
	}))
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode response: %v body=%s", err, res.Body.String())
	}
	if env.Code == 0 {
		t.Fatalf("create must fail (node B hard-fails UpdateService), got success: %s", res.Body.String())
	}
	var forwardCount int64
	if err := r.DB().Table("forward").Count(&forwardCount).Error; err != nil {
		t.Fatalf("count forwards: %v", err)
	}
	if forwardCount != 0 {
		t.Fatalf("forward rows = %d, want 0 (failed create must not leave a DB row)", forwardCount)
	}
	// The discriminating assertion: node A already received the service,
	// so the compensation must have sent it a DeleteService.
	waitForCondition(t, 5*time.Second, func() bool { return rcA.sawCommand("DeleteService") }, "DeleteService on node A")
	if !strings.Contains(env.Msg, "补偿清理未完成") {
		t.Fatalf("failure receipt must name the incomplete compensation cleanup, got msg=%q", env.Msg)
	}
}
