package handler

import (
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

// R3 批 4 · R3X-002（E3-01）/ R3X-016（E3-02）/ R3X-017（E3-03）/
// R3X-018（E3-04）：白名单/RemoteAddr 漏斗层统一接线回归。四漏接面
// 此前绕过了 forwardDelete 已有的「删前收集受影响入站 → 落库 →
// 删后 refreshInboundWhitelists」编排：单条编辑切隧道、权限撤销
// 漏斗 cleanupForwardsForUserTunnel、删节点、jobs pending 终删。
// 断言口径与 R1-B3/R2-B9 一致：操作后落地节点必须收到新的
// KernelApply，且 allow 集随拨号节点集变化；RemoteAddr 以库内
// forward 行现值断言。

// ① forwardUpdate 切隧道（R3X-002）：旧出口 allow 收回、新拨号
// 节点（落地自身）加入、Managed relay 的 RemoteAddr 按新隧道
// 口径重算为回环地址（批量换隧道 :4288/:4345 同口径）。
func TestR3B4ForwardUpdateTunnelSwitchRefreshesWhitelistAndRemoteAddr(t *testing.T) {
	s := setupR1B3Whitelist(t, 2)
	if err := s.repo.DB().Exec(`UPDATE node SET port = '1000-65535'`).Error; err != nil {
		t.Fatal(err)
	}
	dstTunnelID := seedRelayTargetTunnel(t, s.repo, "wl-tunnel-landing", s.frontID, 30002, s.landingID)
	baseline := s.allowBaseline()
	initial := s.landingRC.kernelApplyAllows()
	if !strings.Contains(initial[len(initial)-1], "198.51.100.2/32") {
		t.Fatalf("setup: initial allow must contain exit A, got %v", initial)
	}

	res := httptest.NewRecorder()
	s.h.forwardUpdate(res, newAuthenticatedJSONRequest(t, map[string]interface{}{
		"id": s.forwardID, "tunnelId": dstTunnelID,
	}))
	if code, msg := decodePendingEnvelopeCode(t, res); code != 0 {
		t.Fatalf("forwardUpdate failed: code=%d msg=%q body=%s", code, msg, res.Body.String())
	}

	fresh := s.newAllowsSince(baseline)
	if len(fresh) == 0 {
		t.Fatalf("R3X-002: no KernelApply pushed after forwardUpdate switched tunnels (whitelist not refreshed)")
	}
	last := fresh[len(fresh)-1]
	if strings.Contains(last, "198.51.100.2/32") {
		t.Fatalf("refreshed allow must drop the old tunnel's exit IP, got %s", last)
	}
	if !strings.Contains(last, "203.0.113.10/32") {
		t.Fatalf("refreshed allow must contain the new dialer (landing itself), got %s", last)
	}
	fwd, err := s.h.getForwardRecord(s.forwardID)
	if err != nil || fwd == nil {
		t.Fatalf("get forward: %v", err)
	}
	if fwd.TunnelID != dstTunnelID {
		t.Fatalf("forward must belong to the new tunnel, got %d", fwd.TunnelID)
	}
	if fwd.RemoteAddr != "127.0.0.1:443" {
		t.Fatalf("R3X-002: managed relay RemoteAddr must be recomputed for the new tunnel (landing exit -> loopback), got %q", fwd.RemoteAddr)
	}
}

// ① 防过修：不切隧道的普通 forwardUpdate 不触发 RemoteAddr 重算、
// 不刷新白名单（存量陈旧 RemoteAddr 原样保留、无新 KernelApply）。
func TestR3B4ForwardUpdateWithoutTunnelSwitchDoesNotResync(t *testing.T) {
	s := setupR1B3Whitelist(t, 2)
	if err := s.repo.DB().Exec(`UPDATE node SET port = '1000-65535'`).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.repo.DB().Exec(`UPDATE forward SET remote_addr = '198.51.100.2:443' WHERE id = ?`, s.forwardID).Error; err != nil {
		t.Fatal(err)
	}
	baseline := s.allowBaseline()
	initial := s.landingRC.kernelApplyAllows()
	initialAllow := initial[len(initial)-1]
	res := httptest.NewRecorder()
	s.h.forwardUpdate(res, newAuthenticatedJSONRequest(t, map[string]interface{}{
		"id": s.forwardID, "name": "relay-fwd-renamed",
	}))
	if code, msg := decodePendingEnvelopeCode(t, res); code != 0 {
		t.Fatalf("forwardUpdate rename failed: code=%d msg=%q body=%s", code, msg, res.Body.String())
	}
	fwd, err := s.h.getForwardRecord(s.forwardID)
	if err != nil || fwd == nil {
		t.Fatalf("get forward: %v", err)
	}
	if fwd.RemoteAddr != "198.51.100.2:443" {
		t.Fatalf("anti-overfix: forwardUpdate without a tunnel switch must not recompute RemoteAddr, got %q", fwd.RemoteAddr)
	}
	// allow 集不得变化：后台 drift 轮询可能重下发同一内容（与漏斗
	// 无关），但任何一次新下发的 allow 都必须与操作前逐字一致。
	for _, allow := range s.newAllowsSince(baseline) {
		if allow != initialAllow {
			t.Fatalf("anti-overfix: whitelist content must not change without a tunnel switch, got %q want %q", allow, initialAllow)
		}
	}
}

// ② cleanupForwardsForUserTunnel（R3X-016）：撤销漏斗删该用户+隧道
// 的组建转发后，落地 allow 收回该隧道的拨号节点地址。
func TestR3B4CleanupForwardsForUserTunnelRefreshesWhitelist(t *testing.T) {
	s := setupR1B3Whitelist(t, 1)
	baseline := s.allowBaseline()
	initial := s.landingRC.kernelApplyAllows()
	if !strings.Contains(initial[len(initial)-1], "198.51.100.1/32") {
		t.Fatalf("setup: initial allow must contain the entry IP, got %v", initial)
	}

	s.h.cleanupForwardsForUserTunnel(1, s.tunnelID)

	if fwd, err := s.h.getForwardRecord(s.forwardID); err == nil && fwd != nil {
		t.Fatalf("cleanup must delete the forward, got %+v", fwd)
	}
	fresh := s.newAllowsSince(baseline)
	if len(fresh) == 0 {
		t.Fatalf("R3X-016: no KernelApply pushed after cleanupForwardsForUserTunnel (whitelist not refreshed)")
	}
	last := fresh[len(fresh)-1]
	if strings.Contains(last, "198.51.100.1/32") {
		t.Fatalf("refreshed allow must drop the cleaned-up tunnel's entry IP, got %s", last)
	}
}

// ③ 删节点（R3X-017，批量入口与单删共用 deleteNodeByID funnel）：
// 经 ChainTunnel 引用被删节点（隧道出口）的他入站 allow 中，被删
// 节点地址消失。
func TestR3B4NodeBatchDeleteRefreshesWhitelist(t *testing.T) {
	s := setupR1B3Whitelist(t, 2)
	baseline := s.allowBaseline()
	initial := s.landingRC.kernelApplyAllows()
	if !strings.Contains(initial[len(initial)-1], "198.51.100.2/32") {
		t.Fatalf("setup: initial allow must contain exit A, got %v", initial)
	}

	res := httptest.NewRecorder()
	s.h.nodeBatchDelete(res, newAuthenticatedJSONRequest(t, map[string]interface{}{
		"ids": []int64{s.exitAID},
	}))
	if code, msg := decodePendingEnvelopeCode(t, res); code != 0 {
		t.Fatalf("nodeBatchDelete failed: code=%d msg=%q body=%s", code, msg, res.Body.String())
	}

	fresh := s.newAllowsSince(baseline)
	if len(fresh) == 0 {
		t.Fatalf("R3X-017: no KernelApply pushed after node delete (whitelist not refreshed)")
	}
	last := fresh[len(fresh)-1]
	if strings.Contains(last, "198.51.100.2/32") {
		t.Fatalf("refreshed allow must drop the deleted node's address, got %s", last)
	}
}

// ③ 链式面（R3X-017）：以被删节点入站为链式出站目标的路径同族——
// 被删节点上的入站曾链式拨号落地入站（chainDialer），删节点后
// 落地 allow 中被删节点地址消失。
func TestR3B4DeleteNodeChainDialerRefreshesWhitelist(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r3b4-chain-node.db")
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
	chainNodeID := seedRelayTargetNode(t, r, "chain-src", "198.51.100.55")
	landingID := seedRelayTargetNode(t, r, "landing", "203.0.113.10")
	if err := r.DB().Exec(`UPDATE node SET status = 1`).Error; err != nil {
		t.Fatal(err)
	}
	landingBox := seedChainKernel(t, r, landingID, kernel.TypeSingbox)
	target := seedChainInbound(t, r, landingBox, "landing-vless", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 443,
		Security: kernel.Security{Type: kernel.SecurityReality}}, 0)
	target.RelayWhitelist = 1
	if err := r.SaveNodeInbound(target); err != nil {
		t.Fatal(err)
	}
	frontBox := seedChainKernel(t, r, chainNodeID, kernel.TypeXray)
	seedChainInbound(t, r, frontBox, "front-vless", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 8443,
		Security: kernel.Security{Type: kernel.SecurityNone}}, target.ID)

	landingRC := &r1b3CommandRecorder{}
	connectR1B3Agent(t, ts, "landing-secret", landingRC)
	waitForCondition(t, 5*time.Second, func() bool {
		return server.IsNodeOnline(landingID)
	}, "landing agent online")
	probe := &r1b3WLScenario{landingRC: landingRC}
	waitForCondition(t, 5*time.Second, func() bool {
		return len(landingRC.kernelApplyAllows()) > 0
	}, "initial kernel apply on reconnect")
	baseline := len(landingRC.kernelApplyAllows())
	initial := landingRC.kernelApplyAllows()
	if !strings.Contains(initial[len(initial)-1], "198.51.100.55/32") {
		t.Fatalf("setup: initial allow must contain the chain dialer IP, got %v", initial)
	}

	if _, err := h.deleteNodeByID(chainNodeID); err != nil {
		t.Fatalf("deleteNodeByID failed: %v", err)
	}
	fresh := probe.newAllowsSince(baseline)
	if len(fresh) == 0 {
		t.Fatalf("R3X-017: no KernelApply pushed after deleteNodeByID removed a chain dialer (whitelist not refreshed)")
	}
	last := fresh[len(fresh)-1]
	if strings.Contains(last, "198.51.100.55/32") {
		t.Fatalf("refreshed allow must drop the deleted chain-dialer node's address, got %s", last)
	}
}

// ④ jobs pending 终删（R3X-018）+ 防过修（N-04）：nft 转发删除在
// 节点不可达时进 pending——pending 窗口内不提前刷新；后台 job
// 终删成功后按节点聚合刷新一次，allow 收回入口节点地址。
func TestR3B4PendingDeleteJobRefreshesWhitelistOnlyAtFinalDelete(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	r := h.repo
	server := ws.NewServer(r, "test-secret")
	server.SetNodeOnlineHook(h.onNodeOnline)
	server.SetNodeOfflineHook(h.notifyNodeOffline)
	h.wsServer = server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		server.ServeHTTP(w, req)
	}))
	t.Cleanup(ts.Close)

	manager := &fakeNftablesManager{reconcileErr: errFakeSSHDown}
	h.nftablesManager = newSerializedNftManager(manager)
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	tunnelID := seedTunnelForNftables(t, h, "nft-tunnel-b4", fixture.nodeID)

	now := time.Now().UnixMilli()
	landingID := seedRelayTargetNode(t, r, "landing", "203.0.113.10")
	if err := r.DB().Exec(`UPDATE node SET status = 1`).Error; err != nil {
		t.Fatal(err)
	}
	landingBox := seedChainKernel(t, r, landingID, kernel.TypeSingbox)
	target := seedChainInbound(t, r, landingBox, "landing-vless", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 443,
		Security: kernel.Security{Type: kernel.SecurityReality}}, 0)
	target.RelayWhitelist = 1
	if err := r.SaveNodeInbound(target); err != nil {
		t.Fatal(err)
	}
	forwardID, err := r.CreateForwardTx(1, "admin", "relay-fwd", tunnelID, "203.0.113.10:443", "fifo", now, 1,
		[]int64{fixture.nodeID}, 20000, "", nil, 0, 0, nil, 0, 0, 0)
	if err != nil {
		t.Fatalf("create forward: %v", err)
	}
	if err := r.CreateInboundRelay(&model.NodeInboundRelay{InboundID: target.ID, ForwardID: forwardID, Name: "relay", Managed: 1, CreatedTime: now}); err != nil {
		t.Fatal(err)
	}

	landingRC := &r1b3CommandRecorder{}
	connectR1B3Agent(t, ts, "landing-secret", landingRC)
	waitForCondition(t, 5*time.Second, func() bool {
		return server.IsNodeOnline(landingID)
	}, "landing agent online")
	probe := &r1b3WLScenario{landingRC: landingRC}
	waitForCondition(t, 5*time.Second, func() bool {
		return len(landingRC.kernelApplyAllows()) > 0
	}, "initial kernel apply on reconnect")
	// R5X-023 deflake (2/3)：基线快照前先等重连波次静默。onNodeOnline
	// 的异步段（reconcileNodeKernels goroutine）可在同一波次尾随推送
	// 多份内容相同的 KernelApply；不等静默就拍 baseline，尾随 apply
	// 会迟到落入 pending 窗口并误杀下方断言。静默判定为事件驱动
	// （轮询计数直至一个轮询间隔内不再增长），不是固定等待掩盖。
	waitForCondition(t, 10*time.Second, func() bool {
		n := len(landingRC.kernelApplyAllows())
		time.Sleep(300 * time.Millisecond)
		return len(landingRC.kernelApplyAllows()) == n
	}, "reconnect wave quiesced before baseline snapshot")
	baseline := len(landingRC.kernelApplyAllows())
	initial := landingRC.kernelApplyAllows()
	initialAllow := initial[len(initial)-1]
	if !strings.Contains(initialAllow, "198.51.100.10/32") {
		t.Fatalf("setup: initial allow must contain the nft entry IP, got %v", initial)
	}

	// 节点不可达：forwardDelete 进 pending（终删未发生）。
	res := httptest.NewRecorder()
	h.forwardDelete(res, newAuthenticatedJSONRequest(t, map[string]any{"id": forwardID}))
	if code, _ := decodePendingEnvelopeCode(t, res); code == 0 {
		t.Fatalf("expected forwardDelete to queue pending while node is down, got success body=%s", res.Body.String())
	}
	if fr, err := h.repo.GetForwardRecord(forwardID); err != nil || fr == nil {
		t.Fatalf("pending window: forward row must survive, got %+v err=%v", fr, err)
	}
	// R5X-023 deflake (1/3)：计数型断言改内容型（与同文件 ①防过修
	// 用例同口径，N-04 语义强度不放宽）——pending 窗口内即使有
	// 重连波次尾随 apply 迟到，其 allow 内容必须与初始逐字一致
	// （仍含待删转发的入口 IP）；任何一份内容变化的刷新都是真刷新。
	for _, allow := range probe.newAllowsSince(baseline) {
		if allow != initialAllow {
			t.Fatalf("N-04 anti-overfix: whitelist content must not refresh during the pending window, got %q want %q", allow, initialAllow)
		}
	}

	// 节点恢复：job 退避到期后终删。
	manager.reconcileErr = nil
	h.retryNftPendingDeletes(time.Now().Add(2 * time.Minute))
	if fr, err := h.repo.GetForwardRecord(forwardID); err != nil || fr != nil {
		t.Fatalf("job must finally delete the forward, got %+v err=%v", fr, err)
	}
	// R5X-023 deflake (3/3)：终删段改事件驱动等待——等「不含入口 IP
	// 的 allow 出现」，替代单次即时快照（消除刷新下发与快照之间的
	// 反向竞态）。
	waitForCondition(t, 5*time.Second, func() bool {
		for _, allow := range probe.newAllowsSince(baseline) {
			if !strings.Contains(allow, "198.51.100.10/32") {
				return true
			}
		}
		return false
	}, "R3X-018: KernelApply without the deleted forward's entry IP after the pending-delete job finalized the delete")
}
