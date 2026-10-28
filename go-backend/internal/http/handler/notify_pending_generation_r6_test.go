package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// R6-I-01: a stale Failed callback must not overwrite a newer pending
// message. The two pending-mark faces (Gist backup failure, quota
// recovery) share one mechanism: mark-then-dispatch, Failed callback
// re-marks the captured value. Without a freshness check, an old
// dispatch that finishes after a newer dispatch overwrites the newer
// pending record with the stale value.
//
// These tests are written red-first against the 0c5ebff baseline: the
// two "old callback" tests FAIL there (pending ends up OLD) and pass
// once the generation guard lands. The dual cells pin the semantics the
// fix must preserve (same-generation clear + failure re-mark keeps the
// mark) and the semantics it must add (new-generation success clear +
// old failure must not resurrect).

// r6iGenWebhook is a webhook that fails or delivers by message marker.
// OLD-marker requests always fail (500); the 2nd and later OLD attempts
// may be delayed (delayOldSecond) so the old dispatch finishes after a
// newer dispatch. NEW-marker requests fail too, unless newOK, in which
// case they are delivered (200). delayEvery delays every OLD attempt
// (used to hold a dispatch in flight while the test clears the mark).
type r6iGenWebhook struct {
	srv            *httptest.Server
	mu             sync.Mutex
	attempts       map[string]int32
	deliveries     []notifyMessage
	oldMarker      string
	newMarker      string
	newOK          bool
	delayOldSecond time.Duration
	delayEvery     time.Duration
}

func newR6IGenWebhook(t *testing.T, oldMarker, newMarker string, newOK bool) *r6iGenWebhook {
	t.Helper()
	w := &r6iGenWebhook{
		attempts:  map[string]int32{},
		oldMarker: oldMarker,
		newMarker: newMarker,
		newOK:     newOK,
	}
	w.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var m notifyMessage
		_ = json.Unmarshal(body, &m)
		marker := ""
		if strings.Contains(string(body), oldMarker) {
			marker = oldMarker
		} else if strings.Contains(string(body), newMarker) {
			marker = newMarker
		}
		var n int32
		if marker != "" {
			w.mu.Lock()
			w.attempts[marker]++
			n = w.attempts[marker]
			w.mu.Unlock()
		}
		if marker == oldMarker {
			if w.delayEvery > 0 {
				time.Sleep(w.delayEvery)
			} else if n >= 2 && w.delayOldSecond > 0 {
				time.Sleep(w.delayOldSecond)
			}
			rw.WriteHeader(http.StatusInternalServerError)
			return
		}
		if marker == newMarker && newOK {
			w.mu.Lock()
			w.deliveries = append(w.deliveries, m)
			w.mu.Unlock()
			rw.WriteHeader(http.StatusOK)
			return
		}
		rw.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(w.srv.Close)
	return w
}

func (w *r6iGenWebhook) markerAttempts(marker string) int32 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.attempts[marker]
}

func (w *r6iGenWebhook) waitMarkerAttempts(t *testing.T, marker string, want int32, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if w.markerAttempts(marker) >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("webhook attempts for %q = %d, want >= %d", marker, w.markerAttempts(marker), want)
}

func (w *r6iGenWebhook) deliveryCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.deliveries)
}

func r6iSetupHandler(t *testing.T, wh *r6iGenWebhook) (*Handler, *repo.Repository) {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	now := time.Now().UnixMilli()
	for k, v := range map[string]string{"notify_enabled": "true", "notify_webhook_url": wh.srv.URL} {
		if err := r.DB().Exec(`INSERT INTO vite_config(name, value, time) VALUES(?, ?, ?)`, k, v, now).Error; err != nil {
			t.Fatal(err)
		}
	}
	return New(r, "unit-test-secret"), r
}

func r6iWaitPendingGist(t *testing.T, h *Handler, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if p := h.loadPendingGistFailure(); p != nil && p.Message == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	p := h.loadPendingGistFailure()
	got := "<nil>"
	if p != nil {
		got = p.Message
	}
	t.Fatalf("pending gist failure = %q, want %q", got, want)
}

func r6iPendingQuotaReason(t *testing.T, h *Handler, userID int64) (string, bool) {
	t.Helper()
	for _, p := range h.loadPendingQuotaRecoveries() {
		if p.UserID == userID {
			return p.Reason, true
		}
	}
	return "", false
}

// TestR6I_GistOldCallbackMustNotOverwriteNewMessage is the Gist face of
// R6-I-01: the old dispatch's second attempt is delayed so it finishes
// after the newer dispatch; the pending record must stay NEW-FAIL.
func TestR6I_GistOldCallbackMustNotOverwriteNewMessage(t *testing.T) {
	wh := newR6IGenWebhook(t, "OLD-FAIL", "NEW-FAIL", false)
	wh.delayOldSecond = 5 * time.Second
	h, r := r6iSetupHandler(t, wh)
	now := time.Now().UnixMilli()
	if err := r.UpsertConfigs(map[string]string{gistConfigLastError: "OLD-FAIL"}, now); err != nil {
		t.Fatalf("seed lastError: %v", err)
	}
	h.notifyGistBackupFailed(errors.New("OLD-FAIL"))
	wh.waitMarkerAttempts(t, "OLD-FAIL", 1, 10*time.Second)
	if err := r.UpsertConfigs(map[string]string{gistConfigLastError: "NEW-FAIL"}, now); err != nil {
		t.Fatalf("update lastError: %v", err)
	}
	h.notifyGistBackupFailed(errors.New("NEW-FAIL"))
	wh.waitMarkerAttempts(t, "NEW-FAIL", 2, 15*time.Second)
	wh.waitMarkerAttempts(t, "OLD-FAIL", 2, 30*time.Second)
	// Attempts are counted on arrival; the old callback lands only after
	// the delayed 2nd response, so wait the delay out before asserting.
	time.Sleep(wh.delayOldSecond + time.Second)
	r6iWaitPendingGist(t, h, "NEW-FAIL", 5*time.Second)
}

// TestR6I_QuotaOldCallbackMustNotOverwriteNewReason is the quota face of
// R6-I-01, isomorphic to the Gist face.
func TestR6I_QuotaOldCallbackMustNotOverwriteNewReason(t *testing.T) {
	wh := newR6IGenWebhook(t, "r6i-old-reason", "r6i-new-reason", false)
	wh.delayOldSecond = 5 * time.Second
	h, r := r6iSetupHandler(t, wh)
	userID := r171b5SeedBlockedQuotaUser(t, r, time.Now())
	h.notifyUserQuotaRecovered(userID, "r6i-old-reason")
	wh.waitMarkerAttempts(t, "r6i-old-reason", 1, 10*time.Second)
	h.notifyUserQuotaRecovered(userID, "r6i-new-reason")
	wh.waitMarkerAttempts(t, "r6i-new-reason", 2, 15*time.Second)
	wh.waitMarkerAttempts(t, "r6i-old-reason", 2, 30*time.Second)
	// See the Gist face: wait out the delayed 2nd response so the old
	// callback has landed before asserting.
	time.Sleep(wh.delayOldSecond + time.Second)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if reason, ok := r6iPendingQuotaReason(t, h, userID); ok && reason == "r6i-new-reason" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	reason, ok := r6iPendingQuotaReason(t, h, userID)
	t.Fatalf("pending quota recovery reason = %q (present=%v), want r6i-new-reason", reason, ok)
}

// TestR6I_GistClearedPendingRearmedBySameGenerationFailure pins the dual
// semantics the fix must preserve: a same-generation dispatch that fails
// after the record was cleared (simulating a concurrent Suppressed
// outcome clearing it) must re-arm the mark.
func TestR6I_GistClearedPendingRearmedBySameGenerationFailure(t *testing.T) {
	wh := newR6IGenWebhook(t, "OLD-FAIL", "NEW-FAIL", false)
	wh.delayEvery = 3 * time.Second
	h, r := r6iSetupHandler(t, wh)
	now := time.Now().UnixMilli()
	if err := r.UpsertConfigs(map[string]string{gistConfigLastError: "OLD-FAIL"}, now); err != nil {
		t.Fatalf("seed lastError: %v", err)
	}
	h.notifyGistBackupFailed(errors.New("OLD-FAIL"))
	wh.waitMarkerAttempts(t, "OLD-FAIL", 1, 10*time.Second)
	// A concurrent Suppressed outcome clears the record while this
	// generation's dispatch is still in flight.
	h.clearGistFailurePending()
	if p := h.loadPendingGistFailure(); p != nil {
		t.Fatalf("pending must be cleared before the failure lands, got %+v", p)
	}
	r6iWaitPendingGist(t, h, "OLD-FAIL", 20*time.Second)
}

// TestR6I_QuotaClearedPendingRearmedBySameGenerationFailure is the quota
// face of the same-generation re-arm dual cell.
func TestR6I_QuotaClearedPendingRearmedBySameGenerationFailure(t *testing.T) {
	wh := newR6IGenWebhook(t, "r6i-old-reason", "r6i-new-reason", false)
	wh.delayEvery = 3 * time.Second
	h, r := r6iSetupHandler(t, wh)
	userID := r171b5SeedBlockedQuotaUser(t, r, time.Now())
	h.notifyUserQuotaRecovered(userID, "r6i-old-reason")
	wh.waitMarkerAttempts(t, "r6i-old-reason", 1, 10*time.Second)
	h.clearQuotaRecoveryPending(userID)
	if _, ok := r6iPendingQuotaReason(t, h, userID); ok {
		t.Fatal("pending must be cleared before the failure lands")
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if reason, ok := r6iPendingQuotaReason(t, h, userID); ok && reason == "r6i-old-reason" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("same-generation failure must re-arm the cleared pending mark")
}

// TestR6I_GistNewGenerationSuccessClearsOldFailureNotResurrected pins
// the second dual cell: a newer generation's dispatch succeeds and
// clears the record; the old dispatch's later failure must not bring
// the stale message back.
func TestR6I_GistNewGenerationSuccessClearsOldFailureNotResurrected(t *testing.T) {
	wh := newR6IGenWebhook(t, "OLD-FAIL", "NEW-FAIL", true)
	wh.delayOldSecond = 5 * time.Second
	h, r := r6iSetupHandler(t, wh)
	now := time.Now().UnixMilli()
	if err := r.UpsertConfigs(map[string]string{gistConfigLastError: "OLD-FAIL"}, now); err != nil {
		t.Fatalf("seed lastError: %v", err)
	}
	h.notifyGistBackupFailed(errors.New("OLD-FAIL"))
	wh.waitMarkerAttempts(t, "OLD-FAIL", 1, 10*time.Second)
	if err := r.UpsertConfigs(map[string]string{gistConfigLastError: "NEW-FAIL"}, now); err != nil {
		t.Fatalf("update lastError: %v", err)
	}
	h.notifyGistBackupFailed(errors.New("NEW-FAIL"))
	// The new dispatch succeeds (single attempt) and clears the mark.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if wh.deliveryCount() >= 1 && h.loadPendingGistFailure() == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if p := h.loadPendingGistFailure(); p != nil {
		t.Fatalf("new-generation success must clear the pending mark, got %+v", p)
	}
	// The old dispatch finishes later; its failure must not resurrect.
	// Wait out the delayed 2nd response so its callback has landed.
	wh.waitMarkerAttempts(t, "OLD-FAIL", 2, 30*time.Second)
	time.Sleep(wh.delayOldSecond + time.Second)
	if p := h.loadPendingGistFailure(); p != nil {
		t.Fatalf("old failure must not resurrect the pending mark, got %+v", p)
	}
}

// TestR6I_QuotaNewGenerationSuccessClearsOldFailureNotResurrected is the
// quota face of the second dual cell.
func TestR6I_QuotaNewGenerationSuccessClearsOldFailureNotResurrected(t *testing.T) {
	wh := newR6IGenWebhook(t, "r6i-old-reason", "r6i-new-reason", true)
	wh.delayOldSecond = 5 * time.Second
	h, r := r6iSetupHandler(t, wh)
	userID := r171b5SeedBlockedQuotaUser(t, r, time.Now())
	h.notifyUserQuotaRecovered(userID, "r6i-old-reason")
	wh.waitMarkerAttempts(t, "r6i-old-reason", 1, 10*time.Second)
	h.notifyUserQuotaRecovered(userID, "r6i-new-reason")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if wh.deliveryCount() >= 1 {
			if _, ok := r6iPendingQuotaReason(t, h, userID); !ok {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, ok := r6iPendingQuotaReason(t, h, userID); ok {
		t.Fatal("new-generation success must clear the pending mark")
	}
	wh.waitMarkerAttempts(t, "r6i-old-reason", 2, 30*time.Second)
	time.Sleep(wh.delayOldSecond + time.Second)
	if reason, ok := r6iPendingQuotaReason(t, h, userID); ok {
		t.Fatalf("old failure must not resurrect the pending mark, got reason %q", reason)
	}
}
