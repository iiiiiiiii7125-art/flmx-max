package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"go-backend/internal/store/repo"
)

// R7-G-02: user_tunnel status / flowResetTime had no legal-set validation
// on any write path — update and assign accepted any value >= 0 (99, 77)
// and persisted it, while the same fields on the user entity are gated
// (status {0,1}, flowResetTime 0-31; O5-007/O5-008). Permanent regression:
// out-of-set values must be rejected without persisting on the update,
// assign (insert and update branches) paths; in-set values keep working.
// The batch-assign entry does not expose these fields (tunnels[] carries
// only tunnelId/speedId), so its rows derive from the gated user defaults
// — asserted here as the in-set control.

type r7g02Fixture struct {
	h *Handler
	r *repo.Repository
}

func seedR7G02Fixture(t *testing.T) *r7g02Fixture {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "r7-g02.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	stmts := []string{
		`INSERT INTO user(id, user, pwd, role_id, exp_time, flow, in_flow, out_flow, flow_reset_time, num, created_time, updated_time, status) VALUES(2, 'member', 'pwd', 1, 0, 100, 0, 0, 1, 10, 1000, 1000, 1)`,
		`INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, in_ip, inx) VALUES(1, 't-one', 1, 1, 'tls', 1, 1000, 1000, 1, NULL, 0)`,
		`INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, in_ip, inx) VALUES(2, 't-two', 1, 1, 'tls', 1, 1000, 1000, 1, NULL, 0)`,
		`INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, in_ip, inx) VALUES(3, 't-three', 1, 1, 'tls', 1, 1000, 1000, 1, NULL, 0)`,
		`INSERT INTO user_tunnel(id, user_id, tunnel_id, num, flow, in_flow, out_flow, flow_reset_time, exp_time, status) VALUES(10, 2, 1, 10, 100, 0, 0, 5, 0, 1)`,
	}
	for _, s := range stmts {
		if err := r.DB().Exec(s).Error; err != nil {
			t.Fatalf("seed: %v (%s)", err, s)
		}
	}
	return &r7g02Fixture{h: New(r, "test-secret"), r: r}
}

func (f *r7g02Fixture) post(t *testing.T, fn func(http.ResponseWriter, *http.Request), body interface{}) int {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/test", bytes.NewReader(raw))
	res := httptest.NewRecorder()
	fn(res, req)
	var payload struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, res.Body.String())
	}
	return payload.Code
}

func (f *r7g02Fixture) utFields(t *testing.T, id int64) (status int, flowReset int64) {
	t.Helper()
	row, err := f.r.GetUserTunnelByID(id)
	if err != nil || row == nil {
		t.Fatalf("get user_tunnel %d: %v / %v", id, row, err)
	}
	return row.Status, row.FlowResetTime
}

func TestR7G02UserTunnelUpdateRejectsOutOfSet(t *testing.T) {
	f := seedR7G02Fixture(t)

	if code := f.post(t, f.h.userTunnelUpdate, map[string]interface{}{"id": 10, "status": 99}); code == 0 {
		t.Fatal("userTunnelUpdate status=99 accepted, want rejection")
	}
	if status, _ := f.utFields(t, 10); status != 1 {
		t.Fatalf("status persisted as %d after rejected update, want 1", status)
	}

	if code := f.post(t, f.h.userTunnelUpdate, map[string]interface{}{"id": 10, "flowResetTime": 99}); code == 0 {
		t.Fatal("userTunnelUpdate flowResetTime=99 accepted, want rejection")
	}
	if _, fr := f.utFields(t, 10); fr != 5 {
		t.Fatalf("flowResetTime persisted as %d after rejected update, want 5", fr)
	}

	// In-set boundary values remain accepted.
	if code := f.post(t, f.h.userTunnelUpdate, map[string]interface{}{"id": 10, "status": 0, "flowResetTime": 31}); code != 0 {
		t.Fatalf("userTunnelUpdate status=0/flowResetTime=31 rejected, want success")
	}
	if status, fr := f.utFields(t, 10); status != 0 || fr != 31 {
		t.Fatalf("in-set update landed status=%d flowResetTime=%d, want 0/31", status, fr)
	}
	if code := f.post(t, f.h.userTunnelUpdate, map[string]interface{}{"id": 10, "status": 1, "flowResetTime": 0}); code != 0 {
		t.Fatalf("userTunnelUpdate status=1/flowResetTime=0 rejected, want success")
	}
}

func TestR7G02UserTunnelAssignRejectsOutOfSet(t *testing.T) {
	f := seedR7G02Fixture(t)

	// Insert branch.
	if code := f.post(t, f.h.userTunnelAssign, map[string]interface{}{
		"userId": 2, "tunnelId": 2, "status": 7, "flow": 10, "num": 1, "expTime": 0, "flowResetTime": 1,
	}); code == 0 {
		t.Fatal("userTunnelAssign status=7 accepted, want rejection")
	}
	if n := mustQueryInt(t, f.r, `SELECT COUNT(*) FROM user_tunnel WHERE user_id = 2 AND tunnel_id = 2`); n != 0 {
		t.Fatalf("rejected assign persisted %d rows", n)
	}
	if code := f.post(t, f.h.userTunnelAssign, map[string]interface{}{
		"userId": 2, "tunnelId": 2, "flowResetTime": 77, "flow": 10, "num": 1, "expTime": 0, "status": 1,
	}); code == 0 {
		t.Fatal("userTunnelAssign flowResetTime=77 accepted, want rejection")
	}
	if n := mustQueryInt(t, f.r, `SELECT COUNT(*) FROM user_tunnel WHERE user_id = 2 AND tunnel_id = 2`); n != 0 {
		t.Fatalf("rejected assign persisted %d rows", n)
	}

	// In-set insert works and lands the explicit values.
	if code := f.post(t, f.h.userTunnelAssign, map[string]interface{}{
		"userId": 2, "tunnelId": 2, "status": 0, "flowResetTime": 0, "flow": 10, "num": 1, "expTime": 0,
	}); code != 0 {
		t.Fatal("userTunnelAssign status=0/flowResetTime=0 rejected, want success")
	}
	var newID int64
	if err := f.r.DB().Raw(`SELECT id FROM user_tunnel WHERE user_id = 2 AND tunnel_id = 2`).Scan(&newID).Error; err != nil || newID <= 0 {
		t.Fatalf("read assigned row: %v", err)
	}
	if status, fr := f.utFields(t, newID); status != 0 || fr != 0 {
		t.Fatalf("assigned row status=%d flowResetTime=%d, want 0/0", status, fr)
	}

	// Update branch of the same upsert landing point.
	if code := f.post(t, f.h.userTunnelAssign, map[string]interface{}{
		"userId": 2, "tunnelId": 2, "status": 99,
	}); code == 0 {
		t.Fatal("userTunnelAssign (existing row) status=99 accepted, want rejection")
	}
	if status, _ := f.utFields(t, newID); status != 0 {
		t.Fatalf("status persisted as %d after rejected upsert-update, want 0", status)
	}

	// Batch entry (no status/flowResetTime in its per-tunnel shape): the
	// row derives from gated user defaults and lands in-set.
	if code := f.post(t, f.h.userTunnelBatchAssign, map[string]interface{}{
		"userId": 2, "tunnels": []map[string]interface{}{{"tunnelId": 3}},
	}); code != 0 {
		t.Fatal("userTunnelBatchAssign rejected, want success")
	}
	if n := mustQueryInt(t, f.r, `SELECT COUNT(*) FROM user_tunnel WHERE user_id = 2 AND tunnel_id = 3 AND status = 1 AND flow_reset_time = 1`); n != 1 {
		t.Fatalf("batch-assigned row not in-set (status=1, flowResetTime=user default 1): count=%d", n)
	}
}
