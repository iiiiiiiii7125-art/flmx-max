package handler

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"go-backend/internal/store/repo"
)

// X-P2-17 (L-11-1, BC-13, V-07): 创建类 POST 接 Idempotency-Key。
// 同（用户,键）重复提交回放首次结果、不再建第二条；不同键/不带键
// 行为不变。修复前：自动选口下同键连发建出两条转发。
func TestR1B8_XP217_ForwardCreateIdempotencyKey(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/xp217.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	nodeID := seedRelayTargetNode(t, r, "entry217", "198.51.100.21")
	tunnelID := seedTunnelForNftables(t, h, "idem-tunnel", nodeID)
	if err := r.DB().Exec(`UPDATE tunnel SET engine = 'realm' WHERE id = ?`, tunnelID).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Exec(`
		INSERT INTO user_tunnel(user_id, tunnel_id, speed_id, num, flow, in_flow, out_flow, flow_reset_time, exp_time, status)
		VALUES(1, ?, NULL, 99999, 99999, 0, 0, 1, 2727251700000, 1)
	`, tunnelID).Error; err != nil {
		t.Fatalf("seed user_tunnel: %v", err)
	}

	post := func(key string) string {
		req := newAuthenticatedJSONRequest(t, map[string]interface{}{
			"tunnelId":   tunnelID,
			"name":       "idem-fwd",
			"remoteAddr": "203.0.113.7:443",
		})
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		res := httptest.NewRecorder()
		h.forwardCreate(res, req)
		return res.Body.String()
	}
	count := func() int {
		return mustQueryInt(t, r, `SELECT COUNT(*) FROM forward WHERE tunnel_id = ?`, tunnelID)
	}

	first := post("key-aaa")
	var env struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal([]byte(first), &env); err != nil || env.Code != 0 {
		t.Fatalf("first create failed: %s", first)
	}
	second := post("key-aaa")
	if second != first {
		t.Fatalf("同键重发应回放首次结果:\nfirst=%s\nsecond=%s", first, second)
	}
	if n := count(); n != 1 {
		t.Fatalf("同键连发建出 %d 条转发, want 1", n)
	}

	third := post("key-bbb")
	if err := json.Unmarshal([]byte(third), &env); err != nil || env.Code != 0 {
		t.Fatalf("different-key create failed: %s", third)
	}
	if n := count(); n != 2 {
		t.Fatalf("不同键应正常建第二条, got %d", n)
	}

	post("")
	if n := count(); n != 3 {
		t.Fatalf("不带键行为不变应建第三条, got %d", n)
	}
}
