package handler

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/repo"
)

// R10-11（I-C4-01）：入站批删的两段式结果集口径——批内豁免只在
// 「引用方确将同批删除」时成立。旧实现按请求集判豁免：目标 A 的
// 引用方 B 在请求集内即放行 A，B 随后因批外引用独立失败而存活
// 时，A 已先删、B 的出站悬空且回执不点名被打断的 B→A 引用。
// R10-05（I-F3-04）：kernelControl restart 对已停用内核点名拒绝——
// agent 侧 Control 对 restart 一律按 Enabled=true 拉起，面板
// enabled=0 与节点实际服务状态不得就此分叉。

// r10RunBatchDelete 调用 inboundBatchDelete；若实现进入了某条入站
// 的中转清理（DeleteService 被 mock agent 挂起），放行它让调用走
// 完，并报告是否发生过删除下发——两段式实现下，被整批守卫拒绝
// 的批次不应触碰任何一条入站的中转清理。
func r10RunBatchDelete(t *testing.T, s *r9InboundScenario, idsBody string) (r9BatchDeleteResp, bool) {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/inbound/batch-delete", strings.NewReader(idsBody))
	res := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.h.inboundBatchDelete(res, req)
	}()
	deleteAttempted := false
	deadline := time.Now().Add(60 * time.Second)
	for {
		select {
		case <-done:
			if len(s.agent.heldCh) > 0 {
				deleteAttempted = true
			}
			var env r9BatchDeleteResp
			if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
				t.Fatalf("decode batch response: %v body=%s", err, res.Body.String())
			}
			return env, deleteAttempted
		default:
		}
		if len(s.agent.heldCh) > 0 {
			deleteAttempted = true
			s.agent.release()
		}
		if time.Now().After(deadline) {
			t.Fatal("batch delete did not complete")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func r10FailureReason(env r9BatchDeleteResp, id int64) string {
	for _, failure := range env.Data.Failures {
		if failure.ID == id {
			return failure.Reason
		}
	}
	return ""
}

func TestR10InboundBatchDeletePartialFailureKeepsReferrerChain(t *testing.T) {
	s := setupR9InboundScenario(t)
	a := s.target // landing-vless，带面板托管中转
	b := seedChainInbound(t, s.r, s.frontXray, "front-vless", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 8443,
		Security: kernel.Security{Type: kernel.SecurityNone}}, a.ID)
	seedChainInbound(t, s.r, s.frontXray, "c-ref", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 8444,
		Security: kernel.Security{Type: kernel.SecurityNone}}, b.ID)

	env, deleteAttempted := r10RunBatchDelete(t, s, `{"ids":[`+nodeIDStr(a.ID)+`,`+nodeIDStr(b.ID)+`]}`)
	if env.Code != 0 {
		t.Fatalf("batch delete envelope must stay code 0, got %d", env.Code)
	}
	// 结果集口径：B 被批外 C 引用 → B 删不掉 → A 的豁免不成立，
	// 两条都不得删除（旧实现：A 成功、B 失败、B 出站悬空）。
	if env.Data.SuccessCount != 0 || env.Data.FailCount != 2 {
		t.Fatalf("R10-11: batch counts = %d/%d, want 0 success + 2 fail (B blocked by outside referrer, A blocked by surviving B)",
			env.Data.SuccessCount, env.Data.FailCount)
	}
	if deleteAttempted {
		t.Fatal("R10-11: a fully guard-rejected batch must not start any inbound's relay removal")
	}
	if reason := r10FailureReason(env, a.ID); !strings.Contains(reason, "front-vless") {
		t.Fatalf("R10-11: A's failure must name its surviving referrer front-vless (the B→A reference), got %q (failures=%+v)", reason, env.Data.Failures)
	}
	if reason := r10FailureReason(env, b.ID); !strings.Contains(reason, "c-ref") {
		t.Fatalf("R10-11: B's failure must name its outside referrer c-ref, got %q (failures=%+v)", reason, env.Data.Failures)
	}
	if !s.targetSurvives(t) {
		t.Fatal("R10-11: target A deleted although its referrer B survives the batch")
	}
	bRow, err := s.r.GetNodeInbound(b.ID)
	if err != nil || bRow == nil {
		t.Fatalf("R10-11: referrer B must survive the batch: %v", err)
	}
	if bRow.OutboundID != a.ID {
		t.Fatalf("R10-11: B's outbound changed to %d, want %d (A)", bRow.OutboundID, a.ID)
	}
}

func TestR10InboundBatchDeleteReferrerDeletedFirst(t *testing.T) {
	s := setupR9InboundScenario(t)
	a := s.target
	b := seedChainInbound(t, s.r, s.frontXray, "front-vless", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 8443,
		Security: kernel.Security{Type: kernel.SecurityNone}}, a.ID)

	// 请求把被引用方 A 排在引用方 B 之前：删除序须按引用拓扑
	// （先删引用方）而非请求序，结果集内无批外引用时整批成功。
	env, _ := r10RunBatchDelete(t, s, `{"ids":[`+nodeIDStr(a.ID)+`,`+nodeIDStr(b.ID)+`]}`)
	if env.Data.SuccessCount != 2 || env.Data.FailCount != 0 {
		t.Fatalf("R10-11: batch counts = %d/%d, want 2 success + 0 fail, failures=%+v",
			env.Data.SuccessCount, env.Data.FailCount, env.Data.Failures)
	}
	if s.targetSurvives(t) {
		t.Fatal("R10-11: target A must be deleted once its only referrer is deleted first")
	}
	if row, err := s.r.GetNodeInbound(b.ID); err != nil || row != nil {
		t.Fatalf("R10-11: referrer B must be deleted, got row=%v err=%v", row, err)
	}
}

func TestR10KernelControlRestartRejectsDisabledKernel(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r10-kernel-restart.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	node := seedRelayTargetNode(t, r, "restart-node", "198.51.100.9")
	k := seedChainKernel(t, r, node, kernel.TypeXray)
	if err := r.UpdateNodeKernelFields(k.ID, map[string]interface{}{"enabled": 0}); err != nil {
		t.Fatalf("disable kernel: %v", err)
	}

	env := c10CallControl(t, h, k.ID, "restart")
	if env.Code == 0 {
		t.Fatalf("R10-05: restart of a disabled kernel must be rejected, got success")
	}
	if !strings.Contains(env.Msg, "已停用") || !strings.Contains(env.Msg, "先启动") {
		t.Fatalf("R10-05: rejection must name the disabled state and direct to start first, got msg=%q", env.Msg)
	}
	row, err := h.repo.GetNodeKernel(k.ID)
	if err != nil || row == nil {
		t.Fatalf("kernel row missing after rejected restart: %v", err)
	}
	if row.Enabled != 0 {
		t.Fatalf("R10-05: rejected restart must not change enabled, got %d", row.Enabled)
	}

	// 启用中的内核不走新守卫：离线节点上 restart 仍进命令下发面
	// （以命令错误收口），回执不得是停用拒绝。
	k2 := seedChainKernel(t, r, node, kernel.TypeSingbox)
	env2 := c10CallControl(t, h, k2.ID, "restart")
	if strings.Contains(env2.Msg, "已停用") {
		t.Fatalf("R10-05: enabled kernel restart must not hit the disabled guard, got msg=%q", env2.Msg)
	}
}
