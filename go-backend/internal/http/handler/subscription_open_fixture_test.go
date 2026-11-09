package handler

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// subFixture builds one node with four enabled trojan inbounds and
// returns the handler, the repo and the inbound ids by name.
func subFixture(t *testing.T) (*Handler, *repo.Repository, map[string]int64) {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/subopen.db")
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
	ids := map[string]int64{}
	for i, name := range []string{"hk-1", "hk-2", "jp-1", "us-1"} {
		if err := r.DB().Exec(`INSERT INTO node_inbound(kernel_id, node_id, name, protocol, listen, port, settings, enabled, created_time, updated_time)
			VALUES(?, ?, ?, 'trojan', '::', ?, '{"password":"owner-pw"}', 1, ?, ?)`, kernelID, nodeID, name, 10443+i, now, now).Error; err != nil {
			t.Fatal(err)
		}
		ids[name] = mustLastInsertID(t, r, "inbound "+name)
	}
	return h, r, ids
}

// createShareSub persists a fully-formed share subscription in one
// create: traffic columns are only written on create (updates omit
// them by design), so fixtures set every field up front.
func createShareSub(t *testing.T, r *repo.Repository, s *model.Subscription) *model.Subscription {
	t.Helper()
	now := time.Now().UnixMilli()
	if s.CreatedTime == 0 {
		s.CreatedTime = now
	}
	s.UpdatedTime = now
	if err := r.SaveSubscription(s); err != nil {
		t.Fatal(err)
	}
	return s
}

// fetchSub runs openSubscription for a token and returns status + body.
func fetchSub(t *testing.T, h *Handler, token, ua string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/open_api/sub/"+token, nil)
	req.Header.Set("User-Agent", ua)
	rec := httptest.NewRecorder()
	h.openSubscription(rec, req)
	return rec.Code, rec.Body.String()
}

// TestSubscriptionSaveRejectsEmptySources: with node groups removed, a
// subscription save with no own sources is an ordinary empty-source
// save and hits the same rejection as any other sourceless save —
// group references no longer count as a source.
func TestSubscriptionSaveRejectsEmptySources(t *testing.T) {
	_, r, _ := subFixture(t)
	rows, err := r.ListNodeInbounds()
	if err != nil {
		t.Fatal(err)
	}
	req := subscriptionRequest{Name: "空源", Kind: model.SubscriptionShare}
	if _, _, err := req.validateDetailed(rows); err == nil ||
		!strings.Contains(err.Error(), "请至少选择一个入站或订阅源") {
		t.Fatalf("sourceless save must be rejected, got %v", err)
	}
}

// TestSourcelessSubscriptionRendersEmpty: a stored subscription row
// with no own sources (the shape a pre-removal group-only subscription
// is left in) renders zero endpoints and an empty push set, without
// error — the inert group_ids column value is simply never read.
func TestSourcelessSubscriptionRendersEmpty(t *testing.T) {
	h, r, _ := subFixture(t)
	s := createShareSub(t, r, &model.Subscription{
		Name: "纯组遗留", Kind: model.SubscriptionShare, Token: strings.Repeat("9", 32), UUID: "uuid-legacy", Password: "pw-legacy",
		Enabled: 1,
	})
	rows, err := r.ListNodeInbounds()
	if err != nil {
		t.Fatal(err)
	}
	if got := h.subRenderEndpoints(s, rows); len(got) != 0 {
		t.Fatalf("sourceless subscription must render zero endpoints, got %d", len(got))
	}
	if got := h.subAllInboundIDs(s, rows, nil); len(got) != 0 {
		t.Fatalf("sourceless subscription must have an empty inbound set, got %v", got)
	}
}

// TestOpenSubscriptionInactiveReasonNodes: a disabled, expired or
// over-quota subscription answers 200 with a single reason placeholder
// instead of the old 403, so clients replace their stale node list.
func TestOpenSubscriptionInactiveReasonNodes(t *testing.T) {
	h, r, ids := subFixture(t)
	_ = ids
	now := time.Now().UnixMilli()

	disabled := createShareSub(t, r, &model.Subscription{
		Name: "停用", Kind: model.SubscriptionShare, Token: strings.Repeat("1", 32), UUID: "uuid-off", Password: "pw-off",
		SourceAll: 1, Enabled: 0,
	})
	expired := createShareSub(t, r, &model.Subscription{
		Name: "过期", Kind: model.SubscriptionShare, Token: strings.Repeat("2", 32), UUID: "uuid-exp", Password: "pw-exp",
		SourceAll: 1, Enabled: 1, ExpireTime: now - 1000,
	})
	over := createShareSub(t, r, &model.Subscription{
		Name: "超量", Kind: model.SubscriptionShare, Token: strings.Repeat("3", 32), UUID: "uuid-over", Password: "pw-over",
		SourceAll: 1, Enabled: 1, TrafficLimit: 100, UploadBytes: 200,
	})

	cases := []struct {
		token  string
		reason string
	}{
		{disabled.Token, "⚠️ 订阅已停用，请联系管理员"},
		{expired.Token, "⚠️ 订阅已过期，请联系管理员"},
		{over.Token, "⚠️ 流量已用完，请联系管理员"},
	}
	for _, c := range cases {
		code, body := fetchSub(t, h, c.token, "clash")
		if code != http.StatusOK {
			t.Fatalf("%s: status %d (body %s)", c.reason, code, body)
		}
		if !strings.Contains(body, c.reason) {
			t.Fatalf("reason node missing:\n%s", body)
		}
		if strings.Contains(body, "hk-1") {
			t.Fatalf("stopped subscription must not serve real nodes:\n%s", body)
		}
		// Base64 (link-list) clients get the reason as a placeholder
		// ss link too.
		code, body = fetchSub(t, h, c.token, "v2rayN")
		if code != http.StatusOK {
			t.Fatalf("%s base64: status %d", c.reason, code)
		}
		raw, err := base64.StdEncoding.DecodeString(body)
		if err != nil {
			t.Fatalf("base64 body: %v", err)
		}
		if !strings.Contains(string(raw), "127.0.0.1:1") {
			t.Fatalf("base64 reason placeholder missing: %s", raw)
		}
	}
}
