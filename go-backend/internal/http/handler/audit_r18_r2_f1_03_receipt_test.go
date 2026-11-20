package handler

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestTunnelCreateOfflineTLSNodesWarnUnverified (R18-R2-F1-03
// receipt): creating a TLS tunnel whose nodes are offline still
// succeeds, but the receipt must warn that the chain was built
// unverified — naming the nodes and the cause — instead of
// reporting bare success.
func TestTunnelCreateOfflineTLSNodesWarnUnverified(t *testing.T) {
	h := setupK9Handler(t)
	res := httptest.NewRecorder()
	h.tunnelCreate(res, newAuthenticatedJSONRequest(t, map[string]interface{}{
		"name": "f1-03-create", "type": 2, "status": 1,
		"inNodeId":   []map[string]interface{}{{"nodeId": 10, "protocol": "tls"}},
		"chainNodes": []interface{}{[]map[string]interface{}{{"nodeId": 12, "protocol": "tls"}}},
		"outNodeId":  []map[string]interface{}{{"nodeId": 11, "protocol": "tls"}},
	}))
	var env struct {
		Code    int    `json:"code"`
		Msg     string `json:"msg"`
		Warning string `json:"warning"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode tunnel create response: %v (%s)", err, res.Body.String())
	}
	if env.Code != 0 {
		t.Fatalf("tunnel create with offline nodes must still succeed, got code=%d msg=%q", env.Code, env.Msg)
	}
	for _, name := range []string{"entry-a", "chain-a", "exit-a"} {
		if !strings.Contains(env.Warning, name) {
			t.Fatalf("receipt warning must name unverified node %q, got warning=%q", name, env.Warning)
		}
	}
	for _, want := range []string{"以未验证方式建成", "无双向证书认证", "节点离线", "重新部署"} {
		if !strings.Contains(env.Warning, want) {
			t.Fatalf("receipt warning must say %q, got warning=%q", want, env.Warning)
		}
	}
}

// TestTunnelUpdateOfflineTLSNodesWarnUnverified (R18-R2-F1-03
// receipt): the update receipt carries the same unverified-chain
// warning in its warnings list.
func TestTunnelUpdateOfflineTLSNodesWarnUnverified(t *testing.T) {
	h := setupK9Handler(t)
	seedK9Type2Tunnel(t, h, 88, "f1-03-upd", 36008, 35009)

	env := k9Post(t, h, h.tunnelUpdate, "/api/v1/tunnel/update", map[string]interface{}{
		"id": 88, "name": "f1-03-upd-renamed", "type": 2, "status": 1, "flow": 1, "trafficRatio": 1,
		"inNodeId":   []map[string]interface{}{{"nodeId": 10, "protocol": "tls"}},
		"chainNodes": []interface{}{[]map[string]interface{}{{"nodeId": 12, "protocol": "tls"}}},
		"outNodeId":  []map[string]interface{}{{"nodeId": 11, "protocol": "tls"}},
	})
	if env.Code != 0 {
		t.Fatalf("tunnel update with offline nodes must still succeed, got code=%d msg=%s", env.Code, env.Msg)
	}
	joined := k6WarningsJoined(env)
	for _, name := range []string{"entry-a", "chain-a", "exit-a"} {
		if !strings.Contains(joined, name) {
			t.Fatalf("update receipt warnings must name unverified node %q, got %q", name, joined)
		}
	}
	for _, want := range []string{"以未验证方式建成", "无双向证书认证", "节点离线", "重新部署"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("update receipt warnings must say %q, got %q", want, joined)
		}
	}
}
