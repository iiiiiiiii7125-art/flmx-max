package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"go-backend/internal/http/response"
)

// Notification events. An empty notify_events config enables all of them.
const (
	notifyEventNodeOffline = "node_offline"
	notifyEventInboundDown = "inbound_down"
	notifyEventKernelError = "kernel_error"
	notifyEventNodeExpiry  = "node_expiry"
	notifyEventLatencyBad  = "latency_bad"
	notifyEventRouteChange = "route_change"
	notifyEventTest        = "test"
)

const (
	notifyConfigEnabled     = "notify_enabled"
	notifyConfigEvents      = "notify_events"
	notifyConfigTGToken     = "notify_telegram_bot_token"
	notifyConfigTGChatID    = "notify_telegram_chat_id"
	notifyConfigTGAPIBase   = "notify_telegram_api_base"
	notifyConfigWebhookURL  = "notify_webhook_url"
	defaultTelegramAPIBase  = "https://api.telegram.org"
	notifySendTimeout       = 10 * time.Second
	notifyExpiryWindow      = 3 * 24 * time.Hour
	notifyInboundDownStreak = 2
	// O8-004：已过期超过该时长的条目不再补发到期通知。到期告警状态（expiryAlerted /
	// alertOnce 键）只在内存中，面板重启后会把历史已过期的节点/用户/订阅再报一次；
	// 加上这条下界后，重启只重报"刚到期"的，久已过期的直接跳过。
	notifyLongExpired = 24 * time.Hour
)

// nodeOfflineGrace keeps short reconnects (agent restart, upgrade) quiet.
var nodeOfflineGrace = 90 * time.Second

type notifySettings struct {
	TelegramToken   string `json:"telegramToken"`
	TelegramChatID  string `json:"telegramChatId"`
	TelegramAPIBase string `json:"telegramApiBase"`
	WebhookURL      string `json:"webhookUrl"`
}

func (s notifySettings) empty() bool {
	return (s.TelegramToken == "" || s.TelegramChatID == "") && s.WebhookURL == ""
}

type notifyMessage struct {
	Event   string `json:"event"`
	Title   string `json:"title"`
	Message string `json:"message"`
	Time    int64  `json:"time"`
	App     string `json:"app"`
}

// notifier tracks alert state so each problem is reported once and its
// recovery once.
type notifier struct {
	mu             sync.Mutex
	offlineTimers  map[int64]*time.Timer
	offlineAlerted map[int64]bool
	expiryAlerted  map[int64]int64
	// alerted holds keys of reminders already sent (traffic, expiry).
	alerted map[string]bool
	// recentSent 记录最近成功发送过的通知指纹与时间（O8-005），用于抑制
	// 同一问题在短时间内抖动重复触发的通知（如内核错误状态反复翻转）。
	recentSent map[string]time.Time
	client     *http.Client
}

func newNotifier() *notifier {
	return &notifier{
		offlineTimers:  make(map[int64]*time.Timer),
		offlineAlerted: make(map[int64]bool),
		expiryAlerted:  make(map[int64]int64),
		alerted:        make(map[string]bool),
		recentSent:     make(map[string]time.Time),
		// AUDIT-R26 (R5R26-001): 重定向目标同样走 validateNotifyURL 校验，
		// 防 302/301 跳转到元数据地址绕过 LAD26-001 的 SSRF 控制。
		// 同时限制最多 5 跳，防重定向环。
		client: &http.Client{
			Timeout: notifySendTimeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return errors.New("通知地址重定向次数过多")
				}
				if err := validateNotifyURL(req.URL.String()); err != nil {
					return err
				}
				return nil
			},
		},
	}
}

// notifyConfig reads the saved settings; ok is false when notifications
// are off or the event is not selected.
func (h *Handler) notifyConfig(event string) (notifySettings, string, bool) {
	cfg, err := h.repo.ListConfigs()
	if err != nil {
		return notifySettings{}, "", false
	}
	app := strings.TrimSpace(cfg["app_name"])
	if app == "" {
		app = "FLMX"
	}
	settings := notifySettings{
		TelegramToken:   strings.TrimSpace(cfg[notifyConfigTGToken]),
		TelegramChatID:  strings.TrimSpace(cfg[notifyConfigTGChatID]),
		TelegramAPIBase: strings.TrimSpace(cfg[notifyConfigTGAPIBase]),
		WebhookURL:      strings.TrimSpace(cfg[notifyConfigWebhookURL]),
	}
	if cfg[notifyConfigEnabled] != "true" || settings.empty() {
		return settings, app, false
	}
	if events := strings.TrimSpace(cfg[notifyConfigEvents]); events != "" {
		// O8-006：兼容旧前端"全不选"时写入的字面量 "none"。之前前端靠"没有事件叫
		// none"实现全禁用，属于脆弱的隐式约定；此处显式视为禁用全部事件。
		if events == "none" {
			return settings, app, false
		}
		selected := false
		for _, e := range strings.Split(events, ",") {
			if strings.TrimSpace(e) == event {
				selected = true
				break
			}
		}
		if !selected {
			return settings, app, false
		}
	}
	return settings, app, true
}

// notifyDupWindow 内事件+标题+内容完全相同的通知只发送一次（O8-005）。
const notifyDupWindow = 30 * time.Minute

// notifyRetryDelay 是通知发送失败后重试一次的间隔（O8-002）。
const notifyRetryDelay = 3 * time.Second

// fingerprint 是通知的去重指纹：同一事件、同一标题、同一内容视为同一问题。
func notifyFingerprint(event, title, message string) string {
	return event + "\x00" + title + "\x00" + message
}

// recentlySent 报告该指纹是否在去重窗口内已成功发送过（O8-005）。
func (n *notifier) recentlySent(fp string, now time.Time) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if t, ok := n.recentSent[fp]; ok && now.Sub(t) < notifyDupWindow {
		return true
	}
	return false
}

// markSent 记录一次成功发送，并顺带清理过期的指纹条目。
func (n *notifier) markSent(fp string, now time.Time) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.recentSent == nil {
		n.recentSent = make(map[string]time.Time)
	}
	n.recentSent[fp] = now
	if len(n.recentSent) > 512 {
		for k, t := range n.recentSent {
			if now.Sub(t) >= notifyDupWindow {
				delete(n.recentSent, k)
			}
		}
	}
}

// sendWithRetry 发送通知，失败后间隔 notifyRetryDelay 重试一次（O8-002）。
func (n *notifier) sendWithRetry(ctx context.Context, s notifySettings, msg notifyMessage) error {
	err := n.send(ctx, s, msg)
	if err == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return err
	case <-time.After(notifyRetryDelay):
	}
	if err2 := n.send(ctx, s, msg); err2 != nil {
		return err2
	}
	return nil
}

// notifySync 同步发送一条通知并返回是否发送成功（配置未启用也视为未发送）。
// 测试事件不参与去重，管理员可反复发送测试消息。
func (h *Handler) notifySync(event, title, message string) bool {
	if h == nil || h.repo == nil || h.notifier == nil {
		return false
	}
	settings, app, ok := h.notifyConfig(event)
	if !ok {
		return false
	}
	fp := notifyFingerprint(event, title, message)
	// O8-005：同一问题只提醒一次。内核错误等事件只按状态翻转触发，状态抖动
	// （error→正常→error）会重复发送相同内容，这里按内容指纹在窗口内抑制。
	if event != notifyEventTest && h.notifier.recentlySent(fp, time.Now()) {
		return false
	}
	msg := notifyMessage{Event: event, Title: title, Message: message, Time: time.Now().UnixMilli(), App: app}
	if err := h.notifier.sendWithRetry(context.Background(), settings, msg); err != nil {
		log.Printf("notify %s failed: %v", event, err)
		return false
	}
	if event != notifyEventTest {
		h.notifier.markSent(fp, time.Now())
	}
	return true
}

// notify sends an event in the background when it is enabled.
func (h *Handler) notify(event, title, message string) {
	go h.notifySync(event, title, message)
}

// send delivers to every configured channel and reports the failures.
func (n *notifier) send(ctx context.Context, s notifySettings, msg notifyMessage) error {
	var errs []string
	if s.TelegramToken != "" && s.TelegramChatID != "" {
		if err := n.sendTelegram(ctx, s, msg); err != nil {
			errs = append(errs, "Telegram: "+err.Error())
		}
	}
	if s.WebhookURL != "" {
		if err := n.sendWebhook(ctx, s.WebhookURL, msg); err != nil {
			errs = append(errs, "Webhook: "+err.Error())
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "；"))
	}
	return nil
}

func notifyText(msg notifyMessage) string {
	return fmt.Sprintf("[%s] %s\n%s\n时间：%s", msg.App, msg.Title, msg.Message,
		time.UnixMilli(msg.Time).Format("2006-01-02 15:04:05 MST"))
}

func (n *notifier) sendTelegram(ctx context.Context, s notifySettings, msg notifyMessage) error {
	base := strings.TrimRight(s.TelegramAPIBase, "/")
	if base == "" {
		base = defaultTelegramAPIBase
	}
	body, _ := json.Marshal(map[string]interface{}{
		"chat_id":                  s.TelegramChatID,
		"text":                     notifyText(msg),
		"disable_web_page_preview": true,
	})
	var out struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	status, raw, err := n.post(ctx, base+"/bot"+s.TelegramToken+"/sendMessage", body)
	if err != nil {
		return err
	}
	if json.Unmarshal(raw, &out) == nil && !out.OK && out.Description != "" {
		return errors.New(out.Description)
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("HTTP %d", status)
	}
	return nil
}

func (n *notifier) sendWebhook(ctx context.Context, url string, msg notifyMessage) error {
	payload := struct {
		notifyMessage
		Text string `json:"text"`
	}{msg, notifyText(msg)}
	body, _ := json.Marshal(payload)
	status, _, err := n.post(ctx, url, body)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("HTTP %d", status)
	}
	return nil
}

func (n *notifier) post(ctx context.Context, url string, body []byte) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, notifySendTimeout)
	defer cancel()
	// AUDIT-R26 (LAD26-001): Webhook 出站做 SSRF 校验。复用 secure_fetch 的
	// 元数据地址黑名单（isMetadataAddress），至少拒绝云元数据地址。
	// 注意：合法 webhook 常为内网地址（企业微信/钉钉内网代理），故此处仅硬拒绝
	// 元数据地址，不做全内网拒绝（产品决策：allowlist 通道后轮评估）。
	if err := validateNotifyURL(url); err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		// AUDIT-R26 (LAD26-017): URL 解析错误含完整 URL（可能带 bot token），
		// 脱敏后返回，与下方 client.Do() 错误处理一致。
		return 0, nil, errors.New("通知地址无效")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		// AUDIT-R27 (OBS27-001): CheckRedirect 返回的 validateNotifyURL 错误
		// 不含 URL/token（"通知地址..." 系列），应透出而非脱敏，便于管理员排查。
		// 其余 Do 错误可能含 URL（含 bot token），继续脱敏。
		// 注意：不能用 url.Error 类型断言，因参数名 url 遮蔽了 net/url 包；
		// 改用错误链消息匹配（validateNotifyURL 错误均为"通知地址"开头）。
		if isNotifyValidationErrorInChain(err) {
			// 从错误链中提取原始的校验错误
			for e := err; e != nil; e = errors.Unwrap(e) {
				if isNotifyValidationError(e) {
					return 0, nil, e
				}
			}
		}
		// The error text carries the URL, which holds the bot token.
		var timeoutErr interface{ Timeout() bool }
		if errors.As(err, &timeoutErr) && timeoutErr.Timeout() {
			return 0, nil, errors.New("请求超时")
		}
		return 0, nil, errors.New("无法连接")
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	return resp.StatusCode, raw, nil
}

// isNotifyValidationError 判断是否为 validateNotifyURL 返回的校验错误。
// 这类错误信息不含 URL/token（均为"通知地址..."固定文案），可安全透出。
func isNotifyValidationError(err error) bool {
	msg := err.Error()
	return strings.HasPrefix(msg, "通知地址")
}

// isNotifyValidationErrorInChain 检查错误链中是否包含通知校验错误。
func isNotifyValidationErrorInChain(err error) bool {
	for e := err; e != nil; e = errors.Unwrap(e) {
		if isNotifyValidationError(e) {
			return true
		}
	}
	return false
}

// validateNotifyURL 校验通知 webhook URL，防 SSRF。
// 拒绝：非 http/https scheme、云元数据地址（isMetadataAddress）。
// 允许：公网与内网地址（企业微信/钉钉内网代理为合法场景）。
func validateNotifyURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return errors.New("通知地址无效")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("通知地址 scheme 不支持: %s", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("通知地址主机为空")
	}
	// IP 字面量直接检查；域名解析后逐 IP 检查（fail-closed）。
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		ips, err = net.LookupIP(host)
		if err != nil || len(ips) == 0 {
			return fmt.Errorf("通知地址无法解析: %s", host)
		}
	}
	for _, ip := range ips {
		if isMetadataAddress(ip) {
			return errors.New("通知地址指向云元数据服务，已拒绝")
		}
	}
	return nil
}

func (h *Handler) nodeLabel(nodeID int64) string {
	if node, err := h.repo.GetNodeByID(nodeID); err == nil && node != nil && node.Name != "" {
		return node.Name
	}
	return fmt.Sprintf("节点 %d", nodeID)
}

// ---- node online / offline ----

// forgetNode 清理已删除节点的通知状态（O8-003/O12-002）：停止等待中的
// 掉线定时器（防止给已删节点发送幽灵"掉线"告警），清除告警标记，避免内存泄漏。
func (h *Handler) forgetNode(nodeID int64) {
	n := h.notifier
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if t := n.offlineTimers[nodeID]; t != nil {
		t.Stop()
		delete(n.offlineTimers, nodeID)
	}
	delete(n.offlineAlerted, nodeID)
	delete(n.expiryAlerted, nodeID)
}

// pruneStaleNodeAlerts 清理数据库中已不存在节点的通知状态（O8-003/O12-002）。
// 删除入口如果没有调用 forgetNode，这里兜底，防止定时器泄漏与幽灵告警。
func (h *Handler) pruneStaleNodeAlerts() {
	n := h.notifier
	if n == nil || h.repo == nil {
		return
	}
	nodes, err := h.repo.ListMonitorNodes()
	if err != nil {
		return
	}
	alive := make(map[int64]bool, len(nodes))
	for _, node := range nodes {
		alive[node.ID] = true
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for id := range n.offlineTimers {
		if !alive[id] {
			if t := n.offlineTimers[id]; t != nil {
				t.Stop()
			}
			delete(n.offlineTimers, id)
		}
	}
	for id := range n.offlineAlerted {
		if !alive[id] {
			delete(n.offlineAlerted, id)
		}
	}
	for id := range n.expiryAlerted {
		if !alive[id] {
			delete(n.expiryAlerted, id)
		}
	}
}

// checkStartupOfflineNodes 在面板启动时扫描仍离线的节点并补发通知（O8-001）。
// 离线状态全部在内存（offlineTimers/offlineAlerted），面板重启前已掉线的节点
// 在重启后从未"断开"过，因而永不告警。启动时等待一个离线宽限期（让 agent
// 重连），仍未上线的节点视为持续离线，发送一次"重启后仍离线"，并标记已告警
// 以便节点恢复在线时发送恢复通知。
func (h *Handler) checkStartupOfflineNodes() {
	if h.notifier == nil || h.wsServer == nil || h.repo == nil {
		return
	}
	nodes, err := h.repo.ListMonitorNodes()
	if err != nil {
		return
	}
	for _, node := range nodes {
		if node.Status != 1 {
			continue
		}
		if h.wsServer.IsNodeOnline(node.ID) {
			continue
		}
		h.notifier.mu.Lock()
		already := h.notifier.offlineAlerted[node.ID]
		if !already {
			h.notifier.offlineAlerted[node.ID] = true
		}
		h.notifier.mu.Unlock()
		if already {
			continue
		}
		h.notify(notifyEventNodeOffline, "节点掉线（面板重启后仍离线）", "节点："+node.Name)
	}
}

func (h *Handler) notifyNodeOffline(nodeID int64) {
	n := h.notifier
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if t := n.offlineTimers[nodeID]; t != nil {
		t.Stop()
	}
	n.offlineTimers[nodeID] = time.AfterFunc(nodeOfflineGrace, func() {
		n.mu.Lock()
		delete(n.offlineTimers, nodeID)
		if n.offlineAlerted[nodeID] || (h.wsServer != nil && h.wsServer.IsNodeOnline(nodeID)) {
			n.mu.Unlock()
			return
		}
		// O8-003：节点在宽限期内被删除则不发告警（否则会发出标签退化为
		// "节点 {id}" 的幽灵掉线通知）。删除入口应调用 forgetNode 提前清理，
		// 此处为发送侧的最终兜底。
		if node, err := h.repo.GetNodeByID(nodeID); err != nil || node == nil {
			n.mu.Unlock()
			return
		}
		n.offlineAlerted[nodeID] = true
		n.mu.Unlock()
		h.notify(notifyEventNodeOffline, "节点掉线", "节点："+h.nodeLabel(nodeID))
	})
}

func (h *Handler) notifyNodeOnline(nodeID int64) {
	n := h.notifier
	if n == nil {
		return
	}
	n.mu.Lock()
	if t := n.offlineTimers[nodeID]; t != nil {
		t.Stop()
		delete(n.offlineTimers, nodeID)
	}
	alerted := n.offlineAlerted[nodeID]
	delete(n.offlineAlerted, nodeID)
	n.mu.Unlock()
	if alerted {
		h.notify(notifyEventNodeOffline, "节点恢复在线", "节点："+h.nodeLabel(nodeID))
	}
}

// ---- node expiry ----

// checkNodeExpiry reports nodes expiring within notifyExpiryWindow once per
// expiry date; dismissed reminders stay quiet.
func (h *Handler) checkNodeExpiry(now time.Time) {
	if h.notifier == nil {
		return
	}
	nodes, err := h.repo.ListNodesWithExpiry()
	if err != nil {
		return
	}
	for _, node := range nodes {
		if !node.ExpiryTime.Valid || node.ExpiryReminderDismissed == 1 {
			continue
		}
		expiry := time.UnixMilli(node.ExpiryTime.Int64)
		left := expiry.Sub(now)
		if left > notifyExpiryWindow {
			continue
		}
		// O8-004：已过期很久的节点跳过，避免面板重启后把历史过期记录重新报一遍。
		if left < -notifyLongExpired {
			continue
		}
		h.notifier.mu.Lock()
		sent := h.notifier.expiryAlerted[node.ID] == node.ExpiryTime.Int64
		if !sent {
			h.notifier.expiryAlerted[node.ID] = node.ExpiryTime.Int64
		}
		h.notifier.mu.Unlock()
		if sent {
			continue
		}
		title := "节点即将到期"
		detail := fmt.Sprintf("剩余约 %d 天", int(left.Hours()/24)+1)
		if left <= 0 {
			title = "节点已到期"
			detail = "已过期"
		}
		h.notify(notifyEventNodeExpiry, title, fmt.Sprintf("节点：%s\n到期：%s（%s）",
			node.Name, expiry.Format("2006-01-02"), detail))
	}
}

// ---- test ----

// notifyTest sends a test message using the settings in the request, falling
// back to the saved ones, so the form can be checked before saving.
func (h *Handler) notifyTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req notifySettings
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	saved, app, _ := h.notifyConfig(notifyEventTest)
	if req.TelegramToken == "" {
		req.TelegramToken = saved.TelegramToken
	}
	if req.TelegramChatID == "" {
		req.TelegramChatID = saved.TelegramChatID
	}
	if req.TelegramAPIBase == "" {
		req.TelegramAPIBase = saved.TelegramAPIBase
	}
	if req.WebhookURL == "" {
		req.WebhookURL = saved.WebhookURL
	}
	req.TelegramToken = strings.TrimSpace(req.TelegramToken)
	req.TelegramChatID = strings.TrimSpace(req.TelegramChatID)
	req.WebhookURL = strings.TrimSpace(req.WebhookURL)
	if req.empty() {
		response.WriteJSON(w, response.ErrDefault("请先填写 Telegram Bot Token 和 Chat ID，或 Webhook 地址"))
		return
	}
	msg := notifyMessage{
		Event: notifyEventTest, Title: "测试通知",
		Message: "收到这条消息说明通知配置正确。", Time: time.Now().UnixMilli(), App: app,
	}
	if err := h.notifier.send(r.Context(), req, msg); err != nil {
		response.WriteJSON(w, response.ErrDefault("发送失败："+err.Error()))
		return
	}
	response.WriteJSON(w, response.OKEmpty())
}
