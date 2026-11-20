package handler

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R18-B4-03: force-deleting a managed relay whose nft reconcile fails
// must NOT wipe the pending-delete ledger along with the DB rows: the
// ledger is the only retry carrier for the DNAT rule, and without it
// the rule lingers forever. The force path must preserve the ledger
// row, best-effort clean the data plane, and name the residue in the
// receipt instead of returning a bare success.
func TestR18B403RelayForceDeleteKeepsNftLedger(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	forward := seedNftForwardForDelete(t, h, fixture)
	h.nftablesManager = newSerializedNftManager(&fakeNftablesManager{reconcileErr: errFakeSSHDown})

	now := time.Now().UnixMilli()
	inbound := &model.NodeInbound{
		Name: "b4-03-inbound", NodeID: fixture.nodeID, Protocol: "ss",
		Listen: "0.0.0.0", Port: 8388, Enabled: 1, CreatedTime: now, UpdatedTime: now,
	}
	if err := h.repo.DB().Create(inbound).Error; err != nil {
		t.Fatalf("seed inbound: %v", err)
	}
	rel := &model.NodeInboundRelay{
		InboundID: inbound.ID, ForwardID: forward.ID, Name: "b4-03-relay",
		Managed: 1, CreatedTime: now,
	}
	if err := h.repo.CreateInboundRelay(rel); err != nil {
		t.Fatalf("seed relay: %v", err)
	}

	res := httptest.NewRecorder()
	h.inboundRelayDelete(res, newAuthenticatedJSONRequest(t, map[string]interface{}{
		"id": rel.ID, "force": true,
	}))
	var env struct {
		Code    int    `json:"code"`
		Msg     string `json:"msg"`
		Warning string `json:"warning"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode response: %v body=%s", err, res.Body.String())
	}
	if env.Code != 0 {
		t.Fatalf("force delete must succeed, got code=%d msg=%q body=%s", env.Code, env.Msg, res.Body.String())
	}
	if fr, err := h.repo.GetForwardRecord(forward.ID); err != nil {
		t.Fatalf("get forward: %v", err)
	} else if fr != nil {
		t.Fatalf("forward row must be deleted by force delete, got %+v", fr)
	}
	if got, err := h.repo.GetInboundRelay(rel.ID); err != nil {
		t.Fatalf("get relay: %v", err)
	} else if got != nil {
		t.Fatalf("relay row must be deleted by force delete, got %+v", got)
	}
	// The discriminating assertion: the ledger row survives the force
	// delete so the 60s retry loop can still converge the DNAT rule.
	pending, err := h.repo.GetNftPendingDelete(forward.ID)
	if err != nil || pending == nil {
		t.Fatalf("nft pending-delete ledger must survive force delete, got %+v err=%v", pending, err)
	}
	if !strings.Contains(env.Warning, "清理未确认") {
		t.Fatalf("force-delete receipt must name the unconfirmed data-plane cleanup, got msg=%q warning=%q", env.Msg, env.Warning)
	}
	// The retry loop must still see the orphaned ledger row: with the
	// node still down it records the attempt instead of dropping it.
	h.retryNftPendingDeletes(time.Now().Add(2 * time.Minute))
	pending, err = h.repo.GetNftPendingDelete(forward.ID)
	if err != nil || pending == nil {
		t.Fatalf("retry loop must keep the orphaned ledger row while reconcile fails, got %+v err=%v", pending, err)
	}
	if pending.RetryCount != 1 {
		t.Fatalf("ledger retry_count = %d, want 1 after one failed retry tick", pending.RetryCount)
	}
}
