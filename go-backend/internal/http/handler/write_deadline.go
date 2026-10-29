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
	// nodeUpgrade: the backend bound is upgradeTimeout (300s).
	// Frontend 360s (batch T2).
	nodeUpgradeWriteDeadline = 360 * time.Second
	// nodeBatchUpgrade: the backend bound is ceil(N/5) x
	// upgradeTimeout and grows with N; 30 minutes (the gh-mirror
	// precedent magnitude) covers the frontend's registered range
	// with headroom.
	nodeBatchUpgradeWriteDeadline = 30 * time.Minute
	// systemUpgrade (run): the synchronous prelude is <= ~210s in the
	// worst case; frontend >= 240s (batch T2).
	systemUpgradeWriteDeadline = 300 * time.Second
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
	// Backup import/restore/export and gist restore/upload perform
	// size-proportional, unbounded work (a full export/import scales
	// with the backup and the fleet). The magnitude follows the
	// gh-mirror precedent; the relief extends write patience only —
	// capping the import itself would be data-hostile and is not done.
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
