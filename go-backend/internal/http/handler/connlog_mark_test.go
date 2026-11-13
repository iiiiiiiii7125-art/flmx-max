package handler

import (
	"fmt"
	"testing"

	"go-backend/internal/auth"
	"go-backend/internal/store/model"
)

// TestConnlogMarkMismatchVerdict pins the desired-vs-actual truth
// table: only a positive "0" from a reporting agent proves the push
// missed; unreported is unknown, never a mismatch; a reported 0 on
// a node with no forwards is a true vacuum, never a mismatch.
func TestConnlogMarkMismatchVerdict(t *testing.T) {
	cases := []struct {
		enabled     bool
		marked      int64
		reported    bool
		hasForwards bool
		mismatch    bool
		warning     string
	}{
		{true, 0, true, true, true, connlogMarkNotAppliedWarning},
		{true, 0, true, false, false, ""},
		{true, 2, true, true, false, ""},
		{true, 2, true, false, false, ""},
		{true, 0, false, true, false, connlogMarkUnknownWarning},
		{true, 0, false, false, false, connlogMarkUnknownWarning},
		{true, 5, false, true, false, connlogMarkUnknownWarning},
		{false, 0, true, true, false, ""},
		{false, 0, true, false, false, ""},
		{false, 0, false, true, false, ""},
		{false, 3, true, true, false, ""},
	}
	for _, c := range cases {
		if got := connlogMarkMismatch(c.enabled, c.marked, c.reported, c.hasForwards); got != c.mismatch {
			t.Fatalf("mismatch(enabled=%v marked=%d reported=%v hasForwards=%v) = %v, want %v",
				c.enabled, c.marked, c.reported, c.hasForwards, got, c.mismatch)
		}
		if got := connlogMarkWarning(c.enabled, c.marked, c.reported, c.hasForwards); got != c.warning {
			t.Fatalf("warning(enabled=%v marked=%d reported=%v hasForwards=%v) = %q, want %q",
				c.enabled, c.marked, c.reported, c.hasForwards, got, c.warning)
		}
	}
	if connlogMarkNotAppliedWarning != "配置未下发到 agent" {
		t.Fatalf("mismatch warning = %q, want the exact operator-facing string", connlogMarkNotAppliedWarning)
	}
}

// TestConnlogMarkStoreAndAttach pins the in-memory store and the
// node-list enrichment: reported counts surface (0 included),
// unreported nodes surface null, mismatch carries the warning.
// This rig builds a bare Handler (nil repo), so the forward gate
// stays open by design — the mismatch row below pins the degrade
// path (lookup unavailable ⇒ pre-gate behaviour).
func TestConnlogMarkStoreAndAttach(t *testing.T) {
	h := &Handler{connlogMarkByNode: make(map[int64]int64)}
	h.setConnlogMark(7, 0)
	h.setConnlogMark(8, 3)
	if got, ok := h.connlogMark(7); !ok || got != 0 {
		t.Fatalf("connlogMark(7) = (%d, %v), want (0, true)", got, ok)
	}
	if _, ok := h.connlogMark(9); ok {
		t.Fatal("connlogMark(9) reported = true, want false (never reported)")
	}
	h.dropConnlogMark(8)
	if _, ok := h.connlogMark(8); ok {
		t.Fatal("connlogMark(8) still reported after drop (offline must forget)")
	}

	items := []map[string]interface{}{
		{"id": int64(7), "connLogEnabled": 1},
		{"id": int64(8), "connLogEnabled": 1},
		{"id": int64(9), "connLogEnabled": 1},
		{"id": int64(10), "connLogEnabled": 0},
	}
	h.setConnlogMark(8, 3)
	h.attachConnlogMarkStates(items)

	if items[0]["connlogMarkedForwards"] != int64(0) || items[0]["connlogMarkMismatch"] != true || items[0]["connlogMarkWarning"] != connlogMarkNotAppliedWarning {
		t.Fatalf("mismatch row = %v, want marked=0 mismatch=true warning=%q", items[0], connlogMarkNotAppliedWarning)
	}
	if items[1]["connlogMarkedForwards"] != int64(3) || items[1]["connlogMarkMismatch"] != false {
		t.Fatalf("ok row = %v, want marked=3 mismatch=false", items[1])
	}
	if _, ok := items[1]["connlogMarkWarning"]; ok {
		t.Fatalf("ok row must not carry a warning: %v", items[1])
	}
	if items[2]["connlogMarkedForwards"] != nil || items[2]["connlogMarkMismatch"] != false {
		t.Fatalf("unknown row = %v, want marked=nil mismatch=false", items[2])
	}
	if items[3]["connlogMarkedForwards"] != nil || items[3]["connlogMarkMismatch"] != false {
		t.Fatalf("switch-off row = %v, want marked=nil mismatch=false", items[3])
	}
}

// TestConnlogMarkAttachWithRealRepo pins the node-list enrichment
// against a real repo: the forward gate applies — a forward-less
// node reporting 0 is not a mismatch, while a node with a forward
// port reporting 0 is.
func TestConnlogMarkAttachWithRealRepo(t *testing.T) {
	rig := newConnlogTestRig(t)
	if err := rig.r.UpdateNodeConnLogEnabled(rig.entryID, 1); err != nil {
		t.Fatal(err)
	}
	if err := rig.r.UpdateNodeConnLogEnabled(rig.landingID, 1); err != nil {
		t.Fatal(err)
	}
	if err := rig.r.DB().Create(&model.ForwardPort{ForwardID: 77, NodeID: rig.entryID, Port: 24300}).Error; err != nil {
		t.Fatal(err)
	}
	rig.h.setConnlogMark(rig.entryID, 0)
	rig.h.setConnlogMark(rig.landingID, 0)
	items := []map[string]interface{}{
		{"id": rig.entryID, "connLogEnabled": 1},
		{"id": rig.landingID, "connLogEnabled": 1},
	}
	rig.h.attachConnlogMarkStates(items)
	if items[0]["connlogMarkMismatch"] != true || items[0]["connlogMarkWarning"] != connlogMarkNotAppliedWarning {
		t.Fatalf("entry row = %v, want mismatch=true warning=%q", items[0], connlogMarkNotAppliedWarning)
	}
	if items[1]["connlogMarkMismatch"] != false {
		t.Fatalf("landing row = %v, want mismatch=false (no forwards)", items[1])
	}
	if _, ok := items[1]["connlogMarkWarning"]; ok {
		t.Fatalf("landing row must not carry a warning: %v", items[1])
	}
}

// TestConnlogMarkMismatchInNodeQuery pins the connlog view's mark
// fields end to end through the real handler: unknown → mismatch
// → ok → forgotten, driven by the in-memory report store.
func TestConnlogMarkMismatchInNodeQuery(t *testing.T) {
	rig := newConnlogTestRig(t)
	admin := &auth.Claims{Sub: "1", RoleID: 0}
	q := fmt.Sprintf("nodeId=%d&offset=0&limit=10", rig.entryID)

	// Switch on, agent never reported: unknown, never a mismatch.
	if err := rig.r.UpdateNodeConnLogEnabled(rig.entryID, 1); err != nil {
		t.Fatal(err)
	}
	code, page := rig.queryNode(t, admin, q)
	if code != 0 {
		t.Fatalf("query code = %d, want 0", code)
	}
	if page.MarkedForwards != nil || page.MarkMismatch || page.MarkWarning != connlogMarkUnknownWarning {
		t.Fatalf("unreported page = (marked=%v mismatch=%v warning=%q), want (nil false %q)",
			page.MarkedForwards, page.MarkMismatch, page.MarkWarning, connlogMarkUnknownWarning)
	}

	// Forward-less node (landing): switch on, agent reports 0 —
	// nothing to mark, so no mismatch and no warning (not unknown:
	// the agent did report, and its answer is vacuously true).
	if err := rig.r.UpdateNodeConnLogEnabled(rig.landingID, 1); err != nil {
		t.Fatal(err)
	}
	rig.h.setConnlogMark(rig.landingID, 0)
	_, page = rig.queryNode(t, admin, fmt.Sprintf("nodeId=%d&offset=0&limit=10", rig.landingID))
	if page.MarkedForwards == nil || *page.MarkedForwards != 0 || page.MarkMismatch || page.MarkWarning != "" {
		t.Fatalf("forward-less page = (marked=%v mismatch=%v warning=%q), want (0 false \"\")",
			page.MarkedForwards, page.MarkMismatch, page.MarkWarning)
	}

	// Agent reports 0 marked forwards: the push missed. The entry
	// node needs a forward port row first: the verdict gate
	// requires ≥1 forward that should carry the mark.
	if err := rig.r.DB().Create(&model.ForwardPort{ForwardID: 77, NodeID: rig.entryID, Port: 24300}).Error; err != nil {
		t.Fatal(err)
	}
	rig.h.setConnlogMark(rig.entryID, 0)
	_, page = rig.queryNode(t, admin, q)
	if page.MarkedForwards == nil || *page.MarkedForwards != 0 || !page.MarkMismatch || page.MarkWarning != connlogMarkNotAppliedWarning {
		t.Fatalf("mismatch page = (marked=%v mismatch=%v warning=%q), want (0 true %q)",
			page.MarkedForwards, page.MarkMismatch, page.MarkWarning, connlogMarkNotAppliedWarning)
	}

	// Agent reports 2 marked forwards: desired == actual.
	rig.h.setConnlogMark(rig.entryID, 2)
	_, page = rig.queryNode(t, admin, q)
	if page.MarkedForwards == nil || *page.MarkedForwards != 2 || page.MarkMismatch || page.MarkWarning != "" {
		t.Fatalf("ok page = (marked=%v mismatch=%v warning=%q), want (2 false \"\")",
			page.MarkedForwards, page.MarkMismatch, page.MarkWarning)
	}

	// Offline forgets the report: back to unknown, not to "0".
	rig.h.dropConnlogMark(rig.entryID)
	_, page = rig.queryNode(t, admin, q)
	if page.MarkedForwards != nil || page.MarkMismatch || page.MarkWarning != connlogMarkUnknownWarning {
		t.Fatalf("forgotten page = (marked=%v mismatch=%v warning=%q), want (nil false %q)",
			page.MarkedForwards, page.MarkMismatch, page.MarkWarning, connlogMarkUnknownWarning)
	}

	// Switch off: even a reported 0 is not a mismatch.
	rig.h.setConnlogMark(rig.entryID, 0)
	if err := rig.r.UpdateNodeConnLogEnabled(rig.entryID, 0); err != nil {
		t.Fatal(err)
	}
	_, page = rig.queryNode(t, admin, q)
	if page.MarkMismatch || page.MarkWarning != "" {
		t.Fatalf("switch-off page = (mismatch=%v warning=%q), want (false \"\")", page.MarkMismatch, page.MarkWarning)
	}
}
