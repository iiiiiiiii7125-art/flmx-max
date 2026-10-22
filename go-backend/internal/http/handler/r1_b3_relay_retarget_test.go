package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
	"go-backend/internal/ws"
)

// R1 批次 3 · X-P2-4（E-03/E-S1，BC-05，V-05）：
//  1. tunnelUpdate 校验段漏复跑 onLanding 判定——给落地型中转的隧道加
//     第二出口能保存，转发 RemoteAddr 仍是回环地址，静默错向。
//  2. forwardBatchChangeTunnel 与删隧道迁移只改端口不重算 RemoteAddr——
//     落地型转发迁入出口集不同的隧道后仍拨旧地址（回环/公网互串）。

type r1b3RelayScenario struct {
	h           *Handler
	repo        *repo.Repository
	frontID     int64
	exitOtherID int64
	landingID   int64
	srcTunnelID int64
	dstTunnelID int64
	forwardID   int64
	plainID     int64
	inboundID   int64
}

// setupR1B3RelayScenario seeds: landing node + kernel + inbound; src tunnel
// (type2, exit = other node) holding a managed relay forward dialing the
// landing public address; dst tunnel (type2, exit = landing node itself).
// withAgent brings the front (entry) node online via a mock agent so the
// change-tunnel sync succeeds.
func setupR1B3RelayScenario(t *testing.T, withAgent bool) *r1b3RelayScenario {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/r1b3-retarget.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	s := &r1b3RelayScenario{h: h, repo: r}
	if withAgent {
		server := ws.NewServer(r, "test-secret")
		server.SetNodeOnlineHook(h.onNodeOnline)
		server.SetNodeOfflineHook(h.notifyNodeOffline)
		h.wsServer = server
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			server.ServeHTTP(w, req)
		}))
		t.Cleanup(ts.Close)
		defer func() {
			rc := &r1b3CommandRecorder{}
			connectR1B3Agent(t, ts, "front-secret", rc)
			waitForCondition(t, 5*time.Second, func() bool { return server.IsNodeOnline(s.frontID) }, "front agent online")
		}()
	}
	now := time.Now().UnixMilli()
	if _, err := r.CreateUser("admin", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now); err != nil {
		t.Fatalf("create user: %v", err)
	}
	s.frontID = seedRelayTargetNode(t, r, "front", "198.51.100.1")
	s.exitOtherID = seedRelayTargetNode(t, r, "exit-other", "198.51.100.9")
	s.landingID = seedRelayTargetNode(t, r, "landing", "203.0.113.10")
	if err := r.DB().Exec(`UPDATE node SET status = 1`).Error; err != nil {
		t.Fatalf("activate nodes: %v", err)
	}
	s.srcTunnelID = seedRelayTargetTunnel(t, r, "src-tunnel", s.frontID, 30000, s.exitOtherID)
	s.dstTunnelID = seedRelayTargetTunnel(t, r, "dst-tunnel", s.frontID, 30000, s.landingID)
	if err := r.DB().Exec(`UPDATE tunnel SET status = 1`).Error; err != nil {
		t.Fatalf("activate tunnels: %v", err)
	}
	landingBox := seedChainKernel(t, r, s.landingID, kernel.TypeSingbox)
	target := seedChainInbound(t, r, landingBox, "landing-vless", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 443,
		Security: kernel.Security{Type: kernel.SecurityReality}}, 0)
	s.inboundID = target.ID
	forwardID, err := r.CreateForwardTx(1, "admin", "relay-fwd", s.srcTunnelID, "203.0.113.10:443", "fifo", now, 1,
		[]int64{s.frontID}, 30005, "", nil, 0, 0, nil, 0, 0, 0)
	if err != nil {
		t.Fatalf("create relay forward: %v", err)
	}
	s.forwardID = forwardID
	if err := r.CreateInboundRelay(&model.NodeInboundRelay{InboundID: target.ID, ForwardID: forwardID, Name: "relay", Managed: 1, CreatedTime: now}); err != nil {
		t.Fatalf("create relay: %v", err)
	}
	plainID, err := r.CreateForwardTx(1, "admin", "plain-fwd", s.srcTunnelID, "203.0.113.10:443", "fifo", now, 1,
		[]int64{s.frontID}, 30006, "", nil, 0, 0, nil, 0, 0, 0)
	if err != nil {
		t.Fatalf("create plain forward: %v", err)
	}
	s.plainID = plainID
	return s
}

func (s *r1b3RelayScenario) forwardRemoteAddr(t *testing.T, id int64) string {
	t.Helper()
	fwd, err := s.h.getForwardRecord(id)
	if err != nil || fwd == nil {
		t.Fatalf("get forward %d: %v", id, err)
	}
	return fwd.RemoteAddr
}

// TestR1B3TunnelUpdateRejectsSecondExitForLandingRelay: 落地型中转的隧道
// 加第二出口必须被拒（文案与 relayTarget 一致）；合法编辑（改名）照常通过。
func TestR1B3TunnelUpdateRejectsSecondExitForLandingRelay(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r1b3-landing-edit.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now().UnixMilli()
	if _, err := r.CreateUser("admin", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now); err != nil {
		t.Fatalf("create user: %v", err)
	}
	front := seedRelayTargetNode(t, r, "front", "198.51.100.1")
	landing := seedRelayTargetNode(t, r, "landing", "203.0.113.10")
	extra := seedRelayTargetNode(t, r, "extra", "198.51.100.7")
	if err := r.DB().Exec(`UPDATE node SET status = 1`).Error; err != nil {
		t.Fatalf("activate nodes: %v", err)
	}
	tunnelID := seedRelayTargetTunnel(t, r, "landing-tunnel", front, 30000, landing)
	landingBox := seedChainKernel(t, r, landing, kernel.TypeSingbox)
	target := seedChainInbound(t, r, landingBox, "landing-vless", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 443,
		Security: kernel.Security{Type: kernel.SecurityReality}}, 0)
	forwardID, err := r.CreateForwardTx(1, "admin", "relay-fwd", tunnelID, "127.0.0.1:443", "fifo", now, 1,
		[]int64{front}, 30005, "", nil, 0, 0, nil, 0, 0, 0)
	if err != nil {
		t.Fatalf("create forward: %v", err)
	}
	if err := r.CreateInboundRelay(&model.NodeInboundRelay{InboundID: target.ID, ForwardID: forwardID, Name: "relay", Managed: 1, CreatedTime: now}); err != nil {
		t.Fatalf("create relay: %v", err)
	}

	// 加第二出口 → 拒绝，文案与 kernel_relay.go relayTarget 一致。
	res := httptest.NewRecorder()
	h.tunnelUpdate(res, newAuthenticatedJSONRequest(t, map[string]interface{}{
		"id": tunnelID, "type": 2, "name": "landing-tunnel",
		"inNodeId": []map[string]interface{}{{"nodeId": front, "protocol": "tls"}},
		"outNodeId": []map[string]interface{}{
			{"nodeId": landing, "protocol": "tls", "port": 30000},
			{"nodeId": extra, "protocol": "tls", "port": 30001},
		},
	}))
	code, msg := decodePendingEnvelopeCode(t, res)
	if code == 0 {
		t.Fatalf("X-P2-4: adding a second exit to a landing-relay tunnel must be rejected, got success body=%s", res.Body.String())
	}
	if !strings.Contains(msg, "出口为落地节点的隧道只能有这一个出口节点") {
		t.Fatalf("rejection message must match relayTarget wording, got %q", msg)
	}
	exits, err := h.repo.TunnelExitNodeIDs(tunnelID)
	if err != nil || len(exits) != 1 || exits[0] != landing {
		t.Fatalf("tunnel exits must be unchanged after rejection: %v err=%v", exits, err)
	}

	// 防过修：合法编辑（改名、出口不变）照常通过。
	res = httptest.NewRecorder()
	h.tunnelUpdate(res, newAuthenticatedJSONRequest(t, map[string]interface{}{
		"id": tunnelID, "type": 2, "name": "landing-tunnel-renamed",
		"inNodeId":  []map[string]interface{}{{"nodeId": front, "protocol": "tls"}},
		"outNodeId": []map[string]interface{}{{"nodeId": landing, "protocol": "tls", "port": 30000}},
	}))
	if code, msg := decodePendingEnvelopeCode(t, res); code != 0 {
		t.Fatalf("legit tunnel edit must pass: code=%d msg=%q body=%s", code, msg, res.Body.String())
	}
}

// TestR1B3BatchChangeTunnelRecomputesRelayRemoteAddr: 落地型转发迁入
// 出口为落地节点的隧道 → RemoteAddr 重算为回环地址；普通转发不被重算。
func TestR1B3BatchChangeTunnelRecomputesRelayRemoteAddr(t *testing.T) {
	s := setupR1B3RelayScenario(t, true)
	res := httptest.NewRecorder()
	s.h.forwardBatchChangeTunnel(res, newAuthenticatedJSONRequest(t, map[string]interface{}{
		"forwardIds":     []int64{s.forwardID, s.plainID},
		"targetTunnelId": s.dstTunnelID,
	}))
	code, msg := decodePendingEnvelopeCode(t, res)
	if code != 0 {
		t.Fatalf("batch change tunnel failed: code=%d msg=%q body=%s", code, msg, res.Body.String())
	}
	var env struct {
		Data struct {
			SuccessCount int `json:"successCount"`
			FailCount    int `json:"failCount"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil || env.Data.SuccessCount != 2 || env.Data.FailCount != 0 {
		t.Fatalf("batch result = %+v err=%v body=%s", env.Data, err, res.Body.String())
	}
	fwd, err := s.h.getForwardRecord(s.forwardID)
	if err != nil || fwd == nil {
		t.Fatalf("get forward: %v", err)
	}
	if fwd.TunnelID != s.dstTunnelID {
		t.Fatalf("forward not moved: tunnel=%d", fwd.TunnelID)
	}
	if fwd.RemoteAddr != "127.0.0.1:443" {
		t.Fatalf("X-P2-4: relay RemoteAddr must be recomputed for the target tunnel (on-landing => loopback), got %q", fwd.RemoteAddr)
	}
	if got := s.forwardRemoteAddr(t, s.plainID); got != "203.0.113.10:443" {
		t.Fatalf("plain forward RemoteAddr must not be recomputed, got %q", got)
	}
}

// TestR1B3TunnelDeleteMigrationRecomputesRelayRemoteAddr: 删隧道迁移
// 同一重算口径。
func TestR1B3TunnelDeleteMigrationRecomputesRelayRemoteAddr(t *testing.T) {
	s := setupR1B3RelayScenario(t, true)
	res := httptest.NewRecorder()
	s.h.tunnelDeleteWithForwards(res, newAuthenticatedJSONRequest(t, map[string]interface{}{
		"id": s.srcTunnelID, "action": "replace", "targetTunnelId": s.dstTunnelID,
	}))
	code, msg := decodePendingEnvelopeCode(t, res)
	if code != 0 {
		t.Fatalf("tunnelDelete replace failed: code=%d msg=%q body=%s", code, msg, res.Body.String())
	}
	fwd, err := s.h.getForwardRecord(s.forwardID)
	if err != nil || fwd == nil {
		t.Fatalf("get forward after migration: %v", err)
	}
	if fwd.TunnelID != s.dstTunnelID {
		t.Fatalf("forward not migrated: tunnel=%d", fwd.TunnelID)
	}
	if fwd.RemoteAddr != "127.0.0.1:443" {
		t.Fatalf("X-P2-4: migrated relay RemoteAddr must be recomputed for the target tunnel, got %q", fwd.RemoteAddr)
	}
	if got := s.forwardRemoteAddr(t, s.plainID); got != "203.0.113.10:443" {
		t.Fatalf("plain forward RemoteAddr must not be recomputed, got %q", got)
	}
}
