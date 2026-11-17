package handler

import (
	"testing"
)

// C-01（流量审计）：新建隧道不传 flow 时缺省为双向（2），显式传 1
// 仍按 1 落库（有意仅计上传的管理员可显式选单向）。
func TestTunnelCreateFlowDefaultsToBidirectional(t *testing.T) {
	h := setupK9Handler(t)
	env := k9Post(t, h, h.tunnelCreate, "/api/v1/tunnel/create", map[string]interface{}{
		"name": "c01-default", "type": 1, "status": 0,
		"inNodeId": []map[string]interface{}{{"nodeId": 10, "protocol": "tls"}},
	})
	if env.Code != 0 {
		t.Fatalf("create without flow must succeed, code=%d msg=%s", env.Code, env.Msg)
	}
	if got := c01TunnelFlow(t, h, "c01-default"); got != 2 {
		t.Fatalf("stored flow = %d, want 2 (bidirectional default)", got)
	}
}

func TestTunnelCreateFlowExplicitOnePersists(t *testing.T) {
	h := setupK9Handler(t)
	env := k9Post(t, h, h.tunnelCreate, "/api/v1/tunnel/create", map[string]interface{}{
		"name": "c01-explicit-one", "type": 1, "status": 0, "flow": 1,
		"inNodeId": []map[string]interface{}{{"nodeId": 10, "protocol": "tls"}},
	})
	if env.Code != 0 {
		t.Fatalf("create with flow=1 must succeed, code=%d msg=%s", env.Code, env.Msg)
	}
	if got := c01TunnelFlow(t, h, "c01-explicit-one"); got != 1 {
		t.Fatalf("stored flow = %d, want 1 (explicit one-way kept)", got)
	}
}

func c01TunnelFlow(t *testing.T, h *Handler, name string) int64 {
	t.Helper()
	var flow int64
	if err := h.repo.DB().Raw(`SELECT flow FROM tunnel WHERE name = ?`, name).Scan(&flow).Error; err != nil {
		t.Fatal(err)
	}
	return flow
}
