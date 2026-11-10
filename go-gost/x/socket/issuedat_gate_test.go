package socket

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/go-gost/x/internal/util/crypto"
)

// issuedAt 生产形状门禁（R2-ISSUEDAT）。
//
// 契约：面板下发 UninstallAgent 时 issuedAt 是 JSON 数字（UnixMilli
// int64，与面板 node_pending_uninstall 登记表同形，见
// go-backend/internal/http/handler/node_delete_uninstall.go:122 与
// :166）；agent 必须 ① 把该数字当作幂等键（同 issuedAt＋新
// requestId 的重复下发只重报终局、不重执行），② 终局
// UninstallResult 帧里的 data.issuedAt 同样发 JSON 数字，面板
// handleUninstallResult 才按 int64 对账、登记才能 acked→终局。
//
// 线型 golden：以下两段字节分别是面板真实下发的命令与 agent 真实
// 发出的终局帧在 data 层的形状；面板半的同值 golden 在
// go-backend/internal/ws/uninstall_issuedat_gate_test.go 与
// go-backend/internal/http/handler/
// node_delete_uninstall_issuedat_gate_test.go，三处数值须一致。
// W4 的 RFC3339 字符串用例保留为回归夹具，但不得作为本契约的唯
// 一证明——生产形状是数字，字符串用例在基线树上全绿、掩盖了本缺
// 陷（R2-ISSUEDAT 根因）。
const issuedAtGateValue = int64(1794276644653)

// issuedAtGateKey 是 issuedAtGateValue 的十进制字串：agent 内部幂
// 等键的形态（handleUninstallAgent 的 ack data.uninstallKey 与落
// 盘状态 Key）。
const issuedAtGateKey = "1794276644653"

// issuedAtGatePanelCommand 是面板在线下发的真实命令字节（未加密
// 层）：issuedAt 为 JSON 数字。
const issuedAtGatePanelCommand = `{"type":"UninstallAgent","data":{"issuedAt":1794276644653},"requestId":"gate-req-1"}`

// issuedAtGatePanelCommandReq2 与上者同一签发（同 issuedAt）、新
// requestId：重复下发形态。
const issuedAtGatePanelCommandReq2 = `{"type":"UninstallAgent","data":{"issuedAt":1794276644653},"requestId":"gate-req-2"}`

var issuedAtGateNumber = regexp.MustCompile(`^\d+$`)

// issuedAtGateShrinkTiming 把协议流的等待缝缩到毫秒级（与 W4 同
// 口径），测试结束恢复。
func issuedAtGateShrinkTiming(t *testing.T) {
	t.Helper()
	origAckWait, origAttempts := uninstallAckWaitTimeout, uninstallReportAttempts
	uninstallAckWaitTimeout = 50 * time.Millisecond
	uninstallReportAttempts = 1
	t.Cleanup(func() { uninstallAckWaitTimeout, uninstallReportAttempts = origAckWait, origAttempts })
}

// issuedAtGateInstallTree 建脚本形态卸载所需的安装布局（与 W4 新
// 签发测试同形），并把状态路径与执行钩子桩进临时树。返回钩子桩：
// 执行钩子的调用序是「是否真的执行过一遍」的进程外观测（二次执
// 行必然重跑钩子并追加记录）。
func issuedAtGateInstallTree(t *testing.T) *uninstallHookStubs {
	t.Helper()
	stubUninstallStatePath(t)
	dir := t.TempDir()
	installDir := filepath.Join(dir, "home", "flmx_agent")
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(installDir, "flmx_agent")
	if err := os.WriteFile(binPath, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	stubs := stubUninstallHooks(t, binPath, false)
	uninstallStarted.Store(false)
	t.Cleanup(func() { uninstallStarted.Store(false) })
	return stubs
}

// issuedAtGateDecodeCommand 按生产解码形态（encoding/json 解进
// CommandMessage：data 里的数字成 float64）解 golden 命令字节。
func issuedAtGateDecodeCommand(t *testing.T, raw string) CommandMessage {
	t.Helper()
	var cmd CommandMessage
	if err := json.Unmarshal([]byte(raw), &cmd); err != nil {
		t.Fatalf("decode gate command %q: %v", raw, err)
	}
	if cmd.Type != "UninstallAgent" {
		t.Fatalf("gate command type = %q, want UninstallAgent", cmd.Type)
	}
	return cmd
}

// issuedAtGateWaitFinal 轮询落盘状态直至终局，返回终局态。
func issuedAtGateWaitFinal(t *testing.T) *uninstallState {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if st, ok := loadUninstallState(); ok && (st.State == "succeeded" || st.State == "failed") {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatal("gate uninstall did not reach a final state")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// issuedAtGateAckKey 断言 dispatch 回执的幂等键。
func issuedAtGateAckKey(t *testing.T, data interface{}) {
	t.Helper()
	m, ok := data.(map[string]interface{})
	if !ok {
		t.Fatalf("ack data is not a map: %#v", data)
	}
	if m["uninstallKey"] != issuedAtGateKey {
		t.Fatalf("ack uninstallKey = %v, want %q (panel-issued numeric issuedAt must be the idempotency key)", m["uninstallKey"], issuedAtGateKey)
	}
	if m["state"] != "acked" {
		t.Fatalf("ack state = %v, want acked", m["state"])
	}
}

// TestIssuedAtGatePanelBytesDriveAgentHandler：面板真实下发字节
// （数字 issuedAt）驱动 handleUninstallAgent——回执幂等键与落盘
// 终局键都必须是该数字的十进制字串。基线树上本测试必红：数字进
// string 字段解码失败、issuedAt 恒空、键回退为 requestId。
func TestIssuedAtGatePanelBytesDriveAgentHandler(t *testing.T) {
	w4Setup()
	issuedAtGateShrinkTiming(t)
	issuedAtGateInstallTree(t)

	cmd := issuedAtGateDecodeCommand(t, issuedAtGatePanelCommand)
	w := &WebSocketReporter{}
	data, err := w.handleUninstallAgent(cmd.Data, cmd.RequestId)
	if err != nil {
		t.Fatalf("handleUninstallAgent: %v", err)
	}
	issuedAtGateAckKey(t, data)
	st := issuedAtGateWaitFinal(t)
	if st.Key != issuedAtGateKey {
		t.Fatalf("final state key = %q, want %q", st.Key, issuedAtGateKey)
	}
	if st.State != "succeeded" {
		t.Fatalf("final state = %q (failedStep %q, steps %+v)", st.State, st.FailedStep, st.Steps)
	}
}

// TestIssuedAtGateDuplicateDispatchWithNewRequestIDReReports：同
// issuedAt＋新 requestId 的重复下发只重报终局、不重执行。基线树
// 上必红：第一次派发键已回退为 requestId「gate-req-1」，第二次
// 派发键为「gate-req-2」——键不同被当新签发、二次执行。
func TestIssuedAtGateDuplicateDispatchWithNewRequestIDReReports(t *testing.T) {
	w4Setup()
	issuedAtGateShrinkTiming(t)
	stubs := issuedAtGateInstallTree(t)

	cmd := issuedAtGateDecodeCommand(t, issuedAtGatePanelCommand)
	w := &WebSocketReporter{}
	data, err := w.handleUninstallAgent(cmd.Data, cmd.RequestId)
	if err != nil {
		t.Fatalf("first dispatch: %v", err)
	}
	issuedAtGateAckKey(t, data)
	st := issuedAtGateWaitFinal(t)
	if st.Key != issuedAtGateKey || st.State != "succeeded" {
		t.Fatalf("first final state = key %q state %q, want key %q succeeded", st.Key, st.State, issuedAtGateKey)
	}
	// 首发终局落盘后执行钩子调用序已定格：二次执行必然重跑钩子
	// 追加记录、且必然先把落盘状态覆写为新 RequestID 的 acked
	// 态——这两点是「未二次执行」的确定性观测。进程级执行闩
	// uninstallStarted 不是观测点：它只在异键新签发时于
	// handleUninstallAgent 内复位，首发真实执行后恒为 true，
	// 同键重报根本不触闩（与路 4 预置终局态口径的差异在此）。
	hookCalls := len(stubs.order)

	cmd2 := issuedAtGateDecodeCommand(t, issuedAtGatePanelCommandReq2)
	data2, err := w.handleUninstallAgent(cmd2.Data, cmd2.RequestId)
	if err != nil {
		t.Fatalf("duplicate dispatch: %v", err)
	}
	m, ok := data2.(map[string]interface{})
	if !ok || m["alreadyFinal"] != true || m["uninstallKey"] != issuedAtGateKey || m["state"] != "succeeded" {
		t.Fatalf("duplicate dispatch must re-report the stored final state, got %+v", data2)
	}
	if len(stubs.order) != hookCalls {
		t.Fatalf("duplicate dispatch must not re-run uninstall steps, hooks before %v after %v", stubs.order[:hookCalls], stubs.order)
	}
	stAfter, ok := loadUninstallState()
	if !ok {
		t.Fatal("stored final state missing after duplicate dispatch")
	}
	if stAfter.Key != st.Key || stAfter.IssuedAt != st.IssuedAt || stAfter.RequestID != st.RequestID ||
		stAfter.State != st.State || stAfter.FailedStep != st.FailedStep || stAfter.Reason != st.Reason ||
		!reflect.DeepEqual(stAfter.Steps, st.Steps) {
		t.Fatalf("stored final state must be unchanged after duplicate dispatch (a second execution would have overwritten it), before %+v after %+v", st, stAfter)
	}
	if st2, ok := loadUninstallState(); !ok || st2.Key != issuedAtGateKey || st2.RequestID != "gate-req-1" {
		t.Fatalf("stored final state must be unchanged after duplicate dispatch, got %+v", st2)
	}
}

// issuedAtGateLiveReporter 建一个带真连接的 reporter：服务端是
// httptest 上的真 WebSocket 端点，收到的每条帧进 frames 通道；
// reporter 的 url 故意指不可达地址（与仓内其他测试同口径），不启
// 动重连——w.conn/w.connected 由测试直设。
func issuedAtGateLiveReporter(t *testing.T, secret string) (*WebSocketReporter, <-chan []byte) {
	t.Helper()
	frames := make(chan []byte, 4)
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			frames <- msg
		}
	}))
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial gate panel endpoint: %v", err)
	}
	w := NewWebSocketReporter("ws://127.0.0.1:1", secret)
	w.conn = conn
	w.connected = true
	t.Cleanup(func() {
		_ = conn.Close()
		w.Stop()
	})
	return w, frames
}

// TestIssuedAtGateFinalFrameCarriesNumericIssuedAt：走真实执行流
// （executeUninstall → reportUninstallFinal → sendResponse）抓线
// 上终局帧，解密后断言 data.issuedAt 是 JSON 数字且等于签发值。
// 基线树上必红：st.IssuedAt 恒空串，帧里 issuedAt 为 ""（面板按
// int64 解整帧即 malformed、登记永不终局——R2-ISSUEDAT 的终局
// 形态）。
func TestIssuedAtGateFinalFrameCarriesNumericIssuedAt(t *testing.T) {
	w4Setup()
	issuedAtGateShrinkTiming(t)
	issuedAtGateInstallTree(t)

	const secret = "issuedat-gate-secret-0123456789abcdef"
	w, frames := issuedAtGateLiveReporter(t, secret)
	cmd := issuedAtGateDecodeCommand(t, issuedAtGatePanelCommand)
	data, err := w.handleUninstallAgent(cmd.Data, cmd.RequestId)
	if err != nil {
		t.Fatalf("handleUninstallAgent: %v", err)
	}
	// 回执键不断言为致命：即使键已错（基线树），执行仍会走完、终
	// 局帧仍会发出——本测试要独立证明帧层面的 issuedAt 形态，
	// 故此处只记错、继续抓帧。
	if m, ok := data.(map[string]interface{}); !ok || m["uninstallKey"] != issuedAtGateKey {
		t.Errorf("ack uninstallKey = %v, want %q (panel-issued numeric issuedAt must be the idempotency key)", m["uninstallKey"], issuedAtGateKey)
	}

	select {
	case raw := <-frames:
		var wrap struct {
			Encrypted bool   `json:"encrypted"`
			Data      string `json:"data"`
		}
		if err := json.Unmarshal(raw, &wrap); err != nil || !wrap.Encrypted {
			t.Fatalf("final frame must be an encrypted envelope, got %s", raw)
		}
		dec, err := crypto.NewAESCrypto(secret)
		if err != nil {
			t.Fatalf("gate crypto: %v", err)
		}
		plain, err := dec.Decrypt(wrap.Data)
		if err != nil {
			t.Fatalf("decrypt final frame: %v", err)
		}
		var resp struct {
			Type string                     `json:"type"`
			Data map[string]json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(plain, &resp); err != nil {
			t.Fatalf("unmarshal final frame %q: %v", plain, err)
		}
		if resp.Type != "UninstallResult" {
			t.Fatalf("final frame type = %q, want UninstallResult (full %s)", resp.Type, plain)
		}
		rawIssuedAt := string(resp.Data["issuedAt"])
		if !issuedAtGateNumber.MatchString(rawIssuedAt) {
			t.Fatalf("final frame data.issuedAt must be a JSON number, got %q (full %s)", rawIssuedAt, plain)
		}
		if rawIssuedAt != issuedAtGateKey {
			t.Fatalf("final frame data.issuedAt = %s, want %s", rawIssuedAt, issuedAtGateKey)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no final UninstallResult frame arrived on the wire")
	}
}
