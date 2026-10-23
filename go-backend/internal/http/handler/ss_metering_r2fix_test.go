package handler

import (
	"testing"
	"time"
)

// ---- SS metering round-2 audit mop-up (G3 = Y-01) ----

// Y-01: one subscription view build consulted the companion map
// twice (shared-credential determination in the view, endpoint
// retargeting in subEndpoints), and every consultation re-evaluated
// the F4 applied gate — a full kernel payload rebuild + hash per
// kernel. On a kernel with ~200 inbounds that priced a single view
// at ~40ms and a 50-subscription list at ~2s of pure rebuilds. The
// gate verdict must be evaluated at most once per kernel within a
// view build / request and reused; it must never be cached across
// requests (the agent's ack state can change at any time).
func TestSSMeteringGateEvaluatedOncePerViewBuild(t *testing.T) {
	r, h, k, origin, sub := seedMeteringFixture(t)
	h.reconcileSSMetering()
	if comp, err := r.GetCompanionInbound(origin.ID); err != nil || comp == nil {
		t.Fatal("companion must be materialized")
	}
	markKernelApplied(t, r, h, k)
	rows, err := r.ListNodeInbounds()
	if err != nil {
		t.Fatal(err)
	}

	before := meteringGatePayloadBuilds.Load()
	view := h.subscriptionView(sub, rows, time.Now(), "")
	if builds := meteringGatePayloadBuilds.Load() - before; builds > 1 {
		t.Fatalf("one view build must evaluate the applied gate at most once per kernel, got %d payload rebuilds", builds)
	}
	// The gate still has to be open (the reuse must not turn into a
	// stuck-closed cache): the metered origin is not reported shared.
	for _, id := range view.SharedIDs {
		if id == origin.ID {
			t.Fatal("metered origin must not be reported as a shared-credential inbound")
		}
	}
}
