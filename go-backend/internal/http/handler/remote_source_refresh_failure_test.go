package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// TestSyncRemoteSourceFailureKeepsLastFetch (R18-F3-01, remote-source
// half): a failed sync must not advance LastFetch — only a success
// stamps "last fetched". The old fail() wrote last_fetch on every
// failure, so a source that failed once at its due time silently
// waited a whole IntervalHours before the next automatic retry,
// because remoteSourceDue gates on LastFetch alone.
func TestSyncRemoteSourceFailureKeepsLastFetch(t *testing.T) {
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	r, err := repo.Open(t.TempDir() + "/remote-f3-01.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	var serveErr bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if serveErr {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(integrityLinkA + "\n"))
	}))
	defer srv.Close()

	now := time.Now().UnixMilli()
	// Last successful fetch was 7h ago with a 6h interval: due.
	lastFetchBefore := now - 7*60*60*1000
	src := &model.RemoteSource{Name: "源", Kind: model.RemoteSourceURL, URL: srv.URL,
		Enabled: 1, IntervalHours: 6, LastFetch: lastFetchBefore,
		CreatedTime: now, UpdatedTime: now}
	if err := r.SaveRemoteSource(src); err != nil {
		t.Fatal(err)
	}

	// Failure (HTTP 500): error recorded, LastFetch untouched.
	serveErr = true
	if _, err := h.syncRemoteSource(context.Background(), src, ""); err == nil {
		t.Fatal("sync of a broken source succeeded")
	}
	got, err := r.GetRemoteSource(src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastFetch != lastFetchBefore {
		t.Fatalf("failed sync advanced LastFetch %d -> %d, want unchanged", lastFetchBefore, got.LastFetch)
	}
	if got.LastError == "" {
		t.Fatalf("failed sync did not record LastError: %+v", got)
	}

	// The next 10-minute loop tick must retry instead of waiting
	// another IntervalHours: with the source healthy again,
	// refreshDueRemoteSources at +10min re-syncs and advances LastFetch.
	serveErr = false
	h.refreshDueRemoteSources(context.Background(), time.UnixMilli(now).Add(10*time.Minute))
	got, err = r.GetRemoteSource(src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastFetch == lastFetchBefore {
		t.Fatalf("due source was not retried on the next tick: LastFetch still %d, LastError %q", got.LastFetch, got.LastError)
	}
	if got.LastError != "" {
		t.Fatalf("successful retry left LastError %q", got.LastError)
	}
}
