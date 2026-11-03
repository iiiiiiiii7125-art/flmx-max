package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"go-backend/internal/kernel"
	"go-backend/internal/security"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R9-06 / R9-08（I-F2-01 / I-F2-03）回归钉：
//   - 删节点漏斗接入 L2-10 在途登记：升级/回退在途时删除被拒并点名
//     在途操作（单删与批删），登记不被删除偷走/释放；删除占用期间
//     升级类入口同理被拒（op「删除」）。
//   - 三个删除面的守卫—删除非原子族：落库前按最终引用集复检——
//     内核 remove（守卫后、下发窗口内插入引用 → 非 Force 被挡点名 /
//     Force 点名后照删）、入站删除（单删与批删，relay 清理窗口内插
//     入引用 → 被挡点名）、节点删除（连带卸载窗口内插入跨节点引用
//     → 删除照常完成但回执点名新引用）。

// ---------- 单删信封（保留 msg） ----------

type r9NodeDeleteResp struct {
	Code int                    `json:"code"`
	Msg  string                 `json:"msg"`
	Data map[string]interface{} `json:"data"`
}

func r9PostNodeDelete(t *testing.T, h *Handler, body string) r9NodeDeleteResp {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/node/delete", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.nodeDelete(rec, req)
	var resp r9NodeDeleteResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return resp
}

// ---------- R9-06：删除漏斗 × 在途登记 ----------

func TestR9NodeDeleteBlockedByInFlightUpgradeOp(t *testing.T) {
	r := openUninstallHandlerRepo(t)
	h := New(r, "test-secret")
	node := seedUninstallNode(t, r, "race-node", "secret-race-node")
	sends := 0
	stubCountingNodeAgentChannel(t, true, &sends)

	// 升级在途：登记被 nodeUpgrade 持有（下发到回执之间）。
	if _, ok := h.tryAcquireNodeUpgradeOp(node.ID, "升级"); !ok {
		t.Fatal("precondition: acquire upgrade op")
	}

	resp := r9PostNodeDelete(t, h, `{"id":`+nodeIDStr(node.ID)+`,"uninstall":true}`)
	if resp.Code == 0 {
		t.Fatalf("R9-06: delete during in-flight upgrade must be rejected, got success resp=%+v", resp)
	}
	if !strings.Contains(resp.Msg, "升级") || !strings.Contains(resp.Msg, "正在进行") {
		t.Fatalf("R9-06: rejection must name the in-flight op, got msg=%q", resp.Msg)
	}
	if sends != 0 {
		t.Fatalf("R9-06: UninstallAgent dispatched %d times despite in-flight upgrade, want 0", sends)
	}
	if got, err := r.GetNodeByID(node.ID); err != nil || got == nil {
		t.Fatalf("R9-06: node row must survive the rejected delete: node=%v err=%v", got, err)
	}
	// 登记仍属在途升级：删除不得偷走或释放它。
	if existing, ok := h.tryAcquireNodeUpgradeOp(node.ID, "回退"); ok || existing != "升级" {
		t.Fatalf("R9-06: registry after rejected delete: existing=%q ok=%v, want 升级/false", existing, ok)
	}

	// 在途结算后删除恢复畅通，且删除占用期间（op「删除」）的释放
	// 由级联段完成——删后登记可被新操作正常取得。
	h.releaseNodeUpgradeOp(node.ID)
	resp = r9PostNodeDelete(t, h, `{"id":`+nodeIDStr(node.ID)+`,"uninstall":true}`)
	if resp.Code != 0 {
		t.Fatalf("R9-06: delete after upgrade settled must succeed, got code=%d msg=%q", resp.Code, resp.Msg)
	}
	if sends != 1 {
		t.Fatalf("R9-06: UninstallAgent dispatches after settle = %d, want 1", sends)
	}
	if got, _ := r.GetNodeByID(node.ID); got != nil {
		t.Fatalf("R9-06: node row must be deleted after settle, got %v", got)
	}
	if _, ok := h.tryAcquireNodeUpgradeOp(node.ID, "升级"); !ok {
		t.Fatal("R9-06: registry must be free after the delete funnel released its 删除 op")
	}
	h.releaseNodeUpgradeOp(node.ID)
}

type r9BatchDeleteResp struct {
	Code int `json:"code"`
	Data struct {
		SuccessCount int                  `json:"successCount"`
		FailCount    int                  `json:"failCount"`
		Failures     []batchFailureDetail `json:"failures"`
	} `json:"data"`
}

func TestR9NodeBatchDeleteBlockedByInFlightOp(t *testing.T) {
	r := openUninstallHandlerRepo(t)
	h := New(r, "test-secret")
	blocked := seedUninstallNode(t, r, "batch-blocked", "secret-batch-blocked")
	plain := seedUninstallNode(t, r, "batch-plain", "secret-batch-plain")

	if _, ok := h.tryAcquireNodeUpgradeOp(blocked.ID, "回退"); !ok {
		t.Fatal("precondition: acquire rollback op")
	}
	defer h.releaseNodeUpgradeOp(blocked.ID)

	res := httptest.NewRecorder()
	h.nodeBatchDelete(res, newAuthenticatedJSONRequest(t, map[string]interface{}{
		"ids": []int64{blocked.ID, plain.ID}, "uninstall": true,
	}))
	var env r9BatchDeleteResp
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode batch response: %v body=%s", err, res.Body.String())
	}
	if env.Data.SuccessCount != 1 || env.Data.FailCount != 1 {
		t.Fatalf("R9-06: batch delete counts = %d/%d, want 1 success (plain) + 1 fail (in-flight), body=%s",
			env.Data.SuccessCount, env.Data.FailCount, res.Body.String())
	}
	named := false
	for _, failure := range env.Data.Failures {
		if failure.ID == blocked.ID && strings.Contains(failure.Reason, "回退") && strings.Contains(failure.Reason, "正在进行") {
			named = true
		}
	}
	if !named {
		t.Fatalf("R9-06: batch failure for the in-flight node must name the op, failures=%+v", env.Data.Failures)
	}
	if got, _ := r.GetNodeByID(blocked.ID); got == nil {
		t.Fatal("R9-06: in-flight node must survive the batch delete")
	}
	if got, _ := r.GetNodeByID(plain.ID); got != nil {
		t.Fatal("R9-06: plain node must be deleted by the batch delete")
	}
}

// ---------- 可控 mock agent（指定命令类型挂起至测试释放） ----------

type r9HoldAgent struct {
	heldCh      chan string
	releaseCh   chan struct{}
	releaseOnce sync.Once
}

func (a *r9HoldAgent) release() {
	a.releaseOnce.Do(func() { close(a.releaseCh) })
}

func (a *r9HoldAgent) waitHeld(t *testing.T, want string) {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		select {
		case got := <-a.heldCh:
			if got == want {
				return
			}
		case <-deadline:
			t.Fatalf("mock agent never received held command %q", want)
		}
	}
}

// startR9HoldAgent 连接一个 mock 节点：holdType 命令到达时先通知
// 测试并挂起，测试 release 后才回成功；其余命令立即回成功。
func startR9HoldAgent(t *testing.T, baseURL, secret, holdType string) *r9HoldAgent {
	t.Helper()
	u, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	u.Scheme = "ws"
	u.Path = "/system-info"
	q := u.Query()
	q.Set("type", "1")
	q.Set("secret", secret)
	q.Set("version", "v1")
	q.Set("http", "0")
	q.Set("tls", "0")
	q.Set("socks", "0")
	u.RawQuery = q.Encode()
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		t.Fatalf("dial mock node: %v", err)
	}
	agent := &r9HoldAgent{heldCh: make(chan string, 8), releaseCh: make(chan struct{})}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			_, raw, readErr := conn.ReadMessage()
			if readErr != nil {
				return
			}
			plain := raw
			var wrap struct {
				Encrypted bool   `json:"encrypted"`
				Data      string `json:"data"`
			}
			if err := json.Unmarshal(raw, &wrap); err == nil && wrap.Encrypted && strings.TrimSpace(wrap.Data) != "" {
				if crypto, cryptoErr := security.NewAESCrypto(secret); cryptoErr == nil {
					if dec, decErr := crypto.Decrypt(wrap.Data); decErr == nil {
						plain = []byte(dec)
					}
				}
			}
			var cmd struct {
				Type      string `json:"type"`
				RequestID string `json:"requestId"`
			}
			if err := json.Unmarshal(plain, &cmd); err != nil || strings.TrimSpace(cmd.RequestID) == "" {
				continue
			}
			if cmd.Type == holdType {
				agent.heldCh <- cmd.Type
				<-agent.releaseCh
			}
			respBytes, err := json.Marshal(map[string]interface{}{
				"type":      cmd.Type + "Response",
				"success":   true,
				"message":   "OK",
				"requestId": cmd.RequestID,
			})
			if err != nil {
				continue
			}
			_ = conn.WriteMessage(websocket.TextMessage, respBytes)
		}
	}()
	var closeOnce sync.Once
	t.Cleanup(func() {
		closeOnce.Do(func() {
			agent.release()
			_ = conn.Close()
			wg.Wait()
		})
	})
	return agent
}

// r9AsyncCall 在协程里调用 handler 并回传解码后的 code/msg 与原文。
type r9CallResult struct {
	Code int
	Msg  string
	Body string
}

func r9AsyncCall(fn http.HandlerFunc, req *http.Request) chan r9CallResult {
	res := httptest.NewRecorder()
	done := make(chan r9CallResult, 1)
	go func() {
		fn(res, req)
		var env struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &env)
		done <- r9CallResult{Code: env.Code, Msg: env.Msg, Body: res.Body.String()}
	}()
	return done
}

func r9Await(t *testing.T, done chan r9CallResult) r9CallResult {
	t.Helper()
	select {
	case out := <-done:
		return out
	case <-time.After(60 * time.Second):
		t.Fatal("handler call did not complete")
		return r9CallResult{}
	}
}

// ---------- R9-08 面一：内核 remove 落库前复检 ----------

type r9KernelScenario struct {
	h          *Handler
	r          *repo.Repository
	frontXray  *model.NodeKernel
	landingBox *model.NodeKernel
	target     *model.NodeInbound
	landingID  int64
	agent      *r9HoldAgent
}

func setupR9KernelScenario(t *testing.T) *r9KernelScenario {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/r9-kernel-remove.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	srv := httptest.NewServer(h.WebSocketHandler())
	t.Cleanup(srv.Close)

	front := seedRelayTargetNode(t, r, "front", "198.51.100.1")
	landing := seedRelayTargetNode(t, r, "landing", "203.0.113.10")
	s := &r9KernelScenario{h: h, r: r, landingID: landing}
	s.frontXray = seedChainKernel(t, r, front, kernel.TypeXray)
	s.landingBox = seedChainKernel(t, r, landing, kernel.TypeSingbox)
	s.target = seedChainInbound(t, r, s.landingBox, "landing-vless", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 443,
		Security: kernel.Security{Type: kernel.SecurityReality}}, 0)
	s.agent = startR9HoldAgent(t, srv.URL, "landing-secret", "KernelControl")
	waitForCondition(t, 10*time.Second, func() bool { return h.wsServer.IsNodeOnline(landing) }, "landing mock node online")
	return s
}

// seedLateReferrer 在守卫已通过、下发挂起期间插入一个指向 target
// 的新引用（模拟另一管理员在窗口内保存引用）。
func (s *r9KernelScenario) seedLateReferrer(t *testing.T) {
	t.Helper()
	_ = seedChainInbound(t, s.r, s.frontXray, "front-vless", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 8443,
		Security: kernel.Security{Type: kernel.SecurityNone}}, s.target.ID)
}

func (s *r9KernelScenario) removeAsync(t *testing.T, force bool) chan kernelRemoveEnvelope {
	t.Helper()
	body := map[string]interface{}{"id": s.landingBox.ID, "action": "remove"}
	if force {
		body["force"] = true
	}
	req := newAuthenticatedJSONRequest(t, body)
	res := httptest.NewRecorder()
	done := make(chan kernelRemoveEnvelope, 1)
	go func() {
		s.h.kernelControl(res, req)
		var env kernelRemoveEnvelope
		_ = json.Unmarshal(res.Body.Bytes(), &env)
		done <- env
	}()
	return done
}

func TestR9KernelRemoveRecheckBlocksLateReferrer(t *testing.T) {
	s := setupR9KernelScenario(t)
	done := s.removeAsync(t, false)

	// 守卫已按空引用集通过，卸载下发在 agent 侧挂起（20s 窗口内）。
	s.agent.waitHeld(t, "KernelControl")
	s.seedLateReferrer(t)
	s.agent.release()

	var env kernelRemoveEnvelope
	select {
	case env = <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("kernel remove did not complete")
	}
	if env.Code == 0 {
		t.Fatalf("R9-08: non-force remove must be blocked by the late referrer, got success")
	}
	if !strings.Contains(env.Msg, "front-vless") {
		t.Fatalf("R9-08: rejection must name the late referrer, got msg=%q", env.Msg)
	}
	if k, _ := s.r.GetNodeKernel(s.landingBox.ID); k == nil {
		t.Fatal("R9-08: kernel row deleted despite recheck rejection")
	}
	if row, _ := s.r.GetNodeInbound(s.target.ID); row == nil {
		t.Fatal("R9-08: target inbound deleted despite recheck rejection")
	}
}

func TestR9KernelRemoveForceNamesLateReferrer(t *testing.T) {
	s := setupR9KernelScenario(t)
	done := s.removeAsync(t, true)

	s.agent.waitHeld(t, "KernelControl")
	s.seedLateReferrer(t)
	s.agent.release()

	var env kernelRemoveEnvelope
	select {
	case env = <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("kernel force remove did not complete")
	}
	if env.Code != 0 {
		t.Fatalf("R9-08: force remove must proceed, got code=%d msg=%q", env.Code, env.Msg)
	}
	named := false
	for _, name := range env.Data.BrokenReferrers {
		if strings.Contains(name, "front-vless") {
			named = true
		}
	}
	if !named {
		t.Fatalf("R9-08: force receipt must name the late referrer, got %v", env.Data.BrokenReferrers)
	}
	if k, _ := s.r.GetNodeKernel(s.landingBox.ID); k != nil {
		t.Fatal("R9-08: kernel row must be deleted by force remove")
	}
}

// ---------- R9-08 面二：入站删除（单删/批删）落库前复检 ----------

type r9InboundScenario struct {
	h         *Handler
	r         *repo.Repository
	frontXray *model.NodeKernel
	target    *model.NodeInbound
	agent     *r9HoldAgent
}

func setupR9InboundScenario(t *testing.T) *r9InboundScenario {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/r9-inbound-delete.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	srv := httptest.NewServer(h.WebSocketHandler())
	t.Cleanup(srv.Close)

	now := time.Now().UnixMilli()
	if _, err := r.CreateUser("admin", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now); err != nil {
		t.Fatalf("create user: %v", err)
	}
	front := seedRelayTargetNode(t, r, "front", "198.51.100.1")
	exitA := seedRelayTargetNode(t, r, "exit-a", "198.51.100.2")
	landing := seedRelayTargetNode(t, r, "landing", "203.0.113.10")
	if err := r.DB().Exec(`UPDATE node SET status = 1`).Error; err != nil {
		t.Fatalf("activate nodes: %v", err)
	}
	tunnelID := seedRelayTargetTunnel(t, r, "wl-tunnel", front, 30000, exitA)
	landingBox := seedChainKernel(t, r, landing, kernel.TypeSingbox)
	s := &r9InboundScenario{h: h, r: r}
	s.target = seedChainInbound(t, r, landingBox, "landing-vless", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 443,
		Security: kernel.Security{Type: kernel.SecurityReality}}, 0)
	forwardID, err := r.CreateForwardTx(1, "admin", "relay-fwd", tunnelID, "203.0.113.10:443", "fifo", now, 1,
		[]int64{front}, 20000, "", nil, 0, 0, nil, 0, 0, 0)
	if err != nil {
		t.Fatalf("create forward: %v", err)
	}
	if err := r.CreateInboundRelay(&model.NodeInboundRelay{InboundID: s.target.ID, ForwardID: forwardID, Name: "relay", Managed: 1, CreatedTime: now}); err != nil {
		t.Fatalf("create relay: %v", err)
	}
	s.frontXray = seedChainKernel(t, r, front, kernel.TypeXray)
	// 目标入站带一条面板托管中转：删除时 removeInboundRelays 会向
	// front 下发 DeleteService 并等待回执——将其挂起即得守卫与落
	// 库之间的确定性窗口。
	s.agent = startR9HoldAgent(t, srv.URL, "front-secret", "DeleteService")
	waitForCondition(t, 10*time.Second, func() bool { return h.wsServer.IsNodeOnline(front) }, "front mock node online")
	return s
}

func (s *r9InboundScenario) seedLateReferrer(t *testing.T) {
	t.Helper()
	_ = seedChainInbound(t, s.r, s.frontXray, "front-vless", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 8443,
		Security: kernel.Security{Type: kernel.SecurityNone}}, s.target.ID)
}

func (s *r9InboundScenario) targetSurvives(t *testing.T) bool {
	t.Helper()
	row, err := s.r.GetNodeInbound(s.target.ID)
	return err == nil && row != nil
}

func TestR9InboundDeleteRecheckBlocksLateReferrer(t *testing.T) {
	s := setupR9InboundScenario(t)
	req := httptest.NewRequest("POST", "/api/v1/inbound/delete", strings.NewReader(`{"id":`+nodeIDStr(s.target.ID)+`}`))
	done := r9AsyncCall(s.h.inboundDelete, req)

	// 守卫已按空引用集通过，relay 清理的 DeleteService 挂起中。
	s.agent.waitHeld(t, "DeleteService")
	s.seedLateReferrer(t)
	s.agent.release()

	out := r9Await(t, done)
	if out.Code == 0 {
		t.Fatalf("R9-08: inbound delete must be blocked by the late referrer, got success body=%s", out.Body)
	}
	if !strings.Contains(out.Msg, "front-vless") {
		t.Fatalf("R9-08: rejection must name the late referrer, got msg=%q", out.Msg)
	}
	if !s.targetSurvives(t) {
		t.Fatal("R9-08: target inbound deleted despite recheck rejection")
	}
}

func TestR9InboundBatchDeleteRecheckBlocksLateReferrer(t *testing.T) {
	s := setupR9InboundScenario(t)
	req := httptest.NewRequest("POST", "/api/v1/inbound/batch-delete", strings.NewReader(`{"ids":[`+nodeIDStr(s.target.ID)+`]}`))
	done := r9AsyncCall(s.h.inboundBatchDelete, req)

	s.agent.waitHeld(t, "DeleteService")
	s.seedLateReferrer(t)
	s.agent.release()

	out := r9Await(t, done)
	if out.Code != 0 {
		t.Fatalf("batch delete envelope must stay code 0, got %d body=%s", out.Code, out.Body)
	}
	var env r9BatchDeleteResp
	if err := json.Unmarshal([]byte(out.Body), &env); err != nil {
		t.Fatalf("decode batch response: %v body=%s", err, out.Body)
	}
	if env.Data.SuccessCount != 0 || env.Data.FailCount != 1 {
		t.Fatalf("R9-08: batch counts = %d/%d, want 0 success + 1 fail (late referrer), body=%s",
			env.Data.SuccessCount, env.Data.FailCount, out.Body)
	}
	named := false
	for _, failure := range env.Data.Failures {
		if failure.ID == s.target.ID && strings.Contains(failure.Reason, "front-vless") {
			named = true
		}
	}
	if !named {
		t.Fatalf("R9-08: batch failure must name the late referrer, failures=%+v", env.Data.Failures)
	}
	if !s.targetSurvives(t) {
		t.Fatal("R9-08: target inbound deleted despite batch recheck rejection")
	}
}

// ---------- R9-08 面三：节点删除落库前复检（回执点名） ----------

func TestR9NodeDeleteRecheckNamesLateReferrer(t *testing.T) {
	r := openUninstallHandlerRepo(t)
	h := New(r, "test-secret")
	victim := seedUninstallNode(t, r, "victim", "secret-victim")
	victimBox := seedChainKernel(t, r, victim.ID, kernel.TypeSingbox)
	target := seedChainInbound(t, r, victimBox, "victim-vless", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 443,
		Security: kernel.Security{Type: kernel.SecurityReality}}, 0)
	front := seedRelayTargetNode(t, r, "front2", "198.51.100.9")
	frontXray := seedChainKernel(t, r, front, kernel.TypeXray)

	// 连带卸载的在线下发即窗口：守卫已在下发前按空引用集收集，
	// 下发瞬间另一管理员保存一个跨节点新引用指向 victim 入站。
	origOnline, origSend := nodeAgentIsOnline, nodeAgentSendCommand
	inserted := false
	nodeAgentIsOnline = func(h *Handler, nodeID int64) bool { return true }
	nodeAgentSendCommand = func(h *Handler, nodeID int64, cmdType string, timeout time.Duration) error {
		if !inserted {
			inserted = true
			_ = seedChainInbound(t, r, frontXray, "late-ref", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 8443,
				Security: kernel.Security{Type: kernel.SecurityNone}}, target.ID)
		}
		return nil
	}
	t.Cleanup(func() { nodeAgentIsOnline, nodeAgentSendCommand = origOnline, origSend })

	resp := r9PostNodeDelete(t, h, `{"id":`+nodeIDStr(victim.ID)+`,"uninstall":true}`)
	if resp.Code != 0 {
		t.Fatalf("R9-08: node delete semantics stay best-effort, got code=%d msg=%q", resp.Code, resp.Msg)
	}
	rawList, _ := resp.Data["brokenReferrers"].([]interface{})
	named := false
	for _, item := range rawList {
		if text, ok := item.(string); ok && strings.Contains(text, "late-ref") {
			named = true
		}
	}
	if !named {
		t.Fatalf("R9-08: receipt must name the referrer saved during the uninstall window, got brokenReferrers=%v", resp.Data["brokenReferrers"])
	}
	if got, _ := r.GetNodeByID(victim.ID); got != nil {
		t.Fatal("R9-08: node delete must still complete (naming, not blocking, is this surface's caliber)")
	}
}
