package handler

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// PortAlloc Fix：自动端口分配盲区（P1-01–P1-07）回归测试。

// Fix C：带已试端口排除的选口——排除集内的口不得再被挑中；
// 排除整段后必须明确报错（无公共可用端口），不得空转。
func TestPickTunnelPortExcludingHonorsTriedPorts(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/portalloc-pick-excl.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	nodeID := seedRelayTargetNode(t, r, "entry", "198.51.100.11")
	if err := r.DB().Exec(`UPDATE node SET port = ? WHERE id = ?`, "36005-36007", nodeID).Error; err != nil {
		t.Fatal(err)
	}
	tunnelID := seedTunnelForNftables(t, h, "pick-excl-tunnel", nodeID)

	picked, err := h.pickTunnelPortExcluding(tunnelID, nil)
	if err != nil {
		t.Fatalf("pick without exclusions: %v", err)
	}
	if picked < 36005 || picked > 36007 {
		t.Fatalf("picked = %d, want within 36005-36007", picked)
	}

	if _, err := h.pickTunnelPortExcluding(tunnelID, map[int]bool{36005: true, 36006: true, 36007: true}); err == nil {
		t.Fatal("excluding the whole range must fail, got no error")
	} else if !strings.Contains(err.Error(), "无公共可用端口") {
		t.Fatalf("exhausted pick error = %q, want 公共可用端口", err.Error())
	}

	picked, err = h.pickTunnelPortExcluding(tunnelID, map[int]bool{36005: true, 36006: true})
	if err != nil {
		t.Fatalf("pick excluding 36005-36006: %v", err)
	}
	if picked != 36007 {
		t.Fatalf("picked = %d, want 36007 (only remaining port)", picked)
	}

	// DB 占用与排除集叠加：36005 被转发占、36007 被排除 → 只剩 36006。
	if err := r.DB().Create(&model.ForwardPort{ForwardID: 900, NodeID: nodeID, Port: 36005}).Error; err != nil {
		t.Fatal(err)
	}
	picked, err = h.pickTunnelPortExcluding(tunnelID, map[int]bool{36007: true})
	if err != nil {
		t.Fatalf("pick with DB-occupied 36005 + excluded 36007: %v", err)
	}
	if picked != 36006 {
		t.Fatalf("picked = %d, want 36006", picked)
	}
}

// Fix D：隧道入口 inPort 在创建/更新时须过 DB 三检（范围＋规格＋
// 跨表占用，与段口显式口径一致）；更新时本隧道名下转发的现口须排除。
func TestTunnelEntryInPortDBCheck(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/portalloc-tunnel-inport.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now().UnixMilli()

	nodeID := seedRelayTargetNode(t, r, "entry", "198.51.100.11")
	exitID := seedRelayTargetNode(t, r, "exit", "198.51.100.12")
	if err := r.DB().Exec(`UPDATE node SET port = ?, status = 1 WHERE id IN (?, ?)`, "36005-36007", nodeID, exitID).Error; err != nil {
		t.Fatal(err)
	}
	// 隧道 700 名下转发 900 占 36005。
	if err := r.DB().Exec(`INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, inx)
		VALUES(700, 'tun', 1.0, 2, 'tls', 2, ?, ?, 1, 0)`, now, now).Error; err != nil {
		t.Fatalf("insert tunnel: %v", err)
	}
	if err := r.DB().Exec(`INSERT INTO forward(id, user_id, user_name, name, tunnel_id, remote_addr, strategy, in_flow, out_flow, created_time, updated_time, status, inx)
		VALUES(900, 1, 'admin', 'f', 700, '203.0.113.5:443', 'fifo', 0, 0, ?, ?, 1, 0)`, now, now).Error; err != nil {
		t.Fatalf("insert forward: %v", err)
	}
	if err := r.DB().Exec(`INSERT INTO forward_port(forward_id, node_id, port) VALUES(900, ?, 36005)`, nodeID).Error; err != nil {
		t.Fatalf("insert forward_port: %v", err)
	}

	req := func(inPort int) map[string]interface{} {
		m := map[string]interface{}{
			"inNodeId":  []interface{}{map[string]interface{}{"nodeId": nodeID, "strategy": "fifo"}},
			"outNodeId": []interface{}{map[string]interface{}{"nodeId": exitID, "port": 36007, "strategy": "fifo"}},
		}
		if inPort > 0 {
			m["inPort"] = inPort
		}
		return m
	}

	tx := h.repo.BeginTx()
	if tx == nil || tx.Error != nil {
		t.Fatalf("begin tx: %v", tx.Error)
	}
	defer func() { _ = tx.Rollback().Error }()

	// 创建：inPort 撞本节点转发占用的 36005 → 拒绝（须点名占用）。
	if _, err := h.prepareTunnelCreateState(tx, req(36005), 2, 0); err == nil {
		t.Fatal("create with inPort colliding with a forward port must be rejected")
	} else if !strings.Contains(err.Error(), "36005") || !strings.Contains(err.Error(), "占用") {
		t.Fatalf("collision error = %q, want port 36005 named as occupied", err.Error())
	}
	// 创建：inPort 36006 空闲 → 通过。
	if _, err := h.prepareTunnelCreateState(tx, req(36006), 2, 0); err != nil {
		t.Fatalf("create with free inPort must pass, got %v", err)
	}
	// 创建：不提交 inPort → 不校验、通过。
	if _, err := h.prepareTunnelCreateState(tx, req(0), 2, 0); err != nil {
		t.Fatalf("create without inPort must pass, got %v", err)
	}
	// 创建：inPort 越界 → 拒绝。
	if _, err := h.prepareTunnelCreateState(tx, req(70000), 2, 0); err == nil {
		t.Fatal("create with out-of-range inPort must be rejected")
	}
	// 更新：inPort 就是本隧道名下转发的现口 → 排除后通过。
	if _, err := h.prepareTunnelCreateState(tx, req(36005), 2, 700); err != nil {
		t.Fatalf("update with own forward's inPort must pass, got %v", err)
	}
	// 更新：inPort 撞他隧道转发的口 → 拒绝。另建隧道 701 的转发占 36006。
	// （经本 tx 写入：tx 已占住 SQLite 唯一连接，r.DB() 会等连接饿死。）
	if err := tx.Exec(`INSERT INTO forward(id, user_id, user_name, name, tunnel_id, remote_addr, strategy, in_flow, out_flow, created_time, updated_time, status, inx)
		VALUES(901, 1, 'admin', 'f2', 701, '203.0.113.5:443', 'fifo', 0, 0, ?, ?, 1, 0)`, now, now).Error; err != nil {
		t.Fatalf("insert forward 901: %v", err)
	}
	if err := tx.Exec(`INSERT INTO forward_port(forward_id, node_id, port) VALUES(901, ?, 36006)`, nodeID).Error; err != nil {
		t.Fatalf("insert forward_port 901: %v", err)
	}
	if _, err := h.prepareTunnelCreateState(tx, req(36006), 2, 700); err == nil {
		t.Fatal("update with inPort colliding with another tunnel's forward must be rejected")
	}
}

// Fix B+C：转发自动创建撞上 live 监听口时须换口重试、最终成功。
// 假 agent 首探上报所探口在监听、之后上报空闲；创建必须成功、
// 探针至少跑两次（重试真实发生）、落库口不是首探命中的口。
func TestForwardCreateRetriesOnLivePortCollision(t *testing.T) {
	a := newNodeSwapFakeAgent(t)
	if err := a.h.repo.DB().Exec(`UPDATE node SET port = ? WHERE id = ?`, "36005-36007", 1).Error; err != nil {
		t.Fatal(err)
	}
	tunnelID := seedTunnelForNftables(t, a.h, "retry-tunnel", 1)
	if err := a.h.repo.DB().Exec(`
		INSERT INTO user_tunnel(user_id, tunnel_id, speed_id, num, flow, in_flow, out_flow, flow_reset_time, exp_time, status)
		VALUES(1, ?, NULL, 99999, 99999, 0, 0, 1, 2727251700000, 1)
	`, tunnelID).Error; err != nil {
		t.Fatalf("seed user_tunnel: %v", err)
	}

	var mu sync.Mutex
	probeCalls := 0
	firstProbed := 0
	a.reply = func(command cleanupAgentCommand) (bool, string, map[string]interface{}) {
		if command.Type == "CheckPortListening" {
			mu.Lock()
			probeCalls++
			n := probeCalls
			mu.Unlock()
			var body struct {
				Ranges []struct {
					Lo int `json:"lo"`
					Hi int `json:"hi"`
				} `json:"ranges"`
			}
			_ = json.Unmarshal(command.Data, &body)
			probed := 0
			if len(body.Ranges) > 0 {
				probed = body.Ranges[0].Lo
			}
			if n == 1 {
				mu.Lock()
				firstProbed = probed
				mu.Unlock()
				return true, "", map[string]interface{}{"listening": []interface{}{probed}}
			}
			return true, "", map[string]interface{}{"listening": []interface{}{}}
		}
		return true, "", nil
	}

	req := newAuthenticatedJSONRequest(t, map[string]interface{}{
		"tunnelId":   tunnelID,
		"name":       "retry-fwd",
		"remoteAddr": "203.0.113.5:443",
	})
	res := httptest.NewRecorder()
	a.h.forwardCreate(res, req)

	var payload struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v body=%s", err, res.Body.String())
	}
	if payload.Code != 0 {
		t.Fatalf("create failed: code=%d msg=%s body=%s", payload.Code, payload.Msg, res.Body.String())
	}
	if got := len(a.commandsOfType("CheckPortListening")); got < 2 {
		t.Fatalf("live probe must run at least twice (retry), got %d CheckPortListening commands", got)
	}
	var port int
	if err := a.h.repo.DB().Raw(`SELECT port FROM forward_port WHERE node_id = 1 AND forward_id = (SELECT id FROM forward WHERE name = 'retry-fwd')`).Scan(&port).Error; err != nil {
		t.Fatalf("read created port: %v", err)
	}
	if port < 36005 || port > 36007 {
		t.Fatalf("created port = %d, want within 36005-36007", port)
	}
	mu.Lock()
	defer mu.Unlock()
	if firstProbed != 0 && port == firstProbed {
		t.Fatalf("created port %d equals the first live-occupied probe target %d: retry did not switch ports", port, firstProbed)
	}
}
