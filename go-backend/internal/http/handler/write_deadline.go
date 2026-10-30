package handler

import (
	"net/http"
	"time"
)

// R8 T-family (timeout-budget three-layer misalignment): the panel
// server's global WriteTimeout is 2 minutes (internal/app/app.go), but
// a set of endpoints legitimately work longer than that, so their
// responses were truncated even though the work itself completed.
// Following the gh_mirror.go precedent, those handlers extend their
// own response write deadline as their first step.
//
// The values below are the server-layer row of the timeout-budget
// registry (x-verdicts-r8 §7, batches T1 and T-02(a)): each exceeds its
// endpoint's backend bound plus margin and pairs with the frontend
// value landed by batch T2. The relief only extends write patience —
// it never caps the work itself and never lowers any existing
// handler/agent budget.
const (
	// kernelInstall and inbound create/update: the backend bound is
	// kernelApplyTimeout (300s; an inbound save inherits it
	// conditionally when the pinned kernel has no installed version).
	// Frontend >= 330s (batch T2).
	kernelApplyWriteDeadline = 360 * time.Second
	// nodeUpgrade: with an empty version the handler first resolves
	// the latest release through the two-stage discovery chain
	// (<=120s prelude, same chain as systemVersion), then waits out
	// upgradeTimeout (300s) — backend bound 420s (B05-002, R2-K2;
	// formerly registered as the bare 300s command leg, so both this
	// relief and the frontend aborted before a slow-prelude upgrade
	// could answer). Frontend 480s.
	nodeUpgradeWriteDeadline = 480 * time.Second
	// nodeBatchUpgrade: the backend bound is ceil(N/5) x
	// upgradeTimeout and grows with N; the handler now caps N at
	// maxBatchUpgradeNodes (25, bound 1500s — B05-002), and an empty
	// version first pays the <=120s discovery prelude, for a worst
	// case of 1620s (R2-K2), so 30 minutes (the gh-mirror precedent
	// magnitude) covers the reachable range with headroom.
	nodeBatchUpgradeWriteDeadline = 30 * time.Minute
	// systemUpgrade (run): the synchronous prelude is <= 240s in the
	// worst case (resolve <=120s — the redirect stage is two 30s legs,
	// not one as the R8 note had it — plus 2x60s shared-ctx asset
	// download legs; B05-002, R2-K2). Frontend 300s.
	systemUpgradeWriteDeadline = 300 * time.Second
	// inboundBatchDelete / inboundBatchToggle (R2-K2 family closure):
	// after the per-item DB work both handlers apply each affected
	// kernel once (deduped across items); every apply carries the
	// conditional kernelApplyTimeout bound (300s when a kernel has no
	// installed version), serially per distinct kernel. The magnitude
	// follows the nodeBatchUpgrade precedent; the frontend sizes its
	// timeout by item count (batchCallTimeoutKernelApply, cap 1800s).
	inboundBatchWriteDeadline = 30 * time.Minute
	// systemVersion / systemCheckUpdates (B05-001/B05-003): the version
	// discovery chain is two sequential stages — the releases API
	// (proxy + direct attempts, 30s client each = 60s) and, on API
	// failure for the stable channel, the redirect fallback (again
	// proxy + direct, 30s each = 60s) — for a 120s backend bound.
	// These two handlers were the last discovery endpoints still on
	// the global 120s cap (an exact-equality bound) because the
	// registry misfiled /system/version as DB/local class (C080).
	// Frontend 150s.
	systemVersionWriteDeadline = 180 * time.Second
	// nodeInstall (C059 closure): getNodeInstallCommand composes the
	// install command only after unconditionally resolving the latest
	// agent version through the same two-stage discovery chain as
	// systemVersion (120s backend bound), yet it stayed on the global
	// 120s cap — an exact-equality bound — because the registry
	// misfiled /node/install as DB/local class and hid the prelude
	// behind the discovery-prelude-legacy named exception. The relief
	// and the frontend value (150s) now match the C080 treatment and
	// the exception is removed. Frontend 150s.
	nodeInstallWriteDeadline = 180 * time.Second
	// Diagnosis: the 120s request context (diagnosisRequestTimeout)
	// stays as the designed work cap; only the outer write layer
	// rises, to ~150s, so a diagnosis that runs its full budget can
	// still deliver its final response / stream done event.
	diagnosisWriteDeadline = 150 * time.Second
	// tunnelUpdate: its composite legs (per-entry-node SSH capability
	// tests, federation applies, runtime cleanup) made the global
	// 120s cap an exact-equality bound; the relief breaks the
	// equality. The work itself remains uncapped.
	tunnelUpdateWriteDeadline = 300 * time.Second
	// tunnelCreate: batch A flagged that create runs the same
	// composite legs as update but was not in the verdict's T1
	// endpoint list, so after batch T2 raised its frontend budget to
	// 120s it sat at FE 120s = SV 120s — the same exact-equality
	// shape. The T-GATE batch completes R8-T-07 for this cell with
	// the parity relief (same value as update); the work itself
	// remains uncapped.
	tunnelCreateWriteDeadline = 300 * time.Second
	// Backup import/restore/export and gist restore/upload perform
	// size-proportional work (a full export/import scales with the
	// backup and the fleet). The magnitude follows the gh-mirror
	// precedent; the relief extends write patience only. The import
	// itself is not capped as a whole: its pre-write snapshot phase
	// stays bound to the request context (an abort there cancels the
	// import with no side effects), and only the detached write phase
	// carries its own same-magnitude cap (backupImportTimeout in
	// handler.go, B14-001 ruling ④).
	backupWriteDeadline = 30 * time.Minute
)

// extendWriteDeadline pushes this response's write deadline out by d
// from now, in the gh_mirror.go precedent form. It is best-effort:
// writers that cannot take a deadline (e.g. httptest recorders outside
// a real server) are silently skipped, matching the precedent. The
// RequestLog statusWriter must stay Unwrap()-able for the call to
// reach the connection (see middleware/request_log.go).
func extendWriteDeadline(w http.ResponseWriter, d time.Duration) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(d))
}
