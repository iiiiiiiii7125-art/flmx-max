package handler

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"go-backend/internal/kernel"
	"go-backend/internal/store/repo"
)

// R-068 / L6-01 (AUDIT-HIST L6): inboundPortConflict skipped damaged
// inbound rows (`continue` on inboundFromModel failure), and the hop
// range lives only in Settings JSON — so a damaged row's hop range
// never took part in the only hop-range-vs-hop-range comparison. A
// new inbound overlapping it was admitted; the agent's hop redirect
// then collided. The fix recovers the hop range from Settings when it
// still parses and reports the conflict; when even Settings is
// unrecoverable it fails closed naming the row.

func histCSeedDamagedHopRow(t *testing.T, r *repo.Repository, nodeID int64, settings, transport string) {
	t.Helper()
	box := seedChainKernel(t, r, nodeID, kernel.TypeSingbox)
	row := seedChainInbound(t, r, box, "hist-l6-broken", kernel.Inbound{Protocol: kernel.ProtocolShadowsocks, Port: 20000}, 0)
	if err := r.DB().Exec(`UPDATE node_inbound SET settings = ?, transport = ? WHERE id = ?`, settings, transport, row.ID).Error; err != nil {
		t.Fatalf("damage inbound row: %v", err)
	}
}

func TestHistCR068DamagedRowHopRangeStillConflicts(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/hist-c-r068.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	nodeID := seedRelayTargetNode(t, r, "hist-c-r068-node", "198.51.100.20")
	// Audit repro shape: Settings valid with hop range 21000-22000,
	// Transport corrupt.
	histCSeedDamagedHopRow(t, r, nodeID, `{"portHopping":"21000-22000"}`, `{corrupt`)

	// Direct: overlapping hop range 21500-21600 must report a conflict.
	msg, err := h.inboundPortConflict(nodeID, kernel.Inbound{Protocol: kernel.ProtocolShadowsocks, Port: 24000, Settings: kernel.Settings{PortHopping: "21500-21600"}}, 0)
	if err != nil {
		t.Fatalf("overlapping hop range must report a conflict, not an error: %v", err)
	}
	if !strings.Contains(msg, "冲突") || !strings.Contains(msg, "hist-l6-broken") || !strings.Contains(msg, "21000-22000") {
		t.Fatalf("conflict message must name the damaged row and its range, got %q", msg)
	}

	// Control: a non-overlapping hop range must not be blamed on the row.
	msg, err = h.inboundPortConflict(nodeID, kernel.Inbound{Protocol: kernel.ProtocolShadowsocks, Port: 24001, Settings: kernel.Settings{PortHopping: "23000-24000"}}, 0)
	if err != nil {
		t.Fatalf("non-overlapping check must not error: %v", err)
	}
	if msg != "" {
		t.Fatalf("non-overlapping hop range must pass, got %q", msg)
	}

	// API: creating the overlapping inbound must be rejected with the
	// conflict named in the response. Port hopping is a Hysteria2
	// feature — Normalize strips it for other protocols — so the new
	// inbound is Hysteria2 (the damaged row's range is recovered from
	// its Settings JSON regardless of protocol).
	seed := seedChainInbound(t, r, seedChainKernel(t, r, nodeID, kernel.TypeSingbox), "hist-c-r068-template", kernel.Inbound{Protocol: kernel.ProtocolHysteria2, Port: 24002}, 0)
	tmpl, err := inboundFromModel(*seed)
	if err != nil {
		t.Fatal(err)
	}
	tmpl.Settings.PortHopping = "21500-21600"
	out := callInboundSave(t, h, false, mustMarshalInboundRequest(t, inboundRequest{
		NodeID: nodeID, KernelType: kernel.TypeSingbox, Name: "hist-c-r068-new",
		Protocol: kernel.ProtocolHysteria2, Settings: tmpl.Settings, Security: tmpl.Security,
	}))
	if out.Code == 0 {
		t.Fatalf("inboundCreate overlapping a damaged row's hop range must fail, got %+v", out)
	}
	if !strings.Contains(out.Msg, "冲突") {
		t.Fatalf("rejection must name the conflict, got %+v", out)
	}
}

func TestHistCR068UnrecoverableDamagedRowFailsClosed(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/hist-c-r068-closed.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	nodeID := seedRelayTargetNode(t, r, "hist-c-r068-node", "198.51.100.20")
	// Settings itself corrupt: hop range unrecoverable.
	histCSeedDamagedHopRow(t, r, nodeID, `{corrupt`, `{corrupt`)

	_, err = h.inboundPortConflict(nodeID, kernel.Inbound{Protocol: kernel.ProtocolShadowsocks, Port: 24000, Settings: kernel.Settings{PortHopping: "21500-21600"}}, 0)
	var blocked *inboundConflictBlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("unrecoverable damaged row must fail closed with inboundConflictBlockedError, got %v", err)
	}
	if !strings.Contains(blocked.msg, "hist-l6-broken") {
		t.Fatalf("fail-closed message must name the damaged row, got %q", blocked.msg)
	}
}

func mustMarshalInboundRequest(t *testing.T, req inboundRequest) string {
	t.Helper()
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
