package handler

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// C-F3-01（LEDGER3，中）：缺席 inPort 时 forwardUpdate 三分支、批量换
// 隧道、删隧道迁移三面均以 MIN(port) 单值重建 forward_port 全行集——
// 异口转发（同一 forward 在不同入口节点的行 port 不一致，产生面：
// 隧道新增入口节点时规范口超新节点规格随机落口；备份导入逐行保真）
// 的非 min 入口端口被一次无关编辑静默改口，违 2026-10-30「入口端口
// 创建后永不换口」定案。次生症状：min 口在非 min 节点上越规格或被
// 其他转发占用时，异口转发的任意编辑（含仅改名）被冲突拒绝且无自
// 愈路径。钉死口径：保留节点逐节点沿用该节点现口，规范口（MIN）
// 只用于真正新增的节点行；占用校验按逐节点现口走。

type fix3ForwardPortsFixture struct {
	h *Handler
	r *repo.Repository
}

func setupFix3ForwardPorts(t *testing.T) *fix3ForwardPortsFixture {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/fix3-forwardports.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return &fix3ForwardPortsFixture{h: New(r, "test-secret"), r: r}
}

func (fx *fix3ForwardPortsFixture) addNode(t *testing.T, name, ip, portRange string) int64 {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := fx.r.DB().Exec(`
		INSERT INTO node(name, secret, server_ip, server_ip_v4, server_ip_v6, port, interface_name, version, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx)
		VALUES(?, ?, ?, ?, '', ?, '', 'v1', 0, 0, 0, ?, ?, 0, '[::]', '[::]', 0)`, name, name+"-secret", ip, ip, portRange, now, now).Error; err != nil {
		t.Fatalf("seed node %s: %v", name, err)
	}
	var id int64
	if err := fx.r.DB().Raw(`SELECT id FROM node WHERE name = ?`, name).Scan(&id).Error; err != nil {
		t.Fatalf("lookup node %s: %v", name, err)
	}
	return id
}

func (fx *fix3ForwardPortsFixture) addTunnel(t *testing.T, name string, entryNodeIDs ...int64) int64 {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := fx.r.DB().Exec(`
		INSERT INTO tunnel(name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, in_ip, inx)
		VALUES(?, 1.0, 2, 'tcp', 99999, ?, ?, 1, NULL, 0)`, name, now, now).Error; err != nil {
		t.Fatalf("seed tunnel %s: %v", name, err)
	}
	var id int64
	if err := fx.r.DB().Raw(`SELECT id FROM tunnel WHERE name = ?`, name).Scan(&id).Error; err != nil {
		t.Fatalf("lookup tunnel %s: %v", name, err)
	}
	for i, nodeID := range entryNodeIDs {
		if err := fx.r.DB().Exec(`INSERT INTO chain_tunnel(tunnel_id, chain_type, node_id, port, strategy, inx, protocol) VALUES(?, 1, ?, NULL, 'round', ?, 'tcp')`, id, nodeID, i+1).Error; err != nil {
			t.Fatalf("seed chain_tunnel for %s: %v", name, err)
		}
	}
	if err := fx.r.DB().Exec(`
		INSERT INTO user_tunnel(user_id, tunnel_id, speed_id, num, flow, in_flow, out_flow, flow_reset_time, exp_time, status)
		VALUES(1, ?, NULL, 99999, 99999, 0, 0, 1, 2727251700000, 1)`, id).Error; err != nil {
		t.Fatalf("seed user_tunnel for %s: %v", name, err)
	}
	return id
}

func (fx *fix3ForwardPortsFixture) addForward(t *testing.T, name string, tunnelID int64, portsByNode map[int64]int) int64 {
	t.Helper()
	now := time.Now().UnixMilli()
	fwd := model.Forward{UserID: 1, UserName: "admin", Name: name, TunnelID: tunnelID, RemoteAddr: "203.0.113.5:443", CreatedTime: now, UpdatedTime: now, Status: 1}
	if err := fx.r.DB().Create(&fwd).Error; err != nil {
		t.Fatalf("seed forward %s: %v", name, err)
	}
	for nodeID, port := range portsByNode {
		if err := fx.r.DB().Create(&model.ForwardPort{ForwardID: fwd.ID, NodeID: nodeID, Port: port}).Error; err != nil {
			t.Fatalf("seed forward_port %s node=%d: %v", name, nodeID, err)
		}
	}
	return fwd.ID
}

func (fx *fix3ForwardPortsFixture) portsOf(t *testing.T, forwardID int64) map[int64]int {
	t.Helper()
	rows, err := fx.h.listForwardPorts(forwardID)
	if err != nil {
		t.Fatalf("list forward ports: %v", err)
	}
	out := make(map[int64]int, len(rows))
	for _, row := range rows {
		out[row.NodeID] = row.Port
	}
	return out
}

func (fx *fix3ForwardPortsFixture) callForwardUpdate(t *testing.T, payload map[string]interface{}) (int, string) {
	t.Helper()
	res := httptest.NewRecorder()
	fx.h.forwardUpdate(res, newAuthenticatedJSONRequest(t, payload))
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode forwardUpdate response: %v body=%s", err, res.Body.String())
	}
	return out.Code, out.Msg
}

func assertPortsEqual(t *testing.T, got, want map[int64]int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("forward ports = %v, want %v", got, want)
	}
	for nodeID, port := range want {
		if got[nodeID] != port {
			t.Fatalf("forward ports = %v, want %v (node %d must keep its own port)", got, want, nodeID)
		}
	}
}

// 异口转发仅改名：B 的规格（30000-40000）不含规范口 15000，旧实现先
// 在逐节点校验处以单值口拒绝（次生症状），修后按逐节点现口校验、
// 落库逐行不变。
func TestFix3CF301ForwardUpdateRenameKeepsPerNodePorts(t *testing.T) {
	fx := setupFix3ForwardPorts(t)
	nodeA := fx.addNode(t, "cf301-a", "198.51.100.11", "10000-20000")
	nodeB := fx.addNode(t, "cf301-b", "198.51.100.12", "30000-40000")
	tunnelID := fx.addTunnel(t, "cf301-t1", nodeA, nodeB)
	id := fx.addForward(t, "cf301-hetero", tunnelID, map[int64]int{nodeA: 15000, nodeB: 35000})

	code, msg := fx.callForwardUpdate(t, map[string]interface{}{"id": id, "name": "cf301-hetero-renamed"})
	if code != 0 {
		t.Fatalf("rename-only update of heterogeneous forward must succeed, got code=%d msg=%s", code, msg)
	}
	assertPortsEqual(t, fx.portsOf(t, id), map[int64]int{nodeA: 15000, nodeB: 35000})
}

// 写面钉死：两节点规格都含规范口时旧实现校验能过、但落库把 B 行
// 静默改写为 MIN 口。本格与校验无关，专钉 replace 写面逐节点保口。
func TestFix3CF301ForwardUpdateRenameDoesNotRewriteWideRangeNode(t *testing.T) {
	fx := setupFix3ForwardPorts(t)
	nodeA := fx.addNode(t, "cf301w-a", "198.51.100.21", "10000-20000")
	nodeB := fx.addNode(t, "cf301w-b", "198.51.100.22", "10000-40000")
	tunnelID := fx.addTunnel(t, "cf301w-t1", nodeA, nodeB)
	id := fx.addForward(t, "cf301w-hetero", tunnelID, map[int64]int{nodeA: 15000, nodeB: 35000})

	code, msg := fx.callForwardUpdate(t, map[string]interface{}{"id": id, "name": "cf301w-hetero-renamed"})
	if code != 0 {
		t.Fatalf("rename-only update must succeed, got code=%d msg=%s", code, msg)
	}
	assertPortsEqual(t, fx.portsOf(t, id), map[int64]int{nodeA: 15000, nodeB: 35000})
}

// hasInIP 分支（replaceForwardPorts 路径）同口径逐节点保口。
func TestFix3CF301ForwardUpdateWithInIPKeyKeepsPerNodePorts(t *testing.T) {
	fx := setupFix3ForwardPorts(t)
	nodeA := fx.addNode(t, "cf301i-a", "198.51.100.31", "10000-20000")
	nodeB := fx.addNode(t, "cf301i-b", "198.51.100.32", "10000-40000")
	tunnelID := fx.addTunnel(t, "cf301i-t1", nodeA, nodeB)
	id := fx.addForward(t, "cf301i-hetero", tunnelID, map[int64]int{nodeA: 15000, nodeB: 35000})

	code, msg := fx.callForwardUpdate(t, map[string]interface{}{"id": id, "name": "cf301i-renamed", "inIp": ""})
	if code != 0 {
		t.Fatalf("update with inIp key must succeed, got code=%d msg=%s", code, msg)
	}
	assertPortsEqual(t, fx.portsOf(t, id), map[int64]int{nodeA: 15000, nodeB: 35000})
}

// 切隧道分支：保留节点（同属新旧隧道入口集）逐节点沿用现口，真正
// 新增的节点行才用规范口。
func TestFix3CF301ForwardUpdateTunnelSwitchKeepsKeptNodePorts(t *testing.T) {
	fx := setupFix3ForwardPorts(t)
	nodeA := fx.addNode(t, "cf301s-a", "198.51.100.41", "10000-20000")
	nodeB := fx.addNode(t, "cf301s-b", "198.51.100.42", "30000-40000")
	nodeC := fx.addNode(t, "cf301s-c", "198.51.100.43", "10000-20000")
	tunnelID := fx.addTunnel(t, "cf301s-t1", nodeA, nodeB)
	targetID := fx.addTunnel(t, "cf301s-t2", nodeA, nodeB, nodeC)
	id := fx.addForward(t, "cf301s-hetero", tunnelID, map[int64]int{nodeA: 15000, nodeB: 35000})

	code, msg := fx.callForwardUpdate(t, map[string]interface{}{"id": id, "tunnelId": targetID, "name": "cf301s-switched"})
	if code != 0 {
		t.Fatalf("tunnel switch must succeed, got code=%d msg=%s", code, msg)
	}
	assertPortsEqual(t, fx.portsOf(t, id), map[int64]int{nodeA: 15000, nodeB: 35000, nodeC: 15000})
}

// 次生症状（占用格）：规范口 15000 在非 min 节点 B 上被另一转发
// 占用（存量行），旧实现以单值口对 B 验占用 → 异口转发任意编辑被
// 冲突拒绝；修后按 B 的现口 35000 验占用，编辑放行、各行不变。
// B 的规格含 15000，使旧实现能走到占用校验一环（与范围格分开钉）。
func TestFix3CF301ForwardUpdateOccupiedMinPortOnOtherNodeDoesNotBlock(t *testing.T) {
	fx := setupFix3ForwardPorts(t)
	nodeA := fx.addNode(t, "cf301o-a", "198.51.100.51", "10000-20000")
	nodeB := fx.addNode(t, "cf301o-b", "198.51.100.52", "10000-40000")
	tunnelID := fx.addTunnel(t, "cf301o-t1", nodeA, nodeB)
	otherTunnel := fx.addTunnel(t, "cf301o-t3", nodeB)
	id := fx.addForward(t, "cf301o-hetero", tunnelID, map[int64]int{nodeA: 15000, nodeB: 35000})
	_ = fx.addForward(t, "cf301o-blocker", otherTunnel, map[int64]int{nodeB: 15000})

	code, msg := fx.callForwardUpdate(t, map[string]interface{}{"id": id, "name": "cf301o-renamed"})
	if code != 0 {
		t.Fatalf("rename must not be blocked by another forward holding the canonical port on node B, got code=%d msg=%s", code, msg)
	}
	assertPortsEqual(t, fx.portsOf(t, id), map[int64]int{nodeA: 15000, nodeB: 35000})
}

// 批量换隧道：保留节点逐节点保口，新增节点用规范口。
func TestFix3CF301BatchChangeTunnelKeepsKeptNodePorts(t *testing.T) {
	fx := setupFix3ForwardPorts(t)
	nodeA := fx.addNode(t, "cf301b-a", "198.51.100.61", "10000-20000")
	nodeB := fx.addNode(t, "cf301b-b", "198.51.100.62", "30000-40000")
	nodeC := fx.addNode(t, "cf301b-c", "198.51.100.63", "10000-20000")
	tunnelID := fx.addTunnel(t, "cf301b-t1", nodeA, nodeB)
	targetID := fx.addTunnel(t, "cf301b-t2", nodeA, nodeB, nodeC)
	id := fx.addForward(t, "cf301b-hetero", tunnelID, map[int64]int{nodeA: 15000, nodeB: 35000})

	res := httptest.NewRecorder()
	fx.h.forwardBatchChangeTunnel(res, newAuthenticatedJSONRequest(t, map[string]interface{}{
		"forwardIds":     []int64{id},
		"targetTunnelId": targetID,
	}))
	var out struct {
		Code int `json:"code"`
		Data struct {
			SuccessCount int `json:"successCount"`
			FailCount    int `json:"failCount"`
			Failures     []struct {
				Reason string `json:"reason"`
			} `json:"failures"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode batch change tunnel response: %v body=%s", err, res.Body.String())
	}
	if out.Code != 0 || out.Data.SuccessCount != 1 || out.Data.FailCount != 0 {
		t.Fatalf("batch change tunnel = %+v body=%s, want one success", out, res.Body.String())
	}
	assertPortsEqual(t, fx.portsOf(t, id), map[int64]int{nodeA: 15000, nodeB: 35000, nodeC: 15000})
}

// 删隧道迁移：保留节点逐节点保口，新增节点用规范口（迁移计划的
// targetPort 只作新行口）。
func TestFix3CF301TunnelDeleteMigrationKeepsKeptNodePorts(t *testing.T) {
	fx := setupFix3ForwardPorts(t)
	nodeA := fx.addNode(t, "cf301m-a", "198.51.100.71", "10000-20000")
	nodeB := fx.addNode(t, "cf301m-b", "198.51.100.72", "30000-40000")
	nodeC := fx.addNode(t, "cf301m-c", "198.51.100.73", "10000-20000")
	sourceID := fx.addTunnel(t, "cf301m-src", nodeA, nodeB)
	targetID := fx.addTunnel(t, "cf301m-dst", nodeB, nodeC)
	id := fx.addForward(t, "cf301m-hetero", sourceID, map[int64]int{nodeA: 15000, nodeB: 35000})

	plans, failures, err := fx.h.planTunnelDeleteForwardMigrations(sourceID, targetID)
	if err != nil {
		t.Fatalf("plan migrations: %v", err)
	}
	if len(failures) != 0 || len(plans) != 1 {
		t.Fatalf("plans=%d failures=%v, want exactly one plan and no failures", len(plans), failures)
	}
	if _, execErr, failDetail := fx.h.executeTunnelDeleteForwardMigrations(plans); execErr != nil {
		t.Fatalf("execute migrations: %v (%+v)", execErr, failDetail)
	}
	assertPortsEqual(t, fx.portsOf(t, id), map[int64]int{nodeB: 35000, nodeC: 15000})
	fwd, err := fx.h.getForwardRecord(id)
	if err != nil {
		t.Fatalf("get forward: %v", err)
	}
	if fwd.TunnelID != targetID {
		t.Fatalf("forward tunnel = %d, want migrated target %d", fwd.TunnelID, targetID)
	}
}

// entries 构造器层钉死：PreservingInIP 路径的端口同样逐节点沿用
// 现口，无存量行的节点才用传入的规范口；InIP 保留口径不变。
func TestFix3CF301BuildEntriesPerNodePorts(t *testing.T) {
	entryNodeIDs := []int64{10, 20, 30}
	oldPorts := []forwardPortRecord{
		{NodeID: 10, Port: 10001, InIP: "10.0.0.10"},
		{NodeID: 20, Port: 10003, InIP: "10.0.0.20"},
	}
	entries := buildForwardPortEntriesWithPreservedInIP(entryNodeIDs, oldPorts, 18080)
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	if entries[0].NodeID != 10 || entries[0].Port != 10001 || entries[0].InIP != "10.0.0.10" {
		t.Fatalf("unexpected first entry: %+v (existing node must keep its own port)", entries[0])
	}
	if entries[1].NodeID != 20 || entries[1].Port != 10003 || entries[1].InIP != "10.0.0.20" {
		t.Fatalf("unexpected second entry: %+v (existing node must keep its own port)", entries[1])
	}
	if entries[2].NodeID != 30 || entries[2].Port != 18080 || entries[2].InIP != "" {
		t.Fatalf("unexpected third entry: %+v (new node takes the canonical port)", entries[2])
	}
}
