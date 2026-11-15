package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// B-L4: a non-share subscription has no per-subscription attribution,
// so openSubscription reports its selected inbounds' row counters
// (inbound totals, shared by every subscription drawing from the same
// inbound). The header must keep that inbound-total usage but must
// NOT pair it with the subscription's TrafficLimit as total: the server
// never enforces a non-share limit, so used >= total would let a client
// show the subscription as exhausted from a number that is not this
// subscription's usage. total is 0 (unlimited) for non-share rows.

// fetchSubHeader runs openSubscription for a token and returns the
// Subscription-Userinfo header and the body.
func fetchSubHeader(t *testing.T, h *Handler, token, ua string) (string, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/open_api/sub/"+token, nil)
	req.Header.Set("User-Agent", ua)
	rec := httptest.NewRecorder()
	h.openSubscription(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("openSubscription %s: status %d (body %s)", token, rec.Code, rec.Body.String())
	}
	return rec.Header().Get("Subscription-Userinfo"), rec.Body.String()
}

func TestOpenSubscriptionNonShareReportsInboundTotal(t *testing.T) {
	h, r, ids := subFixture(t)
	if err := r.DB().Exec(`UPDATE node_inbound SET upload_bytes = 1000, download_bytes = 2000 WHERE id = ?`, ids["hk-1"]).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Exec(`UPDATE node_inbound SET upload_bytes = 300, download_bytes = 400 WHERE id = ?`, ids["jp-1"]).Error; err != nil {
		t.Fatal(err)
	}

	// Two non-share subscriptions drawing from the same inbounds. A has
	// a TrafficLimit deliberately below the inbound total: under the old
	// header (total=TrafficLimit) a client would show it as exhausted.
	ownA := createShareSub(t, r, &model.Subscription{
		Name: "非分享甲", Kind: model.SubscriptionOwn, Token: strings.Repeat("a", 32),
		SourceAll: 1, Enabled: 1, TrafficLimit: 100,
	})
	// B carries nonzero counters on its own row: the header must ignore
	// them and still report the inbound sums.
	ownB := createShareSub(t, r, &model.Subscription{
		Name: "非分享乙", Kind: model.SubscriptionOwn, Token: strings.Repeat("b", 32),
		SourceAll: 1, Enabled: 1, UploadBytes: 5000, DownloadBytes: 7000,
	})
	// Share control: its own metered counters and its own limit.
	shareC := createShareSub(t, r, &model.Subscription{
		Name: "分享丙", Kind: model.SubscriptionShare, Token: strings.Repeat("c", 32),
		UUID: "uuid-c", Password: "pw-c",
		SourceAll: 1, Enabled: 1, TrafficLimit: 100, UploadBytes: 10, DownloadBytes: 20,
	})

	headerA, bodyA := fetchSubHeader(t, h, ownA.Token, "clash")
	headerB, _ := fetchSubHeader(t, h, ownB.Token, "clash")
	headerC, _ := fetchSubHeader(t, h, shareC.Token, "clash")

	// Both non-share subscriptions show the same inbound totals
	// (1300 up / 2400 down) with total=0 (unlimited): the shared
	// inbound's traffic is not either subscription's own usage, and the
	// figure must never read as "over quota" against a limit.
	const wantNonShare = "upload=1300; download=2400; total=0; expire=0"
	if headerA != wantNonShare {
		t.Fatalf("non-share A header = %q, want %q", headerA, wantNonShare)
	}
	if headerB != wantNonShare {
		t.Fatalf("non-share B header = %q, want %q (must equal A: shared inbound total)", headerB, wantNonShare)
	}
	if !strings.Contains(bodyA, "hk-1") || strings.Contains(bodyA, "流量已用完") {
		t.Fatalf("non-share A must serve real nodes, body:\n%s", bodyA)
	}
	const wantShare = "upload=10; download=20; total=100; expire=0"
	if headerC != wantShare {
		t.Fatalf("share header = %q, want %q", headerC, wantShare)
	}
}

func TestSubscriptionViewUsageInboundTotalFlag(t *testing.T) {
	h, r, _ := subFixture(t)
	rows, err := r.ListNodeInbounds()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	own := createShareSub(t, r, &model.Subscription{
		Name: "非分享", Kind: model.SubscriptionOwn, Token: strings.Repeat("d", 32),
		SourceAll: 1, Enabled: 1,
	})
	share := createShareSub(t, r, &model.Subscription{
		Name: "分享", Kind: model.SubscriptionShare, Token: strings.Repeat("e", 32),
		UUID: "uuid-e", Password: "pw-e",
		SourceAll: 1, Enabled: 1,
	})
	if v := h.subscriptionView(own, rows, now, ""); !v.UsageInboundTotal {
		t.Fatalf("non-share view must set usageInboundTotal, got %+v", v)
	}
	if v := h.subscriptionView(share, rows, now, ""); v.UsageInboundTotal {
		t.Fatalf("share view must not set usageInboundTotal, got %+v", v)
	}
}
