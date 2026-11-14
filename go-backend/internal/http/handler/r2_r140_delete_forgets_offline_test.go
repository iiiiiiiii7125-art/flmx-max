package handler

import (
	"strings"
	"testing"
	"time"
)

// R-140：删节点不取消待发离线定时器，节点 id 复用后误发幽灵“节点离线”告警。
// 根因：forgetNode 零调用点。修复：全部删节点入口的汇聚漏斗
// deleteNodeByIDAfterGuardsOutcome 在级联删行成功后调 forgetNode。
// 本文件两面验证：(a) 状态面——删节点后待发定时器与告警态已清；
// (b) 行为面——同一 id 复用建新节点后无幽灵告警，且对照节点的
// 正常离线告警仍能发出（不许修成静默）。

func insertR140Node(t *testing.T, h *Handler, id int64, name string) {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := h.repo.DB().Exec(`
		INSERT INTO node(id, name, secret, server_ip, server_ip_v4, server_ip_v6, port, interface_name, version, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx)
		VALUES(?, ?, ?, '10.0.0.7', '10.0.0.7', '', '40000-40010', '', 'v1', 1, 1, 1, ?, ?, 1, '[::]', '[::]', 0)
	`, id, name, name+"-secret", now, now).Error; err != nil {
		t.Fatalf("insert node %d: %v", id, err)
	}
}

func TestR140DeleteNodeCancelsPendingOfflineTimer(t *testing.T) {
	h, _, _ := setupNotifyHandler(t)
	insertR140Node(t, h, 7, "r2-a-r140-del")

	// 待发离线定时器 + 各类告警态全部预置，模拟“掉线宽限期内删节点”。
	h.notifyNodeOffline(7)
	h.notifier.mu.Lock()
	h.notifier.offlineAlerted[7] = true
	h.notifier.recoveryPending[7] = true
	h.notifier.expiryAlerted[7] = time.Now().UnixMilli()
	h.notifier.mu.Unlock()

	if _, _, err := h.deleteNodeByID(7); err != nil {
		t.Fatalf("deleteNodeByID: %v", err)
	}
	h.notifier.mu.Lock()
	defer h.notifier.mu.Unlock()
	if _, pending := h.notifier.offlineTimers[7]; pending {
		t.Fatalf("R-140: pending offline timer survived node delete")
	}
	if _, pending := h.notifier.recoveryTimers[7]; pending {
		t.Fatalf("R-140: pending recovery timer survived node delete")
	}
	if h.notifier.offlineAlerted[7] || h.notifier.recoveryPending[7] {
		t.Fatalf("R-140: alert state survived node delete (alerted=%v pending=%v)",
			h.notifier.offlineAlerted[7], h.notifier.recoveryPending[7])
	}
	if _, ok := h.notifier.expiryAlerted[7]; ok {
		t.Fatalf("R-140: expiry alert state survived node delete")
	}
}

func TestR140DeleteNodeNoGhostAlertOnIDReuse(t *testing.T) {
	old := nodeOfflineGrace
	nodeOfflineGrace = 100 * time.Millisecond
	t.Cleanup(func() { nodeOfflineGrace = old })
	h, _, sent := setupNotifyHandler(t)
	insertR140Node(t, h, 7, "r2-a-r140-del")
	insertR140Node(t, h, 8, "r2-a-r140-ctl")

	// 两节点同时掉线、定时器同时待发；删 7 并立即以同一 id 7 重建。
	h.notifyNodeOffline(7)
	h.notifyNodeOffline(8)
	if _, _, err := h.deleteNodeByID(7); err != nil {
		t.Fatalf("deleteNodeByID: %v", err)
	}
	insertR140Node(t, h, 7, "r2-a-r140-reuse")

	msgs := sent()
	time.Sleep(400 * time.Millisecond)
	msgs = sent()
	if len(msgs) != 1 {
		t.Fatalf("expected exactly 1 offline alert (control node), got %d: %v", len(msgs), msgs)
	}
	if msgs[0].Title != "节点掉线" || msgs[0].Event != notifyEventNodeOffline {
		t.Fatalf("unexpected alert: %v", msgs[0])
	}
	if !strings.Contains(msgs[0].Message, "r2-a-r140-ctl") {
		t.Fatalf("the one alert must be the control node, got: %v", msgs[0])
	}
	if strings.Contains(msgs[0].Message, "r2-a-r140-reuse") {
		t.Fatalf("R-140: ghost offline alert for reused id: %v", msgs[0])
	}
}

// TestR140StartupDispatchStopsPendingTimer 复现 V1b 真机定案的交错：
// 面板重启后 90s startup 检查（runNotifyLoop 等一个宽限期后调
// checkStartupOfflineNodes）直接派发告警——不经过定时器回调。
// dispatchNodeOfflineAlert 只从 map 删定时器、不 Stop，待发定时器
// 成孤儿；随后删节点（forgetNode 查 map 无定时器可停、却清掉了
// offlineAlerted），孤儿定时器到点按复用节点名发幽灵告警。
// 未修复时本测试必须红（3 条告警、含复用名幽灵告警）；修复后必须
// 绿（2 条：启动检查派发的原节点名＋对照节点，复用名 0 条）。
// 注意：测试里直接调 dispatchNodeOfflineAlert 模拟 startup 检查，
// 因为 checkStartupOfflineNodes 在 wsServer 为 nil 的测试 handler
// 里会直接返回（其入口守卫要求 wsServer 非空）。
func TestR140StartupDispatchStopsPendingTimer(t *testing.T) {
	old := nodeOfflineGrace
	nodeOfflineGrace = 100 * time.Millisecond
	t.Cleanup(func() { nodeOfflineGrace = old })
	h, _, sent := setupNotifyHandler(t)
	insertR140Node(t, h, 7, "r2-a-r140-orig")
	insertR140Node(t, h, 8, "r2-a-r140-ctl")

	// 两节点同时掉线、定时器同时待发（宽限期内）。
	h.notifyNodeOffline(7)
	h.notifyNodeOffline(8)
	// 模拟 startup 检查直接派发 7 的告警（定时器回调尚未触发）。
	h.dispatchNodeOfflineAlert(7, "节点掉线（面板重启后仍离线）")

	// 删 7 并立即以同一 id 7 重建（新名）。
	if _, _, err := h.deleteNodeByID(7); err != nil {
		t.Fatalf("deleteNodeByID: %v", err)
	}
	insertR140Node(t, h, 7, "r2-a-r140-reuse")

	// 等过宽限期：孤儿定时器若存活会在此窗口触发幽灵告警。
	time.Sleep(400 * time.Millisecond)
	msgs := sent()
	if len(msgs) != 2 {
		t.Fatalf("expected exactly 2 offline alerts (startup dispatch + control), got %d: %v", len(msgs), msgs)
	}
	var sawOrig, sawCtl bool
	for _, m := range msgs {
		if strings.Contains(m.Message, "r2-a-r140-reuse") {
			t.Fatalf("R-140: ghost offline alert for reused id: %v", msgs)
		}
		if strings.Contains(m.Message, "r2-a-r140-orig") {
			sawOrig = true
		}
		if strings.Contains(m.Message, "r2-a-r140-ctl") {
			sawCtl = true
		}
	}
	if !sawOrig || !sawCtl {
		t.Fatalf("expected startup-dispatch alert for orig node and timer alert for control node, got: %v", msgs)
	}
}
