package handler

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// R2-I-02: a Gist backup-failure notice must not be lost when the notify
// channel fails at failure time. Written red-first: on the baseline the
// loop sends it via a bare h.notify with no pending mark, so a failed
// send leaves nothing for the next check round to resend, and a later
// successful upload clears gist_last_error, making the failure invisible.
//
// Mirrors the I-01 (R171-B5) rearm shape: mark pending before dispatch,
// notifyTracked callback re-marks on failure and clears on sent /
// suppressed / disabled, 10-minute checkUserReminders round retries.

func r2i02SetupHandler(t *testing.T, wh *r171b5CountingWebhook) (*Handler, *repo.Repository) {
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

func r2i02Messages(t *testing.T, wh *r171b5CountingWebhook) []notifyMessage {
	t.Helper()
	wh.mu.Lock()
	defer wh.mu.Unlock()
	out := make([]notifyMessage, len(wh.deliveries))
	copy(out, wh.deliveries)
	return out
}

func r2i02WaitPendingCleared(t *testing.T, h *Handler, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if h.loadPendingGistFailure() == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pending gist failure notice not cleared within %v", timeout)
}

// TestR2I02GistFailureNoticeResendAfterChannelFailure is the core R2-I-02
// case: channel fails when the backup fails -> notice is pending, not
// lost -> channel recovers -> next checkUserReminders round resends it,
// exactly once, and clears the pending mark.
func TestR2I02GistFailureNoticeResendAfterChannelFailure(t *testing.T) {
	wh := newR171B5CountingWebhook(t)
	h, r := r2i02SetupHandler(t, wh)
	now := time.Now().UnixMilli()
	if err := r.UpsertConfigs(map[string]string{gistConfigLastError: "GitHub API 500"}, now); err != nil {
		t.Fatalf("seed lastError: %v", err)
	}

	// 1. Channel fails; the backup failure notice must leave a pending
	// mark instead of dropping.
	wh.fail.Store(true)
	h.notifyGistBackupFailed(errors.New("GitHub API 500"))
	if p := h.loadPendingGistFailure(); p == nil || p.Message != "GitHub API 500" {
		t.Fatalf("pending gist failure = %+v, want {GitHub API 500}", p)
	}
	wh.waitAttempts(t, 2, 12*time.Second) // initial + one retry 3s later
	if got := wh.count(); got != 0 {
		t.Fatalf("failing channel must not record deliveries, got %d (%v)", got, wh.titles())
	}

	// 2. Channel recovers: the next check round resends the notice,
	// exactly once, and clears the pending mark.
	wh.fail.Store(false)
	h.checkUserReminders(time.Now())
	wh.waitDeliveries(t, 1, 5*time.Second)
	msgs := r2i02Messages(t, wh)
	if msgs[0].Title != "Gist 备份失败" || !strings.Contains(msgs[0].Message, "GitHub API 500") {
		t.Fatalf("resent message = %+v, want title Gist 备份失败 with GitHub API 500", msgs[0])
	}
	r2i02WaitPendingCleared(t, h, 5*time.Second)

	// 3. A further check round must not duplicate the notice.
	h.checkUserReminders(time.Now().Add(time.Minute))
	time.Sleep(300 * time.Millisecond)
	if got := wh.count(); got != 1 {
		t.Fatalf("notice duplicated: deliveries = %d (%v), want 1", got, wh.titles())
	}
}

// TestR2I02GistFailurePendingDroppedAfterSuccess: a later successful
// upload supersedes the failure — the pending mark is cleared by the
// success itself (same moment gist_last_error is cleared), so the next
// check round sends nothing stale.
func TestR2I02GistFailurePendingDroppedAfterSuccess(t *testing.T) {
	wh := newR171B5CountingWebhook(t)
	h, r := r2i02SetupHandler(t, wh)
	now := time.Now().UnixMilli()
	if err := r.UpsertConfigs(map[string]string{
		gistConfigToken: "test-token-1234567890", gistConfigPassword: "enc-pw",
		gistConfigInterval: "0", gistConfigLastError: "GitHub API 500",
	}, now); err != nil {
		t.Fatalf("seed configs: %v", err)
	}
	newMockGistServer(t, func(w http.ResponseWriter, req *http.Request, rec *mockGistServer) {
		if req.Method == http.MethodPost && req.URL.Path == "/gists" {
			writeJSON(w, 201, map[string]interface{}{"id": "gist-new-id"})
			return
		}
		writeJSON(w, 404, map[string]interface{}{"message": "not found"})
	})
	h.markGistFailurePending("GitHub API 500")
	if p := h.loadPendingGistFailure(); p == nil {
		t.Fatal("pending mark must exist before the upload")
	}
	res, err := h.gistUpload(context.Background(), false)
	if err != nil || !res.Uploaded {
		t.Fatalf("gistUpload = %+v err = %v, want Uploaded=true", res, err)
	}
	if p := h.loadPendingGistFailure(); p != nil {
		t.Fatalf("successful upload must clear the pending mark, got %+v", p)
	}
	cfg, _ := r.GetConfigsByNames([]string{gistConfigLastError})
	if cfg[gistConfigLastError] != "" {
		t.Fatalf("lastError = %q after success, want empty", cfg[gistConfigLastError])
	}

	h.checkUserReminders(time.Now())
	time.Sleep(300 * time.Millisecond)
	if got := wh.count(); got != 0 {
		t.Fatalf("stale failure must not be sent after success, got %d (%v)", got, wh.titles())
	}
}

// TestR2I02StaleFailureNotArmed: a failure that is no longer current
// (gist_last_error already empty — a later upload succeeded first) must
// not arm a retry nor be sent at all.
func TestR2I02StaleFailureNotArmed(t *testing.T) {
	wh := newR171B5CountingWebhook(t)
	h, _ := r2i02SetupHandler(t, wh)
	h.notifyGistBackupFailed(errors.New("GitHub API 500"))
	time.Sleep(500 * time.Millisecond)
	if p := h.loadPendingGistFailure(); p != nil {
		t.Fatalf("stale failure must not leave a pending mark, got %+v", p)
	}
	if got := wh.count(); got != 0 {
		t.Fatalf("stale failure must not be sent, got %d (%v)", got, wh.titles())
	}
}

// TestR2I02GistFailurePendingCorruptDropped: a corrupt pending record
// must not wedge the check round — it is dropped and reset, like I-01.
func TestR2I02GistFailurePendingCorruptDropped(t *testing.T) {
	wh := newR171B5CountingWebhook(t)
	h, r := r2i02SetupHandler(t, wh)
	now := time.Now().UnixMilli()
	if err := r.UpsertConfigs(map[string]string{
		gistConfigLastError:               "GitHub API 500",
		notifyGistFailurePendingConfigKey: "{not json",
	}, now); err != nil {
		t.Fatalf("seed configs: %v", err)
	}
	h.checkUserReminders(time.Now())
	time.Sleep(300 * time.Millisecond)
	if got := wh.count(); got != 0 {
		t.Fatalf("corrupt pending must not send, got %d (%v)", got, wh.titles())
	}
	if p := h.loadPendingGistFailure(); p != nil {
		t.Fatalf("corrupt pending must be dropped, got %+v", p)
	}
}

// TestR2I02GistFailureDisabledClearsPending: when notifications are not
// enabled, the failure notice must not pile up — the Disabled outcome
// clears the mark, like I-01.
func TestR2I02GistFailureDisabledClearsPending(t *testing.T) {
	h := setupGistTest(t)
	r := gistTestRepo(t, h)
	now := time.Now().UnixMilli()
	if err := r.UpsertConfigs(map[string]string{gistConfigLastError: "GitHub API 500"}, now); err != nil {
		t.Fatalf("seed lastError: %v", err)
	}
	h.notifyGistBackupFailed(errors.New("GitHub API 500"))
	r2i02WaitPendingCleared(t, h, 5*time.Second)
}
