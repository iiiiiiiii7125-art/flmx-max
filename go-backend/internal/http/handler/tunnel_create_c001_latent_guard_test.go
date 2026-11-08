package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// C-001 LATENT GUARD (not a reproduction of a live incident).
//
// This test does NOT reproduce a production failure: the defective branch it
// guards is currently unreachable (see Reachability note below), so it passes
// both before and after the fix. Its purpose is purely preventive: it pins the
// trigger-shaped request end-to-end so that if the branch ever becomes
// reachable again without restoring tx discipline, the test hangs and fails
// loudly instead of wedging a production panel's SQLite database.
//
// Background: tunnelCreate once contained a global-repo query
// (h.repo.GetConfigByName("panel_domain")) inside the open SQLite transaction
// on the type-1 + remote-entry path. With SQLite MaxOpenConns(1) that pattern
// deadlocks deterministically and wedges the whole panel DB until restart
// (see plans/042 for the hard constraint: a tx must reuse its own handle).
//
// Removal note (2.1.0): the remote-entry (federation) branch itself was
// deleted with the whole federation/sharing feature, so the guarded shape
// no longer exists; this guard now pins the surviving plain type-1 create
// path end-to-end with the same tx-discipline tripwire (a hang fails loudly
// instead of wedging a production panel's SQLite database).
func TestTunnelCreateType1LatentGuard(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "c001.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()
	h := &Handler{repo: r}
	now := time.Now().UnixMilli()

	// Entry node (id 1).
	if err := r.DB().Exec(
		`INSERT INTO node(name, secret, server_ip, port, status, forward_mode, created_time)
		 VALUES(?, ?, ?, ?, ?, ?, ?)`,
		"entry-1", "s1", "10.0.0.1", "10000-20000", 1, "agent", now,
	).Error; err != nil {
		t.Fatalf("insert entry node: %v", err)
	}
	// Local landing node (id 2).
	if err := r.DB().Exec(
		`INSERT INTO node(name, secret, server_ip, port, status, forward_mode, created_time)
		 VALUES(?, ?, ?, ?, ?, ?, ?)`,
		"local-1", "s2", "10.0.0.2", "10000-20000", 1, "agent", now,
	).Error; err != nil {
		t.Fatalf("insert local node: %v", err)
	}

	body, _ := json.Marshal(map[string]interface{}{
		"name":      "c001-guard",
		"type":      1,
		"inNodeId":  []map[string]interface{}{{"nodeId": 1, "port": 18080, "protocol": "tcp"}},
		"outNodeId": []map[string]interface{}{{"nodeId": 2, "port": 29000, "protocol": "tcp"}},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tunnel/create", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.tunnelCreate(rec, req)
	}()

	select {
	case <-done:
		// Must return; a hang here means the tx-discipline landmine fired.
	case <-time.After(5 * time.Second):
		t.Fatal("tunnelCreate did not return within 5s: suspected SQLite tx self-deadlock (C-001)")
	}

	var env struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode response: %v (body=%q)", err, rec.Body.String())
	}
	if env.Code != 0 {
		t.Fatalf("expected code 0, got %d (body=%q)", env.Code, rec.Body.String())
	}
}
