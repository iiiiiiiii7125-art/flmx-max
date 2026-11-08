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
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-gost/x/bypass"
	"github.com/go-gost/x/config"
	"github.com/go-gost/x/internal/util/crypto"
	"github.com/go-gost/x/internal/util/download"
	"github.com/go-gost/x/kernel"
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
	Uptime           uint64 `json:"uptime"`
	BytesReceived    uint64 `json:"bytes_received"`
	BytesTransmitted uint64 `json:"bytes_transmitted"`
	// BootID 是本机本次开机的标识（machineBootID）：面板据此区分
	// 网卡累计计数的回退是真重启还是接口增删（加法字段，旧面板
	// 忽略未知字段、旧 agent 不发则为空）。
	BootID      string  `json:"boot_id"`
	CPUUsage    float64 `json:"cpu_usage"`
	MemoryUsage float64 `json:"memory_usage"`
	DiskUsage   float64 `json:"disk_usage"`
	Load1       float64 `json:"load1"`
	Load5       float64 `json:"load5"`
	Load15      float64 `json:"load15"`
	TCPConns    int64   `json:"tcp_conns"`
	UDPConns    int64   `json:"udp_conns"`
	NetInSpeed  int64   `json:"net_in_speed"`
	NetOutSpeed int64   `json:"net_out_speed"`
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
	// TimeoutMS 是面板下发 TcpPing 时附带的整体超时预算（毫秒，
	// DB-5）；老面板不发此字段（0），
	// 此时节点侧排队预算回退 reporterReadWait。
	TimeoutMS int64 `json:"timeoutMs,omitempty"`
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
	// Unverified 仅 UdpPing 使用（R6-L4-01）：UDP 无通用回声协议、
	// 不可实测，拨号「成功」不构成连通性结论——置此标记且
	// Success=false。AverageTime/PacketLoss 在此形态下不承载测
	// 量值：本结构体字段无 omitempty，线上恒序列化为零值，面板
	// 组装层收口时统一覆写为 null 后才出面板——零值不是测量值，
	// 任何直接消费本报文的面都须先判本标记。面板组装层据此
	// （或据老 agent 的 success=true）收口为未验证形态。
	Unverified bool `json:"unverified,omitempty"`
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
	// DB-5：TcpPing 不再抢槽即弃，改入队
	// 等待 worker 执行，队列深度给突发轮次留缓冲。
	maxQueuedTCPPings         = 256
	maxConcurrentReadCommands = 16
	maxQueuedMutationCommands = 256
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
	connecting        bool               // 正在连接状态
	connMutex         sync.Mutex         // 连接状态锁
	aesCrypto         *crypto.AESCrypto  // AES加密器
	seenCommands      *seenCommandIDs    // D1-02: 已处理 requestId 集合（防信封重放）
	tcpPingQueue      chan queuedTCPPing // TcpPing 专用队列 + worker 池（tcp_ping_queue.go）
	readCommandSem    chan struct{}      // 限制只读命令并发，避免诊断请求耗尽资源
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
		seenCommands:   newSeenCommandIDs(),
		tcpPingQueue:   make(chan queuedTCPPing, maxQueuedTCPPings),
		readCommandSem: make(chan struct{}, maxConcurrentReadCommands),
		mutationQueue:  make(chan CommandMessage, maxQueuedMutationCommands),
	}
}

// Start 启动WebSocket报告器
func (w *WebSocketReporter) Start() {
	// 第二代调优靠 sysctl.d 配置与 RPS systemd unit 在开机时自
	// 行重放，agent 启动不需要做调优恢复（第一代的未完成扫描事
	// 务恢复与容器整形重下发已随自动调优一并退役）。
	w.startTCPPingWorkers()
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
	// D1-03: cap inbound frame size (see maxInboundCommandFrameSize).
	conn.SetReadLimit(maxInboundCommandFrameSize)
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

// --- Panel-source download route (panel distribution, phase 1) ---
//
// The panel has served a GitHub mirror endpoint
// (/api/v1/open_api/gh/<full GitHub URL>) all along, built for nodes
// that can reach the panel but not GitHub. The self-upgrade now
// derives ONE extra download route from it — locally, from the
// agent's OWN configured panel address (config.json addr, persisted
// at install time), never from anything the panel sends at upgrade
// time. The downloadable URL set therefore stays a pure function of
// (compile-time repo constant × strictly validated version × the
// agent's own configuration), exactly like the built-in mirror
// routes: S-02 is preserved (see verifyUpgradeHashes for the
// phase-2 hash anchor on this channel).

// panelMirrorPath is the panel GitHub-mirror endpoint path; it must
// match ghMirrorPath in the backend handler.
const panelMirrorPath = "/api/v1/open_api/gh"

// mapToHTTPOriginScheme maps a configured address scheme to the
// HTTP scheme of the panel origin — the counterpart of
// mapToWebSocketScheme, with the same caliber as the traffic
// reporter's mapToHTTPScheme: wss/https → https, ws/http → http,
// anything else (including no explicit scheme) → "" and the caller
// applies the secure https default. No silent downgrade: an
// explicit ws/http address is an operator-confirmed plaintext
// deployment (D1-02), and only then is the asset route plaintext.
func mapToHTTPOriginScheme(scheme string) string {
	switch strings.ToLower(strings.TrimSpace(scheme)) {
	case "wss", "https":
		return "https"
	case "ws", "http":
		return "http"
	default:
		return ""
	}
}

// derivePanelOrigin returns the panel's HTTP origin
// (scheme://host[:port]) for addr, or false when addr carries no
// usable host. Only the origin survives: any path/query in addr is
// dropped by normalizeReporterAddress, so the mirror endpoint must
// (and does) live at the origin root. A non-standard port is kept
// as part of the host.
func derivePanelOrigin(addr string) (string, bool) {
	host, explicitScheme := normalizeReporterAddress(addr)
	if host == "" {
		return "", false
	}
	scheme := mapToHTTPOriginScheme(explicitScheme)
	if scheme == "" {
		scheme = "https"
	}
	return scheme + "://" + host, true
}

// panelSourceRoute builds the panel-source download route for
// addr: a download.Route whose prefix is the panel origin plus the
// mirror path (the route's URL form is then prefix + "/" + the
// locally built GitHub URL, byte-identical in shape to the
// built-in mirror routes). It also returns the panel host (for the
// download client's redirect allowlist extra slot). False when no
// origin can be derived — callers then simply run without the
// panel route, i.e. exactly the pre-panel-source behavior.
func panelSourceRoute(addr string) (download.Route, string, bool) {
	origin, ok := derivePanelOrigin(addr)
	if !ok {
		return download.Route{}, "", false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return download.Route{}, "", false
	}
	return download.Route{Prefix: origin + panelMirrorPath}, strings.ToLower(u.Host), true
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
		// D1-01 同族：拨号 error 文本可能内嵌完整 URL（含 secret），
		// 进日志/聚合前把原文 URL 替换为脱敏版。
		errMsg = strings.ReplaceAll(errMsg, targetURL, sanitizeWebSocketURL(targetURL))
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
	// JSON keys whose values are credentials (same masking policy as
	// the panel side, FIX-A): matched as a case-insensitive substring
	// of the key name.
	sensitiveJSONKeyRe = regexp.MustCompile(`(?i)secret|password|passwd|token|auth|private_?key`)
	// URL query parameters carrying credentials (defense in depth for URLs
	// embedded inside command payload string values).
	sensitiveQueryValueRe = regexp.MustCompile(`(?i)((?:secret|token|password)=)[^&"\s]+`)
)

// redactCommandForLog renders a command payload for the log with every
// credential value masked, keeping the structure (types, names, ids)
// identifiable.
//
// FIX2-C #4（V1-N3）：the previous implementation masked JSON string
// values with a `"[^"]*"` regex. A value containing an escaped quote
// (\" — routine for passwords with a `"` in them, and exactly what
// json.Marshal emits) terminated the match early and left the rest of
// the secret in the log in clear text. Masking now happens on the
// parsed document: unmarshal, replace values of credential-bearing
// keys wholesale, re-marshal. If the payload does not parse, ok is
// false and the caller must not render a detail line at all (the
// type+requestId line from logReceivedCommand still identifies the
// command) — never fall back to rendering the raw payload.
func redactCommandForLog(payload []byte) (string, bool) {
	var doc any
	if err := json.Unmarshal(payload, &doc); err != nil {
		return "", false
	}
	out, err := json.Marshal(maskSensitiveJSONValue(doc))
	if err != nil {
		return "", false
	}
	return string(out), true
}

// maskSensitiveJSONValue walks a parsed JSON document: values under
// credential-bearing keys are replaced wholesale; string values get
// their embedded credential query parameters masked as defense in
// depth (URLs inside command payloads).
func maskSensitiveJSONValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for key, val := range t {
			if sensitiveJSONKeyRe.MatchString(key) {
				t[key] = "***"
				continue
			}
			t[key] = maskSensitiveJSONValue(val)
		}
		return t
	case []any:
		for i, val := range t {
			t[i] = maskSensitiveJSONValue(val)
		}
		return t
	case string:
		return sensitiveQueryValueRe.ReplaceAllString(t, "${1}***")
	default:
		return v
	}
}

// sanitizeWebSocketURL masks credentials in a URL for logging.
// FIX2-C #6（V1-N2）：the previous implementation only masked the
// exact lowercase key "secret" and returned the raw URL when parsing
// failed (fail-open — the opposite of the panel-side logredact
// policy). Now: parameter names match case-insensitively, every
// occurrence is masked, and an unparseable URL degrades to a
// placeholder instead of the original text.
func sanitizeWebSocketURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" {
		return "<invalid-url>"
	}

	if u.User != nil {
		u.User = url.User("***")
	}
	if u.RawQuery != "" {
		pairs := strings.Split(u.RawQuery, "&")
		for i, pair := range pairs {
			key, _, found := strings.Cut(pair, "=")
			if !found {
				continue
			}
			if name, err := url.QueryUnescape(key); err == nil {
				key = name
			}
			switch strings.ToLower(key) {
			case "secret", "token", "password", "passwd", "auth":
				// Keep the original key spelling (incl. case), mask the value.
				pairs[i] = pair[:strings.Index(pair, "=")] + "=***"
			}
		}
		u.RawQuery = strings.Join(pairs, "&")
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
		BootID:           machineBootID(),
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

// maxInboundCommandFrameSize caps a single inbound WebSocket frame on
// the command channel (D1-03). It matches the panel's SetReadLimit
// (10<<20) and maxDecompressedCommandSize: no legitimate command — even
// a large service batch — comes near it, while an unbounded frame from
// a compromised or broken sender must not be able to OOM the agent.
// Gorilla semantics apply: an over-limit frame fails the read and the
// connection drops (the reconnect loop re-establishes it).
const maxInboundCommandFrameSize = 10 << 20

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
// commands remain accepted only when no node secret is configured (the
// local no-secret form); with a secret configured they are rejected
// outright (D1-01, see dispatchReceivedCommand).
func (w *WebSocketReporter) warnPlaintextCommandOnce() {
	if w.plaintextCommandWarned.CompareAndSwap(false, true) {
		fmt.Println("⚠️ 安全警告: 正在通过未加密的明文 WebSocket 通道接收面板命令。明文通道可被链路中间人窃听节点 secret 并注入任意命令（含升级指令）；如非有意部署，请为面板配置 TLS 并使用 wss 地址。")
	}
}

// commandChannelSecretConfigured reports whether this reporter was built
// with a usable node secret. The agent's config loader refuses an empty
// secret, so every real deployment qualifies.
func (w *WebSocketReporter) commandChannelSecretConfigured() bool {
	return w.aesCrypto != nil || w.secret != ""
}

const (
	// commandReplayWindow is how long a claimed requestId stays claimed.
	// An encrypted envelope's timestamp may sit up to
	// maxEncryptedCommandSkew in the future and stays acceptable until
	// maxEncryptedCommandSkew after it, so a claim must live twice the
	// skew to cover the envelope's whole acceptance span.
	commandReplayWindow = 2 * maxEncryptedCommandSkew
	// maxSeenCommandIDs bounds the claim set. Panel command rates are
	// low (mutations serialize, reads cap at maxConcurrentReadCommands),
	// so this only trips under a replay flood; expired claims are pruned
	// first and the oldest claims evicted after that.
	maxSeenCommandIDs = 8192
)

// seenCommandIDs is the bounded claim set behind D1-02 replay rejection.
type seenCommandIDs struct {
	mu     sync.Mutex
	claims map[string]time.Time // requestId -> claim expiry
}

func newSeenCommandIDs() *seenCommandIDs {
	return &seenCommandIDs{claims: make(map[string]time.Time)}
}

// claim records id as seen at now and reports whether it was fresh.
// A duplicate inside the window returns false. Empty ids are always
// fresh: they cannot be told apart, and no supported sender omits the
// requestId.
func (s *seenCommandIDs) claim(id string, now time.Time) bool {
	if id == "" {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if expiry, ok := s.claims[id]; ok && now.Before(expiry) {
		return false
	}
	s.claims[id] = now.Add(commandReplayWindow)
	if len(s.claims) > maxSeenCommandIDs {
		for k, expiry := range s.claims {
			if !now.Before(expiry) {
				delete(s.claims, k)
			}
		}
		if overflow := len(s.claims) - maxSeenCommandIDs*7/8; overflow > 0 {
			// Evict the oldest claims in one batch so a sustained
			// flood does not pay a full scan per claim.
			type claimEntry struct {
				id     string
				expiry time.Time
			}
			entries := make([]claimEntry, 0, len(s.claims))
			for k, expiry := range s.claims {
				entries = append(entries, claimEntry{k, expiry})
			}
			sort.Slice(entries, func(i, j int) bool { return entries[i].expiry.Before(entries[j].expiry) })
			for _, e := range entries[:overflow] {
				delete(s.claims, e.id)
			}
		}
	}
	return true
}

func (s *seenCommandIDs) size() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.claims)
}

// claimCommandRequestID claims a received command's requestId (D1-02).
// The claim set lives on the reporter, so it survives reconnects within
// the process.
func (w *WebSocketReporter) claimCommandRequestID(id string, now time.Time) bool {
	if w.seenCommands == nil {
		// Zero-value reporters (tests, ad-hoc construction) lazily
		// acquire a claim set; the double-check race is benign because
		// NewWebSocketReporter is the production constructor.
		w.seenCommands = newSeenCommandIDs()
	}
	return w.seenCommands.claim(id, now)
}

// dispatchReceivedCommand is the single gate every parsed inbound command
// passes through before dispatch (D1-01). Compatibility basis: every
// panel of this product line encrypts commands whenever the node secret
// is non-empty — the AES envelope exists on both ends since v1.0.0 and
// the panel's encryptor only fails on an empty secret, in which case the
// connection could never have been registered. A plaintext command frame
// reaching a secret-configured agent therefore cannot come from a real
// panel of any supported version: it is an in-path injection or a
// misconfiguration, and is rejected fail-closed with an explicit failure
// receipt (the sender learns immediately instead of timing out). Only
// the no-secret local form keeps accepting plaintext, with the standing
// one-time warning.
func (w *WebSocketReporter) dispatchReceivedCommand(cmd CommandMessage, wasEncrypted bool) {
	// R2-X2-03：已删除旧版 "call" 帧型的静默丢弃分支与零调用的
	// handleCall 死代码——该帧型没有任何现行发送方，删除后它与其余
	// 未知命令同路：经下方加密/重放门后落 routeCommand 兜底，回
	// UnknownCommandResponse 明确回执，不再有永不达处理层的静默
	// 接收面。
	if !wasEncrypted {
		if w.commandChannelSecretConfigured() {
			fmt.Printf("❌ 拒绝明文命令: type=%s requestId=%s（本节点已配置 secret，命令必须走加密信封）\n", cmd.Type, cmd.RequestId)
			w.sendCommandFailure(cmd, "本节点已配置 secret，面板命令必须使用加密信封，明文命令已拒绝执行")
			return
		}
		w.warnPlaintextCommandOnce()
	}
	// D1-02: the timestamp window alone does not stop a verbatim replay
	// of a captured envelope inside the window. Each requestId executes
	// at most once per claim window; a repeat is rejected with an
	// explicit receipt instead of executing again.
	if !w.claimCommandRequestID(cmd.RequestId, time.Now()) {
		fmt.Printf("❌ 拒绝重放命令: type=%s requestId=%s\n", cmd.Type, cmd.RequestId)
		w.sendCommandFailure(cmd, "重复的命令 requestId，疑似信封重放，已拒绝再次执行")
		return
	}
	w.dispatchCommand(cmd)
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
				w.sendErrorResponse("StaleTimestamp", err.Error(), "")
				return
			}
			if w.aesCrypto != nil {
				// 解密数据
				decryptedData, err := w.aesCrypto.Decrypt(encryptedWrapper.Data)
				if err != nil {
					fmt.Printf("❌ 解密失败: %v\n", err)
					w.sendErrorResponse("DecryptError", fmt.Sprintf("解密失败: %v", err), "")
					return
				}
				message = decryptedData
				wasEncrypted = true
			} else {
				fmt.Printf("❌ 收到加密消息但没有加密器\n")
				w.sendErrorResponse("NoDecryptor", "没有可用的解密器", "")
				return
			}
		}
		// 先尝试解析是否是压缩消息
		var compressedMsg struct {
			Type       string          `json:"type"`
			Compressed bool            `json:"compressed"`
			Data       json.RawMessage `json:"data"`
			RequestId  string          `json:"requestId,omitempty"`
			TimeoutMS  int64           `json:"timeoutMs,omitempty"`
		}

		if err := json.Unmarshal(message, &compressedMsg); err == nil && compressedMsg.Compressed {
			// 处理压缩消息
			fmt.Printf("📥 收到压缩消息，正在解压...\n")

			// 解压数据（带大小上限，防 gzip bomb，D1-03）
			decompressedData, err := decompressCommandPayload(compressedMsg.Data)
			if err != nil {
				fmt.Printf("❌ 解压数据失败: %v\n", err)
				w.sendErrorResponse("DecompressError", fmt.Sprintf("解压失败: %v", err), compressedMsg.RequestId)
				return
			}

			// 使用解压后的数据继续处理
			message = decompressedData

			// 构建解压后的命令消息
			var cmdMsg CommandMessage
			cmdMsg.Type = compressedMsg.Type
			cmdMsg.RequestId = compressedMsg.RequestId
			cmdMsg.TimeoutMS = compressedMsg.TimeoutMS
			if err := json.Unmarshal(message, &cmdMsg.Data); err != nil {
				fmt.Printf("❌ 解析解压后的命令数据失败: %v\n", err)
				w.sendErrorResponse("ParseError", fmt.Sprintf("解析命令失败: %v", err), compressedMsg.RequestId)
				return
			}

			w.dispatchReceivedCommand(cmdMsg, wasEncrypted)
		} else {
			// 处理普通消息
			var cmdMsg CommandMessage
			if err := json.Unmarshal(message, &cmdMsg); err != nil {
				fmt.Printf("❌ 解析命令消息失败: %v\n", err)
				w.sendErrorResponse("ParseError", fmt.Sprintf("解析命令失败: %v", err), bestEffortRequestID(message))
				return
			}
			w.dispatchReceivedCommand(cmdMsg, wasEncrypted)
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
	// DB-5：TcpPing 走专用队列（预算 ctx + worker 池），不占只读命令
	// 信号量——一轮探测扇出几十个 TcpPing 时其余只读命令仍可执行。
	if cmd.Type == "TcpPing" {
		w.enqueueTCPPing(cmd)
		return
	}
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
			// D1-06: every read-path command runs under its own budget
			// context instead of the process context, so a command the
			// panel has already given up on does not start (or keep)
			// executing agent-side and get duplicated by a retry.
			ctx, cancel := context.WithTimeout(w.ctx, readCommandBudget(cmd.TimeoutMS))
			defer cancel()
			w.routeCommandSafely(ctx, cmd)
		}()
	case <-w.ctx.Done():
		w.sendCommandFailure(cmd, "Agent is shutting down")
	default:
		w.sendCommandFailure(cmd, "只读命令并发过多，请稍后重试")
	}
}

const (
	// readCommandReplyMargin mirrors tcpPingReplyMargin: stop a little
	// before the panel's own deadline so the refusal (or result) can
	// still reach it.
	readCommandReplyMargin = 500 * time.Millisecond
	minReadCommandBudget   = 500 * time.Millisecond
	// maxReadCommandBudget caps a panel-supplied TimeoutMS for read
	// commands (longer than any panel-side wait; purely a bound
	// against absurd values).
	maxReadCommandBudget = 10 * time.Minute
	// defaultReadCommandBudget applies when the panel transmits no
	// budget — today timeoutMs only rides along with TcpPing. It is
	// deliberately above every panel-side command wait, so no
	// legitimate execution is cut short; it exists so a read command
	// can never run unbounded (e.g. a KernelApply whose caller left
	// minutes ago — see the entry check in routeCommand).
	defaultReadCommandBudget = 5 * time.Minute
)

// readCommandBudget derives one read-path command's budget from the
// panel-supplied TimeoutMS, mirroring tcpPingBudget's clamping shape.
func readCommandBudget(timeoutMS int64) time.Duration {
	if timeoutMS <= 0 {
		return defaultReadCommandBudget
	}
	timeout := time.Duration(timeoutMS)*time.Millisecond - readCommandReplyMargin
	if timeout < minReadCommandBudget {
		return minReadCommandBudget
	}
	if timeout > maxReadCommandBudget {
		return maxReadCommandBudget
	}
	return timeout
}

func (w *WebSocketReporter) runMutationCommands() {
	for {
		select {
		case <-w.ctx.Done():
			return
		case cmd := <-w.mutationQueue:
			w.routeCommandSafely(w.ctx, cmd)
		}
	}
}

func (w *WebSocketReporter) routeCommandSafely(ctx context.Context, cmd CommandMessage) {
	defer func() {
		if recovered := recover(); recovered != nil {
			fmt.Printf("❌ 命令处理 panic: type=%s panic=%v\n%s", cmd.Type, recovered, debug.Stack())
			w.sendCommandFailure(cmd, fmt.Sprintf("命令处理异常: %v", recovered))
		}
	}()
	w.routeCommand(ctx, cmd)
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
		// R3-A01-OPT-01: 原列表尾部的 "reload" 是死分类——
		// routeCommand 无此命令分支、面板从不下发，删去防误接线。
		// R9-06（I-F2-01）：UninstallAgent 此前经只读通道派发，其卸载
		// 协程与在途变更命令全程并发（升级下载中途被调优恢复/内核卸载/
		// 自卸载交错）。纳入串行集后，卸载命令在变更队列里排到在途
		// 变更之后，回执随之延后——面板侧删除漏斗已先行以在途登记挡下
		// 这一时序，此处是 agent 侧的兜底串行化（老面板/重连拦截再下发
		// 时同样有序）；回执超时上面板按未确认登记待卸载，重连时再下
		// 发，与离线口径一致。
		"setprotocol", "setrestartschedule", "upgradeagent", "rollbackagent", "uninstallagent":
		return true
	default:
		return false
	}
}

// logReceivedCommand 记录收到的面板命令（D1-01）：只记 type 与
// requestId。命令全文含服务配置，其中的代理密码/SS 密钥等凭据字段
// 不得落日志（旧实现 fmt.Println 打印命令 JSON 全文）。
func logReceivedCommand(cmd CommandMessage) {
	fmt.Printf("🔔 收到命令: type=%s requestId=%s\n", cmd.Type, cmd.RequestId)
}

// panelRemoteHost returns the remote address of the agent's live
// WebSocket connection to the panel (host:port), or "" when not
// connected. It is the probe's panel-RTT target: a fresh TCP connect
// to this endpoint, not the panel's configured URL (which may be a
// domain or a fallback list) and not the probe call's own duration.
// Caveat: if a proxy sits between agent and panel, this measures to
// the proxy endpoint as seen by the agent; the value stays labelled
// a reference, never a suggested input.
func (w *WebSocketReporter) panelRemoteHost() string {
	w.connMutex.Lock()
	conn := w.conn
	w.connMutex.Unlock()
	if conn == nil {
		return ""
	}
	if addr := conn.RemoteAddr(); addr != nil {
		return addr.String()
	}
	return ""
}

// routeCommand 路由命令到对应的处理函数。ctx 是本次命令的预算上下文：
// 读命令/变更命令传 reporter 的 w.ctx，TcpPing 传队列任务的预算 ctx。
func (w *WebSocketReporter) routeCommand(ctx context.Context, cmd CommandMessage) {
	if isMutationCommand(cmd.Type) {
		unlock := config.LockMutation()
		defer unlock()
		if w.ctx.Err() != nil {
			w.sendCommandFailure(cmd, "Agent is shutting down")
			return
		}
	} else if err := ctx.Err(); err != nil {
		// D1-06: a read-path command whose per-command budget already
		// expired (queued behind the read semaphore, or delivered with
		// a spent panel budget) must not start executing now: the
		// caller has given up, and running it anyway is what makes a
		// later retry double-execute Kernel/Tune side effects.
		fmt.Printf("❌ 拒绝执行预算已耗尽的命令: type=%s requestId=%s err=%v\n", cmd.Type, cmd.RequestId, err)
		w.sendCommandFailure(cmd, fmt.Sprintf("命令执行预算已耗尽，拒绝执行: %v", err))
		return
	}
	// D1-01（FIX-A 与 FIX-H 两路同修，集成取并集）：先按 FIX-A 口径只记
	// type + requestId，再按 FIX-H 口径补一行脱敏渲染供排障辨识结构；
	// 两行都不得出现命令原文——服务配置内含代理密码/SS 密钥等凭据，
	// 旧实现打印命令 JSON 全文会把凭据写进服务 journal。
	logReceivedCommand(cmd)
	// FIX2-C #4（V1-N3）：详情行走真 JSON 解析掩码；渲染失败（理论上
	// 不会发生，输入是刚 Marshal 的命令）时整行不打，只保留上面
	// type+requestId 一行，绝不回退原文。
	if jsonBytes, errs := json.Marshal(cmd); errs == nil {
		if detail, ok := redactCommandForLog(jsonBytes); ok {
			fmt.Println("🔔 命令详情(已脱敏): ", detail)
		}
	}
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

	// TCP Ping 诊断命令（只读，不需要保存配置）。并发由队列 worker
	// 池约束（见 tcp_ping_queue.go），此处不再抢槽。
	case "TcpPing":
		response.Type = "TcpPingResponse"
		var tcpPingResult TcpPingResponse
		tcpPingResult, err = w.handleTcpPing(ctx, cmd.Data)
		response.Data = tcpPingResult
		// needSaveConfig = false (默认值)

	// 节点调优（第二代模型）：TuneStatus 读四面状态，Tune 带动作
	// 应用/回退，TuneProbe 是保留的线路探针（只读）。第一代命令
	// （TuneApply / TuneStart / TuneCancel）已退役，仍在此应答——
	// 由 handleTuneCommandContext 回明确的不支持错误，不落到未知
	// 命令兜底。
	case "Tune", "TuneStatus", "TuneProbe", "TuneApply", "TuneStart", "TuneCancel":
		response.Type = cmd.Type + "Response"
		if cmd.Type == "TuneProbe" {
			// panelRttMs 的取数口径（契约对齐）：前端契约不带面板
			// 地址字段，agent 在此从当前 WebSocket 连接的远端地址
			// 自动补 referenceHost；调用方显式给的优先，不覆盖。
			// 未连接时补不到，探测的 panelRttMs 腿按契约保持可选缺失。
			if m, ok := cmd.Data.(map[string]interface{}); ok {
				if v, has := m["referenceHost"]; !has || strings.TrimSpace(fmt.Sprint(v)) == "" {
					if host := w.panelRemoteHost(); host != "" {
						m["referenceHost"] = host
					}
				}
			}
		}
		response.Data, err = handleTuneCommandContext(ctx, cmd.Type, cmd.Data)

	// UDP Ping 诊断命令（只读，不需要保存配置）
	case "UdpPing":
		var udpPingResult TcpPingResponse
		udpPingResult, err = w.handleUdpPing(cmd.Data)
		response.Type = "UdpPingResponse"
		response.Data = udpPingResult

	// 路由追踪（只读，回程线路识别用）
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

	// 定时重启 agent 服务设置：落成 systemd timer 持久化，
	// 面板离线照常触发（见 restart_schedule.go）。
	case "SetRestartSchedule":
		err = w.handleSetRestartSchedule(cmd.Data)
		response.Type = "SetRestartScheduleResponse"

	// 升级 Agent 命令（异步执行，不需要保存配置）
	case "UpgradeAgent":
		err = w.handleUpgradeAgent(cmd.Data)
		response.Type = "UpgradeAgentResponse"
		// needSaveConfig = false (默认值)

	// 回退 Agent 到旧版本
	case "RollbackAgent":
		response.Data, err = w.handleRollbackAgent(cmd.Data)
		response.Type = "RollbackAgentResponse"
		// needSaveConfig = false (默认值)

	// 面板删除服务器时卸载 Agent（先回复，再异步卸载）；回执
	// data 携带调优恢复预检 warnings（R2F2-D01），与
	// RollbackAgent 的 data 回执同形态。
	case "UninstallAgent":
		response.Data, err = w.handleUninstallAgent()
		response.Type = "UninstallAgentResponse"
		// needSaveConfig = false (默认值)

	// 代理内核（Xray / sing-box）相关命令，由 kernel.Manager 自行串行化
	case "KernelApply", "KernelControl", "KernelStatus":
		response.Type = cmd.Type + "Response"
		response.Data, err = handleKernelCommandContext(ctx, cmd.Type, cmd.Data)

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

	// 读取当前值作为默认（A01-002）：未下发的字段沿用现值。此前
	// 默认一律 0，部分字段下发会把未给出的开关清零并落盘——静默
	// 解除协议屏蔽且重启不恢复，与本函数注释自相矛盾。现值以
	// service 包的内存态为准（启动时由 config.json 加载、每次
	// SetProtocolBlock 更新），与下方落盘的是同一组值。
	httpVal, tlsVal, socksVal := service.GetProtocolBlock()
	// R3-A01-001: 落盘失败回滚运行时需要旧值（见函数尾）。
	oldHTTP, oldTLS, oldSOCKS := httpVal, tlsVal, socksVal

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

	// 设置至 service，全量传递（未提供的值沿用当前值，见上方 A01-002）
	service.SetProtocolBlock(httpVal, tlsVal, socksVal)

	// 同步写入本地 config.json。R3-A01-001: 落盘失败必须回滚运行时
	// ——与 createServices 的 D1-06 同口径（先改运行时、落盘失败回滚）。
	// 此前不回滚：命令报失败但新屏蔽值已在运行时生效、文件仍是旧值，
	// 面板/运行时/落盘三态分裂，直到重启才以静默撤销的方式「自愈」。
	if err := updateLocalConfigJSON(httpVal, tlsVal, socksVal); err != nil {
		service.SetProtocolBlock(oldHTTP, oldTLS, oldSOCKS)
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

// KU4 (1.7.7): the self-upgrade no longer resolves ONE proxy from
// the panel's proxyUrl or the local config.json "proxy" field. Both
// were single static routes chosen out-of-band — the failure mode
// behind the recurring "upgrade never executes" reports (one sick
// mirror burned the whole budget before the direct fallback ran).
// Routing is now probe-selected at download time from the built-in
// candidate set (download.MirrorPrefixes + direct): see
// probeUpgradeRoute and download.Plan. The config.json field
// itself is left on disk untouched (updateLocalConfigJSON preserves
// unknown fields); it simply has no consumer anymore.

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

// agentServiceNameFor derives the init service name of an install
// layout from its binary path: install.sh names the unit (and the
// OpenRC init script) after the binary it installs, in both layout
// generations — flmx_agent for current installs, flux_agent for
// not-yet-migrated legacy installs. Commands for a layout must
// address that layout's own service: a legacy node upgraded in
// place by the panel still runs under the flux_agent unit until
// install.sh migrates it. Paths outside a recognized layout fall
// back to the current generation's name.
func agentServiceNameFor(binaryPath string) string {
	if name := filepath.Base(binaryPath); isAgentInstallName(name) {
		return name
	}
	return agentInstallName
}

// resolveAgentBinaryPath locates the script-form install layout
// this process belongs to, for the panel-driven upgrade/rollback
// paths (they replace the binary in place and never move layouts):
//
//  1. The running executable itself, when it sits in a recognized
//     layout (<name>/<name> with a current or legacy install
//     name) — exact for both generations, migrated or not.
//  2. The first recognized layout present on disk, current name
//     first — covers launches through a copied/symlinked binary.
//  3. The current generation's path as the default: new installs
//     are flmx-only, and a missing binary there degrades to the
//     same "nothing to back up" behavior the fixed path had.
func resolveAgentBinaryPath() string {
	if exe, err := uninstallBinaryPath(); err == nil {
		if name := filepath.Base(exe); isAgentInstallName(name) && filepath.Base(filepath.Dir(exe)) == name {
			return exe
		}
	}
	for _, name := range []string{agentInstallName, legacyAgentInstallName} {
		p := filepath.Join("/etc", name, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return filepath.Join("/etc", agentInstallName, agentInstallName)
}

// serviceManagerCommandsFor returns the stop/start/is-active shell
// commands for the manager and one install layout's service name
// (see agentServiceNameFor), spelled exactly as install.sh spells
// them. Callers must pass the service name of the layout they act
// on: layouts that were never migrated still run the legacy service
// name, and commands against the wrong name silently do nothing.
func serviceManagerCommandsFor(manager serviceManager, serviceName string) (stop, start, active string, err error) {
	switch manager {
	case serviceManagerSystemd:
		return "systemctl stop " + serviceName, "systemctl start " + serviceName, "systemctl is-active --quiet " + serviceName, nil
	case serviceManagerOpenRC:
		return "rc-service " + serviceName + " stop", "rc-service " + serviceName + " start", "rc-service " + serviceName + " status >/dev/null 2>&1", nil
	default:
		return "", "", "", fmt.Errorf("不支持的服务管理器: %q", string(manager))
	}
}

// serviceManagerPIDCommandFor returns the shell command printing
// one install layout's service main PID (empty output or 0 when not
// running; see agentServiceNameFor). The OpenRC path follows
// install.sh's init script, which pins
// pidfile="/run/${RC_SVCNAME}.pid".
func serviceManagerPIDCommandFor(manager serviceManager, serviceName string) (string, error) {
	switch manager {
	case serviceManagerSystemd:
		return "systemctl show -p MainPID --value " + serviceName, nil
	case serviceManagerOpenRC:
		return "cat /run/" + serviceName + ".pid 2>/dev/null", nil
	default:
		return "", fmt.Errorf("不支持的服务管理器: %q", string(manager))
	}
}

// agentHealthCheckFunctionFor renders a POSIX sh function
// flmx_agent_health_ok implementing the R4-N2 liveness rule: sample
// (is-active + main PID) twice, sampleGapSec apart; the service counts
// as alive only if BOTH samples are active and the PID is non-empty,
// non-zero and identical across samples. A single sample is not proof
// of life: a binary that exits ~1s after start still shows active in
// systemd's flap window, which previously made the upgrade script log
// "restart ok" and skip the rollback while the service died for good
// moments later.
func agentHealthCheckFunctionFor(activeCmd, pidCmd string, sampleGapSec int) string {
	return fmt.Sprintf("flmx_agent_health_ok() { p1=$(%s); if ! %s; then return 1; fi; sleep %d; p2=$(%s); if ! %s; then return 1; fi; [ -n \"$p1\" ] && [ \"$p1\" != \"0\" ] && [ \"$p1\" = \"$p2\" ]; }",
		pidCmd, activeCmd, sampleGapSec, pidCmd, activeCmd)
}

// agentHealthCheckFunction resolves one install layout's commands
// and renders flmx_agent_health_ok with the production sampling gap.
func agentHealthCheckFunction(manager serviceManager, serviceName string) (string, error) {
	_, _, active, err := serviceManagerCommandsFor(manager, serviceName)
	if err != nil {
		return "", err
	}
	pidCmd, err := serviceManagerPIDCommandFor(manager, serviceName)
	if err != nil {
		return "", err
	}
	return agentHealthCheckFunctionFor(active, pidCmd, 2), nil
}

// agentControlScriptCommand returns the detached launcher for a control
// script. systemd runs it in a transient unit (systemd-run) so that
// stopping the agent service cannot kill the script via its own cgroup;
// OpenRC has no cgroups, so a new session (setsid) provides the same
// escape.
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

// scriptStartCommand wraps the manager start command for the detached
// upgrade/rollback scripts. Under systemd a binary that exits ~1s after
// start (the R4-N2 flap shape) burns through the unit's start-rate
// limit within seconds and leaves the unit in the failed state; a bare
// "systemctl start" is then refused with "Start request repeated too
// quickly", so the rollback restored the good binary yet never brought
// the service back (release-gate re-verification of R4-N2). Clear the
// failed state before every scripted start. OpenRC has no equivalent
// limiter, so its start command is used as-is.
func scriptStartCommand(manager serviceManager, serviceName, start string) string {
	if manager == serviceManagerSystemd {
		return "systemctl reset-failed " + serviceName + " 2>/dev/null; " + start
	}
	return start
}

// legacyGostConfigPath is the legacy pre-FLMX gost agent's connection
// config; its addr field is the ownership evidence for the cleanup
// guard below.
const legacyGostConfigPath = "/etc/gost/config.json"

// readAgentConfigAddr extracts the addr field from an agent
// config.json. The value comes back JSON-decoded, so it compares
// verbatim against a configured panel address — the same
// normalization install.sh's agent_config_addr applies (JSON
// unescape) before its verbatim comparison. A missing or unreadable
// file, malformed JSON, or an absent addr yields "" (ownership
// undecidable). A package-level variable so tests can stub the
// filesystem reads.
var readAgentConfigAddr = func(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var cfg struct {
		Addr string `json:"addr"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return ""
	}
	return cfg.Addr
}

// legacyGostCleanupOwnershipConfirmed is the agent-side twin of
// install.sh's legacy_gost_cleanup_ownership_confirmed (C4-F01). The
// legacy gost footprint — gost.service pointing at /etc/gost, the
// gost binary — is shape-identical for this panel's own pre-rename
// installs and for another panel's live agent, so the fingerprint
// cannot prove ownership, while the cleanup stops the service and
// deletes its files. Cleanup is confirmed only when the legacy
// config's addr is non-empty and verbatim-equal to this agent's own
// configured panel addr (the address this agent connects to — the
// panel that issued the upgrade command now running). Any other
// outcome — a different addr, or either side unreadable — leaves the
// legacy install untouched.
//
// Evidence-surface difference from the script side: install.sh also
// accepts the address passed to that install run, and offers the
// FLMX_AGENT_TAKEOVER=1 and interactive-confirmation escapes. A
// remote upgrade command has no operator present and no second
// address, so neither exists here; an operator who confirms
// ownership runs the panel install/update script on the node, whose
// guard has them.
func legacyGostCleanupOwnershipConfirmed(ownAddr, legacyAddr string) bool {
	return ownAddr != "" && legacyAddr != "" && ownAddr == legacyAddr
}

// legacyGostCleanupSegment renders the legacy gost cleanup portion
// of the systemd upgrade restart script. The service-file
// fingerprint scan (WorkingDirectory=/etc/gost plus the ExecStart
// shapes, the /usr/local/bin form additionally requiring both legacy
// config files) is unchanged from the pre-guard morphology. What a
// fingerprint match triggers is gated on ownership:
//   - confirmed: stop/disable gost.service and delete its files,
//     exactly as before the guard;
//   - not confirmed: stop and delete nothing, and echo a skip line
//     naming the legacy addr found (or that it could not be read).
//     The restart script redirects all output to agent-upgrade.log,
//     so the skip is on the record for this command.
func legacyGostCleanupSegment(ownAddr, legacyAddr string) string {
	scan := "legacy_service='' && for service_file in /etc/systemd/system/gost.service /lib/systemd/system/gost.service /usr/lib/systemd/system/gost.service; do if [ -f \"$service_file\" ] && grep -Fq \"WorkingDirectory=/etc/gost\" \"$service_file\" && (grep -Fq \"ExecStart=/etc/gost/gost\" \"$service_file\" || (grep -Fq \"ExecStart=/usr/local/bin/gost\" \"$service_file\" && [ -f /etc/gost/config.json ] && [ -f /etc/gost/gost.json ])); then legacy_service=\"$service_file\"; break; fi; done; "
	if legacyGostCleanupOwnershipConfirmed(ownAddr, legacyAddr) {
		return scan + "if [ -n \"$legacy_service\" ]; then (systemctl stop gost 2>/dev/null || true) && (systemctl disable gost 2>/dev/null || true) && rm -f /usr/local/bin/gost /etc/gost/gost \"$legacy_service\" && (systemctl daemon-reload 2>/dev/null || true); fi; "
	}
	shownLegacy := legacyAddr
	if shownLegacy == "" {
		shownLegacy = "无法读取"
	}
	shownOwn := ownAddr
	if shownOwn == "" {
		shownOwn = "无法读取"
	}
	msg := fmt.Sprintf("⚠️ 检测到旧式 gost 形态的 agent（连接地址：%s），无法确认它属于本面板（本 agent 的面板地址：%s），已跳过旧式 gost 清理：未停止其服务、未删除其任何文件。如确认它属于本面板，请在本机运行面板安装/更新脚本处理。", shownLegacy, shownOwn)
	return scan + "if [ -n \"$legacy_service\" ]; then echo " + tuneShellQuote(msg) + "; fi; "
}

// agentUpgradeLogMaxBytes 是 agent-upgrade.log 的大小封顶（字节）：
// GARBAGE-AUDIT A2——该日志此前只 append、无 truncate/rotate/上限，
// 每次升级/回滚 append 一段，口径上真无界（增速极低、KB 级/年）。
const agentUpgradeLogMaxBytes = 1048576

// agentUpgradeLogCapSegment renders the size cap prepended to the
// upgrade/rollback scripts: before the script redirects its output
// into agent-upgrade.log, a log already past the cap is truncated
// and reopened empty. The log is a post-mortem trail of the most
// recent operations, so whole-file truncation past the cap keeps
// the newest record while bounding the file. Size probing is best
// effort (wc failure reads as 0 = under cap) and truncation failure
// never blocks the upgrade script itself.
func agentUpgradeLogCapSegment(logPath string) string {
	return fmt.Sprintf("if [ -f %s ]; then _flmx_log_size=$(wc -c < %s 2>/dev/null || echo 0); case \"$_flmx_log_size\" in ''|*[!0-9]*) _flmx_log_size=0 ;; esac; if [ \"$_flmx_log_size\" -gt %d ]; then : > %s || true; fi; fi; ", logPath, logPath, agentUpgradeLogMaxBytes, logPath)
}

// buildAgentRestartScriptFor builds the self-contained upgrade restart
// script: stop, swap the binary in, start, then verify the service
// actually came up — if it did not, restore the backup copy and start
// again, all locally on the node without any panel round-trip
// (A11-132-02). Progress is appended to agent-upgrade.log next to the
// binary for post-mortem diagnosis.
func buildAgentRestartScriptFor(manager serviceManager, tmpPath, binaryPath, backupPath string) (string, error) {
	serviceName := agentServiceNameFor(binaryPath)
	stop, start, _, err := serviceManagerCommandsFor(manager, serviceName)
	if err != nil {
		return "", err
	}
	start = scriptStartCommand(manager, serviceName, start)
	healthFn, err := agentHealthCheckFunction(manager, serviceName)
	if err != nil {
		return "", err
	}
	logPath := filepath.Join(filepath.Dir(binaryPath), "agent-upgrade.log")

	var b strings.Builder
	fmt.Fprintf(&b, "%sexec >>%s 2>&1; echo \"=== agent upgrade restart $(date) ===\"; %s; sleep 1; %s; ", agentUpgradeLogCapSegment(logPath), logPath, healthFn, stop)
	if manager == serviceManagerSystemd {
		// Legacy pre-FLMX gost cleanup (systemd units only), gated
		// on the C4-F01 ownership guard: the fingerprint alone does
		// not prove the legacy install belongs to this panel, so the
		// segment is rendered from the two config addresses.
		ownAddr := readAgentConfigAddr(filepath.Join(filepath.Dir(binaryPath), "config.json"))
		b.WriteString(legacyGostCleanupSegment(ownAddr, readAgentConfigAddr(legacyGostConfigPath)))
	}
	// R4-N2：判活走 flmx_agent_health_ok 的两次采样（PID 稳定），
	// 不再单次 is-active 采样——flap 窗口内的假活会跳过自动回滚。
	// F-RM-01: the swap itself is guarded. An unchecked mv let a
	// missing staging file (handler-side cleanup racing the script)
	// flow into the health check on the OLD binary and log
	// 'agent upgrade restart ok' — a false success. On swap failure
	// the script removes any staging leftover (it owns the file
	// after handoff), restarts the current binary, and logs the
	// failure by name; 'restart ok' is only reachable past a
	// successful mv.
	// L2-02 (round-6): the auto-rollback leg below gets the same
	// treatment FRM4 gave the manual rollback script. It used to
	// cp the backup straight onto the binary path, unchecked: a
	// copy dying midway truncated the NEW binary, a copy against
	// the still-running new binary could fail ETXTBSY, and either
	// way the start + health check ran on whatever was in place
	// and could log 'rollback succeeded' for a rollback that never
	// happened. The leg now stops the service first (the failed
	// health check does not prove the new process is gone), stages
	// the copy in a unique mktemp sibling of the binary, proves it
	// byte-identical with cmp, and only then mv's it into place;
	// any failure before the mv leaves the new binary untouched,
	// is logged by name, and 'rollback succeeded' stays reachable
	// only past the staged swap.
	rollbackTmpTemplate := filepath.Join(filepath.Dir(binaryPath), "."+serviceName+".upgrade-rollback-XXXXXX")
	fmt.Fprintf(&b, "if mv %s %s; then %s; sleep 3; if flmx_agent_health_ok; then echo 'agent upgrade restart ok'; else echo 'new agent failed health check, rolling back'; if [ -f %s ]; then %s; sleep 1; rollback_tmp=; if rollback_tmp=$(mktemp %s) && cp %s \"$rollback_tmp\" && chmod 755 \"$rollback_tmp\" && cmp %s \"$rollback_tmp\" && mv \"$rollback_tmp\" %s; then %s; sleep 2; if flmx_agent_health_ok; then echo 'rollback succeeded'; else echo 'rollback start failed'; fi; else echo 'agent upgrade rollback swap failed: backup binary could not be staged and swapped into place intact, keeping new version'; if [ -n \"$rollback_tmp\" ]; then rm -f \"$rollback_tmp\"; fi; %s; sleep 2; if flmx_agent_health_ok; then echo 'agent upgrade rollback aborted: new version still running'; else echo 'agent upgrade rollback aborted and new version failed to restart'; fi; fi; else echo 'no backup available for rollback'; fi; fi; else echo 'agent upgrade swap failed: staged binary could not be moved into place, keeping current version'; rm -f %s; %s; sleep 2; if flmx_agent_health_ok; then echo 'agent upgrade aborted: previous version still running'; else echo 'agent upgrade aborted and previous version failed to restart'; fi; fi",
		tmpPath, binaryPath, start, backupPath, stop, rollbackTmpTemplate, backupPath, backupPath, binaryPath, start, start, tmpPath, start)
	return b.String(), nil
}

// buildAgentRollbackScriptFor builds the manual rollback script with the
// same init abstraction as the upgrade path.
func buildAgentRollbackScriptFor(manager serviceManager, backupPath, binaryPath string) (string, error) {
	serviceName := agentServiceNameFor(binaryPath)
	stop, start, _, err := serviceManagerCommandsFor(manager, serviceName)
	if err != nil {
		return "", err
	}
	start = scriptStartCommand(manager, serviceName, start)
	healthFn, err := agentHealthCheckFunction(manager, serviceName)
	if err != nil {
		return "", err
	}
	logPath := filepath.Join(filepath.Dir(binaryPath), "agent-upgrade.log")
	tmpTemplate := filepath.Join(filepath.Dir(binaryPath), "."+serviceName+".rollback-XXXXXX")
	// 判活与升级路径同口径（R4-N2 两次采样），避免 'rollback restart ok'
	// 在 flap 窗口内误报。
	// FRM3: the copy itself is guarded, same caliber as the F-RM-01
	// upgrade swap. The handler stats the backup before launching,
	// but the script copies seconds later (after sleep + service
	// stop); an unchecked cp let a vanished or unreadable backup
	// flow into the health check on the CURRENT binary and log
	// 'rollback restart ok' — a false success for a rollback that
	// never happened. On copy failure the script restarts the
	// current binary and logs the failure by name; 'rollback
	// restart ok' is only reachable past a successful cp.
	// FRM4: the guarded copy lands atomically, the same shape as
	// the upgrade path's staged mv. FRM3's cp wrote straight onto
	// the binary path, so a copy dying midway (e.g. disk full)
	// left the CURRENT binary truncated: the failure branch could
	// name the failure, but the node was already bricked. The
	// script now stages the copy in a unique mktemp sibling of the
	// binary (same directory, so the final mv is a same-filesystem
	// atomic rename), proves the staged copy byte-identical to the
	// backup with cmp — POSIX and present in both coreutils and
	// BusyBox, unlike sha256sum, and strictly stronger than a size
	// compare — and only then mv's it into place. Any failure
	// before the mv leaves the current binary untouched, and the
	// script removes the temp file it created. mktemp lands the
	// file 0600 and cp onto an existing file keeps that mode, so
	// the staged copy is chmod'ed back to 755 before the swap.
	return fmt.Sprintf("%sexec >>%s 2>&1; echo \"=== agent rollback $(date) ===\"; %s; sleep 1; %s; rollback_tmp=; if rollback_tmp=$(mktemp %s) && cp %s \"$rollback_tmp\" && chmod 755 \"$rollback_tmp\" && cmp %s \"$rollback_tmp\" && mv \"$rollback_tmp\" %s; then %s; sleep 2; if flmx_agent_health_ok; then echo 'rollback restart ok'; else echo 'rollback start failed'; fi; else echo 'agent rollback swap failed: backup binary could not be staged and swapped into place intact, keeping current version'; if [ -n \"$rollback_tmp\" ]; then rm -f \"$rollback_tmp\"; fi; %s; sleep 2; if flmx_agent_health_ok; then echo 'agent rollback aborted: current version still running'; else echo 'agent rollback aborted and current version failed to restart'; fi; fi",
		agentUpgradeLogCapSegment(logPath), logPath, healthFn, stop, tmpTemplate, backupPath, backupPath, binaryPath, start, start), nil
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

// --- Rollback backup pairing (R6-05) ---
//
// The panel upgrade path and the install.sh script update path both
// replace the agent binary, but only the panel path maintains the
// rollback backup (<binary>.old): a script update replaces the
// binary and deletes its own .bak on success, so .old silently
// becomes one generation staler than the rollback handler assumes
// (panel upgrade A->B, script update B->C, panel rollback lands on
// A, skipping B) — and the rollback receipt never named the landing
// version. The pairing record written next to the backup at
// panel-upgrade time makes the two paths mutually aware: it pins
// the backup's content hash and the hash + version of the binary
// installed alongside it. At rollback time the current binary must
// still be that paired install; anything else means another path
// replaced the binary in between, and the rollback is refused with
// every version named instead of silently crossing a generation.
// Backups written before this record existed carry no pairing:
// they still roll back (the fleet's existing backups must not be
// stranded), the receipt names the landing version, and the
// generation is reported as unverified.
type agentBackupMeta struct {
	BackupSHA256     string `json:"backupSha256"`
	InstalledVersion string `json:"installedVersion"`
	InstalledSHA256  string `json:"installedSha256"`
}

func agentBackupMetaPath(backupPath string) string { return backupPath + ".meta" }

func writeAgentBackupMeta(backupPath string, meta *agentBackupMeta) error {
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return writeFileAtomic(agentBackupMetaPath(backupPath), data, 0644)
}

// readAgentBackupMeta returns nil when no usable pairing record
// exists (legacy backup, unreadable or malformed file): absence of
// a record is not an error, it only downgrades the generation
// verdict to unverified.
func readAgentBackupMeta(backupPath string) *agentBackupMeta {
	data, err := os.ReadFile(agentBackupMetaPath(backupPath))
	if err != nil {
		return nil
	}
	var meta agentBackupMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil
	}
	if meta.BackupSHA256 == "" || meta.InstalledSHA256 == "" {
		return nil
	}
	return &meta
}

func hashFileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// probeAgentBinaryVersion runs path -V and parses the version out
// of the "gost <version> (<go version> <os>/<arch>)" banner the
// agent prints for -V. The upgrade smoke test only checks
// executability; the rollback pre-flight also needs the version so
// the receipt can name the landing point.
func probeAgentBinaryVersion(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "-V").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s -V 执行失败: %v, 输出: %s", path, err, strings.TrimSpace(string(out)))
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 || fields[0] != "gost" {
		return "", fmt.Errorf("%s -V 输出无法解析: %s", path, strings.TrimSpace(string(out)))
	}
	return fields[1], nil
}

// Rollback generations reported in the RollbackAgent response data.
const (
	// rollbackGenerationPrevious: the backup is the immediately
	// previous generation, verified against the pairing record.
	rollbackGenerationPrevious = "previous"
	// rollbackGenerationLegacy: the backup predates pairing
	// records; its generation is unverified.
	rollbackGenerationLegacy = "legacy-unknown"
)

type agentRollbackPlan struct {
	FromVersion string
	ToVersion   string
	Generation  string
}

func displayVersion(v string) string {
	if v == "" {
		return "未知"
	}
	return v
}

// planAgentRollback is the pre-flight for a manual panel rollback:
// it decides, before any script is launched, whether the backup at
// backupPath may replace the binary at binaryPath, and what the
// receipt must name. See agentBackupMeta for the pairing semantics.
func planAgentRollback(binaryPath, backupPath string) (*agentRollbackPlan, error) {
	if _, err := os.Stat(backupPath); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("没有可用的备份文件，无法回退")
		}
		return nil, fmt.Errorf("检查备份文件失败: %v", err)
	}
	// The landing version must be nameable in the receipt, and a
	// backup that cannot execute -V would leave the node running a
	// binary that cannot start (the rollback script has no second
	// fallback), so an unverifiable backup refuses up front — the
	// same caliber as the upgrade path's pre-swap smoke test.
	toVersion, err := probeAgentBinaryVersion(backupPath)
	if err != nil {
		return nil, fmt.Errorf("回退备份校验失败，拒绝回退: %v", err)
	}
	plan := &agentRollbackPlan{ToVersion: toVersion, Generation: rollbackGenerationLegacy}
	if v, err := probeAgentBinaryVersion(binaryPath); err == nil {
		plan.FromVersion = v
	}
	backupHash, err := hashFileSHA256(backupPath)
	if err != nil {
		return nil, fmt.Errorf("读取回退备份失败: %v", err)
	}
	currentHash, err := hashFileSHA256(binaryPath)
	if err != nil {
		return nil, fmt.Errorf("读取当前二进制失败: %v", err)
	}
	if currentHash == backupHash {
		return nil, fmt.Errorf("当前二进制与回退备份相同（版本 %s），已在备份版本，无需回退", toVersion)
	}
	if meta := readAgentBackupMeta(backupPath); meta != nil {
		if backupHash != meta.BackupSHA256 {
			return nil, fmt.Errorf("回退备份与配对记录不一致（备份在生成后被替换），拒绝回退；当前版本 %s，备份版本 %s", displayVersion(plan.FromVersion), toVersion)
		}
		if currentHash != meta.InstalledSHA256 {
			return nil, fmt.Errorf("回退备份不是紧邻上一代：该备份生成于面板升级到 %s 时，当前二进制（版本 %s）其后已被其他方式更新，回退将跨过一代直接回到 %s，已拒绝；如需回到 %s，请用面板的节点安装命令重新安装该版本", displayVersion(meta.InstalledVersion), displayVersion(plan.FromVersion), toVersion, toVersion)
		}
		plan.Generation = rollbackGenerationPrevious
	}
	return plan, nil
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

func upgradeGET(ctx context.Context, client *http.Client, rawURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	return client.Do(req)
}

// R8-T-09: the whole upgrade download phase — binary GET, checksum
// GET, and any proxy->direct fallback retries — shares ONE budget,
// mirroring x/kernel's downloadTimeout for kernel installs. The
// previous shape set Timeout: 60s on the HTTP client: a
// whole-request wall counted from byte zero (it spans the body
// read), so a link slow enough that the ~10MB binary needs >60s in
// a single GET could never upgrade, however patient the panel's
// 300s upgradeTimeout is. 4 minutes keeps the download phase inside
// the panel's patience with room for verify/install afterwards,
// exactly the kernel face's arithmetic (240s < 300s). A var so
// tests can shrink it.
var upgradeDownloadTimeout = 4 * time.Minute

// upgradeDownloadHeaderTimeout bounds only the wait for response
// headers on each attempt. A peer that never sends headers is
// stuck, not slow: failing the attempt at this cap lets the J-02
// fallback spend the remaining shared budget on the direct URL
// instead of starving behind a hung proxy. Body transfer is NOT
// bounded by this value; it runs on upgradeDownloadTimeout. A var
// so tests can shrink it.
var upgradeDownloadHeaderTimeout = 60 * time.Second

var upgradeLookupIP = func(ctx context.Context, host string) ([]net.IP, error) {
	return net.DefaultResolver.LookupIP(ctx, "ip", host)
}

// judgeUpgradeHost resolves host and judges every answer for the
// self-upgrade download client (F2 caliber, shared with the
// kernel fetch face in x/kernel/install.go): hardOnly applies
// just the hard-deny layer (entry / same-origin hops); otherwise
// the full non-public judgment applies (cross-host hops).
func judgeUpgradeHost(ctx context.Context, host string, hardOnly bool) ([]net.IP, error) {
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		var err error
		ips, err = upgradeLookupIP(ctx, host)
		if err != nil || len(ips) == 0 {
			return nil, fmt.Errorf("下载地址无法解析，已拒绝: %s", host)
		}
	}
	for _, ip := range ips {
		if bypass.IsHardDenyIP(ip) {
			return nil, fmt.Errorf("下载地址指向云元数据服务，已拒绝: %s", host)
		}
		if !hardOnly && bypass.IsNonPublicIP(ip) {
			return nil, fmt.Errorf("下载地址指向非公网地址，已拒绝: %s", host)
		}
	}
	return ips, nil
}

// upgradeDialFullContextKey carries the dial-time judgment layer
// for one request of the upgrade client: true = full non-public
// judgment (cross-host redirect hop), false/absent = entry layer
// (hard-deny only). CheckRedirect stamps it per hop (round-5
// L2-07/L2-15: the upgrade client used to have NO dial judgment
// at all — a hostname allowlist over the default transport, so
// ProxyFromEnvironment applied and any allowlisted name could
// resolve or redirect to a private address and be dialed).
type upgradeDialFullContextKey struct{}

// upgradeDialContext pins the upgrade client's dial: re-resolve,
// re-judge at the layer the request's hop was judged at, dial a
// validated IP.
func upgradeDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	hardOnly := ctx.Value(upgradeDialFullContextKey{}) != true
	ips, err := judgeUpgradeHost(ctx, host, hardOnly)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: 30 * time.Second}
	var lastErr error
	for _, ip := range ips {
		dialIP := ip
		if ip4 := ip.To4(); ip4 != nil {
			dialIP = ip4
		}
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(dialIP.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// newUpgradeDownloadClient builds the S-02 download client for
// handleUpgradeAgent. The redirect allowlist covers GitHub hosts
// plus the built-in mirror candidates' own hosts (KU4: downloads
// may ride any candidate route, and a mirror may redirect within
// itself); proxyHost remains as an optional extra allowed host for
// compatibility with existing callers/tests. There is deliberately
// no client-level Timeout: the overall deadline lives in the
// caller's context (see upgradeDownloadTimeout) and per-attempt
// header patience in the transport, so no per-GET wall can kill a
// slow body transfer mid-flight (R8-T-09).
//
// F2 outbound policy (round-5 L2-07), aligned with the kernel
// fetch face: the transport never rides an environment proxy and
// dials pinned, re-judged IPs (upgradeDialContext); redirect hops
// additionally keep https from downgrading, are judged on the
// hostname actually dialed (an explicit :443 no longer breaks the
// allowlist comparison), and — beyond the host allowlist — a hop
// back to the entry's own host is judged at the entry layer
// (hard-deny only, so the privately addressed panel route's own
// redirects keep working) while every cross-host hop gets the
// full non-public judgment at BOTH the hop check and the dial.
func newUpgradeDownloadClient(proxyHost string) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil // pinned dialing is meaningless via a proxy
	tr.DialContext = upgradeDialContext
	tr.ResponseHeaderTimeout = upgradeDownloadHeaderTimeout
	mirrorHosts := download.MirrorHosts()
	return &http.Client{
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("S-02: 重定向次数过多")
			}
			// https preservation: once any hop in the chain was
			// https, no later hop may drop to http.
			for _, v := range via {
				if v.URL.Scheme == "https" && req.URL.Scheme != "https" {
					return fmt.Errorf("S-02: 重定向降级到 http，已拒绝: %s", req.URL.Host)
				}
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("S-02: 重定向 scheme 不支持，已拒绝: %s", req.URL.Scheme)
			}
			// 只允许 GitHub 官方域名：github.com 及其子域名、资产 CDN
			// githubusercontent.com 及其子域名（release 下载必经此域名）；
			// 另允许内置镜像候选自身域名（镜像站内跳转）。比较一律
			// 用 Hostname()（不带端口）：此前拿含端口的 URL.Host 比
			// 对，github.com:443 形态会被误拒（L2-20 agent 面）。
			hostname := strings.ToLower(req.URL.Hostname())
			hostport := strings.ToLower(req.URL.Host)
			allowed := isGitHubDownloadHost(hostname) || (proxyHost != "" && hostport == proxyHost)
			if !allowed {
				for _, mh := range mirrorHosts {
					if hostname == mh {
						allowed = true
						break
					}
				}
			}
			if !allowed {
				return fmt.Errorf("S-02: 重定向到非 GitHub 域名被拒绝: %s", req.URL.Host)
			}
			if len(via) > 0 && strings.EqualFold(req.URL.Host, via[0].URL.Host) {
				if _, err := judgeUpgradeHost(req.Context(), hostname, true); err != nil {
					return err
				}
				*req = *req.WithContext(context.WithValue(req.Context(), upgradeDialFullContextKey{}, false))
				return nil
			}
			if _, err := judgeUpgradeHost(req.Context(), hostname, false); err != nil {
				return err
			}
			*req = *req.WithContext(context.WithValue(req.Context(), upgradeDialFullContextKey{}, true))
			return nil
		},
	}
}

// isSHA256Hex reports whether s is a full-length hex SHA-256.
func isSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// verifyUpgradeHashes is the self-upgrade integrity gate (S-02).
// panelExpected is the expected SHA-256 the panel attached to the
// upgrade command (expectedSha256, panel-source phase 2): it
// arrives over the authenticated, AES-encrypted control channel,
// so when present it is the trust anchor — a download route (even
// the panel's own HTTP cache) serving a matched fake binary +
// fake .sha256 pair still fails here. downloadedChecksum is the
// .sha256 fetched over the download routes; it stays mandatory
// (its leg failing aborts the upgrade in the caller, unchanged)
// and acts as the cross-check / sole anchor when panelExpected is
// absent (old panel). The panel value is a hash VALUE, never a
// URL — the request struct's idle ChecksumURL field stays
// unconsumed for exactly that reason.
func verifyUpgradeHashes(panelExpected, downloadedChecksum, actual string) error {
	if panelExpected != "" && !strings.EqualFold(panelExpected, actual) {
		return fmt.Errorf("S-02: 校验失败: 实际哈希与面板下发的期望校验值(expectedSha256)不符: 期望 %s, 实际 %s", panelExpected, actual)
	}
	if !strings.EqualFold(downloadedChecksum, actual) {
		return fmt.Errorf("S-02: 校验失败: 期望 %s, 实际 %s", downloadedChecksum, actual)
	}
	return nil
}

// probeUpgradeRoute is the route admission probe for the
// self-upgrade (KU4): a candidate counts as alive ONLY if it
// delivers the release's real .sha256 file with a parseable
// SHA-256 in it. A mirror that answers its landing page (or any
// other 200 junk) is rejected, so fake-alive proxies never enter
// the ranking. The probe target is the checksum URL in direct
// form; the route plan builds each candidate's form from it.
func probeUpgradeRoute(client *http.Client) download.ProbeFunc {
	return func(ctx context.Context, rawURL string) error {
		resp, err := upgradeGET(ctx, client, rawURL)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("HTTP状态码: %d", resp.StatusCode)
		}
		hash, err := readAgentChecksum(resp.Body)
		if err != nil {
			return fmt.Errorf("校验文件不可解析: %v", err)
		}
		if !isSHA256Hex(hash) {
			return fmt.Errorf("校验文件内容不是合法 SHA256")
		}
		return nil
	}
}

// describeUpgradeRoutes renders the probe ranking for the agent
// log and the panel progress message, e.g.
// "gcode.hostcentral.cc(38ms) > 直连(120ms) > ghfast.top(210ms)".
func describeUpgradeRoutes(routes []download.Route) string {
	parts := make([]string, 0, len(routes))
	for _, r := range routes {
		if r.Probed {
			parts = append(parts, fmt.Sprintf("%s(%dms)", r.Name(), r.Latency.Milliseconds()))
		} else {
			parts = append(parts, r.Name())
		}
	}
	return strings.Join(parts, " > ")
}

// maxAgentBinarySize caps the self-upgrade binary download (S-02).
const maxAgentBinarySize = 100 << 20 // 100MB

// upgradeStallWindow is the no-progress watchdog window for each
// self-upgrade download attempt (R3-WDG-01): an attempt with zero
// byte progress for this long is declared stalled and fails, so
// the proxy->direct fallback leg runs with budget to spare instead
// of the attempt burning the whole upgradeDownloadTimeout (the
// 2026-10-30 batch-upgrade failure: a trickling proxy body cost
// the full 240s per node). The value is the shared
// download.StallWindow (45s), registered in
// doc/audit/timeout-budgets.tsv row A002. A var so tests can
// shrink it.
var upgradeStallWindow = download.StallWindow

// upgradeTransferAttempt performs ONE self-upgrade binary
// download leg over an existing resumable transfer
// (download.FileTransfer): the persisted bytes of earlier legs
// stay on disk and this leg resumes after them with a Range
// request (a route answering the full body instead restarts the
// file inside the transfer — see FileTransfer.Attempt). The leg
// runs under a fresh no-progress watchdog (upgradeStallWindow): a
// stalled or trickling peer fails the leg with
// download.ErrNoProgress, keeping its persisted bytes for the
// next route.
func upgradeTransferAttempt(ctx context.Context, client *http.Client, transfer *download.FileTransfer, rawURL string, report func(percent int)) error {
	watcher := download.NewWatcher(upgradeStallWindow)
	return transfer.Attempt(ctx, watcher, client.Do, rawURL, func(downloaded, total int64) {
		if report != nil && total > 0 {
			report(int(downloaded * 100 / total))
		}
	})
}

// downloadUpgradeAttempt performs ONE self-upgrade binary download
// attempt from byte zero into tmpPath and returns its byte count
// and SHA-256. It is the one-shot shape kept for direct callers
// (tests); the production path (downloadUpgradeBinary) drives one
// resumable transfer across all of the plan's legs instead, so a
// failed leg no longer discards the bytes it already fetched.
func downloadUpgradeAttempt(ctx context.Context, client *http.Client, rawURL, tmpPath string, report func(percent int)) (int64, string, error) {
	transfer := download.NewFileTransfer(tmpPath, maxAgentBinarySize, 0o700)
	if err := transfer.Reset(); err != nil {
		return 0, "", err
	}
	err := upgradeTransferAttempt(ctx, client, transfer, rawURL, report)
	return transfer.Size(), transfer.Sum(), err
}

// downloadUpgradeBinary downloads the self-upgrade binary (the
// direct-form URL directURL) into tmpPath over the probe-selected
// route plan, and returns the byte count and SHA-256 hex of the
// content. All legs share ONE resumable transfer
// (download.FileTransfer): ANY failure of a leg — transport
// error, non-200, or a body that stalls or breaks mid-transfer —
// moves to the plan's next route, which resumes from the bytes
// already on disk instead of restarting from zero (1.7.12; the
// pre-resume caliber burned the shared budget on slow nodes: the
// first leg's trickle left the later legs an expired context).
// When every route fails, download.TryPlan's error names each
// route and its cause, and the per-leg log lines carry the
// persisted byte count for diagnosis. The route policy is shared
// with the checksum leg (downloadUpgradeChecksum) so the two
// legs cannot drift apart; the checksum leg itself is a tiny
// file and deliberately does not resume.
func downloadUpgradeBinary(ctx context.Context, client *http.Client, plan *download.Plan, directURL, tmpPath string, report func(percent int)) (int64, string, error) {
	return downloadUpgradeBinaryChecked(ctx, client, plan, directURL, tmpPath, "", report)
}

// downloadUpgradeBinaryChecked is downloadUpgradeBinary with the
// integrity check INSIDE each leg (round-5 L2-03): wantHash (the
// panel expectedSha256 when present, else the already-fetched
// .sha256 value — both known before the body leg starts) is
// compared against the assembled bytes the moment a leg
// completes; a route that delivered a complete-but-wrong body
// loses the leg, its bytes are discarded (Reset), and the next
// route restarts the file instead of the whole upgrade failing
// with every other route untried — the geodata leg's caliber,
// applied to the binary leg. wantHash == "" keeps the unchecked
// shape (direct callers/tests); production always passes an
// anchor because handleUpgradeAgent fetches the checksum first.
func downloadUpgradeBinaryChecked(ctx context.Context, client *http.Client, plan *download.Plan, directURL, tmpPath, wantHash string, report func(percent int)) (int64, string, error) {
	transfer := download.NewFileTransfer(tmpPath, maxAgentBinarySize, 0o700)
	_, err := download.TryPlan(plan, directURL, func(route download.Route, url string) error {
		aerr := upgradeTransferAttempt(ctx, client, transfer, url, report)
		if aerr == nil && wantHash != "" && !strings.EqualFold(transfer.Sum(), wantHash) {
			got := transfer.Sum()
			_ = transfer.Reset()
			aerr = fmt.Errorf("SHA256 校验失败: 期望 %s, 实际 %s（本线路作废，换线重下）", wantHash, got)
		}
		if aerr != nil {
			fmt.Printf("⚠️ 下载线路 %s 失败 (%v)，已下载 %d bytes，按测速排名尝试下一线路（保留断点续传）\n", route.Name(), aerr, transfer.Size())
		}
		return aerr
	})
	if err != nil {
		return 0, "", err
	}
	return transfer.Size(), transfer.Sum(), nil
}

// fetchUpgradeChecksumAttempt performs ONE checksum download
// attempt: GET rawURL under a fresh no-progress watchdog, then
// read and parse the body through the watcher. The whole attempt —
// headers and body — is the unit of failure, exactly like one
// binary attempt (downloadUpgradeAttempt): a body that stalls
// after the 200 headers fails the attempt with
// download.ErrNoProgress instead of escaping the fallback.
func fetchUpgradeChecksumAttempt(ctx context.Context, client *http.Client, rawURL string) (string, error) {
	watcher := download.NewWatcher(upgradeStallWindow)
	actx, stopWatch := watcher.Watch(ctx)
	defer stopWatch()
	resp, err := upgradeGET(actx, client, rawURL)
	if err != nil {
		if watcher.Stalled() {
			return "", download.ErrNoProgress
		}
		if watcher.TooSlow() {
			return "", download.ErrTooSlow
		}
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP状态码: %d", resp.StatusCode)
	}
	// 格式: "<hash>  <filename>" 或 "<hash>"；读取带上限 (D1-08)
	hash, err := readAgentChecksum(watcher.Reader(resp.Body))
	if err != nil {
		if watcher.Stalled() {
			return "", fmt.Errorf("读取校验文件失败: %w", download.ErrNoProgress)
		}
		if watcher.TooSlow() {
			return "", fmt.Errorf("读取校验文件失败: %w", download.ErrTooSlow)
		}
		return "", err
	}
	// 腿内内容校验（round-5 L2-04）：探针（probeUpgradeRoute）对
	// 同一内容做了 isSHA256Hex 判定，实拉此前只取首字段非空即
	// 算成功——stage-3 静态尾的未探测线路回一个 200 垃圾首字段
	// 就能赢下整腿、让事后比对整单失败。腿内同口径判定，垃圾
	// 内容只废本腿、计划换线。
	if !isSHA256Hex(hash) {
		return "", fmt.Errorf("校验文件内容不是合法 SHA256")
	}
	return hash, nil
}

// downloadUpgradeChecksum downloads and parses the expected sha256
// of the self-upgrade binary (the direct-form URL directURL) over
// the same probe-selected route plan and the same shared ctx
// budget as the binary leg: any failure of an attempt — transport
// error, non-200, or a body that stalls or breaks mid-read —
// moves to the plan's next route, and only when every route fails
// does the checksum leg fail. Phase F regression (fixed by keeping
// the whole GET+read inside the attempt): the checksum read used
// to sit outside a header-stage-only GET fallback, so a checksum
// body stalling after 200 aborted the whole upgrade with
// ErrNoProgress and no retry, contradicting the A002 caliber.
func downloadUpgradeChecksum(ctx context.Context, client *http.Client, plan *download.Plan, directURL string) (string, error) {
	hash, _, err := downloadUpgradeChecksumRouted(ctx, client, plan, directURL)
	return hash, err
}

// downloadUpgradeChecksumRouted is downloadUpgradeChecksum plus
// the winning route (the anchor corroboration in
// handleUpgradeAgent needs to know which route NOT to trust
// alone, round-5 L2-08).
func downloadUpgradeChecksumRouted(ctx context.Context, client *http.Client, plan *download.Plan, directURL string) (string, download.Route, error) {
	var hash string
	route, err := download.TryPlan(plan, directURL, func(route download.Route, url string) error {
		var aerr error
		hash, aerr = fetchUpgradeChecksumAttempt(ctx, client, url)
		if aerr != nil {
			fmt.Printf("⚠️ 校验文件线路 %s 失败 (%v)，按测速排名尝试下一线路\n", route.Name(), aerr)
		}
		return aerr
	})
	if err != nil {
		return "", download.Route{}, err
	}
	return hash, route, nil
}

// corroborateUpgradeChecksum re-fetches the self-upgrade .sha256
// through a route different from winner and requires the same
// hash (round-5 L2-08). It runs only when the panel sent no
// expectedSha256 — with a panel anchor the .sha256 is already a
// mere cross-check against an out-of-band value. A second route
// answering with a DIFFERENT hash aborts the upgrade. When no
// second route answers, the verdict is layered by the winning
// route (FIX-R2-9, same rule as the script side R2-01-01): a
// direct winner — or the first-party panel-source route —
// degrades to the single anchor, announced through note (the
// caller's panel-visible surface, RV2-03: the upgrade progress
// channel, not only the agent's stdout; a nil note falls back
// to stdout); a winning third-party mirror instead fails
// closed — uncorroborated, the (binary, .sha256) pair it served
// is its own self-consistent attestation, not verification.
func corroborateUpgradeChecksum(ctx context.Context, client *http.Client, winner download.Route, checksumURL, want string, note func(string)) error {
	// FIX-R2-9 分层判定：获胜线路是否为第三方公共镜像。直连的
	// Prefix 为空；面板源线路的 Prefix 恒为面板 origin（仅
	// scheme://host、derivePanelOrigin 不带路径）+ panelMirrorPath
	// ——第一方：命令下发者的自有镜像端点、与控制通道同信任域，
	// 且正是为够不到 GitHub/公共镜像的节点所设的首选路，归降级
	// 类；其余非空 Prefix 只能是编译期内置公共镜像表成员（无一
	// 以 panelMirrorPath 结尾），归 fail-closed 类。
	proxyWin := winner.Prefix != "" && !strings.HasSuffix(winner.Prefix, panelMirrorPath)
	announce := func(msg string) {
		if note != nil {
			note(msg)
		} else {
			fmt.Printf("⚠️ %s\n", msg)
		}
	}
	var routes []download.Route
	for _, r := range download.AllRoutes() {
		if r.Prefix != winner.Prefix {
			routes = append(routes, r)
		}
	}
	if len(routes) == 0 {
		if proxyWin {
			return fmt.Errorf("S-02: 跨线路锚复核不可用且获胜线路为代理（%s），无其他线路可复核，已中止升级（FIX-R2-9 第二锚）", winner.Name())
		}
		announce("跨线路锚复核已降级：升级校验锚复核不可用（无其他线路可复核），按单锚继续")
		return nil
	}
	actx, cancel := context.WithTimeout(ctx, anchorCorroborateTimeout)
	defer cancel()
	got, err := downloadUpgradeChecksum(actx, client, download.NewStaticPlan(routes), checksumURL)
	if err != nil {
		if proxyWin {
			return fmt.Errorf("S-02: 跨线路锚复核不可用且获胜线路为代理（%s），已中止升级（FIX-R2-9 第二锚）: %v", winner.Name(), err)
		}
		announce(fmt.Sprintf("跨线路锚复核已降级：升级校验锚复核不可用（其他线路均未给出校验值），按单锚继续（%v）", err))
		return nil
	}
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("S-02: 校验文件经两条不同线路获取的结果不一致，已中止升级: %s != %s", got, want)
	}
	return nil
}

// anchorCorroborateTimeout bounds one anchor corroboration: a
// cross-check on a tiny file, never worth the download budget.
// Registered in doc/audit/timeout-budgets.tsv (A002 note, L2-01);
// the x/kernel twin is pinned equal by gate tests in both
// packages.
var anchorCorroborateTimeout = 20 * time.Second

// upgradeStaging owns the lifecycle of the staged upgrade binary
// across the handler → detached restart script boundary (F-RM-01).
//
// The handler creates and fills the staging file, but the consumer is
// the restart script, which runs seconds AFTER the handler returns
// (it sleeps, stops the service, then mv's the staging file into
// place). Cleanup is therefore ownership-based, not defer-based:
//
//   - failure paths remove the file immediately (Discard, and the
//     deferred Close as backstop), so failed attempts never pile up;
//   - once the restart script has been launched, Handoff transfers
//     ownership to it and Close becomes a no-op: the script's mv
//     consumes the file on success, and the script removes it
//     explicitly when the swap itself fails.
//
// FB's L2-05 fix registered an unconditional defer os.Remove on the
// staging path; on the success path that deleted the staged binary
// the moment the handler returned, the script's mv then failed with
// "cannot stat", and the node came back on the old binary while the
// panel held a success receipt (real-machine finding F-RM-01).
type upgradeStaging struct {
	path     string
	handsOff bool
}

// newUpgradeStaging creates the per-attempt unique staging file
// (L2-05: a fixed name let a later upgrade adopt a previous attempt's
// partial download across versions; uniqueness keeps any resume
// inside the single operation that created the file).
func newUpgradeStaging(dir string) (*upgradeStaging, error) {
	// The staging prefix follows the layout generation of the
	// directory it lands in (legacy installs keep the legacy
	// prefix); anything unrecognized uses the current name.
	name := filepath.Base(dir)
	if !isAgentInstallName(name) {
		name = agentInstallName
	}
	tmpFile, err := os.CreateTemp(dir, "."+name+".upgrade-*")
	if err != nil {
		return nil, err
	}
	tmpPath := tmpFile.Name()
	_ = tmpFile.Close()
	return &upgradeStaging{path: tmpPath}, nil
}

// Path returns the staging file path handed to the download and to
// the restart script.
func (s *upgradeStaging) Path() string { return s.path }

// Discard removes the staging file immediately (failure paths).
// Idempotent: removing an already-removed path is not an error here.
func (s *upgradeStaging) Discard() { _ = os.Remove(s.path) }

// Handoff transfers ownership of the staging file to the launched
// restart script; Close will no longer remove it.
func (s *upgradeStaging) Handoff() { s.handsOff = true }

// Close is the deferred backstop cleanup: it removes the staging
// file unless ownership was handed off to the restart script.
func (s *upgradeStaging) Close() {
	if !s.handsOff {
		s.Discard()
	}
}

// upgradeInContainer is the container-form seam for the upgrade and
// rollback handlers, mirroring uninstallInContainer.
var upgradeInContainer = InContainer

func (w *WebSocketReporter) handleUpgradeAgent(data interface{}) error {
	// R2-004: the container form has no binary at the script layout
	// path and no service manager — panel-driven upgrade can never
	// apply. Refuse up front with a receipt that names the correct
	// path, before any staging file, download or progress report:
	// upgrading a container node means rebuilding the container from
	// a fresh install command.
	if upgradeInContainer() {
		return fmt.Errorf("容器形态节点无法通过面板升级：请在面板的节点安装弹窗重新获取 Docker/Compose 安装命令并在节点上重跑，即可完成升级")
	}
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
		// 兼容字段：旧面板在系统设置开启 GitHub 代理时下发 proxyUrl。
		// KU4 (1.7.7) 起不再消费此字段决定路由——面板的 GitHub 加速
		// 设置已整体退役，下载线路由 agent 在执行前对内置候选集
		// （download.MirrorPrefixes + 直连）实测排名决定；字段仅作
		// 线格式兼容保留解析。下载地址仍由 agent 用固定仓库+版本号
		// 本地构造，S-02 模型不变。
		ProxyURL string `json:"proxyUrl"`
		// 面板源二期：面板随升级命令下发的期望 SHA256（哈希值，
		// 不是 URL）。它走已鉴权+AES 加密的控制通道，在场时是校验
		// 的信任根（见 verifyUpgradeHashes）；缺失（旧面板）时回落
		// 现行同源 .sha256 比对。闲置的 ChecksumURL 字段不作此用——
		// URL 正是 S-02 禁止信任的形态，载体只能是哈希值本身。
		ExpectedSha256 string `json:"expectedSha256"`
	}
	if err := json.Unmarshal(jsonData, &req); err != nil {
		return fmt.Errorf("解析升级参数失败: %v", err)
	}
	// S-02: 拒绝面板传入的任意 URL（旧面板兼容性检查）
	if strings.TrimSpace(req.DownloadURL) != "" {
		return fmt.Errorf("S-02: 不再接受面板指定的下载地址，请升级面板到修复版本")
	}
	// D1-05: checksumUrl 与 downloadUrl 同律硬拒。此前该字段只解析不
	// 消费、静默忽略，守卫面不对称：URL 正是 S-02 禁止信任的载体——
	// 从发送方指定地址拉来的校验值与二进制同源、证明不了任何事。
	// 可信载体只能是哈希值本身，走 expectedSha256（在场时强制比对）。
	if strings.TrimSpace(req.ChecksumURL) != "" {
		return fmt.Errorf("S-02: 不再接受面板指定的校验地址(checksumUrl)，校验值只能以 expectedSha256 哈希形式下发，请升级面板到修复版本")
	}
	version := strings.TrimSpace(req.Version)
	if version == "" {
		return fmt.Errorf("版本号不能为空")
	}
	if !agentVersionRe.MatchString(version) {
		return fmt.Errorf("S-02: 版本号格式无效: %s", version)
	}
	expectedSha256 := strings.TrimSpace(req.ExpectedSha256)
	if expectedSha256 != "" && !isSHA256Hex(expectedSha256) {
		return fmt.Errorf("S-02: 面板下发的期望校验值(expectedSha256)格式无效")
	}

	// S-02: Agent 本地用固定仓库 + 严格校验的版本号拼 URL（直连形态）。
	// 实际下载线路在下方测速后按排名逐条构造（<候选>/<直连地址>），
	// 路径仍由固定仓库+已校验版本号构造；完整性由 SHA256 兜底。
	base := fmt.Sprintf("https://github.com/%s/releases/download/%s/", agentRepo, version)
	downloadURL := base + "gost-" + runtime.GOARCH
	checksumURL := base + "gost-" + runtime.GOARCH + ".sha256"

	w.sendUpgradeProgress("downloading", 0, "开始下载升级包...")
	fmt.Printf("📦 开始下载升级包: %s\n", sanitizeWebSocketURL(downloadURL))

	// 下载新版本二进制。落点是本进程所属的脚本安装布局（现行
	// /etc/flmx_agent/flmx_agent 或存量 /etc/flux_agent/flux_agent）：
	// 面板升级只换二进制、不动布局，存量节点在 install.sh 迁移
	// 前必须仍能被面板升级/回退，故按实际布局双认（见
	// resolveAgentBinaryPath）。
	binaryPath := resolveAgentBinaryPath()
	// L2-05: 暂存路径必须每次升级唯一。此前固定为 binaryPath +
	// ".new"，进程被杀/断电绕过清理后，下一次升级（不同版本资产）
	// 会被 FileTransfer.init 无条件收养旧断点：旧前缀＋新后缀跨
	// 版本拼装，只能靠最终哈希拦截、整次升级必废。唯一临时名让
	// 断点收养只可能发生在同一次操作内（download.FileTransfer
	// 的既定契约：callers stage into a fresh temp path per
	// operation）。清理按所有权走 upgradeStaging：失败路径即时删、
	// defer Close 兜底未显式删的错误返回；成功路径在重启脚本启动
	// 后 Handoff，暂存文件活过 handler 返回、由脚本 mv 消费
	// （F-RM-01：无条件 defer 删除曾让脚本 mv 必然扑空、假成功）。
	staging, err := newUpgradeStaging(filepath.Dir(binaryPath))
	if err != nil {
		return fmt.Errorf("创建升级暂存文件失败: %v", err)
	}
	tmpPath := staging.Path()
	defer staging.Close()
	backupPath := binaryPath + ".old"

	// S-02: 限制重定向只允许 GitHub 官方域名与内置镜像候选域名，
	// 防止跳转到恶意域名。限制下载大小 100MB。时限形态见
	// upgradeDownloadTimeout (R8-T-09)：整段下载（测速 + 本体 +
	// 校验 + 逐线路重试）共享一个整程预算，不再有单 GET 的
	// 60 秒整请求墙。
	// 面板源一期：从本机配置的面板地址（w.addr）派生面板线路，
	// 派生不出（地址为空/非法）则不注入、行为退化为现状。面板
	// 主机名同时填进下载客户端的重定向放行位（既有 proxyHost
	// 钩子）：gh_mirror 正常形态直接 200 出文件无重定向，此位只
	// 兜运维方反代自跳（www→apex 等）的情形。
	panelRoute, panelHost, hasPanelRoute := panelSourceRoute(w.addr)
	httpClient := newUpgradeDownloadClient(panelHost)
	dlCtx, dlCancel := context.WithTimeout(context.Background(), upgradeDownloadTimeout)
	defer dlCancel()
	// KU4: 下载线路走内置候选的测速计划（download.Plan）：先单独
	// 测直连（以本次的 .sha256 文件为准入探针——拉到且内容是
	// 合法 SHA256 才算活，防落地页假活），通则直连、不测任何
	// 代理；不通才并行测 9 个代理按延迟排名。测速计入整程预算
	// （单次 ~3s、代理段总界 ~4s）。下载途中已选线路失败时计划
	// 自动补测/顺排名续切（见 download.TryPlan）。选中的线路经
	// 进度消息上报面板，运维能看到本次走的哪条线。
	// 面板源一期：面板线路作为额外线路与 9 个代理同场参与阶段 2
	// 排名（实测延迟定胜负、无无条件先行），直连短路行为不变。
	var extraRoutes []download.Route
	if hasPanelRoute {
		extraRoutes = append(extraRoutes, panelRoute)
	}
	plan := download.NewPlan(dlCtx, checksumURL, probeUpgradeRoute(httpClient), extraRoutes...)
	routes := plan.Routes()
	if len(routes) > 0 && routes[0].Probed {
		w.sendUpgradeProgress("downloading", 0, fmt.Sprintf("下载线路测速完成，选用 %s（实测 %dms）", routes[0].Name(), routes[0].Latency.Milliseconds()))
	} else {
		w.sendUpgradeProgress("downloading", 0, "下载线路测速未选出准入线路，按内置顺序逐个尝试")
	}
	fmt.Printf("📦 升级下载线路排名: %s\n", describeUpgradeRoutes(plan.Probed()))

	// 校验先行（round-5 L2-03）：锚在本体下载之前定下，本体腿才
	// 能在腿内校验（见 downloadUpgradeBinaryChecked）。两个锚源
	// 都在此就绪：面板 expectedSha256（命令已带）与下载通道的
	// .sha256（本步拉取）。校验腿与二进制腿同线路排名、同共享
	// 预算（dlCtx, R8-T-09）——每次尝试是 GET+正文读取的整体、
	// 自带进展看门狗（upgradeStallWindow），任一失败切下一排名
	// 线路，全部失败才整单失败（R7-J-02 + R3-WDG-01 + Phase F
	// 修复 + KU4 的口径不变，只是顺序由本体后移到本体之前）。
	// S-02: Checksum 校验强制，拿不到校验值一律中止。
	w.sendUpgradeProgress("verifying", 0, "获取校验文件...")
	expectedHash, checksumRoute, err := downloadUpgradeChecksumRouted(dlCtx, httpClient, plan, checksumURL)
	if err != nil {
		return fmt.Errorf("S-02: 获取校验值失败: %v", err)
	}
	// L2-08: 面板未下发 expectedSha256 时（旧面板/辨不出架构/
	// 预热超时），.sha256 与本体同通道、无带外信任根——经一条
	// 不同线路复核同一锚值，两线不一致即中止；面板锚在场时
	// .sha256 本就是交叉核对，不必复核（见
	// corroborateUpgradeChecksum）。
	if expectedSha256 == "" {
		// RV2-03: a corroboration degradation is reported on the
		// upgrade progress channel (panel-visible), not stdout only.
		note := func(msg string) { w.sendUpgradeProgress("verifying", 0, msg) }
		if err := corroborateUpgradeChecksum(dlCtx, httpClient, checksumRoute, checksumURL, expectedHash, note); err != nil {
			return err
		}
	}
	anchor := expectedSha256
	if anchor == "" {
		anchor = expectedHash
	}

	// R7-J-02 + R3-WDG-01 + KU4: downloadUpgradeBinary 按测速排名
	// 逐线路跑下载尝试，任一线路失败（含正文停滞被看门狗判失败）
	// 自动切下一线路，口径见该函数注释。
	lastPercent := 0
	report := func(percent int) {
		if percent-lastPercent >= 10 {
			lastPercent = percent
			w.sendUpgradeProgress("downloading", percent, fmt.Sprintf("下载中... %d%%", percent))
		}
	}
	downloaded, actualHash, err := downloadUpgradeBinaryChecked(dlCtx, httpClient, plan, downloadURL, tmpPath, anchor, report)
	if err != nil {
		staging.Discard()
		return fmt.Errorf("下载升级包失败: %v", err)
	}

	w.sendUpgradeProgress("downloading", 100, fmt.Sprintf("下载完成 (%d bytes)", downloaded))

	// S-02 最终门（语义不变）：面板源二期——命令带了
	// expectedSha256 时它就是信任根（控制通道下发），下载通道的
	// .sha256 退为交叉核对；未带时与旧口径逐字一致（见
	// verifyUpgradeHashes）。腿内校验已按同一锚逐腿把关，此处
	// 为整单兜底复核。
	w.sendUpgradeProgress("verifying", 50, "校验文件完整性...")
	if err := verifyUpgradeHashes(expectedSha256, expectedHash, actualHash); err != nil {
		staging.Discard()
		return err
	}
	if expectedSha256 != "" {
		fmt.Printf("✅ 与面板下发的期望校验值一致: %s\n", actualHash)
	}
	fmt.Printf("✅ Checksum 校验通过: %s\n", actualHash)
	w.sendUpgradeProgress("verifying", 100, "校验通过")

	if err := os.Chmod(tmpPath, 0755); err != nil {
		staging.Discard()
		return fmt.Errorf("设置执行权限失败: %v", err)
	}

	// A11-132-02: 替换前先做可执行冒烟（-V）。sha256 只证明完整性，
	// 不证明新二进制在本机可执行（错架构/UPX 损坏/libc 不兼容）。
	w.sendUpgradeProgress("installing", 30, "冒烟测试新版本...")
	if err := smokeTestAgentBinary(tmpPath); err != nil {
		staging.Discard()
		return err
	}
	fmt.Println("✅ 新版本二进制冒烟测试通过")

	// 备份旧版本（复制，不用 rename，因为可能正在运行）。
	// A11-132-02: 备份失败必须中止升级——没有备份就没有回滚路径，
	// 旧实现吞掉写错误继续替换是节点变砖的直接原因。
	w.sendUpgradeProgress("installing", 50, "备份旧版本...")
	// R6-05: pair the backup with the binary being installed (see
	// agentBackupMeta), so a later rollback can prove the backup is
	// still the immediately previous generation even if another
	// path (install.sh script update) replaced the binary in
	// between. Failure aborts the upgrade with the same caliber as
	// a backup failure — an unpairable backup would leave the
	// rollback generation verdict unprovable. The pairing is only
	// written when this upgrade actually produced the backup: if
	// there was no binary to back up, any pre-existing .old keeps
	// its previous (or legacy, absent) pairing rather than being
	// falsely paired with the incoming version.
	_, binaryStatErr := os.Stat(binaryPath)
	if err := backupCurrentAgentBinary(binaryPath, backupPath); err != nil {
		staging.Discard()
		return err
	}
	fmt.Println("📦 旧版本已备份到", backupPath)

	if binaryStatErr == nil {
		backupHash, herr := hashFileSHA256(backupPath)
		if herr != nil {
			staging.Discard()
			return fmt.Errorf("计算回退备份校验值失败: %v", herr)
		}
		meta := &agentBackupMeta{BackupSHA256: backupHash, InstalledVersion: version, InstalledSHA256: actualHash}
		if err := writeAgentBackupMeta(backupPath, meta); err != nil {
			staging.Discard()
			return fmt.Errorf("写入回退备份配对记录失败: %v", err)
		}
	}

	w.sendUpgradeProgress("installing", 80, "准备重启...")
	fmt.Printf("✅ 升级包下载完成 (%d bytes), 准备重启...\n", downloaded)

	// A11-132-01: 按节点的 init 系统分发重启脚本（systemd / OpenRC），
	// 不支持的 init 明确报错而不是静默失败。脚本在脱离 agent 进程的
	// 环境中运行（systemd-run transient unit / setsid 新会话），避免
	// 停服务时把脚本自己一并杀掉；脚本内含启动失败自动回滚（本地闭环，
	// 不依赖面板连接）。
	manager, err := detectServiceManager()
	if err != nil {
		staging.Discard()
		return err
	}
	script, err := buildAgentRestartScriptFor(manager, tmpPath, binaryPath, backupPath)
	if err != nil {
		staging.Discard()
		return err
	}
	if err := launchAgentControlScript(manager, script); err != nil {
		staging.Discard()
		return fmt.Errorf("启动重启脚本失败: %v", err)
	}
	// F-RM-01: 脚本已启动，暂存文件的所有权移交脚本——它 sleep＋
	// 停服务之后才 mv，handler 返回后必须仍在。Close 自此为空操作，
	// 文件由脚本的 mv 消费（失败时脚本自行删除并在日志中点名）。
	staging.Handoff()

	w.sendUpgradeProgress("installing", 100, "重启中...")
	fmt.Println("🔄 重启脚本已启动, Agent 将在 1 秒后重启...")
	return nil
}

func (w *WebSocketReporter) handleRollbackAgent(data interface{}) (map[string]interface{}, error) {
	// R2-004: same container-form refusal as handleUpgradeAgent —
	// there is no script-layout backup to roll back to; the previous
	// version is restored by re-running its install command.
	if upgradeInContainer() {
		return nil, fmt.Errorf("容器形态节点无法通过面板回退：请在面板的节点安装弹窗重新获取目标版本的 Docker/Compose 安装命令并在节点上重跑")
	}
	binaryPath := resolveAgentBinaryPath()
	backupPath := binaryPath + ".old"

	// R6-05: the pre-flight decides whether the backup may be used
	// at all (generation pairing vs. cross-path replacement, see
	// planAgentRollback) and what the receipt must name, before any
	// script is launched.
	plan, err := planAgentRollback(binaryPath, backupPath)
	if err != nil {
		return nil, err
	}

	fmt.Println("🔄 开始回退到旧版本...")

	// 执行回退脚本（与升级同一套 init 分发，A11-132-01）
	manager, err := detectServiceManager()
	if err != nil {
		return nil, err
	}
	script, err := buildAgentRollbackScriptFor(manager, backupPath, binaryPath)
	if err != nil {
		return nil, err
	}
	if err := launchAgentControlScript(manager, script); err != nil {
		return nil, fmt.Errorf("启动回退脚本失败: %v", err)
	}

	fmt.Println("🔄 回退脚本已启动, Agent 将在 1 秒后重启...")
	return map[string]interface{}{
		"fromVersion":      plan.FromVersion,
		"toVersion":        plan.ToVersion,
		"backupGeneration": plan.Generation,
	}, nil
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
//
// R2-A01-002: 写回必须保全其余字段。此前用仅含 addr/secret/http/tls/
// socks 五字段的结构体整表重构写回，安装时落盘的 proxy（原升级代理
// 前缀；KU4 起该字段已无消费方、仅作未知字段保全）与任何结构体外
// 字段（手工加的键）都被静默吞掉，无声改变节点本地配置且无日志。
// 改为 map 合并：既有键以 json.RawMessage 原样保留、只覆写三个目标键，
// 规范字段全集与未知字段均不丢失。文件不存在时按空 map 起步（与旧行为
// 一致地新建）；文件存在但不是 JSON 对象时 fail-closed 报错——解析不了
// 的文件不许整表覆盖销毁（LoadConfig 启动期亦会拒绝该文件）。
func updateLocalConfigJSON(httpVal int, tlsVal int, socksVal int) error {
	path := "config.json"

	fields := map[string]json.RawMessage{}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &fields); err != nil {
			return fmt.Errorf("解析现有 config.json 失败，拒绝覆盖写回: %w", err)
		}
		// 裸 JSON null 解码成功但会把 map 置为 nil，后续赋值 panic（LB174-01 守卫）。
		if fields == nil {
			fields = map[string]json.RawMessage{}
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	setInt := func(key string, val int) error {
		raw, err := json.Marshal(val)
		if err != nil {
			return err
		}
		fields[key] = raw
		return nil
	}
	if err := setInt("http", httpVal); err != nil {
		return err
	}
	if err := setInt("tls", tlsVal); err != nil {
		return err
	}
	if err := setInt("socks", socksVal); err != nil {
		return err
	}

	data, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return err
	}
	// R3-A01-002: 原子写（temp+rename，复用 tuning.go 的
	// writeFileAtomic）。config.json 是 LoadConfig 的启动必读文件，
	// 此前 os.WriteFile 整表直写是全 agent 唯一对启动关键配置的
	// 直写点：写中途崩溃留下截断 JSON，下次启动直接失败、节点永久
	// 离线。rename 保证磁盘上恒为完整旧版或完整新版。权限口径与直
	// 写时代一致：已存在文件保留其现有权限，新建为 0644。
	mode := os.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	return writeFileAtomic(path, data, mode)
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

// sendErrorResponse 发送解析前错误响应（R2-X2-02）。这类失败发生在
// 命令执行之前，requestId 的可得性因失败点而异：压缩帧与明文帧在
// 解析失败时仍可从帧上读出 requestId（压缩帧取信封字段、明文帧走
// bestEffortRequestID 宽松提取），回执带上它，面板 tryResolvePending
// 即可把失败原因直达等待方而不是超时；加密信封三型（StaleTimestamp/
// DecryptError/NoDecryptor）的 requestId 在未解密的信封内层，构造上
// 不可得，传空串——该情形由面板侧按类型白名单记录 message 兜底
// （ws/server.go 的 agentPreParseErrorReason），双侧都不改
// fail-closed 语义。
func (w *WebSocketReporter) sendErrorResponse(responseType, message, requestID string) {
	response := CommandResponse{
		Type:      responseType,
		Success:   false,
		Message:   message,
		RequestId: requestID,
	}
	w.sendResponse(response)
}

// bestEffortRequestID 在 CommandMessage 整体解析失败时宽松提取帧上
// 的 requestId（解析失败常出在 data 字段类型不符，顶层字段仍可读）。
// 提取不到返回空串，回执退化为无关联形态。
func bestEffortRequestID(message []byte) string {
	var probe struct {
		RequestId string `json:"requestId"`
	}
	if err := json.Unmarshal(message, &probe); err != nil {
		return ""
	}
	return probe.RequestId
}

// getUptime 获取系统开机时间（秒）
func getUptime() uint64 {
	uptime, err := host.Uptime()
	if err != nil {
		return 0
	}
	return uptime
}

var (
	bootIDOnce  sync.Once
	bootIDValue string
)

// machineBootID 返回本机本次开机的标识：Linux 的
// /proc/sys/kernel/random/boot_id，读不到时回退开机时间戳
// （gopsutil host.BootTime，同为每次开机一值、开机内不变）。
// 进程内只解析一次——同一次开机内 agent 重启标识不变；解析不
// 出时为空串，面板对空标识按「无法证明重启」保守处理。
func machineBootID() string {
	bootIDOnce.Do(func() {
		if data, err := os.ReadFile("/proc/sys/kernel/random/boot_id"); err == nil {
			bootIDValue = strings.TrimSpace(string(data))
		}
		if bootIDValue == "" {
			if bt, err := host.BootTime(); err == nil && bt > 0 {
				bootIDValue = strconv.FormatUint(bt, 10)
			}
		}
	})
	return bootIDValue
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
	// 面板源三期：把同一条由本机面板地址派生的面板线路注册给
	// 内核下载面（x/kernel 的 install/geodata 计划与自升级同
	// 口径纳入排名）；派生不出则不注册，内核面行为与现状一致。
	if route, _, ok := panelSourceRoute(addr); ok {
		kernel.SetPanelRoute(route)
	}
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

// tcpPing 字面量复检的段集，与面板 go-backend/internal/netguard 的
// PolicyHardDenyOrLoopback 判定口径镜像（x 为独立 module 无法 import
// 面板包，故在此自含实现；修改段集时须与 netguard 分类器同步核对）。
var (
	tcpPingNAT64WellKnown = netip.MustParsePrefix("64:ff9b::/96")
	tcpPingNAT64Local     = netip.MustParsePrefix("64:ff9b:1::/48")
	tcpPingSixToFour      = netip.MustParsePrefix("2002::/16")
	tcpPingLinkLocalV4    = netip.MustParsePrefix("169.254.0.0/16")
	tcpPingMetadataExact  = []netip.Addr{
		netip.MustParseAddr("169.254.169.254"),
		netip.MustParseAddr("100.100.100.200"),
		netip.MustParseAddr("fd00:ec2::254"),
	}
	tcpPingReservedPrefixes = []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("192.0.0.0/24"),
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("240.0.0.0/4"),
		netip.MustParsePrefix("100::/64"),
	}
)

// tcpPingLiteralTargetDenied 报告 host 字面 IP 是否命中
// PolicyHardDenyOrLoopback 等价拒绝集：元数据精确集 / 169.254/16
// 整段 / 未指定（硬拒层）+ 回环 + 保留段 + 组播 + 链路本地
// （fe80::/10）；NAT64/6to4 按内嵌 IPv4 同判，本地 NAT64 整段拒。
// 非字面量（主机名）返回 false，不在本复检范围。私网/CGNAT 放行。
func tcpPingLiteralTargetDenied(host string) bool {
	a, err := netip.ParseAddr(strings.TrimSpace(host))
	if err != nil {
		return false
	}
	return tcpPingResolvedDenied(a)
}

// effectivePingIP 返回判定口径地址：Unmap 规范化后，NAT64 /96 与
// 6to4 提取内嵌 IPv4；本地 NAT64（64:ff9b:1::/48）整段以第二返回值
// 标记（布局含 u 字节穿插、不做内嵌提取）。与 tcpPingLiteralTargetDenied
// 的既有翻译口径一致，抽出供本文件全部判定入口共用（R4 批7 收口：
// 此前只有 TcpPing 字面量一面消费，监控/Traceroute/解析落点各面漏接）。
func effectivePingIP(a netip.Addr) (netip.Addr, bool) {
	a = a.Unmap().WithZone("")
	if tcpPingNAT64Local.Contains(a) {
		return a, true
	}
	if tcpPingNAT64WellKnown.Contains(a) {
		b := a.As16()
		return netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}), false
	}
	if tcpPingSixToFour.Contains(a) {
		b := a.As16()
		return netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}), false
	}
	return a, false
}

// hardDeniedIP 报告地址是否命中硬拒层（面板 netguard HardDeny 的镜像
// 子集：元数据精确集 ∪ 169.254/16 整段 ∪ 未指定 ∪ 本地 NAT64 整段，
// 翻译内嵌同判）。这是全仓一切出站面的最低底线：私网/回环/CGNAT/
// 保留/组播/链路本地均不在此集——需要更严口径的面用
// tcpPingLiteralTargetDenied（HardDenyOrLoopback 等价整集）。
// 服务监控执行端的硬拒复检（R4X-008）即由本谓词承担：
// handleServiceMonitorCheck（TCP 腿）与 icmpPing 对字面量目标与主机名
// 解析落点统一走 resolvedAddrDenied(ip, hardDeniedIP)，没有独立的
// 字面量专用助手——勿按「只判字面量」的窄口径另行接线，会漏掉主机名
// 解析落点复检。
func hardDeniedIP(a netip.Addr) bool {
	if !a.IsValid() {
		return true
	}
	eff, localNAT64 := effectivePingIP(a)
	if localNAT64 {
		return true
	}
	for _, m := range tcpPingMetadataExact {
		if eff == m {
			return true
		}
	}
	return tcpPingLinkLocalV4.Contains(eff) || eff.IsUnspecified()
}

// resolvedAddrDenied 是主机名解析落点的复检谓词（R4X-020）：解析
// 结果（net.IP 形态）按给定全集谓词判定——TcpPing/Traceroute 面传
// tcpPingLiteralTargetDenied 的等价整集判定，监控面传硬拒子集。
// 解析结果为 nil/非法按命中处理（fail-closed）。
func resolvedAddrDenied(ip net.IP, deny func(netip.Addr) bool) bool {
	if ip == nil {
		return true
	}
	a, err := netip.ParseAddr(ip.String())
	if err != nil {
		return true
	}
	return deny(a)
}

// tcpPingResolvedDenied 是 TcpPing/Traceroute 解析落点的整集判定
// （与 tcpPingLiteralTargetDenied 同集，入参为已解析的 netip.Addr）。
func tcpPingResolvedDenied(a netip.Addr) bool {
	eff, localNAT64 := effectivePingIP(a)
	if localNAT64 {
		return true
	}
	for _, m := range tcpPingMetadataExact {
		if eff == m {
			return true
		}
	}
	if tcpPingLinkLocalV4.Contains(eff) || eff.IsUnspecified() {
		return true
	}
	if eff.IsLoopback() || eff.IsMulticast() || eff.IsLinkLocalUnicast() || eff.IsLinkLocalMulticast() {
		return true
	}
	for _, p := range tcpPingReservedPrefixes {
		if p.Contains(eff) {
			return true
		}
	}
	if eff.IsPrivate() {
		return false
	}
	return !eff.IsGlobalUnicast()
}

// handleTcpPing 处理TCP ping诊断命令
func (w *WebSocketReporter) handleTcpPing(ctx context.Context, data interface{}) (TcpPingResponse, error) {
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

	// R3X-011 纵深复检：下发目标若为字面 IP，按面板
	// PolicyHardDenyOrLoopback 同口径在拨号前拒绝（硬拒 + 回环 +
	// 保留 + 组播 + 链路本地；私网/CGNAT 字面量不在拒绝集内——
	// 节点间链路目标常为内网地址）。主机名不在字面量复检范围。
	if tcpPingLiteralTargetDenied(req.IP) {
		return TcpPingResponse{
			IP:           req.IP,
			Port:         req.Port,
			Success:      false,
			ErrorMessage: "目标地址不允许",
			RequestId:    req.RequestId,
		}, nil
	}

	// 设置默认值并钳制上限（F-088）
	req.Count, req.Timeout = clampPingParams(req.Count, req.Timeout)

	// 执行TCP ping操作
	avgTime, packetLoss, err := tcpPingHost(ctx, req.IP, req.Port, req.Count, req.Timeout)
	// DB-5：预算耗尽/取消导致的失败以
	// 命令级错误回明确原因；已有部分成功结果时 err 为 nil，照常回
	// 部分结果（取消不抹掉已测得的 attempts）。
	if err != nil && ctx.Err() != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return TcpPingResponse{}, fmt.Errorf("TCP探测执行超时（含排队等待）")
		}
		return TcpPingResponse{}, fmt.Errorf("TCP探测已取消")
	}

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
	}
	// RM-VERIFY S5 观察②：tcpPingHost 在失败时同样返回实测值
	// （全失败 packetLoss=100、DNS 失败同），错误分支不得丢弃——
	// 旧组装只在成功分支赋值，死目标被呈现为 0% 丢包，与实测相反
	// （L4-04 诊断数必须真实）。验证前置返回未执行探测、不在此列。
	response.AverageTime = avgTime
	response.PacketLoss = packetLoss

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

	// R6X-015：与 handleTcpPing 同口径的字面量复检（R3X-011 整集），
	// UdpPing 不再是同命令族唯一未接面。主机名不在字面量复检范围，
	// 其解析落点在 udpPingHost 内逐 IP 复检。
	if tcpPingLiteralTargetDenied(req.IP) {
		return TcpPingResponse{
			IP:           req.IP,
			Port:         req.Port,
			Success:      false,
			ErrorMessage: "目标地址不允许",
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
		// RM-VERIFY S5 观察②（UDP 腿同形态）：udpPingHost 失败时同样
		// 返回实测 packetLoss=100，错误分支不得丢弃，理由同 handleTcpPing。
		// 此类失败是探测前错误（DNS 解析失败/落点被拒/本地拨号错误），
		// 与协议无关、是真实失败，不在未验证豁免内。
		response.AverageTime = avgTime
		response.PacketLoss = packetLoss
		return response, nil
	}

	// R6-L4-01：udpPingHost 只 Dial 不发包（UDP 无通用回声协议），
	// 其「成功」只说明本机能建 UDP socket，averageTime 是本地系统
	// 调用耗时、packetLoss 恒 0——都不是目标路径的测量值，旧实现
	// 将其作为 success+实测数值回传，面板呈现为「0ms/0% 优秀」，
	// 与目标真实状态无关（死目标同呈成功）。自本版起：拨号无错
	// 只用于区分「探测前失败」（上方分支）与「不可验证」，改回未
	// 验证标记（Success=false）；数值字段不承载测量值——结构体
	// 字段无 omitempty，线上恒为零值，由面板组装层收口时覆写为
	// null 后才出面板（见面板 markDiagnosisItemUnverified）。
	response.Success = false
	response.Unverified = true
	response.ErrorMessage = "UDP 无通用回声协议，无法实测连通性"

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
		host, port, splitErr := net.SplitHostPort(target)
		if splitErr != nil {
			res.Success = false
			res.ErrorMessage = "无效的TCP目标"
			res.LatencyMs = float64(time.Since(start).Milliseconds())
			return res, nil
		}
		// R4X-008：拨号前硬拒子集复检（只取硬拒层；私网/回环/CGNAT
		// 是节点监控 LAN 服务的正当目标，严禁顺手禁）。字面量直接
		// 判；主机名在节点侧解析后逐 IP 复检落点并拨已检 IP 字面量
		//（防面板/节点 DNS 分视图与校验后翻址），解析失败按检查
		// 失败回原错误面。
		dialTarget := target
		if ip := net.ParseIP(host); ip != nil {
			if resolvedAddrDenied(ip, hardDeniedIP) {
				res.Success = false
				res.ErrorMessage = "目标地址不允许"
				res.LatencyMs = float64(time.Since(start).Milliseconds())
				return res, nil
			}
		} else {
			ips, lookupErr := net.LookupIP(host)
			if lookupErr != nil || len(ips) == 0 {
				res.Success = false
				if lookupErr != nil {
					res.ErrorMessage = lookupErr.Error()
				} else {
					res.ErrorMessage = "目标主机无法解析"
				}
				res.LatencyMs = float64(time.Since(start).Milliseconds())
				return res, nil
			}
			for _, ip := range ips {
				if resolvedAddrDenied(ip, hardDeniedIP) {
					res.Success = false
					res.ErrorMessage = "目标地址不允许"
					res.LatencyMs = float64(time.Since(start).Milliseconds())
					return res, nil
				}
			}
			dialTarget = net.JoinHostPort(ips[0].String(), port)
		}
		conn, dialErr := net.DialTimeout("tcp", dialTarget, timeout)
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

	// R4X-008：ICMP 发包前对解析落点做硬拒子集复检（icmpPing 的调用方
	// 为服务监控执行面；字面量与主机名两路在此合流——字面量经
	// ResolveIPAddr 原样返回、同样过检）。仅硬拒层，私网/回环放行。
	if resolvedAddrDenied(ipAddr.IP, hardDeniedIP) {
		return time.Since(start), fmt.Errorf("目标地址不允许")
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
func tcpPingHost(ctx context.Context, ip string, port int, count int, timeoutMs int) (float64, float64, error) {
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

		addrs, err := net.DefaultResolver.LookupHost(ctx, ip)
		dnsDuration := time.Since(dnsStart)

		if err != nil {
			return 0, 100.0, fmt.Errorf("DNS解析失败: %v", err)
		}
		if len(addrs) == 0 {
			return 0, 100.0, fmt.Errorf("DNS解析未返回任何IP地址")
		}

		fmt.Printf("✅ DNS解析完成 (%.2fms)，解析到 %d 个IP: %v\n",
			dnsDuration.Seconds()*1000, len(addrs), addrs)

		// R4X-020：主机名在节点侧解析后以同口径复检解析落点——面板
		// 保存面校验用的是面板 DNS，与节点分视图时落点可入硬拒/回环
		// 集（S2 残余）。任一解析结果命中整集即整体拒绝（fail-closed，
		// 与面板解析面逐 IP 判定同口径），不得只查首个地址。
		for _, s := range addrs {
			picked := net.ParseIP(s)
			if picked == nil || resolvedAddrDenied(picked, tcpPingResolvedDenied) {
				return 0, 100.0, fmt.Errorf("目标地址不允许")
			}
		}

		// 使用第一个解析到的IP进行测试
		target = net.JoinHostPort(addrs[0], fmt.Sprintf("%d", port))
		fmt.Printf("🎯 使用IP地址进行测试: %s\n", target)
	} else {
		fmt.Printf("🎯 使用IP地址进行测试: %s\n", target)
	}

	// attempts counts the dials that finished; one cut short by ctx is not
	// a loss. When the budget runs out the attempts so far are the result.
	attempts := 0
	for i := 0; i < count; i++ {
		if ctx.Err() != nil {
			break
		}
		start := time.Now()

		// 创建带超时的TCP连接
		dialer := net.Dialer{Timeout: timeout}
		conn, err := dialer.DialContext(ctx, "tcp", target)

		elapsed := time.Since(start)
		if err != nil && ctx.Err() != nil {
			break
		}
		attempts++

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
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
			}
		}
	}

	if successCount == 0 {
		if err := ctx.Err(); err != nil {
			return 0, 0, err
		}
		return 0, 100.0, fmt.Errorf("所有TCP连接尝试都失败")
	}

	avgTime := totalTime / float64(successCount)
	packetLoss := float64(attempts-successCount) / float64(attempts) * 100

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

		// R6X-015：镜像 tcpPingHost 的解析落点复检（R4X-020）——任一
		// 解析结果命中整集即整体拒绝（fail-closed）。注：本函数只
		// Dial 后即 Close、从不 Write，本批只补谓词与复检，不加发包行为。
		for _, s := range addrs {
			picked := net.ParseIP(s)
			if picked == nil || resolvedAddrDenied(picked, tcpPingResolvedDenied) {
				return 0, 100.0, fmt.Errorf("目标地址不允许")
			}
		}

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
