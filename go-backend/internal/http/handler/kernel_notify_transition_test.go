package handler

import (
	"encoding/json"
	"fmt"
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

// INV-NOTIFY-KERNELERR（承 LIVE-BATCH-FINAL §U-2 / M-0160）：
// kernel_error 通知的唯一触发点是 pollNodeKernels 摄入时的状态跃迁
// 判定（读 DB 现行 status 与 agent 回报比对）。但 applyKernelWithMode
// 的三条落状态路径（payload 构建失败／命令发送失败／成功回执带
// error 状态）都直写 status=error、不经跃迁判定——跃迁被 apply
// 提前「消费」，其后 poll 摄入时 DB 已是 error、永不跃迁，告警永久
// 丢失（真机三触发之 (a) 同步安装失败、(b) 恢复→再坏均未得事件）。
//
// 本文件以可编程 mock 节点复现两类被点名输入（apply 失败、drift
// error），并以 poll 原生跃迁为对照组：
//   - 对照组在修复前后都必须 PASS（poll 分支本身无恙）；
//   - apply/drift/rearm 用例在修复前必须 FAIL（0 事件）、修复后 PASS。

type kernelNotifyMock struct {
	mu          sync.Mutex
	applyMode   string // "ok" | "errorState" | "fail"
	applyErr    string
	reportMode  string // "running" | "error" | "none"
	reportErr   string
	lastHash    string
	applyCalls  int
	statusCalls int
}

func (m *kernelNotifyMock) setApply(mode, errText string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.applyMode, m.applyErr = mode, errText
}

func (m *kernelNotifyMock) setReport(mode, errText string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reportMode, m.reportErr = mode, errText
}

func (m *kernelNotifyMock) calls() (apply, status int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.applyCalls, m.statusCalls
}

func (m *kernelNotifyMock) stateLocked(hash string) map[string]interface{} {
	st := map[string]interface{}{
		"type": "xray", "running": true, "enabled": true, "pid": 4242, "startedAt": 1,
		"version": "v25.1.1", "configHash": hash,
	}
	switch m.reportMode {
	case "error":
		st["running"] = false
		st["pid"] = 0
		st["error"] = m.reportErr
	}
	return st
}

func startKernelNotifyMockNode(t *testing.T, baseURL, secret string, mock *kernelNotifyMock) func() {
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
	q.Set("http", "1")
	q.Set("tls", "1")
	q.Set("socks", "1")
	u.RawQuery = q.Encode()
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		t.Fatalf("dial mock node: %v", err)
	}
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
				Type      string          `json:"type"`
				RequestID string          `json:"requestId"`
				Data      json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal(plain, &cmd); err != nil || strings.TrimSpace(cmd.RequestID) == "" {
				continue
			}
			resp := map[string]interface{}{
				"type":      fmt.Sprintf("%sResponse", cmd.Type),
				"success":   true,
				"message":   "OK",
				"requestId": cmd.RequestID,
			}
			switch cmd.Type {
			case "KernelApply":
				var body struct {
					Hash string `json:"hash"`
				}
				_ = json.Unmarshal(cmd.Data, &body)
				mock.mu.Lock()
				mock.applyCalls++
				mock.lastHash = body.Hash
				mode, aerr := mock.applyMode, mock.applyErr
				hash := mock.lastHash
				mock.mu.Unlock()
				switch mode {
				case "fail":
					resp["success"] = false
					resp["message"] = aerr
				case "errorState":
					resp["data"] = map[string]interface{}{
						"type": "xray", "running": false, "enabled": true, "pid": 0, "startedAt": 0,
						"version": "v25.1.1", "configHash": hash, "error": aerr,
					}
				default:
					resp["data"] = map[string]interface{}{
						"type": "xray", "running": true, "enabled": true, "pid": 4242, "startedAt": 1,
						"version": "v25.1.1", "configHash": hash,
					}
				}
			case "KernelStatus":
				mock.mu.Lock()
				mock.statusCalls++
				mode := mock.reportMode
				data := map[string]interface{}{
					"kernels":      []map[string]interface{}{},
					"nftAvailable": false,
					"epoch":        "e1",
					"counters":     []map[string]interface{}{},
					"users":        []map[string]interface{}{},
				}
				if mode != "none" {
					data["kernels"] = []map[string]interface{}{mock.stateLocked(mock.lastHash)}
				}
				mock.mu.Unlock()
				resp["data"] = data
			}
			respBytes, err := json.Marshal(resp)
			if err != nil {
				continue
			}
			_ = conn.WriteMessage(websocket.TextMessage, respBytes)
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = conn.Close()
			wg.Wait()
		})
	}
}

type kernelNotifyCapture struct {
	mu   sync.Mutex
	msgs []notifyMessage
}

func (c *kernelNotifyCapture) handler(w http.ResponseWriter, req *http.Request) {
	var m notifyMessage
	_ = json.NewDecoder(req.Body).Decode(&m)
	c.mu.Lock()
	c.msgs = append(c.msgs, m)
	c.mu.Unlock()
}

func (c *kernelNotifyCapture) kernelErrors() []notifyMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []notifyMessage
	for _, m := range c.msgs {
		if m.Event == notifyEventKernelError {
			out = append(out, m)
		}
	}
	return out
}

func (c *kernelNotifyCapture) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = nil
}

func awaitKernelErrors(t *testing.T, c *kernelNotifyCapture, want int, timeout time.Duration) []notifyMessage {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		msgs := c.kernelErrors()
		if len(msgs) >= want || time.Now().After(deadline) {
			return msgs
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// setupKernelNotifyRig 立起：带 webhook 捕获的通知配置 + 在线 mock
// 节点 + 一个 xray 内核行；等重连对账轮静默后把行状态归一为
// running 并清捕获，返回可编程 mock 与内核行。
func setupKernelNotifyRig(t *testing.T) (*Handler, *repo.Repository, *kernelNotifyMock, *kernelNotifyCapture, *model.NodeKernel, int64) {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/kernel-notify.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })

	capture := &kernelNotifyCapture{}
	srv := httptest.NewServer(http.HandlerFunc(capture.handler))
	t.Cleanup(srv.Close)

	now := time.Now().UnixMilli()
	for k, v := range map[string]string{"notify_enabled": "true", "notify_webhook_url": srv.URL} {
		if err := r.DB().Exec(`INSERT INTO vite_config(name, value, time) VALUES(?, ?, ?)`, k, v, now).Error; err != nil {
			t.Fatal(err)
		}
	}
	h := New(r, "test-secret")

	nodeID := seedRelayTargetNode(t, r, "notify-node", "198.51.100.41")
	k := seedChainKernel(t, r, nodeID, kernel.TypeXray)
	if err := r.UpdateNodeKernelFields(k.ID, map[string]interface{}{"version": "v25.1.1"}); err != nil {
		t.Fatal(err)
	}

	mock := &kernelNotifyMock{applyMode: "ok", reportMode: "running"}
	wsSrv := httptest.NewServer(h.WebSocketHandler())
	t.Cleanup(wsSrv.Close)
	stop := startKernelNotifyMockNode(t, wsSrv.URL, "notify-node-secret", mock)
	t.Cleanup(stop)

	waitForCondition(t, 5*time.Second, func() bool { return h.wsServer.IsNodeOnline(nodeID) }, "mock node online")
	// 等重连对账轮（apply + 轮尾 poll）命令计数稳定再归一状态。
	{
		lastApply, lastStatus, stable := -1, -1, 0
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			a, s := mock.calls()
			if a == lastApply && s == lastStatus && a >= 1 {
				stable++
				if stable >= 3 {
					break
				}
			} else {
				stable = 0
				lastApply, lastStatus = a, s
			}
			time.Sleep(100 * time.Millisecond)
		}
		if lastApply < 1 {
			t.Fatal("reconnect round never applied the kernel")
		}
	}
	if err := r.UpdateNodeKernelFields(k.ID, map[string]interface{}{"status": kernelStatusRunning}); err != nil {
		t.Fatal(err)
	}
	capture.reset()
	return h, r, mock, capture, k, nodeID
}

func kernelRowStatus(t *testing.T, r *repo.Repository, id int64) string {
	t.Helper()
	row, err := r.GetNodeKernel(id)
	if err != nil || row == nil {
		t.Fatalf("get kernel row: %v", err)
	}
	return row.Status
}

// W4：apply 成功回执带 agent error 状态——apply 直写 error，修复前
// 通知永不触发；修复后恰一条，且其后 poll 摄入不得重复。
func TestKernelErrorNotifyApplyErrorStateReceipt(t *testing.T) {
	h, r, mock, capture, k, nodeID := setupKernelNotifyRig(t)
	mock.setApply("errorState", "mock apply boom")
	mock.setReport("error", "mock apply boom")

	if err := h.applyKernel(k.ID, true); err != nil {
		t.Fatalf("apply with error-state receipt must not fail the call itself: %v", err)
	}
	if st := kernelRowStatus(t, r, k.ID); st != kernelStatusError {
		t.Fatalf("status after error-state receipt: want error, got %q", st)
	}
	msgs := awaitKernelErrors(t, capture, 1, 5*time.Second)
	if len(msgs) != 1 {
		t.Fatalf("kernel_error after apply error-state receipt: want 1, got %d (%v)", len(msgs), capture.kernelErrors())
	}
	if msgs[0].Title != "内核运行出错" || !strings.Contains(msgs[0].Message, "mock apply boom") {
		t.Fatalf("unexpected kernel_error payload: %+v", msgs[0])
	}
	// 其后 poll 摄入同一 error 状态不得重复通知（跃迁已由 apply 消费）。
	h.pollNodeKernels(nodeID, false)
	time.Sleep(1200 * time.Millisecond)
	if n := len(capture.kernelErrors()); n != 1 {
		t.Fatalf("poll after apply-consumed transition must not re-notify: got %d total", n)
	}
}

// W3：apply 命令发送失败（agent 回 success=false）——直写 error。
func TestKernelErrorNotifyApplyCommandFailure(t *testing.T) {
	h, r, mock, capture, k, _ := setupKernelNotifyRig(t)
	mock.setApply("fail", "mock agent exploded")

	if err := h.applyKernel(k.ID, true); err == nil {
		t.Fatal("apply must surface the command failure")
	}
	if st := kernelRowStatus(t, r, k.ID); st != kernelStatusError {
		t.Fatalf("status after command failure: want error, got %q", st)
	}
	msgs := awaitKernelErrors(t, capture, 1, 5*time.Second)
	if len(msgs) != 1 {
		t.Fatalf("kernel_error after apply command failure: want 1, got %d", len(msgs))
	}
	if !strings.Contains(msgs[0].Message, "mock agent exploded") {
		t.Fatalf("kernel_error must carry the failure text: %+v", msgs[0])
	}
}

// W2：payload 构建失败（内核类型不在 catalog——代表组合不匹配/
// 设置条件/版本门同址的构建失败全集）——命令未发出即直写 error。
func TestKernelErrorNotifyPayloadBuildFailure(t *testing.T) {
	h, r, _, capture, k, _ := setupKernelNotifyRig(t)
	if err := r.UpdateNodeKernelFields(k.ID, map[string]interface{}{"kernel_type": "nosuch-kernel"}); err != nil {
		t.Fatal(err)
	}
	if err := h.applyKernel(k.ID, true); err == nil {
		t.Fatal("apply must surface the payload build failure")
	}
	if st := kernelRowStatus(t, r, k.ID); st != kernelStatusError {
		t.Fatalf("status after payload build failure: want error, got %q", st)
	}
	msgs := awaitKernelErrors(t, capture, 1, 5*time.Second)
	if len(msgs) != 1 {
		t.Fatalf("kernel_error after payload build failure: want 1, got %d", len(msgs))
	}
}

// 对照组：poll 原生跃迁（agent 自行转 error、无 apply 介入）——修复
// 前后都必须触发，证明 poll 分支本身无恙、缺陷只在 apply 绕行。
func TestKernelErrorNotifyPollTransitionControl(t *testing.T) {
	h, _, mock, capture, _, nodeID := setupKernelNotifyRig(t)
	mock.setReport("error", "kernel died on its own: mock")

	h.pollNodeKernels(nodeID, false)
	msgs := awaitKernelErrors(t, capture, 1, 5*time.Second)
	if len(msgs) != 1 {
		t.Fatalf("kernel_error on poll transition: want 1, got %d", len(msgs))
	}
	if !strings.Contains(msgs[0].Message, "kernel died on its own: mock") {
		t.Fatalf("kernel_error must carry the agent error text: %+v", msgs[0])
	}
}

// drift：agent 丢失已确认配置（KernelStatus 不报该内核）→ poll 派
// drift 补推 → 补推回执 error 状态。补推的落状态同样绕开跃迁判定。
func TestKernelErrorNotifyDriftReapplyFailure(t *testing.T) {
	h, r, mock, capture, k, nodeID := setupKernelNotifyRig(t)
	if err := r.UpdateNodeKernelFields(k.ID, map[string]interface{}{
		"config_hash": "drift-hash", "applied_hash": "drift-hash", "status": kernelStatusRunning,
	}); err != nil {
		t.Fatal(err)
	}
	mock.setReport("none", "")
	mock.setApply("errorState", "drift reapply boom")

	h.pollNodeKernels(nodeID, false)
	deadline := time.Now().Add(5 * time.Second)
	for kernelRowStatus(t, r, k.ID) != kernelStatusError && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if st := kernelRowStatus(t, r, k.ID); st != kernelStatusError {
		t.Fatalf("drift re-apply must land error status, got %q", st)
	}
	msgs := awaitKernelErrors(t, capture, 1, 5*time.Second)
	if len(msgs) != 1 {
		t.Fatalf("kernel_error after failed drift re-apply: want 1, got %d", len(msgs))
	}
	if !strings.Contains(msgs[0].Message, "drift reapply boom") {
		t.Fatalf("kernel_error must carry the re-apply error text: %+v", msgs[0])
	}
}

// 再武装：apply 路径恢复（error→running）必须与 poll 恢复同口径清
// 指纹，否则同文案再 error 会被 30 分钟去重窗吞掉。
func TestKernelErrorNotifyRearmAfterApplyRecovery(t *testing.T) {
	h, r, mock, capture, k, _ := setupKernelNotifyRig(t)
	mock.setApply("errorState", "rearm boom")
	if err := h.applyKernel(k.ID, true); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if msgs := awaitKernelErrors(t, capture, 1, 5*time.Second); len(msgs) != 1 {
		t.Fatalf("first error must notify once, got %d", len(msgs))
	}
	mock.setApply("ok", "")
	mock.setReport("running", "")
	if err := h.applyKernel(k.ID, true); err != nil {
		t.Fatalf("recovery apply: %v", err)
	}
	if st := kernelRowStatus(t, r, k.ID); st != kernelStatusRunning {
		t.Fatalf("status after recovery apply: want running, got %q", st)
	}
	mock.setApply("errorState", "rearm boom")
	if err := h.applyKernel(k.ID, true); err != nil {
		t.Fatalf("second error apply: %v", err)
	}
	if msgs := awaitKernelErrors(t, capture, 2, 5*time.Second); len(msgs) != 2 {
		t.Fatalf("same-text error after apply recovery must notify again (fingerprint rearmed), got %d total", len(msgs))
	}
}
