package handler

import (
	"bytes"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// R18-F2-02 (M-3): a permanently vetoed quota release used to be
// retried by the per-minute backstop forever, with no backoff and no
// escalation. The backstop now counts consecutive failures per user,
// backs off (1m → 5m → 15m → 60m), and after 10 failures marks the
// release needs-admin (one action=needs_admin line) while still
// probing at the 60m cap so a cleared cause recovers on its own.
func TestR18F202QuotaReleaseBackoffTiers(t *testing.T) {
	cases := map[int]time.Duration{
		0:  time.Minute,
		1:  time.Minute,
		2:  5 * time.Minute,
		3:  15 * time.Minute,
		4:  time.Hour,
		10: time.Hour,
	}
	for failures, want := range cases {
		if got := quotaReleaseBackoff(failures); got != want {
			t.Fatalf("quotaReleaseBackoff(%d) = %v, want %v", failures, got, want)
		}
	}
}

func TestR18F202BackstopBacksOffEscalatesThenRecovers(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now()
	userID, forwardID := c05SeedBlockedUser(t, r, now, false)
	// Permanent veto: the tunnel is disabled, so every release
	// attempt fails at ensureTunnelEnabledForForward before any
	// agent call (tunnel_not_enabled).
	if err := r.DB().Exec(`UPDATE tunnel SET status = 0 WHERE id = 41`).Error; err != nil {
		t.Fatalf("disable tunnel: %v", err)
	}
	if _, err := r.ResetUserQuotaUsage(userID, "all", now); err != nil {
		t.Fatalf("reset quota: %v", err)
	}

	var buf bytes.Buffer
	oldOut := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(oldOut) })
	pendingLines := func() int { return strings.Count(buf.String(), "quota release retry pending") }
	needsAdminLines := func() int { return strings.Count(buf.String(), "action=needs_admin") }
	failuresOf := func() (int, bool) {
		h.quotaReleaseFailMu.Lock()
		defer h.quotaReleaseFailMu.Unlock()
		st := h.quotaReleaseFail[userID]
		if st == nil {
			return 0, false
		}
		return st.failures, st.needsAdmin
	}
	nextAllowed := func() time.Time {
		h.quotaReleaseFailMu.Lock()
		defer h.quotaReleaseFailMu.Unlock()
		return time.UnixMilli(h.quotaReleaseFail[userID].nextAllowed)
	}

	// Round 1 attempts and arms a 1m backoff.
	h.reconcilePendingQuotaReleases(now)
	if got, _ := failuresOf(); got != 1 {
		t.Fatalf("after round 1 failures = %d, want 1", got)
	}
	if got := pendingLines(); got != 1 {
		t.Fatalf("after round 1 pending lines = %d, want 1", got)
	}

	// Round 2 inside the window is skipped: no new attempt, no log.
	h.reconcilePendingQuotaReleases(now.Add(30 * time.Second))
	if got, _ := failuresOf(); got != 1 {
		t.Fatalf("round inside backoff must not attempt, failures = %d, want 1", got)
	}
	if got := pendingLines(); got != 1 {
		t.Fatalf("round inside backoff must not log, pending lines = %d, want 1", got)
	}

	// Drive to the cap, one due attempt per round.
	for i := 0; i < 15; i++ {
		if got, _ := failuresOf(); got >= quotaReleaseMaxConsecutiveFailures {
			break
		}
		now = nextAllowed().Add(time.Second)
		h.reconcilePendingQuotaReleases(now)
	}
	if got, needsAdmin := failuresOf(); got != quotaReleaseMaxConsecutiveFailures || !needsAdmin {
		t.Fatalf("after driving to the cap failures = %d needsAdmin = %v, want %d true", got, needsAdmin, quotaReleaseMaxConsecutiveFailures)
	}
	if got := needsAdminLines(); got != 1 {
		t.Fatalf("needs_admin lines = %d, want exactly 1 (transition only)", got)
	}

	// Inside the 60m cap the backstop stays quiet.
	h.reconcilePendingQuotaReleases(now.Add(30 * time.Minute))
	if got, _ := failuresOf(); got != quotaReleaseMaxConsecutiveFailures {
		t.Fatalf("round inside the 60m cap must not attempt, failures = %d", got)
	}
	if got := needsAdminLines(); got != 1 {
		t.Fatalf("needs_admin lines = %d after a skipped round, want 1", got)
	}

	// Cause cleared (tunnel re-enabled): the next due attempt recovers
	// the forward and drops the failure state.
	if err := r.DB().Exec(`UPDATE tunnel SET status = 1 WHERE id = 41`).Error; err != nil {
		t.Fatalf("re-enable tunnel: %v", err)
	}
	h.reconcilePendingQuotaReleases(nextAllowed().Add(time.Second))
	if got := c05ForwardStatus(t, r, forwardID); got != 1 {
		t.Fatalf("forward %d status = %d after recovery, want 1", forwardID, got)
	}
	if got := c05PausedList(t, r, userID); got != "" {
		t.Fatalf("PausedForwardIDs = %q after recovery, want empty", got)
	}
	if got, _ := failuresOf(); got != 0 {
		t.Fatalf("failure state must be cleared after recovery, failures = %d", got)
	}
}
