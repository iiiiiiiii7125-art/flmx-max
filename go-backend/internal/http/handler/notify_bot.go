package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Telegram 机器人：菜单＋自主查询（getUpdates 长轮询）。
//
// 接收面与通知派发面（notify.go）分离：派发只调 sendMessage 单向
// 外发，本文件补上 getUpdates / setMyCommands / answerCallbackQuery。
// 复用 notifier 的 http client 传输（pinned-dial＋重定向逐跳复检）
// 与 validateNotifyURL，不自造裸 client。唯一例外是长轮询用的
// client 不带 notifier 的 10s Timeout——30s 长轮询会被它提前掐断；
// Transport 与 CheckRedirect 与 notifier 共用同一份，超时改由 35s
// 的请求 ctx 控制（见 telegramBotHTTPClient）。
//
// 安全：token 在 URL 里，任何错误文案一律脱敏（不含 URL）；只认
// 配置里那个 chat（TelegramChatID），其他 chat 的消息与按钮回调
// 一律静默忽略、不回复，防陌生人枚举节点/流量。

const (
	// telegramBotPollTimeout 是单次 getUpdates 的长轮询时长（秒，
	// Telegram 侧参数）。
	telegramBotPollTimeout = 30
	// telegramBotRequestTimeout 是单次 getUpdates 的 HTTP 超时。
	telegramBotRequestTimeout = 35 * time.Second
	// telegramBotRetryDelay 是轮询出错后的退避。
	telegramBotRetryDelay = 5 * time.Second
	// telegramBotIdleDelay 是未配置 Telegram 时的复查间隔。
	telegramBotIdleDelay = 30 * time.Second
	// telegramBotEmptyDelay 是空轮询后的短暂停，避免服务端秒回时空转。
	telegramBotEmptyDelay = time.Second
	// telegramBotLatencyFresh 是 /nodes 显示延迟的保鲜窗：探针结果
	// 超过此时长即视为过期、不展示。
	telegramBotLatencyFresh = 15 * time.Minute
)

// telegramBotSettings 是机器人运行所需的 Telegram 配置（通知配置的
// 子集，不含事件选择门——查询不是通知）。
type telegramBotSettings struct {
	Token   string
	ChatID  string
	APIBase string
}

func (s telegramBotSettings) base() string {
	base := strings.TrimRight(s.APIBase, "/")
	if base == "" {
		base = defaultTelegramAPIBase
	}
	return base
}

// telegramBotSettingsOf 从配置缓存读机器人配置；ok 为 false 时表示
// 未启用或未配齐（通知总开关关、或 token/chat 任一为空），轮询保
// 持休眠、等配置出现。
func (h *Handler) telegramBotSettingsOf() (telegramBotSettings, bool) {
	if h == nil || h.repo == nil {
		return telegramBotSettings{}, false
	}
	cfg, err := h.cachedConfigs()
	if err != nil {
		return telegramBotSettings{}, false
	}
	s := telegramBotSettings{
		Token:   strings.TrimSpace(cfg[notifyConfigTGToken]),
		ChatID:  strings.TrimSpace(cfg[notifyConfigTGChatID]),
		APIBase: strings.TrimSpace(cfg[notifyConfigTGAPIBase]),
	}
	if cfg[notifyConfigEnabled] != "true" || s.Token == "" || s.ChatID == "" {
		return s, false
	}
	return s, true
}

// runTelegramBotLoop 是 Telegram 机器人轮询循环，随
// StartBackgroundJobs 启动、随 jobsCtx 停。未配置时休眠复查；配
// 置变更时重注册命令；换 token 时重置 offset 并从最新一条之后开
// 始（不重放积压的旧命令）。
func (h *Handler) runTelegramBotLoop(ctx context.Context) {
	defer h.jobsWG.Done()
	if h == nil || h.notifier == nil {
		return
	}
	var offset int64
	var applied telegramBotSettings
	haveApplied := false
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		settings, ok := h.telegramBotSettingsOf()
		if !ok {
			if !sleepOrDone(ctx, telegramBotIdleDelay) {
				return
			}
			continue
		}
		if !haveApplied || settings != applied {
			if err := h.telegramBotRegisterCommands(ctx, settings); err != nil {
				log.Printf("telegram bot: register commands failed: %v", err)
				if !sleepOrDone(ctx, telegramBotRetryDelay) {
					return
				}
				continue
			}
			if haveApplied && settings.Token != applied.Token {
				offset = 0
			}
			next, err := h.telegramBotCatchUp(ctx, settings)
			if err != nil {
				log.Printf("telegram bot: catch-up failed: %v", err)
				if !sleepOrDone(ctx, telegramBotRetryDelay) {
					return
				}
				continue
			}
			if next > offset {
				offset = next
			}
			applied = settings
			haveApplied = true
		}
		updates, err := h.telegramBotFetch(ctx, settings, offset)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("telegram bot: poll failed: %v", err)
			if !sleepOrDone(ctx, telegramBotRetryDelay) {
				return
			}
			continue
		}
		for _, u := range updates {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			h.telegramBotHandleUpdate(ctx, settings, u)
		}
		if len(updates) == 0 {
			if !sleepOrDone(ctx, telegramBotEmptyDelay) {
				return
			}
		}
	}
}

// sleepOrDone 等待 d 或 ctx 取消；返回 false 表示应退出。
func sleepOrDone(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// telegramBotCatchUp 取最新一条 update 的 id+1，供重启/换号后跳过
// 积压的旧命令。无 update 时返回 0（offset 不变）。
func (h *Handler) telegramBotCatchUp(ctx context.Context, s telegramBotSettings) (int64, error) {
	updates, err := h.telegramBotFetchRaw(ctx, s, -1, 1)
	if err != nil {
		return 0, err
	}
	if len(updates) == 0 {
		return 0, nil
	}
	return updates[0].UpdateID + 1, nil
}

// telegramBotFetch 取一批 update（offset 为 0 表示从头取——仅在
// catch-up 无 update 的冷启动时出现）。
func (h *Handler) telegramBotFetch(ctx context.Context, s telegramBotSettings, offset int64) ([]telegramUpdate, error) {
	return h.telegramBotFetchRaw(ctx, s, offset, 0)
}

func (h *Handler) telegramBotFetchRaw(ctx context.Context, s telegramBotSettings, offset int64, limit int) ([]telegramUpdate, error) {
	u := s.base() + "/bot" + s.Token + "/getUpdates?timeout=" + strconv.Itoa(telegramBotPollTimeout)
	if offset != 0 {
		u += "&offset=" + strconv.FormatInt(offset, 10)
	}
	if limit > 0 {
		u += "&limit=" + strconv.Itoa(limit)
	}
	status, raw, err := h.telegramBotGet(ctx, u)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("HTTP %d", status)
	}
	var resp telegramUpdatesResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, errors.New("getUpdates 响应解析失败")
	}
	if !resp.OK {
		// Telegram 自身的错误文案（401 未授权等）不含 token，可透出
		// 以便管理员定位配置问题。
		if resp.Description != "" {
			return nil, fmt.Errorf("getUpdates 被拒绝：%s", resp.Description)
		}
		return nil, errors.New("getUpdates 被拒绝")
	}
	return resp.Result, nil
}

// telegramBotGet 是机器人侧的 GET（getUpdates）：复用 notifier
// client 的传输与重定向校验（pinned-dial＋逐跳复检），超时由调用
// 方 ctx 控制（35s）。错误一律脱敏——URL 含 token，绝不原样返回。
func (h *Handler) telegramBotGet(ctx context.Context, url string) (int, []byte, error) {
	if h == nil || h.notifier == nil || h.notifier.client == nil || h.notifier.client.Transport == nil {
		return 0, nil, errors.New("notifier 未初始化")
	}
	ctx, cancel := context.WithTimeout(ctx, telegramBotRequestTimeout)
	defer cancel()
	if err := validateNotifyURL(url); err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, errors.New("通知地址无效")
	}
	resp, err := telegramBotHTTPClient(h.notifier.client).Do(req)
	if err != nil {
		var timeoutErr interface{ Timeout() bool }
		if errors.As(err, &timeoutErr) && timeoutErr.Timeout() {
			return 0, nil, errors.New("请求超时")
		}
		if isNotifyValidationErrorInChain(err) {
			for e := err; e != nil; e = errors.Unwrap(e) {
				if isNotifyValidationError(e) {
					return 0, nil, e
				}
			}
		}
		return 0, nil, errors.New("无法连接")
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, raw, nil
}

// telegramBotHTTPClient 复用 src 的传输与重定向策略，只去掉 10s
// 的 client.Timeout（长轮询需要 35s；超时改由请求 ctx 控制）。
func telegramBotHTTPClient(src *http.Client) *http.Client {
	return &http.Client{Transport: src.Transport, CheckRedirect: src.CheckRedirect}
}

// telegramBotPost 调 Bot API 的 POST 方法（sendMessage、
// setMyCommands、answerCallbackQuery），复用 notifier.post 的校验、
// 钉死拨号与脱敏。
func (h *Handler) telegramBotPost(ctx context.Context, s telegramBotSettings, method string, body []byte) (int, []byte, error) {
	if h == nil || h.notifier == nil {
		return 0, nil, errors.New("notifier 未初始化")
	}
	return h.notifier.post(ctx, s.base()+"/bot"+s.Token+"/"+method, body)
}

// telegramBotRegisterCommands 向 Telegram 注册命令菜单（/start、
// /nodes、/traffic、/status），配置变更时重调（幂等）。
func (h *Handler) telegramBotRegisterCommands(ctx context.Context, s telegramBotSettings) error {
	body, _ := json.Marshal(map[string]interface{}{
		"commands": []map[string]string{
			{"command": "start", "description": "打开菜单"},
			{"command": "nodes", "description": "节点状态"},
			{"command": "traffic", "description": "流量汇总"},
			{"command": "status", "description": "面板状态"},
		},
	})
	status, raw, err := h.telegramBotPost(ctx, s, "setMyCommands", body)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("HTTP %d", status)
	}
	var out struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return errors.New("setMyCommands 响应解析失败")
	}
	if !out.OK {
		if out.Description != "" {
			return fmt.Errorf("setMyCommands 被拒绝：%s", out.Description)
		}
		return errors.New("setMyCommands 被拒绝")
	}
	return nil
}

// telegramBotSendMessage 发一条 HTML 格式的机器人回复。text 须已
// 是合法 HTML（调用方负责转义数据）；markup 为 inline keyboard
// （nil＝无按钮）。
func (h *Handler) telegramBotSendMessage(ctx context.Context, s telegramBotSettings, chatID int64, text string, markup *telegramInlineKeyboard) error {
	payload := map[string]interface{}{
		"chat_id":                  chatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}
	if markup != nil {
		payload["reply_markup"] = markup
	}
	body, _ := json.Marshal(payload)
	status, raw, err := h.telegramBotPost(ctx, s, "sendMessage", body)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("HTTP %d", status)
	}
	var out struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return errors.New("sendMessage 响应解析失败")
	}
	if !out.OK {
		if out.Description != "" {
			return fmt.Errorf("sendMessage 被拒绝：%s", out.Description)
		}
		return errors.New("sendMessage 被拒绝")
	}
	return nil
}

// telegramBotAnswerCallback 应答按钮回调（先调，消掉客户端的
// loading 圈；空文本＝无提示）。
func (h *Handler) telegramBotAnswerCallback(ctx context.Context, s telegramBotSettings, callbackID string) error {
	body, _ := json.Marshal(map[string]interface{}{"callback_query_id": callbackID})
	status, raw, err := h.telegramBotPost(ctx, s, "answerCallbackQuery", body)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("HTTP %d", status)
	}
	var out struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return errors.New("answerCallbackQuery 响应解析失败")
	}
	if !out.OK {
		return errors.New("answerCallbackQuery 被拒绝")
	}
	return nil
}

// telegramBotHandleUpdate 处理一条 update；单个 update 的任何失败
// 都只记日志、不影响轮询（recover 兜底 panic）。
func (h *Handler) telegramBotHandleUpdate(ctx context.Context, s telegramBotSettings, u telegramUpdate) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("telegram bot: update %d panic recovered: %v", u.UpdateID, r)
		}
	}()
	if u.CallbackQuery != nil {
		h.telegramBotHandleCallback(ctx, s, u.CallbackQuery)
		return
	}
	if u.Message == nil {
		return
	}
	msg := u.Message
	if strconv.FormatInt(msg.Chat.ID, 10) != s.ChatID {
		return // 非配置 chat：静默忽略，不回复
	}
	switch telegramBotCommand(msg.Text) {
	case "start":
		_ = h.telegramBotSendMessage(ctx, s, msg.Chat.ID, telegramBotMenuText(), telegramBotMenuKeyboard())
	case "nodes":
		_ = h.telegramBotSendMessage(ctx, s, msg.Chat.ID, h.telegramBotNodesText(), nil)
	case "traffic":
		_ = h.telegramBotSendMessage(ctx, s, msg.Chat.ID, h.telegramBotTrafficText(), nil)
	case "status":
		_ = h.telegramBotSendMessage(ctx, s, msg.Chat.ID, h.telegramBotStatusText(), nil)
	default:
		// 未知命令/普通文本：忽略，不打扰
	}
}

func (h *Handler) telegramBotHandleCallback(ctx context.Context, s telegramBotSettings, cq *telegramCallbackQuery) {
	if cq == nil {
		return
	}
	var chatID int64
	if cq.Message != nil {
		chatID = cq.Message.Chat.ID
	} else {
		chatID = cq.From.ID
	}
	if strconv.FormatInt(chatID, 10) != s.ChatID {
		return // 非配置 chat：静默忽略，连 answer 都不回
	}
	// 先应答（消 loading），再按与同名命令相同的回答发新消息。
	_ = h.telegramBotAnswerCallback(ctx, s, cq.ID)
	var text string
	switch cq.Data {
	case "nodes":
		text = h.telegramBotNodesText()
	case "traffic":
		text = h.telegramBotTrafficText()
	case "status":
		text = h.telegramBotStatusText()
	default:
		return
	}
	_ = h.telegramBotSendMessage(ctx, s, chatID, text, nil)
}

// telegramBotCommand 从消息文本取命令词：去 "/"、"@机器人名"、
// 参数，小写化。非 "/" 开头返回 ""。
func telegramBotCommand(text string) string {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return ""
	}
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return ""
	}
	cmd := strings.TrimPrefix(fields[0], "/")
	if i := strings.Index(cmd, "@"); i >= 0 {
		cmd = cmd[:i]
	}
	return strings.ToLower(cmd)
}

func telegramBotMenuText() string {
	return "<b>🤖 FLMX 面板</b>\n\n点下面的按钮查询节点、流量与面板状态。"
}

func telegramBotMenuKeyboard() *telegramInlineKeyboard {
	return &telegramInlineKeyboard{InlineKeyboard: [][]telegramInlineButton{
		{{Text: "🖥 节点状态", CallbackData: "nodes"}},
		{{Text: "📊 流量", CallbackData: "traffic"}},
		{{Text: "ℹ️ 面板状态", CallbackData: "status"}},
	}}
}

// telegramBotNodesText：节点列表（名＋在线/离线＋新鲜延迟）。
// 数据源：repo.ListNodes（status 1＝在线）；延迟取内存探针最新结果
// （15 分钟内且 OK 才展示，无则不写）。
func (h *Handler) telegramBotNodesText() string {
	const title = "<b>🖥 节点状态</b>\n\n"
	if h == nil || h.repo == nil {
		return title + "查询失败，请稍后再试。"
	}
	nodes, err := h.repo.ListNodes()
	if err != nil {
		return title + "查询失败，请稍后再试。"
	}
	if len(nodes) == 0 {
		return title + "暂无节点。"
	}
	online := 0
	var b strings.Builder
	b.WriteString(title)
	for _, m := range nodes {
		name := telegramBotMapString(m, "name")
		if name == "" {
			name = telegramBotMapString(m, "ip")
		}
		id := telegramBotMapInt(m, "id")
		if telegramBotMapInt(m, "status") == 1 {
			online++
			b.WriteString("🟢 " + html.EscapeString(name))
			if ms, ok := h.telegramBotNodeLatency(id); ok {
				fmt.Fprintf(&b, " — %dms", ms)
			}
		} else {
			b.WriteString("🔴 " + html.EscapeString(name))
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "\n共 %d 个，在线 %d 个", len(nodes), online)
	return b.String()
}

// telegramBotNodeLatency 取节点的最新探针延迟（内存态，15 分钟内
// 且 OK 才算新鲜）。
func (h *Handler) telegramBotNodeLatency(nodeID int64) (int64, bool) {
	if h == nil || h.nodeLatency == nil || nodeID <= 0 {
		return 0, false
	}
	p, ok := h.nodeLatency.get(nodeID)
	if !ok || !p.OK || p.LatencyMs < 0 {
		return 0, false
	}
	if time.Since(time.UnixMilli(p.CheckedAt)) > telegramBotLatencyFresh {
		return 0, false
	}
	return p.LatencyMs, true
}

// telegramBotTrafficText：流量汇总。
// 口径：今日＝statistics_flow 自今日 0 点以来的每小时增量合计
// （整点统计，当前不足一小时的部分未计入）；本周期＝用户表 in/out
// 快照合计（按月重置）。
func (h *Handler) telegramBotTrafficText() string {
	const title = "<b>📊 流量汇总</b>\n\n"
	if h == nil || h.repo == nil {
		return title + "查询失败，请稍后再试。"
	}
	now := time.Now()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).UnixMilli()
	today, err := h.repo.SumStatisticsFlowsSince(midnight)
	if err != nil {
		return title + "查询失败，请稍后再试。"
	}
	snaps, err := h.repo.ListAllUserFlowSnapshots()
	if err != nil {
		return title + "查询失败，请稍后再试。"
	}
	var in, out int64
	for _, s := range snaps {
		in += s.InFlow
		out += s.OutFlow
	}
	var b strings.Builder
	b.WriteString(title)
	fmt.Fprintf(&b, "今日：%s\n", formatBytes(today))
	fmt.Fprintf(&b, "本周期：%s（入 %s / 出 %s）\n", formatBytes(in+out), formatBytes(in), formatBytes(out))
	b.WriteString("\n口径：今日＝0 点以来整点增量合计；本周期＝用户快照合计，按月重置。")
	return b.String()
}

// telegramBotStatusText：面板版本、节点数、运行时间。
// 版本源：currentPanelVersion（FLMX_VERSION，未设为 dev）；
// 运行时间源：Handler.startedAt（New 时记录）。
func (h *Handler) telegramBotStatusText() string {
	const title = "<b>ℹ️ 面板状态</b>\n\n"
	if h == nil || h.repo == nil {
		return title + "查询失败，请稍后再试。"
	}
	nodes, err := h.repo.ListNodes()
	if err != nil {
		return title + "查询失败，请稍后再试。"
	}
	online := 0
	for _, m := range nodes {
		if telegramBotMapInt(m, "status") == 1 {
			online++
		}
	}
	var b strings.Builder
	b.WriteString(title)
	fmt.Fprintf(&b, "版本：%s\n", html.EscapeString(currentPanelVersion()))
	fmt.Fprintf(&b, "节点：共 %d 个，在线 %d 个\n", len(nodes), online)
	fmt.Fprintf(&b, "运行时间：%s", telegramBotUptime(h.startedAt, time.Now()))
	return b.String()
}

// telegramBotUptime 把运行时长格式化为中文（天/小时/分）。
func telegramBotUptime(start, now time.Time) string {
	if start.IsZero() || now.Before(start) {
		return "未知"
	}
	d := now.Sub(start)
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	if days > 0 {
		return fmt.Sprintf("%d天%d小时%d分", days, hours, mins)
	}
	if hours > 0 {
		return fmt.Sprintf("%d小时%d分", hours, mins)
	}
	if mins > 0 {
		return fmt.Sprintf("%d分", mins)
	}
	return "不到1分钟"
}

func telegramBotMapString(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

func telegramBotMapInt(m map[string]interface{}, key string) int64 {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	}
	return 0
}

// ---- Telegram Bot API wire types ----

type telegramUpdate struct {
	UpdateID      int64                  `json:"update_id"`
	Message       *telegramMessage       `json:"message,omitempty"`
	CallbackQuery *telegramCallbackQuery `json:"callback_query,omitempty"`
}

type telegramMessage struct {
	MessageID int64        `json:"message_id"`
	Chat      telegramChat `json:"chat"`
	Text      string       `json:"text"`
}

type telegramChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type telegramCallbackQuery struct {
	ID      string           `json:"id"`
	From    telegramUser     `json:"from"`
	Message *telegramMessage `json:"message,omitempty"`
	Data    string           `json:"data"`
}

type telegramUser struct {
	ID int64 `json:"id"`
}

type telegramUpdatesResponse struct {
	OK          bool             `json:"ok"`
	Result      []telegramUpdate `json:"result"`
	Description string           `json:"description,omitempty"`
}

type telegramInlineKeyboard struct {
	InlineKeyboard [][]telegramInlineButton `json:"inline_keyboard"`
}

type telegramInlineButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}
