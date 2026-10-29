package handler

import (
	"net/http"
	"testing"
	"time"
)

// TestR8T02BackupEndpointsExtendWriteDeadline covers the element-(a)
// server legs of R8-T-02 (x-verdicts-r8 §2.2): /backup/import,
// /backup/restore (both served by backupImport) and
// /backup/gist/restore get per-endpoint write-deadline relief so the
// aligned budget chain can deliver a long import's response. The import
// work itself is NOT capped, and the transaction's binding to
// r.Context() (element (b), X-P3-21 semantics) is deliberately left
// untouched pending a user ruling — this test asserts only the
// server-layer patience.
func TestR8T02BackupEndpointsExtendWriteDeadline(t *testing.T) {
	h := r8NewHandler(t)
	cases := []struct {
		name string
		call func(w http.ResponseWriter, r *http.Request)
		want time.Duration
	}{
		{"backupImport", h.backupImport, 30 * time.Minute},
		{"gistBackupRestore", h.gistBackupRestore, 30 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r8AssertDeadline(t, h, tc.name, tc.call, tc.want)
		})
	}
}
