package handler

import (
	"context"
	"encoding/base64"
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
	"go-backend/internal/monitoring"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// FIX2-B 回归批（V4-N1 / V5-1 / V5-2）：
//   - V4-N1：备份导入写 configs 段后未失效通知/prober 配置缓存；
//     prober 失效 switch 漏 retention 键。
//   - V5-1：拉取侧 ?user=&sig= 个性化链接按用户入站分配过滤端点；
//     其计数口径曾被 mine 复用——R1-C08（B10-001）纠正：mine 的
//     卡片是独立链接（无 ?user=），计数改全量口径并与无 ?user=
//     拉取恒等；个性化拉取的过滤语义本身不变，仍在此守。
//   - V5-2：format 参数大小写口径两入口不一致（计数侧敏感、拉取侧
//     ToLower），"Clash" 在计数侧静默回落 base64。

// V5-2：nodeCountFormat 与拉取侧同口径大小写不敏感。
func TestFix2BNodeCountFormatCaseInsensitive(t *testing.T) {
	cases := map[string]string{
		"Clash":        kernel.FormatClash,
		"CLASH":        kernel.FormatClash,
		"SingBox":      kernel.FormatSingbox,
		" surge ":      kernel.FormatSurge,
		"ShadowRocket": kernel.FormatShadowrocket,
		"":             kernel.FormatBase64,
		"bogus":        kernel.FormatBase64,
	}
	for in, want := range cases {
		if got := nodeCountFormat(in); got != want {
			t.Fatalf("nodeCountFormat(%q) = %q, want %q", in, got, want)
		}
	}
}

// V5-1 纯逻辑面：按用户分配过滤端点——直连端点需 relayID=0 的分配，
// 中转端点需该 relay 的分配（与 openSubscription 拉取侧同语义）。
func TestFix2BFilterEndpointsByUserAssignments(t *testing.T) {
	mk := func() []kernel.Endpoint {
		return []kernel.Endpoint{
			{Name: "a-direct", Direct: true, Inbound: kernel.Inbound{ID: 1}},
			{Name: "a-relay9", RelayID: 9, Inbound: kernel.Inbound{ID: 1}},
			{Name: "b-direct", Direct: true, Inbound: kernel.Inbound{ID: 2}},
		}
	}
	names := func(eps []kernel.Endpoint) []string {
		out := make([]string, 0, len(eps))
		for _, e := range eps {
			out = append(out, e.Name)
		}
		return out
	}
	join := func(eps []kernel.Endpoint) string { return strings.Join(names(eps), ",") }

	if got := filterEndpointsByUserAssignments(mk(), []repo.UserInboundAssignment{{InboundID: 1, RelayID: 0}}); join(got) != "a-direct" {
		t.Fatalf("direct-only assignment: got %v", names(got))
	}
	if got := filterEndpointsByUserAssignments(mk(), []repo.UserInboundAssignment{{InboundID: 1, RelayID: 9}}); join(got) != "a-relay9" {
		t.Fatalf("relay-only assignment: got %v", names(got))
	}
	if got := filterEndpointsByUserAssignments(mk(), []repo.UserInboundAssignment{{InboundID: 1, RelayID: 0}, {InboundID: 1, RelayID: 9}, {InboundID: 2, RelayID: 0}}); join(got) != "a-direct,a-relay9,b-direct" {
		t.Fatalf("full assignment: got %v", names(got))
	}
}

// V5-1 集成面（R1-C08/B10-001 纠正口径）：mine 的卡片是独立链接
// （path 自带 token、不带 ?user=），其 nodeCount 必须等于无 ?user=
// 拉取实际拿到的节点数，对只走订阅分配的用户亦然；同时守住
// FIX2-B 的原语义——?user=&sig= 个性化拉取仍按入站分配过滤
// （同 fixture 下三名用户的个性化实得仍为 1/0/0），管理员全量
// 口径不受影响。
func TestFix2BMineNodeCountMatchesIndependentPull(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/fix2b_mine.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	now := time.Now().UnixMilli()

	if err := r.DB().Exec(`INSERT INTO node(name, secret, server_ip, port, created_time, status) VALUES('n', 's', '203.0.113.1', '1000-2000', ?, 1)`, now).Error; err != nil {
		t.Fatal(err)
	}
	nodeID := mustLastInsertID(t, r, "node")
	if err := r.DB().Exec(`INSERT INTO node_kernel(node_id, kernel_type, version, status, created_time, updated_time) VALUES(?, 'xray', 'latest', 'pending', ?, ?)`, nodeID, now, now).Error; err != nil {
		t.Fatal(err)
	}
	kernelID := mustLastInsertID(t, r, "kernel")
	mkInbound := func(name string, port int) int64 {
		if err := r.DB().Exec(`INSERT INTO node_inbound(kernel_id, node_id, name, protocol, listen, port, settings, enabled, created_time, updated_time)
			VALUES(?, ?, ?, 'trojan', '::', ?, '{"password":"owner-pw"}', 1, ?, ?)`, kernelID, nodeID, name, port, now, now).Error; err != nil {
			t.Fatal(err)
		}
		return mustLastInsertID(t, r, "inbound "+name)
	}
	inboundA := mkInbound("a", 443)
	inboundB := mkInbound("b", 444)
	_ = inboundB

	token := strings.Repeat("d", 32)
	sub := &model.Subscription{Name: "all", Kind: model.SubscriptionShare, Token: token,
		UUID: "u", Password: "p", SourceAll: 1, Enabled: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.SaveSubscription(sub); err != nil {
		t.Fatal(err)
	}
	// 用户 2 被分配该订阅，但入站分配只有 A 的直连；用户 3 被分配
	// 订阅但无任何入站分配；用户 4 只有 B 的某个中转分配（无直连）。
	if err := r.SetUserSubscriptions(2, []int64{sub.ID}); err != nil {
		t.Fatal(err)
	}
	if err := r.SetUserSubscriptions(3, []int64{sub.ID}); err != nil {
		t.Fatal(err)
	}
	if err := r.SetUserSubscriptions(4, []int64{sub.ID}); err != nil {
		t.Fatal(err)
	}
	if err := r.SetUserInboundAssignments(2, []repo.UserInboundAssignment{{InboundID: inboundA, RelayID: 0}}); err != nil {
		t.Fatal(err)
	}
	if err := r.SetUserInboundAssignments(4, []repo.UserInboundAssignment{{InboundID: inboundB, RelayID: 77}}); err != nil {
		t.Fatal(err)
	}

	mineCount := func(userID int64) int {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/user-subscription/mine", strings.NewReader("{}"))
		ctx := context.WithValue(req.Context(), middleware.ClaimsContextKey,
			auth.Claims{Sub: fmt.Sprint(userID), RoleID: 1})
		rec := httptest.NewRecorder()
		h.userSubscriptionMine(rec, req.WithContext(ctx))
		var env struct {
			Code int `json:"code"`
			Data struct {
				Subscriptions []userSubscriptionMineView `json:"subscriptions"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode mine: %v (body=%s)", err, rec.Body.String())
		}
		if env.Code != 0 || len(env.Data.Subscriptions) != 1 {
			t.Fatalf("mine user %d: code=%d subs=%+v", userID, env.Code, env.Data.Subscriptions)
		}
		return env.Data.Subscriptions[0].NodeCount
	}

	countPull := func(url string) int {
		req := httptest.NewRequest(http.MethodGet, url, nil)
		rec := httptest.NewRecorder()
		h.openSubscription(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("pull %s: status=%d body=%s", url, rec.Code, rec.Body.String())
		}
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(rec.Body.String()))
		if err != nil {
			t.Fatalf("pull %s: body not base64: %v", url, err)
		}
		n := 0
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.TrimSpace(line) != "" {
				n++
			}
		}
		return n
	}
	personalizedPullCount := func(userID int64) int {
		sig := h.subURLSigForToken(token, userID)
		return countPull(fmt.Sprintf("/api/v1/open_api/sub/%s?format=base64&user=%d&sig=%s", token, userID, sig))
	}
	// 独立链接：mine 卡片实际呈现的链接（无 ?user=、不带 sig）。
	independentPullCount := func() int {
		return countPull(fmt.Sprintf("/api/v1/open_api/sub/%s?format=base64", token))
	}

	// 管理员全量口径：两个入站都计。
	rows, err := r.ListNodeInbounds()
	if err != nil {
		t.Fatal(err)
	}
	if got := h.subscriptionView(sub, rows, time.Now(), "").NodeCount; got != 2 {
		t.Fatalf("admin view nodeCount = %d, want 2", got)
	}

	// B10-001：独立链接实得 2；三名被分配用户的 mine 必须都等于它，
	// 与各自的旧入站分配无关（用户 3 无任何入站分配，旧口径计 0）。
	if got := independentPullCount(); got != 2 {
		t.Fatalf("independent pull = %d, want 2 (fixture sanity)", got)
	}
	for _, userID := range []int64{2, 3, 4} {
		if got := mineCount(userID); got != 2 {
			t.Fatalf("B10-001: mine nodeCount user %d = %d, want 2 (independent-link count)", userID, got)
		}
	}
	// FIX2-B 原语义守恒：个性化拉取仍按入站分配过滤。
	for userID, want := range map[int64]int{2: 1, 3: 0, 4: 0} {
		if got := personalizedPullCount(userID); got != want {
			t.Fatalf("personalized pull count user %d = %d, want %d (FIX2-B pull semantics)", userID, got, want)
		}
	}
}

// V4-N1：备份导入 configs 段后，通知缓存与 prober 缓存必须即时
// 失效（与 updateConfigs/updateSingleConfig 两 UI 写入口同口径）；
// prober 失效 switch 须含 retention 键。
func TestFix2BBackupImportInvalidatesConfigCaches(t *testing.T) {
	h, _ := setupFixdNotifyHandler(t, newFixdWebhook(t))

	// 预热两处缓存：通知侧读到 notify_enabled=true；prober 侧载入配置。
	if _, _, ok := h.notifyConfig(notifyEventNodeOffline); !ok {
		t.Fatal("setup: offline event should be enabled")
	}
	_ = h.qualityProber.probeConfig()
	h.qualityProber.cfgMu.Lock()
	loaded := h.qualityProber.cfgLoaded
	h.qualityProber.cfgMu.Unlock()
	if !loaded {
		t.Fatal("setup: prober config cache should be loaded")
	}

	body := `{"types":["configs"],"version":"1.0","configs":{"notify_enabled":"false","monitor_retention_days":"30"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/backup/import", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.backupImport(rec, req)
	var env struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Code != 0 {
		t.Fatalf("backupImport: code=%d err=%v body=%s", env.Code, err, rec.Body.String())
	}

	if _, _, ok := h.notifyConfig(notifyEventNodeOffline); ok {
		t.Fatal("V4-N1: notify_enabled=false from backup import must take effect immediately (cache not invalidated)")
	}
	h.qualityProber.cfgMu.Lock()
	loaded = h.qualityProber.cfgLoaded
	h.qualityProber.cfgMu.Unlock()
	if loaded {
		t.Fatal("V4-N1/O1: prober config cache must be invalidated by backup import (retention key included)")
	}
}

// V4-O1 单点：retention 键变更须触发 prober 配置失效。
func TestFix2BProberRetentionKeyTriggersInvalidation(t *testing.T) {
	h, _ := setupFixdNotifyHandler(t, newFixdWebhook(t))
	_ = h.qualityProber.probeConfig()
	h.notifyTunnelQualityConfigChanged(monitoring.ConfigMonitorRetentionDays)
	h.qualityProber.cfgMu.Lock()
	loaded := h.qualityProber.cfgLoaded
	h.qualityProber.cfgMu.Unlock()
	if loaded {
		t.Fatal("V4-O1: monitor_retention_days change must invalidate prober config cache")
	}
}
