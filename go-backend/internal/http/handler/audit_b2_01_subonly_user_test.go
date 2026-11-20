package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R18-B2-01: a user assigned ONLY through the subscription model (no
// legacy inbound assignments) fetches the default personal link
// (?user=&sig=) and must get the subscription's full node set — the
// same nodes the token-only assigned link serves. Before the fix the
// ?user= branch filtered rows by the (empty) inbound-assignment set
// and rendered an empty subscription.
func TestR18B201SubOnlyUserPersonalLinkReturnsFullNodes(t *testing.T) {
	h, r, _ := subFixture(t)
	sub := createShareSub(t, r, &model.Subscription{
		Name: "b2-01", Kind: model.SubscriptionShare, Token: strings.Repeat("b", 32),
		UUID: "uuid-b201", Password: "pw-b201", Enabled: 1, SourceAll: 1,
	})
	now := time.Now().UnixMilli()
	if err := r.DB().Exec(`INSERT INTO user(id, user, pwd, role_id, exp_time, flow, in_flow, out_flow, flow_reset_time, num, created_time, updated_time, status) VALUES(7, 'b2-01-user', 'pwd', 1, 2727251700000, 99999, 0, 0, 1, 99999, ?, ?, 1)`, now, now).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	// Only a subscription assignment — deliberately no inbound assignments.
	if err := r.SetUserSubscriptions(7, []int64{sub.ID}); err != nil {
		t.Fatalf("seed subscription assignment: %v", err)
	}
	sig := h.subURLSigForToken(sub.Token, 7)

	// Control: the token-only link serves the nodes.
	if code, body := fetchSub(t, h, sub.Token, "clash-verge/1.0"); code != 200 || !strings.Contains(body, "hk-1") {
		t.Fatalf("control token-only fetch: code=%d body=%s", code, body)
	}
	// The personal link must serve the same nodes, not an empty subscription.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/open_api/sub/"+sub.Token+"?user=7&sig="+sig, nil)
	req.Header.Set("User-Agent", "clash-verge/1.0")
	rec := httptest.NewRecorder()
	h.openSubscription(rec, req)
	if rec.Code != 200 {
		t.Fatalf("personal link fetch: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "hk-1") {
		t.Fatalf("personal link must render the subscription nodes, got body=%s", rec.Body.String())
	}
}
