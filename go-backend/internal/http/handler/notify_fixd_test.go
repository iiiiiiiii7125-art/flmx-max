package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// FIX-D 通知族回归（A7-02 / A7-03 / A7-04 / D6-07）：
//   - A7-02：指纹去重不得吞掉「恢复后再告警」（T26 真机定案）
//   - A7-03：状态先标记后异步发送、失败不回滚 = 告警永久丢失；
//     改为发送成功才置位、失败回滚可重试（BC-16）
//   - A7-04：并发同指纹穿透去重重复投递；多通道整条重试使成功通道重复收

type fixdWebhook struct {
	srv   *httptest.Server
	mu    sync.Mutex
	msgs  []notifyMessage
	fail  atomic.Bool
	delay time.Duration
}

func newFixdWebhook(t *testing.T) *fixdWebhook {
	t.Helper()
	w := &fixdWebhook{}
	w.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		if w.delay > 0 {
			time.Sleep(w.delay)
		}
		if w.fail.Load() {
			rw.WriteHeader(http.StatusInternalServerError)
			return
		}
		var m notifyMessage
		_ = json.NewDecoder(req.Body).Decode(&m)
		w.mu.Lock()
		w.msgs = append(w.msgs, m)
		w.mu.Unlock()
		rw.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(w.srv.Close)
	return w
}

func (w *fixdWebhook) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.msgs)
}

func (w *fixdWebhook) titles() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]string, len(w.msgs))
	for i, m := range w.msgs {
		out[i] = m.Title
	}
	return out
}

func (w *fixdWebhook) waitCount(t *testing.T, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if w.count() >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("webhook messages = %d (%v), want >= %d", w.count(), w.titles(), want)
}

func setupFixdNotifyHandler(t *testing.T, wh *fixdWebhook) (*Handler, *repo.Repository) {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/notify-fixd.db")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	now := time.Now().UnixMilli()
	for k, v := range map[string]string{"notify_enabled": "true", "notify_webhook_url": wh.srv.URL} {
		if err := r.DB().Exec(`INSERT INTO vite_config(name, value, time) VALUES(?, ?, ?)`, k, v, now).Error; err != nil {
			t.Fatal(err)
		}
	}
	return New(r, "unit-test-secret"), r
}

func insertFixdNode(t *testing.T, r *repo.Repository, id int64, name string) {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := r.DB().Exec(`
		INSERT INTO node(id, name, secret, server_ip, server_ip_v4, server_ip_v6, port, interface_name, version, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx, is_remote, remote_url, remote_token)
		VALUES(?, ?, 'secret-x', '10.0.0.7', '10.0.0.7', '', '40000-40010', '', 'v1', 1, 1, 1, ?, ?, 1, '[::]', '[::]', 0, 0, '', '')
	`, id, name, now, now).Error; err != nil {
		t.Fatalf("insert node: %v", err)
	}
}

// A7-02（T26 定案）：掉线→恢复→再掉线→再恢复，30 分钟指纹窗内第二次
// 掉线与恢复都必须送达。旧实现成对被指纹吞掉（总共只有 2 条）。
func TestFixdOfflineRecoveryRearmsWithinWindow(t *testing.T) {
	old := nodeOfflineGrace
	nodeOfflineGrace = 50 * time.Millisecond
	t.Cleanup(func() { nodeOfflineGrace = old })

	wh := newFixdWebhook(t)
	h, r := setupFixdNotifyHandler(t, wh)
	insertFixdNode(t, r, 7, "fixd-node-7")

	h.notifyNodeOffline(7)
	wh.waitCount(t, 1, 3*time.Second)
	h.notifyNodeOnline(7)
	wh.waitCount(t, 2, 3*time.Second)

	h.notifyNodeOffline(7)
	wh.waitCount(t, 3, 3*time.Second)
	h.notifyNodeOnline(7)
	wh.waitCount(t, 4, 3*time.Second)

	titles := wh.titles()
	want := []string{"节点掉线", "节点恢复在线", "节点掉线", "节点恢复在线"}
	for i := range want {
		if titles[i] != want[i] {
			t.Fatalf("titles = %v, want %v", titles, want)
		}
	}
}

// A7-03：节点到期告警发送失败（webhook 500）后，状态不得置位；通道恢复
// 后下一轮检查必须补发；补发成功后不再重复。
func TestFixdExpiryFailureRollsBackAndRetries(t *testing.T) {
	wh := newFixdWebhook(t)
	h, r := setupFixdNotifyHandler(t, wh)
	now := time.Now()
	if err := r.DB().Exec(`INSERT INTO node(name, secret, server_ip, port, created_time, status, expiry_time, expiry_reminder_dismissed)
		VALUES('fixd-soon', 'fixd-secret', '203.0.113.1', '1000-2000', ?, 1, ?, 0)`,
		now.UnixMilli(), now.Add(48*time.Hour).UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}

	wh.fail.Store(true)
	h.checkNodeExpiry(now)
	// 等首次派发（含 3s 后的一次重试）彻底失败并回滚。
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		h.notifier.mu.Lock()
		marked := len(h.notifier.expiryAlerted)
		h.notifier.mu.Unlock()
		if marked == 0 && wh.count() == 0 {
			// 需要确认派发已结束而非还没开始：再多等一个重试周期。
			time.Sleep(100 * time.Millisecond)
			h.notifier.mu.Lock()
			marked = len(h.notifier.expiryAlerted)
			h.notifier.mu.Unlock()
			if marked == 0 {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := wh.count(); got != 0 {
		t.Fatalf("failing webhook must not record deliveries, got %d", got)
	}
	h.notifier.mu.Lock()
	marked := len(h.notifier.expiryAlerted)
	h.notifier.mu.Unlock()
	if marked != 0 {
		t.Fatalf("A7-03: expiry state stayed marked after failed send (%d entries)", marked)
	}

	wh.fail.Store(false)
	h.checkNodeExpiry(now.Add(time.Minute))
	wh.waitCount(t, 1, 3*time.Second)
	h.checkNodeExpiry(now.Add(2 * time.Minute))
	time.Sleep(300 * time.Millisecond)
	if got := wh.count(); got != 1 {
		t.Fatalf("expiry alert must fire exactly once after recovery, got %d (%v)", got, wh.titles())
	}
}

// A7-03（alertOnce 族）：用户流量告警发送失败后回滚，恢复后补发一次。
func TestFixdUserFlowFailureRollsBackAndRetries(t *testing.T) {
	wh := newFixdWebhook(t)
	h, r := setupFixdNotifyHandler(t, wh)
	now := time.Now()
	gb := int64(1024 * 1024 * 1024)
	if err := r.DB().Exec(`INSERT INTO user(user, pwd, role_id, exp_time, flow, in_flow, out_flow, flow_reset_time, num, created_time, status)
		VALUES('fixd-alice', 'x', 1, 0, 10, ?, ?, 0, 10, ?, 1)`, 5*gb, gb*9/2, now.UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}

	wh.fail.Store(true)
	h.checkUserReminders(now)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		h.notifier.mu.Lock()
		marked := len(h.notifier.alerted)
		h.notifier.mu.Unlock()
		if marked == 0 {
			time.Sleep(150 * time.Millisecond)
			h.notifier.mu.Lock()
			marked = len(h.notifier.alerted)
			h.notifier.mu.Unlock()
			if marked == 0 {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	h.notifier.mu.Lock()
	marked := len(h.notifier.alerted)
	h.notifier.mu.Unlock()
	if marked != 0 {
		t.Fatalf("A7-03: flow alert state stayed marked after failed send (%d entries)", marked)
	}

	wh.fail.Store(false)
	h.checkUserReminders(now.Add(time.Minute))
	wh.waitCount(t, 1, 3*time.Second)
	if titles := wh.titles(); titles[0] != "用户流量即将用完" {
		t.Fatalf("titles = %v", titles)
	}
}

// A7-03（延迟族）：连续劣化告警发送失败后 tracker 的 down 标记回滚，
// 下一轮同一劣化必须重新触发；成功后不再重复。
func TestFixdLatencyFailureRollsBackAndRetries(t *testing.T) {
	wh := newFixdWebhook(t)
	h, _ := setupFixdNotifyHandler(t, wh)

	bad := map[int64]externalProbe{
		1: {Points: []latencyPoint{{Name: "香港", OK: true, LatencyMs: 30, LossPct: 50}}},
	}
	names := map[int64]string{1: "节点一"}

	wh.fail.Store(true)
	for i := 0; i < latencyAlertStreak; i++ {
		h.alertLatency(bad, names)
	}
	// 等失败派发完成并回滚 down 标记。
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		h.latencyAlerts.mu.Lock()
		down := len(h.latencyAlerts.down)
		h.latencyAlerts.mu.Unlock()
		if down == 0 {
			time.Sleep(150 * time.Millisecond)
			h.latencyAlerts.mu.Lock()
			down = len(h.latencyAlerts.down)
			h.latencyAlerts.mu.Unlock()
			if down == 0 {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	h.latencyAlerts.mu.Lock()
	down := len(h.latencyAlerts.down)
	h.latencyAlerts.mu.Unlock()
	if down != 0 {
		t.Fatalf("A7-03: latency down state stayed marked after failed send")
	}
	if got := wh.count(); got != 0 {
		t.Fatalf("failing webhook recorded %d deliveries", got)
	}

	wh.fail.Store(false)
	h.alertLatency(bad, names)
	wh.waitCount(t, 1, 3*time.Second)
	h.alertLatency(bad, names)
	time.Sleep(300 * time.Millisecond)
	if got := wh.count(); got != 1 {
		t.Fatalf("latency alert must fire exactly once after recovery, got %d", got)
	}
}

// A7-04：同一通知并发触发时只投递一次（旧实现 markSent 在发送成功后才
// 写，16 个并发全部穿透 recentlySent 检查、各发一份）。
func TestFixdConcurrentSameNotificationDeliveredOnce(t *testing.T) {
	wh := newFixdWebhook(t)
	wh.delay = 80 * time.Millisecond
	h, _ := setupFixdNotifyHandler(t, wh)

	const n = 16
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.notifySync(notifyEventKernelError, "内核运行出错", "节点：并发测试")
		}()
	}
	wg.Wait()
	time.Sleep(200 * time.Millisecond)
	if got := wh.count(); got != 1 {
		t.Fatalf("A7-04: concurrent duplicates delivered %d times, want 1", got)
	}
}

// A7-04：双通道一成一败时，重试只发失败通道，成功通道不重复收。
func TestFixdRetryOnlyFailedChannel(t *testing.T) {
	wh := newFixdWebhook(t)

	var tgHits atomic.Int32
	tg := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		tgHits.Add(1)
		rw.WriteHeader(http.StatusInternalServerError)
	}))
	defer tg.Close()

	n := newNotifier()
	settings := notifySettings{
		TelegramToken:   "tok",
		TelegramChatID:  "chat",
		TelegramAPIBase: tg.URL,
		WebhookURL:      wh.srv.URL,
	}
	msg := notifyMessage{Event: notifyEventTest, Title: "t", Message: "m", Time: time.Now().UnixMilli(), App: "FLMX"}
	err := n.sendWithRetry(context.Background(), settings, msg)
	if err == nil {
		t.Fatal("sendWithRetry must report the still-failing telegram channel")
	}
	if got := wh.count(); got != 1 {
		t.Fatalf("A7-04: webhook received %d copies, want exactly 1", got)
	}
	if got := tgHits.Load(); got != 2 {
		t.Fatalf("telegram attempts = %d, want 2 (initial + one retry)", got)
	}
}
