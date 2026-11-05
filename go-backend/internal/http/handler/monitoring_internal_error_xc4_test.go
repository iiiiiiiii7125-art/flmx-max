package handler

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// XC-4 pin (1): FX-E unified the monitoring admin faces onto
// respondInternalError (code -2, generic "Internal error", raw
// repository error only in server logs), but no dedicated test
// pinned the shape. Sabotage the backing tables so the repository
// calls fail with raw storage errors and assert every face
// returns the unified envelope without leaking the raw text.

func TestXC4MonitoringFacesUnifiedInternalError(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now().UnixMilli()
	adminID, err := r.CreateUser("xc4-admin", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now)
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}

	// Drop the tables the two list faces read, so their repository
	// calls fail with raw "no such table" storage errors.
	if err := r.DB().Migrator().DropTable(&model.Node{}, &model.ServiceMonitor{}); err != nil {
		t.Fatalf("drop tables: %v", err)
	}

	faces := []struct {
		name string
		fn   func(http.ResponseWriter, *http.Request)
		path string
	}{
		{"node list", h.monitorNodeListHandler, "/api/v1/monitor/nodes"},
		{"service list", h.monitorServiceListHandler, "/api/v1/monitor/services"},
	}
	for _, f := range faces {
		rec := httptest.NewRecorder()
		f.fn(rec, claimsCtxReq(http.MethodGet, f.path, "", "", adminID, 0))
		body := rec.Body.String()
		env := decodeEnvelope(t, rec)
		if env.Code != -2 || env.Msg != "Internal error" {
			t.Fatalf("%s: want unified internal-error envelope (code -2, msg %q), got code=%d msg=%q body=%s", f.name, "Internal error", env.Code, env.Msg, body)
		}
		if strings.Contains(body, "no such table") || strings.Contains(body, "ListMonitor") || strings.Contains(body, "ListServiceMonitors") {
			t.Fatalf("%s: raw repository error leaked into the response: %s", f.name, body)
		}
	}
}
