package handler

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R-031/R-032 (AUDIT-HIST L4): inboundEndpoints silently skipped relay
// endpoints when the relay's forward was not ready (R-031) or when the
// tunnel had no entries (R-032) — the same loop's other skip branches
// (relayViews/inboundFromModel/GetNodeByID/share host) all log, these
// two did not, so a subscription missing relay endpoints left no trace.
// The fix logs both skips; endpoint production is unchanged.

func TestHistCR031R032RelaySkipsAreLogged(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/hist-c-r031.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	nodeID := seedRelayTargetNode(t, r, "hist-c-relay-node", "198.51.100.20")
	box := seedChainKernel(t, r, nodeID, kernel.TypeSingbox)
	row := seedChainInbound(t, r, box, "hist-c-entry", kernel.Inbound{Protocol: kernel.ProtocolShadowsocks, Port: 8388}, 0)

	now := time.Now().UnixMilli()
	if _, err := h.repo.CreateUser("admin", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now); err != nil {
		t.Fatalf("create user: %v", err)
	}
	tunnelID := seedTunnelForNftables(t, h, "hist-c-relay-tunnel", nodeID)
	fwd := seedForwardForNftables(t, h, tunnelID, nodeID, "203.0.113.9:8080", 20000)
	// R-032 fixture: the forward exists and is enabled, but has no
	// entries (its forward_port rows removed).
	if err := r.DB().Exec(`DELETE FROM forward_port WHERE forward_id = ?`, fwd.ID).Error; err != nil {
		t.Fatalf("strip forward ports: %v", err)
	}

	// R-031 fixture: relay pointing at a forward that does not exist.
	if err := r.CreateInboundRelay(&model.NodeInboundRelay{InboundID: row.ID, ForwardID: 999999, Name: "hist-c-gone", Managed: 1, CreatedTime: now}); err != nil {
		t.Fatalf("create gone relay: %v", err)
	}
	// R-032 fixture: relay pointing at the entry-less forward.
	if err := r.CreateInboundRelay(&model.NodeInboundRelay{InboundID: row.ID, ForwardID: fwd.ID, Name: "hist-c-empty", Managed: 1, CreatedTime: now}); err != nil {
		t.Fatalf("create empty relay: %v", err)
	}

	views, err := h.relayViews(row.ID)
	if err != nil {
		t.Fatalf("relayViews: %v", err)
	}
	if len(views) != 2 {
		t.Fatalf("want 2 relay views, got %d", len(views))
	}
	if views[0].ForwardExists || views[0].ForwardStatus == 1 {
		t.Fatalf("gone relay must be not-ready: %+v", views[0])
	}
	if !views[1].ForwardExists || views[1].ForwardStatus != 1 || len(views[1].Entries) != 0 {
		t.Fatalf("empty relay must be ready with zero entries: %+v", views[1])
	}

	var logBuf bytes.Buffer
	oldLog := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(oldLog)

	endpoints, kinds := h.inboundEndpoints([]model.NodeInbound{*row})
	logs := logBuf.String()
	if !strings.Contains(logs, "not ready") || !strings.Contains(logs, "hist-c-gone") {
		t.Fatalf("R-031 skip must be logged, got logs:\n%s", logs)
	}
	if !strings.Contains(logs, "has no entries") || !strings.Contains(logs, "hist-c-empty") {
		t.Fatalf("R-032 skip must be logged, got logs:\n%s", logs)
	}
	// Behaviour unchanged: the direct endpoint is still produced, no
	// relay endpoint is invented for either skipped relay.
	direct := 0
	for _, k := range kinds {
		if k == "direct" {
			direct++
		}
	}
	if direct != 1 || len(endpoints) != 1 {
		t.Fatalf("skipped relays must not change endpoints: kinds=%v endpoints=%d", kinds, len(endpoints))
	}
}
