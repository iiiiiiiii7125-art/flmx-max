package handler

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// R2-005 / L4-01（2026-11-03 拍板锁死）：转发编辑面（forwardUpdate）
// 不得变更入口端口——落实 2026-10-30 定案「入口段端口创建时严查
// 避开、创建后永不换口」。锁点在 forwardUpdate 的 port 解析之后：
// 显式提交的 inPort 与存量入口端口任一行不一致即拒绝（走既有错误
// 回执形态）；未提交 inPort、提交同值、切隧道沿用存量口照常放行；
// 链式中间/出口段在隧道更新面按既有口径换口不受影响（由
// TestR5FEL9Type1DetachedExitPortLifecycle 等既有用例钉住）。
//
// 夹具复用 r30_fix_f01_test.go 的 setupR30FixF01（单入口节点隧道）。

type fix2TunnelLockResp struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

func (fx *r30FixF01Fixture) callForwardUpdate(t *testing.T, payload map[string]interface{}) fix2TunnelLockResp {
	t.Helper()
	res := httptest.NewRecorder()
	fx.h.forwardUpdate(res, newAuthenticatedJSONRequest(t, payload))
	var out fix2TunnelLockResp
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode forwardUpdate response: %v body=%s", err, res.Body.String())
	}
	return out
}

func (fx *r30FixF01Fixture) storedPort(t *testing.T, forwardID int64) int {
	t.Helper()
	var port int
	if err := fx.r.DB().Raw(`SELECT port FROM forward_port WHERE forward_id = ?`, forwardID).Scan(&port).Error; err != nil {
		t.Fatalf("query forward_port.port: %v", err)
	}
	return port
}

func (fx *r30FixF01Fixture) storedTunnelID(t *testing.T, forwardID int64) int64 {
	t.Helper()
	var tunnelID int64
	if err := fx.r.DB().Raw(`SELECT tunnel_id FROM forward WHERE id = ?`, forwardID).Scan(&tunnelID).Error; err != nil {
		t.Fatalf("query forward.tunnel_id: %v", err)
	}
	return tunnelID
}

func TestFix2R2005ForwardUpdateRejectsEntryPortChange(t *testing.T) {
	fx := setupR30FixF01(t, "198.51.100.77")
	id := fx.createForward(t, "fix2-lock-reject", "198.51.100.77", 30001)

	// 无 inIp 提交（PreservingInIP 路径）：异值 inPort 必须拒绝，
	// 错误文案点名入口端口不可变更且回执后落库端口不变。
	out := fx.callForwardUpdate(t, map[string]interface{}{
		"id":       id,
		"tunnelId": fx.tunnelID,
		"inPort":   30002,
	})
	if out.Code == 0 {
		t.Fatalf("forwardUpdate with changed inPort must be rejected, got success msg=%q", out.Msg)
	}
	if !strings.Contains(out.Msg, "入口端口不可变更") {
		t.Errorf("rejection msg must name entry-port immutability, got %q", out.Msg)
	}
	if got := fx.storedPort(t, id); got != 30001 {
		t.Errorf("stored port after rejection = %d, want 30001", got)
	}

	// 带 inIp 提交（replaceForwardPorts 路径）：同口径拒绝。
	out = fx.callForwardUpdate(t, map[string]interface{}{
		"id":       id,
		"tunnelId": fx.tunnelID,
		"inPort":   30002,
		"inIp":     "198.51.100.77",
	})
	if out.Code == 0 {
		t.Fatalf("forwardUpdate with inIp and changed inPort must be rejected, got success msg=%q", out.Msg)
	}
	if !strings.Contains(out.Msg, "入口端口不可变更") {
		t.Errorf("rejection msg must name entry-port immutability, got %q", out.Msg)
	}
	if got := fx.storedPort(t, id); got != 30001 {
		t.Errorf("stored port after rejection = %d, want 30001", got)
	}
	if got := fx.storedInIP(t, id); got != "198.51.100.77" {
		t.Errorf("stored in_ip after rejection = %q, want %q", got, "198.51.100.77")
	}
}

func TestFix2R2005ForwardUpdateSamePortAccepted(t *testing.T) {
	fx := setupR30FixF01(t, "198.51.100.77")
	id := fx.createForward(t, "fix2-lock-same", "198.51.100.77", 30001)

	// 提交同值 inPort：照常放行（前端编辑表单恒回填现口）。
	out := fx.callForwardUpdate(t, map[string]interface{}{
		"id":       id,
		"tunnelId": fx.tunnelID,
		"inPort":   30001,
		"name":     "fix2-lock-same-renamed",
	})
	if out.Code != 0 {
		t.Fatalf("forwardUpdate with unchanged inPort must succeed: code=%d msg=%s", out.Code, out.Msg)
	}
	if got := fx.storedPort(t, id); got != 30001 {
		t.Errorf("stored port = %d, want 30001", got)
	}

	// 未提交 inPort：回填存量口，照常放行。
	out = fx.callForwardUpdate(t, map[string]interface{}{
		"id":   id,
		"name": "fix2-lock-same-renamed-again",
	})
	if out.Code != 0 {
		t.Fatalf("forwardUpdate without inPort must succeed: code=%d msg=%s", out.Code, out.Msg)
	}
	if got := fx.storedPort(t, id); got != 30001 {
		t.Errorf("stored port = %d, want 30001", got)
	}
}

func TestFix2R2005ForwardUpdateTunnelSwitchPortRules(t *testing.T) {
	fx := setupR30FixF01(t, "198.51.100.77")
	id := fx.createForward(t, "fix2-lock-switch", "198.51.100.77", 30001)

	otherTunnelID := seedTunnelForNftables(t, fx.h, "fix2-lock-tunnel-b", fx.nodeID)
	if err := fx.r.DB().Exec(`
		INSERT INTO user_tunnel(user_id, tunnel_id, speed_id, num, flow, in_flow, out_flow, flow_reset_time, exp_time, status)
		VALUES(1, ?, NULL, 99999, 99999, 0, 0, 1, 2727251700000, 1)
	`, otherTunnelID).Error; err != nil {
		t.Fatalf("seed user_tunnel for tunnel B: %v", err)
	}

	// 切隧道同时显式改口：同一锁拒绝，转发仍留在原隧道原口。
	out := fx.callForwardUpdate(t, map[string]interface{}{
		"id":       id,
		"tunnelId": otherTunnelID,
		"inPort":   30002,
	})
	if out.Code == 0 {
		t.Fatalf("tunnel switch with changed inPort must be rejected, got success msg=%q", out.Msg)
	}
	if !strings.Contains(out.Msg, "入口端口不可变更") {
		t.Errorf("rejection msg must name entry-port immutability, got %q", out.Msg)
	}
	if got := fx.storedTunnelID(t, id); got != fx.tunnelID {
		t.Errorf("forward tunnel after rejection = %d, want original %d", got, fx.tunnelID)
	}
	if got := fx.storedPort(t, id); got != 30001 {
		t.Errorf("stored port after rejection = %d, want 30001", got)
	}

	// 切隧道不提交 inPort：沿用存量口放行（既有换隧道口径不回归）。
	out = fx.callForwardUpdate(t, map[string]interface{}{
		"id":       id,
		"tunnelId": otherTunnelID,
	})
	if out.Code != 0 {
		t.Fatalf("tunnel switch without inPort must succeed: code=%d msg=%s", out.Code, out.Msg)
	}
	if got := fx.storedTunnelID(t, id); got != otherTunnelID {
		t.Errorf("forward tunnel after switch = %d, want %d", got, otherTunnelID)
	}
	if got := fx.storedPort(t, id); got != 30001 {
		t.Errorf("stored port after switch = %d, want 30001 (entry port follows the forward)", got)
	}
}
