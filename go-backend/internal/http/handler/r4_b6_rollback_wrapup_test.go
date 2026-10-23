package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"go-backend/internal/kernel"
	"go-backend/internal/security"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
	"go-backend/internal/ws"
)

// R4 批 6 · R4X-003（P2）/ R4X-011（P3）回滚收尾回归（verdicts §3）。
//
// R4X-003：删隧道 replace 动作的两条回滚返回路径（execute 层迁移失败、
// 删源隧道失败）在回滚后必须按执行前收集集 affectedInboundIDs 补刷一次
// 白名单；否则窗口内被暂态 DB 推出的错误 allow（或从未被纠正的旧
// allow 物化缺失）无限驻留。两形态均以落地节点收到的 KernelApply
// 为断言面（与 GATE-F3 / R1-B3 同口径）。
//
// R4X-011：单条 UT assign/update 下发中途失败、DB 回滚后，必须按回滚
// 后 DB 对本条 UT 的转发集重下发（对齐批量路径整批重跑正本）；否则
// 失败点之前已按新口径下发成功的转发与回滚后 DB 分叉。本形态以
// 先行成功节点收到的服务载荷（limiter 字段）为断言面。

// ─── R4X-003 共用场景 ──────────────────────────────────────────────────

type r4b6TunnelScenario struct {
	h           *Handler
	repo        *repo.Repository
	server      *ws.Server
	frontID     int64
	exitAID     int64
	landingID   int64
	srcTunnelID int64
	dstTunnelID int64
	forwardID   int64
	kernelID    int64
	landingRC   *r1b3CommandRecorder
	frontRC     *r1b3CommandRecorder
	frontCtl    *blockingServiceAgent
}

// setupR4B6TunnelScenario 复刻 GATE-F3 场景：front 入口、exit-a 出口的
// 源隧道挂一条 Managed relay 转发拨向 landing 入站（RelayWhitelist=1）；
// 目标隧道出口为 landing 自身。frontMode="blockfail" 时 front agent 为
// F3 阻塞失败型；"ack" 时为全 ACK 记录型。landing 恒为记录型 agent。
func setupR4B6TunnelScenario(t *testing.T, frontMode string) *r4b6TunnelScenario {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/r4b6-tunnel.db")
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

	s := &r4b6TunnelScenario{h: h, repo: r, server: server, landingRC: &r1b3CommandRecorder{}}
	now := time.Now().UnixMilli()
	if _, err := r.CreateUser("admin", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now); err != nil {
		t.Fatalf("create user: %v", err)
	}
	s.frontID = seedRelayTargetNode(t, r, "front", "198.51.100.1")
	s.exitAID = seedRelayTargetNode(t, r, "exit-a", "198.51.100.2")
	s.landingID = seedRelayTargetNode(t, r, "landing", "203.0.113.10")
	if err := r.DB().Exec(`UPDATE node SET status = 1`).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Exec(`UPDATE node SET port = '1000-65535'`).Error; err != nil {
		t.Fatal(err)
	}
	s.srcTunnelID = seedRelayTargetTunnel(t, r, "r4b6-src", s.frontID, 30000, s.exitAID)
	s.dstTunnelID = seedRelayTargetTunnel(t, r, "r4b6-dst", s.frontID, 30002, s.landingID)
	landingBox := seedChainKernel(t, r, s.landingID, kernel.TypeSingbox)
	s.kernelID = landingBox.ID
	target := seedChainInbound(t, r, landingBox, "landing-vless", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 443,
		Security: kernel.Security{Type: kernel.SecurityReality}}, 0)
	target.RelayWhitelist = 1
	if err := r.SaveNodeInbound(target); err != nil {
		t.Fatal(err)
	}
	forwardID, err := r.CreateForwardTx(1, "admin", "relay-fwd", s.srcTunnelID, "203.0.113.10:443", "fifo", now, 1,
		[]int64{s.frontID}, 20000, "", nil, 0, 0, nil, 0, 0, 0)
	if err != nil {
		t.Fatalf("create forward: %v", err)
	}
	s.forwardID = forwardID
	if err := r.CreateInboundRelay(&model.NodeInboundRelay{InboundID: target.ID, ForwardID: forwardID, Name: "relay", Managed: 1, CreatedTime: now}); err != nil {
		t.Fatal(err)
	}

	if frontMode == "blockfail" {
		s.frontCtl = &blockingServiceAgent{arrived: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{})}
		connectBlockingServiceAgent(t, ts, "front-secret", s.frontCtl)
	} else {
		s.frontRC = &r1b3CommandRecorder{}
		connectR1B3Agent(t, ts, "front-secret", s.frontRC)
	}
	connectR1B3Agent(t, ts, "landing-secret", s.landingRC)
	waitForCondition(t, 5*time.Second, func() bool {
		return server.IsNodeOnline(s.frontID) && server.IsNodeOnline(s.landingID)
	}, "mock agents online")
	waitForCondition(t, 5*time.Second, func() bool {
		return len(s.landingRC.kernelApplyAllows()) > 0
	}, "initial kernel apply on reconnect")
	// 等上线对账的 KernelApply 流静默后再返回，避免在途的初始下发
	// 落进用例的操作窗被误判为操作触发的刷新。
	waitForCondition(t, 5*time.Second, func() bool {
		n := len(s.landingRC.kernelApplyAllows())
		time.Sleep(400 * time.Millisecond)
		return len(s.landingRC.kernelApplyAllows()) == n
	}, "landing kernel apply stream quiescent")
	initial := s.landingRC.kernelApplyAllows()
	if !strings.Contains(initial[len(initial)-1], "198.51.100.2/32") {
		t.Fatalf("setup: initial allow must contain exit A, got %v", initial)
	}
	return s
}

func (s *r4b6TunnelScenario) assertForwardRestoredToSource(t *testing.T) {
	t.Helper()
	fwd, err := s.h.getForwardRecord(s.forwardID)
	if err != nil || fwd == nil {
		t.Fatalf("get forward: %v", err)
	}
	if fwd.TunnelID != s.srcTunnelID {
		t.Fatalf("rollback: forward must be back on the source tunnel, got %d want %d", fwd.TunnelID, s.srcTunnelID)
	}
	if fwd.RemoteAddr != "203.0.113.10:443" {
		t.Fatalf("rollback: forward RemoteAddr must be restored, got %q", fwd.RemoteAddr)
	}
	ports, err := s.h.listForwardPorts(s.forwardID)
	if err != nil || len(ports) != 1 || ports[0].NodeID != s.frontID || ports[0].Port != 20000 {
		t.Fatalf("rollback: forward ports must be unchanged, got %+v err=%v", ports, err)
	}
}

// 形态 1（迁移中途失败）：迁移的服务下发在 front agent 处阻塞时，DB
// 已处于暂态迁移状态；测试线程显式 applyKernel 充当窗口内并发下发，
// 把暂态 allow（落地自身拨号、出口 A 消失）推给 landing。放行失败 →
// execute 回滚 → process 返回 failures。断言回滚后 landing 最后一次
// KernelApply 已恢复为回滚后口径（修复前最后一次仍是暂态 allow）。
func TestR4B6TunnelReplaceMigrationFailureRepushesWhitelist(t *testing.T) {
	s := setupR4B6TunnelScenario(t, "blockfail")
	ctl := s.frontCtl
	waitForCondition(t, 5*time.Second, func() bool {
		n := ctl.cmds.Load()
		time.Sleep(400 * time.Millisecond)
		return ctl.cmds.Load() == n
	}, "front agent command stream quiescent")

	ctl.armed.Store(true)
	resCh := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		res := httptest.NewRecorder()
		s.h.tunnelDeleteWithForwards(res, newAuthenticatedJSONRequest(t, map[string]interface{}{
			"id": s.srcTunnelID, "action": "replace", "targetTunnelId": s.dstTunnelID,
		}))
		resCh <- res
	}()
	select {
	case <-ctl.arrived:
	case <-time.After(15 * time.Second):
		t.Fatal("migration never reached the service sync on the entry node")
	}
	if fwdNow, ferr := s.h.getForwardRecord(s.forwardID); ferr != nil || fwdNow == nil || fwdNow.TunnelID != s.dstTunnelID {
		t.Fatalf("window precondition: forward must already be on the target tunnel in DB, got %+v err=%v", fwdNow, ferr)
	}
	if err := s.h.applyKernel(s.kernelID, false); err != nil {
		t.Fatalf("window applyKernel: %v", err)
	}
	windowAllows := s.landingRC.kernelApplyAllows()
	windowLast := windowAllows[len(windowAllows)-1]
	if strings.Contains(windowLast, "198.51.100.2/32") || !strings.Contains(windowLast, "203.0.113.10/32") {
		t.Fatalf("window apply must reflect the transient DB state (landing as dialer, exit A gone), got %q", windowLast)
	}

	close(ctl.release)
	var res *httptest.ResponseRecorder
	select {
	case res = <-resCh:
	case <-time.After(30 * time.Second):
		t.Fatal("tunnelDeleteWithForwards did not return after the agent failure was released")
	}
	if code, _ := decodePendingEnvelopeCode(t, res); code == 0 {
		t.Fatalf("replace must fail when migration sync fails, got success body=%s", res.Body.String())
	}
	s.assertForwardRestoredToSource(t)
	if tun, err := s.h.getTunnelRecord(s.srcTunnelID); err != nil || tun == nil {
		t.Fatalf("source tunnel must survive a failed migration, got %+v err=%v", tun, err)
	}
	finalAllows := s.landingRC.kernelApplyAllows()
	last := finalAllows[len(finalAllows)-1]
	if !strings.Contains(last, "198.51.100.2/32") || strings.Contains(last, "203.0.113.10/32") {
		t.Fatalf("R4X-003(a): after migration rollback the last KernelApply must carry the restored allow (exit A present, transient dialer gone), got %q (all: %v)", last, finalAllows)
	}
}

// 形态 2（删源隧道失败）：迁移全部成功后，以 SQLite 触发器注入源隧道
// 行删除失败（级联事务整笔回滚）。断言：源隧道保留、转发回滚回源
// 隧道、且回滚后 landing 收到至少一次新 KernelApply，其 allow 为
// 回滚后口径（修复前全程无任何 KernelApply 到 landing）。
func TestR4B6TunnelReplaceDeleteSourceFailureRepushesWhitelist(t *testing.T) {
	s := setupR4B6TunnelScenario(t, "ack")
	baseline := len(s.landingRC.kernelApplyAllows())

	trigger := fmt.Sprintf(`CREATE TRIGGER r4b6_fail_tunnel_delete BEFORE DELETE ON tunnel FOR EACH ROW WHEN OLD.id = %d BEGIN SELECT RAISE(ABORT, 'r4b6 injected source tunnel delete failure'); END`, s.srcTunnelID)
	if err := s.repo.DB().Exec(trigger).Error; err != nil {
		t.Fatalf("install delete-failure trigger: %v", err)
	}

	res := httptest.NewRecorder()
	s.h.tunnelDeleteWithForwards(res, newAuthenticatedJSONRequest(t, map[string]interface{}{
		"id": s.srcTunnelID, "action": "replace", "targetTunnelId": s.dstTunnelID,
	}))
	if code, _ := decodePendingEnvelopeCode(t, res); code == 0 {
		t.Fatalf("replace must fail when deleting the source tunnel fails, got success body=%s", res.Body.String())
	}
	if tun, err := s.h.getTunnelRecord(s.srcTunnelID); err != nil || tun == nil {
		t.Fatalf("source tunnel must survive its failed delete, got %+v err=%v", tun, err)
	}
	s.assertForwardRestoredToSource(t)

	allows := s.landingRC.kernelApplyAllows()
	if len(allows) <= baseline {
		t.Fatalf("R4X-003(b): no KernelApply pushed after delete-source rollback (whitelist not repushed), baseline=%d total=%d", baseline, len(allows))
	}
	last := allows[len(allows)-1]
	if !strings.Contains(last, "198.51.100.2/32") || strings.Contains(last, "203.0.113.10/32") {
		t.Fatalf("R4X-003(b): repushed allow must be the rolled-back allow (exit A present, landing dialer gone), got %q", last)
	}
}

// ─── R4X-011 共用场景 ──────────────────────────────────────────────────

// connectR4B6FailingServiceAgent 同 connectR1B3Agent，但对服务下发命令
// （UpdateService/AddService）恒回失败（非离线、非 not-found 的真实
// 失败文案），其余命令 ACK 成功——用于在 syncUserTunnelForwards 的
// 第二条转发处注入「中途失败」：第一条已按新口径下发成功、第二条失败。
func connectR4B6FailingServiceAgent(t *testing.T, ts *httptest.Server, secret string) {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"?type=1&secret="+secret, nil)
	if err != nil {
		t.Fatalf("dial failing mock agent: %v", err)
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
			resp := map[string]interface{}{"type": command.Type, "requestId": command.RequestID, "success": true}
			if command.Type == "UpdateService" || command.Type == "AddService" {
				resp["success"] = false
				resp["message"] = "r4b6 simulated dispatch failure: permission denied"
			}
			if err := conn.WriteJSON(resp); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = conn.Close()
		<-done
	})
}

type r4b6UTScenario struct {
	h         *Handler
	repo      *repo.Repository
	server    *ws.Server
	tunnelID  int64
	nodeAID   int64
	nodeBID   int64
	forward1  int64
	forward2  int64
	nodeARC   *r1b3CommandRecorder
	speedOld  int64
	speedNew  int64
}

// setupR4B6UTScenario：type-1 隧道双入口（A/B）；user 1 的两条转发
// 分别落在 A（先行、agent 全 ACK 并记录）与 B（服务下发恒失败）。
// syncUserTunnelForwards 按 forward id 升序逐条下发，故恒为 fwd1
// 成功、fwd2 失败的「中途失败」形态。
func setupR4B6UTScenario(t *testing.T) *r4b6UTScenario {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/r4b6-ut.db")
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

	s := &r4b6UTScenario{h: h, repo: r, server: server, nodeARC: &r1b3CommandRecorder{}}
	now := time.Now().UnixMilli()
	if _, err := r.CreateUser("admin", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now); err != nil {
		t.Fatalf("create user: %v", err)
	}
	s.nodeAID = seedRelayTargetNode(t, r, "ut-a", "198.51.100.21")
	s.nodeBID = seedRelayTargetNode(t, r, "ut-b", "198.51.100.22")
	if err := r.DB().Exec(`UPDATE node SET status = 1`).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Exec(`UPDATE node SET port = '1000-65535'`).Error; err != nil {
		t.Fatal(err)
	}
	tx := r.BeginTx()
	if tx == nil || tx.Error != nil {
		t.Fatalf("begin tx: %v", tx.Error)
	}
	tunnelID, err := r.CreateTunnelTx(tx, "r4b6-ut-tunnel", 1, 1, 1, now, 1, nil, 1, "", "", 0)
	if err != nil {
		t.Fatalf("create tunnel: %v", err)
	}
	for _, nodeID := range []int64{s.nodeAID, s.nodeBID} {
		if err := r.CreateChainTunnelTx(tx, tunnelID, "1", nodeID, sql.NullInt64{}, "", 1, "tls", ""); err != nil {
			t.Fatalf("create chain tunnel: %v", err)
		}
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatalf("commit tunnel: %v", err)
	}
	s.tunnelID = tunnelID
	fwd1, err := r.CreateForwardTx(1, "admin", "ut-fwd-1", tunnelID, "203.0.113.20:443", "fifo", now, 1,
		[]int64{s.nodeAID}, 20001, "", nil, 0, 0, nil, 0, 0, 0)
	if err != nil {
		t.Fatalf("create forward1: %v", err)
	}
	s.forward1 = fwd1
	fwd2, err := r.CreateForwardTx(1, "admin", "ut-fwd-2", tunnelID, "203.0.113.20:443", "fifo", now, 1,
		[]int64{s.nodeBID}, 20002, "", nil, 0, 0, nil, 0, 0, 0)
	if err != nil {
		t.Fatalf("create forward2: %v", err)
	}
	s.forward2 = fwd2
	s.speedOld = seedG3SpeedLimit(t, r, "r4b6-old")
	s.speedNew = seedG3SpeedLimit(t, r, "r4b6-new")

	connectR1B3Agent(t, ts, "ut-a-secret", s.nodeARC)
	connectR4B6FailingServiceAgent(t, ts, "ut-b-secret")
	waitForCondition(t, 5*time.Second, func() bool {
		return server.IsNodeOnline(s.nodeAID) && server.IsNodeOnline(s.nodeBID)
	}, "ut mock agents online")
	// 等上线对账的命令流静默后再取基线。
	waitForCondition(t, 5*time.Second, func() bool {
		n := len(s.nodeARC.snapshot())
		time.Sleep(400 * time.Millisecond)
		return len(s.nodeARC.snapshot()) == n
	}, "node A command stream quiescent")
	return s
}

// lastForward1Limiter 返回节点 A 记录中最后一条 fwd1 服务下发载荷里
// tcp 服务的 limiter 值（无 limiter 时返回 "" 与 true）；从未下发过
// fwd1 服务时 ok=false。
func (s *r4b6UTScenario) lastForward1Limiter() (string, bool) {
	var last string
	found := false
	namePrefix := fmt.Sprintf(`"name":"%d_1_`, s.forward1)
	for _, c := range s.nodeARC.snapshot() {
		if c.Type != "UpdateService" && c.Type != "AddService" {
			continue
		}
		if !strings.Contains(string(c.Data), namePrefix) {
			continue
		}
		var services []struct {
			Name    string  `json:"name"`
			Limiter *string `json:"limiter"`
		}
		if err := json.Unmarshal(c.Data, &services); err != nil {
			continue
		}
		for _, svc := range services {
			if strings.HasPrefix(svc.Name, fmt.Sprintf("%d_1_", s.forward1)) && strings.HasSuffix(svc.Name, "_tcp") {
				if svc.Limiter != nil {
					last = *svc.Limiter
				} else {
					last = ""
				}
				found = true
			}
		}
	}
	return last, found
}

// 形态 3a（单条 assign 中途失败）：assign 带 speedId=New 落库后逐条
// 下发——fwd1 已按新 UT（limiter=New）下发成功、fwd2 失败 → DB 回滚
// （UT 行删除）。断言回滚后 fwd1 最后一次下发已按回滚后 DB 重下发
// （无 UT → 无 limiter），而非停留在新口径。
func TestR4B6SingleAssignSyncFailureRedispatchesAfterRollback(t *testing.T) {
	s := setupR4B6UTScenario(t)

	res := httptest.NewRecorder()
	s.h.userTunnelAssign(res, newAuthenticatedJSONRequest(t, map[string]interface{}{
		"userId": 1, "tunnelId": s.tunnelID, "speedId": s.speedNew,
	}))
	if code, _ := decodePendingEnvelopeCode(t, res); code == 0 {
		t.Fatalf("assign must fail when the second forward dispatch fails, got success body=%s", res.Body.String())
	}
	var utCount int
	if err := s.repo.DB().Raw(`SELECT COUNT(*) FROM user_tunnel WHERE user_id = 1 AND tunnel_id = ?`, s.tunnelID).Scan(&utCount).Error; err != nil {
		t.Fatal(err)
	}
	if utCount != 0 {
		t.Fatalf("rollback: user_tunnel row must be deleted after failed assign, got %d", utCount)
	}
	limiter, found := s.lastForward1Limiter()
	if !found {
		t.Fatal("setup: forward1 must have been dispatched to node A at least once")
	}
	if limiter != "" {
		t.Fatalf("R4X-011(assign): after rollback the last dispatch of forward1 must reflect the rolled-back DB (no user_tunnel -> no limiter), got limiter=%q (speedNew=%d)", limiter, s.speedNew)
	}
}

// 形态 3b（单条 update 中途失败）：存量 UT 的 speedId 由 Old 改 New，
// 下发中途失败 → DB 回滚恢复 Old。断言 fwd1 最后一次下发已按回滚
// 后 DB 重下发（limiter=Old），而非停留在 New。
func TestR4B6SingleUpdateSyncFailureRedispatchesAfterRollback(t *testing.T) {
	s := setupR4B6UTScenario(t)
	if err := s.repo.DB().Exec(`INSERT INTO user_tunnel(user_id, tunnel_id, speed_id, num, flow, flow_mib, in_flow, out_flow, flow_reset_time, exp_time, status) VALUES(1, ?, ?, 10, 100, 0, 0, 0, 0, 0, 1)`,
		s.tunnelID, s.speedOld).Error; err != nil {
		t.Fatalf("seed user_tunnel: %v", err)
	}
	var utID int64
	if err := s.repo.DB().Raw(`SELECT id FROM user_tunnel WHERE user_id = 1 AND tunnel_id = ?`, s.tunnelID).Scan(&utID).Error; err != nil || utID <= 0 {
		t.Fatalf("read seeded user_tunnel id: %v", err)
	}

	res := httptest.NewRecorder()
	s.h.userTunnelUpdate(res, newAuthenticatedJSONRequest(t, map[string]interface{}{
		"id": utID, "speedId": s.speedNew,
	}))
	if code, _ := decodePendingEnvelopeCode(t, res); code == 0 {
		t.Fatalf("update must fail when the second forward dispatch fails, got success body=%s", res.Body.String())
	}
	_, _, _, _, _, dbSpeedID, _, err := s.repo.GetExistingUserTunnel(1, s.tunnelID)
	if err != nil {
		t.Fatal(err)
	}
	if !dbSpeedID.Valid || dbSpeedID.Int64 != s.speedOld {
		t.Fatalf("rollback: user_tunnel speed_id must be restored to old (%d), got %+v", s.speedOld, dbSpeedID)
	}
	limiter, found := s.lastForward1Limiter()
	if !found {
		t.Fatal("setup: forward1 must have been dispatched to node A at least once")
	}
	if want := fmt.Sprintf("%d", s.speedOld); limiter != want {
		t.Fatalf("R4X-011(update): after rollback the last dispatch of forward1 must reflect the rolled-back DB (limiter=%s), got limiter=%q (speedNew=%d)", want, limiter, s.speedNew)
	}
}

// 成功路径防过修（R4X-011）：两条转发均可达时单条 assign 成功，且
// 不发生回滚重下发——节点 A 收到的 fwd1 下发次数恰为上线对账基线
// +1（assign 的一次同步），limiter 为新口径。
func TestR4B6SingleAssignSuccessPathDispatchesExactlyOnce(t *testing.T) {
	s := setupR4B6UTScenario(t)
	// 让 B 也可达：断开恒失败 agent 不现实，改为在本场景内把 fwd2 删
	// 除后只剩 fwd1——assign 成功路径的下发计数断言只针对 fwd1。
	// （B 的失败注入只影响 fwd2；删 fwd2 后 assign 不再触达 B。）
	if err := s.h.deleteForwardByID(s.forward2); err != nil {
		t.Fatalf("delete forward2: %v", err)
	}
	countFwd1Dispatches := func() int {
		n := 0
		namePrefix := fmt.Sprintf(`"name":"%d_1_`, s.forward1)
		for _, c := range s.nodeARC.snapshot() {
			if (c.Type == "UpdateService" || c.Type == "AddService") && strings.Contains(string(c.Data), namePrefix) {
				n++
			}
		}
		return n
	}
	baseline := countFwd1Dispatches()
	res := httptest.NewRecorder()
	s.h.userTunnelAssign(res, newAuthenticatedJSONRequest(t, map[string]interface{}{
		"userId": 1, "tunnelId": s.tunnelID, "speedId": s.speedNew,
	}))
	if code, msg := decodePendingEnvelopeCode(t, res); code != 0 {
		t.Fatalf("assign on the success path must succeed: code=%d msg=%q body=%s", code, msg, res.Body.String())
	}
	if got := countFwd1Dispatches() - baseline; got != 1 {
		t.Fatalf("success path must dispatch forward1 exactly once (no rollback re-dispatch), got %d extra dispatches", got)
	}
	if limiter, found := s.lastForward1Limiter(); !found || limiter != fmt.Sprintf("%d", s.speedNew) {
		t.Fatalf("success path: forward1 limiter must be the new speed limit, got %q found=%v", limiter, found)
	}
}
