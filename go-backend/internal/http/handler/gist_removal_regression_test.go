package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// DEV-RM-GIST regression pins (2.1.0): the Gist backup face is gone.
// All five /backup/gist/* routes must 404 on both registered prefixes,
// while the retained local backup routes stay registered (any non-404
// handler response proves the route exists; auth handling is covered
// by the config/backup suites).
func TestGistRoutesRemoved(t *testing.T) {
	h := setupHandlerTestRepo(t)
	mux := http.NewServeMux()
	h.Register(mux)

	gistPaths := []string{
		"/api/v1/backup/gist/status",
		"/api/v1/backup/gist/save",
		"/api/v1/backup/gist/upload",
		"/api/v1/backup/gist/versions",
		"/api/v1/backup/gist/restore",
		"/api/v1/api/v1/backup/gist/status",
	}
	for _, p := range gistPaths {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, adminRequest(t, http.MethodPost, p, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("gist route %s: status = %d, want 404 (body=%s)", p, rec.Code, rec.Body.String())
		}
	}

	keptPaths := []string{
		"/api/v1/backup/export",
		"/api/v1/backup/import-auto-backup/status",
	}
	for _, p := range keptPaths {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, adminRequest(t, http.MethodPost, p, nil))
		if rec.Code == http.StatusNotFound {
			t.Fatalf("retained backup route %s must stay registered, got 404", p)
		}
	}
}
