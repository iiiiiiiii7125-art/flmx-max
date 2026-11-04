package handler

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"go-backend/internal/store/repo"
)

// R4-F4-02 回归钉：转发 strategy 写面合法集 {fifo, round, rand,
// hash}（kernel.ForwardStrategyLegal 单点，恢复面共用）。
// 修复前 create/update 原文落库，集外值经 agent 解析一律
// roundrobin——声明与生效静默不一致。本组钉 create/update 两面：
// 集外（含大小写异形）点名拒绝；带空白的合法值归一落库；存量坏
// 策略行经合并值口径在任何编辑时被拒、补正后放行。

func setupStrategyLegalFixture(t *testing.T) (*Handler, *repo.Repository, int64) {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/strategy-legal.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	nodeID := seedRelayTargetNode(t, r, "entry", "198.51.100.12")
	tunnelID := seedTunnelForNftables(t, h, "strategy-legal-tunnel", nodeID)
	if err := r.DB().Exec(`
		INSERT INTO user_tunnel(user_id, tunnel_id, speed_id, num, flow, in_flow, out_flow, flow_reset_time, exp_time, status)
		VALUES(1, ?, NULL, 99999, 99999, 0, 0, 1, 2727251700000, 1)
	`, tunnelID).Error; err != nil {
		t.Fatalf("seed user_tunnel: %v", err)
	}
	return h, r, tunnelID
}

func TestR4F4ForwardCreateStrategyLegalSet(t *testing.T) {
	h, r, tunnelID := setupStrategyLegalFixture(t)

	// 集外值（gost 隧道，realm 门不参与）→ 点名拒绝。
	code, msg := callForwardCreate(t, h, map[string]interface{}{
		"tunnelId": tunnelID, "name": "bad-banana",
		"remoteAddr": "203.0.113.5:443,203.0.113.6:443",
		"inPort":     30001, "strategy": "banana",
	})
	if code == 0 {
		t.Fatal("create with strategy=banana must be rejected")
	}
	if !strings.Contains(msg, "banana") {
		t.Fatalf("rejection must name the illegal strategy, msg=%s", msg)
	}

	// 大小写异形同样是集外值（agent 解析大小写敏感、落 default）。
	code, msg = callForwardCreate(t, h, map[string]interface{}{
		"tunnelId": tunnelID, "name": "bad-fifo-upper",
		"remoteAddr": "203.0.113.5:443",
		"inPort":     30002, "strategy": "FIFO",
	})
	if code == 0 {
		t.Fatal("create with strategy=FIFO must be rejected")
	}
	if !strings.Contains(msg, "FIFO") {
		t.Fatalf("rejection must name the illegal strategy, msg=%s", msg)
	}

	// 带空白的合法值归一落库（存修剪后的规范形）。
	code, msg = callForwardCreate(t, h, map[string]interface{}{
		"tunnelId": tunnelID, "name": "padded-fifo",
		"remoteAddr": "203.0.113.5:443",
		"inPort":     30003, "strategy": " fifo ",
	})
	if code != 0 {
		t.Fatalf("create with padded legal strategy must succeed, code=%d msg=%s", code, msg)
	}
	var stored string
	if err := r.DB().Raw(`SELECT strategy FROM forward WHERE name = 'padded-fifo'`).Scan(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored != "fifo" {
		t.Fatalf("stored strategy = %q, want normalized %q", stored, "fifo")
	}

	// 其余合法值照常。
	for i, s := range []string{"round", "rand", "hash"} {
		code, msg = callForwardCreate(t, h, map[string]interface{}{
			"tunnelId": tunnelID, "name": "ok-" + s,
			"remoteAddr": "203.0.113.5:443",
			"inPort":     30004 + i, "strategy": s,
		})
		if code != 0 {
			t.Fatalf("create with strategy=%s must succeed, code=%d msg=%s", s, code, msg)
		}
	}
}

func callForwardUpdate(t *testing.T, h *Handler, payload map[string]interface{}) (int, string) {
	t.Helper()
	res := httptest.NewRecorder()
	h.forwardUpdate(res, newAuthenticatedJSONRequest(t, payload))
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v body=%s", err, res.Body.String())
	}
	return env.Code, env.Msg
}

func TestR4F4ForwardUpdateStrategyLegalSet(t *testing.T) {
	h, r, tunnelID := setupStrategyLegalFixture(t)

	code, msg := callForwardCreate(t, h, map[string]interface{}{
		"tunnelId": tunnelID, "name": "legacy-bad",
		"remoteAddr": "203.0.113.5:443",
		"inPort":     30007, "strategy": "fifo",
	})
	if code != 0 {
		t.Fatalf("seed create: code=%d msg=%s", code, msg)
	}
	var forwardID int64
	if err := r.DB().Raw(`SELECT id FROM forward WHERE name = 'legacy-bad'`).Scan(&forwardID).Error; err != nil || forwardID == 0 {
		t.Fatalf("seed forward id: id=%d err=%v", forwardID, err)
	}
	// 模拟存量坏值（修复前版本/API 直写落库的形态）。
	if err := r.DB().Exec(`UPDATE forward SET strategy = 'banana' WHERE id = ?`, forwardID).Error; err != nil {
		t.Fatal(err)
	}

	// 合并值口径：只改名也须先补正策略（与零目标/段数帽同口径）。
	code, msg = callForwardUpdate(t, h, map[string]interface{}{
		"id": forwardID, "name": "legacy-bad-renamed",
	})
	if code == 0 {
		t.Fatal("update over a stored illegal strategy must be rejected until corrected")
	}
	if !strings.Contains(msg, "banana") {
		t.Fatalf("rejection must name the illegal strategy, msg=%s", msg)
	}

	// 提交集外新值同样拒绝。
	code, _ = callForwardUpdate(t, h, map[string]interface{}{
		"id": forwardID, "strategy": "banana2",
	})
	if code == 0 {
		t.Fatal("update to an illegal strategy must be rejected")
	}

	// 补正为合法值后放行。
	code, msg = callForwardUpdate(t, h, map[string]interface{}{
		"id": forwardID, "name": "legacy-bad-renamed", "strategy": "hash",
	})
	if code != 0 {
		t.Fatalf("update correcting the strategy must succeed, code=%d msg=%s", code, msg)
	}
}
