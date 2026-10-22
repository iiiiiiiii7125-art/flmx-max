package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"go-backend/internal/auth"
	"go-backend/internal/security"
	"go-backend/internal/store/repo"
)

type encryptedMessage struct {
	Encrypted bool   `json:"encrypted"`
	Data      string `json:"data"`
	Timestamp int64  `json:"timestamp"`
}

type broadcastMessage struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
	Data string `json:"data"`
}

type connWrap struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

type nodeSession struct {
	nodeID int64
	secret string
	conn   *connWrap
	crypto *security.AESCrypto // 缓存的 AES 加密器，避免每条消息重建
}

type adminSession struct {
	userID int64
	claims auth.Claims
	conn   *connWrap
	queue  chan []byte
}

type monitorSession struct {
	userID int64
	claims auth.Claims
	conn   *connWrap
	queue  chan []byte
}

type commandResponse struct {
	Type      string          `json:"type"`
	Success   bool            `json:"success"`
	Message   string          `json:"message"`
	Data      json.RawMessage `json:"data,omitempty"`
	RequestID string          `json:"requestId,omitempty"`
}

type pendingRequest struct {
	nodeID int64
	ch     chan CommandResult
}

const (
	wsPingPeriod = 15 * time.Second
	wsPongWait   = 45 * time.Second
	wsWriteWait  = 5 * time.Second
	// Max inbound WebSocket message size (10MB). Prevents a malicious or
	// compromised peer from exhausting server memory with a single huge frame.
	wsMaxMessageSize = 10 << 20
	// D2-03: 同一用户可并存的实时会话上限（admin / monitor 各自计）。
	// 前端全局共享一条连接，10 条足够多标签/多设备并存；超限拒新连接。
	wsMaxSessionsPerUser = 10
	// D2-04/D6-01: 每会话广播发送队列深度。队列满即丢弃该会话的本条
	// 实时消息（metric/status 均为最新值语义，可丢），绝不阻塞广播方。
	wsBroadcastQueueSize = 16
	// D2-03: /system-info 未认证失败尝试按 IP 限流的窗口与上限。
	// 每次失败鉴权都要打一次 DB（GetNodeBySecret / token 校验），
	// 无限流时伪造 secret 洪泛可独占单连接 SQLite。
	wsAuthFailWindow       = time.Minute
	wsAuthFailMaxPerWindow = 60
	wsAuthFailMaxHosts     = 4096
)

type CommandResult struct {
	Type    string                 `json:"type"`
	Success bool                   `json:"success"`
	Message string                 `json:"message"`
	Data    map[string]interface{} `json:"data,omitempty"`
}

type Server struct {
	repo             *repo.Repository
	jwtSecret        string
	upgrader         websocket.Upgrader
	onNodeOnline     func(nodeID int64)
	onNodeMetric     func(nodeID int64, info SystemInfo)
	onNodeOffline    func(nodeID int64)
	getUserAuthState func(userID int64) (*auth.UserAuthState, error)

	mu       sync.RWMutex
	admins   map[*adminSession]struct{}
	monitors map[*monitorSession]struct{}
	nodes    map[int64]*nodeSession
	byConn   map[*websocket.Conn]*nodeSession
	pending  map[string]pendingRequest

	authFail *wsAuthFailLimiter
}

// wsAuthFailLimiter 记录 /system-info 的未认证失败尝试（按 IP 固定窗口）。
// 只对失败计数：正常 agent 重连与浏览器连接不受影响。
type wsAuthFailBucket struct {
	count       int
	windowStart time.Time
}

type wsAuthFailLimiter struct {
	mu      sync.Mutex
	buckets map[string]*wsAuthFailBucket
}

func newWSAuthFailLimiter() *wsAuthFailLimiter {
	return &wsAuthFailLimiter{buckets: make(map[string]*wsAuthFailBucket)}
}

// blocked 报告该 IP 在当前窗口内失败次数是否已达上限。
func (l *wsAuthFailLimiter) blocked(ip string, now time.Time) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[ip]
	if !ok || now.Sub(b.windowStart) >= wsAuthFailWindow {
		return false
	}
	return b.count >= wsAuthFailMaxPerWindow
}

func (l *wsAuthFailLimiter) recordFailure(ip string, now time.Time) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[ip]
	if !ok {
		if len(l.buckets) >= wsAuthFailMaxHosts {
			// 满额先清过期桶，避免 map 无界增长（与 D6-05 同型治理）。
			for k, v := range l.buckets {
				if now.Sub(v.windowStart) >= wsAuthFailWindow {
					delete(l.buckets, k)
				}
			}
			if len(l.buckets) >= wsAuthFailMaxHosts {
				return
			}
		}
		l.buckets[ip] = &wsAuthFailBucket{count: 1, windowStart: now}
		return
	}
	if now.Sub(b.windowStart) >= wsAuthFailWindow {
		b.count = 1
		b.windowStart = now
		return
	}
	b.count++
}

type SystemInfo struct {
	Uptime           uint64  `json:"uptime"`
	BytesReceived    uint64  `json:"bytes_received"`
	BytesTransmitted uint64  `json:"bytes_transmitted"`
	CPUUsage         float64 `json:"cpu_usage"`
	MemoryUsage      float64 `json:"memory_usage"`
	DiskUsage        float64 `json:"disk_usage"`
	Load1            float64 `json:"load1"`
	Load5            float64 `json:"load5"`
	Load15           float64 `json:"load15"`
	TCPConns         int64   `json:"tcp_conns"`
	UDPConns         int64   `json:"udp_conns"`
	NetInSpeed       int64   `json:"net_in_speed"`
	NetOutSpeed      int64   `json:"net_out_speed"`
}

func (s *Server) SetNodeOnlineHook(fn func(nodeID int64)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.onNodeOnline = fn
	s.mu.Unlock()
}

func (s *Server) SetNodeOfflineHook(fn func(nodeID int64)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.onNodeOffline = fn
	s.mu.Unlock()
}

// DisconnectNode 主动断开指定节点的 agent 会话（D2-05）。
// 删节点后调用：移除会话、在途命令以失败收尾、关连接并广播离线。
// 节点行已删除，此处不再写 DB 状态。
func (s *Server) DisconnectNode(nodeID int64) {
	if s == nil {
		return
	}
	s.mu.Lock()
	ns, ok := s.nodes[nodeID]
	if ok {
		delete(s.nodes, nodeID)
		if ns != nil && ns.conn != nil && ns.conn.conn != nil {
			delete(s.byConn, ns.conn.conn)
		}
	}
	s.mu.Unlock()
	if !ok || ns == nil {
		return
	}
	s.failPendingForNode(nodeID, "节点连接已断开")
	if ns.conn != nil && ns.conn.conn != nil {
		_ = ns.conn.conn.Close()
	}
	s.broadcastStatus(nodeID, 0)
}

// validateNodeSession 是节点会话的 keepalive 复验（D2-05）：
// 节点仍存在且 secret 与建连时一致才有效——覆盖删节点（DB 侧
// 直接删除）与 secret 轮换两种存量会话失效场景。无 repo 的嵌入
// 场景无法复验，按有效处理（生产装配恒有 repo）。
func (s *Server) validateNodeSession(nodeID int64, secret string) bool {
	if s == nil || s.repo == nil {
		return true
	}
	node, err := s.repo.GetNodeBySecret(secret)
	return err == nil && node != nil && node.ID == nodeID
}

// IsNodeOnline reports whether the node has a live agent connection.
func (s *Server) IsNodeOnline(nodeID int64) bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.nodes[nodeID]
	return ok
}

func (s *Server) SetNodeMetricHook(fn func(nodeID int64, info SystemInfo)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.onNodeMetric = fn
	s.mu.Unlock()
}

func NewServer(repo *repo.Repository, jwtSecret string) *Server {
	return &Server{
		repo:      repo,
		jwtSecret: jwtSecret,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		admins:   make(map[*adminSession]struct{}),
		monitors: make(map[*monitorSession]struct{}),
		nodes:    make(map[int64]*nodeSession),
		byConn:   make(map[*websocket.Conn]*nodeSession),
		pending:  make(map[string]pendingRequest),
		authFail: newWSAuthFailLimiter(),
	}
}

func (s *Server) SetUserAuthStateLookup(fn func(userID int64) (*auth.UserAuthState, error)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.getUserAuthState = fn
	s.mu.Unlock()
}

func wsClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || host == "" {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	typeVal := query.Get("type")
	secret := query.Get("secret")
	clientIP := wsClientIP(r)
	now := time.Now()

	// D2-03：失败鉴权已达上限的 IP 直接 429，不再打 DB。
	if s.authFail.blocked(clientIP, now) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	forbidden := func() {
		s.authFail.recordFailure(clientIP, now)
		http.Error(w, "forbidden", http.StatusForbidden)
	}

	if typeVal == "1" {
		node, err := s.repo.GetNodeBySecret(secret)
		if err != nil || node == nil {
			forbidden()
			return
		}
		s.handleNode(w, r, node.ID, secret)
		return
	}

	if typeVal == "0" {
		claims, ok := auth.ValidateToken(secret, s.jwtSecret)
		if !ok {
			forbidden()
			return
		}
		userID, err := strconv.ParseInt(claims.Sub, 10, 64)
		if err != nil {
			forbidden()
			return
		}
		if claims.RoleID == 0 {
			if !s.validateAdminSession(userID, claims) {
				forbidden()
				return
			}
			s.handleAdmin(w, r, userID, claims)
			return
		}
		if !s.validateMonitorSession(userID, claims) {
			forbidden()
			return
		}
		s.handleMonitor(w, r, userID, claims)
		return
	}

	http.Error(w, "bad request", http.StatusBadRequest)
}

// userSessionCounts 返回该用户当前 admin / monitor 会话数（D2-03 上限判定）。
func (s *Server) userSessionCounts(userID int64) (admins int, monitors int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for sess := range s.admins {
		if sess.userID == userID {
			admins++
		}
	}
	for sess := range s.monitors {
		if sess.userID == userID {
			monitors++
		}
	}
	return admins, monitors
}

// runRealtimeWriter 是每会话独立的广播写 goroutine（D2-04/D6-01）：
// 广播方只往会话队列非阻塞投递，慢消费者只会拖住自己的 writer——
// 写失败即关连接逐出（读循环随之退出并清理会话），不阻塞任何人。
func runRealtimeWriter(cw *connWrap, queue <-chan []byte, done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case msg := <-queue:
			if err := writeWSMessage(cw, websocket.TextMessage, msg); err != nil {
				_ = cw.conn.Close()
				return
			}
		}
	}
}

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request, userID int64, claims auth.Claims) {
	if admins, _ := s.userSessionCounts(userID); admins >= wsMaxSessionsPerUser {
		http.Error(w, "too many sessions", http.StatusTooManyRequests)
		return
	}
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(wsMaxMessageSize)
	cw := &connWrap{conn: conn}
	_ = conn.SetReadDeadline(time.Now().Add(wsPongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(wsPongWait))
	})
	done := make(chan struct{})
	session := &adminSession{userID: userID, claims: claims, conn: cw, queue: make(chan []byte, wsBroadcastQueueSize)}
	go runRealtimeWriter(cw, session.queue, done)
	go startKeepalive(cw, done, func() bool {
		return s.validateAdminSession(session.userID, session.claims)
	})

	s.mu.Lock()
	s.admins[session] = struct{}{}
	s.mu.Unlock()

	defer func() {
		close(done)
		s.mu.Lock()
		delete(s.admins, session)
		s.mu.Unlock()
		_ = conn.Close()
	}()

	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

func (s *Server) handleMonitor(w http.ResponseWriter, r *http.Request, userID int64, claims auth.Claims) {
	if _, monitors := s.userSessionCounts(userID); monitors >= wsMaxSessionsPerUser {
		http.Error(w, "too many sessions", http.StatusTooManyRequests)
		return
	}
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(wsMaxMessageSize)
	cw := &connWrap{conn: conn}
	_ = conn.SetReadDeadline(time.Now().Add(wsPongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(wsPongWait))
	})
	done := make(chan struct{})
	session := &monitorSession{userID: userID, claims: claims, conn: cw, queue: make(chan []byte, wsBroadcastQueueSize)}
	go runRealtimeWriter(cw, session.queue, done)
	go startKeepalive(cw, done, func() bool {
		return s.validateMonitorSession(session.userID, session.claims)
	})

	s.mu.Lock()
	s.monitors[session] = struct{}{}
	s.mu.Unlock()

	defer func() {
		close(done)
		s.mu.Lock()
		delete(s.monitors, session)
		s.mu.Unlock()
		_ = conn.Close()
	}()

	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

func (s *Server) handleNode(w http.ResponseWriter, r *http.Request, nodeID int64, secret string) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(wsMaxMessageSize)
	cw := &connWrap{conn: conn}
	_ = conn.SetReadDeadline(time.Now().Add(wsPongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(wsPongWait))
	})
	done := make(chan struct{})
	// D2-05：节点会话与用户会话对齐——keepalive 周期复验节点仍存在
	// 且 secret 未轮换；删节点/换 secret 后存量会话最迟一个周期被踢线。
	go startKeepalive(cw, done, func() bool {
		return s.validateNodeSession(nodeID, secret)
	})

	version := r.URL.Query().Get("version")
	httpVal := parseIntDefault(r.URL.Query().Get("http"), 0)
	tlsVal := parseIntDefault(r.URL.Query().Get("tls"), 0)
	socksVal := parseIntDefault(r.URL.Query().Get("socks"), 0)

	s.mu.Lock()
	if old, ok := s.nodes[nodeID]; ok {
		_ = old.conn.conn.Close()
		delete(s.byConn, old.conn.conn)
	}
	// 初始化 AES 加密器并缓存（仅创建一次）
	var nodeCrypto *security.AESCrypto
	if strings.TrimSpace(secret) != "" {
		nodeCrypto, _ = security.NewAESCrypto(secret)
	}
	ns := &nodeSession{nodeID: nodeID, secret: secret, conn: cw, crypto: nodeCrypto}
	s.nodes[nodeID] = ns
	s.byConn[conn] = ns
	s.mu.Unlock()

	_ = s.repo.UpdateNodeOnline(nodeID, 1, version, httpVal, tlsVal, socksVal)
	s.broadcastStatus(nodeID, 1)

	s.mu.RLock()
	onlineHook := s.onNodeOnline
	s.mu.RUnlock()
	if onlineHook != nil {
		go onlineHook(nodeID)
	}

	defer func() {
		close(done)
		needOfflineBroadcast := false
		s.mu.Lock()
		current, ok := s.nodes[nodeID]
		if ok && current.conn != nil && current.conn.conn == conn {
			delete(s.nodes, nodeID)
			needOfflineBroadcast = true
		}
		delete(s.byConn, conn)
		s.mu.Unlock()
		if needOfflineBroadcast {
			s.failPendingForNode(nodeID, "节点连接已断开")
			_ = s.repo.UpdateNodeStatus(nodeID, 0)
			s.broadcastStatus(nodeID, 0)
			s.mu.RLock()
			offlineHook := s.onNodeOffline
			s.mu.RUnlock()
			if offlineHook != nil {
				go offlineHook(nodeID)
			}
		}
		_ = conn.Close()
	}()

	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			return
		}

		msg := decryptIfNeeded(payload, ns.crypto, secret)
		s.tryResolvePending(nodeID, msg)

		var parsed struct {
			Type      string `json:"type"`
			RequestID string `json:"requestId"`
		}
		_ = json.Unmarshal([]byte(msg), &parsed)
		// D2-02：带 requestId 的是命令的定向响应，已由 tryResolvePending
		// 回执给等待方，绝不广播（旧实现经 default 分支把响应原文推给
		// 全部 admin+monitor 会话，越过其 REST 可见范围）。
		if parsed.RequestID != "" {
			continue
		}
		if parsed.Type != "" {
			switch parsed.Type {
			case "metric":
				// Agent 新版指标消息：{type:"metric", data:{...}}
				var envelope struct {
					Data json.RawMessage `json:"data"`
				}
				if err := json.Unmarshal([]byte(msg), &envelope); err == nil && len(envelope.Data) > 0 {
					// 解析 SystemInfo 并调用 hook
					var sysInfo SystemInfo
					if json.Unmarshal(envelope.Data, &sysInfo) == nil {
						s.mu.RLock()
						onMetric := s.onNodeMetric
						s.mu.RUnlock()
						if onMetric != nil {
							// X-P3-30（L-12-1）：直调。hook 本体仅内存
							// 追加、短命；旧实现每帧起 goroutine 无背压，
							// 高频帧可瞬时堆积 goroutine。
							onMetric(nodeID, sysInfo)
						}
					}
					// 广播内层 data 给前端（保持平坦结构兼容性）
					s.broadcastTyped(nodeID, "metric", string(envelope.Data))
				}
				continue
			case "UpgradeProgress":
				s.broadcastTyped(nodeID, "upgrade_progress", msg)
				continue
			default:
				// D2-02：广播改白名单制——仅 metric / upgrade_progress
				// 进实时广播。未知类型一律不广播（旧 default 全量广播是
				// fail-open：未来新增 agent 上行类型会自动进入 monitor
				// 可见集）。只记类型名，不记原文（原文可能含敏感数据）。
				log.Printf("websocket: dropping non-whitelisted agent message type=%q from node %d", parsed.Type, nodeID)
				continue
			}
		}

		// 兼容旧版 Agent：无 type 字段的系统信息消息
		if looksLikeSystemInfoMessage(msg) {
			var sysInfo SystemInfo
			if err := json.Unmarshal([]byte(msg), &sysInfo); err == nil {
				s.mu.RLock()
				onMetric := s.onNodeMetric
				s.mu.RUnlock()
				if onMetric != nil {
					// X-P3-30（L-12-1）：同 metric 分支，直调不另起 goroutine。
					onMetric(nodeID, sysInfo)
				}
				s.broadcastTyped(nodeID, "metric", msg)
				continue
			}
		}

		// D2-02：无 type 且不像系统信息的消息同样不广播（白名单制）。
	}
}

func looksLikeSystemInfoMessage(msg string) bool {
	// Keep this as a cheap heuristic so that arbitrary JSON objects don't get
	// misclassified as metrics (SystemInfo unmarshal would otherwise succeed with
	// all-zero values).
	if strings.TrimSpace(msg) == "" {
		return false
	}
	if !strings.Contains(msg, "{") {
		return false
	}

	keys := []string{
		"\"uptime\"",
		"\"cpu_usage\"",
		"\"memory_usage\"",
		"\"disk_usage\"",
		"\"bytes_received\"",
		"\"bytes_transmitted\"",
		"\"net_in_speed\"",
		"\"net_out_speed\"",
		"\"tcp_conns\"",
		"\"udp_conns\"",
		"\"load1\"",
		"\"load5\"",
		"\"load15\"",
	}
	matched := 0
	for _, k := range keys {
		if strings.Contains(msg, k) {
			matched++
			if matched >= 3 {
				return true
			}
		}
	}
	return false
}

func (s *Server) SendCommand(nodeID int64, cmdType string, data interface{}, timeout time.Duration) (CommandResult, error) {
	return s.SendCommandContext(context.Background(), nodeID, cmdType, data, timeout)
}

// SendCommandContext X-P3-26：带上下文的 SendCommand 变体——ctx 取消时
// 立即清理 pending 并返回 ctx.Err()，不再等满 timeout（批量升级/下发在
// 请求取消后须停止派发与等待）。
func (s *Server) SendCommandContext(ctx context.Context, nodeID int64, cmdType string, data interface{}, timeout time.Duration) (CommandResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return CommandResult{}, err
	}
	if s == nil {
		return CommandResult{}, errors.New("server not initialized")
	}
	if strings.TrimSpace(cmdType) == "" {
		return CommandResult{}, errors.New("command type is empty")
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	s.mu.RLock()
	ns, ok := s.nodes[nodeID]
	s.mu.RUnlock()
	if !ok || ns == nil || ns.conn == nil || ns.conn.conn == nil {
		return CommandResult{}, errors.New("节点不在线")
	}

	requestID := fmt.Sprintf("%d_%d", nodeID, time.Now().UnixNano())
	ch := make(chan CommandResult, 1)

	s.mu.Lock()
	s.pending[requestID] = pendingRequest{nodeID: nodeID, ch: ch}
	s.mu.Unlock()

	cleanup := func() {
		s.mu.Lock()
		if p, exists := s.pending[requestID]; exists {
			delete(s.pending, requestID)
			close(p.ch)
		}
		s.mu.Unlock()
	}

	cmdPayload := map[string]interface{}{
		"type":      cmdType,
		"data":      data,
		"requestId": requestID,
	}
	rawCmd, err := json.Marshal(cmdPayload)
	if err != nil {
		cleanup()
		return CommandResult{}, err
	}

	messageData := rawCmd
	if ns.crypto != nil {
		encrypted, err := ns.crypto.Encrypt(rawCmd)
		if err != nil {
			cleanup()
			return CommandResult{}, err
		}
		wrapper := map[string]interface{}{
			"encrypted": true,
			"data":      encrypted,
			"timestamp": time.Now().UnixMilli(),
		}
		messageData, err = json.Marshal(wrapper)
		if err != nil {
			cleanup()
			return CommandResult{}, err
		}
	}

	err = writeWSMessage(ns.conn, websocket.TextMessage, messageData)
	if err != nil {
		cleanup()
		return CommandResult{}, err
	}

	select {
	case <-ctx.Done():
		cleanup()
		return CommandResult{}, ctx.Err()
	case result, ok := <-ch:
		if !ok {
			return CommandResult{}, errors.New("命令通道已关闭")
		}
		if !result.Success {
			if strings.TrimSpace(result.Message) == "" {
				result.Message = "命令执行失败"
			}
			return result, errors.New(result.Message)
		}
		return result, nil
	case <-time.After(timeout):
		cleanup()
		return CommandResult{}, errors.New("等待节点响应超时")
	}
}

func (s *Server) tryResolvePending(nodeID int64, message string) {
	if s == nil || strings.TrimSpace(message) == "" {
		return
	}

	// 快速短路：指标消息永远不含 requestId，跳过完整 JSON 解析
	if !strings.Contains(message, "\"requestId\"") {
		return
	}

	var resp commandResponse
	if err := json.Unmarshal([]byte(message), &resp); err != nil {
		return
	}
	if strings.TrimSpace(resp.RequestID) == "" {
		return
	}

	s.mu.Lock()
	p, ok := s.pending[resp.RequestID]
	if ok {
		delete(s.pending, resp.RequestID)
	}
	s.mu.Unlock()
	if !ok {
		return
	}
	if p.nodeID != nodeID {
		select {
		case p.ch <- CommandResult{Type: resp.Type, Success: false, Message: "节点响应与请求不匹配"}:
		default:
		}
		close(p.ch)
		return
	}

	result := CommandResult{
		Type:    resp.Type,
		Success: resp.Success,
		Message: resp.Message,
	}
	if len(resp.Data) > 0 {
		var data map[string]interface{}
		if err := json.Unmarshal(resp.Data, &data); err == nil {
			result.Data = data
		}
	}

	select {
	case p.ch <- result:
	default:
	}
	close(p.ch)
}

func (s *Server) failPendingForNode(nodeID int64, message string) {
	if s == nil {
		return
	}

	type pair struct {
		id string
		pr pendingRequest
	}
	items := make([]pair, 0)

	s.mu.Lock()
	for id, pr := range s.pending {
		if pr.nodeID != nodeID {
			continue
		}
		items = append(items, pair{id: id, pr: pr})
		delete(s.pending, id)
	}
	s.mu.Unlock()

	for _, item := range items {
		select {
		case item.pr.ch <- CommandResult{Success: false, Message: message}:
		default:
		}
		close(item.pr.ch)
	}
}

func (s *Server) broadcastStatus(nodeID int64, status int) {
	payload := map[string]interface{}{
		"id":   strconv.FormatInt(nodeID, 10),
		"type": "status",
		"data": status,
	}
	raw, _ := json.Marshal(payload)
	s.broadcastToRealtime(string(raw))
}

func (s *Server) broadcastTyped(nodeID int64, msgType string, data string) {
	payload := broadcastMessage{ID: nodeID, Type: msgType, Data: data}
	raw, _ := json.Marshal(payload)
	s.broadcastToRealtime(string(raw))
}

// broadcastToRealtime 把实时消息投递到各会话的发送队列（D2-04/D6-01）。
// 旧实现在节点读循环内同步串行直写全部会话：一个慢消费者每次广播
// 吃满 5s 写截止，队头阻塞该节点的全部后续消息。现改为非阻塞投递：
// 队列满即丢弃该会话的本条消息（实时值为最新值语义），实际写由
// runRealtimeWriter 完成，写失败在那里关连接逐出。
func (s *Server) broadcastToRealtime(message string) {
	payload := []byte(message)
	s.mu.RLock()
	queues := make([]chan []byte, 0, len(s.admins)+len(s.monitors))
	for c := range s.admins {
		if c != nil && c.queue != nil {
			queues = append(queues, c.queue)
		}
	}
	for c := range s.monitors {
		if c != nil && c.queue != nil {
			queues = append(queues, c.queue)
		}
	}
	s.mu.RUnlock()

	for _, q := range queues {
		select {
		case q <- payload:
		default:
			// 慢消费者：丢本条，不阻塞广播方与其他会话。
		}
	}
}

func decryptIfNeeded(payload []byte, crypto *security.AESCrypto, secret string) string {
	text := string(payload)
	var wrap encryptedMessage
	if err := json.Unmarshal(payload, &wrap); err != nil || !wrap.Encrypted || strings.TrimSpace(wrap.Data) == "" {
		return text
	}

	// 优先使用缓存的 crypto 实例
	c := crypto
	if c == nil && strings.TrimSpace(secret) != "" {
		c, _ = security.NewAESCrypto(secret)
	}
	if c == nil {
		return text
	}
	plain, err := c.Decrypt(wrap.Data)
	if err != nil {
		return text
	}
	return string(plain)
}

func parseIntDefault(v string, fallback int) int {
	x, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return x
}

func (s *Server) validateAdminSession(userID int64, claims auth.Claims) bool {
	if s == nil {
		return false
	}
	// R5-B2 A-005: 受限 token（M-05，默认口令登录签发）不得建立 WS 会话，
	// 与 HTTP 侧中间件（middleware/auth.go:78）一致。
	if claims.PwdChangeRequired {
		return false
	}
	if claims.Exp <= time.Now().Unix() {
		return false
	}
	if s.getUserAuthState == nil {
		return true
	}
	state, err := s.getUserAuthState(userID)
	if err != nil || state == nil || state.Status != 1 || state.RoleID != claims.RoleID || claims.IatMs <= state.PasswordChangedAt {
		return false
	}
	return true
}

func (s *Server) validateMonitorSession(userID int64, claims auth.Claims) bool {
	if s == nil {
		return false
	}
	// R5-B2 A-005: 同上，受限 token 不得建立 WS 会话。
	if claims.PwdChangeRequired {
		return false
	}
	if claims.Exp <= time.Now().Unix() {
		return false
	}
	if s.getUserAuthState != nil {
		state, err := s.getUserAuthState(userID)
		if err != nil || state == nil || state.Status != 1 || state.RoleID != claims.RoleID || claims.IatMs <= state.PasswordChangedAt {
			return false
		}
	}
	if s.repo == nil {
		return false
	}
	allowed, err := s.repo.HasMonitorPermission(userID)
	if err != nil || !allowed {
		return false
	}
	return true
}

func writeWSMessage(cw *connWrap, messageType int, payload []byte) error {
	if cw == nil || cw.conn == nil {
		return errors.New("websocket connection not initialized")
	}
	cw.mu.Lock()
	defer cw.mu.Unlock()

	_ = cw.conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
	err := cw.conn.WriteMessage(messageType, payload)
	_ = cw.conn.SetWriteDeadline(time.Time{})
	return err
}

func startKeepalive(cw *connWrap, done <-chan struct{}, validate func() bool) {
	if cw == nil || cw.conn == nil {
		return
	}
	ticker := time.NewTicker(wsPingPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if validate != nil && !validate() {
				_ = cw.conn.Close()
				return
			}
			err := writeWSMessage(cw, websocket.PingMessage, nil)
			if err != nil {
				_ = cw.conn.Close()
				return
			}
		}
	}
}
