package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"go-backend/internal/security"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
	"go-backend/internal/ws"
)

// nodeSwapFakeAgent 经真 WS 传输桩驱动节点命令通道（形态同
// flow_cleanup_regression_test.go 的 cleanupAgent），但回执可按
// 命令类型配置：成功带 data、或 success=false 带业务错误文案，
// 以便钉死 swap 两接口的四格回执形状。节点 1 在线（假 agent），
// 节点 2 只落库不连线（离线格），节点 999 不存在。
type nodeSwapFakeAgent struct {
	h        *Handler
	mu       sync.Mutex
	commands []cleanupAgentCommand
	// dropReplyTypes 命中的命令只记录、不回执——用于模拟面板等回执
	// 超时（OPEN-7）：SwapSet 发出后 agent 侧继续执行，但回执永不
	// 到达，面板须走超时后复查。
	dropReplyTypes map[string]bool
	reply          func(command cleanupAgentCommand) (success bool, message string, data map[string]interface{})
}

func newNodeSwapFakeAgent(t *testing.T) *nodeSwapFakeAgent {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "node-swap.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	const secret = "node-swap-test-secret"
	for _, n := range []model.Node{
		{ID: 1, Name: "online", Secret: secret, ServerIP: "127.0.0.1", Port: "30000", Status: 1},
		{ID: 2, Name: "offline", Secret: "node-swap-offline-secret", ServerIP: "127.0.0.1", Port: "30000", Status: 1},
	} {
		if err := r.DB().Create(&n).Error; err != nil {
			t.Fatal(err)
		}
	}
	server := ws.NewServer(r, "test-jwt-secret")
	online := make(chan struct{})
	server.SetNodeOnlineHook(func(int64) { close(online) })
	serverDone := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		defer close(serverDone)
		server.ServeHTTP(w, req)
	}))
	t.Cleanup(ts.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"?type=1&secret="+secret, nil)
	if err != nil {
		t.Fatal(err)
	}
	crypto, err := security.NewAESCrypto(secret)
	if err != nil {
		t.Fatal(err)
	}
	a := &nodeSwapFakeAgent{h: &Handler{repo: r, wsServer: server}}
	a.reply = func(command cleanupAgentCommand) (bool, string, map[string]interface{}) {
		return true, "", nil
	}
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
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
				t.Errorf("decode command envelope: %v", err)
				return
			}
			if envelope.Encrypted {
				payload, err = crypto.Decrypt(envelope.Data)
				if err != nil {
					t.Errorf("decrypt command: %v", err)
					return
				}
			}
			var command cleanupAgentCommand
			if err := json.Unmarshal(payload, &command); err != nil {
				t.Errorf("decode command: %v", err)
				return
			}
			a.mu.Lock()
			a.commands = append(a.commands, command)
			drop := a.dropReplyTypes != nil && a.dropReplyTypes[command.Type]
			reply := a.reply
			a.mu.Unlock()
			if drop {
				continue
			}
			success, message, data := reply(command)
			resp := map[string]interface{}{
				"type":      command.Type,
				"requestId": command.RequestID,
				"success":   success,
				"message":   message,
			}
			if data != nil {
				resp["data"] = data
			}
			if err := conn.WriteJSON(resp); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = conn.Close()
		<-readerDone
		<-serverDone
	})
	select {
	case <-online:
	case <-time.After(3 * time.Second):
		t.Fatal("fake swap agent did not come online")
	}
	return a
}

func (a *nodeSwapFakeAgent) commandsOfType(commandType string) []cleanupAgentCommand {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []cleanupAgentCommand
	for _, c := range a.commands {
		if c.Type == commandType {
			out = append(out, c)
		}
	}
	return out
}

type nodeSwapEnvelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

func postNodeSwap(t *testing.T, h *Handler, method string, body string) nodeSwapEnvelope {
	t.Helper()
	var fn func(http.ResponseWriter, *http.Request)
	switch method {
	case "nodeSwapInfo":
		fn = h.nodeSwapInfo
	case "nodeSwapSet":
		fn = h.nodeSwapSet
	default:
		t.Fatalf("unknown method %s", method)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/node/swap", strings.NewReader(body))
	fn(rec, req)
	var env nodeSwapEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope %q: %v", rec.Body.String(), err)
	}
	return env
}

func TestNodeSwapNodeMissing(t *testing.T) {
	a := newNodeSwapFakeAgent(t)
	env := postNodeSwap(t, a.h, "nodeSwapInfo", `{"nodeId":999}`)
	if env.Code == 0 || env.Msg != "节点不存在" {
		t.Fatalf("missing node must be named, got %+v", env)
	}
	env = postNodeSwap(t, a.h, "nodeSwapSet", `{"nodeId":999,"sizeGB":2}`)
	if env.Code == 0 || env.Msg != "节点不存在" {
		t.Fatalf("missing node must be named on set, got %+v", env)
	}
}

func TestNodeSwapOfflineNode(t *testing.T) {
	a := newNodeSwapFakeAgent(t)
	env := postNodeSwap(t, a.h, "nodeSwapInfo", `{"nodeId":2}`)
	if env.Code == 0 || !strings.Contains(env.Msg, "不在线") {
		t.Fatalf("offline node must answer with the command-channel wording, got %+v", env)
	}
	if cmds := a.commandsOfType("SwapInfo"); len(cmds) != 0 {
		t.Fatalf("offline node must receive no command, got %+v", cmds)
	}
}

func TestNodeSwapAgentBusinessErrorPassthrough(t *testing.T) {
	a := newNodeSwapFakeAgent(t)
	const bizMsg = "设置 Swap 失败（校验磁盘空间）: 磁盘空间不足：设置 4 GB Swap 需要至少 4352 MB 磁盘剩余（目标 4096 MB＋预留 256 MB），当前磁盘剩余 1000 MB（现有 /swapfile 占用 0 MB 在重建期间仍占盘，不计入可用）；机器状态未改变"
	a.reply = func(command cleanupAgentCommand) (bool, string, map[string]interface{}) {
		if command.Type == "SwapSet" {
			return false, bizMsg, nil
		}
		return true, "", nil
	}
	env := postNodeSwap(t, a.h, "nodeSwapSet", `{"nodeId":1,"sizeGB":4}`)
	if env.Code != -2 || env.Msg != bizMsg {
		t.Fatalf("agent business error must pass through verbatim, got %+v", env)
	}
}

func TestNodeSwapSuccessPassthrough(t *testing.T) {
	a := newNodeSwapFakeAgent(t)
	infoData := map[string]interface{}{
		"totalMB": 2048, "usedMB": 30,
		"entries": []interface{}{
			map[string]interface{}{"name": "/swapfile", "type": "file", "sizeMB": 2048, "usedMB": 30},
		},
		"diskFreeMB": 5000, "memTotalMB": 8000, "managedFilePresent": true,
		"fileSizeMB": 1536,
	}
	setData := map[string]interface{}{
		"before": map[string]interface{}{"totalMB": 1024, "usedMB": 10},
		"after":  map[string]interface{}{"totalMB": 2048, "usedMB": 0},
		"sizeGB": 2,
	}
	a.reply = func(command cleanupAgentCommand) (bool, string, map[string]interface{}) {
		switch command.Type {
		case "SwapInfo":
			return true, "", infoData
		case "SwapSet":
			return true, "", setData
		}
		return true, "", nil
	}
	env := postNodeSwap(t, a.h, "nodeSwapInfo", `{"nodeId":1}`)
	if env.Code != 0 {
		t.Fatalf("info must succeed, got %+v", env)
	}
	var gotInfo map[string]interface{}
	if err := json.Unmarshal(env.Data, &gotInfo); err != nil {
		t.Fatal(err)
	}
	if gotInfo["totalMB"] != float64(2048) || gotInfo["managedFilePresent"] != true || gotInfo["diskFreeMB"] != float64(5000) {
		t.Fatalf("info data must pass through untouched, got %+v", gotInfo)
	}
	if gotInfo["fileSizeMB"] != float64(1536) {
		t.Fatalf("fileSizeMB must pass through untouched (shown in the modal shortage note as still-occupying-disk; the precheck itself mirrors the agent predicate on diskFreeMB only, R18-D2-01-mirror), got %+v", gotInfo)
	}
	if cmds := a.commandsOfType("SwapInfo"); len(cmds) != 1 {
		t.Fatalf("expected exactly one SwapInfo command, got %+v", cmds)
	}

	env = postNodeSwap(t, a.h, "nodeSwapSet", `{"nodeId":1,"sizeGB":2}`)
	if env.Code != 0 {
		t.Fatalf("set must succeed, got %+v", env)
	}
	var gotSet map[string]interface{}
	if err := json.Unmarshal(env.Data, &gotSet); err != nil {
		t.Fatal(err)
	}
	after, _ := gotSet["after"].(map[string]interface{})
	if gotSet["sizeGB"] != float64(2) || after["totalMB"] != float64(2048) {
		t.Fatalf("set before/after must pass through untouched, got %+v", gotSet)
	}
	cmds := a.commandsOfType("SwapSet")
	if len(cmds) != 1 {
		t.Fatalf("expected exactly one SwapSet command, got %+v", cmds)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(cmds[0].Data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["sizeGB"] != float64(2) {
		t.Fatalf("SwapSet payload must carry the requested sizeGB, got %+v", payload)
	}
}

func TestNodeSwapSetSizeValidation(t *testing.T) {
	a := newNodeSwapFakeAgent(t)
	for _, body := range []string{`{"nodeId":1,"sizeGB":0}`, `{"nodeId":1,"sizeGB":33}`, `{"nodeId":1,"sizeGB":-1}`} {
		env := postNodeSwap(t, a.h, "nodeSwapSet", body)
		if env.Code == 0 || !strings.Contains(env.Msg, "1–32") {
			t.Fatalf("size in %s must be rejected with the range, got %+v", body, env)
		}
	}
	env := postNodeSwap(t, a.h, "nodeSwapSet", `{"sizeGB":2}`)
	if env.Code == 0 || env.Msg != "请求参数错误" {
		t.Fatalf("missing nodeId must be a parameter error, got %+v", env)
	}
	if cmds := a.commandsOfType("SwapSet"); len(cmds) != 0 {
		t.Fatalf("invalid sizes must never reach the agent, got %+v", cmds)
	}
}

func TestCheckNodeSwapCapacityMirror(t *testing.T) {
	// R18-D2-01-mirror：面板侧预检与 agent checkSwapCapacity 同谓词——
	// 只认真实磁盘剩余。重建时旧文件只是改名备份、仍占盘，新文件再整块
	// fallocate，峰值占用是旧+新，现有 /swapfile 占量绝不能计入可用。
	// 恰好达标（磁盘剩余 == 目标＋预留）放行。
	if err := checkNodeSwapCapacity(2048+256, 0, 2); err != nil {
		t.Fatalf("exact fit must pass: %v", err)
	}
	// 审计复现数：磁盘剩余 1000MB、已有 2GB 文件时调到 2GB 必须拒绝
	// （旧谓词 1000+2048=3048 ≥ 2304 会放行，随后在 swapoff 之后 ENOSPC）。
	if err := checkNodeSwapCapacity(1000, 2048, 2); err == nil {
		t.Fatalf("existing file space must not count toward capacity")
	}
	// 磁盘剩余本身达标时，已有文件大小不影响放行。
	if err := checkNodeSwapCapacity(2048+256, 2048, 2); err != nil {
		t.Fatalf("sufficient disk free must pass regardless of existing file: %v", err)
	}
	// 边界与 agent TestCheckSwapCapacityBoundaryMirror 共用一组数字：
	// diskFree 2304 → 恰达需求放行；2303 → 差 1MB 拒绝（在盘文件
	// 1024MB 的大小不影响边界）。
	if err := checkNodeSwapCapacity(2304, 1024, 2); err != nil {
		t.Fatalf("exact boundary must pass: %v", err)
	}
	if err := checkNodeSwapCapacity(2303, 1024, 2); err == nil {
		t.Fatalf("1MB below boundary must be rejected")
	}
	// 差 1MB 拒绝，且错误带全部关键数字（与 agent 新文案同口径）。
	err := checkNodeSwapCapacity(2048+255, 0, 2)
	if err == nil {
		t.Fatalf("1MB short must be rejected")
	}
	for _, want := range []string{"磁盘空间不足", "2304", "2048", "256", "2303", "不计入可用"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("capacity error must carry %q: %v", want, err)
		}
	}
}

// --- OPEN-7：SwapSet 等回执超时后按复查到的实际状态回执 ------------
//
// 背景：SwapSet 走 agent mutation 队列、用进程级 ctx，面板 600s 停止
// 等待后 agent 仍会执行到底——只报「等待节点响应超时」会让用户把
// 已生效的设置当成失败重做一次。executeNodeSwapSet 在超时后立即
// 再拉一次 SwapInfo（复查腿），按实际状态回执。

// swapInfoDataForReconcile 拼一份 SwapInfo 回执 data：fileSizeMB 是
// /swapfile 在盘文件大小（stat 口径），entries 是 /proc/swaps 行。
func swapInfoDataForReconcile(t *testing.T, fileSizeMB int64, managedActive bool) map[string]interface{} {
	t.Helper()
	var entries []interface{}
	if managedActive {
		// 在用条目 2047MB 是真实内核口径：2GiB 文件经 mkswap 去头后
		// /proc/swaps 报 2097148 KiB、向下取整 2047MB——复查判定
		// 绝不能拿条目 sizeMB 与目标 2048 精确比对。
		entries = append(entries, map[string]interface{}{"name": "/swapfile", "type": "file", "sizeMB": float64(2047), "usedMB": float64(0)})
	}
	return map[string]interface{}{
		"totalMB": float64(2048), "usedMB": float64(0),
		"entries":    entries,
		"diskFreeMB": float64(5000), "memTotalMB": float64(8000),
		"managedFilePresent": true, "fileSizeMB": float64(fileSizeMB),
	}
}

func TestNodeSwapSetTimeoutReconcilesCompleted(t *testing.T) {
	a := newNodeSwapFakeAgent(t)
	a.dropReplyTypes = map[string]bool{"SwapSet": true}
	a.reply = func(command cleanupAgentCommand) (bool, string, map[string]interface{}) {
		if command.Type == "SwapInfo" {
			return true, "", swapInfoDataForReconcile(t, 2048, true)
		}
		return true, "", nil
	}
	code, msg, data := a.h.executeNodeSwapSet(1, 2, 200*time.Millisecond)
	if code == 0 || data != nil {
		t.Fatalf("reconciled completion must not be code 0 (no before/after pair to render), got code=%d data=%v", code, data)
	}
	for _, want := range []string{"已完成", "2 GB", "2048"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("completion receipt must carry %q, got %q", want, msg)
		}
	}
	if cmds := a.commandsOfType("SwapSet"); len(cmds) != 1 {
		t.Fatalf("expected exactly one SwapSet command, got %d", len(cmds))
	}
	if cmds := a.commandsOfType("SwapInfo"); len(cmds) != 1 {
		t.Fatalf("timeout must trigger exactly one SwapInfo reconcile, got %d", len(cmds))
	}
}

func TestNodeSwapSetTimeoutReconcilesNotLanded(t *testing.T) {
	a := newNodeSwapFakeAgent(t)
	a.dropReplyTypes = map[string]bool{"SwapSet": true}
	a.reply = func(command cleanupAgentCommand) (bool, string, map[string]interface{}) {
		if command.Type == "SwapInfo" {
			// 旧 1GB 文件仍在用：设置未落地（仍在执行，或已恢复原状）。
			return true, "", swapInfoDataForReconcile(t, 1024, true)
		}
		return true, "", nil
	}
	code, msg, _ := a.h.executeNodeSwapSet(1, 2, 200*time.Millisecond)
	if code != -2 {
		t.Fatalf("not-landed reconcile must be code -2, got %d", code)
	}
	for _, want := range []string{"未达到目标", "1024", "2 GB"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("not-landed receipt must carry %q, got %q", want, msg)
		}
	}
	if cmds := a.commandsOfType("SwapInfo"); len(cmds) != 1 {
		t.Fatalf("timeout must trigger exactly one SwapInfo reconcile, got %d", len(cmds))
	}
}

func TestNodeSwapSetTimeoutReconcilesFileBuiltNotActive(t *testing.T) {
	a := newNodeSwapFakeAgent(t)
	a.dropReplyTypes = map[string]bool{"SwapSet": true}
	a.reply = func(command cleanupAgentCommand) (bool, string, map[string]interface{}) {
		if command.Type == "SwapInfo" {
			// 文件已建到目标大小、但 /proc/swaps 里还没有它：在
			// fallocate/dd 与 swapon 之间的收尾窗口。
			return true, "", swapInfoDataForReconcile(t, 2048, false)
		}
		return true, "", nil
	}
	code, msg, _ := a.h.executeNodeSwapSet(1, 2, 200*time.Millisecond)
	if code != -2 {
		t.Fatalf("file-built-not-active reconcile must be code -2, got %d", code)
	}
	for _, want := range []string{"尚未启用", "2 GB"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("file-built receipt must carry %q, got %q", want, msg)
		}
	}
}

func TestNodeSwapSetTimeoutReconcileFailsUnknown(t *testing.T) {
	a := newNodeSwapFakeAgent(t)
	a.dropReplyTypes = map[string]bool{"SwapSet": true, "SwapInfo": true}
	old := nodeSwapReconcileInfoTimeout
	nodeSwapReconcileInfoTimeout = 200 * time.Millisecond
	t.Cleanup(func() { nodeSwapReconcileInfoTimeout = old })
	code, msg, _ := a.h.executeNodeSwapSet(1, 2, 200*time.Millisecond)
	if code != -2 {
		t.Fatalf("failed reconcile must be code -2, got %d", code)
	}
	for _, want := range []string{"复查节点现状失败", "状态未知"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("unknown-state receipt must carry %q, got %q", want, msg)
		}
	}
}

func TestNodeSwapSetNonTimeoutDoesNotReconcile(t *testing.T) {
	a := newNodeSwapFakeAgent(t)
	// 节点 2 离线：SwapSet 腿立即报「节点不在线」，不是超时——
	// 不复查、原文回执，且全程不发 SwapInfo。
	code, msg, _ := a.h.executeNodeSwapSet(2, 2, 200*time.Millisecond)
	if code != -2 || !strings.Contains(msg, "不在线") {
		t.Fatalf("offline node must answer with the command-channel wording, got code=%d msg=%q", code, msg)
	}
	if cmds := a.commandsOfType("SwapInfo"); len(cmds) != 0 {
		t.Fatalf("non-timeout error must not trigger a reconcile, got %d SwapInfo commands", len(cmds))
	}
}

func TestIsNodeSwapSetTimeoutError(t *testing.T) {
	if !isNodeSwapSetTimeoutError(errors.New("等待节点响应超时")) {
		t.Fatalf("exact timeout text must match")
	}
	if isNodeSwapSetTimeoutError(nil) {
		t.Fatalf("nil must not match")
	}
	if isNodeSwapSetTimeoutError(errors.New("节点不在线")) {
		t.Fatalf("offline error must not match")
	}
	if isNodeSwapSetTimeoutError(errors.New("设置 Swap 失败（校验磁盘空间）: 磁盘空间不足")) {
		t.Fatalf("agent business error must not match")
	}
}

func TestParseSwapSetReconcileState(t *testing.T) {
	if st := parseSwapSetReconcileState(nil); st != (swapSetReconcileState{}) {
		t.Fatalf("nil data must parse to the zero state, got %+v", st)
	}
	st := parseSwapSetReconcileState(swapInfoDataForReconcile(t, 2048, true))
	if !st.managedActive || st.fileSizeMB != 2048 || st.totalMB != 2048 {
		t.Fatalf("full data must parse managedActive/fileSizeMB/totalMB, got %+v", st)
	}
	// 只有非托管条目时不得判托管在用。
	other := map[string]interface{}{
		"totalMB": float64(1024), "fileSizeMB": float64(0),
		"entries": []interface{}{map[string]interface{}{"name": "/dev/sda2", "type": "partition", "sizeMB": float64(1024)}},
	}
	if st := parseSwapSetReconcileState(other); st.managedActive {
		t.Fatalf("non-managed entry must not count as managedActive, got %+v", st)
	}
}
