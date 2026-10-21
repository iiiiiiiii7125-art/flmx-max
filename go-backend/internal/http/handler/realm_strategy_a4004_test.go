package handler

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"go-backend/internal/store/repo"
)

// FIX-B A4-004（BC-26）：realm 多目标转发用 fifo/rand 策略曾被渲染层静默
// 改写为轮询。修复后创建/编辑双路径明确拒绝；单目标与 round/hash 不受影响。
func setupRealmStrategyFixture(t *testing.T) (*Handler, int64) {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/realm-strategy.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	nodeID := seedRelayTargetNode(t, r, "entry", "198.51.100.12")
	tunnelID := seedTunnelForNftables(t, h, "realm-strategy-tunnel", nodeID)
	if err := r.DB().Exec(`UPDATE tunnel SET engine = 'realm' WHERE id = ?`, tunnelID).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Exec(`
		INSERT INTO user_tunnel(user_id, tunnel_id, speed_id, num, flow, in_flow, out_flow, flow_reset_time, exp_time, status)
		VALUES(1, ?, NULL, 99999, 99999, 0, 0, 1, 2727251700000, 1)
	`, tunnelID).Error; err != nil {
		t.Fatalf("seed user_tunnel: %v", err)
	}
	return h, tunnelID
}

func callForwardCreate(t *testing.T, h *Handler, payload map[string]interface{}) (int, string) {
	t.Helper()
	res := httptest.NewRecorder()
	h.forwardCreate(res, newAuthenticatedJSONRequest(t, payload))
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v body=%s", err, res.Body.String())
	}
	return env.Code, env.Msg
}

func TestForwardCreateRealmRejectsUnsupportedStrategy(t *testing.T) {
	h, tunnelID := setupRealmStrategyFixture(t)

	// 多目标 + fifo → 明确拒绝（修复前：创建成功且被静默按轮询渲染）。
	code, msg := callForwardCreate(t, h, map[string]interface{}{
		"tunnelId": tunnelID, "name": "multi-fifo",
		"remoteAddr": "203.0.113.5:443,203.0.113.6:443",
		"inPort": 30005, "strategy": "fifo",
	})
	if code == 0 {
		t.Fatalf("A4-004: realm multi-target fifo create must be rejected, got success")
	}
	if !strings.Contains(msg, "realm") {
		t.Fatalf("rejection should name realm limitation, msg=%s", msg)
	}

	// 多目标 + rand → 同样拒绝。
	code, _ = callForwardCreate(t, h, map[string]interface{}{
		"tunnelId": tunnelID, "name": "multi-rand",
		"remoteAddr": "203.0.113.5:443,203.0.113.6:443",
		"inPort": 30006, "strategy": "rand",
	})
	if code == 0 {
		t.Fatalf("A4-004: realm multi-target rand create must be rejected, got success")
	}

	// 多目标 + round → 允许。
	code, msg = callForwardCreate(t, h, map[string]interface{}{
		"tunnelId": tunnelID, "name": "multi-round",
		"remoteAddr": "203.0.113.5:443,203.0.113.6:443",
		"inPort": 30007, "strategy": "round",
	})
	if code != 0 {
		t.Fatalf("realm multi-target round create must succeed, code=%d msg=%s", code, msg)
	}

	// 单目标 + fifo（前端默认策略）→ 允许（无负载语义，不误伤存量形态）。
	code, msg = callForwardCreate(t, h, map[string]interface{}{
		"tunnelId": tunnelID, "name": "single-fifo",
		"remoteAddr": "203.0.113.5:443",
		"inPort": 30008, "strategy": "fifo",
	})
	if code != 0 {
		t.Fatalf("realm single-target fifo create must succeed, code=%d msg=%s", code, msg)
	}
}

func TestValidateRealmForwardStrategyUnits(t *testing.T) {
	realmTunnel := &tunnelRecord{Engine: "realm"}
	gostTunnel := &tunnelRecord{Engine: ""}
	if err := validateRealmForwardStrategy(realmTunnel, "a:1,b:2", "fifo"); err == nil {
		t.Fatal("realm multi fifo must error")
	}
	if err := validateRealmForwardStrategy(realmTunnel, "a:1,b:2", "hash"); err != nil {
		t.Fatalf("realm multi hash must pass: %v", err)
	}
	if err := validateRealmForwardStrategy(realmTunnel, "a:1", "rand"); err != nil {
		t.Fatalf("realm single rand must pass: %v", err)
	}
	if err := validateRealmForwardStrategy(gostTunnel, "a:1,b:2", "fifo"); err != nil {
		t.Fatalf("gost multi fifo must pass: %v", err)
	}
	if err := validateRealmForwardStrategy(nil, "a:1,b:2", "fifo"); err != nil {
		t.Fatalf("nil tunnel must pass: %v", err)
	}
}
