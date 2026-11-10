package handler

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

// R2-RAW291 门禁盲区行为测试：TestAdminRawErrorGate 只展开一层
// （深度上限 2）＋不展开函数字面量＋只扫 admin 路由——深度 ≥2 的
// 仓储直通、闭包内错误均不可见。下列 11 处盲区修复（见 W2 报告
// §RAW291）以掉表／触发器法做行为实证：仓储原文不得进回执。
// 其中 (a)(b)(d) 为端到端格，(c)(e) 为标记单测（端到端格因行载
// 入先于索引载入、同表不可达，已在报告中论证）。

// TestR2Raw291TunnelUpdateValidationSegmentGeneralizes：验证段 -2
// 码道——tunnel 表 UPDATE 触发器让 UpdateTunnelTx 撞
// 'boom-marker'，守卫须泛化为 "Internal error"，不得透出标记文本。
func TestR2Raw291TunnelUpdateValidationSegmentGeneralizes(t *testing.T) {
	a := newCleanupAgent(t)
	tunnelID := seedA102Base(t, a)
	if err := a.h.repo.DB().Exec(`CREATE TRIGGER r2_raw291_block_tunnel_update BEFORE UPDATE ON "tunnel" FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'boom-marker'); END;`).Error; err != nil {
		t.Fatalf("create tunnel update trigger: %v", err)
	}
	res := httptest.NewRecorder()
	a.h.tunnelUpdate(res, newAuthenticatedJSONRequest(t, map[string]interface{}{
		"id":       tunnelID,
		"inNodeId": []map[string]interface{}{{"nodeId": 1, "protocol": "tls"}},
	}))
	code, msg := decodeEnvelopeA204(t, res)
	if code == 0 {
		t.Fatalf("triggered validation failure must not succeed, body=%s", res.Body.String())
	}
	if msg != "Internal error" {
		t.Fatalf("validation -2 branch must generalize, got msg=%q body=%s", msg, res.Body.String())
	}
	if strings.Contains(res.Body.String(), "boom-marker") {
		t.Fatalf("storage marker leaked into response: %s", res.Body.String())
	}
}

// TestR2Raw291TunnelUpdatePrepareGeneralizes：验证段 0 码道——
// DROP node 表后，prepare 的 GetNodeRecordTx 标记错经守卫泛化；
// 前置段只读 tunnel／chain_tunnel，故可达闭包。
func TestR2Raw291TunnelUpdatePrepareGeneralizes(t *testing.T) {
	a := newCleanupAgent(t)
	tunnelID := seedA102Base(t, a)
	if err := a.h.repo.DB().Exec("DROP TABLE node").Error; err != nil {
		t.Fatalf("drop node: %v", err)
	}
	res := httptest.NewRecorder()
	a.h.tunnelUpdate(res, newAuthenticatedJSONRequest(t, map[string]interface{}{
		"id":       tunnelID,
		"inNodeId": []map[string]interface{}{{"nodeId": 1, "protocol": "tls"}},
	}))
	code, msg := decodeEnvelopeA204(t, res)
	if code == 0 {
		t.Fatalf("dropped-nodes prepare failure must not succeed, body=%s", res.Body.String())
	}
	if msg != "Internal error" {
		t.Fatalf("prepare 0 branch must generalize, got msg=%q body=%s", msg, res.Body.String())
	}
	if strings.Contains(res.Body.String(), "no such table") {
		t.Fatalf("storage error leaked into response: %s", res.Body.String())
	}
}

// TestR2Raw291ChainReferrersMarksRepoError：chainReferrers 的索引
// 加载是仓储直通（深度 2、门不可见）——DROP node_inbound 后须回
// 带 *repoError 标记的错，调用方守卫方可泛化。
func TestR2Raw291ChainReferrersMarksRepoError(t *testing.T) {
	a := newCleanupAgent(t)
	if err := a.h.repo.DB().Exec("DROP TABLE node_inbound").Error; err != nil {
		t.Fatalf("drop node_inbound: %v", err)
	}
	_, err := a.h.chainReferrers(1)
	if err == nil {
		t.Fatal("chainReferrers must fail after dropping node_inbound")
	}
	var re *repoError
	if !errors.As(err, &re) {
		t.Fatalf("chainReferrers error must carry *repoError mark, got %T %v", err, err)
	}
}

// TestR2Raw291NodeNftablesReconcileGeneralizes：节点 1 置 nft 模式
// ＋DROP node_ssh_config——reconcile 首错即 GetNodeSSHConfig
// （已标记），守卫须泛化为 "Internal error"。
func TestR2Raw291NodeNftablesReconcileGeneralizes(t *testing.T) {
	a := newCleanupAgent(t)
	if err := a.h.repo.DB().Exec("UPDATE node SET forward_mode='nftables' WHERE id=1").Error; err != nil {
		t.Fatalf("set nft mode: %v", err)
	}
	if err := a.h.repo.DB().Exec("DROP TABLE node_ssh_config").Error; err != nil {
		t.Fatalf("drop node_ssh_config: %v", err)
	}
	res := postJSONToHandler(t, a.h.nodeNftablesReconcile, map[string]interface{}{"nodeId": 1})
	code, msg := decodeEnvelopeA204(t, res)
	if code == 0 {
		t.Fatalf("reconcile must fail without ssh config table, body=%s", res.Body.String())
	}
	if msg != "Internal error" {
		t.Fatalf("reconcile must generalize storage error, got msg=%q body=%s", msg, res.Body.String())
	}
	if strings.Contains(res.Body.String(), "no such table") {
		t.Fatalf("storage error leaked into response: %s", res.Body.String())
	}
}

// TestR2Raw291ValidateNftablesTunnelStateMarksRepoError：节点 1 置
// nft 模式＋DROP node_ssh_config——validateNftablesTunnelStateTx
// 的 collect 错须带 *repoError 标记，:2640 调用点守卫方可泛化。
func TestR2Raw291ValidateNftablesTunnelStateMarksRepoError(t *testing.T) {
	a := newCleanupAgent(t)
	if err := a.h.repo.DB().Exec("UPDATE node SET forward_mode='nftables' WHERE id=1").Error; err != nil {
		t.Fatalf("set nft mode: %v", err)
	}
	if err := a.h.repo.DB().Exec("DROP TABLE node_ssh_config").Error; err != nil {
		t.Fatalf("drop node_ssh_config: %v", err)
	}
	err := a.h.validateNftablesTunnelStateTx(nil, []int64{1})
	if err == nil {
		t.Fatal("validateNftablesTunnelStateTx must fail after dropping node_ssh_configs")
	}
	var re *repoError
	if !errors.As(err, &re) {
		t.Fatalf("validate error must carry *repoError mark, got %T %v", err, err)
	}
}
