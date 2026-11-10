package handler

import (
	"encoding/json"
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
	reply    func(command cleanupAgentCommand) (success bool, message string, data map[string]interface{})
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
			reply := a.reply
			a.mu.Unlock()
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
	const bizMsg = "设置 Swap 失败（校验磁盘空间）: 磁盘空间不足：设置 4 GB Swap 需要可用 4352 MB（目标 4096 MB＋预留 256 MB），当前可用 1000 MB（磁盘剩余 1000 MB＋现有 /swapfile 占量 0 MB）；机器状态未改变"
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
		t.Fatalf("fileSizeMB must pass through untouched (frontend precheck mirrors the agent predicate with it), got %+v", gotInfo)
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
