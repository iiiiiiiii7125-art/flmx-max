package handler

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"go-backend/internal/store/repo"
)

// FIX-B A4-002：forwardCreate 原先调 syncForwardServices（丢弃 warnings）后恒
// OKEmpty——realm 限速/连接数「不生效」告警在创建时永远到不了前端（update
// 路径有、create 路径无，R3 真机定案）。要求 create 与 update 同形返回
// data.warnings。
func TestForwardCreateReturnsRealmWarnings(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/fwd-create-warnings.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	nodeID := seedRelayTargetNode(t, r, "entry", "198.51.100.11")
	tunnelID := seedTunnelForNftables(t, h, "realm-create-tunnel", nodeID)
	if err := r.DB().Exec(`UPDATE tunnel SET engine = 'realm' WHERE id = ?`, tunnelID).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Exec(`
		INSERT INTO user_tunnel(user_id, tunnel_id, speed_id, num, flow, in_flow, out_flow, flow_reset_time, exp_time, status)
		VALUES(1, ?, NULL, 99999, 99999, 0, 0, 1, 2727251700000, 1)
	`, tunnelID).Error; err != nil {
		t.Fatalf("seed user_tunnel: %v", err)
	}

	req := newAuthenticatedJSONRequest(t, map[string]interface{}{
		"tunnelId":   tunnelID,
		"name":       "realm-limited",
		"remoteAddr": "203.0.113.5:443",
		"inPort":     30005,
		"maxConn":    5,
	})
	res := httptest.NewRecorder()
	h.forwardCreate(res, req)

	var payload struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Warnings []string `json:"warnings"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v body=%s", err, res.Body.String())
	}
	if payload.Code != 0 {
		t.Fatalf("create failed: code=%d msg=%s body=%s", payload.Code, payload.Msg, res.Body.String())
	}
	found := false
	for _, w := range payload.Data.Warnings {
		if strings.Contains(w, "realm 引擎不支持限速和连接数限制") {
			found = true
		}
	}
	if !found {
		t.Fatalf("A4-002: create response missing realm limit warning, warnings=%v body=%s", payload.Data.Warnings, res.Body.String())
	}
}
