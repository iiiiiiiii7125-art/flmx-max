package handler

import (
	"net/http"
	"testing"
	"time"
)

// TestR8T07TunnelCreateExtendsWriteDeadline closes the observation
// batch A flagged when it landed the T1 relief set: /tunnel/update
// received server-side write relief (tunnelUpdateWriteDeadline), but
// /tunnel/create — which runs the same composite legs (per-entry-node
// SSH capability tests, federation applies, guarded service sync) —
// was not in the verdict's T1 endpoint list, so after batch T2 raised
// its frontend budget to 120s it sat at FE 120s = SV 120s, the same
// exact-equality shape T1 relieved for update. This test pins the
// parity relief (300s) armed as the handler's first step, probed with
// a wrong-method request like the batch-A cases (see
// write_deadline_r8_test.go).
func TestR8T07TunnelCreateExtendsWriteDeadline(t *testing.T) {
	h := r8NewHandler(t)
	r8AssertDeadline(t, h, "tunnelCreate", func(w http.ResponseWriter, r *http.Request) {
		h.tunnelCreate(w, r)
	}, 300*time.Second)
}
