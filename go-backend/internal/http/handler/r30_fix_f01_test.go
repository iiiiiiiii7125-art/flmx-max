package handler

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"go-backend/internal/store/repo"
)

// R30-L1-E01 / R30-FIX-F01（产品 P2）：forwardCreate / forwardUpdate 的
// inIp intake 对含 :port 的值原值落库，inPort 被静默丢弃——下发层
// buildForwardServiceConfigs / realmListenAddr 按“端口胜出”把服务
// 监听在嵌入端口上。立案对象严格限定为 intake/下拉缝；下发层
// “端口胜出”分支由既有测试钉死（control_plane_test.go:455、
// 470-530），一字不动。
//
// 探针按 L1 红日志（R30-L1-e01-probe-red.log）逐格重建：节点显式
// serverIpV4="198.51.100.77:8443"、inPort 30001；CELL-B 落库格、
// CELL-A ×2 下发格、CELL-R realm 格 + 裸值对照格。修复前 CELL-B/A/R
// 均 FAIL、对照 PASS；修复后全部 PASS。另覆 forwardUpdate 同型
// intake（hasInIP 分支）与 extraIPs 向量（取自 extraIPs 的含端口
// 值作为 inIp 提交时同样被剥离）。

type r30FixF01Fixture struct {
	h        *Handler
	r        *repo.Repository
	nodeID   int64
	tunnelID int64
}

func setupR30FixF01(t *testing.T, nodeIP string) *r30FixF01Fixture {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/r30-f01.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	nodeID := seedRelayTargetNode(t, r, "entry", nodeIP)
	tunnelID := seedTunnelForNftables(t, h, "r30-f01-tunnel", nodeID)
	if err := r.DB().Exec(`
		INSERT INTO user_tunnel(user_id, tunnel_id, speed_id, num, flow, in_flow, out_flow, flow_reset_time, exp_time, status)
		VALUES(1, ?, NULL, 99999, 99999, 0, 0, 1, 2727251700000, 1)
	`, tunnelID).Error; err != nil {
		t.Fatalf("seed user_tunnel: %v", err)
	}
	return &r30FixF01Fixture{h: h, r: r, nodeID: nodeID, tunnelID: tunnelID}
}

func (fx *r30FixF01Fixture) createForward(t *testing.T, name, inIP string, inPort int) int64 {
	t.Helper()
	res := httptest.NewRecorder()
	fx.h.forwardCreate(res, newAuthenticatedJSONRequest(t, map[string]interface{}{
		"tunnelId":   fx.tunnelID,
		"name":       name,
		"remoteAddr": "203.0.113.5:443",
		"inPort":     inPort,
		"inIp":       inIP,
	}))
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode forwardCreate response: %v body=%s", err, res.Body.String())
	}
	if out.Code != 0 {
		t.Fatalf("forwardCreate failed: code=%d msg=%s body=%s", out.Code, out.Msg, res.Body.String())
	}
	var id int64
	if err := fx.r.DB().Raw(`SELECT id FROM forward WHERE name = ?`, name).Scan(&id).Error; err != nil || id == 0 {
		t.Fatalf("lookup forward %q: id=%d err=%v", name, id, err)
	}
	return id
}

func (fx *r30FixF01Fixture) storedInIP(t *testing.T, forwardID int64) string {
	t.Helper()
	var inIP string
	if err := fx.r.DB().Raw(`SELECT in_ip FROM forward_port WHERE forward_id = ?`, forwardID).Scan(&inIP).Error; err != nil {
		t.Fatalf("query forward_port.in_ip: %v", err)
	}
	return inIP
}

// serviceAddrs 按落库后的 in_ip 重算下发地址（CELL-A：gost 面）与
// realm 监听地址（CELL-R：realm 面），断言口径与 L1 探针一致。
func (fx *r30FixF01Fixture) checkDownstreamAddrs(t *testing.T, forwardID int64, port int, wantAddr string) {
	t.Helper()
	fwd, err := fx.h.getForwardRecord(forwardID)
	if err != nil {
		t.Fatalf("get forward: %v", err)
	}
	node, err := fx.h.getNodeRecord(fx.nodeID)
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	stored := fx.storedInIP(t, forwardID)
	services := buildForwardServiceConfigs("1_1_1", fwd, nil, node, port, stored, forwardRuntimeLimiters{})
	if len(services) != 2 {
		t.Fatalf("CELL-A: expected 2 services, got %d", len(services))
	}
	for _, svc := range services {
		addr, _ := svc["addr"].(string)
		if addr != wantAddr {
			t.Errorf("CELL-A service addr = %q, want %q (inPort %d silently dropped; service listens on embedded port)", addr, wantAddr, port)
		}
	}
	if got := realmListenAddr(node, port, stored); got != wantAddr {
		t.Errorf("CELL-R realmListenAddr = %q, want %q (inPort %d silently dropped)", got, wantAddr, port)
	}
}

func TestR30FixF01ForwardCreateStripsPortedInIP(t *testing.T) {
	fx := setupR30FixF01(t, "198.51.100.77:8443")
	id := fx.createForward(t, "r30-f01-fwd", "198.51.100.77:8443", 30001)
	if got := fx.storedInIP(t, id); got != "198.51.100.77" {
		t.Errorf("CELL-B forward_port.in_ip = %q, want %q (ported inIp stored verbatim)", got, "198.51.100.77")
	}
	fx.checkDownstreamAddrs(t, id, 30001, "198.51.100.77:30001")
}

func TestR30FixF01ForwardCreateBareInIPControl(t *testing.T) {
	fx := setupR30FixF01(t, "198.51.100.77")
	id := fx.createForward(t, "r30-f01-control", "198.51.100.77", 30001)
	if got := fx.storedInIP(t, id); got != "198.51.100.77" {
		t.Errorf("CELL-B forward_port.in_ip = %q, want %q", got, "198.51.100.77")
	}
	fx.checkDownstreamAddrs(t, id, 30001, "198.51.100.77:30001")
}

func TestR30FixF01ForwardCreateStripsBracketedV6InIP(t *testing.T) {
	fx := setupR30FixF01(t, "198.51.100.77")
	id := fx.createForward(t, "r30-f01-v6", "[2001:db8::1]:8443", 30001)
	if got := fx.storedInIP(t, id); got != "2001:db8::1" {
		t.Errorf("CELL-B forward_port.in_ip = %q, want %q (bracketed v6 inIp stored verbatim)", got, "2001:db8::1")
	}
	fx.checkDownstreamAddrs(t, id, 30001, "[2001:db8::1]:30001")
}

func TestR30FixF01ForwardUpdateStripsPortedInIP(t *testing.T) {
	fx := setupR30FixF01(t, "198.51.100.77")
	id := fx.createForward(t, "r30-f01-upd", "198.51.100.77", 30001)
	res := httptest.NewRecorder()
	fx.h.forwardUpdate(res, newAuthenticatedJSONRequest(t, map[string]interface{}{
		"id":       id,
		"tunnelId": fx.tunnelID,
		"inPort":   30002,
		"inIp":     "198.51.100.77:8443",
	}))
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode forwardUpdate response: %v body=%s", err, res.Body.String())
	}
	if out.Code != 0 {
		t.Fatalf("forwardUpdate failed: code=%d msg=%s body=%s", out.Code, out.Msg, res.Body.String())
	}
	if got := fx.storedInIP(t, id); got != "198.51.100.77" {
		t.Errorf("CELL-B forward_port.in_ip = %q, want %q (ported inIp stored verbatim via forwardUpdate)", got, "198.51.100.77")
	}
	fx.checkDownstreamAddrs(t, id, 30002, "198.51.100.77:30002")
}

func TestR30FixF01ForwardCreateStripsExtraIPsPortedInIP(t *testing.T) {
	fx := setupR30FixF01(t, "198.51.100.77")
	// extraIPs 向量：节点 extra_ips 原值含 :port（nodeCreate 原值落库，
	// 不改写侧存储语义）；前端 parseNodeIPs 会把该值原样放进下拉，
	// 用户选中后以 inIp 提交——intake 处同样须剥离。
	if err := fx.r.DB().Exec(`UPDATE node SET extra_ips = ? WHERE id = ?`, "198.51.100.77:8443", fx.nodeID).Error; err != nil {
		t.Fatalf("seed extra_ips: %v", err)
	}
	id := fx.createForward(t, "r30-f01-extra", "198.51.100.77:8443", 30001)
	if got := fx.storedInIP(t, id); got != "198.51.100.77" {
		t.Errorf("CELL-B forward_port.in_ip = %q, want %q (ported extraIPs value stored verbatim)", got, "198.51.100.77")
	}
	fx.checkDownstreamAddrs(t, id, 30001, "198.51.100.77:30001")
}

func TestR30FixF01ForwardUpdateWithoutInIPPreservesStored(t *testing.T) {
	fx := setupR30FixF01(t, "198.51.100.77")
	id := fx.createForward(t, "r30-f01-preserve", "198.51.100.77", 30001)
	res := httptest.NewRecorder()
	fx.h.forwardUpdate(res, newAuthenticatedJSONRequest(t, map[string]interface{}{
		"id":   id,
		"name": "r30-f01-preserve-renamed",
	}))
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode forwardUpdate response: %v body=%s", err, res.Body.String())
	}
	if out.Code != 0 {
		t.Fatalf("forwardUpdate failed: code=%d msg=%s body=%s", out.Code, out.Msg, res.Body.String())
	}
	if got := fx.storedInIP(t, id); got != "198.51.100.77" {
		t.Errorf("forwardUpdate without inIp must preserve stored in_ip, got %q want %q", got, "198.51.100.77")
	}
}
