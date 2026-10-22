package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"go-backend/internal/kernel"
	"go-backend/internal/security"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
	"go-backend/internal/ws"
)

// R3 发版门项 5 观察定点回归（GATE-F3）：forwardUpdate 切隧道是
// 「先落库、后下发、失败补偿回滚」的非原子序列——落库与回滚之间，
// DB 处于已切换状态；allow 是当前 DB 的纯函数（buildKernelApplyPayload
// → inboundAllowList 现算），窗口内任何一次落地内核下发（漂移轮询、
// 重连对账、其他操作触发的 applyKernel）都会把「未提交切换」的 allow
// 推给 agent。失败回滚只恢复 DB + 重下发 gost 服务，原实现不在失败
// 路径刷新白名单，且内核 config hash 不含 ports/allow，漂移轮询事后
// 也不会纠正——错误 allow 就此驻留（发版门项 5 真机观察）。
//
// 本用例确定性复刻该序列：front agent 阻塞首个 UpdateService（此刻
// DB 已切换）→ 测试线程显式 applyKernel 充当窗口内的并发下发 →
// 放行 agent 以 bind 错误失败 → 断言：回执错误、DB 逐字回滚、且回滚
// 后落地节点最后一次 KernelApply 的 allow 已恢复为回滚后口径
// （含旧出口 A、不含未提交切换的落地自身拨号）。

type blockingServiceAgent struct {
	arrived chan struct{}
	release chan struct{}
	done    chan struct{}
	armed   atomic.Bool
	once    sync.Once
	cmds    atomic.Int64
}

func connectBlockingServiceAgent(t *testing.T, ts *httptest.Server, secret string, ctl *blockingServiceAgent) {
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
			ctl.cmds.Add(1)
			resp := map[string]interface{}{"type": command.Type, "requestId": command.RequestID, "success": true}
			// arm 之前（上线 redeploy 等）一律正常 ACK，让 setup 落定；
			// arm 之后首个服务创建命令阻塞（打开切换窗口），其余创建
			// 命令直接以 bind 错误失败（与门上 17521 冲突同型）。
			if ctl.armed.Load() && (command.Type == "UpdateService" || command.Type == "AddService") {
				blocked := false
				ctl.once.Do(func() {
					close(ctl.arrived)
					blocked = true
				})
				if blocked {
					select {
					case <-ctl.release:
					case <-ctl.done:
					case <-time.After(60 * time.Second):
					}
				}
				resp["success"] = false
				resp["message"] = "create service failed: listen tcp 0.0.0.0:20000: bind: address already in use"
			}
			if err := conn.WriteJSON(resp); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		close(ctl.done)
		_ = conn.Close()
		<-done
	})
}

func TestR3GateF3FailedTunnelSwitchRepushesWhitelistAfterRollback(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r3-gate-f3.db")
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
	frontID := seedRelayTargetNode(t, r, "front", "198.51.100.1")
	exitAID := seedRelayTargetNode(t, r, "exit-a", "198.51.100.2")
	landingID := seedRelayTargetNode(t, r, "landing", "203.0.113.10")
	if err := r.DB().Exec(`UPDATE node SET status = 1`).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Exec(`UPDATE node SET port = '1000-65535'`).Error; err != nil {
		t.Fatal(err)
	}
	tunnelID := seedRelayTargetTunnel(t, r, "wl-tunnel", frontID, 30000, exitAID)
	landingBox := seedChainKernel(t, r, landingID, kernel.TypeSingbox)
	target := seedChainInbound(t, r, landingBox, "landing-vless", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 443,
		Security: kernel.Security{Type: kernel.SecurityReality}}, 0)
	target.RelayWhitelist = 1
	if err := r.SaveNodeInbound(target); err != nil {
		t.Fatal(err)
	}
	forwardID, err := r.CreateForwardTx(1, "admin", "relay-fwd", tunnelID, "203.0.113.10:443", "fifo", now, 1,
		[]int64{frontID}, 20000, "", nil, 0, 0, nil, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.CreateInboundRelay(&model.NodeInboundRelay{InboundID: target.ID, ForwardID: forwardID, Name: "relay", Managed: 1, CreatedTime: now}); err != nil {
		t.Fatal(err)
	}
	dstTunnelID := seedRelayTargetTunnel(t, r, "wl-tunnel-landing", frontID, 30002, landingID)

	landingRC := &r1b3CommandRecorder{}
	ctl := &blockingServiceAgent{arrived: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{})}
	connectBlockingServiceAgent(t, ts, "front-secret", ctl)
	connectR1B3Agent(t, ts, "landing-secret", landingRC)
	waitForCondition(t, 5*time.Second, func() bool {
		return server.IsNodeOnline(frontID) && server.IsNodeOnline(landingID)
	}, "mock agents online")
	waitForCondition(t, 5*time.Second, func() bool {
		return len(landingRC.kernelApplyAllows()) > 0
	}, "initial kernel apply on reconnect")
	initial := landingRC.kernelApplyAllows()
	if !strings.Contains(initial[len(initial)-1], "198.51.100.2/32") {
		t.Fatalf("setup: initial allow must contain exit A, got %v", initial)
	}

	// 等 front 侧上线 redeploy 的命令流静默后再 arm，避免在途命令
	// 跨越 arm 边界被误当作切换窗口内的下发。
	waitForCondition(t, 5*time.Second, func() bool {
		n := ctl.cmds.Load()
		time.Sleep(400 * time.Millisecond)
		return ctl.cmds.Load() == n
	}, "front agent command stream quiescent")

	// 切隧道：DB 先落库，服务下发阻塞在 front agent 处（窗口打开）。
	ctl.armed.Store(true)
	resCh := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		res := httptest.NewRecorder()
		h.forwardUpdate(res, newAuthenticatedJSONRequest(t, map[string]interface{}{
			"id": forwardID, "tunnelId": dstTunnelID,
		}))
		resCh <- res
	}()
	select {
	case <-ctl.arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("forwardUpdate never reached the service sync on the entry node")
	}

	// 窗口内并发下发（漂移轮询/重连对账/其他操作的等价物）：此刻 DB
	// 已切换，allow 按未提交状态现算——落地自身成为拨号者、出口 A 消失。
	if fwdNow, ferr := h.getForwardRecord(forwardID); ferr != nil || fwdNow == nil || fwdNow.TunnelID != dstTunnelID {
		t.Fatalf("window precondition: forward must already be on the target tunnel in DB, got %+v err=%v", fwdNow, ferr)
	}
	if err := h.applyKernel(landingBox.ID, false); err != nil {
		t.Fatalf("window applyKernel: %v", err)
	}
	windowAllows := landingRC.kernelApplyAllows()
	windowLast := windowAllows[len(windowAllows)-1]
	if strings.Contains(windowLast, "198.51.100.2/32") || !strings.Contains(windowLast, "203.0.113.10/32") {
		t.Fatalf("window apply must reflect the transient DB state (landing as dialer, exit A gone), got %q", windowLast)
	}

	// 放行失败 → 同步失败 → 面板回滚并返回错误。
	close(ctl.release)
	var res *httptest.ResponseRecorder
	select {
	case res = <-resCh:
	case <-time.After(30 * time.Second):
		t.Fatal("forwardUpdate did not return after the agent failure was released")
	}
	if code, _ := decodePendingEnvelopeCode(t, res); code == 0 {
		t.Fatalf("forwardUpdate must be rejected when service sync fails, got success body=%s", res.Body.String())
	}

	// DB 逐字回滚。
	fwd, err := h.getForwardRecord(forwardID)
	if err != nil || fwd == nil {
		t.Fatalf("get forward: %v", err)
	}
	if fwd.TunnelID != tunnelID {
		t.Fatalf("rollback: forward tunnel must stay on the original tunnel, got %d want %d", fwd.TunnelID, tunnelID)
	}
	ports, err := h.listForwardPorts(forwardID)
	if err != nil || len(ports) != 1 || ports[0].NodeID != frontID || ports[0].Port != 20000 {
		t.Fatalf("rollback: forward ports must be unchanged, got %+v err=%v", ports, err)
	}

	// 核心断言：回滚完成后，落地节点最后一次收到的 KernelApply 必须已
	// 按回滚后 DB 恢复 allow（含出口 A、不含落地自身拨号）——即失败路径
	// 必须在回滚后重推白名单，纠正窗口内的暂态下发。
	finalAllows := landingRC.kernelApplyAllows()
	last := finalAllows[len(finalAllows)-1]
	if !strings.Contains(last, "198.51.100.2/32") || strings.Contains(last, "203.0.113.10/32") {
		t.Fatalf("GATE-F3: after rollback the last KernelApply must carry the restored allow (exit A present, transient dialer gone), got %q (all: %v)", last, finalAllows)
	}
}
