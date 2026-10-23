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

	"crypto/tls"

	"go-backend/internal/http/response"
	"go-backend/internal/netguard"
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
	// inflight 是正在派发中的指纹集合（A7-04）：旧实现只在发送成功后
	// markSent，同一通知被并发触发时全部穿透去重、各发一份；改为在
	// 派发前先占位，后到者直接按已抑制处理。
	inflight map[string]bool
	// delivered 是通道级投递台账（R2X-018）：指纹 → 通道 → 送达时刻。
	// 派发前以台账过滤待发通道、每通道成功即记账；全部已配置通道
	// 在账才 markSent 并清台账条目。与 recentSent/inflight 同为内存态
	// （调用方状态机本身即内存态，落库不能消除重启维度重复，见
	// R2-X-verdicts §2.3）。TTL 与 30 分钟去重窗口对齐。
	delivered map[string]map[string]time.Time
	// recoveryTimers / recoveryPending 是节点恢复通知发送失败后的
	// 重排派发（R2X-017）：节点持续在线时无任何周期路径会再调
	// notifyNodeOnline，置回 offlineAlerted 之外还须 AfterFunc 重排
	// 一次；recoveryPending 标记「恢复待发」，掉线路径会清掉它，
	// 重排回调据此复验节点未再掉线。
	recoveryTimers  map[int64]*time.Timer
	recoveryPending map[int64]bool
	client          *http.Client
}

func newNotifier() *notifier {
	return &notifier{
		offlineTimers:   make(map[int64]*time.Timer),
		offlineAlerted:  make(map[int64]bool),
		expiryAlerted:   make(map[int64]int64),
		alerted:         make(map[string]bool),
		recentSent:      make(map[string]time.Time),
		inflight:        make(map[string]bool),
		delivered:       make(map[string]map[string]time.Time),
		recoveryTimers:  make(map[int64]*time.Timer),
		recoveryPending: make(map[int64]bool),
		// AUDIT-R26 (R5R26-001): 重定向目标同样走 validateNotifyURL 校验，
		// 防 302/301 跳转到元数据地址绕过 LAD26-001 的 SSRF 控制。
		// 同时限制最多 5 跳，防重定向环。
		// R2X-044：出站改 pinned-dial（见 notifyDialContext），重定向
		// 每一跳都经同一钉死拨号逐跳复检+逐跳钉死。
		client: &http.Client{
			Timeout:   notifySendTimeout,
			Transport: notifyTransport(),
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

// notifyConfigCacheTTL 是通知配置缓存的有效期（D6-07）：旧实现每条
// 通知都 ListConfigs 全表扫一遍 vite_config。配置更新的两个写入口
// （updateConfigs/updateSingleConfig）会显式失效缓存，TTL 只兜底
// 其他写路径（如日报落盘键）。
const notifyConfigCacheTTL = 5 * time.Second

// cachedConfigs 返回 vite_config 的短时缓存副本。
func (h *Handler) cachedConfigs() (map[string]string, error) {
	h.notifyCfgMu.Lock()
	defer h.notifyCfgMu.Unlock()
	if h.notifyCfgVals != nil && time.Since(h.notifyCfgAt) < notifyConfigCacheTTL {
		return h.notifyCfgVals, nil
	}
	cfg, err := h.repo.ListConfigs()
	if err != nil {
		return nil, err
	}
	h.notifyCfgVals = cfg
	h.notifyCfgAt = time.Now()
	return cfg, nil
}

// invalidateNotifyConfigCache 在配置写成功后调用，使通知立即用新配置。
func (h *Handler) invalidateNotifyConfigCache() {
	if h == nil {
		return
	}
	h.notifyCfgMu.Lock()
	defer h.notifyCfgMu.Unlock()
	h.notifyCfgVals = nil
}

// notifyConfig reads the saved settings; ok is false when notifications
// are off or the event is not selected.
func (h *Handler) notifyConfig(event string) (notifySettings, string, bool) {
	cfg, err := h.cachedConfigs()
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
	// 集成修正（integration fix）：事件选择（notify_events）每次现读
	// 单行，不走 5s 缓存。理由：选择门是「发不发」的授权面——缓存
	// 陈旧的后果不是延迟发送而是误发未选事件，契约
	// TestNotifyImportedNodeDownContract 锁定其即时生效语义（该契约
	// 经直写 DB 改配置，绕过 updateConfigs 的失效钩子）；其余键仍按
	// FIX-D 的 D6-07 缓存语义（TTL 内直写为旧值、写入口失效后即时
	// 生效，TestFixdNotifyConfigCacheInvalidation 锁定）。现读失败
	// 时回退缓存值，不因一次查询失败丢通知。
	eventsValue := cfg[notifyConfigEvents]
	if row, ferr := h.repo.GetConfigByName(notifyConfigEvents); ferr == nil {
		if row == nil {
			eventsValue = ""
		} else {
			eventsValue = row.Value
		}
	}
	if events := strings.TrimSpace(eventsValue); events != "" {
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

// claimInflight 占位一个正在派发的指纹；已有同指纹在途时返回 false，
// 调用方按「已抑制」处理（在途的那一份代表本次触发投递）。
func (n *notifier) claimInflight(fp string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.inflight == nil {
		n.inflight = make(map[string]bool)
	}
	if n.inflight[fp] {
		return false
	}
	n.inflight[fp] = true
	return true
}

func (n *notifier) releaseInflight(fp string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.inflight, fp)
}

// forgetFingerprints 删除满足 match 的已发送指纹（A7-02）。
// 通知文档承诺「同一个问题只提醒一次，恢复后才会再次提醒」，但旧实现
// 的指纹窗口在恢复后仍继续拦截：掉线→恢复→30 分钟内再掉线的第二次
// 告警（及第二次恢复）被成对吞掉（T26 真机定案）。恢复送达时必须把
// 该问题的指纹清掉、重新武装下一次告警。
func (n *notifier) forgetFingerprints(match func(fp string) bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for fp := range n.recentSent {
		if match(fp) {
			delete(n.recentSent, fp)
		}
	}
	// R2X-018：通道台账必须连带清——否则恢复后同指纹的合法重发会被
	// 台账误判为「全部已送达」而吞掉（再武装失效）。
	for fp := range n.delivered {
		if match(fp) {
			delete(n.delivered, fp)
		}
	}
}

// configuredNotifyChannels 返回 settings 中实际配置完整的通道名集合，
// 与 sendChannelsRecord 的投递条件一致。
func configuredNotifyChannels(s notifySettings) []string {
	var out []string
	if s.TelegramToken != "" && s.TelegramChatID != "" {
		out = append(out, notifyChannelTelegram)
	}
	if s.WebhookURL != "" {
		out = append(out, notifyChannelWebhook)
	}
	return out
}

// ledgerPending 返回 fp 尚未在台账中送达（或台账条目已过期）的通道集，
// 形态与 sendChannelsRecord 的 only 参数一致（nil 之外的空集 = 无需投递）。
func (n *notifier) ledgerPending(fp string, configured []string, now time.Time) map[string]error {
	n.mu.Lock()
	defer n.mu.Unlock()
	pending := make(map[string]error)
	entry := n.delivered[fp]
	for _, ch := range configured {
		if t, ok := entry[ch]; ok && now.Sub(t) < notifyDupWindow {
			continue
		}
		pending[ch] = nil
	}
	return pending
}

// ledgerRecord 记一笔通道送达。
func (n *notifier) ledgerRecord(fp, channel string, now time.Time) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.delivered == nil {
		n.delivered = make(map[string]map[string]time.Time)
	}
	entry, ok := n.delivered[fp]
	if !ok {
		entry = make(map[string]time.Time)
		n.delivered[fp] = entry
	}
	entry[channel] = now
	if len(n.delivered) > 512 {
		for k, e := range n.delivered {
			fresh := false
			for _, t := range e {
				if now.Sub(t) < notifyDupWindow {
					fresh = true
					break
				}
			}
			if !fresh {
				delete(n.delivered, k)
			}
		}
	}
}

// ledgerClear 清掉一个指纹的台账条目（整条投递完成时）。
func (n *notifier) ledgerClear(fp string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.delivered, fp)
}

// forgetEventMessage 清掉某事件下消息体完全相同的所有指纹（不限标题：
// 同一问题的「掉线」与「恢复」标题不同、消息体同为「节点：X」）。
func (n *notifier) forgetEventMessage(event, message string) {
	prefix := event + "\x00"
	suffix := "\x00" + message
	n.forgetFingerprints(func(fp string) bool {
		return strings.HasPrefix(fp, prefix) && strings.HasSuffix(fp, suffix)
	})
}

// forgetFingerprintPrefix 清掉指纹前缀命中的条目（event\x00标题\x00消息前缀）。
func (n *notifier) forgetFingerprintPrefix(prefix string) {
	n.forgetFingerprints(func(fp string) bool {
		return strings.HasPrefix(fp, prefix)
	})
}

// forgetUserQuotaFlow 清掉某用户流量 warn/full 两子型的已发送指纹
// （R3X-030）：用量回落是 user_quota 流量子型的静默恢复，与
// kernel_error 静默恢复（A7-02）在恢复点补 forget 同口径——否则
// alertOnce 已被 clearAlerts 再武装、指纹却仍在 30 分钟窗内把同文案
// 再越界成对吞掉。消息体含已用字节的格式化值，故按「标题 + 消息前缀
// （用户：<名>\n）」清，与 kernel 恢复点的前缀清法一致。到期类子型
// 标题不同（用户即将到期/已到期），不被波及。
func (n *notifier) forgetUserQuotaFlow(user string) {
	msgPrefix := "用户：" + user + "\n"
	n.forgetFingerprintPrefix(notifyEventUserQuota + "\x00用户流量已用完\x00" + msgPrefix)
	n.forgetFingerprintPrefix(notifyEventUserQuota + "\x00用户流量即将用完\x00" + msgPrefix)
}

// forgetSubQuotaFlow 是 forgetUserQuotaFlow 的订阅面对偶：清掉某分享
// 订阅流量 warn/full 两子型的指纹，到期类子型（分享订阅即将到期/
// 已到期）不被波及。
func (n *notifier) forgetSubQuotaFlow(name string) {
	msgPrefix := "订阅：" + name + "\n"
	n.forgetFingerprintPrefix(notifyEventUserQuota + "\x00分享订阅流量已用完，已断开\x00" + msgPrefix)
	n.forgetFingerprintPrefix(notifyEventUserQuota + "\x00分享订阅流量即将用完\x00" + msgPrefix)
}

// clearExpiryAlert 回滚节点到期告警的已标记状态（A7-03：发送失败不置位）。
func (n *notifier) clearExpiryAlert(nodeID int64, expiryMs int64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.expiryAlerted[nodeID] == expiryMs {
		delete(n.expiryAlerted, nodeID)
	}
}

// 通知通道名（sendChannelsRecord 的 only/failed 集合键）。
const (
	notifyChannelTelegram = "telegram"
	notifyChannelWebhook  = "webhook"
)

// sendWithRetry 发送通知，失败后间隔 notifyRetryDelay 重试一次（O8-002）。
// A7-04：重试粒度按通道——旧实现以整条消息为粒度，单通道失败会把已
// 成功的通道再发一份（一次告警变两份；日报补发时每 tick 再叠加）。
// 现改为只重发仍失败的通道，成功通道每次调用至多投递一次。
func (n *notifier) sendWithRetry(ctx context.Context, s notifySettings, msg notifyMessage) error {
	return n.sendWithRetryRecord(ctx, s, msg, nil, nil)
}

// sendWithRetryRecord 与 sendWithRetry 相同，但首轮只投 only 指定的
// 通道集（nil = 全部），且每通道投递成功即回调 record（通道台账记账）。
func (n *notifier) sendWithRetryRecord(ctx context.Context, s notifySettings, msg notifyMessage, only map[string]error, record func(channel string)) error {
	failed := n.sendChannelsRecord(ctx, s, msg, only, record)
	if len(failed) == 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return notifyChannelsError(failed)
	case <-time.After(notifyRetryDelay):
	}
	failed = n.sendChannelsRecord(ctx, s, msg, failed, record)
	if len(failed) > 0 {
		return notifyChannelsError(failed)
	}
	return nil
}

// sendChannelsRecord 向 only 指定的通道集合投递（nil = 全部已配置
// 通道；only 只看键集合，其值即上轮的错误映射，可直接回喂），返回
// 仍失败的通道→错误映射；每通道成功即回调 record（可为 nil）。
func (n *notifier) sendChannelsRecord(ctx context.Context, s notifySettings, msg notifyMessage, only map[string]error, record func(channel string)) map[string]error {
	failed := make(map[string]error)
	want := func(ch string) bool {
		if only == nil {
			return true
		}
		_, ok := only[ch]
		return ok
	}
	if want(notifyChannelTelegram) && s.TelegramToken != "" && s.TelegramChatID != "" {
		if err := n.sendTelegram(ctx, s, msg); err != nil {
			failed[notifyChannelTelegram] = err
		} else if record != nil {
			record(notifyChannelTelegram)
		}
	}
	if want(notifyChannelWebhook) && s.WebhookURL != "" {
		if err := n.sendWebhook(ctx, s.WebhookURL, msg); err != nil {
			failed[notifyChannelWebhook] = err
		} else if record != nil {
			record(notifyChannelWebhook)
		}
	}
	return failed
}

// notifyChannelsError 把按通道的失败拼成与旧 send 相同的文案
// （"Telegram: ...；Webhook: ..."）。
func notifyChannelsError(failed map[string]error) error {
	var errs []string
	if err, ok := failed[notifyChannelTelegram]; ok {
		errs = append(errs, "Telegram: "+err.Error())
	}
	if err, ok := failed[notifyChannelWebhook]; ok {
		errs = append(errs, "Webhook: "+err.Error())
	}
	if len(errs) == 0 {
		return nil
	}
	return errors.New(strings.Join(errs, "；"))
}

// notifyOutcome 是一次派发的结果，供告警状态机决定是否置位（A7-03）。
type notifyOutcome int

const (
	// notifyOutcomeFailed 实际尝试发送但失败（通道故障）——状态机必须
	// 回滚已标记状态，让问题在下一轮检查/触发时重新告警（BC-16）。
	notifyOutcomeFailed notifyOutcome = iota
	// notifyOutcomeSent 已投递成功。
	notifyOutcomeSent
	// notifyOutcomeSuppressed 被去重抑制：窗口内同指纹已成功发送过，
	// 或有同指纹正在派发。本问题视为已通知，状态标记成立。
	notifyOutcomeSuppressed
	// notifyOutcomeDisabled 通知未启用或事件未勾选（配置面决定，
	// 不是发送失败）：保持既有标记语义，避免每轮重复无谓尝试。
	notifyOutcomeDisabled
)

// dispatchNotify 执行一次派发：配置检查 → 指纹去重（含在途占位）→
// 按通道发送。测试事件不参与去重，管理员可反复发送测试消息。
func (h *Handler) dispatchNotify(ctx context.Context, event, title, message string) notifyOutcome {
	if h == nil || h.repo == nil || h.notifier == nil {
		return notifyOutcomeFailed
	}
	settings, app, ok := h.notifyConfig(event)
	if !ok {
		return notifyOutcomeDisabled
	}
	fp := notifyFingerprint(event, title, message)
	// O8-005：同一问题只提醒一次。内核错误等事件只按状态翻转触发，状态抖动
	// （error→正常→error）会重复发送相同内容，这里按内容指纹在窗口内抑制。
	if event != notifyEventTest {
		if h.notifier.recentlySent(fp, time.Now()) {
			return notifyOutcomeSuppressed
		}
		// A7-04：并发同指纹只有一个能占位派发，其余按已抑制处理，
		// 不再出现 16 个并发各发一份的穿透。
		if !h.notifier.claimInflight(fp) {
			return notifyOutcomeSuppressed
		}
		defer h.notifier.releaseInflight(fp)
	}
	msg := notifyMessage{Event: event, Title: title, Message: message, Time: time.Now().UnixMilli(), App: app}
	// R9X-006：test 事件不经 dispatch（全仓无调用方以 test 事件走
	// dispatchNotify，原 test 重试分支不可达、已删）——测试端点
	// notifyTest 用请求内未保存设置直调 sendWithRetry，同一重试语义。
	// R2X-018 通道级台账：只投台账中尚未送达的通道，每通道成功即记账；
	// 全部已配置通道都在账才算整条送达——markSent 并清台账条目。
	// 任一通道仍失败时整条 Failed（状态机回滚后补发），但已送达通道
	// 下一轮被台账过滤、不再重复收。
	configured := configuredNotifyChannels(settings)
	pending := h.notifier.ledgerPending(fp, configured, time.Now())
	if len(pending) > 0 {
		err := h.notifier.sendWithRetryRecord(ctx, settings, msg, pending, func(channel string) {
			h.notifier.ledgerRecord(fp, channel, time.Now())
		})
		if err != nil {
			log.Printf("notify %s failed: %v", event, err)
			return notifyOutcomeFailed
		}
	}
	h.notifier.markSent(fp, time.Now())
	h.notifier.ledgerClear(fp)
	return notifyOutcomeSent
}

// notifySync 同步派发并返回是否可视为已通知（日报落盘判定沿用此口径：
// 成功，或窗口内刚成功发送过同一条）。
func (h *Handler) notifySync(event, title, message string) bool {
	outcome := h.dispatchNotify(context.Background(), event, title, message)
	return outcome == notifyOutcomeSent || outcome == notifyOutcomeSuppressed
}

// notifyQueueSize 是派发队列容量（D6-07）：旧实现每条通知起一个无界
// goroutine 且用 context.Background() 发送，节点群集体抖动时 goroutine
// 与配置全表扫描同时放大。现改为有界队列 + 固定 worker 池，worker 的
// ctx 归 jobs 管辖（派生自 StartBackgroundJobs 的 ctx）。
const notifyQueueSize = 128

const notifyWorkerCount = 4

type notifyJob struct {
	event, title, message string
	onDone                func(notifyOutcome)
}

// ensureNotifyDispatcher 懒启动派发 worker（StartBackgroundJobs 会显式
// 调用以绑定 jobs ctx；其余路径——如单元测试与 jobs 启动前——首次通知
// 时以当前可用 ctx 启动）。
func (h *Handler) ensureNotifyDispatcher() {
	if h == nil {
		return
	}
	h.notifyMu.Lock()
	defer h.notifyMu.Unlock()
	if h.notifyRunning {
		return
	}
	if h.notifyQueue == nil {
		h.notifyQueue = make(chan notifyJob, notifyQueueSize)
	}
	h.jobsMu.Lock()
	base := h.jobsCtx
	h.jobsMu.Unlock()
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithCancel(base)
	h.notifyCancel = cancel
	h.notifyRunning = true
	for i := 0; i < notifyWorkerCount; i++ {
		h.notifyWG.Add(1)
		go h.runNotifyWorker(ctx)
	}
}

// stopNotifyDispatcher 停掉派发 worker（StopBackgroundJobs 调用）。
func (h *Handler) stopNotifyDispatcher() {
	if h == nil {
		return
	}
	h.notifyMu.Lock()
	cancel := h.notifyCancel
	h.notifyCancel = nil
	h.notifyMu.Unlock()
	if cancel != nil {
		cancel()
	}
	h.notifyWG.Wait()
	h.notifyMu.Lock()
	h.notifyRunning = false
	h.notifyMu.Unlock()
}

func (h *Handler) runNotifyWorker(ctx context.Context) {
	defer h.notifyWG.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-h.notifyQueue:
			outcome := h.dispatchNotify(ctx, job.event, job.title, job.message)
			if job.onDone != nil {
				job.onDone(outcome)
			}
		}
	}
}

// enqueueNotify 把一条通知放入有界队列；队列满时按发送失败处理
// （回滚状态机标记并记日志），绝不阻塞调用方、绝不无界起 goroutine。
func (h *Handler) enqueueNotify(job notifyJob) {
	if h == nil || h.notifier == nil {
		if job.onDone != nil {
			job.onDone(notifyOutcomeFailed)
		}
		return
	}
	h.ensureNotifyDispatcher()
	h.notifyMu.Lock()
	queue := h.notifyQueue
	h.notifyMu.Unlock()
	select {
	case queue <- job:
	default:
		log.Printf("notify %s dropped: dispatch queue full", job.event)
		if job.onDone != nil {
			job.onDone(notifyOutcomeFailed)
		}
	}
}

// notify sends an event in the background when it is enabled.
func (h *Handler) notify(event, title, message string) {
	h.enqueueNotify(notifyJob{event: event, title: title, message: message})
}

// notifyTracked 与 notify 相同，但派发结束后以 outcome 回调调用方，
// 供告警状态机实现「发送成功才置位、失败回滚重试」（A7-03/BC-16）。
func (h *Handler) notifyTracked(event, title, message string, onDone func(notifyOutcome)) {
	h.enqueueNotify(notifyJob{event: event, title: title, message: message, onDone: onDone})
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
	// AUDIT-R26 (LAD26-001): Webhook 出站做 SSRF 校验。硬拒集与
	// SecureFetch 共用 netguard 硬拒层（isMetadataAddress，R2X-026）。
	// 注意：合法 webhook 常为内网地址（企业微信/钉钉内网代理），故此处仅硬拒绝
	// 硬拒层地址，不做全内网拒绝（产品决策：allowlist 通道后轮评估）。
	// 发送走钉死拨号（notifyDialContext，R2X-044），校验与拨号同一次解析。
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

// notifyLookupIP 是通知出站的解析入口（var 以便测试注入，含
// rebinding 场景）。校验与钉死拨号共用同一入口。
var notifyLookupIP = net.LookupIP

// notifyTransport 构造钉死拨号传输（R2X-044）：Proxy 关闭（经代理
// 出站时目标由代理解析，钉死失效，与联邦传输同口径）；TLS 最低
// 1.2，SNI/Host 由 http.Transport 按原主机名保留（DialContext 只
// 负责把已校验 IP 连上）。
func notifyTransport() *http.Transport {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.DialContext = notifyDialContext
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	return tr
}

// notifyDialContext 是通知出站的钉死拨号：解析→逐 IP 过 netguard
// 硬拒层→拨已校验 IP。此前 notify 是全仓唯一「校验与发送两次
// 解析」的出站面，DNS rebinding 可在校验后把目标翻到元数据地址
// （R2X-044）。任一解析结果命中硬拒整体不拨。
func notifyDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		ips, err = notifyLookupIP(host)
		if err != nil || len(ips) == 0 {
			return nil, fmt.Errorf("通知地址无法解析: %s", host)
		}
	}
	for _, ip := range ips {
		if d := netguard.Judge(ip, netguard.PolicyHardDenyOnly, false); !d.Allowed {
			return nil, errors.New("通知地址指向云元数据服务，已拒绝")
		}
	}
	dialer := &net.Dialer{Timeout: notifySendTimeout}
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

// validateNotifyURL 校验通知 webhook URL，防 SSRF。
// 拒绝：非 http/https scheme、硬拒层地址（isMetadataAddress，现为
// netguard 硬拒集：元数据精确集 ∪ 169.254/16 整段 ∪ 未指定）。
// 允许：公网与内网地址（企业微信/钉钉内网代理为合法场景）。
// fe80::/10 不在硬拒层（R3X-010 政策分层）：本面用
// PolicyHardDenyOnly，与 1.3.7 钉版口径一致，不另设拒。
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
		ips, err = notifyLookupIP(host)
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
	if t := n.recoveryTimers[nodeID]; t != nil {
		t.Stop()
		delete(n.recoveryTimers, nodeID)
	}
	delete(n.recoveryPending, nodeID)
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
		h.dispatchNodeOfflineAlert(node.ID, "节点掉线（面板重启后仍离线）")
	}
}

// notifyOfflineRetryDelay 是掉线告警发送失败后的重排间隔（A7-03）。
var notifyOfflineRetryDelay = time.Minute

// notifyOnlineRetryDelay 是恢复通知发送失败后的重排间隔（R2X-017），
// 与掉线向重排同款 1 分钟。
var notifyOnlineRetryDelay = time.Minute

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
	// 节点再掉线：取消待发的恢复补发（R2X-017）——节点已不在线，
	// 恢复消息不得再被重排定时器送出。
	if t := n.recoveryTimers[nodeID]; t != nil {
		t.Stop()
		delete(n.recoveryTimers, nodeID)
	}
	delete(n.recoveryPending, nodeID)
	n.offlineTimers[nodeID] = time.AfterFunc(nodeOfflineGrace, func() {
		h.dispatchNodeOfflineAlert(nodeID, "节点掉线")
	})
}

// dispatchNodeOfflineAlert 校验节点仍离线/仍存在后标记已告警并派发；
// 发送失败时回滚标记并重排一次延迟重试（A7-03/BC-16：旧实现先标记后
// 异步发送，通道短暂故障会把整段掉线告警永久吞掉）。title 区分首次
// 掉线与面板重启后仍离线两种文案。节点标签取自 DB 行名（调用方语境）。
func (h *Handler) dispatchNodeOfflineAlert(nodeID int64, title string) {
	n := h.notifier
	if n == nil {
		return
	}
	n.mu.Lock()
	delete(n.offlineTimers, nodeID)
	if n.offlineAlerted[nodeID] || (h.wsServer != nil && h.wsServer.IsNodeOnline(nodeID)) {
		n.mu.Unlock()
		return
	}
	// O8-003：节点在宽限期内被删除则不发告警（否则会发出标签退化为
	// "节点 {id}" 的幽灵掉线通知）。删除入口应调用 forgetNode 提前清理，
	// 此处为发送侧的最终兜底。
	node, err := h.repo.GetNodeByID(nodeID)
	if err != nil || node == nil {
		n.mu.Unlock()
		return
	}
	n.offlineAlerted[nodeID] = true
	n.mu.Unlock()
	msg := "节点：" + node.Name
	if title == "节点掉线" {
		// 首次掉线沿用 nodeLabel（与历史文案一致，含名称回退）。
		msg = "节点：" + h.nodeLabel(nodeID)
	}
	h.notifyTracked(notifyEventNodeOffline, title, msg, func(outcome notifyOutcome) {
		if outcome != notifyOutcomeFailed {
			return
		}
		n.mu.Lock()
		defer n.mu.Unlock()
		if !n.offlineAlerted[nodeID] || (h.wsServer != nil && h.wsServer.IsNodeOnline(nodeID)) {
			return
		}
		delete(n.offlineAlerted, nodeID)
		if _, pending := n.offlineTimers[nodeID]; !pending {
			n.offlineTimers[nodeID] = time.AfterFunc(notifyOfflineRetryDelay, func() {
				h.dispatchNodeOfflineAlert(nodeID, title)
			})
		}
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
	// 本次上线即一次新的恢复派发：清掉上一轮失败遗留的重排定时器，
	// 防其稍后重复派发（重排回调走的也是本函数，先清再验）。
	if t := n.recoveryTimers[nodeID]; t != nil {
		t.Stop()
		delete(n.recoveryTimers, nodeID)
	}
	delete(n.recoveryPending, nodeID)
	alerted := n.offlineAlerted[nodeID]
	delete(n.offlineAlerted, nodeID)
	n.mu.Unlock()
	if alerted {
		label := h.nodeLabel(nodeID)
		h.notifyTracked(notifyEventNodeOffline, "节点恢复在线", "节点："+label, func(outcome notifyOutcome) {
			if outcome == notifyOutcomeSent || outcome == notifyOutcomeSuppressed {
				// A7-02（BC-15）：恢复送达后清掉该节点的问题指纹——
				// 掉线与恢复同事件、同消息体（差异只在标题，经
				// forgetEventMessage 一并清除）。否则指纹窗口内第二次
				// 掉线与第二次恢复会被成对吞掉（T26 真机定案）。
				// 未恢复的问题其指纹一直有效，防抖语义不变。
				h.notifier.forgetEventMessage(notifyEventNodeOffline, "节点："+label)
				return
			}
			// R2X-017：恢复通知发送失败不得消费跃迁。只置回
			// offlineAlerted 不够——节点持续在线时无任何周期路径会
			// 再调 notifyNodeOnline——须同时按掉线向同款 AfterFunc
			// 重排一次恢复派发。Disabled 不在此列（V-16 维持消费语义）。
			if outcome != notifyOutcomeFailed {
				return
			}
			n.mu.Lock()
			defer n.mu.Unlock()
			// 期间节点再掉线（offlineAlerted 已由掉线向重新置位且
			// 有待发掉线定时器）或节点已被删除时不重排。
			if _, offlinePending := n.offlineTimers[nodeID]; offlinePending {
				return
			}
			n.offlineAlerted[nodeID] = true
			n.recoveryPending[nodeID] = true
			if t := n.recoveryTimers[nodeID]; t != nil {
				t.Stop()
			}
			n.recoveryTimers[nodeID] = time.AfterFunc(notifyOnlineRetryDelay, func() {
				h.dispatchNodeRecoveryRetry(nodeID)
			})
		})
	}
}

// dispatchNodeRecoveryRetry 是恢复通知失败后的重排派发：复验节点仍
// 在线（恢复待发标记仍在——掉线路径会清掉它并停定时器；且无待发
// 的掉线定时器）且 offlineAlerted 仍置位，防竞态双发；通过后走
// notifyNodeOnline 正常派发（其 Failed 回调会再次重排）。
func (h *Handler) dispatchNodeRecoveryRetry(nodeID int64) {
	n := h.notifier
	if n == nil {
		return
	}
	n.mu.Lock()
	delete(n.recoveryTimers, nodeID)
	alerted := n.offlineAlerted[nodeID]
	pending := n.recoveryPending[nodeID]
	_, offlinePending := n.offlineTimers[nodeID]
	n.mu.Unlock()
	if !alerted || !pending || offlinePending {
		return
	}
	// 节点已被删除时不再补发（与掉线向派发的存在性兜底同口径）。
	if h.repo != nil {
		if node, err := h.repo.GetNodeByID(nodeID); err != nil || node == nil {
			n.mu.Lock()
			delete(n.offlineAlerted, nodeID)
			delete(n.recoveryPending, nodeID)
			n.mu.Unlock()
			return
		}
	}
	h.notifyNodeOnline(nodeID)
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
		nodeID, expiryMs := node.ID, node.ExpiryTime.Int64
		h.notifyTracked(notifyEventNodeExpiry, title, fmt.Sprintf("节点：%s\n到期：%s（%s）",
			node.Name, expiry.Format("2006-01-02"), detail), func(outcome notifyOutcome) {
			// A7-03：发送失败回滚标记，下一轮 10 分钟检查时重试。
			if outcome == notifyOutcomeFailed {
				h.notifier.clearExpiryAlert(nodeID, expiryMs)
			}
		})
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
	if err := h.notifier.sendWithRetry(r.Context(), req, msg); err != nil {
		response.WriteJSON(w, response.ErrDefault("发送失败："+err.Error()))
		return
	}
	response.WriteJSON(w, response.OKEmpty())
}
