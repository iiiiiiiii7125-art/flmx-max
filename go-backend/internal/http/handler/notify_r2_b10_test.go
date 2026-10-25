package handler

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R2 批 10 回归（R2X-017 / R2X-018 / R2X-038）：
//   - R2X-017：三类事件「恢复」通知发送 Failed 后不得消费跃迁——
//     inbound 补 remarkDown 对偶、latency 恢复向补 unmark 对偶、
//     node 侧置回 offlineAlerted 并以 AfterFunc(1m) 重排恢复派发；
//     route 子面恢复基准时 CheckedAt 置 0。Disabled 维持 V-16 不补发。
//   - R2X-018：通道级台账——部分通道失败时整条补发只投未送达通道；
//     forgetFingerprints 连带清台账（再武装）；日报纯计算 + pending
//     原文缓存，snapshot 与 daily_last 仅整条成功后推进。

// ---- dual-channel mocks ----

type b10Chan struct {
	srv   *httptest.Server
	mu    sync.Mutex
	texts []string // 成功投递的正文（webhook 为 Message 字段，telegram 为 text 全文）
	fail  atomic.Bool
	tg    bool
}

func newB10Chan(t *testing.T, tg bool) *b10Chan {
	t.Helper()
	c := &b10Chan{tg: tg}
	c.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		if c.fail.Load() {
			rw.WriteHeader(http.StatusInternalServerError)
			return
		}
		var text string
		if tg {
			var body struct {
				Text string `json:"text"`
			}
			_ = json.NewDecoder(req.Body).Decode(&body)
			text = body.Text
			rw.Header().Set("Content-Type", "application/json")
			_, _ = rw.Write([]byte(`{"ok":true}`))
		} else {
			var m notifyMessage
			_ = json.NewDecoder(req.Body).Decode(&m)
			text = m.Message
			rw.WriteHeader(http.StatusOK)
		}
		c.mu.Lock()
		c.texts = append(c.texts, text)
		c.mu.Unlock()
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *b10Chan) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.texts)
}

func (c *b10Chan) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.texts...)
}

func (c *b10Chan) countTitle(title string) int {
	n := 0
	for _, s := range c.snapshot() {
		if strings.Contains(s, title) {
			n++
		}
	}
	return n
}

func setupB10DualHandler(t *testing.T, tg, wh *b10Chan) (*Handler, *repo.Repository) {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "notify-b10.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	now := time.Now().UnixMilli()
	cfg := map[string]string{
		"notify_enabled":            "true",
		"notify_telegram_bot_token": "tok",
		"notify_telegram_chat_id":   "chat",
		"notify_telegram_api_base":  tg.srv.URL,
		"notify_webhook_url":        wh.srv.URL,
	}
	for k, v := range cfg {
		if err := r.DB().Exec(`INSERT INTO vite_config(name, value, time) VALUES(?, ?, ?)`, k, v, now).Error; err != nil {
			t.Fatal(err)
		}
	}
	return New(r, "unit-test-secret"), r
}

// waitB10 polls cond until deadline; returns false on timeout.
func waitB10(deadline time.Duration, cond func() bool) bool {
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if cond() {
			// 二次确认，防一瞬态命中。
			time.Sleep(120 * time.Millisecond)
			if cond() {
				return true
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return cond()
}

// ---- R2X-018: channel ledger ----

func TestR2B10PartialFailureResendsOnlyFailedChannel(t *testing.T) {
	tg := newB10Chan(t, true)
	wh := newB10Chan(t, false)
	h, _ := setupB10DualHandler(t, tg, wh)

	wh.fail.Store(true)
	if h.notifySync(notifyEventNodeExpiry, "节点即将到期", "节点：台账测试") {
		t.Fatal("dispatch with a failing channel must report failure")
	}
	if got := tg.count(); got != 1 {
		t.Fatalf("telegram deliveries after first attempt = %d, want 1", got)
	}
	if got := wh.count(); got != 0 {
		t.Fatalf("webhook deliveries after first attempt = %d, want 0", got)
	}

	wh.fail.Store(false)
	if !h.notifySync(notifyEventNodeExpiry, "节点即将到期", "节点：台账测试") {
		t.Fatal("resend after channel recovery must succeed")
	}
	if got := tg.count(); got != 1 {
		t.Fatalf("R2X-018: telegram got duplicate on resend, count = %d, want 1", got)
	}
	if got := wh.count(); got != 1 {
		t.Fatalf("webhook deliveries after resend = %d, want 1", got)
	}

	// 整条成功后指纹已记：同指纹再派发被抑制，两通道都不再收。
	if !h.notifySync(notifyEventNodeExpiry, "节点即将到期", "节点：台账测试") {
		t.Fatal("suppressed dispatch must count as notified")
	}
	if tg.count() != 1 || wh.count() != 1 {
		t.Fatalf("suppressed dispatch delivered again: tg=%d wh=%d", tg.count(), wh.count())
	}

	// 再武装：恢复清指纹必须连带清台账，否则同问题复发被台账吞掉。
	h.notifier.forgetEventMessage(notifyEventNodeExpiry, "节点：台账测试")
	if !h.notifySync(notifyEventNodeExpiry, "节点即将到期", "节点：台账测试") {
		t.Fatal("re-armed dispatch must succeed")
	}
	if tg.count() != 2 || wh.count() != 2 {
		t.Fatalf("R2X-018: re-armed problem must reach both channels once more, tg=%d wh=%d", tg.count(), wh.count())
	}
}

func TestR2B10SingleChannelBehaviourUnchanged(t *testing.T) {
	wh := newFixdWebhook(t)
	h, _ := setupFixdNotifyHandler(t, wh)
	if !h.notifySync(notifyEventNodeExpiry, "节点即将到期", "节点：单通道") {
		t.Fatal("single channel dispatch must succeed")
	}
	if !h.notifySync(notifyEventNodeExpiry, "节点即将到期", "节点：单通道") {
		t.Fatal("suppressed dispatch must count as notified")
	}
	time.Sleep(200 * time.Millisecond)
	if got := wh.count(); got != 1 {
		t.Fatalf("single channel deliveries = %d, want 1", got)
	}
}

// ---- R2X-018: daily report pending snapshot ----

func TestR2B10DailyReportPendingVerbatimAndSnapshot(t *testing.T) {
	tg := newB10Chan(t, true)
	wh := newB10Chan(t, false)
	h, r := setupB10DualHandler(t, tg, wh)
	// R24-FIX F-02（O-A-1）：把 now 固定到本小时的 30 分，避免 :59 启动
	// 时 now.Add(1m)/Add(2m) 跨小时、maybeDailyReport 因小时不符提前
	// 返回导致补发腿 flake。仅改夹具时间固定，断言一字不动。
	t0 := time.Now()
	now := time.Date(t0.Year(), t0.Month(), t0.Day(), t0.Hour(), 30, 0, 0, t0.Location())
	gb := int64(1024 * 1024 * 1024)
	row := &model.NodeInbound{Name: "日报入站", Protocol: "ss", Listen: "203.0.113.9", Port: 8388,
		Enabled: 1, UploadBytes: 10 * gb, CreatedTime: now.UnixMilli(), UpdatedTime: now.UnixMilli()}
	if err := r.DB().Create(row).Error; err != nil {
		t.Fatalf("create inbound: %v", err)
	}
	if err := r.UpsertConfig(notifyConfigDailyHour, strconv.Itoa(now.Hour()), now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	// 基线快照为 0：今日增量即全部 10 GiB，文案走「过去一天」口径。
	if err := r.UpsertConfig(notifyConfigDailySnapshot, "0", now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	h.invalidateNotifyConfigCache()

	wh.fail.Store(true)
	h.maybeDailyReport(now)
	if got := tg.count(); got != 1 {
		t.Fatalf("telegram daily deliveries = %d, want 1", got)
	}
	if got := wh.count(); got != 0 {
		t.Fatalf("webhook daily deliveries = %d, want 0", got)
	}
	// 整条未成功：snapshot 与 daily_last 都不得推进。
	if cfg, err := r.GetConfigByName(notifyConfigDailySnapshot); err != nil || cfg == nil || cfg.Value != "0" {
		t.Fatalf("R2X-018: snapshot advanced on failed daily report: %v, %v", cfg, err)
	}
	if cfg, err := r.GetConfigByName(notifyConfigDailyLast); err == nil && cfg != nil && cfg.Value != "" {
		t.Fatalf("daily_last advanced on failed daily report: %q", cfg.Value)
	}
	tgText := tg.snapshot()[0]
	if !strings.Contains(tgText, "入站流量（过去一天）：10.00 GB") {
		t.Fatalf("first daily text wrong: %q", tgText)
	}

	wh.fail.Store(false)
	h.maybeDailyReport(now.Add(time.Minute))
	if got := tg.count(); got != 1 {
		t.Fatalf("R2X-018: telegram got duplicate daily, count = %d", got)
	}
	if got := wh.count(); got != 1 {
		t.Fatalf("webhook daily deliveries after resend = %d, want 1", got)
	}
	whMsg := wh.snapshot()[0]
	if !strings.Contains(whMsg, "入站流量（过去一天）：10.00 GB") {
		t.Fatalf("R2X-018: resent daily text lost the traffic figure: %q", whMsg)
	}
	if !strings.Contains(tgText, whMsg) {
		t.Fatalf("resent daily text must be verbatim first text:\nfirst(tg)=%q\nresent(wh)=%q", tgText, whMsg)
	}
	// 整条成功后才推进 snapshot 与 daily_last。
	cfg, err := r.GetConfigByName(notifyConfigDailySnapshot)
	if err != nil || cfg == nil || cfg.Value != strconv.FormatInt(10*gb, 10) {
		t.Fatalf("snapshot after success = %v, %v", cfg, err)
	}
	cfg, err = r.GetConfigByName(notifyConfigDailyLast)
	if err != nil || cfg == nil || cfg.Value != now.Format("2006-01-02") {
		t.Fatalf("daily_last after success = %v, %v", cfg, err)
	}
	// 同日不再重发。
	h.maybeDailyReport(now.Add(2 * time.Minute))
	if tg.count() != 1 || wh.count() != 1 {
		t.Fatalf("daily report repeated same day: tg=%d wh=%d", tg.count(), wh.count())
	}
}

// ---- R2X-017: node recovery ----

func TestR2B10NodeRecoveryFailureResendsOnce(t *testing.T) {
	old := nodeOfflineGrace
	nodeOfflineGrace = 50 * time.Millisecond
	t.Cleanup(func() { nodeOfflineGrace = old })

	wh := newFixdWebhook(t)
	h, r := setupFixdNotifyHandler(t, wh)
	insertFixdNode(t, r, 7, "b10-node-7")

	h.notifyNodeOffline(7)
	wh.waitCount(t, 1, 3*time.Second)

	// 通道全败下恢复：恢复通知失败后 offlineAlerted 必须置回，
	// 并由 AfterFunc(1m) 重排补发（本用例等重排真实触发）。
	wh.fail.Store(true)
	h.notifyNodeOnline(7)
	if !waitB10(12*time.Second, func() bool {
		h.notifier.mu.Lock()
		defer h.notifier.mu.Unlock()
		return h.notifier.offlineAlerted[7]
	}) {
		t.Fatal("R2X-017: offlineAlerted not restored after failed recovery notify")
	}
	if got := wh.count(); got != 1 {
		t.Fatalf("failed recovery must not record deliveries, got %d", got)
	}
	wh.fail.Store(false)
	wh.waitCount(t, 2, 90*time.Second)
	if titles := wh.titles(); titles[1] != "节点恢复在线" {
		t.Fatalf("resent titles = %v", titles)
	}
	time.Sleep(300 * time.Millisecond)
	if got := wh.count(); got != 2 {
		t.Fatalf("recovery must be resent exactly once, got %d (%v)", got, wh.titles())
	}

	// 补发后再次掉线仍能告警、再次恢复仍能送达（再武装）。
	h.notifyNodeOffline(7)
	wh.waitCount(t, 3, 3*time.Second)
	h.notifyNodeOnline(7)
	wh.waitCount(t, 4, 3*time.Second)
	want := []string{"节点掉线", "节点恢复在线", "节点掉线", "节点恢复在线"}
	for i, title := range wh.titles() {
		if title != want[i] {
			t.Fatalf("titles = %v, want %v", wh.titles(), want)
		}
	}
}

func TestR2B10NodeRecoveryDisabledDoesNotResend(t *testing.T) {
	old := nodeOfflineGrace
	nodeOfflineGrace = 50 * time.Millisecond
	t.Cleanup(func() { nodeOfflineGrace = old })

	wh := newFixdWebhook(t)
	h, r := setupFixdNotifyHandler(t, wh)
	insertFixdNode(t, r, 7, "b10-node-disabled")

	h.notifyNodeOffline(7)
	wh.waitCount(t, 1, 3*time.Second)

	// 通知被停用期间恢复：Disabled 维持 V-16 语义——跃迁被消费、
	// 重新启用后不补发恢复。
	if err := r.UpsertConfig(notifyConfigEnabled, "false", time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	h.invalidateNotifyConfigCache()
	h.notifyNodeOnline(7)
	time.Sleep(400 * time.Millisecond)
	if err := r.UpsertConfig(notifyConfigEnabled, "true", time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	h.invalidateNotifyConfigCache()
	h.notifyNodeOnline(7)
	time.Sleep(400 * time.Millisecond)
	if got := wh.count(); got != 1 {
		t.Fatalf("V-16: recovery during disabled period must not be resent, got %d (%v)", got, wh.titles())
	}
}

// ---- R2X-017: latency recovery ----

func TestR2B10LatencyRecoveryFailureResendsOnce(t *testing.T) {
	wh := newFixdWebhook(t)
	h, _ := setupFixdNotifyHandler(t, wh)

	bad := map[int64]externalProbe{
		1: {Points: []latencyPoint{{Name: "香港", OK: true, LatencyMs: 30, LossPct: 50}}},
	}
	good := map[int64]externalProbe{
		1: {Points: []latencyPoint{{Name: "香港", OK: true, LatencyMs: 30, LossPct: 0}}},
	}
	names := map[int64]string{1: "节点一"}

	for i := 0; i < latencyAlertStreak; i++ {
		h.alertLatency(bad, names)
	}
	wh.waitCount(t, 1, 3*time.Second)

	wh.fail.Store(true)
	h.alertLatency(good, names)
	if !waitB10(12*time.Second, func() bool {
		h.latencyAlerts.mu.Lock()
		defer h.latencyAlerts.mu.Unlock()
		return len(h.latencyAlerts.down) > 0
	}) {
		t.Fatal("R2X-017: latency down mark not restored after failed recovery notify")
	}
	if got := wh.count(); got != 1 {
		t.Fatalf("failed recovery must not record deliveries, got %d", got)
	}

	wh.fail.Store(false)
	h.alertLatency(good, names)
	wh.waitCount(t, 2, 3*time.Second)
	if titles := wh.titles(); titles[1] != "延迟已恢复" {
		t.Fatalf("titles = %v", titles)
	}
	h.alertLatency(good, names)
	time.Sleep(300 * time.Millisecond)
	if got := wh.count(); got != 2 {
		t.Fatalf("latency recovery must be resent exactly once, got %d", got)
	}
}

// ---- R2X-017: inbound recovery ----

func TestR2B10InboundRecoveryFailureResendsOnce(t *testing.T) {
	wh := newFixdWebhook(t)
	h, r := setupFixdNotifyHandler(t, wh)
	ctx := context.Background()

	// R2 登记 #3 同型迁移：probe 拨号前过 netguard 拒 loopback
	// （R2X-005/R2X-043），被探测目标须为非 loopback RFC1918 地址。
	probeAddr := probeFixtureLocalAddr(t)
	ln, err := net.Listen("tcp", probeAddr+":0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	row := &model.NodeInbound{Name: "b10-imported", Protocol: "ss", Listen: probeAddr, Port: port,
		Enabled: 1, CreatedTime: time.Now().UnixMilli(), UpdatedTime: time.Now().UnixMilli()}
	if err := r.DB().Create(row).Error; err != nil {
		t.Fatalf("create inbound: %v", err)
	}

	if _, err := h.probeExternalInbounds(ctx, nil, probeRoundScheduled); err != nil {
		t.Fatal(err)
	}
	_ = ln.Close()
	for i := 0; i < notifyInboundDownStreak; i++ {
		if _, err := h.probeExternalInbounds(ctx, nil, probeRoundScheduled); err != nil {
			t.Fatal(err)
		}
	}
	wh.waitCount(t, 1, 3*time.Second)

	ln2, err := net.Listen("tcp", probeAddr+":"+strconv.Itoa(port))
	if err != nil {
		t.Fatalf("relisten: %v", err)
	}
	defer ln2.Close()
	wh.fail.Store(true)
	if _, err := h.probeExternalInbounds(ctx, nil, probeRoundScheduled); err != nil {
		t.Fatal(err)
	}
	if !waitB10(12*time.Second, func() bool {
		h.externalProbes.mu.Lock()
		defer h.externalProbes.mu.Unlock()
		return h.externalProbes.down[row.ID]
	}) {
		t.Fatal("R2X-017: inbound down mark not restored after failed recovery notify")
	}
	if got := wh.count(); got != 1 {
		t.Fatalf("failed recovery must not record deliveries, got %d", got)
	}

	wh.fail.Store(false)
	if _, err := h.probeExternalInbounds(ctx, nil, probeRoundScheduled); err != nil {
		t.Fatal(err)
	}
	wh.waitCount(t, 2, 3*time.Second)
	if titles := wh.titles(); titles[1] != "导入节点已恢复" {
		t.Fatalf("titles = %v", titles)
	}
	if _, err := h.probeExternalInbounds(ctx, nil, probeRoundScheduled); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if got := wh.count(); got != 2 {
		t.Fatalf("inbound recovery must be resent exactly once, got %d", got)
	}
}

// ---- R2X-017: route baseline CheckedAt ----

func TestR2B10RouteBaselineCheckedAtResetOnFailure(t *testing.T) {
	wh := newFixdWebhook(t)
	h, r := setupFixdNotifyHandler(t, wh)
	if err := r.UpsertConfig(latencyTargetsConfigKey,
		`[{"name":"电信","host":"ct.example.com","port":80,"enabled":true}]`, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	nodeID := mustCreateLatencyNode(t, r, "b10-route-node", 1, 0)
	freshAt := time.Now().UnixMilli()
	h.nodeRoutes.set(nodeID, nodeRoute{
		CheckedAt: freshAt,
		Points:    []routePoint{{Name: "电信", Route: "电信 163", host: "ct.example.com"}},
	})
	old := nodeTraceroute
	t.Cleanup(func() { nodeTraceroute = old })
	nodeTraceroute = func(_ *Handler, _ int64, _ string, family int) (map[string]interface{}, error) {
		if family == 6 {
			return nil, context.DeadlineExceeded
		}
		return map[string]interface{}{
			"reached": true,
			"hops":    []interface{}{map[string]interface{}{"ttl": 1.0, "ip": "59.43.180.1"}},
		}, nil
	}
	storedCheckedAt := func() int64 {
		stored, ok := h.nodeRoutes.get(nodeID)
		if !ok {
			return -1
		}
		return stored.CheckedAt
	}

	wh.fail.Store(true)
	if _, err := h.traceNodeRoutes(context.Background(), []int64{nodeID}, true); err != nil {
		t.Fatal(err)
	}
	if !waitB10(12*time.Second, func() bool { return storedCheckedAt() == 0 }) {
		t.Fatalf("R2X-017(route): baseline CheckedAt = %d after failed notify, want 0 (fresh was %d)", storedCheckedAt(), freshAt)
	}

	// 下一周期轮（force=false）应立即重测补发，而非被 routeFresh 挡住。
	wh.fail.Store(false)
	if _, err := h.traceNodeRoutes(context.Background(), []int64{nodeID}, false); err != nil {
		t.Fatal(err)
	}
	wh.waitCount(t, 1, 3*time.Second)
	if titles := wh.titles(); titles[0] != "回程线路变化" {
		t.Fatalf("titles = %v", titles)
	}
}

// ---- R2X-038: monitor access admin field ----

func TestR2B10MonitorAccessAdminField(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now().UnixMilli()
	adminID, err := r.CreateUser("b10-admin", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	viewerID, err := r.CreateUser("b10-viewer", "hash", 1, now+86400000, 1, 1, 100, 1, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.InsertMonitorPermission(viewerID, now); err != nil {
		t.Fatal(err)
	}
	call := func(userID int64, roleID int) (bool, bool) {
		w := httptest.NewRecorder()
		h.monitorAccessHandler(w, claimsCtxReq(http.MethodGet, "/api/v1/monitor/access", "", "", userID, roleID))
		env := decodeEnvelope(t, w)
		if env.Code != 0 {
			t.Fatalf("access code = %d msg=%q", env.Code, env.Msg)
		}
		var data struct {
			Allowed bool `json:"allowed"`
			Admin   bool `json:"admin"`
		}
		if err := json.Unmarshal(env.Data, &data); err != nil {
			t.Fatalf("decode access data: %v (raw=%s)", err, env.Data)
		}
		return data.Allowed, data.Admin
	}
	if allowed, admin := call(adminID, 0); !allowed || !admin {
		t.Fatalf("admin access = allowed:%v admin:%v, want true/true", allowed, admin)
	}
	if allowed, admin := call(viewerID, 1); !allowed || admin {
		t.Fatalf("viewer access = allowed:%v admin:%v, want true/false", allowed, admin)
	}
}
