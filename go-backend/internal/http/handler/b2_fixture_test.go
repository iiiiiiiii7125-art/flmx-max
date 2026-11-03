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

// b2Fixture 是 R1/R2/R3 批测试共用的造数夹具（原置于已删的
// r1_b2_group_grants_test.go）：用户 U=2/U=4、隧道 T=1/2/3、
// user_tunnel UT10–UT13、forward20/21。分组三件套已于 1.9.6
// 整套移除，夹具不再造分组表行（组建/手工的来源区分亦不复存在，
// UT10–UT13 现为普通授权行）。

type b2Fixture struct {
	h *Handler
	r *repo.Repository
}

func seedB2Fixture(t *testing.T) *b2Fixture {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "r1-b2.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	stmts := []string{
		`INSERT INTO user(id, user, pwd, role_id, exp_time, flow, in_flow, out_flow, flow_reset_time, num, created_time, updated_time, status) VALUES(2, 'member', 'pwd', 1, 0, 100, 0, 0, 0, 10, 1000, 1000, 1)`,
		`INSERT INTO user(id, user, pwd, role_id, exp_time, flow, in_flow, out_flow, flow_reset_time, num, created_time, updated_time, status) VALUES(4, 'member2', 'pwd', 1, 0, 100, 0, 0, 0, 10, 1000, 1000, 1)`,
		`INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, in_ip, inx) VALUES(1, 't-one', 1, 1, 'tls', 1, 1000, 1000, 1, NULL, 0)`,
		`INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, in_ip, inx) VALUES(2, 't-two', 1, 1, 'tls', 1, 1000, 1000, 1, NULL, 0)`,
		`INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, in_ip, inx) VALUES(3, 't-three', 1, 1, 'tls', 1, 1000, 1000, 1, NULL, 0)`,
		`INSERT INTO user_tunnel(id, user_id, tunnel_id, num, flow, in_flow, out_flow, flow_reset_time, exp_time, status) VALUES(10, 2, 1, 10, 100, 0, 0, 0, 0, 1)`,
		`INSERT INTO user_tunnel(id, user_id, tunnel_id, num, flow, in_flow, out_flow, flow_reset_time, exp_time, status) VALUES(11, 2, 2, 10, 100, 0, 0, 0, 0, 1)`,
		`INSERT INTO user_tunnel(id, user_id, tunnel_id, num, flow, in_flow, out_flow, flow_reset_time, exp_time, status) VALUES(12, 2, 3, 10, 100, 0, 0, 0, 0, 1)`,
		`INSERT INTO user_tunnel(id, user_id, tunnel_id, num, flow, in_flow, out_flow, flow_reset_time, exp_time, status) VALUES(13, 4, 1, 10, 100, 0, 0, 0, 0, 1)`,
		`INSERT INTO forward(id, user_id, user_name, name, tunnel_id, remote_addr, strategy, in_flow, out_flow, created_time, updated_time, status, inx) VALUES(20, 2, 'member', 'f-t1', 1, '10.0.0.1:80', 'fifo', 0, 0, 1000, 1000, 1, 0)`,
		`INSERT INTO forward(id, user_id, user_name, name, tunnel_id, remote_addr, strategy, in_flow, out_flow, created_time, updated_time, status, inx) VALUES(21, 2, 'member', 'f-t3', 3, '10.0.0.3:80', 'fifo', 0, 0, 1000, 1000, 1, 0)`,
	}
	for _, s := range stmts {
		if err := r.DB().Exec(s).Error; err != nil {
			t.Fatalf("seed: %v (%s)", err, s)
		}
	}
	return &b2Fixture{h: New(r, "test-secret"), r: r}
}

func (f *b2Fixture) count(t *testing.T, query string, args ...interface{}) int {
	t.Helper()
	return mustQueryInt(t, f.r, query, args...)
}

func (f *b2Fixture) post(t *testing.T, fn func(http.ResponseWriter, *http.Request), body interface{}) (int, string) {
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
	return payload.Code, res.Body.String()
}
