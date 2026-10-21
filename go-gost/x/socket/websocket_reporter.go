package socket

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"runtime/debug"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-gost/x/config"
	"github.com/go-gost/x/internal/util/crypto"
	"github.com/go-gost/x/service"
	"github.com/gorilla/websocket"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/load"
	"github.com/shirou/gopsutil/v3/mem"
	psnet "github.com/shirou/gopsutil/v3/net"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// SystemInfo 系统信息结构体
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

// NetworkStats 网络统计信息
type NetworkStats struct {
	BytesReceived    uint64 `json:"bytes_received"`
	BytesTransmitted uint64 `json:"bytes_transmitted"`
	BytesRecvDelta   uint64 `json:"bytes_recv_delta"`
	BytesSentDelta   uint64 `json:"bytes_sent_delta"`
}

// CPUInfo CPU信息
type CPUInfo struct {
	Usage float64 `json:"usage"`
}

// MemoryInfo 内存信息
type MemoryInfo struct {
	Usage float64 `json:"usage"`
}

// DiskInfo 磁盘信息
type DiskInfo struct {
	Usage float64 `json:"usage"`
}

// LoadInfo 负载信息
type LoadInfo struct {
	Load1  float64 `json:"load1"`
	Load5  float64 `json:"load5"`
	Load15 float64 `json:"load15"`
}

// ConnectionInfo 连接信息
type ConnectionInfo struct {
	TCPConns int64 `json:"tcp_conns"`
	UDPConns int64 `json:"udp_conns"`
}

// CommandMessage 命令消息结构体
type CommandMessage struct {
	Type      string      `json:"type"`
	Data      interface{} `json:"data"`
	RequestId string      `json:"requestId,omitempty"`
}

// CommandResponse 命令响应结构体
type CommandResponse struct {
	Type      string      `json:"type"`
	Success   bool        `json:"success"`
	Message   string      `json:"message"`
	Data      interface{} `json:"data,omitempty"`
	RequestId string      `json:"requestId,omitempty"`
}

// TcpPingRequest TCP ping请求结构体
type TcpPingRequest struct {
	IP        string `json:"ip"`
	Port      int    `json:"port"`
	Count     int    `json:"count"`
	Timeout   int    `json:"timeout"` // 超时时间(毫秒)
	RequestId string `json:"requestId,omitempty"`
}

// TcpPingResponse TCP ping响应结构体
type TcpPingResponse struct {
	IP           string  `json:"ip"`
	Port         int     `json:"port"`
	Success      bool    `json:"success"`
	AverageTime  float64 `json:"averageTime"` // 平均连接时间(ms)
	PacketLoss   float64 `json:"packetLoss"`  // 连接失败率(%)
	ErrorMessage string  `json:"errorMessage,omitempty"`
	RequestId    string  `json:"requestId,omitempty"`
}

// ServiceMonitorCheckRequest service monitor check request.
type ServiceMonitorCheckRequest struct {
	MonitorID  int64  `json:"monitorId"`
	Type       string `json:"type"` // tcp|icmp
	Target     string `json:"target"`
	TimeoutSec int    `json:"timeoutSec"`
}

// ServiceMonitorCheckResult node-executed check output.
// CommandResponse.Success indicates command execution status.
// Actual check success is represented by this struct.
type ServiceMonitorCheckResult struct {
	MonitorID    int64   `json:"monitorId"`
	Success      bool    `json:"success"`
	LatencyMs    float64 `json:"latencyMs"`
	StatusCode   int     `json:"statusCode,omitempty"`
	ErrorMessage string  `json:"errorMessage,omitempty"`
}

const (
	reporterReadWait            = 60 * time.Second
	reporterWriteWait           = 5 * time.Second
	wsPingInterval              = 20 * time.Second // 独立 WebSocket ping 间隔
	initialBackoff              = 2 * time.Second  // 重连初始退避
	maxBackoff                  = 2 * time.Minute  // 重连最大退避
	defaultMetricReportInterval = 5 * time.Second
	maxConcurrentTCPPings       = 8
	maxConcurrentReadCommands   = 16
	maxQueuedMutationCommands   = 256
)

type WebSocketReporter struct {
	url               string
	addr              string // 保存服务器地址
	secret            string // 保存密钥
	version           string // 保存版本号
	http              int
	tls               int
	socks             int
	preferredWSScheme string
	conn              *websocket.Conn
	curBackoff        time.Duration // 当前重连退避间隔
	pingInterval      time.Duration
	configInterval    time.Duration
	ctx               context.Context
	cancel            context.CancelFunc
	connected         bool
	connecting        bool              // 正在连接状态
	connMutex         sync.Mutex        // 连接状态锁
	aesCrypto         *crypto.AESCrypto // AES加密器
	tcpPingSem        chan struct{}     // 限制诊断探测并发，避免离线目标耗尽连接
	readCommandSem    chan struct{}     // 限制只读命令并发，避免诊断请求耗尽资源
	mutationQueue     chan CommandMessage
	workers           sync.WaitGroup
	// plaintextCommandWarned ensures the D1-02 plaintext-command security
	// warning is printed once per process, not per command.
	plaintextCommandWarned atomic.Bool
}

var wsDial = func(dialer *websocket.Dialer, rawURL string) (*websocket.Conn, *http.Response, error) {
	return dialer.Dial(rawURL, nil)
}

// NewWebSocketReporter 创建一个新的WebSocket报告器
func NewWebSocketReporter(serverURL string, secret string) *WebSocketReporter {
	ctx, cancel := context.WithCancel(context.Background())

	// 创建 AES 加密器
	aesCrypto, err := crypto.NewAESCrypto(secret)
	if err != nil {
		fmt.Printf("❌ 创建 AES 加密器失败: %v\n", err)
		aesCrypto = nil
	} else {
		fmt.Printf("🔐 AES 加密器创建成功\n")
	}

	return &WebSocketReporter{
		url:            serverURL,
		curBackoff:     initialBackoff,              // 当前退避间隔
		pingInterval:   defaultMetricReportInterval, // 指标上报间隔
		configInterval: 10 * time.Minute,            // 配置上报间隔
		ctx:            ctx,
		cancel:         cancel,
		connected:      false,
		connecting:     false,
		aesCrypto:      aesCrypto,
		tcpPingSem:     make(chan struct{}, maxConcurrentTCPPings),
		readCommandSem: make(chan struct{}, maxConcurrentReadCommands),
		mutationQueue:  make(chan CommandMessage, maxQueuedMutationCommands),
	}
}

func (w *WebSocketReporter) tryAcquireTCPPingSlot() bool {
	if w == nil || w.tcpPingSem == nil {
		return false
	}
	select {
	case w.tcpPingSem <- struct{}{}:
		return true
	default:
		return false
	}
}

func (w *WebSocketReporter) releaseTCPPingSlot() {
	if w == nil || w.tcpPingSem == nil {
		return
	}
	select {
	case <-w.tcpPingSem:
	default:
	}
}

// Start 启动WebSocket报告器
func (w *WebSocketReporter) Start() {
	w.workers.Add(2)
	go func() {
		defer w.workers.Done()
		w.runMutationCommands()
	}()
	go func() {
		defer w.workers.Done()
		w.run()
	}()
}

// Stop 停止WebSocket报告器
func (w *WebSocketReporter) Stop() {
	w.cancel()
	w.connMutex.Lock()
	if w.conn != nil {
		w.conn.Close()
	}
	w.connMutex.Unlock()
	w.workers.Wait()
}

// backoffWithJitter 返回带随机抖动的退避时间（±25%）
func backoffWithJitter(base time.Duration) time.Duration {
	jitter := time.Duration(float64(base) * (0.75 + rand.Float64()*0.5))
	return jitter
}

// run 主运行循环
func (w *WebSocketReporter) run() {
	for {
		select {
		case <-w.ctx.Done():
			return
		default:
			// 检查连接状态，避免重复连接
			w.connMutex.Lock()
			needConnect := !w.connected && !w.connecting
			w.connMutex.Unlock()

			if needConnect {
				if err := w.connect(); err != nil {
					wait := backoffWithJitter(w.curBackoff)
					fmt.Printf("❌ WebSocket连接失败: %v，%v后重试\n", err, wait)
					// 指数退避：翻倍当前退避间隔，上限 maxBackoff
					w.curBackoff *= 2
					if w.curBackoff > maxBackoff {
						w.curBackoff = maxBackoff
					}
					select {
					case <-time.After(wait):
						continue
					case <-w.ctx.Done():
						return
					}
				}
				// 连接成功：重置退避
				w.curBackoff = initialBackoff
			}

			// 连接成功，开始发送消息
			if w.connected {
				w.handleConnection()
			} else {
				wait := backoffWithJitter(w.curBackoff)
				// 如果连接失败，等待重试
				select {
				case <-time.After(wait):
					continue
				case <-w.ctx.Done():
					return
				}
			}
		}
	}
}

// connect 建立WebSocket连接
func (w *WebSocketReporter) connect() error {
	w.connMutex.Lock()
	defer w.connMutex.Unlock()

	// 如果已经在连接中或已连接，直接返回
	if w.connecting || w.connected {
		return nil
	}

	// 设置连接中状态
	w.connecting = true
	defer func() {
		w.connecting = false
	}()

	// 重新读取 config.json 获取最新的协议配置
	type LocalConfig struct {
		Addr   string `json:"addr"`
		Secret string `json:"secret"`
		Http   int    `json:"http"`
		Tls    int    `json:"tls"`
		Socks  int    `json:"socks"`
	}

	cfg := LocalConfig{Http: w.http, Tls: w.tls, Socks: w.socks}
	if b, err := os.ReadFile("config.json"); err == nil {
		_ = json.Unmarshal(b, &cfg)
	}

	candidates := buildWebSocketCandidates(w.addr, w.secret, w.version, cfg.Http, cfg.Tls, cfg.Socks, w.preferredWSScheme)

	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = 10 * time.Second

	conn, usedURL, err := dialWebSocketWithFallback(&dialer, candidates)
	if err != nil {
		return err
	}

	// 如果在连接过程中已经有连接了，关闭新连接
	if w.conn != nil && w.connected {
		conn.Close()
		return nil
	}

	w.conn = conn
	w.connected = true
	if scheme := detectWebSocketScheme(usedURL); scheme != "" {
		w.preferredWSScheme = scheme
	}
	_ = conn.SetReadDeadline(time.Now().Add(reporterReadWait))
	conn.SetPingHandler(func(appData string) error {
		_ = conn.SetReadDeadline(time.Now().Add(reporterReadWait))
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(reporterWriteWait))
	})
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(reporterReadWait))
	})

	// 设置关闭处理器来检测连接状态
	w.conn.SetCloseHandler(func(code int, text string) error {
		w.connMutex.Lock()
		w.connected = false
		w.connMutex.Unlock()
		return nil
	})

	fmt.Printf("✅ WebSocket连接建立成功 (%s, http=%d, tls=%d, socks=%d)\n", sanitizeWebSocketURL(usedURL), cfg.Http, cfg.Tls, cfg.Socks)
	return nil
}

func buildWebSocketCandidates(addr string, secret string, version string, http int, tls int, socks int, preferredScheme string) []string {
	normalizedAddr, explicitScheme := normalizeReporterAddress(addr)
	if normalizedAddr == "" {
		normalizedAddr = strings.TrimSpace(addr)
	}

	query := "/system-info?type=1&secret=" + url.QueryEscape(secret) + "&version=" + url.QueryEscape(version) +
		"&http=" + strconv.Itoa(http) + "&tls=" + strconv.Itoa(tls) + "&socks=" + strconv.Itoa(socks)

	// No silent downgrade (D1-02, behavior change): exactly one scheme is
	// ever attempted. Previously a wss failure silently fell back to
	// plaintext ws, letting anyone who can block/break TLS (or the panel's
	// certificate) force the node secret and the whole command channel
	// onto a plaintext, injectable connection. Scheme selection order:
	// explicit scheme in the configured address > scheme learned from the
	// last successful connection > secure default (wss). Operators whose
	// panel is intentionally plain ws must say so explicitly via a ws://
	// (or http://) prefix in config.json's addr.
	scheme := mapToWebSocketScheme(explicitScheme)
	if scheme == "" {
		scheme = preferredScheme
	}
	if scheme != "ws" && scheme != "wss" {
		scheme = "wss"
	}

	return []string{
		scheme + "://" + normalizedAddr + query,
	}
}

func normalizeReporterAddress(addr string) (string, string) {
	raw := strings.TrimSpace(addr)
	if raw == "" {
		return "", ""
	}

	scheme := ""
	if idx := strings.Index(raw, "://"); idx > 0 {
		scheme = strings.ToLower(strings.TrimSpace(raw[:idx]))
		if parsed, err := url.Parse(raw); err == nil {
			if host := strings.TrimSpace(parsed.Host); host != "" {
				return host, scheme
			}
		}
		raw = raw[idx+3:]
	}

	if idx := strings.IndexAny(raw, "/?#"); idx >= 0 {
		raw = raw[:idx]
	}
	return strings.TrimSpace(raw), scheme
}

func mapToWebSocketScheme(scheme string) string {
	switch strings.ToLower(strings.TrimSpace(scheme)) {
	case "wss", "https":
		return "wss"
	case "ws", "http":
		return "ws"
	default:
		return ""
	}
}

func detectWebSocketScheme(rawURL string) string {
	if strings.HasPrefix(rawURL, "wss://") {
		return "wss"
	}
	if strings.HasPrefix(rawURL, "ws://") {
		return "ws"
	}
	return ""
}

func dialWebSocketWithFallback(dialer *websocket.Dialer, candidates []string) (*websocket.Conn, string, error) {
	if len(candidates) == 0 {
		return nil, "", fmt.Errorf("WebSocket候选地址为空")
	}

	var errs []string
	for _, targetURL := range candidates {
		conn, resp, err := wsDial(dialer, targetURL)
		if err == nil {
			return conn, targetURL, nil
		}
		errMsg := formatWebSocketDialError(err, resp)
		errs = append(errs, fmt.Sprintf("%s => %s", sanitizeWebSocketURL(targetURL), errMsg))
	}

	err := fmt.Errorf("连接WebSocket失败: %s", strings.Join(errs, " | "))
	// Fail loudly on the secure path (D1-02): never retry over plaintext,
	// and tell the operator exactly what the alternatives are.
	if len(candidates) > 0 && detectWebSocketScheme(candidates[0]) == "wss" {
		err = fmt.Errorf("%w（已禁止从 wss 自动降级到 ws 明文连接；请检查面板 TLS/证书，或在 config.json 的 addr 中显式使用 ws:// 前缀以确认明文部署）", err)
	}
	return nil, "", err
}

var (
	// JSON string values of credential-bearing keys.
	sensitiveJSONValueRe = regexp.MustCompile(`(?i)("(?:[^"]*(?:secret|password|passwd|token|auth|private_?key)[^"]*)"\s*:\s*")[^"]*(")`)
	// URL query parameters carrying credentials (defense in depth for URLs
	// embedded inside command payloads).
	sensitiveQueryValueRe = regexp.MustCompile(`(?i)((?:secret|token|password)=)[^&"\s]+`)
)

// redactSensitiveForLog masks credential values in a log rendering while
// keeping the structure (types, names, ids) identifiable. Same masking
// policy as the panel side (FIX-A): values of secret/token/password-class
// keys become ***.
func redactSensitiveForLog(s string) string {
	s = sensitiveJSONValueRe.ReplaceAllString(s, "${1}***${2}")
	s = sensitiveQueryValueRe.ReplaceAllString(s, "${1}***")
	return s
}

func sanitizeWebSocketURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}

	q := u.Query()
	if q.Get("secret") != "" {
		q.Set("secret", "***")
		u.RawQuery = q.Encode()
	}
	return u.String()
}

func formatWebSocketDialError(err error, resp *http.Response) string {
	if err == nil {
		return ""
	}
	if resp == nil {
		return err.Error()
	}

	msg := fmt.Sprintf("%s (HTTP %s)", err, resp.Status)
	if resp.Body == nil {
		return msg
	}

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 256))
	if readErr != nil {
		return msg
	}
	bodyText := strings.TrimSpace(string(body))
	if bodyText == "" {
		return msg
	}
	return fmt.Sprintf("%s, body=%q", msg, bodyText)
}

// handleConnection 处理WebSocket连接
func (w *WebSocketReporter) handleConnection() {
	defer func() {
		w.connMutex.Lock()
		if w.conn != nil {
			w.conn.Close()
			w.conn = nil
		}
		w.connected = false
		w.connMutex.Unlock()
		fmt.Printf("🔌 WebSocket连接已关闭\n")
	}()

	// 启动消息接收goroutine
	w.workers.Add(1)
	go func() {
		defer w.workers.Done()
		w.receiveMessages()
	}()

	// 指标上报 ticker
	metricTicker := time.NewTicker(w.pingInterval)
	defer metricTicker.Stop()

	// 独立 WebSocket keepalive ping ticker
	pingTicker := time.NewTicker(wsPingInterval)
	defer pingTicker.Stop()

	for {
		select {
		case <-w.ctx.Done():
			return

		case <-pingTicker.C:
			// 发送 WebSocket ping 保活，独立于指标上报
			w.connMutex.Lock()
			conn := w.conn
			isConnected := w.connected
			w.connMutex.Unlock()
			if !isConnected || conn == nil {
				return
			}
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(reporterWriteWait)); err != nil {
				fmt.Printf("❌ 发送WebSocket ping失败: %v，准备重连\n", err)
				return
			}

		case <-metricTicker.C:
			// 检查连接状态
			w.connMutex.Lock()
			isConnected := w.connected
			w.connMutex.Unlock()

			if !isConnected {
				return
			}

			// 获取系统信息并发送
			sysInfo := w.collectSystemInfo()
			if err := w.sendSystemInfo(sysInfo); err != nil {
				fmt.Printf("❌ 发送系统信息失败: %v，准备重连\n", err)
				return
			}
		}
	}
}

var lastNetBytesReceived uint64
var lastNetBytesTransmitted uint64
var lastNetTime int64

var connInfoCached ConnectionInfo
var connInfoCachedAt int64
var connInfoCachedMu sync.Mutex

// collectSystemInfo 收集系统信息
func (w *WebSocketReporter) collectSystemInfo() SystemInfo {
	networkStats := getNetworkStats()
	cpuInfo := getCPUInfo()
	memoryInfo := getMemoryInfo()
	diskInfo := getDiskInfo()
	loadInfo := getLoadInfo()
	connInfo := getConnectionInfo()

	now := time.Now().UnixMilli()
	var netInSpeed, netOutSpeed int64
	if lastNetTime > 0 {
		deltaMs := now - lastNetTime
		if deltaMs > 0 {
			netInSpeed = int64(float64(networkStats.BytesRecvDelta) * 1000 / float64(deltaMs))
			netOutSpeed = int64(float64(networkStats.BytesSentDelta) * 1000 / float64(deltaMs))
		}
	}
	lastNetBytesReceived = networkStats.BytesReceived
	lastNetBytesTransmitted = networkStats.BytesTransmitted
	lastNetTime = now

	return SystemInfo{
		Uptime:           getUptime(),
		BytesReceived:    networkStats.BytesReceived,
		BytesTransmitted: networkStats.BytesTransmitted,
		CPUUsage:         cpuInfo.Usage,
		MemoryUsage:      memoryInfo.Usage,
		DiskUsage:        diskInfo.Usage,
		Load1:            loadInfo.Load1,
		Load5:            loadInfo.Load5,
		Load15:           loadInfo.Load15,
		TCPConns:         connInfo.TCPConns,
		UDPConns:         connInfo.UDPConns,
		NetInSpeed:       netInSpeed,
		NetOutSpeed:      netOutSpeed,
	}
}

// encryptPayload 加密 JSON 数据，返回加密后的消息字节。
// F-089: fail-closed——加密器缺失或加密失败时返回错误，调用方不得
// 回退明文发送（旧回退与全仓 fail-closed 基调不一致且掩盖配置错误）。
func (w *WebSocketReporter) encryptPayload(jsonData []byte) ([]byte, error) {
	if w.aesCrypto == nil {
		return nil, fmt.Errorf("加密器未初始化（节点 secret 为空或无效），拒绝明文发送")
	}

	encryptedData, err := w.aesCrypto.Encrypt(jsonData)
	if err != nil {
		return nil, fmt.Errorf("加密失败: %v", err)
	}

	encryptedMessage := map[string]interface{}{
		"encrypted": true,
		"data":      encryptedData,
		"timestamp": time.Now().Unix(),
	}
	messageData, err := json.Marshal(encryptedMessage)
	if err != nil {
		return nil, fmt.Errorf("序列化加密消息失败: %v", err)
	}
	return messageData, nil
}

// metricEnvelope wraps SystemInfo with a type field for fast identification on the panel side.
type metricEnvelope struct {
	Type string     `json:"type"`
	Data SystemInfo `json:"data"`
}

// sendSystemInfo 发送系统信息
func (w *WebSocketReporter) sendSystemInfo(sysInfo SystemInfo) error {
	w.connMutex.Lock()
	defer w.connMutex.Unlock()

	if w.conn == nil || !w.connected {
		return fmt.Errorf("连接未建立")
	}

	// 使用 type:"metric" 信封包装，Panel 可通过 type 字段直接识别指标消息
	envelope := metricEnvelope{Type: "metric", Data: sysInfo}
	jsonData, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("序列化系统信息失败: %v", err)
	}

	messageData, err := w.encryptPayload(jsonData)
	if err != nil {
		return err
	}

	w.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))

	if err := w.conn.WriteMessage(websocket.TextMessage, messageData); err != nil {
		w.connected = false
		return fmt.Errorf("写入消息失败: %v", err)
	}

	return nil
}

// receiveMessages 接收服务端发送的消息
func (w *WebSocketReporter) receiveMessages() {
	// 获取连接引用一次即可，连接生命周期由 handleConnection 管理
	w.connMutex.Lock()
	conn := w.conn
	w.connMutex.Unlock()
	if conn == nil {
		return
	}

	for {
		select {
		case <-w.ctx.Done():
			return
		default:
			messageType, message, err := conn.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					fmt.Printf("❌ WebSocket读取消息错误: %v\n", err)
				}
				w.connMutex.Lock()
				w.connected = false
				w.connMutex.Unlock()
				return
			}

			// 处理接收到的消息
			w.handleReceivedMessage(messageType, message)
		}
	}
}

// maxEncryptedCommandSkew bounds the accepted age of an encrypted command
// envelope (D1-02). Without a window, a captured envelope could be replayed
// verbatim on any later connection.
const maxEncryptedCommandSkew = 5 * time.Minute

// maxDecompressedCommandSize caps gzip command decompression (D1-03),
// aligned with the 10MB large-message threshold in sendResponse. A small
// compressed blob must not be able to inflate unboundedly into memory.
const maxDecompressedCommandSize = 10 << 20

// validateEncryptedTimestamp checks an encrypted envelope's timestamp
// against the acceptance window. The panel sends Unix milliseconds; plain
// Unix seconds are also accepted for robustness.
func validateEncryptedTimestamp(timestamp int64, now time.Time) error {
	if timestamp <= 0 {
		return fmt.Errorf("加密命令缺少有效时间戳，拒绝执行")
	}
	ts := time.UnixMilli(timestamp)
	if timestamp < 1e12 {
		// Seconds-scale value (panel currently sends milliseconds; tolerate
		// seconds so a unit fix on either side does not break the channel).
		ts = time.Unix(timestamp, 0)
	}
	if now.Sub(ts) > maxEncryptedCommandSkew || ts.Sub(now) > maxEncryptedCommandSkew {
		return fmt.Errorf("加密命令时间戳超出允许窗口 (±%v)，疑似重放或时钟偏差过大，拒绝执行", maxEncryptedCommandSkew)
	}
	return nil
}

// decompressCommandPayload inflates a gzip command payload with a hard size
// cap (D1-03).
func decompressCommandPayload(data []byte) ([]byte, error) {
	gzipReader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer gzipReader.Close()

	out, err := io.ReadAll(io.LimitReader(gzipReader, maxDecompressedCommandSize+1))
	if err != nil {
		return nil, err
	}
	if len(out) > maxDecompressedCommandSize {
		return nil, fmt.Errorf("解压后数据超过大小上限 (%d bytes)", maxDecompressedCommandSize)
	}
	return out, nil
}

// warnPlaintextCommandOnce loudly notes, once per process, that commands
// are being accepted over an unencrypted channel (D1-02). Plaintext
// commands remain accepted for intentionally plain deployments (explicit
// ws:// addr), but never silently.
func (w *WebSocketReporter) warnPlaintextCommandOnce() {
	if w.plaintextCommandWarned.CompareAndSwap(false, true) {
		fmt.Println("⚠️ 安全警告: 正在通过未加密的明文 WebSocket 通道接收面板命令。明文通道可被链路中间人窃听节点 secret 并注入任意命令（含升级指令）；如非有意部署，请为面板配置 TLS 并使用 wss 地址。")
	}
}

// handleReceivedMessage 处理接收到的消息
func (w *WebSocketReporter) handleReceivedMessage(messageType int, message []byte) {
	switch messageType {
	case websocket.TextMessage:
		// 先检查是否是加密消息
		var encryptedWrapper struct {
			Encrypted bool   `json:"encrypted"`
			Data      string `json:"data"`
			Timestamp int64  `json:"timestamp"`
		}

		// 尝试解析为加密消息格式
		wasEncrypted := false
		if err := json.Unmarshal(message, &encryptedWrapper); err == nil && encryptedWrapper.Encrypted {
			// D1-02: 校验时间戳窗口，拒绝重放的加密信封。
			if err := validateEncryptedTimestamp(encryptedWrapper.Timestamp, time.Now()); err != nil {
				fmt.Printf("❌ 拒绝加密命令: %v\n", err)
				w.sendErrorResponse("StaleTimestamp", err.Error())
				return
			}
			if w.aesCrypto != nil {
				// 解密数据
				decryptedData, err := w.aesCrypto.Decrypt(encryptedWrapper.Data)
				if err != nil {
					fmt.Printf("❌ 解密失败: %v\n", err)
					w.sendErrorResponse("DecryptError", fmt.Sprintf("解密失败: %v", err))
					return
				}
				message = decryptedData
				wasEncrypted = true
			} else {
				fmt.Printf("❌ 收到加密消息但没有加密器\n")
				w.sendErrorResponse("NoDecryptor", "没有可用的解密器")
				return
			}
		}
		if !wasEncrypted {
			w.warnPlaintextCommandOnce()
		}
		// 先尝试解析是否是压缩消息
		var compressedMsg struct {
			Type       string          `json:"type"`
			Compressed bool            `json:"compressed"`
			Data       json.RawMessage `json:"data"`
			RequestId  string          `json:"requestId,omitempty"`
		}

		if err := json.Unmarshal(message, &compressedMsg); err == nil && compressedMsg.Compressed {
			// 处理压缩消息
			fmt.Printf("📥 收到压缩消息，正在解压...\n")

			// 解压数据（带大小上限，防 gzip bomb，D1-03）
			decompressedData, err := decompressCommandPayload(compressedMsg.Data)
			if err != nil {
				fmt.Printf("❌ 解压数据失败: %v\n", err)
				w.sendErrorResponse("DecompressError", fmt.Sprintf("解压失败: %v", err))
				return
			}

			// 使用解压后的数据继续处理
			message = decompressedData

			// 构建解压后的命令消息
			var cmdMsg CommandMessage
			cmdMsg.Type = compressedMsg.Type
			cmdMsg.RequestId = compressedMsg.RequestId
			if err := json.Unmarshal(message, &cmdMsg.Data); err != nil {
				fmt.Printf("❌ 解析解压后的命令数据失败: %v\n", err)
				w.sendErrorResponse("ParseError", fmt.Sprintf("解析命令失败: %v", err))
				return
			}

			if cmdMsg.Type != "call" {
				w.dispatchCommand(cmdMsg)
			}
		} else {
			// 处理普通消息
			var cmdMsg CommandMessage
			if err := json.Unmarshal(message, &cmdMsg); err != nil {
				fmt.Printf("❌ 解析命令消息失败: %v\n", err)
				w.sendErrorResponse("ParseError", fmt.Sprintf("解析命令失败: %v", err))
				return
			}
			if cmdMsg.Type != "call" {
				w.dispatchCommand(cmdMsg)
			}
		}

	default:
		fmt.Printf("📨 收到未知类型消息: %d\n", messageType)
	}
}

// dispatchCommand keeps all runtime mutations ordered while allowing bounded
// concurrency for read-only diagnostics. Mutations share process-wide
// registries and configuration, so running them concurrently can corrupt the
// persisted config or interleave service lifecycle operations.
func (w *WebSocketReporter) dispatchCommand(cmd CommandMessage) {
	if isMutationCommand(cmd.Type) {
		select {
		case w.mutationQueue <- cmd:
		case <-w.ctx.Done():
			w.sendCommandFailure(cmd, "Agent is shutting down")
		default:
			w.sendCommandFailure(cmd, "运行时配置命令队列已满，请稍后重试")
		}
		return
	}

	select {
	case w.readCommandSem <- struct{}{}:
		go func() {
			defer func() { <-w.readCommandSem }()
			w.routeCommandSafely(cmd)
		}()
	case <-w.ctx.Done():
		w.sendCommandFailure(cmd, "Agent is shutting down")
	default:
		w.sendCommandFailure(cmd, "只读命令并发过多，请稍后重试")
	}
}

func (w *WebSocketReporter) runMutationCommands() {
	for {
		select {
		case <-w.ctx.Done():
			return
		case cmd := <-w.mutationQueue:
			w.routeCommandSafely(cmd)
		}
	}
}

func (w *WebSocketReporter) routeCommandSafely(cmd CommandMessage) {
	defer func() {
		if recovered := recover(); recovered != nil {
			fmt.Printf("❌ 命令处理 panic: type=%s panic=%v\n%s", cmd.Type, recovered, debug.Stack())
			w.sendCommandFailure(cmd, fmt.Sprintf("命令处理异常: %v", recovered))
		}
	}()
	w.routeCommand(cmd)
}

func (w *WebSocketReporter) sendCommandFailure(cmd CommandMessage, message string) {
	w.sendResponse(CommandResponse{
		Type:      commandResponseType(cmd.Type),
		Success:   false,
		Message:   message,
		RequestId: cmd.RequestId,
	})
}

func commandResponseType(commandType string) string {
	commandType = strings.TrimSpace(commandType)
	if commandType == "" {
		return "UnknownCommandResponse"
	}
	return commandType + "Response"
}

func isMutationCommand(commandType string) bool {
	switch strings.ToLower(strings.TrimSpace(commandType)) {
	case "addservice", "updateservice", "deleteservice", "pauseservice", "resumeservice",
		"addchains", "updatechains", "deletechains",
		"addlimiters", "updatelimiters", "deletelimiters",
		"addclimiters", "updateclimiters", "deleteclimiters",
		"setprotocol", "upgradeagent", "rollbackagent", "reload":
		return true
	default:
		return false
	}
}

// routeCommand 路由命令到对应的处理函数
func (w *WebSocketReporter) routeCommand(cmd CommandMessage) {
	if isMutationCommand(cmd.Type) {
		unlock := config.LockMutation()
		defer unlock()
		if w.ctx.Err() != nil {
			w.sendCommandFailure(cmd, "Agent is shutting down")
			return
		}
	}
	jsonBytes, errs := json.Marshal(cmd)
	if errs != nil {
		fmt.Println("Error marshaling JSON:", errs)
		return
	}

	// D1-01: never print the raw command body — service configs inside it
	// carry credentials (proxy passwords, SS keys, node secrets) and this
	// output lands in the service journal. Log a redacted rendering.
	fmt.Println("🔔 收到命令: ", redactSensitiveForLog(string(jsonBytes)))
	var err error
	var response CommandResponse

	// 传递 requestId
	response.RequestId = cmd.RequestId

	switch cmd.Type {
	// Service 相关命令
	case "AddService":
		err = w.handleAddService(cmd.Data)
		response.Type = "AddServiceResponse"
	case "UpdateService":
		err = w.handleUpdateService(cmd.Data)
		response.Type = "UpdateServiceResponse"
	case "DeleteService":
		err = w.handleDeleteService(cmd.Data)
		response.Type = "DeleteServiceResponse"
	case "PauseService":
		err = w.handlePauseService(cmd.Data)
		response.Type = "PauseServiceResponse"
	case "ResumeService":
		err = w.handleResumeService(cmd.Data)
		response.Type = "ResumeServiceResponse"

	// Chain 相关命令
	case "AddChains":
		err = w.handleAddChain(cmd.Data)
		response.Type = "AddChainsResponse"
	case "UpdateChains":
		err = w.handleUpdateChain(cmd.Data)
		response.Type = "UpdateChainsResponse"
	case "DeleteChains":
		err = w.handleDeleteChain(cmd.Data)
		response.Type = "DeleteChainsResponse"

	// Limiter 相关命令
	case "AddLimiters":
		err = w.handleAddLimiter(cmd.Data)
		response.Type = "AddLimitersResponse"
	case "UpdateLimiters":
		err = w.handleUpdateLimiter(cmd.Data)
		response.Type = "UpdateLimitersResponse"
	case "DeleteLimiters":
		err = w.handleDeleteLimiter(cmd.Data)
		response.Type = "DeleteLimitersResponse"
	case "AddCLimiters":
		err = w.handleAddCLimiter(cmd.Data)
		response.Type = "AddCLimitersResponse"
	case "UpdateCLimiters":
		err = w.handleUpdateCLimiter(cmd.Data)
		response.Type = "UpdateCLimitersResponse"
	case "DeleteCLimiters":
		err = w.handleDeleteCLimiter(cmd.Data)
		response.Type = "DeleteCLimitersResponse"

	// TCP Ping 诊断命令（只读，不需要保存配置）
	case "TcpPing":
		response.Type = "TcpPingResponse"
		if !w.tryAcquireTCPPingSlot() {
			err = fmt.Errorf("TCP探测任务过多，请稍后重试")
			break
		}
		defer w.releaseTCPPingSlot()
		var tcpPingResult TcpPingResponse
		tcpPingResult, err = w.handleTcpPing(cmd.Data)
		response.Data = tcpPingResult
		// needSaveConfig = false (默认值)

	// 节点调优：读取 / 应用 sysctl 网络参数（移植自 kure29/flyx）
	case "TuneStatus", "TuneApply":
		response.Type = cmd.Type + "Response"
		response.Data, err = handleTuneCommand(cmd.Type, cmd.Data)

	// UDP Ping 诊断命令（只读，不需要保存配置）
	case "UdpPing":
		var udpPingResult TcpPingResponse
		udpPingResult, err = w.handleUdpPing(cmd.Data)
		response.Type = "UdpPingResponse"
		response.Data = udpPingResult

	// 路由追踪（只读，回程线路识别用，移植自 kure29/flyx）
	case "Traceroute":
		var traceResult TracerouteResponse
		traceResult, err = w.handleTraceroute(cmd.Data)
		response.Type = "TracerouteResponse"
		response.Data = traceResult

	// Service monitor check (read-only)
	case "ServiceMonitorCheck":
		var checkResult ServiceMonitorCheckResult
		checkResult, err = w.handleServiceMonitorCheck(cmd.Data)
		response.Type = "ServiceMonitorCheckResponse"
		response.Data = checkResult

	// Protocol blocking switches
	case "SetProtocol":
		err = w.handleSetProtocol(cmd.Data)
		response.Type = "SetProtocolResponse"

	// 升级 Agent 命令（异步执行，不需要保存配置）
	case "UpgradeAgent":
		err = w.handleUpgradeAgent(cmd.Data)
		response.Type = "UpgradeAgentResponse"
		// needSaveConfig = false (默认值)

	// 回退 Agent 到旧版本
	case "RollbackAgent":
		err = w.handleRollbackAgent(cmd.Data)
		response.Type = "RollbackAgentResponse"
		// needSaveConfig = false (默认值)

	// 代理内核（Xray / sing-box / Snell）相关命令，由 kernel.Manager 自行串行化
	case "KernelApply", "KernelControl", "KernelStatus":
		response.Type = cmd.Type + "Response"
		response.Data, err = handleKernelCommand(cmd.Type, cmd.Data)

	default:
		err = fmt.Errorf("未知命令类型: %s", cmd.Type)
		response.Type = "UnknownCommandResponse"
	}

	// 发送响应
	if err != nil {
		response.Success = false
		response.Message = err.Error()
	} else {
		response.Success = true
		response.Message = "OK"
	}

	w.sendResponse(response)
}

// Service 命令处理函数
func (w *WebSocketReporter) handleAddService(data interface{}) error {
	// 将 interface{} 转换为 JSON 再解析为具体类型
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("序列化数据失败: %v", err)
	}

	// 预处理：将字符串格式的 duration 转换为纳秒数
	processedData, err := w.preprocessDurationFields(jsonData)
	if err != nil {
		return fmt.Errorf("预处理duration字段失败: %v", err)
	}

	var services []config.ServiceConfig
	if err := json.Unmarshal(processedData, &services); err != nil {
		return fmt.Errorf("解析服务配置失败: %v", err)
	}

	req := createServicesRequest{Data: services}
	return createServices(req)
}

func (w *WebSocketReporter) handleUpdateService(data interface{}) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("序列化数据失败: %v", err)
	}

	// 预处理：将字符串格式的 duration 转换为纳秒数
	processedData, err := w.preprocessDurationFields(jsonData)
	if err != nil {
		return fmt.Errorf("预处理duration字段失败: %v", err)
	}

	var services []config.ServiceConfig
	if err := json.Unmarshal(processedData, &services); err != nil {
		return fmt.Errorf("解析服务配置失败: %v", err)
	}

	req := updateServicesRequest{Data: services}
	return updateServices(req)
}

func (w *WebSocketReporter) handleDeleteService(data interface{}) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("序列化数据失败: %v", err)
	}

	var req deleteServicesRequest
	if err := json.Unmarshal(jsonData, &req); err != nil {
		return fmt.Errorf("解析删除请求失败: %v", err)
	}

	return deleteServices(req)
}

func (w *WebSocketReporter) handlePauseService(data interface{}) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("序列化数据失败: %v", err)
	}

	var req pauseServicesRequest
	if err := json.Unmarshal(jsonData, &req); err != nil {
		return fmt.Errorf("解析暂停请求失败: %v", err)
	}

	return pauseServices(req)
}

func (w *WebSocketReporter) handleResumeService(data interface{}) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("序列化数据失败: %v", err)
	}

	var req resumeServicesRequest
	if err := json.Unmarshal(jsonData, &req); err != nil {
		return fmt.Errorf("解析恢复请求失败: %v", err)
	}

	return resumeServices(req)
}

// Chain 命令处理函数
func (w *WebSocketReporter) handleAddChain(data interface{}) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("序列化数据失败: %v", err)
	}

	var chainConfig config.ChainConfig
	if err := json.Unmarshal(jsonData, &chainConfig); err != nil {
		return fmt.Errorf("解析链配置失败: %v", err)
	}

	req := createChainRequest{Data: chainConfig}
	return createChain(req)
}

func (w *WebSocketReporter) handleUpdateChain(data interface{}) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("序列化数据失败: %v", err)
	}

	// 对于更新操作，Java端发送的格式可能是: {"chain": "name", "data": {...}}
	var updateReq struct {
		Chain string             `json:"chain"`
		Data  config.ChainConfig `json:"data"`
	}

	// 尝试解析为更新请求格式
	if err := json.Unmarshal(jsonData, &updateReq); err != nil {
		// 如果失败，可能是直接的ChainConfig，从name字段获取chain名称
		var chainConfig config.ChainConfig
		if err := json.Unmarshal(jsonData, &chainConfig); err != nil {
			return fmt.Errorf("解析链配置失败: %v", err)
		}
		updateReq.Chain = chainConfig.Name
		updateReq.Data = chainConfig
	}

	req := updateChainRequest{
		Chain: updateReq.Chain,
		Data:  updateReq.Data,
	}
	return updateChain(req)
}

func (w *WebSocketReporter) handleDeleteChain(data interface{}) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("序列化数据失败: %v", err)
	}

	// 删除操作可能是: {"chain": "name"} 或者直接是链名称字符串
	var deleteReq deleteChainRequest

	// 尝试解析为删除请求格式
	if err := json.Unmarshal(jsonData, &deleteReq); err != nil {
		// 如果失败，可能是字符串格式的名称
		var chainName string
		if err := json.Unmarshal(jsonData, &chainName); err != nil {
			return fmt.Errorf("解析链删除请求失败: %v", err)
		}
		deleteReq.Chain = chainName
	}

	return deleteChain(deleteReq)
}

// Limiter 命令处理函数
func (w *WebSocketReporter) handleAddLimiter(data interface{}) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("序列化数据失败: %v", err)
	}

	var limiterConfig config.LimiterConfig
	if err := json.Unmarshal(jsonData, &limiterConfig); err != nil {
		return fmt.Errorf("解析限流器配置失败: %v", err)
	}

	req := createLimiterRequest{Data: limiterConfig}
	return createLimiter(req)
}

func (w *WebSocketReporter) handleUpdateLimiter(data interface{}) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("序列化数据失败: %v", err)
	}

	// 对于更新操作，Java端发送的格式可能是: {"limiter": "name", "data": {...}}
	var updateReq struct {
		Limiter string               `json:"limiter"`
		Data    config.LimiterConfig `json:"data"`
	}

	// 尝试解析为更新请求格式
	if err := json.Unmarshal(jsonData, &updateReq); err != nil {
		// 如果失败，可能是直接的LimiterConfig，从name字段获取limiter名称
		var limiterConfig config.LimiterConfig
		if err := json.Unmarshal(jsonData, &limiterConfig); err != nil {
			return fmt.Errorf("解析限流器配置失败: %v", err)
		}
		updateReq.Limiter = limiterConfig.Name
		updateReq.Data = limiterConfig
	}

	req := updateLimiterRequest{
		Limiter: updateReq.Limiter,
		Data:    updateReq.Data,
	}
	return updateLimiter(req)
}

func (w *WebSocketReporter) handleDeleteLimiter(data interface{}) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("序列化数据失败: %v", err)
	}

	// 删除操作可能是: {"limiter": "name"} 或者直接是限流器名称字符串
	var deleteReq deleteLimiterRequest

	// 尝试解析为删除请求格式
	if err := json.Unmarshal(jsonData, &deleteReq); err != nil {
		// 如果失败，可能是字符串格式的名称
		var limiterName string
		if err := json.Unmarshal(jsonData, &limiterName); err != nil {
			return fmt.Errorf("解析限流器删除请求失败: %v", err)
		}
		deleteReq.Limiter = limiterName
	}

	return deleteLimiter(deleteReq)
}

func (w *WebSocketReporter) handleAddCLimiter(data interface{}) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("序列化数据失败: %v", err)
	}

	var limiterConfig config.LimiterConfig
	if err := json.Unmarshal(jsonData, &limiterConfig); err != nil {
		return fmt.Errorf("解析限流器配置失败: %v", err)
	}

	req := createLimiterRequest{Data: limiterConfig}
	return createConnLimiter(req)
}

func (w *WebSocketReporter) handleUpdateCLimiter(data interface{}) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("序列化数据失败: %v", err)
	}

	var updateReq struct {
		Limiter string               `json:"limiter"`
		Data    config.LimiterConfig `json:"data"`
	}

	if err := json.Unmarshal(jsonData, &updateReq); err != nil {
		var limiterConfig config.LimiterConfig
		if err := json.Unmarshal(jsonData, &limiterConfig); err != nil {
			return fmt.Errorf("解析更新请求失败: %v", err)
		}
		updateReq.Limiter = limiterConfig.Name
		updateReq.Data = limiterConfig
	}

	req := updateLimiterRequest{
		Limiter: updateReq.Limiter,
		Data:    updateReq.Data,
	}
	return updateConnLimiter(req)
}

func (w *WebSocketReporter) handleDeleteCLimiter(data interface{}) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("序列化数据失败: %v", err)
	}

	var deleteReq deleteLimiterRequest

	if err := json.Unmarshal(jsonData, &deleteReq); err != nil {
		var limiterName string
		if err := json.Unmarshal(jsonData, &limiterName); err != nil {
			return fmt.Errorf("解析删除请求失败: %v", err)
		}
		deleteReq.Limiter = limiterName
	}

	return deleteConnLimiter(deleteReq)
}

// handleSetProtocol 处理设置屏蔽协议的命令
func (w *WebSocketReporter) handleSetProtocol(data interface{}) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("序列化协议设置失败: %v", err)
	}

	// 支持 {"http":0/1, "tls":0/1, "socks":0/1}
	var req struct {
		HTTP  *int `json:"http"`
		TLS   *int `json:"tls"`
		SOCKS *int `json:"socks"`
	}
	if err := json.Unmarshal(jsonData, &req); err != nil {
		return fmt.Errorf("解析协议设置失败: %v", err)
	}

	// 读取当前值作为默认
	httpVal, tlsVal, socksVal := 0, 0, 0

	if req.HTTP != nil {
		if *req.HTTP != 0 && *req.HTTP != 1 {
			return fmt.Errorf("http 取值必须为0或1")
		}
		httpVal = *req.HTTP
	}
	if req.TLS != nil {
		if *req.TLS != 0 && *req.TLS != 1 {
			return fmt.Errorf("tls 取值必须为0或1")
		}
		tlsVal = *req.TLS
	}
	if req.SOCKS != nil {
		if *req.SOCKS != 0 && *req.SOCKS != 1 {
			return fmt.Errorf("socks 取值必须为0或1")
		}
		socksVal = *req.SOCKS
	}

	// 设置至 service，全量传递（未提供的值沿用0）
	service.SetProtocolBlock(httpVal, tlsVal, socksVal)

	// 同步写入本地 config.json
	if err := updateLocalConfigJSON(httpVal, tlsVal, socksVal); err != nil {
		return fmt.Errorf("写入config.json失败: %v", err)
	}
	return nil
}

// sendUpgradeProgress 通过 WS 发送升级进度消息
func (w *WebSocketReporter) sendUpgradeProgress(stage string, percent int, message string) {
	response := CommandResponse{
		Type:    "UpgradeProgress",
		Success: true,
		Message: message,
		Data: map[string]interface{}{
			"stage":   stage,
			"percent": percent,
		},
	}
	w.sendResponse(response)
}

// resolveAgentUpgradeProxy 返回升级下载用的代理前缀（无尾斜杠），无代理时返回 ""。
// 来源优先级：面板下发 > 本地 config.json 的 proxy 字段（安装时落盘）。
// 候选必须能解析为 http/https URL 且 host 非空，否则忽略并打日志（不硬失败，
// 直连兜底让错误暴露在下载阶段而不是配置阶段）。
func resolveAgentUpgradeProxy(panelProxy string) string {
	candidates := []string{panelProxy}
	type proxyConfig struct {
		Proxy string `json:"proxy"`
	}
	var cfg proxyConfig
	if b, err := os.ReadFile("config.json"); err == nil {
		_ = json.Unmarshal(b, &cfg)
	}
	candidates = append(candidates, cfg.Proxy)
	for _, c := range candidates {
		c = strings.TrimSuffix(strings.TrimSpace(c), "/")
		if c == "" {
			continue
		}
		u, err := url.Parse(c)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			fmt.Printf("⚠️ 忽略无效的升级代理配置: %q\n", c)
			continue
		}
		return c
	}
	return ""
}

// isGitHubDownloadHost 判断重定向目标是否为 GitHub 官方下载域名。
// release 文件会先 302 到 release-assets.githubusercontent.com
// （*.githubusercontent.com），这是 GitHub 的资产分发域名，必须放行，
// 否则升级包永远下载不了（S-02 初版白名单遗漏此域名导致面板内升级全灭）。
func isGitHubDownloadHost(host string) bool {
	host = strings.ToLower(host)
	return host == "github.com" || strings.HasSuffix(host, ".github.com") ||
		host == "githubusercontent.com" || strings.HasSuffix(host, ".githubusercontent.com")
}

// --- Agent in-panel upgrade: init-system abstraction & safety chain ---
//
// A11-132-01: the restart/rollback scripts used to hardcode systemd-run /
// systemctl, so panel-initiated upgrades could never work on OpenRC
// (Alpine) nodes even though install.sh installs the agent under OpenRC
// there. A11-132-02: the downloaded binary was swapped in with no
// executability smoke test, backup write failures were swallowed, and if
// the new binary failed to start the node stayed dead — the panel's
// rollback command needs a live agent, so recovery required SSH. The
// helpers below mirror install.sh's established standard: detect the
// service manager the same way, smoke test with -V before swapping,
// abort when the backup cannot be written, and let the detached restart
// script itself roll back when the new binary does not come up.

type serviceManager string

const (
	serviceManagerSystemd serviceManager = "systemd"
	serviceManagerOpenRC  serviceManager = "openrc"
)

// serviceManagerProbe abstracts the environment checks of
// detectServiceManagerWith so they can be unit tested.
type serviceManagerProbe struct {
	lookPath   func(string) (string, error)
	fileExists func(string) bool
	dirExists  func(string) bool
}

func detectServiceManager() (serviceManager, error) {
	return detectServiceManagerWith(serviceManagerProbe{
		lookPath: exec.LookPath,
		fileExists: func(path string) bool {
			_, err := os.Stat(path)
			return err == nil
		},
		dirExists: func(path string) bool {
			st, err := os.Stat(path)
			return err == nil && st.IsDir()
		},
	})
}

// detectServiceManagerWith mirrors install.sh's ensure_service_manager:
// Alpine implies OpenRC even if a systemctl compatibility binary exists;
// otherwise a live systemd runtime wins; OpenRC tools are the fallback.
// Anything else is an explicit error — panel upgrades must say so instead
// of failing obscurely inside a shell script.
func detectServiceManagerWith(p serviceManagerProbe) (serviceManager, error) {
	hasOpenRC := false
	if p.lookPath != nil {
		_, e1 := p.lookPath("rc-service")
		_, e2 := p.lookPath("rc-update")
		hasOpenRC = e1 == nil && e2 == nil
	}
	if p.fileExists != nil && p.fileExists("/etc/alpine-release") {
		if hasOpenRC {
			return serviceManagerOpenRC, nil
		}
	} else if p.lookPath != nil && p.dirExists != nil {
		if _, err := p.lookPath("systemctl"); err == nil && p.dirExists("/run/systemd/system") {
			return serviceManagerSystemd, nil
		}
	}
	if hasOpenRC {
		return serviceManagerOpenRC, nil
	}
	return "", fmt.Errorf("未检测到受支持的服务管理器（systemd 或 OpenRC），面板内升级不可用，请使用 install.sh 更新节点")
}

// serviceManagerCommands returns the stop/start/is-active shell commands
// for the manager, spelled exactly as install.sh spells them.
func serviceManagerCommands(manager serviceManager) (stop, start, active string, err error) {
	switch manager {
	case serviceManagerSystemd:
		return "systemctl stop flux_agent", "systemctl start flux_agent", "systemctl is-active --quiet flux_agent", nil
	case serviceManagerOpenRC:
		return "rc-service flux_agent stop", "rc-service flux_agent start", "rc-service flux_agent status >/dev/null 2>&1", nil
	default:
		return "", "", "", fmt.Errorf("不支持的服务管理器: %q", string(manager))
	}
}

// agentControlScriptCommand returns the detached launcher for a control
// script. systemd runs it in a transient unit (systemd-run) so that
// stopping flux_agent cannot kill the script via its own cgroup; OpenRC
// has no cgroups, so a new session (setsid) provides the same escape.
func agentControlScriptCommand(manager serviceManager, script string) (string, []string, error) {
	switch manager {
	case serviceManagerSystemd:
		return "systemd-run", []string{"--quiet", "/bin/sh", "-c", script}, nil
	case serviceManagerOpenRC:
		return "setsid", []string{"/bin/sh", "-c", script}, nil
	default:
		return "", nil, fmt.Errorf("不支持的服务管理器: %q", string(manager))
	}
}

func launchAgentControlScript(manager serviceManager, script string) error {
	name, args, err := agentControlScriptCommand(manager, script)
	if err != nil {
		return err
	}
	return exec.Command(name, args...).Start()
}

// buildAgentRestartScriptFor builds the self-contained upgrade restart
// script: stop, swap the binary in, start, then verify the service
// actually came up — if it did not, restore the backup copy and start
// again, all locally on the node without any panel round-trip
// (A11-132-02). Progress is appended to agent-upgrade.log next to the
// binary for post-mortem diagnosis.
func buildAgentRestartScriptFor(manager serviceManager, tmpPath, binaryPath, backupPath string) (string, error) {
	stop, start, active, err := serviceManagerCommands(manager)
	if err != nil {
		return "", err
	}
	logPath := filepath.Join(filepath.Dir(binaryPath), "agent-upgrade.log")

	var b strings.Builder
	fmt.Fprintf(&b, "exec >>%s 2>&1; echo \"=== agent upgrade restart $(date) ===\"; sleep 1; %s; ", logPath, stop)
	if manager == serviceManagerSystemd {
		// Legacy pre-FLMX gost cleanup (systemd units only).
		b.WriteString("legacy_service='' && for service_file in /etc/systemd/system/gost.service /lib/systemd/system/gost.service /usr/lib/systemd/system/gost.service; do if [ -f \"$service_file\" ] && grep -Fq \"WorkingDirectory=/etc/gost\" \"$service_file\" && (grep -Fq \"ExecStart=/etc/gost/gost\" \"$service_file\" || (grep -Fq \"ExecStart=/usr/local/bin/gost\" \"$service_file\" && [ -f /etc/gost/config.json ] && [ -f /etc/gost/gost.json ])); then legacy_service=\"$service_file\"; break; fi; done; if [ -n \"$legacy_service\" ]; then (systemctl stop gost 2>/dev/null || true) && (systemctl disable gost 2>/dev/null || true) && rm -f /usr/local/bin/gost /etc/gost/gost \"$legacy_service\" && (systemctl daemon-reload 2>/dev/null || true); fi; ")
	}
	fmt.Fprintf(&b, "mv %s %s; %s; sleep 3; if ! %s; then echo 'new agent failed to start, rolling back'; if [ -f %s ]; then cp %s %s; %s; sleep 2; if %s; then echo 'rollback succeeded'; else echo 'rollback start failed'; fi; else echo 'no backup available for rollback'; fi; else echo 'agent upgrade restart ok'; fi",
		tmpPath, binaryPath, start, active, backupPath, backupPath, binaryPath, start, active)
	return b.String(), nil
}

// buildAgentRollbackScriptFor builds the manual rollback script with the
// same init abstraction as the upgrade path.
func buildAgentRollbackScriptFor(manager serviceManager, backupPath, binaryPath string) (string, error) {
	stop, start, active, err := serviceManagerCommands(manager)
	if err != nil {
		return "", err
	}
	logPath := filepath.Join(filepath.Dir(binaryPath), "agent-upgrade.log")
	return fmt.Sprintf("exec >>%s 2>&1; echo \"=== agent rollback $(date) ===\"; sleep 1; %s; cp %s %s; %s; sleep 2; if %s; then echo 'rollback restart ok'; else echo 'rollback start failed'; fi",
		logPath, stop, backupPath, binaryPath, start, active), nil
}

// smokeTestAgentBinary verifies a downloaded binary actually executes on
// this node (-V) before it is swapped in. A checksum proves integrity,
// not executability (wrong-arch asset, corrupted UPX payload, libc
// mismatch all pass sha256 and brick the node without this).
func smokeTestAgentBinary(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "-V").CombinedOutput()
	if err != nil {
		return fmt.Errorf("新版本二进制冒烟测试失败 (%s -V): %v, 输出: %s", path, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// backupCurrentAgentBinary copies the running binary to the rollback path.
// Any failure aborts the upgrade: proceeding without a backup would leave
// nothing to roll back to. A not-yet-existing binary is not an error
// (nothing to preserve).
func backupCurrentAgentBinary(binaryPath, backupPath string) error {
	if _, err := os.Stat(binaryPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("检查当前二进制失败: %v", err)
	}
	oldData, err := os.ReadFile(binaryPath)
	if err != nil {
		return fmt.Errorf("读取当前二进制用于备份失败: %v", err)
	}
	if err := os.WriteFile(backupPath, oldData, 0755); err != nil {
		return fmt.Errorf("写入旧版本备份失败: %v", err)
	}
	return nil
}

// maxAgentChecksumSize bounds the checksum download (D1-08): the body is a
// single hash line; the binary download next to it was already capped at
// 100MB while this read was unbounded.
const maxAgentChecksumSize = 4096

// readAgentChecksum reads and parses the expected sha256 from a checksum
// response body with a hard size cap.
func readAgentChecksum(r io.Reader) (string, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxAgentChecksumSize+1))
	if err != nil {
		return "", err
	}
	if len(body) > maxAgentChecksumSize {
		return "", fmt.Errorf("校验文件过大 (%d bytes)，拒绝安装", len(body))
	}
	fields := strings.Fields(string(body))
	if len(fields) == 0 {
		return "", fmt.Errorf("校验值为空，拒绝安装")
	}
	return fields[0], nil
}

func (w *WebSocketReporter) handleUpgradeAgent(data interface{}) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("序列化数据失败: %v", err)
	}

	// S-02: Agent 只接受 version 参数，不再接受面板传入的任意 URL
	// 仓库路径为编译期常量，防止面板指定任意下载源
	const agentRepo = "iiiiiiiii7125-art/flmx-max"
	var agentVersionRe = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+$`)

	var req struct {
		Version string `json:"version"`
		// 兼容旧面板：若仍发送 downloadUrl 则拒绝（不允许任意 URL）
		DownloadURL string `json:"downloadUrl"`
		ChecksumURL string `json:"checksumUrl"`
		// 代理前缀（可选）：面板在系统设置开启 GitHub 代理时下发，或 agent
		// 本地 config.json 的 proxy 字段（安装时落盘）。仅作前缀拼接，
		// 下载地址仍由 agent 用固定仓库+版本号本地构造，S-02 模型不变。
		ProxyURL string `json:"proxyUrl"`
	}
	if err := json.Unmarshal(jsonData, &req); err != nil {
		return fmt.Errorf("解析升级参数失败: %v", err)
	}
	// S-02: 拒绝面板传入的任意 URL（旧面板兼容性检查）
	if strings.TrimSpace(req.DownloadURL) != "" {
		return fmt.Errorf("S-02: 不再接受面板指定的下载地址，请升级面板到修复版本")
	}
	version := strings.TrimSpace(req.Version)
	if version == "" {
		return fmt.Errorf("版本号不能为空")
	}
	if !agentVersionRe.MatchString(version) {
		return fmt.Errorf("S-02: 版本号格式无效: %s", version)
	}

	// S-02: Agent 本地用固定仓库 + 严格校验的版本号拼 URL
	base := fmt.Sprintf("https://github.com/%s/releases/download/%s/", agentRepo, version)
	downloadURL := base + "gost-" + runtime.GOARCH
	checksumURL := base + "gost-" + runtime.GOARCH + ".sha256"

	// 代理支持：国内节点直连 GitHub 不通时经代理前缀下载（与 install.sh 的
	// maybe_proxy_url 同一模式：<proxy>/https://github.com/...）。
	// 代理仅作前缀，路径仍由固定仓库+已校验版本号构造；完整性由 SHA256 兜底。
	proxyPrefix := resolveAgentUpgradeProxy(req.ProxyURL)
	proxyHost := ""
	if proxyPrefix != "" {
		downloadURL = proxyPrefix + "/" + downloadURL
		checksumURL = proxyPrefix + "/" + checksumURL
		if u, err := url.Parse(proxyPrefix); err == nil {
			proxyHost = strings.ToLower(u.Host)
		}
		fmt.Printf("📦 升级包经代理下载: %s\n", proxyPrefix)
	}

	w.sendUpgradeProgress("downloading", 0, "开始下载升级包...")
	fmt.Printf("📦 开始下载升级包: %s\n", downloadURL)

	// 下载新版本二进制
	const binaryPath = "/etc/flux_agent/flux_agent"
	tmpPath := binaryPath + ".new"
	backupPath := binaryPath + ".old"

	// S-02: 限制重定向只允许 GitHub 官方域名，防止跳转到恶意域名
	// 限制下载大小 100MB，超时 60 秒
	httpClient := &http.Client{
		Timeout: 60 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("S-02: 重定向次数过多")
			}
			// 只允许 GitHub 官方域名：github.com 及其子域名、资产 CDN
			// githubusercontent.com 及其子域名（release 下载必经此域名）；
			// 经代理下载时额外允许代理自身域名（镜像站内跳转）。
			host := strings.ToLower(req.URL.Host)
			if isGitHubDownloadHost(host) || (proxyHost != "" && host == proxyHost) {
				return nil
			}
			return fmt.Errorf("S-02: 重定向到非 GitHub 域名被拒绝: %s", req.URL.Host)
		},
	}
	resp, err := httpClient.Get(downloadURL)
	if err != nil {
		return fmt.Errorf("下载升级包失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载升级包失败, HTTP状态码: %d", resp.StatusCode)
	}

	// S-02: 限制下载大小，防止超大文件耗尽磁盘
	const maxAgentBinarySize = 100 << 20 // 100MB
	if resp.ContentLength > maxAgentBinarySize {
		return fmt.Errorf("S-02: 升级包过大 (%d bytes)，拒绝下载", resp.ContentLength)
	}

	// S-02: 临时文件权限 0700，仅所有者可读写
	outFile, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0700)
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %v", err)
	}

	// 带进度的下载
	totalSize := resp.ContentLength
	var downloaded int64
	buf := make([]byte, 32*1024)
	lastPercent := 0
	hasher := sha256.New()

	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			// S-02: 下载过程中也检查大小（防 ContentLength 欺骗）
			if downloaded+int64(n) > maxAgentBinarySize {
				outFile.Close()
				os.Remove(tmpPath)
				return fmt.Errorf("S-02: 升级包超过大小限制 (%d bytes)", maxAgentBinarySize)
			}
			if _, wErr := outFile.Write(buf[:n]); wErr != nil {
				outFile.Close()
				os.Remove(tmpPath)
				return fmt.Errorf("写入升级包失败: %v", wErr)
			}
			hasher.Write(buf[:n])
			downloaded += int64(n)
			if totalSize > 0 {
				percent := int(downloaded * 100 / totalSize)
				if percent-lastPercent >= 10 {
					lastPercent = percent
					w.sendUpgradeProgress("downloading", percent, fmt.Sprintf("下载中... %d%%", percent))
				}
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			outFile.Close()
			os.Remove(tmpPath)
			return fmt.Errorf("读取升级包失败: %v", readErr)
		}
	}
	outFile.Close()

	if downloaded == 0 {
		os.Remove(tmpPath)
		return fmt.Errorf("下载的升级包为空")
	}

	w.sendUpgradeProgress("downloading", 100, fmt.Sprintf("下载完成 (%d bytes)", downloaded))

	// S-02: Checksum 校验强制，不再允许为空或跳过
	// 若拿不到 checksum 或校验失败，一律中止并删除临时文件
	w.sendUpgradeProgress("verifying", 0, "校验文件完整性...")
	checksumResp, err := httpClient.Get(checksumURL)
	if err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("S-02: 获取校验值失败: %v", err)
	}
	defer checksumResp.Body.Close()
	if checksumResp.StatusCode != http.StatusOK {
		os.Remove(tmpPath)
		return fmt.Errorf("S-02: 获取校验值失败, HTTP状态码: %d", checksumResp.StatusCode)
	}
	// 格式: "<hash>  <filename>" 或 "<hash>"；读取带上限 (D1-08)
	expectedHash, err := readAgentChecksum(checksumResp.Body)
	if err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("S-02: 读取校验值失败: %v", err)
	}
	actualHash := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(expectedHash, actualHash) {
		os.Remove(tmpPath)
		return fmt.Errorf("S-02: 校验失败: 期望 %s, 实际 %s", expectedHash, actualHash)
	}
	fmt.Printf("✅ Checksum 校验通过: %s\n", actualHash)
	w.sendUpgradeProgress("verifying", 100, "校验通过")

	if err := os.Chmod(tmpPath, 0755); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("设置执行权限失败: %v", err)
	}

	// A11-132-02: 替换前先做可执行冒烟（-V）。sha256 只证明完整性，
	// 不证明新二进制在本机可执行（错架构/UPX 损坏/libc 不兼容）。
	w.sendUpgradeProgress("installing", 30, "冒烟测试新版本...")
	if err := smokeTestAgentBinary(tmpPath); err != nil {
		os.Remove(tmpPath)
		return err
	}
	fmt.Println("✅ 新版本二进制冒烟测试通过")

	// 备份旧版本（复制，不用 rename，因为可能正在运行）。
	// A11-132-02: 备份失败必须中止升级——没有备份就没有回滚路径，
	// 旧实现吞掉写错误继续替换是节点变砖的直接原因。
	w.sendUpgradeProgress("installing", 50, "备份旧版本...")
	if err := backupCurrentAgentBinary(binaryPath, backupPath); err != nil {
		os.Remove(tmpPath)
		return err
	}
	fmt.Println("📦 旧版本已备份到", backupPath)

	w.sendUpgradeProgress("installing", 80, "准备重启...")
	fmt.Printf("✅ 升级包下载完成 (%d bytes), 准备重启...\n", downloaded)

	// A11-132-01: 按节点的 init 系统分发重启脚本（systemd / OpenRC），
	// 不支持的 init 明确报错而不是静默失败。脚本在脱离 agent 进程的
	// 环境中运行（systemd-run transient unit / setsid 新会话），避免
	// 停服务时把脚本自己一并杀掉；脚本内含启动失败自动回滚（本地闭环，
	// 不依赖面板连接）。
	manager, err := detectServiceManager()
	if err != nil {
		os.Remove(tmpPath)
		return err
	}
	script, err := buildAgentRestartScriptFor(manager, tmpPath, binaryPath, backupPath)
	if err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := launchAgentControlScript(manager, script); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("启动重启脚本失败: %v", err)
	}

	w.sendUpgradeProgress("installing", 100, "重启中...")
	fmt.Println("🔄 重启脚本已启动, Agent 将在 1 秒后重启...")
	return nil
}

func (w *WebSocketReporter) handleRollbackAgent(data interface{}) error {
	const binaryPath = "/etc/flux_agent/flux_agent"
	backupPath := binaryPath + ".old"

	// 检查备份文件是否存在
	if _, err := os.Stat(backupPath); os.IsNotExist(err) {
		return fmt.Errorf("没有可用的备份文件，无法回退")
	}

	fmt.Println("🔄 开始回退到旧版本...")

	// 执行回退脚本（与升级同一套 init 分发，A11-132-01）
	manager, err := detectServiceManager()
	if err != nil {
		return err
	}
	script, err := buildAgentRollbackScriptFor(manager, backupPath, binaryPath)
	if err != nil {
		return err
	}
	if err := launchAgentControlScript(manager, script); err != nil {
		return fmt.Errorf("启动回退脚本失败: %v", err)
	}

	fmt.Println("🔄 回退脚本已启动, Agent 将在 1 秒后重启...")
	return nil
}

// buildAgentRestartScript is the systemd variant of
// buildAgentRestartScriptFor, kept for compatibility with existing
// callers/tests.
func buildAgentRestartScript(tmpPath, binaryPath string) string {
	script, err := buildAgentRestartScriptFor(serviceManagerSystemd, tmpPath, binaryPath, binaryPath+".old")
	if err != nil {
		return ""
	}
	return script
}

// updateLocalConfigJSON 将 http/tls/socks 写入工作目录下的 config.json
func updateLocalConfigJSON(httpVal int, tlsVal int, socksVal int) error {
	path := "config.json"

	// 读取现有配置
	type LocalConfig struct {
		Addr   string `json:"addr"`
		Secret string `json:"secret"`
		Http   int    `json:"http"`
		Tls    int    `json:"tls"`
		Socks  int    `json:"socks"`
	}

	var cfg LocalConfig
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &cfg)
	}

	cfg.Http = httpVal
	cfg.Tls = tlsVal
	cfg.Socks = socksVal

	// 写回
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// handleCall 处理服务端的call回调消息
func (w *WebSocketReporter) handleCall(data interface{}) error {
	// 解析call数据
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("序列化call数据失败: %v", err)
	}

	// 可以根据call的具体内容进行不同的处理
	var callData map[string]interface{}
	if err := json.Unmarshal(jsonData, &callData); err != nil {
		return fmt.Errorf("解析call数据失败: %v", err)
	}

	fmt.Printf("🔔 收到服务端call回调: %v\n", callData)

	// 根据call的类型执行不同的操作
	if callType, exists := callData["type"]; exists {
		switch callType {
		case "ping":
			fmt.Printf("📡 收到ping，发送pong回应\n")
			// 可以在这里发送pong响应
		case "info_request":
			fmt.Printf("📊 服务端请求额外信息\n")
			// 可以在这里发送额外的系统信息
		case "command":
			fmt.Printf("⚡ 服务端发送执行命令\n")
			// 可以在这里执行特定命令
		default:
			fmt.Printf("❓ 未知的call类型: %v\n", callType)
		}
	}

	// 简单返回成功，表示call已被处理
	return nil
}

// sendResponse 发送响应消息到服务端
func (w *WebSocketReporter) sendResponse(response CommandResponse) {
	w.connMutex.Lock()
	defer w.connMutex.Unlock()

	if w.conn == nil || !w.connected {
		fmt.Printf("❌ 无法发送响应：连接未建立\n")
		return
	}

	jsonData, err := json.Marshal(response)
	if err != nil {
		fmt.Printf("❌ 序列化响应失败: %v\n", err)
		return
	}

	messageData, err := w.encryptPayload(jsonData)
	if err != nil {
		fmt.Printf("❌ 无法发送响应: %v\n", err)
		return
	}

	// 检查消息大小，如果超过10MB则记录警告
	if len(messageData) > 10*1024*1024 {
		fmt.Printf("⚠️ 响应消息过大 (%.2f MB)，可能会被拒绝\n", float64(len(messageData))/(1024*1024))
	}

	// 设置较长的写入超时，以应对大消息
	timeout := 5 * time.Second
	if len(messageData) > 1024*1024 {
		timeout = 30 * time.Second
	}

	w.conn.SetWriteDeadline(time.Now().Add(timeout))
	if err := w.conn.WriteMessage(websocket.TextMessage, messageData); err != nil {
		fmt.Printf("❌ 发送响应失败: %v\n", err)
		w.connected = false
	}
}

// sendErrorResponse 发送错误响应
func (w *WebSocketReporter) sendErrorResponse(responseType, message string) {
	response := CommandResponse{
		Type:    responseType,
		Success: false,
		Message: message,
	}
	w.sendResponse(response)
}

// getUptime 获取系统开机时间（秒）
func getUptime() uint64 {
	uptime, err := host.Uptime()
	if err != nil {
		return 0
	}
	return uptime
}

// getNetworkStats 获取网络统计信息
func getNetworkStats() NetworkStats {
	var stats NetworkStats

	ioCounters, err := psnet.IOCounters(true)
	if err != nil {
		fmt.Printf("获取网络统计失败: %v\n", err)
		return stats
	}

	for _, io := range ioCounters {
		if io.Name == "lo" || strings.HasPrefix(io.Name, "lo") {
			continue
		}
		stats.BytesReceived += io.BytesRecv
		stats.BytesTransmitted += io.BytesSent
	}

	if lastNetBytesReceived > 0 && stats.BytesReceived >= lastNetBytesReceived {
		stats.BytesRecvDelta = stats.BytesReceived - lastNetBytesReceived
	}
	if lastNetBytesTransmitted > 0 && stats.BytesTransmitted >= lastNetBytesTransmitted {
		stats.BytesSentDelta = stats.BytesTransmitted - lastNetBytesTransmitted
	}

	return stats
}

// getCPUInfo 获取CPU信息
func getCPUInfo() CPUInfo {
	var cpuInfo CPUInfo

	// 获取CPU使用率 (non-blocking)
	percentages, err := cpu.Percent(0, false)
	if err == nil && len(percentages) > 0 {
		cpuInfo.Usage = percentages[0]
	}

	return cpuInfo
}

// getMemoryInfo 获取内存信息
func getMemoryInfo() MemoryInfo {
	var memInfo MemoryInfo

	vmStat, err := mem.VirtualMemory()
	if err != nil {
		return memInfo
	}

	memInfo.Usage = vmStat.UsedPercent

	return memInfo
}

// getDiskInfo 获取磁盘信息
func getDiskInfo() DiskInfo {
	var diskInfo DiskInfo

	usage, err := disk.Usage("/")
	if err != nil {
		return diskInfo
	}

	diskInfo.Usage = usage.UsedPercent

	return diskInfo
}

// getLoadInfo 获取负载信息
func getLoadInfo() LoadInfo {
	var loadInfo LoadInfo

	avg, err := load.Avg()
	if err != nil {
		return loadInfo
	}

	loadInfo.Load1 = avg.Load1
	loadInfo.Load5 = avg.Load5
	loadInfo.Load15 = avg.Load15

	return loadInfo
}

// getConnectionInfo 获取连接信息
func getConnectionInfo() ConnectionInfo {
	now := time.Now().UnixMilli()
	const refreshEveryMs = int64((15 * time.Second) / time.Millisecond)

	connInfoCachedMu.Lock()
	if connInfoCachedAt > 0 && now-connInfoCachedAt < refreshEveryMs {
		v := connInfoCached
		connInfoCachedMu.Unlock()
		return v
	}
	connInfoCachedMu.Unlock()

	var connInfo ConnectionInfo

	connStats, err := psnet.Connections("tcp")
	if err == nil {
		connInfo.TCPConns = int64(len(connStats))
	}

	udpStats, err := psnet.Connections("udp")
	if err == nil {
		connInfo.UDPConns = int64(len(udpStats))
	}

	connInfoCachedMu.Lock()
	connInfoCached = connInfo
	connInfoCachedAt = now
	connInfoCachedMu.Unlock()

	return connInfo
}

// StartWebSocketReporterWithConfig 使用配置字段启动WebSocket报告器
func StartWebSocketReporterWithConfig(addr string, secret string, http int, tls int, socks int, version string) *WebSocketReporter {

	// 构建初始 WebSocket URL
	candidates := buildWebSocketCandidates(addr, secret, version, http, tls, socks, "")
	fullURL := candidates[0]

	fmt.Printf("🔗 WebSocket连接URL: %s\n", sanitizeWebSocketURL(fullURL))

	reporter := NewWebSocketReporter(fullURL, secret)
	// 保存 addr, secret, version 和协议能力供重连时使用
	reporter.addr = addr
	reporter.secret = secret
	reporter.version = version
	reporter.http = http
	reporter.tls = tls
	reporter.socks = socks
	reporter.Start()
	return reporter
}

var configPersistPath string

// SetConfigPersistPath sets the path where runtime config changes will be
// persisted to disk (gost.json). Called by main during agent startup.
func SetConfigPersistPath(path string) {
	configPersistPath = path
	config.SetPersistPath(path)
}

// EnableConfigPersist turns on automatic disk persistence after the initial
// config has been loaded and applied.
func EnableConfigPersist() {
	config.EnablePersist()
	path := config.PersistPath()
	if path == "" {
		path = configPersistPath
	}
	fmt.Printf("🔒 节点配置持久化已启用，运行时变更将自动保存到 %s\n", path)
}

// F-088: 诊断 ping 的 count/timeout 上限（与仓库其他上限风格一致：
// 延迟测试点上限 10、导入上限 500）。未钳制时面板下发 count=100000
// 可让单个只读命令按 count×(timeout+100ms 间隔) 长期占用命令槽。
const (
	maxPingCount     = 20
	maxPingTimeoutMs = 10000
)

// clampPingParams 归一化 ping 参数：非正值回退默认，超出上限钳制。
func clampPingParams(count, timeout int) (int, int) {
	if count <= 0 {
		count = 4
	}
	if count > maxPingCount {
		count = maxPingCount
	}
	if timeout <= 0 {
		timeout = 5000 // 默认5秒超时
	}
	if timeout > maxPingTimeoutMs {
		timeout = maxPingTimeoutMs
	}
	return count, timeout
}

// handleTcpPing 处理TCP ping诊断命令
func (w *WebSocketReporter) handleTcpPing(data interface{}) (TcpPingResponse, error) {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return TcpPingResponse{}, fmt.Errorf("序列化TCP ping数据失败: %v", err)
	}

	var req TcpPingRequest
	if err := json.Unmarshal(jsonData, &req); err != nil {
		return TcpPingResponse{}, fmt.Errorf("解析TCP ping请求失败: %v", err)
	}

	// 验证IP地址格式
	if net.ParseIP(req.IP) == nil && !isValidHostname(req.IP) {
		return TcpPingResponse{
			IP:           req.IP,
			Port:         req.Port,
			Success:      false,
			ErrorMessage: "无效的IP地址或主机名",
			RequestId:    req.RequestId,
		}, nil
	}

	// 验证端口范围
	if req.Port <= 0 || req.Port > 65535 {
		return TcpPingResponse{
			IP:           req.IP,
			Port:         req.Port,
			Success:      false,
			ErrorMessage: "无效的端口号，范围应为1-65535",
			RequestId:    req.RequestId,
		}, nil
	}

	// 设置默认值并钳制上限（F-088）
	req.Count, req.Timeout = clampPingParams(req.Count, req.Timeout)

	// 执行TCP ping操作
	avgTime, packetLoss, err := tcpPingHost(req.IP, req.Port, req.Count, req.Timeout)

	response := TcpPingResponse{
		IP:        req.IP,
		Port:      req.Port,
		RequestId: req.RequestId,
	}

	if err != nil {
		response.Success = false
		response.ErrorMessage = err.Error()
	} else {
		response.Success = true
		response.AverageTime = avgTime
		response.PacketLoss = packetLoss
	}

	return response, nil
}

func (w *WebSocketReporter) handleUdpPing(data interface{}) (TcpPingResponse, error) {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return TcpPingResponse{}, fmt.Errorf("序列化UDP ping数据失败: %v", err)
	}

	var req TcpPingRequest
	if err := json.Unmarshal(jsonData, &req); err != nil {
		return TcpPingResponse{}, fmt.Errorf("解析UDP ping请求失败: %v", err)
	}

	if net.ParseIP(req.IP) == nil && !isValidHostname(req.IP) {
		return TcpPingResponse{
			IP:           req.IP,
			Port:         req.Port,
			Success:      false,
			ErrorMessage: "无效的IP地址或主机名",
			RequestId:    req.RequestId,
		}, nil
	}

	if req.Port <= 0 || req.Port > 65535 {
		return TcpPingResponse{
			IP:           req.IP,
			Port:         req.Port,
			Success:      false,
			ErrorMessage: "无效的端口号，范围应为1-65535",
			RequestId:    req.RequestId,
		}, nil
	}

	req.Count, req.Timeout = clampPingParams(req.Count, req.Timeout)

	avgTime, packetLoss, err := udpPingHost(req.IP, req.Port, req.Count, req.Timeout)

	response := TcpPingResponse{
		IP:        req.IP,
		Port:      req.Port,
		RequestId: req.RequestId,
	}

	if err != nil {
		response.Success = false
		response.ErrorMessage = err.Error()
	} else {
		response.Success = true
		response.AverageTime = avgTime
		response.PacketLoss = packetLoss
	}

	return response, nil
}

const (
	defaultServiceMonitorTimeoutSec = 5
	// Aligned with the panel's service-monitor limit default
	// (monitoring.MaxTimeoutSec=60): larger panel-supplied values were
	// previously honored unbounded, letting one check occupy a bounded
	// read-command slot for arbitrarily long (D1-04, the F-088 clamp
	// missed this command).
	maxServiceMonitorTimeoutSec = 60
)

// clampServiceMonitorTimeoutSec normalizes a panel-supplied monitor
// timeout: non-positive falls back to the default, oversized is clamped.
func clampServiceMonitorTimeoutSec(sec int) int {
	if sec <= 0 {
		return defaultServiceMonitorTimeoutSec
	}
	if sec > maxServiceMonitorTimeoutSec {
		return maxServiceMonitorTimeoutSec
	}
	return sec
}

// handleServiceMonitorCheck executes a service monitor check on this node.
// It always returns a result (command execution is considered successful even if the check fails).
func (w *WebSocketReporter) handleServiceMonitorCheck(data interface{}) (ServiceMonitorCheckResult, error) {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return ServiceMonitorCheckResult{}, fmt.Errorf("序列化检查数据失败: %v", err)
	}

	var req ServiceMonitorCheckRequest
	if err := json.Unmarshal(jsonData, &req); err != nil {
		return ServiceMonitorCheckResult{}, fmt.Errorf("解析检查请求失败: %v", err)
	}

	checkType := strings.ToLower(strings.TrimSpace(req.Type))
	target := strings.TrimSpace(req.Target)
	res := ServiceMonitorCheckResult{MonitorID: req.MonitorID}

	if checkType != "tcp" && checkType != "icmp" {
		res.Success = false
		res.ErrorMessage = "不支持的检查类型"
		return res, nil
	}
	if target == "" {
		res.Success = false
		res.ErrorMessage = "检查目标为空"
		return res, nil
	}

	timeoutSec := clampServiceMonitorTimeoutSec(req.TimeoutSec)
	timeout := time.Duration(timeoutSec) * time.Second

	start := time.Now()

	switch checkType {
	case "tcp":
		// Validate and normalize host:port.
		_, _, splitErr := net.SplitHostPort(target)
		if splitErr != nil {
			res.Success = false
			res.ErrorMessage = "无效的TCP目标"
			res.LatencyMs = float64(time.Since(start).Milliseconds())
			return res, nil
		}
		conn, dialErr := net.DialTimeout("tcp", target, timeout)
		res.LatencyMs = float64(time.Since(start).Milliseconds())
		if dialErr != nil {
			res.Success = false
			res.ErrorMessage = dialErr.Error()
			return res, nil
		}
		_ = conn.Close()
		res.Success = true
		return res, nil

	case "icmp":
		rtt, pingErr := icmpPing(target, timeout)
		res.LatencyMs = float64(rtt.Milliseconds())
		if pingErr != nil {
			res.Success = false
			res.ErrorMessage = pingErr.Error()
			return res, nil
		}
		res.Success = true
		return res, nil
	}

	res.Success = false
	res.ErrorMessage = "未知错误"
	res.LatencyMs = float64(time.Since(start).Milliseconds())
	return res, nil
}

func icmpPing(target string, timeout time.Duration) (time.Duration, error) {
	start := time.Now()

	target = strings.TrimSpace(target)
	if target == "" {
		return time.Since(start), fmt.Errorf("无效的ICMP目标")
	}
	// Avoid accepting URL-like targets.
	if strings.Contains(target, "://") {
		return time.Since(start), fmt.Errorf("无效的ICMP目标")
	}
	if strings.HasPrefix(target, "[") && strings.HasSuffix(target, "]") {
		target = strings.TrimSuffix(strings.TrimPrefix(target, "["), "]")
	}

	ipAddr, err := net.ResolveIPAddr("ip", target)
	if err != nil || ipAddr == nil || ipAddr.IP == nil {
		if err == nil {
			err = fmt.Errorf("unknown address")
		}
		return time.Since(start), fmt.Errorf("解析目标失败: %v", err)
	}

	isV4 := ipAddr.IP.To4() != nil
	listenAddr := "0.0.0.0"
	proto := 1
	var echoType icmp.Type = ipv4.ICMPTypeEcho
	var echoReplyType icmp.Type = ipv4.ICMPTypeEchoReply
	networks := []string{"udp4", "ip4:icmp"}
	if !isV4 {
		listenAddr = "::"
		proto = 58
		echoType = ipv6.ICMPTypeEchoRequest
		echoReplyType = ipv6.ICMPTypeEchoReply
		networks = []string{"udp6", "ip6:ipv6-icmp"}
	}

	var conn *icmp.PacketConn
	selectedNetwork := ""
	var lastErr error
	for _, nw := range networks {
		c, err := icmp.ListenPacket(nw, listenAddr)
		if err == nil {
			conn = c
			selectedNetwork = nw
			break
		}
		lastErr = err
	}
	if conn == nil {
		if lastErr != nil {
			return time.Since(start), fmt.Errorf("创建ICMP连接失败: %v", lastErr)
		}
		return time.Since(start), fmt.Errorf("创建ICMP连接失败")
	}
	defer conn.Close()

	id := os.Getpid() & 0xffff
	seq := 1

	wm := icmp.Message{
		Type: echoType,
		Code: 0,
		Body: &icmp.Echo{
			ID:   id,
			Seq:  seq,
			Data: []byte("FLMX-PING"),
		},
	}
	wb, err := wm.Marshal(nil)
	if err != nil {
		return time.Since(start), err
	}

	_ = conn.SetDeadline(time.Now().Add(timeout))

	var dst net.Addr
	if strings.HasPrefix(selectedNetwork, "udp") {
		dst = &net.UDPAddr{IP: ipAddr.IP, Zone: ipAddr.Zone}
	} else {
		dst = &net.IPAddr{IP: ipAddr.IP, Zone: ipAddr.Zone}
	}

	if _, err := conn.WriteTo(wb, dst); err != nil {
		return time.Since(start), err
	}

	addrIP := func(a net.Addr) net.IP {
		switch v := a.(type) {
		case *net.IPAddr:
			return v.IP
		case *net.UDPAddr:
			return v.IP
		default:
			return nil
		}
	}

	rb := make([]byte, 1500)
	for {
		n, peer, err := conn.ReadFrom(rb)
		if err != nil {
			return time.Since(start), err
		}
		if p := addrIP(peer); p != nil && !p.Equal(ipAddr.IP) {
			continue
		}
		rm, err := icmp.ParseMessage(proto, rb[:n])
		if err != nil {
			continue
		}
		if rm.Type != echoReplyType {
			continue
		}
		echo, ok := rm.Body.(*icmp.Echo)
		if !ok {
			continue
		}
		if echo.Seq != seq {
			continue
		}
		// For non-privileged endpoints, the kernel may choose the ID.
		if !strings.HasPrefix(selectedNetwork, "udp") && echo.ID != id {
			continue
		}
		return time.Since(start), nil
	}
}

// tcpPingHost 执行TCP连接测试，返回平均连接时间和失败率
func tcpPingHost(ip string, port int, count int, timeoutMs int) (float64, float64, error) {
	var totalTime float64
	var successCount int

	timeout := time.Duration(timeoutMs) * time.Millisecond

	// 使用net.JoinHostPort来正确处理IPv4、IPv6和域名
	// 它会自动为IPv6地址添加方括号
	target := net.JoinHostPort(ip, fmt.Sprintf("%d", port))

	fmt.Printf("🔍 开始TCP ping测试: %s，次数: %d，超时: %dms\n", target, count, timeoutMs)

	// 如果是域名，先解析一次DNS，避免每次连接都重新解析导致延迟累加
	if net.ParseIP(ip) == nil {
		// 是域名，需要解析
		fmt.Printf("🔍 检测到域名，正在解析DNS...\n")
		dnsStart := time.Now()

		addrs, err := net.LookupHost(ip)
		dnsDuration := time.Since(dnsStart)

		if err != nil {
			return 0, 100.0, fmt.Errorf("DNS解析失败: %v", err)
		}
		if len(addrs) == 0 {
			return 0, 100.0, fmt.Errorf("DNS解析未返回任何IP地址")
		}

		fmt.Printf("✅ DNS解析完成 (%.2fms)，解析到 %d 个IP: %v\n",
			dnsDuration.Seconds()*1000, len(addrs), addrs)

		// 使用第一个解析到的IP进行测试
		target = net.JoinHostPort(addrs[0], fmt.Sprintf("%d", port))
		fmt.Printf("🎯 使用IP地址进行测试: %s\n", target)
	} else {
		fmt.Printf("🎯 使用IP地址进行测试: %s\n", target)
	}

	for i := 0; i < count; i++ {
		start := time.Now()

		// 创建带超时的TCP连接
		conn, err := net.DialTimeout("tcp", target, timeout)

		elapsed := time.Since(start)

		if err != nil {
			fmt.Printf("  第%d次连接失败: %v (%.2fms)\n", i+1, err, elapsed.Seconds()*1000)
		} else {
			fmt.Printf("  第%d次连接成功: %.2fms\n", i+1, elapsed.Seconds()*1000)
			conn.Close()
			totalTime += elapsed.Seconds() * 1000 // 转换为毫秒
			successCount++
		}

		// 如果不是最后一次，等待一下再进行下次测试
		if i < count-1 {
			time.Sleep(100 * time.Millisecond)
		}
	}

	if successCount == 0 {
		return 0, 100.0, fmt.Errorf("所有TCP连接尝试都失败")
	}

	avgTime := totalTime / float64(successCount)
	packetLoss := float64(count-successCount) / float64(count) * 100

	fmt.Printf("✅ TCP ping完成: 平均连接时间 %.2fms，失败率 %.1f%%\n", avgTime, packetLoss)

	return avgTime, packetLoss, nil
}

func udpPingHost(ip string, port int, count int, timeoutMs int) (float64, float64, error) {
	var totalTime float64
	var successCount int

	timeout := time.Duration(timeoutMs) * time.Millisecond
	target := net.JoinHostPort(ip, fmt.Sprintf("%d", port))

	fmt.Printf("🔍 开始UDP ping测试: %s，次数: %d，超时: %dms\n", target, count, timeoutMs)

	if net.ParseIP(ip) == nil {
		fmt.Printf("🔍 检测到域名，正在解析DNS...\n")
		dnsStart := time.Now()

		addrs, err := net.LookupHost(ip)
		dnsDuration := time.Since(dnsStart)

		if err != nil {
			return 0, 100.0, fmt.Errorf("DNS解析失败: %v", err)
		}
		if len(addrs) == 0 {
			return 0, 100.0, fmt.Errorf("DNS解析未返回任何IP地址")
		}

		fmt.Printf("✅ DNS解析完成 (%.2fms)，解析到 %d 个IP: %v\n",
			dnsDuration.Seconds()*1000, len(addrs), addrs)

		target = net.JoinHostPort(addrs[0], fmt.Sprintf("%d", port))
		fmt.Printf("🎯 使用IP地址进行测试: %s\n", target)
	} else {
		fmt.Printf("🎯 使用IP地址进行测试: %s\n", target)
	}

	addr, err := net.ResolveUDPAddr("udp", target)
	if err != nil {
		return 0, 100.0, fmt.Errorf("解析UDP地址失败: %v", err)
	}

	for i := 0; i < count; i++ {
		start := time.Now()

		conn, err := net.DialTimeout("udp", addr.String(), timeout)

		elapsed := time.Since(start)

		if err != nil {
			fmt.Printf("  第%d次UDP连接失败: %v (%.2fms)\n", i+1, err, elapsed.Seconds()*1000)
		} else {
			fmt.Printf("  第%d次UDP连接成功: %.2fms\n", i+1, elapsed.Seconds()*1000)
			conn.Close()
			totalTime += elapsed.Seconds() * 1000
			successCount++
		}

		if i < count-1 {
			time.Sleep(100 * time.Millisecond)
		}
	}

	if successCount == 0 {
		return 0, 100.0, fmt.Errorf("所有UDP连接尝试都失败")
	}

	avgTime := totalTime / float64(successCount)
	packetLoss := float64(count-successCount) / float64(count) * 100

	fmt.Printf("✅ UDP ping完成: 平均连接时间 %.2fms，失败率 %.1f%%\n", avgTime, packetLoss)

	return avgTime, packetLoss, nil
}

// isValidHostname 验证主机名格式
func isValidHostname(hostname string) bool {
	if len(hostname) == 0 || len(hostname) > 253 {
		return false
	}

	// 简单的主机名验证
	for _, r := range hostname {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '.') {
			return false
		}
	}

	return true
}

// preprocessDurationFields 预处理 JSON 数据中的 duration 字段
func (w *WebSocketReporter) preprocessDurationFields(jsonData []byte) ([]byte, error) {
	var rawData interface{}
	if err := json.Unmarshal(jsonData, &rawData); err != nil {
		return nil, err
	}

	// 递归处理 duration 字段
	processed := w.processDurationInData(rawData)

	return json.Marshal(processed)
}

// processDurationInData 递归处理数据中的 duration 字段
func (w *WebSocketReporter) processDurationInData(data interface{}) interface{} {
	switch v := data.(type) {
	case []interface{}:
		// 处理数组
		for i, item := range v {
			v[i] = w.processDurationInData(item)
		}
		return v
	case map[string]interface{}:
		// 处理对象
		for key, value := range v {
			if key == "selector" {
				// 处理 selector 对象中的 failTimeout
				if selectorObj, ok := value.(map[string]interface{}); ok {
					if failTimeoutVal, exists := selectorObj["failTimeout"]; exists {
						if failTimeoutStr, ok := failTimeoutVal.(string); ok {
							// 将字符串格式的 duration 转换为纳秒数
							if duration, err := time.ParseDuration(failTimeoutStr); err == nil {
								selectorObj["failTimeout"] = int64(duration)
							}
						}
					}
				}
			}
			v[key] = w.processDurationInData(value)
		}
		return v
	default:
		return v
	}
}
