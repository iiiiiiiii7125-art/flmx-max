package socket

import (
	"encoding/json"
	"testing"
)

// C4-G2 跨侧契约门禁·agent 生产半（终局帧字段对齐）。
//
// 契约：UninstallResult 终局帧是 CommandResponse 正形——success
// 与失败原因（message）在帧顶层，data 只载幂等键与明细
// （issuedAt/uninstallKey/requestId/state/failedStep/steps）。
// 面板消费半按此解码（go-backend/internal/ws/uninstall.go
// handleUninstallResult）。LIVE 真机实证（2026-11-10）：面板曾按
// data.success/data.reason 解码，agent 真帧喂入后成功终局被误记
// failed、节点记录永不删——两侧单测各自自洽、真帧从未喂过面板
// 解码。本门禁把构造器输出钉成线型 golden；面板半的同值 golden
// 在 go-backend/internal/ws/uninstall_final_contract_test.go，
// 两处字节须逐字一致。
//
// 成功格的状态与 LIVE 证据帧同源（live-evidence/28-c4g2-state.json
// 的 issuedAt/requestId/五步）；失败格的 message 须由构造器自
// failedStep 与步骤 error 原文合成（执行路径只落 FailedStep、
// 不写 Reason，message 是面板落 failed 原因的唯一通道）。
const uninstallFinalContractIssuedAt = int64(1794317291868)

const uninstallFinalSuccessGolden = `{"type":"UninstallResult","success":true,"message":"","data":{"failedStep":"","issuedAt":1794317291868,"requestId":"34_1794317291871470883","state":"succeeded","steps":[{"step":"restore-tuning","ok":true},{"step":"cleanup-host-tuning-files","ok":true},{"step":"uninstall-kernels","ok":true},{"step":"locate-binary","ok":true},{"step":"wipe-data-dir","ok":true}],"uninstallKey":"1794317291868"}}`

const uninstallFinalFailureGolden = `{"type":"UninstallResult","success":false,"message":"step uninstall-kernels failed: kernel uninstall exited: boom","data":{"failedStep":"uninstall-kernels","issuedAt":1794317291868,"requestId":"34_1794317291871470883","state":"failed","steps":[{"step":"restore-tuning","ok":true},{"step":"cleanup-host-tuning-files","ok":true},{"step":"uninstall-kernels","ok":false,"error":"kernel uninstall exited: boom"}],"uninstallKey":"1794317291868"}}`

// uninstallFinalFailureReason 是失败格面板须逐字落账的原因文本
// （＝失败格 golden 的顶层 message）。
const uninstallFinalFailureReason = "step uninstall-kernels failed: kernel uninstall exited: boom"

func uninstallFinalSuccessState() *uninstallState {
	return &uninstallState{
		Key:       "1794317291868",
		IssuedAt:  issuedAtMillis(uninstallFinalContractIssuedAt),
		RequestID: "34_1794317291871470883",
		State:     "succeeded",
		Steps: []uninstallStepResult{
			{Step: "restore-tuning", OK: true},
			{Step: "cleanup-host-tuning-files", OK: true},
			{Step: "uninstall-kernels", OK: true},
			{Step: "locate-binary", OK: true},
			{Step: "wipe-data-dir", OK: true},
		},
	}
}

func uninstallFinalFailureState() *uninstallState {
	return &uninstallState{
		Key:        "1794317291868",
		IssuedAt:   issuedAtMillis(uninstallFinalContractIssuedAt),
		RequestID:  "34_1794317291871470883",
		State:      "failed",
		FailedStep: "uninstall-kernels",
		Steps: []uninstallStepResult{
			{Step: "restore-tuning", OK: true},
			{Step: "cleanup-host-tuning-files", OK: true},
			{Step: "uninstall-kernels", OK: false, Error: "kernel uninstall exited: boom"},
		},
	}
}

// TestUninstallFinalFrameContractSuccessGolden：成功终局帧的线
// 型字节与 golden 逐字一致（success 在顶层、data.issuedAt 为数
// 字、五步明细齐）。
func TestUninstallFinalFrameContractSuccessGolden(t *testing.T) {
	b, err := json.Marshal(buildUninstallFinalResponse(uninstallFinalSuccessState()))
	if err != nil {
		t.Fatalf("marshal final response: %v", err)
	}
	if string(b) != uninstallFinalSuccessGolden {
		t.Fatalf("success final frame drifted from contract golden:\n got: %s\nwant: %s", b, uninstallFinalSuccessGolden)
	}
}

// TestUninstallFinalFrameContractFailureGolden：失败终局帧的
// success=false 在顶层，且 message 载合成原因（failedStep＋步
// 骤 error 原文）——基线构造器 message 恒空，本格在基线判红。
func TestUninstallFinalFrameContractFailureGolden(t *testing.T) {
	b, err := json.Marshal(buildUninstallFinalResponse(uninstallFinalFailureState()))
	if err != nil {
		t.Fatalf("marshal final response: %v", err)
	}
	if string(b) != uninstallFinalFailureGolden {
		t.Fatalf("failure final frame drifted from contract golden:\n got: %s\nwant: %s", b, uninstallFinalFailureGolden)
	}
}

// TestUninstallFinalMessageComposition：message 合成口径——存量
// 状态已写 Reason 时逐字沿用（不被合成覆盖）；failed 无
// failedStep 时不造原因；succeeded 恒空。
func TestUninstallFinalMessageComposition(t *testing.T) {
	withReason := uninstallFinalFailureState()
	withReason.Reason = "agent restarted before the uninstall completed"
	if got := uninstallFinalMessage(withReason); got != withReason.Reason {
		t.Fatalf("stored reason must win, got %q", got)
	}
	noStep := uninstallFinalFailureState()
	noStep.FailedStep = ""
	noStep.Steps = nil
	if got := uninstallFinalMessage(noStep); got != "" {
		t.Fatalf("failed without failedStep must not invent a reason, got %q", got)
	}
	if got := uninstallFinalMessage(uninstallFinalSuccessState()); got != "" {
		t.Fatalf("succeeded message must be empty, got %q", got)
	}
	if got := uninstallFinalMessage(uninstallFinalFailureState()); got != uninstallFinalFailureReason {
		t.Fatalf("composed reason = %q, want %q", got, uninstallFinalFailureReason)
	}
}
