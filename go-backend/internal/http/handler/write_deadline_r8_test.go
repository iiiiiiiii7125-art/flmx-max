package handler

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// r8DeadlineWriter records the deadline a handler sets through
// http.ResponseController (the extendWriteDeadline helper), so tests can
// assert that an endpoint actually arms its server-side write relief and
// with the registered duration (R8 T-family, batches T1/T-02(a)).
type r8DeadlineWriter struct {
	*httptest.ResponseRecorder
	deadline    time.Time
	deadlineSet bool
}

func (w *r8DeadlineWriter) SetWriteDeadline(t time.Time) error {
	w.deadline = t
	w.deadlineSet = true
	return nil
}

func r8AssertDeadline(t *testing.T, h *Handler, name string, call func(w http.ResponseWriter, r *http.Request), want time.Duration) {
	t.Helper()
	w := &r8DeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	start := time.Now()
	call(w, req)
	if !w.deadlineSet {
		t.Fatalf("%s: handler did not extend the response write deadline", name)
	}
	got := w.deadline.Sub(start)
	if got < want-15*time.Second || got > want+60*time.Second {
		t.Fatalf("%s: write deadline extension = %v, want ~%v", name, got, want)
	}
}

// TestR8ExtendWriteDeadlineHelper pins the helper itself: it must set
// a deadline ~d out on writers that support one, and silently no-op on
// writers that do not (the gh-mirror precedent's best-effort shape).
func TestR8ExtendWriteDeadlineHelper(t *testing.T) {
	w := &r8DeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	start := time.Now()
	extendWriteDeadline(w, 90*time.Second)
	if !w.deadlineSet {
		t.Fatalf("helper did not set a deadline on a supporting writer")
	}
	if got := w.deadline.Sub(start); got < 75*time.Second || got > 150*time.Second {
		t.Fatalf("helper deadline = %v from start, want ~90s", got)
	}
	// A bare recorder (no SetWriteDeadline, no Unwrap) must not panic.
	extendWriteDeadline(httptest.NewRecorder(), time.Minute)
}

func r8NewHandler(t *testing.T) *Handler {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "r8-write-deadline.db"))
	if err != nil {
		t.Fatal(err)
	}
	return New(r, "unit-test-secret")
}

// TestR8T1EndpointsExtendWriteDeadline covers the batch-T1 endpoint set
// of x-verdicts-r8 §7. Every handler is probed with a wrong-method
// request: the relief is armed as the handler's first step, before the
// method check rejects the call, so the recording writer observes it
// without needing full request fixtures. Expected durations are the
// registered server-layer values from the verdict's per-cell calibers
// (backend bound + margin; see write_deadline.go).
func TestR8T1EndpointsExtendWriteDeadline(t *testing.T) {
	h := r8NewHandler(t)
	cases := []struct {
		name string
		call func(w http.ResponseWriter, r *http.Request)
		want time.Duration
	}{
		// R8-T-01: kernelApplyTimeout = 300s; frontend >= 330s (T2).
		{"kernelInstall", h.kernelInstall, 360 * time.Second},
		// R8-T-01 row 5: inbound save conditionally inherits the same 300s.
		{"inboundCreate", h.inboundCreate, 360 * time.Second},
		{"inboundUpdate", h.inboundUpdate, 360 * time.Second},
		// R8-T-08: export generation is size-proportional (unbounded).
		{"backupExport", h.backupExport, 30 * time.Minute},
		// R8-T-08: gist upload = export build + up to 120s of gist calls.
		{"gistBackupUpload", h.gistBackupUpload, 30 * time.Minute},
		// R8-T-04 + B05-002 (R2-K2): empty version pays the <=120s
		// discovery prelude before upgradeTimeout 300s (bound 420s);
		// server relief and frontend both 480s.
		{"nodeUpgrade", h.nodeUpgrade, 480 * time.Second},
		// R8-T-04: batch bound is ceil(N/5) x 300s, unbounded in N.
		{"nodeBatchUpgrade", h.nodeBatchUpgrade, 30 * time.Minute},
		// R8-T-04: self-upgrade synchronous prelude <= ~210s; frontend >= 240s (T2).
		{"systemUpgrade", h.systemUpgrade, 300 * time.Second},
		// R8-T-07: tunnel update's composite legs vs the 120s global cap.
		{"tunnelUpdate", h.tunnelUpdate, 300 * time.Second},
		// B11-001 (R2-K2): kernel control start/stop conditionally
		// inherits the 300s apply budget via applyKernelWith, like
		// kernelInstall; formerly only the 120s global cap applied.
		{"kernelControl", h.kernelControl, 360 * time.Second},
		// B11-001 family closure (R2-K2): inbound/subscription tails
		// that synchronously await a kernel apply carry the same
		// conditional 300s bound; batch tails apply per distinct
		// kernel and take the batch relief.
		{"inboundDelete", h.inboundDelete, 360 * time.Second},
		{"inboundToggle", h.inboundToggle, 360 * time.Second},
		{"inboundRegenerate", h.inboundRegenerate, 360 * time.Second},
		{"inboundSplitSave", h.inboundSplitSave, 360 * time.Second},
		{"inboundExternalUpdate", h.inboundExternalUpdate, 360 * time.Second},
		{"subscriptionSave", h.subscriptionSave, 360 * time.Second},
		{"inboundBatchDelete", h.inboundBatchDelete, 30 * time.Minute},
		{"inboundBatchToggle", h.inboundBatchToggle, 30 * time.Minute},
		// R8-T-05: the 120s diagnosis ctx stays; outer layers rise to ~150s.
		{"tunnelDiagnose", h.tunnelDiagnose, 150 * time.Second},
		{"forwardDiagnose", h.forwardDiagnose, 150 * time.Second},
		{"tunnelDiagnoseStream", h.tunnelDiagnoseStream, 150 * time.Second},
		{"forwardDiagnoseStream", h.forwardDiagnoseStream, 150 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r8AssertDeadline(t, h, tc.name, tc.call, tc.want)
		})
	}
}
