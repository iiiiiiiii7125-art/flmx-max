package ws

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"go-backend/internal/auth"
	"go-backend/internal/security"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

const fix132Secret = "fix132-ws-secret"

func fix132Repo(t *testing.T) *repo.Repository {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func fix132AdminServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	server := NewServer(nil, fix132Secret)
	server.SetUserAuthStateLookup(func(userID int64) (*auth.UserAuthState, error) {
		return &auth.UserAuthState{ID: userID, RoleID: 0, Status: 1, PasswordChangedAt: 0}, nil
	})
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)
	return server, ts
}

func fix132AdminToken(t *testing.T, userID int64) string {
	t.Helper()
	token, err := auth.GenerateToken(userID, "admin", 0, fix132Secret)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	return token
}

func dialWS(t *testing.T, ts *httptest.Server, query string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	u := "ws" + strings.TrimPrefix(ts.URL, "http") + "/system-info?" + query
	return websocket.DefaultDialer.Dial(u, nil)
}

func dialAdmin(t *testing.T, ts *httptest.Server, userID int64) *websocket.Conn {
	t.Helper()
	conn, _, err := dialWS(t, ts, "type=0&secret="+url.QueryEscape(fix132AdminToken(t, userID)))
	if err != nil {
		t.Fatalf("dial admin ws: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func dialNode(t *testing.T, ts *httptest.Server, secret string) *websocket.Conn {
	t.Helper()
	conn, _, err := dialWS(t, ts, "type=1&secret="+url.QueryEscape(secret))
	if err != nil {
		t.Fatalf("dial node ws: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func waitNodeOnline(t *testing.T, server *Server, nodeID int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if server.IsNodeOnline(nodeID) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("node %d session not registered in time", nodeID)
}

func readWithTimeout(conn *websocket.Conn, d time.Duration) (string, error) {
	_ = conn.SetReadDeadline(time.Now().Add(d))
	_, msg, err := conn.ReadMessage()
	return string(msg), err
}

// FIX-A #6（D2-02）：带 requestId 的命令响应是定向回执，绝不得广播给
// admin/monitor 会话；未知类型同样不得广播（白名单制）。
// 修复前 default 分支把响应原文以 type:"info" 全量广播 → FAIL。
func TestCommandResponseNotBroadcast(t *testing.T) {
	r := fix132Repo(t)
	if err := r.DB().Create(&model.Node{ID: 42, Name: "n42", Secret: "node-secret-42", ServerIP: "127.0.0.1", Port: "30000", Status: 1}).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	server := NewServer(r, fix132Secret)
	server.SetUserAuthStateLookup(func(userID int64) (*auth.UserAuthState, error) {
		return &auth.UserAuthState{ID: userID, RoleID: 0, Status: 1, PasswordChangedAt: 0}, nil
	})
	ts := httptest.NewServer(server)
	defer ts.Close()

	admin := dialAdmin(t, ts, 7)
	// 后台收集 admin 收到的全部消息（gorilla 连接读超时后不可复用，
	// 负断言必须走 channel 而非 SetReadDeadline）。
	adminMsgs := collectMessages(admin)
	node := dialNode(t, ts, "node-secret-42")
	waitNodeOnline(t, server, 42)
	// 排空节点上线时合法的 status 广播，避免与后续断言串扰。
	if msg := nextMessage(t, adminMsgs, 2*time.Second); !strings.Contains(msg, `"status"`) {
		t.Fatalf("expected node-online status broadcast, got %q", msg)
	}

	// 在途命令：响应应只回 pending 通道。
	resCh := make(chan CommandResult, 1)
	go func() {
		res, err := server.SendCommand(42, "TcpPing", map[string]interface{}{"target": "127.0.0.1"}, 5*time.Second)
		if err == nil {
			resCh <- res
		}
	}()
	// 读到命令（面板侧对节点会话加密下发）后回带 requestId 的响应。
	_ = node.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, raw, err := node.ReadMessage()
	if err != nil {
		t.Fatalf("node read command: %v", err)
	}
	var wrap encryptedMessage
	if err := json.Unmarshal(raw, &wrap); err == nil && wrap.Encrypted {
		crypto, err := security.NewAESCrypto("node-secret-42")
		if err != nil {
			t.Fatalf("new crypto: %v", err)
		}
		plain, err := crypto.Decrypt(wrap.Data)
		if err != nil {
			t.Fatalf("decrypt command: %v", err)
		}
		raw = plain
	}
	var cmd struct {
		RequestID string `json:"requestId"`
	}
	if err := json.Unmarshal(raw, &cmd); err != nil || cmd.RequestID == "" {
		t.Fatalf("command missing requestId: err=%v raw=%s", err, raw)
	}
	resp := `{"type":"TcpPingResponse","success":true,"requestId":"` + cmd.RequestID + `","data":{"latencyMs":3,"target":"sensitive-target"}}`
	if err := node.WriteMessage(websocket.TextMessage, []byte(resp)); err != nil {
		t.Fatalf("node write response: %v", err)
	}
	select {
	case <-resCh:
	case <-time.After(3 * time.Second):
		t.Fatal("pending command did not resolve")
	}
	// admin 会话不得收到这条响应（修复前会收到 type:"info" 广播）。
	assertNoMessage(t, adminMsgs, 600*time.Millisecond, "command response leaked to admin session via broadcast")

	// 未知类型（无 requestId）同样不得广播。
	if err := node.WriteMessage(websocket.TextMessage, []byte(`{"type":"FutureAgentThing","data":{"secret":"x"}}`)); err != nil {
		t.Fatalf("node write unknown type: %v", err)
	}
	assertNoMessage(t, adminMsgs, 600*time.Millisecond, "unknown agent message type leaked to admin session via broadcast")

	// 正向对照：metric 仍可达 admin 会话。
	if err := node.WriteMessage(websocket.TextMessage, []byte(`{"type":"metric","data":{"uptime":5,"cpu_usage":1.5,"memory_usage":2.5}}`)); err != nil {
		t.Fatalf("node write metric: %v", err)
	}
	if msg := nextMessage(t, adminMsgs, 2*time.Second); !strings.Contains(msg, "metric") {
		t.Fatalf("metric broadcast broken: msg=%q", msg)
	}
}

func collectMessages(conn *websocket.Conn) chan string {
	ch := make(chan string, 64)
	go func() {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			ch <- string(msg)
		}
	}()
	return ch
}

func nextMessage(t *testing.T, ch chan string, d time.Duration) string {
	t.Helper()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(d):
		t.Fatalf("no message within %v", d)
		return ""
	}
}

func assertNoMessage(t *testing.T, ch chan string, d time.Duration, what string) {
	t.Helper()
	select {
	case msg := <-ch:
		t.Fatalf("%s: %s", what, msg)
	case <-time.After(d):
	}
}

// FIX-A #6（D2-04/D6-01）：广播不得在节点读循环内同步阻塞——
// 一个永不读取的 admin 会话不得拖住其余会话收到 metric。
func TestBroadcastNotBlockedBySlowConsumer(t *testing.T) {
	r := fix132Repo(t)
	if err := r.DB().Create(&model.Node{ID: 43, Name: "n43", Secret: "node-secret-43", ServerIP: "127.0.0.1", Port: "30000", Status: 1}).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	server := NewServer(r, fix132Secret)
	server.SetUserAuthStateLookup(func(userID int64) (*auth.UserAuthState, error) {
		return &auth.UserAuthState{ID: userID, RoleID: 0, Status: 1, PasswordChangedAt: 0}, nil
	})
	ts := httptest.NewServer(server)
	defer ts.Close()

	_ = dialAdmin(t, ts, 8)     // 慢消费者：连上后永不读
	fast := dialAdmin(t, ts, 9) // 正常消费者
	node := dialNode(t, ts, "node-secret-43")
	waitNodeOnline(t, server, 43)

	// 连发多条大 metric：足以塞满慢消费者的套接字缓冲。修复前广播在
	// 节点读循环内同步串行写，慢消费者每次写吃满 5s 写截止，正常消费者
	// 被队头阻塞；修复后广播走异步队列，慢消费者不影响正常消费者。
	pad := strings.Repeat("p", 512<<10)
	payload := `{"type":"metric","data":{"uptime":1,"cpu_usage":1,"memory_usage":1,"pad":"` + pad + `"}}`
	for i := 0; i < 48; i++ {
		if err := node.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
			t.Fatalf("node write metric %d: %v", i, err)
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	got := false
	for time.Now().Before(deadline) {
		msg, err := readWithTimeout(fast, time.Until(deadline))
		if err != nil {
			break
		}
		if strings.Contains(msg, "metric") {
			got = true
			break
		}
	}
	if !got {
		t.Fatal("healthy admin session starved by slow consumer (head-of-line blocking in broadcast)")
	}
}

// FIX-A #7（D2-03）：同一用户的实时会话数有上限。
func TestPerUserSessionLimit(t *testing.T) {
	_, ts := fix132AdminServer(t)
	var conns []*websocket.Conn
	for i := 0; i < wsMaxSessionsPerUser; i++ {
		conns = append(conns, dialAdmin(t, ts, 11))
	}
	conn, resp, err := dialWS(t, ts, "type=0&secret="+url.QueryEscape(fix132AdminToken(t, 11)))
	if err == nil {
		_ = conn.Close()
		t.Fatalf("session %d for same user accepted; per-user cap missing", wsMaxSessionsPerUser+1)
	}
	if resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		t.Fatalf("over-limit dial: status=%d err=%v, want 429", code, err)
	}
	// 其他用户不受影响。
	_ = dialAdmin(t, ts, 12)
	for _, c := range conns {
		_ = c.Close()
	}
}

// FIX-A #7（D2-03）：/system-info 未认证失败尝试按 IP 限流，
// 防止伪造 secret 洪泛独占单连接 DB。
func TestWSAuthFailureLimiter(t *testing.T) {
	l := newWSAuthFailLimiter()
	now := time.Now()
	ip := "203.0.113.7"
	for i := 0; i < wsAuthFailMaxPerWindow; i++ {
		if l.blocked(ip, now) {
			t.Fatalf("blocked too early at failure %d", i+1)
		}
		l.recordFailure(ip, now)
	}
	if !l.blocked(ip, now) {
		t.Fatal("limiter did not engage after threshold failures")
	}
	if l.blocked("198.51.100.3", now) {
		t.Fatal("unrelated IP blocked")
	}
	if l.blocked(ip, now.Add(wsAuthFailWindow+time.Second)) {
		t.Fatal("limiter did not reset after window")
	}
}

// FIX-A #8（D2-05）：DisconnectNode 必须断开已删节点的存量会话，
// 且 keepalive 的节点存在性复验在节点删除/secret 轮换后必须失败。
func TestDisconnectNodeAndValidateNodeSession(t *testing.T) {
	r := fix132Repo(t)
	if err := r.DB().Create(&model.Node{ID: 44, Name: "n44", Secret: "node-secret-44", ServerIP: "127.0.0.1", Port: "30000", Status: 1}).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	server := NewServer(r, fix132Secret)
	ts := httptest.NewServer(server)
	defer ts.Close()

	node := dialNode(t, ts, "node-secret-44")
	waitNodeOnline(t, server, 44)
	if !server.validateNodeSession(44, "node-secret-44") {
		t.Fatal("live node session must validate")
	}

	server.DisconnectNode(44)
	if server.IsNodeOnline(44) {
		t.Fatal("DisconnectNode did not remove the session")
	}
	if _, err := readWithTimeout(node, 2*time.Second); err == nil {
		t.Fatal("client connection still open after DisconnectNode")
	}

	// 节点行删除后复验失败（keepalive 将据此踢线）。
	if err := r.DB().Exec(`DELETE FROM node WHERE id = 44`).Error; err != nil {
		t.Fatalf("delete node row: %v", err)
	}
	if server.validateNodeSession(44, "node-secret-44") {
		t.Fatal("deleted node session still validates (ghost session would live forever)")
	}
	// secret 轮换后旧 secret 复验失败。
	if err := r.DB().Create(&model.Node{ID: 45, Name: "n45", Secret: "rotated-secret", ServerIP: "127.0.0.1", Port: "30000", Status: 1}).Error; err != nil {
		t.Fatalf("create node 45: %v", err)
	}
	if server.validateNodeSession(45, "node-secret-44") {
		t.Fatal("stale secret still validates after rotation")
	}
}
