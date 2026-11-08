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

// applyFlowItemViaFormalEntry 是 F-S02 迁移助手（仅测试代码）：
// 生产死代码 processFlowItem 已删，单条上报一律经正式入口组合
// buildNodeFlowUploadBatch + applyFlowUploadBatch。metas 获取与
// 原单条路径同口径（失败置 nil），断言语义不变。
func applyFlowItemViaFormalEntry(h *Handler, nodeID int64, item flowItem) {
	if h == nil || h.repo == nil || nodeID <= 0 {
		return
	}
	metas, err := h.repo.GetFlowUploadForwardMetas(collectFlowUploadForwardIDs([]flowItem{item}))
	if err != nil {
		metas = nil
	}
	// 本助手无 *testing.T 且只在健康测试库上驱动：归属查询出错时
	// 按零批次继续（与 metas 失败置 nil 同口径的驱动层宽容）；
	// 生产拒收口径由 flow_upload_ownership_failclosed_test.go 钉死。
	batch, _ := h.buildNodeFlowUploadBatch(nodeID, []flowItem{item}, metas)
	h.applyFlowUploadBatch(nodeID, batch, time.Now())
}

type cleanupAgentCommand struct {
	Type      string          `json:"type"`
	Data      json.RawMessage `json:"data"`
	RequestID string          `json:"requestId"`
}

// cleanupAgent exercises the actual command transport. Each command is recorded
// before its ACK, so synchronous handler calls need no sleeps to inspect it.
type cleanupAgent struct {
	h        *Handler
	mu       sync.Mutex
	commands []cleanupAgentCommand
}

func newCleanupAgent(t *testing.T) *cleanupAgent {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "cleanup.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	const secret = "cleanup-regression-node"
	if err := r.DB().Create(&model.Node{ID: 1, Name: "relay", Secret: secret, ServerIP: "127.0.0.1", Port: "30000", Status: 1}).Error; err != nil {
		t.Fatal(err)
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
	a := &cleanupAgent{h: &Handler{repo: r, wsServer: server}}
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
			a.mu.Unlock()
			if err := conn.WriteJSON(map[string]interface{}{"type": command.Type, "requestId": command.RequestID, "success": true}); err != nil {
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
		t.Fatal("mock node did not come online")
	}
	a.probe(t)
	return a
}

func (a *cleanupAgent) probe(t *testing.T) {
	t.Helper()
	if _, err := a.h.wsServer.SendCommand(1, "CleanupTestProbe", nil, time.Second); err != nil {
		t.Fatalf("mock agent transport unavailable: %v", err)
	}
}

func (a *cleanupAgent) commandsOfType(commandType string) []cleanupAgentCommand {
	a.mu.Lock()
	defer a.mu.Unlock()
	var commands []cleanupAgentCommand
	for _, command := range a.commands {
		if command.Type == commandType {
			commands = append(commands, command)
		}
	}
	return commands
}

func runCleanupPath(h *Handler, mode string, names []string) {
	items := make([]flowItem, 0, len(names))
	services := make([]namedConfigItem, 0, len(names))
	for _, name := range names {
		items = append(items, flowItem{N: name, U: 120, D: 80})
		services = append(services, namedConfigItem{Name: name})
	}
	switch mode {
	case "single":
		for _, item := range items {
			applyFlowItemViaFormalEntry(h, 1, item)
		}
	case "batch":
		// 同 applyFlowItemViaFormalEntry：驱动层宽容，健康库上不
		// 应出错；拒收口径由专门的 fail-closed 用例钉死。
		batch, _ := h.buildNodeFlowUploadBatch(1, items, nil)
		h.applyFlowUploadBatch(1, batch, time.Now())
	case "config":
		h.cleanOrphanedServices(1, services)
	}
}

func TestForwardCleanupMissingMetadataPreservesLocalForward(t *testing.T) {
	a := newCleanupAgent(t)
	if err := a.h.repo.DB().Create(&model.Forward{ID: 70, UserID: 1, UserName: "local", Name: "local", TunnelID: 1, RemoteAddr: "127.0.0.1:80", Status: 1}).Error; err != nil {
		t.Fatal(err)
	}
	runCleanupPath(a.h, "batch", []string{"70_1_0", "70_1_0_tcp", "70_1_0_udp"})
	a.probe(t)
	if commands := a.commandsOfType("DeleteService"); len(commands) != 0 {
		t.Fatalf("missing batch metadata must not delete a local forward: %+v", commands)
	}
}
