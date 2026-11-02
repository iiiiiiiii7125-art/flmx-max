package ws

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"go-backend/internal/security"
	"go-backend/internal/store/repo"
)

const uninstallTestSecret = "uninstall-test-secret-0123456789"

func uninstallRepo(t *testing.T) *repo.Repository {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "uninstall-ws.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// readUninstallCommand 读取面板下发的第一条消息（处理可选的 AES
// 包装），返回命令类型与 requestId。
func readUninstallCommand(t *testing.T, conn *websocket.Conn, secret string) (string, string) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read command: %v", err)
	}
	text := string(raw)
	var wrap struct {
		Encrypted bool   `json:"encrypted"`
		Data      string `json:"data"`
	}
	if err := json.Unmarshal(raw, &wrap); err == nil && wrap.Encrypted {
		crypto, err := security.NewAESCrypto(secret)
		if err != nil {
			t.Fatalf("crypto: %v", err)
		}
		plain, err := crypto.Decrypt(wrap.Data)
		if err != nil {
			t.Fatalf("decrypt command: %v", err)
		}
		text = string(plain)
	}
	var cmd struct {
		Type      string `json:"type"`
		RequestID string `json:"requestId"`
	}
	if err := json.Unmarshal([]byte(text), &cmd); err != nil {
		t.Fatalf("unmarshal command %q: %v", text, err)
	}
	return cmd.Type, cmd.RequestID
}

func replyUninstall(t *testing.T, conn *websocket.Conn, requestID string, success bool, message string) {
	t.Helper()
	resp, err := json.Marshal(map[string]interface{}{
		"type":      "UninstallAgentResponse",
		"success":   success,
		"message":   message,
		"requestId": requestID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, resp); err != nil {
		t.Fatalf("write reply: %v", err)
	}
}

func waitUninstallRow(t *testing.T, r *repo.Repository, secret string, wantPresent bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		row, err := r.GetNodeUninstallBySecret(secret)
		if err == nil && (row != nil) == wantPresent {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	row, err := r.GetNodeUninstallBySecret(secret)
	t.Fatalf("pending row present=%v, want %v (row=%v err=%v)", row != nil, wantPresent, row, err)
}

// 离线删服务器 → agent 重连 → 面板不注册它、直接下发卸载命令；
// agent 回执成功后登记清除，连接由面板关闭。
func TestServeUninstalledNodeSuccessClearsRegistry(t *testing.T) {
	r := uninstallRepo(t)
	if err := r.AddNodeUninstall(uninstallTestSecret, "gone-node", time.Now()); err != nil {
		t.Fatalf("seed pending: %v", err)
	}
	server := NewServer(r, "jwt-secret")
	ts := httptest.NewServer(server)
	defer ts.Close()

	conn, _, err := dialWS(t, ts, "type=1&secret="+uninstallTestSecret)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	cmdType, requestID := readUninstallCommand(t, conn, uninstallTestSecret)
	if cmdType != "UninstallAgent" || requestID == "" {
		t.Fatalf("expected UninstallAgent command with requestId, got type=%q id=%q", cmdType, requestID)
	}
	replyUninstall(t, conn, requestID, true, "OK")
	waitUninstallRow(t, r, uninstallTestSecret, false)

	// 面板处理完即关闭连接，后续读应失败。
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("server must close the connection after handling the uninstall")
	}
}

// agent 回「未知命令」（版本过旧）：登记同样清除，避免每次重连重复
// 打扰一台永远学不会自卸载的 agent。
func TestServeUninstalledNodeUnsupportedClearsRegistry(t *testing.T) {
	r := uninstallRepo(t)
	if err := r.AddNodeUninstall(uninstallTestSecret, "old-node", time.Now()); err != nil {
		t.Fatalf("seed pending: %v", err)
	}
	server := NewServer(r, "jwt-secret")
	ts := httptest.NewServer(server)
	defer ts.Close()

	conn, _, err := dialWS(t, ts, "type=1&secret="+uninstallTestSecret)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	_, requestID := readUninstallCommand(t, conn, uninstallTestSecret)
	replyUninstall(t, conn, requestID, false, "未知命令类型: UninstallAgent")
	waitUninstallRow(t, r, uninstallTestSecret, false)
}

// agent 回执其他失败：保留登记，下次重连再试。
func TestServeUninstalledNodeFailureKeepsRegistry(t *testing.T) {
	r := uninstallRepo(t)
	if err := r.AddNodeUninstall(uninstallTestSecret, "flaky-node", time.Now()); err != nil {
		t.Fatalf("seed pending: %v", err)
	}
	server := NewServer(r, "jwt-secret")
	ts := httptest.NewServer(server)
	defer ts.Close()

	conn, _, err := dialWS(t, ts, "type=1&secret="+uninstallTestSecret)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	_, requestID := readUninstallCommand(t, conn, uninstallTestSecret)
	replyUninstall(t, conn, requestID, false, "uninstall script failed")
	// 面板保留登记并关闭连接：等连接关闭后再断言登记仍在。
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, _ = conn.ReadMessage()
	waitUninstallRow(t, r, uninstallTestSecret, true)
}

// 没有待卸载登记的未知 secret 仍按原路径 403，不被卸载通道接管。
func TestServeUninstalledNodeNotPendingStaysForbidden(t *testing.T) {
	r := uninstallRepo(t)
	server := NewServer(r, "jwt-secret")
	ts := httptest.NewServer(server)
	defer ts.Close()

	_, resp, err := dialWS(t, ts, "type=1&secret=no-such-secret")
	if err == nil {
		t.Fatal("dial must fail for an unknown secret without pending uninstall")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %v", resp)
	}
}
