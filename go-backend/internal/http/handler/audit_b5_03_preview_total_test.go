package handler

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"go-backend/internal/kernel"
	"go-backend/internal/store/repo"
)

// R18-B5-03: preview total/dropped always reported dropped=0 because
// subRenderEndpoints had already applied the relay-scope filter and
// ApplySubOptions re-applied it, so len(source)-len(items) was always
// 0. total must count the pre-pipeline endpoint set.
func TestR18B503PreviewTotalCountsPrePipelineEndpoints(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/sub_preview_b503.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	nodeID := seedRelayTargetNode(t, r, "b5-03-node", "203.0.113.10")
	box := seedChainKernel(t, r, nodeID, kernel.TypeSingbox)
	seedChainInbound(t, r, box, "b5-03-a", kernel.Inbound{Protocol: kernel.ProtocolTrojan, Port: 8443,
		Settings: kernel.Settings{Password: "trojan-secret"},
		Security: kernel.Security{Type: kernel.SecurityTLS, SNI: "a.example.com"}}, 0)
	seedChainInbound(t, r, box, "b5-03-b", kernel.Inbound{Protocol: kernel.ProtocolTrojan, Port: 8444,
		Settings: kernel.Settings{Password: "trojan-secret"},
		Security: kernel.Security{Type: kernel.SecurityTLS, SNI: "b.example.com"}}, 0)

	preview := func(body string) (int, int, int) {
		t.Helper()
		req := httptest.NewRequest("POST", "/api/v1/subscription/preview", strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.subscriptionPreview(rec, req)
		var env struct {
			Code int `json:"code"`
			Msg  string `json:"msg"`
			Data struct {
				Items   []json.RawMessage `json:"items"`
				Total   int               `json:"total"`
				Dropped int               `json:"dropped"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode preview (%s): %v (body=%s)", body, err, rec.Body.String())
		}
		if env.Code != 0 {
			t.Fatalf("preview (%s) must succeed, got code=%d msg=%q body=%s", body, env.Code, env.Msg, rec.Body.String())
		}
		return len(env.Data.Items), env.Data.Total, env.Data.Dropped
	}

	// No filter: both direct nodes render, nothing dropped.
	if items, total, dropped := preview(`{"name":"preview","sourceAll":true}`); items != 2 || total != 2 || dropped != 0 {
		t.Fatalf("unfiltered preview: items=%d total=%d dropped=%d, want 2/2/0", items, total, dropped)
	}
	// lines=relay keeps only relayed endpoints: both direct nodes drop.
	if items, total, dropped := preview(`{"name":"preview","sourceAll":true,"options":{"lines":"relay"}}`); items != 0 || total != 2 || dropped != 2 {
		t.Fatalf("relay-only preview: items=%d total=%d dropped=%d, want 0/2/2", items, total, dropped)
	}
}
