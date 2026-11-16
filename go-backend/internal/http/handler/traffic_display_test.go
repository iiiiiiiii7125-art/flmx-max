package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/auth"
	"go-backend/internal/http/middleware"
	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// jsonNum reads a JSON-decoded number field as float64.
func jsonNum(m map[string]interface{}, key string) float64 {
	v, _ := m[key].(float64)
	return v
}

// trafficDisplayRow builds a NodeInbound row from a kernel inbound
// (applyInboundToModel serializes settings/transport/security); mutate
// adjusts row-level fields such as MeteringEnabled after the fact.
func trafficDisplayRow(t *testing.T, id int64, in kernel.Inbound, mutate func(*model.NodeInbound)) model.NodeInbound {
	t.Helper()
	row := model.NodeInbound{ID: id, KernelID: 1, NodeID: 1, Name: "t", Enabled: 1}
	if err := applyInboundToModel(in, &row); err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(&row)
	}
	return row
}

func trafficSS2022() kernel.Inbound {
	return kernel.Inbound{Protocol: kernel.ProtocolShadowsocks, Port: 8388,
		Settings: kernel.Settings{Method: "2022-blake3-aes-128-gcm", Password: "AAAAAAAAAAAAAAAAAAAAAA=="}}
}

func TestSubMeteringSourceReason(t *testing.T) {
	ssOn := func(r *model.NodeInbound) { r.MeteringEnabled = 1 }
	ssOff := func(r *model.NodeInbound) { r.MeteringEnabled = 0 }
	cases := []struct {
		name string
		row  model.NodeInbound
		live bool
		want string
	}{
		{
			name: "vless per-user is metered natively",
			row: trafficDisplayRow(t, 1, kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 443,
				Security: kernel.Security{Type: kernel.SecurityReality}}, nil),
			want: "",
		},
		{
			name: "ss2022 switch on no companion",
			row:  trafficDisplayRow(t, 2, trafficSS2022(), ssOn),
			want: "伴生入站未下发到节点，不计量",
		},
		{
			name: "ss2022 switch on live companion",
			row:  trafficDisplayRow(t, 3, trafficSS2022(), ssOn),
			live: true,
			want: "",
		},
		{
			name: "ss2022 switch off",
			row:  trafficDisplayRow(t, 4, trafficSS2022(), ssOff),
			want: "计量开关已关，不计量",
		},
		{
			name: "ss2022 shadowtls front",
			row: trafficDisplayRow(t, 5, kernel.Inbound{Protocol: kernel.ProtocolShadowsocks, Port: 8388,
				Settings: kernel.Settings{Method: "2022-blake3-aes-128-gcm", Password: "AAAAAAAAAAAAAAAAAAAAAA=="},
				Security: kernel.Security{Type: kernel.SecurityShadowTLS}}, ssOn),
			want: "ShadowTLS 前置入站，不计量",
		},
		{
			name: "ss non-2022 method",
			row: trafficDisplayRow(t, 6, kernel.Inbound{Protocol: kernel.ProtocolShadowsocks, Port: 8388,
				Settings: kernel.Settings{Method: "aes-128-gcm", Password: "AAAAAAAAAAAAAAAAAAAAAA=="}}, ssOn),
			want: "非 SS2022 入站，不计量",
		},
		{
			name: "ss2022 proxy protocol shape",
			row: trafficDisplayRow(t, 7, trafficSS2022(), func(r *model.NodeInbound) {
				r.MeteringEnabled = 1
				r.AcceptProxyProtocol = 1
			}),
			want: "开启 PROXY 协议的入站不支持订阅计量",
		},
		{
			name: "ss2022 relay-only shape",
			row: trafficDisplayRow(t, 8, trafficSS2022(), func(r *model.NodeInbound) {
				r.MeteringEnabled = 1
				r.RelayOnly = 1
			}),
			want: "仅中转入站不支持订阅计量",
		},
		{
			name: "external imported node",
			row: trafficDisplayRow(t, 9, trafficSS2022(), func(r *model.NodeInbound) {
				r.KernelID = 0
			}),
			want: "导入节点共用凭据，不计量",
		},
		{
			name: "broken settings json",
			row: func() model.NodeInbound {
				r := trafficDisplayRow(t, 10, trafficSS2022(), ssOn)
				r.Settings = "{bad json"
				return r
			}(),
			want: "入站配置无法解析，不计量",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := subMeteringSourceReason(&c.row, c.live); got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestSubMeteringStatus(t *testing.T) {
	h := &Handler{}
	vless := trafficDisplayRow(t, 1, kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 443,
		Security: kernel.Security{Type: kernel.SecurityReality}}, nil)
	ssOff := trafficDisplayRow(t, 2, trafficSS2022(), func(r *model.NodeInbound) { r.MeteringEnabled = 0 })
	ssOn := trafficDisplayRow(t, 3, trafficSS2022(), func(r *model.NodeInbound) { r.MeteringEnabled = 1 })
	noCompanions := &companionGateMemo{loaded: true, byOrigin: map[int64]kernel.Inbound{}}
	share := func(id int64, sourceAll int, inboundIDs string) *model.Subscription {
		return &model.Subscription{ID: id, Name: "s", Kind: model.SubscriptionShare,
			Enabled: 1, SourceAll: sourceAll, InboundIDs: inboundIDs}
	}
	cases := []struct {
		name   string
		sub    *model.Subscription
		rows   []model.NodeInbound
		memo   *companionGateMemo
		status string
		reason string
	}{
		{
			name:   "own subscription not applicable",
			sub:    &model.Subscription{ID: 1, Name: "o", Kind: model.SubscriptionOwn, Enabled: 1, SourceAll: 1},
			rows:   []model.NodeInbound{vless},
			memo:   noCompanions,
			status: "", reason: "",
		},
		{
			name:   "share id over metering cap",
			sub:    share(kernel.MaxUserMarkID+1, 1, ""),
			rows:   []model.NodeInbound{vless},
			memo:   noCompanions,
			status: "unmetered", reason: "订阅 ID 超出计量编码上限，流量永不计量",
		},
		{
			name:   "share no sources",
			sub:    share(10, 0, ""),
			rows:   []model.NodeInbound{vless},
			memo:   noCompanions,
			status: "unmetered", reason: "未选择任何入站，流量不计量",
		},
		{
			name:   "share selected inbounds gone",
			sub:    share(15, 0, "99"),
			rows:   []model.NodeInbound{vless},
			memo:   noCompanions,
			status: "unmetered", reason: "所选入站均不存在，流量不计量",
		},
		{
			name:   "share all metered",
			sub:    share(11, 1, ""),
			rows:   []model.NodeInbound{vless},
			memo:   noCompanions,
			status: "metered", reason: "",
		},
		{
			name:   "share all unmetered switch off",
			sub:    share(12, 1, ""),
			rows:   []model.NodeInbound{ssOff},
			memo:   noCompanions,
			status: "unmetered", reason: "计量开关已关，不计量",
		},
		{
			name:   "share partial one of two unmetered",
			sub:    share(13, 1, ""),
			rows:   []model.NodeInbound{vless, ssOff},
			memo:   noCompanions,
			status: "partial", reason: "其中 1 个源未计量（计量开关已关，不计量）",
		},
		{
			name:   "share ss with live companion is metered",
			sub:    share(14, 1, ""),
			rows:   []model.NodeInbound{ssOn},
			memo:   &companionGateMemo{loaded: true, byOrigin: map[int64]kernel.Inbound{3: {}}},
			status: "metered", reason: "",
		},
		{
			// D-OBS-01：截断进行中（超限且已有计数）伴生重建，
			// 伴生未下发是瞬态——徽标不翻未计量。
			name:   "share over quota with counts keeps metered while companion rebuilding",
			sub:    &model.Subscription{ID: 16, Name: "s", Kind: model.SubscriptionShare, Enabled: 1, SourceAll: 1, TrafficLimit: 100, UploadBytes: 60, DownloadBytes: 60},
			rows:   []model.NodeInbound{ssOn},
			memo:   noCompanions,
			status: "metered", reason: "",
		},
		{
			// D-OBS-01 对照：未超限时伴生缺失仍如实报未计量，
			// 抑制只在截断进行中生效。
			name:   "share not over quota with counts still unmetered without companion",
			sub:    &model.Subscription{ID: 17, Name: "s", Kind: model.SubscriptionShare, Enabled: 1, SourceAll: 1, TrafficLimit: 1000, UploadBytes: 60, DownloadBytes: 60},
			rows:   []model.NodeInbound{ssOn},
			memo:   noCompanions,
			status: "unmetered", reason: "伴生入站未下发到节点，不计量",
		},
		{
			// D-OBS-01：多源时被抑制的伴生成因不进 partial 文案。
			name:   "share over quota mixed sources suppressed companion reason stays metered",
			sub:    &model.Subscription{ID: 18, Name: "s", Kind: model.SubscriptionShare, Enabled: 1, SourceAll: 1, TrafficLimit: 100, UploadBytes: 60, DownloadBytes: 60},
			rows:   []model.NodeInbound{vless, ssOn},
			memo:   noCompanions,
			status: "metered", reason: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, reason := h.subMeteringStatus(c.sub, c.rows, c.memo)
			if status != c.status || reason != c.reason {
				t.Fatalf("got (%q, %q) want (%q, %q)", status, reason, c.status, c.reason)
			}
		})
	}
}

func TestSubZeroEndpointReason(t *testing.T) {
	h := &Handler{}
	now := time.Now()
	active := &model.Subscription{ID: 1, Name: "s", Kind: model.SubscriptionShare, Enabled: 1, SourceAll: 1}
	vless := trafficDisplayRow(t, 1, kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 443,
		Security: kernel.Security{Type: kernel.SecurityReality}}, nil)
	disabled := trafficDisplayRow(t, 2, kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 443,
		Security: kernel.Security{Type: kernel.SecurityReality}}, func(r *model.NodeInbound) { r.Enabled = 0 })
	relayOnly := trafficDisplayRow(t, 3, kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 443,
		Security: kernel.Security{Type: kernel.SecurityReality}}, func(r *model.NodeInbound) { r.RelayOnly = 1 })
	cases := []struct {
		name string
		sub  *model.Subscription
		rows []model.NodeInbound
		want string
	}{
		{
			name: "inactive subscription uses its own placeholder",
			sub:  &model.Subscription{ID: 2, Name: "d", Kind: model.SubscriptionShare, Enabled: 0, SourceAll: 1},
			rows: []model.NodeInbound{},
			want: "",
		},
		{
			name: "over-quota subscription uses its own placeholder",
			sub: &model.Subscription{ID: 3, Name: "q", Kind: model.SubscriptionShare, Enabled: 1,
				SourceAll: 1, TrafficLimit: 100, UploadBytes: 60, DownloadBytes: 60},
			rows: []model.NodeInbound{},
			want: "",
		},
		{
			name: "active no sources",
			sub:  active,
			rows: []model.NodeInbound{},
			want: "系统中没有任何入站，拉取为空",
		},
		{
			name: "active explicit selection but nothing chosen",
			sub:  &model.Subscription{ID: 8, Name: "n", Kind: model.SubscriptionShare, Enabled: 1, InboundIDs: ""},
			rows: []model.NodeInbound{vless},
			want: "该订阅未选择任何入站，拉取为空",
		},
		{
			name: "active selected inbounds missing",
			sub:  &model.Subscription{ID: 4, Name: "m", Kind: model.SubscriptionShare, Enabled: 1, InboundIDs: "99"},
			rows: []model.NodeInbound{vless},
			want: "所选入站均不存在，拉取为空",
		},
		{
			name: "active all selected disabled",
			sub:  &model.Subscription{ID: 5, Name: "x", Kind: model.SubscriptionShare, Enabled: 1, InboundIDs: "2"},
			rows: []model.NodeInbound{disabled},
			want: "所选入站均已停用，拉取为空",
		},
		{
			name: "active all relay-only",
			sub:  &model.Subscription{ID: 6, Name: "r", Kind: model.SubscriptionShare, Enabled: 1, InboundIDs: "3"},
			rows: []model.NodeInbound{relayOnly},
			want: "所选入站均为仅中转或仅本机监听，且其中转未就绪，拉取为空",
		},
		{
			name: "active usable source falls to generic",
			sub:  &model.Subscription{ID: 7, Name: "g", Kind: model.SubscriptionShare, Enabled: 1, InboundIDs: "1"},
			rows: []model.NodeInbound{vless},
			want: "该订阅当前没有可用节点（中转未就绪、分享地址无效或格式过滤），请检查入站与中转状态",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := h.subZeroEndpointReason(c.sub, c.rows, now); got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestSubYieldsNothingAndScopeReason(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/yield.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	nodeID := seedRelayTargetNode(t, r, "y-node", "203.0.113.20")
	k := seedChainKernel(t, r, nodeID, kernel.TypeXray)
	vless := seedChainInbound(t, r, k, "y-vless", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 443,
		Security: kernel.Security{Type: kernel.SecurityReality}}, 0)
	rows := []model.NodeInbound{*vless}
	now := time.Now()

	// Healthy source, no scope: yields endpoints (the reason helper is
	// only consulted when the render is already empty, so no assertion
	// on its return here).
	plain := &model.Subscription{ID: 1, Name: "p", Kind: model.SubscriptionShare, Enabled: 1, SourceAll: 1}
	if h.subYieldsNothing(plain, rows) {
		t.Fatal("healthy vless source must yield endpoints")
	}
	// Healthy source, relay scope: yields before scope, empty after —
	// the reason must name the scope, not the generic broken text.
	relayScoped := &model.Subscription{ID: 2, Name: "r", Kind: model.SubscriptionShare, Enabled: 1,
		SourceAll: 1, Options: `{"lines":"relay"}`}
	if h.subYieldsNothing(relayScoped, rows) {
		t.Fatal("relay-scoped subscription yields endpoints before scope")
	}
	if got := h.subZeroEndpointReason(relayScoped, rows, now); got != "按订阅的中转范围筛选后无可用节点，拉取为空" {
		t.Fatalf("scope reason: got %q", got)
	}
	// No sources at all: yields nothing.
	empty := &model.Subscription{ID: 3, Name: "e", Kind: model.SubscriptionShare, Enabled: 1, SourceAll: 1}
	if !h.subYieldsNothing(empty, nil) {
		t.Fatal("sourceless subscription yields nothing")
	}
	// All sources disabled: yields nothing.
	disabled := trafficDisplayRow(t, 9, kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 443,
		Security: kernel.Security{Type: kernel.SecurityReality}}, func(r *model.NodeInbound) { r.Enabled = 0 })
	disSub := &model.Subscription{ID: 4, Name: "d", Kind: model.SubscriptionShare, Enabled: 1, InboundIDs: "9"}
	if !h.subYieldsNothing(disSub, []model.NodeInbound{disabled}) {
		t.Fatal("all-disabled sources yield nothing")
	}
}

// fetchSubFormat runs openSubscription for a token with an explicit
// format (the notice-node reason is only legible in a text format).
func fetchSubFormat(t *testing.T, h *Handler, token, format string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/open_api/sub/"+token+"?format="+format, nil)
	rec := httptest.NewRecorder()
	h.openSubscription(rec, req)
	return rec.Code, rec.Body.String()
}

func TestOpenSubscriptionZeroEndpointPlaceholder(t *testing.T) {
	h, r, ids := subFixture(t)
	_ = ids
	// (a) Genuinely broken: share subscription with no sources at all —
	// the pull must carry the placeholder node with the reason.
	broken := createShareSub(t, r, &model.Subscription{Name: "broken", Kind: model.SubscriptionShare,
		Token: strings.Repeat("b", 32), UUID: "u-b", Password: "pw-b", Enabled: 1})
	code, body := fetchSubFormat(t, h, broken.Token, "clash")
	if code != 200 {
		t.Fatalf("broken pull code = %d", code)
	}
	if !strings.Contains(body, "未选择任何入站") {
		t.Fatalf("broken pull must carry the zero-endpoint reason, got: %s", body)
	}
	// (b) Relay scope over direct-only inbounds: the scope's answer is
	// an empty pull — no placeholder node, no node URI at all.
	scoped := createShareSub(t, r, &model.Subscription{Name: "scoped", Kind: model.SubscriptionShare,
		Token: strings.Repeat("s", 32), UUID: "u-s", Password: "pw-s", Enabled: 1,
		SourceAll: 1, Options: `{"lines":"relay"}`})
	code, body = fetchSubFormat(t, h, scoped.Token, "clash")
	if code != 200 {
		t.Fatalf("scoped pull code = %d", code)
	}
	if strings.Contains(body, "trojan://") || strings.Contains(body, "未选择任何入站") || strings.Contains(body, "中转范围") {
		t.Fatalf("scoped pull must stay empty (no node, no placeholder): %s", body)
	}
	// (c) Selected inbounds gone: placeholder names the missing sources.
	gone := createShareSub(t, r, &model.Subscription{Name: "gone", Kind: model.SubscriptionShare,
		Token: strings.Repeat("g", 32), UUID: "u-g", Password: "pw-g", Enabled: 1, InboundIDs: "99999"})
	code, body = fetchSubFormat(t, h, gone.Token, "clash")
	if code != 200 {
		t.Fatalf("gone pull code = %d", code)
	}
	if !strings.Contains(body, "所选入站均不存在") {
		t.Fatalf("gone pull must name the missing sources, got: %s", body)
	}
}

func TestNotifyHealth(t *testing.T) {
	setConfig := func(t *testing.T, r *repo.Repository, kv map[string]string) {
		t.Helper()
		now := time.Now().UnixMilli()
		for k, v := range kv {
			if err := r.DB().Exec(`INSERT INTO vite_config(name, value, time) VALUES(?, ?, ?) ON CONFLICT(name) DO UPDATE SET value = excluded.value`,
				k, v, now).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	call := func(t *testing.T, h *Handler) map[string]interface{} {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/notify/health", nil)
		rec := httptest.NewRecorder()
		h.notifyHealth(rec, req)
		var out struct {
			Code int                    `json:"code"`
			Msg  string                 `json:"msg"`
			Data map[string]interface{} `json:"data"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
			t.Fatalf("decode notify health: %v", err)
		}
		if out.Code != 0 {
			t.Fatalf("notify health code = %d (%s)", out.Code, out.Msg)
		}
		return out.Data
	}
	cases := []struct {
		name    string
		config  map[string]string
		usable  bool
		missing []string
	}{
		{
			name:    "enabled but no channel is unusable",
			config:  map[string]string{"notify_enabled": "true"},
			usable:  false,
			missing: []string{"telegramToken", "telegramChatId", "webhook"},
		},
		{
			name:    "token without chat id still unusable",
			config:  map[string]string{"notify_enabled": "true", "notify_telegram_bot_token": "tok"},
			usable:  false,
			missing: []string{"telegramChatId", "webhook"},
		},
		{
			name:    "token plus chat id is usable",
			config:  map[string]string{"notify_enabled": "true", "notify_telegram_bot_token": "tok", "notify_telegram_chat_id": "123"},
			usable:  true,
			missing: []string{},
		},
		{
			name:    "webhook alone is usable",
			config:  map[string]string{"notify_enabled": "true", "notify_webhook_url": "https://example.invalid/hook"},
			usable:  true,
			missing: []string{},
		},
		{
			name:    "events none is unusable",
			config:  map[string]string{"notify_enabled": "true", "notify_telegram_bot_token": "tok", "notify_telegram_chat_id": "123", "notify_events": "none"},
			usable:  false,
			missing: []string{"events"},
		},
		{
			name:    "master off is not a gap",
			config:  map[string]string{"notify_enabled": "false"},
			usable:  false,
			missing: []string{},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := repo.Open(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = r.Close() })
			setConfig(t, r, c.config)
			h := New(r, "unit-test-secret")
			data := call(t, h)
			if data["usable"] != c.usable {
				t.Fatalf("usable = %v, want %v (data %v)", data["usable"], c.usable, data)
			}
			gotMissing, _ := data["missing"].([]interface{})
			if len(gotMissing) != len(c.missing) {
				t.Fatalf("missing = %v, want %v", gotMissing, c.missing)
			}
			for i, m := range c.missing {
				if gotMissing[i] != m {
					t.Fatalf("missing = %v, want %v", gotMissing, c.missing)
				}
			}
			if _, ok := data["telegramToken"]; ok {
				t.Fatalf("health must not leak secrets, got keys %v", data)
			}
		})
	}
}

func TestUserQuotaMine(t *testing.T) {
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	now := time.Now()
	dayKey := now.Year()*10000 + int(now.Month())*100 + now.Day()
	monthKey := now.Year()*100 + int(now.Month())
	if err := r.DB().Exec(`INSERT INTO user_quota(user_id, daily_limit_gb, monthly_limit_gb, daily_used_bytes, monthly_used_bytes, day_key, month_key, created_time, updated_time) VALUES(2, 10, 100, 1234567, 76543210, ?, ?, ?, ?)`,
		dayKey, monthKey, now.UnixMilli(), now.UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	// Own quota for the caller (user 2, regular role).
	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/quota/mine", nil)
	req = req.WithContext(context.WithValue(req.Context(), middleware.ClaimsContextKey, auth.Claims{Sub: "2", RoleID: 1}))
	rec := httptest.NewRecorder()
	h.userQuotaMine(rec, req)
	var out struct {
		Code int                    `json:"code"`
		Data map[string]interface{} `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode user quota mine: %v", err)
	}
	if out.Code != 0 {
		t.Fatalf("user quota mine code = %d", out.Code)
	}
	if jsonNum(out.Data, "dailyQuotaGB") != 10 || jsonNum(out.Data, "monthlyQuotaGB") != 100 ||
		jsonNum(out.Data, "dailyUsedBytes") != 1234567 || jsonNum(out.Data, "monthlyUsedBytes") != 76543210 {
		t.Fatalf("user quota mine data = %v", out.Data)
	}
	// No quota row at all → data null (not an error).
	req = httptest.NewRequest(http.MethodPost, "/api/v1/user/quota/mine", nil)
	req = req.WithContext(context.WithValue(req.Context(), middleware.ClaimsContextKey, auth.Claims{Sub: "99", RoleID: 1}))
	rec = httptest.NewRecorder()
	h.userQuotaMine(rec, req)
	var out2 struct {
		Code int         `json:"code"`
		Data interface{} `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out2); err != nil {
		t.Fatalf("decode user quota mine (no row): %v", err)
	}
	if out2.Code != 0 || out2.Data != nil {
		t.Fatalf("user quota mine (no row) = code %d data %v, want code 0 data null", out2.Code, out2.Data)
	}
}

func TestUserQuotaResetOfflineNodeDeferred(t *testing.T) {
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	now := time.Now()
	nowMs := now.UnixMilli()
	userID, err := r.CreateUser("f4-deferred", "hash", 1, 0, 0, 0, 10, 1, 0, nowMs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.AddUserQuotaUsage(userID, 0, now); err != nil {
		t.Fatal(err)
	}
	// Forward 1: paused by quota, entry port on node 99 — no agent is
	// ever connected in tests, so node 99 is offline. The tunnel must
	// exist and be enabled, or the release path skips the forward
	// before reaching the (offline) service call.
	if err := r.DB().Exec(`INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, in_ip, inx) VALUES(1, 't-one', 1, 1, 'tls', 1, ?, ?, 1, NULL, 0)`,
		nowMs, nowMs).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Exec(`INSERT INTO forward(id, user_id, user_name, name, tunnel_id, remote_addr, strategy, in_flow, out_flow, created_time, updated_time, status, inx) VALUES(1, ?, 'f4-deferred', 'f-deferred', 1, '10.0.0.1:80', 'fifo', 0, 0, ?, ?, 0, 0)`,
		userID, nowMs, nowMs).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Exec(`INSERT INTO forward_port(forward_id, node_id, port) VALUES(1, 99, 20000)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Exec(`UPDATE user_quota SET daily_limit_gb = 0, monthly_limit_gb = 0, daily_used_bytes = 0, monthly_used_bytes = 0, disabled_by_quota = 1, disabled_at = ?, paused_forward_ids = '1' WHERE user_id = ?`,
		nowMs, userID).Error; err != nil {
		t.Fatal(err)
	}
	code, msg := r171b5CallUserQuotaReset(t, h, userID, "all")
	if code != 0 {
		t.Fatalf("reset returned code %d (msg %q), want 0 with deferred receipt", code, msg)
	}
	if !strings.Contains(msg, "待节点上线后恢复") {
		t.Fatalf("reset receipt msg = %q, want it to name the offline-node deferral", msg)
	}
	// F2 根治（quota_release_offline_f4_test.go）后：离线节点释放不再
	// 把行标 Active——行保持暂停、留在待恢复清单由 backstop 在节点上线
	// 后重试；回执的「待节点上线后恢复」与行态一致，不再是与行相悖的
	// 收据层修正（F4 原断言“行标 Active”是根治前的旧行为前提）。
	if st := mustQueryInt(t, r, `SELECT status FROM forward WHERE id = 1`); st != 0 {
		t.Fatalf("forward status after release = %d, want 0 (still paused, never Active on offline node)", st)
	}
}

func TestSubscriptionResetTrafficPushFailureReceipt(t *testing.T) {
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	nowMs := time.Now().UnixMilli()
	nodeID, err := r.CreateNode("f4-node", "secret", "192.0.2.1", nil, nil, "1000-65535", nil, nil, nil, nil, nil, 0, 0, 0, nowMs, 1, "", "", 1, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	k := seedChainKernel(t, r, nodeID, "sing-box")
	seedChainInbound(t, r, k, "f4-vless", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 443,
		Security: kernel.Security{Type: kernel.SecurityReality}}, 0)
	s := &model.Subscription{Name: "f4-pushfail", Kind: model.SubscriptionShare, Token: "f4-pushfail-token",
		SourceAll: 1, Enabled: 1, AppliedActive: -1}
	if err := r.SaveSubscription(s); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"id":%d}`, s.ID)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/subscription/reset-traffic", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.subscriptionResetTraffic(rec, req)
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("decode reset-traffic response: %v (body %q)", err, rec.Body.String())
	}
	if env.Code != 0 {
		t.Fatalf("reset-traffic returned code %d (msg %q), want 0 with failure receipt", env.Code, env.Msg)
	}
	if !strings.Contains(env.Msg, "下发到节点失败") {
		t.Fatalf("reset-traffic receipt msg = %q, want it to name the push failure", env.Msg)
	}
	if got := mustQueryInt(t, r, `SELECT applied_active FROM subscription WHERE id = ?`, s.ID); got != -1 {
		t.Fatalf("applied_active after failed push = %d, want -1 (must not be optimistically set)", got)
	}
}

func TestLastSubscriptionCounterUpdates(t *testing.T) {
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	// Seed counter rows directly: sub 7 reported twice (max wins),
	// sub 8 once, sub 9 never.
	seed := func(subID, updated int64, dir string) {
		t.Helper()
		if err := r.DB().Exec(
			`INSERT INTO subscription_counter(node_id, subscription_id, direction, epoch, bytes, updated_time) VALUES(1, ?, ?, '', 100, ?)`,
			subID, dir, updated).Error; err != nil {
			t.Fatal(err)
		}
	}
	seed(7, 1700000000000, "up")
	seed(7, 1700000001000, "down")
	seed(8, 1700000002000, "up")

	got, err := r.LastSubscriptionCounterUpdates([]int64{7, 8, 9})
	if err != nil {
		t.Fatal(err)
	}
	if got[7] != 1700000001000 || got[8] != 1700000002000 {
		t.Fatalf("got %v", got)
	}
	if _, ok := got[9]; ok {
		t.Fatalf("never-reported subscription must be absent, got %v", got)
	}
	if empty, err := r.LastSubscriptionCounterUpdates(nil); err != nil || len(empty) != 0 {
		t.Fatalf("empty ids must give empty map, got %v err %v", empty, err)
	}
}

func TestNodeTrafficLastHour(t *testing.T) {
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	seed := func(nodeID, hour int64) {
		t.Helper()
		if err := r.DB().Exec(
			`INSERT INTO node_traffic(node_id, hour, in_bytes, out_bytes) VALUES(?, ?, 1, 2)`,
			nodeID, hour).Error; err != nil {
			t.Fatal(err)
		}
	}
	seed(1, 1000)
	seed(1, 2000)
	seed(2, 1500)

	got, err := r.NodeTrafficLastHour()
	if err != nil {
		t.Fatal(err)
	}
	if got[1] != 2000 || got[2] != 1500 || len(got) != 2 {
		t.Fatalf("got %v", got)
	}
}
