package handler

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// Telegram 机器人菜单自主查询的测试：httptest mock Telegram 服务，
// 不调真实 Telegram、不动测试机。

const telegramBotTestToken = "tok123"
const telegramBotTestChatID = "42"

type telegramBotMockCall struct {
	Path string
	Body map[string]interface{}
	Raw  string
}

type telegramBotMock struct {
	srv    *httptest.Server
	mu     sync.Mutex
	calls  []telegramBotMockCall
	served bool
}

func newTelegramBotMock(t *testing.T) *telegramBotMock {
	t.Helper()
	m := &telegramBotMock{}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		m.mu.Lock()
		defer m.mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/getUpdates") {
			// catch-up（offset=-1）与已派发后一律空结果；首个正常
			// 拉取返回 canned 更新（合法 /nodes、陌生 /nodes、按
			// 钮回调、一条无内容的畸形 update）。
			if r.URL.Query().Get("offset") == "-1" || m.served {
				_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
				return
			}
			m.served = true
			_, _ = w.Write([]byte(`{"ok":true,"result":[
				{"update_id":100,"message":{"message_id":1,"chat":{"id":42,"type":"private"},"text":"/nodes"}},
				{"update_id":101,"message":{"message_id":2,"chat":{"id":999,"type":"private"},"text":"/nodes"}},
				{"update_id":102,"callback_query":{"id":"cq-1","from":{"id":42},"message":{"message_id":3,"chat":{"id":42,"type":"private"},"text":"menu"},"data":"nodes"}},
				{"update_id":103}
			]}`))
			return
		}
		var parsed map[string]interface{}
		_ = json.Unmarshal(body, &parsed)
		m.calls = append(m.calls, telegramBotMockCall{Path: r.URL.Path, Body: parsed, Raw: string(body)})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *telegramBotMock) baseURL(t *testing.T) string {
	t.Helper()
	return "http://tg.test:" + portOf(t, m.srv)
}

func (m *telegramBotMock) callsOf(pathSuffix string) []telegramBotMockCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []telegramBotMockCall
	for _, c := range m.calls {
		if strings.HasSuffix(c.Path, pathSuffix) {
			out = append(out, c)
		}
	}
	return out
}

func setupTelegramBotHandler(t *testing.T, mock *telegramBotMock) *Handler {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/tgbot.db")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	now := time.Now().UnixMilli()
	for k, v := range map[string]string{
		"notify_enabled":            "true",
		"notify_telegram_bot_token": telegramBotTestToken,
		"notify_telegram_chat_id":   telegramBotTestChatID,
		"notify_telegram_api_base":  mock.baseURL(t),
	} {
		if err := r.DB().Exec(`INSERT INTO vite_config(name, value, time) VALUES(?, ?, ?)`, k, v, now).Error; err != nil {
			t.Fatal(err)
		}
	}
	// 两节点：11 在线、12 离线（名字带 HTML，验转义）。
	for _, n := range []struct {
		id     int64
		name   string
		status int
	}{
		{11, "hk-ix", 1},
		{12, "sh-<b>front</b>", 0},
	} {
		if err := r.DB().Exec(`
			INSERT INTO node(id, name, secret, server_ip, server_ip_v4, server_ip_v6, port, interface_name, version, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx)
			VALUES(?, ?, 'secret', '10.0.0.1', '10.0.0.1', '', '40000', '', 'v1', 1, 1, 1, ?, ?, ?, '[::]', '[::]', 0)
		`, n.id, n.name, now, now, n.status).Error; err != nil {
			t.Fatalf("insert node %d: %v", n.id, err)
		}
	}
	// 流量种子：用户 2 本周期入 3 GiB / 出 1 GiB；今日两笔整点增量
	// 合计 768 MiB；昨日一笔 1 GiB（今日口径须排除）。
	// 注意：repo.Open 会自动种 admin 用户（id=1），种子用户用 id=2。
	if err := r.DB().Exec(`
		INSERT INTO "user"(id, "user", pwd, role_id, exp_time, flow, flow_mib, in_flow, out_flow, flow_reset_time, num, created_time, status)
		VALUES(2, 'u1', 'x', 2, 0, 0, 0, 3221225472, 1073741824, 0, 0, ?, 1)
	`, now).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}
	midnight := time.Date(time.Now().Year(), time.Now().Month(), time.Now().Day(), 0, 0, 0, 0, time.Now().Location())
	for _, f := range []struct {
		flow    int64
		created int64
	}{
		{536870912, midnight.Add(10 * time.Hour).UnixMilli()},
		{268435456, midnight.Add(11 * time.Hour).UnixMilli()},
		{1073741824, midnight.Add(-14 * time.Hour).UnixMilli()},
	} {
		if err := r.DB().Exec(`
			INSERT INTO statistics_flow(user_id, flow, total_flow, time, created_time)
			VALUES(2, ?, ?, '10:00', ?)
		`, f.flow, f.flow, f.created).Error; err != nil {
			t.Fatalf("insert statistics_flow: %v", err)
		}
	}
	return New(r, "unit-test-secret")
}

func TestTelegramBotLoopEndToEnd(t *testing.T) {
	mock := newTelegramBotMock(t)
	stubNotifyLookup(t, func(host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	})
	h := setupTelegramBotHandler(t, mock)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h.jobsWG.Add(1)
	go h.runTelegramBotLoop(ctx)

	// 等 mock 收到：1 次 setMyCommands、2 次 sendMessage、1 次
	// answerCallbackQuery（15s 内）。
	deadline := time.Now().Add(15 * time.Second)
	for {
		if len(mock.callsOf("/setMyCommands")) == 1 &&
			len(mock.callsOf("/sendMessage")) == 2 &&
			len(mock.callsOf("/answerCallbackQuery")) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("mock calls not complete: setMyCommands=%d sendMessage=%d answerCallbackQuery=%d",
				len(mock.callsOf("/setMyCommands")), len(mock.callsOf("/sendMessage")), len(mock.callsOf("/answerCallbackQuery")))
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	done := make(chan struct{})
	go func() { h.jobsWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("bot loop did not exit after cancel")
	}

	// setMyCommands：四条命令都注册了。
	cmds := mock.callsOf("/setMyCommands")
	if len(cmds) != 1 {
		t.Fatalf("setMyCommands must be called once, got %d", len(cmds))
	}
	raw, _ := json.Marshal(cmds[0].Body["commands"])
	for _, want := range []string{`"command":"start"`, `"command":"nodes"`, `"command":"traffic"`, `"command":"status"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("setMyCommands missing %s: %s", want, raw)
		}
	}

	// sendMessage：两次都发给合法 chat 42，内容相同（命令与回调
	// 同款回答），HTML 格式，节点名已转义；陌生 chat 999 无任何
	// 回复。
	sends := mock.callsOf("/sendMessage")
	if len(sends) != 2 {
		t.Fatalf("want 2 sendMessage calls, got %d", len(sends))
	}
	for _, s := range sends {
		if s.Body["chat_id"] != float64(42) {
			t.Fatalf("sendMessage must go to chat 42, got %v", s.Body["chat_id"])
		}
		if s.Body["parse_mode"] != "HTML" {
			t.Fatalf("sendMessage must use HTML parse_mode, got %v", s.Body["parse_mode"])
		}
		text, _ := s.Body["text"].(string)
		if !strings.Contains(text, "hk-ix") || !strings.Contains(text, "sh-&lt;b&gt;front&lt;/b&gt;") {
			t.Fatalf("nodes text missing/escaped wrong: %q", text)
		}
		if strings.Contains(text, "sh-<b>front</b>") {
			t.Fatalf("node name must be HTML-escaped: %q", text)
		}
		if !strings.Contains(text, "共 2 个，在线 1 个") {
			t.Fatalf("nodes text missing footer: %q", text)
		}
	}
	if sends[0].Body["text"] != sends[1].Body["text"] {
		t.Fatalf("callback must return the same content as the command:\n%v\nvs\n%v", sends[0].Body["text"], sends[1].Body["text"])
	}

	// answerCallbackQuery：应答了按钮回调 cq-1。
	answers := mock.callsOf("/answerCallbackQuery")
	if len(answers) != 1 || answers[0].Body["callback_query_id"] != "cq-1" {
		t.Fatalf("answerCallbackQuery must answer cq-1 once, got %v", answers)
	}
}

func TestTelegramBotLoopDormantWithoutConfig(t *testing.T) {
	mock := newTelegramBotMock(t)
	stubNotifyLookup(t, func(host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	})
	r, err := repo.Open(t.TempDir() + "/tgbot-dormant.db")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h.jobsWG.Add(1)
	go h.runTelegramBotLoop(ctx)
	time.Sleep(300 * time.Millisecond)
	cancel()
	done := make(chan struct{})
	go func() { h.jobsWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("bot loop did not exit after cancel")
	}
	if n := len(mock.calls); n != 0 {
		t.Fatalf("unconfigured bot must make no Telegram calls, got %d", n)
	}
}

func TestTelegramBotNodesText(t *testing.T) {
	mock := newTelegramBotMock(t)
	h := setupTelegramBotHandler(t, mock)
	h.nodeLatency.replace(map[int64]externalProbe{
		11: {OK: true, LatencyMs: 12, CheckedAt: time.Now().UnixMilli()},
		// 12 离线且无探针：不展示延迟。另放一个过期探针也不展示。
		12: {OK: true, LatencyMs: 99, CheckedAt: time.Now().Add(-time.Hour).UnixMilli()},
	}, false)

	text := h.telegramBotNodesText()
	for _, want := range []string{
		"<b>🖥 节点状态</b>",
		"🟢 hk-ix — 12ms",
		"🔴 sh-&lt;b&gt;front&lt;/b&gt;",
		"共 2 个，在线 1 个",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("nodes text missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "99ms") {
		t.Fatalf("stale probe latency must not be shown:\n%s", text)
	}
}

func TestTelegramBotNodesTextEmpty(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/tgbot-empty.db")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	if text := h.telegramBotNodesText(); !strings.Contains(text, "暂无节点") {
		t.Fatalf("empty nodes must say 暂无节点, got:\n%s", text)
	}
}

func TestTelegramBotTrafficText(t *testing.T) {
	mock := newTelegramBotMock(t)
	h := setupTelegramBotHandler(t, mock)
	text := h.telegramBotTrafficText()
	for _, want := range []string{
		"<b>📊 流量汇总</b>",
		"今日：768.00 MB",
		"本周期：4.00 GB（入 3.00 GB / 出 1.00 GB）",
		"口径：今日＝0 点以来整点增量合计；本周期＝用户快照合计，按月重置。",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("traffic text missing %q:\n%s", want, text)
		}
	}
}

func TestTelegramBotStatusText(t *testing.T) {
	t.Setenv("FLMX_VERSION", "2.1.12")
	mock := newTelegramBotMock(t)
	h := setupTelegramBotHandler(t, mock)
	text := h.telegramBotStatusText()
	for _, want := range []string{
		"<b>ℹ️ 面板状态</b>",
		"版本：2.1.12",
		"节点：共 2 个，在线 1 个",
		"运行时间：",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("status text missing %q:\n%s", want, text)
		}
	}
}

func TestTelegramBotFetchRejectsBadToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":false,"error_code":401,"description":"Unauthorized"}`))
	}))
	defer srv.Close()
	stubNotifyLookup(t, func(host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	})
	r, err := repo.Open(t.TempDir() + "/tgbot-401.db")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	u, _ := url.Parse(srv.URL)
	settings := telegramBotSettings{Token: telegramBotTestToken, ChatID: telegramBotTestChatID, APIBase: "http://tg.test:" + u.Port()}
	if _, err := h.telegramBotFetch(context.Background(), settings, 0); err == nil || !strings.Contains(err.Error(), "Unauthorized") {
		t.Fatalf("bad token must surface Telegram's Unauthorized, got %v", err)
	}
}

func TestTelegramBotCommandParse(t *testing.T) {
	for in, want := range map[string]string{
		"/nodes":            "nodes",
		"/nodes@MyBot":      "nodes",
		"/nodes extra args": "nodes",
		"/START":            "start",
		"nodes":             "",
		"hello":             "",
		"":                  "",
		"/":                 "",
	} {
		if got := telegramBotCommand(in); got != want {
			t.Fatalf("telegramBotCommand(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTelegramBotUptime(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		start time.Time
		want  string
	}{
		{now.Add(-3*24*time.Hour - 2*time.Hour - 15*time.Minute), "3天2小时15分"},
		{now.Add(-2*time.Hour - 5*time.Minute), "2小时5分"},
		{now.Add(-5 * time.Minute), "5分"},
		{now.Add(-30 * time.Second), "不到1分钟"},
		{time.Time{}, "未知"},
	} {
		if got := telegramBotUptime(tc.start, now); got != tc.want {
			t.Fatalf("telegramBotUptime(%v) = %q, want %q", tc.start, got, tc.want)
		}
	}
}

func TestTelegramBotMenuKeyboard(t *testing.T) {
	kb := telegramBotMenuKeyboard()
	if len(kb.InlineKeyboard) != 3 {
		t.Fatalf("menu must have 3 buttons, got %d", len(kb.InlineKeyboard))
	}
	for i, want := range []string{"nodes", "traffic", "status"} {
		row := kb.InlineKeyboard[i]
		if len(row) != 1 || row[0].CallbackData != want {
			t.Fatalf("menu row %d must be callback %q, got %+v", i, want, row)
		}
	}
	raw, _ := json.Marshal(kb)
	if !strings.Contains(string(raw), `"inline_keyboard"`) {
		t.Fatalf("keyboard must marshal with inline_keyboard key: %s", raw)
	}
}
