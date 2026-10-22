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

// R2 批 9 · R2X-008（白名单补接五批路径）/ R2X-009（RemoteAddr 重算闭环）
// 回归。白名单断言复用 R1-B3 的 mock agent 观测法：操作后落地节点必须
// 收到新的 KernelApply，且 allow 集随拨号节点集变化。

// --- R2X-008 ---

// 带转发删隧道（delete_forwards，单条入口）：删后拨号 IP 退出 allow。
func TestR2B9TunnelDeleteWithForwardsDeleteRefreshesWhitelist(t *testing.T) {
	s := setupR1B3Whitelist(t, 2)
	baseline := s.allowBaseline()
	res := httptest.NewRecorder()
	s.h.tunnelDeleteWithForwards(res, newAuthenticatedJSONRequest(t, map[string]interface{}{
		"id": s.tunnelID, "action": "delete_forwards",
	}))
	if code, msg := decodePendingEnvelopeCode(t, res); code != 0 {
		t.Fatalf("tunnelDeleteWithForwards failed: code=%d msg=%q body=%s", code, msg, res.Body.String())
	}
	fresh := s.newAllowsSince(baseline)
	if len(fresh) == 0 {
		t.Fatalf("R2X-008: no KernelApply pushed after delete-with-forwards (whitelist not refreshed)")
	}
	last := fresh[len(fresh)-1]
	if strings.Contains(last, "198.51.100.2/32") {
		t.Fatalf("refreshed allow must drop the deleted tunnel's exit IP, got %s", last)
	}
}

// 带转发删隧道（delete_forwards，批量入口）：同一接线。
func TestR2B9TunnelBatchDeleteWithForwardsDeleteRefreshesWhitelist(t *testing.T) {
	s := setupR1B3Whitelist(t, 2)
	baseline := s.allowBaseline()
	res := httptest.NewRecorder()
	s.h.tunnelBatchDeleteWithForwards(res, newAuthenticatedJSONRequest(t, map[string]interface{}{
		"ids": []int64{s.tunnelID}, "action": "delete_forwards",
	}))
	if code, msg := decodePendingEnvelopeCode(t, res); code != 0 {
		t.Fatalf("tunnelBatchDeleteWithForwards failed: code=%d msg=%q body=%s", code, msg, res.Body.String())
	}
	fresh := s.newAllowsSince(baseline)
	if len(fresh) == 0 {
		t.Fatalf("R2X-008: no KernelApply pushed after batch delete-with-forwards (whitelist not refreshed)")
	}
	last := fresh[len(fresh)-1]
	if strings.Contains(last, "198.51.100.2/32") {
		t.Fatalf("refreshed allow must drop the deleted tunnel's exit IP, got %s", last)
	}
}

// 带转发删隧道（replace 到隧道 B）：allow 含 B 不含 A。
func TestR2B9TunnelDeleteWithForwardsReplaceRefreshesWhitelist(t *testing.T) {
	s := setupR1B3Whitelist(t, 2)
	// 场景转发端口 20000 由 CreateForwardTx 直种（绕过端口范围校验），
	// 迁移计划会按节点 PortRange 复检，先放宽范围使迁移本身可达。
	if err := s.repo.DB().Exec(`UPDATE node SET port = '1000-65535'`).Error; err != nil {
		t.Fatal(err)
	}
	dstTunnelID := seedRelayTargetTunnel(t, s.repo, "wl-tunnel-b", s.frontID, 30001, s.exitBID)
	if err := s.repo.DB().Exec(`UPDATE tunnel SET status = 1 WHERE id = ?`, dstTunnelID).Error; err != nil {
		t.Fatal(err)
	}
	baseline := s.allowBaseline()
	res := httptest.NewRecorder()
	s.h.tunnelDeleteWithForwards(res, newAuthenticatedJSONRequest(t, map[string]interface{}{
		"id": s.tunnelID, "action": "replace", "targetTunnelId": dstTunnelID,
	}))
	if code, msg := decodePendingEnvelopeCode(t, res); code != 0 {
		t.Fatalf("tunnelDeleteWithForwards replace failed: code=%d msg=%q body=%s", code, msg, res.Body.String())
	}
	fresh := s.newAllowsSince(baseline)
	if len(fresh) == 0 {
		t.Fatalf("R2X-008: no KernelApply pushed after delete-with-forwards replace (whitelist not refreshed)")
	}
	last := fresh[len(fresh)-1]
	if !strings.Contains(last, "198.51.100.3/32") || strings.Contains(last, "198.51.100.2/32") {
		t.Fatalf("refreshed allow must contain B and not A, got %s", last)
	}
}

// 转发页批量换隧道：allow 含新隧道出口 B、不含旧出口 A。
func TestR2B9ForwardBatchChangeTunnelRefreshesWhitelist(t *testing.T) {
	s := setupR1B3Whitelist(t, 2)
	// 同 replace 用例：放宽节点端口范围使换隧道校验可达。
	if err := s.repo.DB().Exec(`UPDATE node SET port = '1000-65535'`).Error; err != nil {
		t.Fatal(err)
	}
	dstTunnelID := seedRelayTargetTunnel(t, s.repo, "wl-tunnel-b", s.frontID, 30001, s.exitBID)
	if err := s.repo.DB().Exec(`UPDATE tunnel SET status = 1 WHERE id = ?`, dstTunnelID).Error; err != nil {
		t.Fatal(err)
	}
	baseline := s.allowBaseline()
	res := httptest.NewRecorder()
	s.h.forwardBatchChangeTunnel(res, newAuthenticatedJSONRequest(t, map[string]interface{}{
		"forwardIds": []int64{s.forwardID}, "targetTunnelId": dstTunnelID,
	}))
	if code, msg := decodePendingEnvelopeCode(t, res); code != 0 {
		t.Fatalf("forwardBatchChangeTunnel failed: code=%d msg=%q body=%s", code, msg, res.Body.String())
	}
	fresh := s.newAllowsSince(baseline)
	if len(fresh) == 0 {
		t.Fatalf("R2X-008: no KernelApply pushed after batch change tunnel (whitelist not refreshed)")
	}
	last := fresh[len(fresh)-1]
	if !strings.Contains(last, "198.51.100.3/32") || strings.Contains(last, "198.51.100.2/32") {
		t.Fatalf("refreshed allow must contain B and not A, got %s", last)
	}
}

// 删用户：其名下中转转发被级联删除后，allow 去掉该隧道的拨号节点。
func TestR2B9UserDeleteRefreshesWhitelist(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r2b9-userdel-wl.db")
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
		t.Fatalf("create admin: %v", err)
	}
	victimID, err := r.CreateUser("victim", "hash", 1, now+86400000, 1, 1, 100, 1, 0, now)
	if err != nil {
		t.Fatalf("create victim: %v", err)
	}
	frontID := seedRelayTargetNode(t, r, "front", "198.51.100.1")
	landingID := seedRelayTargetNode(t, r, "landing", "203.0.113.10")
	if err := r.DB().Exec(`UPDATE node SET status = 1`).Error; err != nil {
		t.Fatal(err)
	}
	tunnelID := seedTunnelForNftables(t, h, "wl-tunnel", frontID)
	landingBox := seedChainKernel(t, r, landingID, kernel.TypeSingbox)
	target := seedChainInbound(t, r, landingBox, "landing-vless", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 443,
		Security: kernel.Security{Type: kernel.SecurityReality}}, 0)
	target.RelayWhitelist = 1
	if err := r.SaveNodeInbound(target); err != nil {
		t.Fatal(err)
	}
	forwardID, err := r.CreateForwardTx(victimID, "victim", "relay-fwd", tunnelID, "203.0.113.10:443", "fifo", now, 1,
		[]int64{frontID}, 20000, "", nil, 0, 0, nil, 0, 0, 0)
	if err != nil {
		t.Fatalf("create forward: %v", err)
	}
	if err := r.CreateInboundRelay(&model.NodeInboundRelay{InboundID: target.ID, ForwardID: forwardID, Name: "relay", Managed: 1, CreatedTime: now}); err != nil {
		t.Fatal(err)
	}
	landingRC := &r1b3CommandRecorder{}
	frontRC := &r1b3CommandRecorder{}
	connectR1B3Agent(t, ts, "front-secret", frontRC)
	connectR1B3Agent(t, ts, "landing-secret", landingRC)
	waitForCondition(t, 5*time.Second, func() bool {
		return server.IsNodeOnline(frontID) && server.IsNodeOnline(landingID)
	}, "mock agents online")
	probe := &r1b3WLScenario{landingRC: landingRC}
	waitForCondition(t, 5*time.Second, func() bool {
		return len(probe.landingRC.kernelApplyAllows()) > 0
	}, "initial kernel apply on reconnect")
	baseline := len(landingRC.kernelApplyAllows())
	initial := landingRC.kernelApplyAllows()
	if !strings.Contains(initial[len(initial)-1], "198.51.100.1/32") {
		t.Fatalf("setup: initial allow must contain the entry IP, got %v", initial)
	}

	res := httptest.NewRecorder()
	h.userDelete(res, newAuthenticatedJSONRequest(t, map[string]interface{}{"id": victimID}))
	if code, msg := decodePendingEnvelopeCode(t, res); code != 0 {
		t.Fatalf("userDelete failed: code=%d msg=%q body=%s", code, msg, res.Body.String())
	}
	fresh := probe.newAllowsSince(baseline)
	if len(fresh) == 0 {
		t.Fatalf("R2X-008: no KernelApply pushed after userDelete (whitelist not refreshed)")
	}
	last := fresh[len(fresh)-1]
	if strings.Contains(last, "198.51.100.1/32") {
		t.Fatalf("refreshed allow must drop the deleted user's relay entry IP, got %s", last)
	}
}

// 节点改址（E S-1 白名单半）：出口节点改址后 allow 含新址不含旧址。
func TestR2B9NodeUpdateAddressRefreshesWhitelist(t *testing.T) {
	s := setupR1B3Whitelist(t, 2)
	baseline := s.allowBaseline()
	res := httptest.NewRecorder()
	// seed 的 server_ip 与 server_ip_v4 同值，改址须两面同改（白名单
	// 地址集含 V4 字段，单改 serverIp 时旧址仍由 V4 字段合法保留）。
	s.h.nodeUpdate(res, newAuthenticatedJSONRequest(t, map[string]interface{}{
		"id": s.exitAID, "serverIp": "198.51.100.22", "serverIpV4": "198.51.100.22",
	}))
	if code, msg := decodePendingEnvelopeCode(t, res); code != 0 {
		t.Fatalf("nodeUpdate failed: code=%d msg=%q body=%s", code, msg, res.Body.String())
	}
	fresh := s.newAllowsSince(baseline)
	if len(fresh) == 0 {
		t.Fatalf("R2X-008/E S-1: no KernelApply pushed after node address change (whitelist not refreshed)")
	}
	last := fresh[len(fresh)-1]
	if !strings.Contains(last, "198.51.100.22/32") || strings.Contains(last, "198.51.100.2/32") {
		t.Fatalf("refreshed allow must contain the new address and not the old, got %s", last)
	}
}

// --- R2X-009 ---

type r2b9RelayFixture struct {
	h         *Handler
	repo      *repo.Repository
	frontID   int64
	landingID int64
	otherID   int64
	tunnelID  int64
	forwardID int64
	inboundID int64
}

// setupR2B9Relay seeds a managed relay forward on a type-2 tunnel whose
// single exit is exitNode (landing or other), with the given stored
// RemoteAddr. No agents: tunnel runtime apply defers offline.
func setupR2B9Relay(t *testing.T, exitIsLanding bool, remoteAddr string) *r2b9RelayFixture {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/r2b9-relay.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	f := &r2b9RelayFixture{h: h, repo: r}
	now := time.Now().UnixMilli()
	if _, err := r.CreateUser("admin", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now); err != nil {
		t.Fatalf("create user: %v", err)
	}
	f.frontID = seedRelayTargetNode(t, r, "front", "198.51.100.1")
	f.otherID = seedRelayTargetNode(t, r, "exit-other", "198.51.100.9")
	f.landingID = seedRelayTargetNode(t, r, "landing", "203.0.113.10")
	if err := r.DB().Exec(`UPDATE node SET status = 1`).Error; err != nil {
		t.Fatal(err)
	}
	exitNode := f.otherID
	if exitIsLanding {
		exitNode = f.landingID
	}
	f.tunnelID = seedRelayTargetTunnel(t, r, "relay-tunnel", f.frontID, 30000, exitNode)
	if err := r.DB().Exec(`UPDATE tunnel SET status = 1`).Error; err != nil {
		t.Fatal(err)
	}
	landingBox := seedChainKernel(t, r, f.landingID, kernel.TypeSingbox)
	target := seedChainInbound(t, r, landingBox, "landing-vless", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 443,
		Security: kernel.Security{Type: kernel.SecurityReality}}, 0)
	f.inboundID = target.ID
	forwardID, err := r.CreateForwardTx(1, "admin", "relay-fwd", f.tunnelID, remoteAddr, "fifo", now, 1,
		[]int64{f.frontID}, 30005, "", nil, 0, 0, nil, 0, 0, 0)
	if err != nil {
		t.Fatalf("create forward: %v", err)
	}
	f.forwardID = forwardID
	if err := r.CreateInboundRelay(&model.NodeInboundRelay{InboundID: target.ID, ForwardID: forwardID, Name: "relay", Managed: 1, CreatedTime: now}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *r2b9RelayFixture) remoteAddr(t *testing.T) string {
	t.Helper()
	fwd, err := f.h.getForwardRecord(f.forwardID)
	if err != nil || fwd == nil {
		t.Fatalf("get forward: %v", err)
	}
	return fwd.RemoteAddr
}

func (f *r2b9RelayFixture) tunnelUpdate(t *testing.T, body map[string]interface{}) (int, string, string) {
	t.Helper()
	res := httptest.NewRecorder()
	f.h.tunnelUpdate(res, newAuthenticatedJSONRequest(t, body))
	code, msg := decodePendingEnvelopeCode(t, res)
	return code, msg, res.Body.String()
}

// {L}→{X}：RemoteAddr 变落地公网地址。
func TestR2B9TunnelUpdateExitLandingToOtherRecomputesRemoteAddr(t *testing.T) {
	f := setupR2B9Relay(t, true, "127.0.0.1:443")
	code, msg, body := f.tunnelUpdate(t, map[string]interface{}{
		"id": f.tunnelID, "type": 2, "name": "relay-tunnel",
		"inNodeId":  []map[string]interface{}{{"nodeId": f.frontID, "protocol": "tls"}},
		"outNodeId": []map[string]interface{}{{"nodeId": f.otherID, "protocol": "tls", "port": 30001}},
	})
	if code != 0 {
		t.Fatalf("tunnelUpdate failed: code=%d msg=%q body=%s", code, msg, body)
	}
	if got := f.remoteAddr(t); got != "203.0.113.10:443" {
		t.Fatalf("R2X-009: RemoteAddr must follow the new exit set (landing public address), got %q", got)
	}
}

// {X}→{L}：RemoteAddr 变 127.0.0.1:端口。
func TestR2B9TunnelUpdateExitOtherToLandingRecomputesRemoteAddr(t *testing.T) {
	f := setupR2B9Relay(t, false, "203.0.113.10:443")
	code, msg, body := f.tunnelUpdate(t, map[string]interface{}{
		"id": f.tunnelID, "type": 2, "name": "relay-tunnel",
		"inNodeId":  []map[string]interface{}{{"nodeId": f.frontID, "protocol": "tls"}},
		"outNodeId": []map[string]interface{}{{"nodeId": f.landingID, "protocol": "tls", "port": 30001}},
	})
	if code != 0 {
		t.Fatalf("tunnelUpdate failed: code=%d msg=%q body=%s", code, msg, body)
	}
	if got := f.remoteAddr(t); got != "127.0.0.1:443" {
		t.Fatalf("R2X-009: RemoteAddr must follow the new exit set (loopback), got %q", got)
	}
}

// Type 2→1：回环物化地址必须按 Type 1 口径重算为落地公网地址。
func TestR2B9TunnelUpdateType2To1RecomputesRemoteAddr(t *testing.T) {
	f := setupR2B9Relay(t, true, "127.0.0.1:443")
	code, msg, body := f.tunnelUpdate(t, map[string]interface{}{
		"id": f.tunnelID, "type": 1, "name": "relay-tunnel",
		"inNodeId": []map[string]interface{}{{"nodeId": f.frontID, "protocol": "tls"}},
	})
	if code != 0 {
		t.Fatalf("tunnelUpdate 2->1 failed: code=%d msg=%q body=%s", code, msg, body)
	}
	if got := f.remoteAddr(t); got != "203.0.113.10:443" {
		t.Fatalf("R2X-009: RemoteAddr must be recomputed for type 1 (landing public address), got %q", got)
	}
}

// Type 2→1 + 入站只监听回环：预演不可计算 → 拒绝保存，不留静默断流态。
func TestR2B9TunnelUpdateType2To1LoopbackInboundRejected(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r2b9-relay-loop.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now().UnixMilli()
	if _, err := r.CreateUser("admin", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now); err != nil {
		t.Fatal(err)
	}
	frontID := seedRelayTargetNode(t, r, "front", "198.51.100.1")
	otherID := seedRelayTargetNode(t, r, "exit-other", "198.51.100.9")
	landingID := seedRelayTargetNode(t, r, "landing", "203.0.113.10")
	if err := r.DB().Exec(`UPDATE node SET status = 1`).Error; err != nil {
		t.Fatal(err)
	}
	tunnelID := seedRelayTargetTunnel(t, r, "relay-tunnel", frontID, 30000, otherID)
	landingBox := seedChainKernel(t, r, landingID, kernel.TypeSingbox)
	target := seedChainInbound(t, r, landingBox, "landing-loop", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 443,
		Listen: "127.0.0.1", Security: kernel.Security{Type: kernel.SecurityReality}}, 0)
	forwardID, err := r.CreateForwardTx(1, "admin", "relay-fwd", tunnelID, "203.0.113.10:443", "fifo", now, 1,
		[]int64{frontID}, 30005, "", nil, 0, 0, nil, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.CreateInboundRelay(&model.NodeInboundRelay{InboundID: target.ID, ForwardID: forwardID, Name: "relay", Managed: 1, CreatedTime: now}); err != nil {
		t.Fatal(err)
	}
	res := httptest.NewRecorder()
	h.tunnelUpdate(res, newAuthenticatedJSONRequest(t, map[string]interface{}{
		"id": tunnelID, "type": 1, "name": "relay-tunnel",
		"inNodeId": []map[string]interface{}{{"nodeId": frontID, "protocol": "tls"}},
	}))
	code, msg := decodePendingEnvelopeCode(t, res)
	if code == 0 {
		t.Fatalf("R2X-009: 2->1 with a loopback-only inbound must be rejected (or explicitly warned), got success body=%s", res.Body.String())
	}
	if !strings.Contains(msg, "只监听本机") {
		t.Fatalf("rejection must explain the loopback constraint, got %q", msg)
	}
	tun, err := h.getTunnelRecord(tunnelID)
	if err != nil || tun == nil || tun.Type != 2 {
		t.Fatalf("tunnel must be unchanged after rejection: %+v err=%v", tun, err)
	}
}

// 入站端口 443→8443：名下 relay 的 RemoteAddr 端口跟随。
func TestR2B9InboundPortChangeRecomputesRelayRemoteAddr(t *testing.T) {
	s := setupR1B3RelayScenario(t, false)
	row, err := s.repo.GetNodeInbound(s.inboundID)
	if err != nil || row == nil {
		t.Fatalf("get inbound: %v", err)
	}
	old, err := inboundFromModel(*row)
	if err != nil {
		t.Fatal(err)
	}
	reqBody := inboundRequest{
		ID: row.ID, Name: row.Name, Protocol: old.Protocol, Listen: old.Listen, Port: 8443,
		Settings: old.Settings, Transport: old.Transport, Security: old.Security,
	}
	raw, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatal(err)
	}
	out := callInboundSave(t, s.h, true, string(raw))
	if out.Code != 0 {
		t.Fatalf("inbound update failed: %+v", out)
	}
	if got := s.forwardRemoteAddr(t, s.forwardID); got != "203.0.113.10:8443" {
		t.Fatalf("R2X-009: relay RemoteAddr port must follow the inbound port change, got %q", got)
	}
}

// 节点改址（R2X-009 子项③）：落在本节点的 Type 1 中转 RemoteAddr host 跟随新址。
func TestR2B9NodeUpdateAddressRecomputesRelayRemoteAddr(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r2b9-nodeaddr-relay.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now().UnixMilli()
	if _, err := r.CreateUser("admin", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now); err != nil {
		t.Fatal(err)
	}
	frontID := seedRelayTargetNode(t, r, "front", "198.51.100.1")
	landingID := seedRelayTargetNode(t, r, "landing", "203.0.113.10")
	if err := r.DB().Exec(`UPDATE node SET status = 1`).Error; err != nil {
		t.Fatal(err)
	}
	tunnelID := seedTunnelForNftables(t, h, "port-tunnel", frontID)
	landingBox := seedChainKernel(t, r, landingID, kernel.TypeSingbox)
	target := seedChainInbound(t, r, landingBox, "landing-vless", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 443,
		Security: kernel.Security{Type: kernel.SecurityReality}}, 0)
	forwardID, err := r.CreateForwardTx(1, "admin", "relay-fwd", tunnelID, "203.0.113.10:443", "fifo", now, 1,
		[]int64{frontID}, 20000, "", nil, 0, 0, nil, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.CreateInboundRelay(&model.NodeInboundRelay{InboundID: target.ID, ForwardID: forwardID, Name: "relay", Managed: 1, CreatedTime: now}); err != nil {
		t.Fatal(err)
	}
	res := httptest.NewRecorder()
	h.nodeUpdate(res, newAuthenticatedJSONRequest(t, map[string]interface{}{
		"id": landingID, "serverIp": "203.0.113.99",
	}))
	if code, msg := decodePendingEnvelopeCode(t, res); code != 0 {
		t.Fatalf("nodeUpdate failed: code=%d msg=%q body=%s", code, msg, res.Body.String())
	}
	fwd, err := h.getForwardRecord(forwardID)
	if err != nil || fwd == nil {
		t.Fatalf("get forward: %v", err)
	}
	if fwd.RemoteAddr != "203.0.113.99:443" {
		t.Fatalf("R2X-009: relay RemoteAddr host must follow the node address change, got %q", fwd.RemoteAddr)
	}
}
