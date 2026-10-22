package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"go-backend/internal/kernel"
	"go-backend/internal/security"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
	"go-backend/internal/ws"
)

// R1 批次 3 · X-P2-3（E-02）：refreshInboundWhitelist 漏接线——隧道编辑
// （改入口/出口）与转发页删转发（单删/强删/批量）后白名单不刷新：新出口
// IP 被丢弃致中转静默断、旧 IP 残留放行。断言口径（端到端）：操作后
// 落地节点必须收到新的 KernelApply，且其 ports.allow 渲染含新 IP、
// 不含旧 IP（未接线时节点收不到任何新下发）。

type r1b3CommandRecorder struct {
	mu   sync.Mutex
	cmds []cleanupAgentCommand
}

func (rc *r1b3CommandRecorder) snapshot() []cleanupAgentCommand {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return append([]cleanupAgentCommand(nil), rc.cmds...)
}

func (rc *r1b3CommandRecorder) kernelApplyAllows() []string {
	var out []string
	for _, c := range rc.snapshot() {
		if c.Type != "KernelApply" {
			continue
		}
		var payload struct {
			Ports []struct {
				Allow []string `json:"allow"`
			} `json:"ports"`
		}
		if err := json.Unmarshal(c.Data, &payload); err != nil {
			continue
		}
		for _, p := range payload.Ports {
			out = append(out, strings.Join(p.Allow, ","))
		}
	}
	return out
}

// connectR1B3Agent dials a mock agent for the node with the given secret and
// ACKs every command with success, recording all commands.
func connectR1B3Agent(t *testing.T, ts *httptest.Server, secret string, rc *r1b3CommandRecorder) {
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
			if err := conn.WriteJSON(map[string]interface{}{"type": command.Type, "requestId": command.RequestID, "success": true}); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = conn.Close()
		<-done
	})
}

type r1b3WLScenario struct {
	h         *Handler
	repo      *repo.Repository
	landingRC *r1b3CommandRecorder
	tunnelID  int64
	forwardID int64
	inboundID int64
	kernelID  int64
	frontID   int64
	exitAID   int64
	exitBID   int64
	landingID int64
}

// setupR1B3Whitelist seeds: landing node + singbox kernel + whitelisted
// inbound; a tunnel (type 2 exit A, or type 1 entry front) whose relay
// forward dials the inbound; mock agents online for front + landing.
func setupR1B3Whitelist(t *testing.T, tunnelType int) *r1b3WLScenario {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/r1b3-wl.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	server := ws.NewServer(r, "test-secret")
	server.SetNodeOnlineHook(h.onNodeOnline)
	server.SetNodeOfflineHook(h.notifyNodeOffline)
	h.wsServer = server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		server.ServeHTTP(w, req)
	}))
	t.Cleanup(ts.Close)

	now := time.Now().UnixMilli()
	if _, err := r.CreateUser("admin", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now); err != nil {
		t.Fatalf("create user: %v", err)
	}
	s := &r1b3WLScenario{h: h, repo: r, landingRC: &r1b3CommandRecorder{}}
	s.frontID = seedRelayTargetNode(t, r, "front", "198.51.100.1")
	s.exitAID = seedRelayTargetNode(t, r, "exit-a", "198.51.100.2")
	s.exitBID = seedRelayTargetNode(t, r, "exit-b", "198.51.100.3")
	s.landingID = seedRelayTargetNode(t, r, "landing", "203.0.113.10")
	if err := r.DB().Exec(`UPDATE node SET status = 1`).Error; err != nil {
		t.Fatalf("activate nodes: %v", err)
	}
	if tunnelType == 2 {
		s.tunnelID = seedRelayTargetTunnel(t, r, "wl-tunnel", s.frontID, 30000, s.exitAID)
	} else {
		s.tunnelID = seedTunnelForNftables(t, h, "wl-tunnel", s.frontID)
	}
	landingBox := seedChainKernel(t, r, s.landingID, kernel.TypeSingbox)
	s.kernelID = landingBox.ID
	target := seedChainInbound(t, r, landingBox, "landing-vless", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 443,
		Security: kernel.Security{Type: kernel.SecurityReality}}, 0)
	target.RelayWhitelist = 1
	if err := r.SaveNodeInbound(target); err != nil {
		t.Fatalf("save inbound whitelist: %v", err)
	}
	s.inboundID = target.ID
	forwardID, err := r.CreateForwardTx(1, "admin", "relay-fwd", s.tunnelID, "203.0.113.10:443", "fifo", now, 1,
		[]int64{s.frontID}, 20000, "", nil, 0, 0, nil, 0, 0, 0)
	if err != nil {
		t.Fatalf("create forward: %v", err)
	}
	s.forwardID = forwardID
	if err := r.CreateInboundRelay(&model.NodeInboundRelay{InboundID: target.ID, ForwardID: forwardID, Name: "relay", Managed: 1, CreatedTime: now}); err != nil {
		t.Fatalf("create relay: %v", err)
	}

	frontRC := &r1b3CommandRecorder{}
	connectR1B3Agent(t, ts, "front-secret", frontRC)
	connectR1B3Agent(t, ts, "landing-secret", s.landingRC)
	waitForCondition(t, 5*time.Second, func() bool {
		return server.IsNodeOnline(s.frontID) && server.IsNodeOnline(s.landingID)
	}, "mock agents online")
	// 等重连对账的首轮 KernelApply 落定后再取基线，避免把对账下发误判
	// 为操作触发的刷新。
	waitForCondition(t, 5*time.Second, func() bool {
		return len(s.landingRC.kernelApplyAllows()) > 0
	}, "initial kernel apply on reconnect")
	return s
}

func (s *r1b3WLScenario) allowBaseline() int {
	return len(s.landingRC.kernelApplyAllows())
}

// newAllowsSince returns the allow lists of KernelApply commands received
// after the baseline snapshot.
func (s *r1b3WLScenario) newAllowsSince(baseline int) []string {
	all := s.landingRC.kernelApplyAllows()
	if baseline >= len(all) {
		return nil
	}
	return all[baseline:]
}

// TestR1B3TunnelUpdateRefreshesWhitelist: 白名单入站 + 隧道转发（出口 A）
// → 编辑隧道出口改 B → 落地内核收到新下发，allow 含 B 不含 A。
func TestR1B3TunnelUpdateRefreshesWhitelist(t *testing.T) {
	s := setupR1B3Whitelist(t, 2)
	baseline := s.allowBaseline()
	initial := s.landingRC.kernelApplyAllows()
	if !strings.Contains(initial[len(initial)-1], "198.51.100.2/32") {
		t.Fatalf("setup: initial allow must contain exit A, got %v", initial)
	}

	res := httptest.NewRecorder()
	s.h.tunnelUpdate(res, newAuthenticatedJSONRequest(t, map[string]interface{}{
		"id": s.tunnelID, "type": 2, "name": "wl-tunnel",
		"inNodeId":  []map[string]interface{}{{"nodeId": s.frontID, "protocol": "tls"}},
		"outNodeId": []map[string]interface{}{{"nodeId": s.exitBID, "protocol": "tls", "port": 30001}},
	}))
	if code, msg := decodePendingEnvelopeCode(t, res); code != 0 {
		t.Fatalf("tunnelUpdate failed: code=%d msg=%q body=%s", code, msg, res.Body.String())
	}

	fresh := s.newAllowsSince(baseline)
	if len(fresh) == 0 {
		t.Fatalf("X-P2-3: no KernelApply pushed after tunnelUpdate changed the exit (whitelist not refreshed)")
	}
	last := fresh[len(fresh)-1]
	if !strings.Contains(last, "198.51.100.3/32") || strings.Contains(last, "198.51.100.2/32") {
		t.Fatalf("refreshed allow must contain B and not A, got %s", last)
	}
}

func assertWhitelistRefreshAfterRemoval(t *testing.T, s *r1b3WLScenario, baseline int) {
	t.Helper()
	fresh := s.newAllowsSince(baseline)
	if len(fresh) == 0 {
		t.Fatalf("X-P2-3: no KernelApply pushed after forward removal (whitelist not refreshed)")
	}
	last := fresh[len(fresh)-1]
	if strings.Contains(last, "198.51.100.1/32") {
		t.Fatalf("refreshed allow must drop the removed relay's entry IP, got %s", last)
	}
}

// TestR1B3ForwardDeleteRefreshesWhitelist: 转发页删中转转发 → allow 去掉 A。
func TestR1B3ForwardDeleteRefreshesWhitelist(t *testing.T) {
	s := setupR1B3Whitelist(t, 1)
	baseline := s.allowBaseline()
	res := httptest.NewRecorder()
	s.h.forwardDelete(res, newAuthenticatedJSONRequest(t, map[string]any{"id": s.forwardID}))
	if code, msg := decodePendingEnvelopeCode(t, res); code != 0 {
		t.Fatalf("forwardDelete failed: code=%d msg=%q", code, msg)
	}
	assertWhitelistRefreshAfterRemoval(t, s, baseline)
}

// TestR1B3ForwardForceDeleteRefreshesWhitelist: 强删同一接线。
func TestR1B3ForwardForceDeleteRefreshesWhitelist(t *testing.T) {
	s := setupR1B3Whitelist(t, 1)
	baseline := s.allowBaseline()
	res := httptest.NewRecorder()
	s.h.forwardForceDelete(res, newAuthenticatedJSONRequest(t, map[string]any{"id": s.forwardID}))
	if code, msg := decodePendingEnvelopeCode(t, res); code != 0 {
		t.Fatalf("forwardForceDelete failed: code=%d msg=%q", code, msg)
	}
	assertWhitelistRefreshAfterRemoval(t, s, baseline)
}

// TestR1B3ForwardBatchDeleteRefreshesWhitelist: 批量删同一接线。
func TestR1B3ForwardBatchDeleteRefreshesWhitelist(t *testing.T) {
	s := setupR1B3Whitelist(t, 1)
	baseline := s.allowBaseline()
	res := httptest.NewRecorder()
	s.h.forwardBatchDelete(res, newAuthenticatedJSONRequest(t, map[string]any{"ids": []int64{s.forwardID}}))
	if code, msg := decodePendingEnvelopeCode(t, res); code != 0 {
		t.Fatalf("forwardBatchDelete failed: code=%d msg=%q body=%s", code, msg, res.Body.String())
	}
	assertWhitelistRefreshAfterRemoval(t, s, baseline)
}
