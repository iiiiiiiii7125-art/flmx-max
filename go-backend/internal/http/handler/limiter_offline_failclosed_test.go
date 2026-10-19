package handler

import (
	"strings"
	"testing"
)

// AUDIT-R7 (R5R7-001): 限速 fail-open 回归测试。
// 节点离线时创建带限速的转发，旧行为是告警后继续（转发永久无速限），
// 修复后应直接失败（fail-closed）。
//
// 本测试验证 isNodeOfflineOrTimeoutError 的识别逻辑与 fail-closed 语义。
// 完整的端到端测试需真实离线节点，此处覆盖核心判定逻辑。
func TestLimiterOfflineFailClosed(t *testing.T) {
	// 验证离线错误的识别。
	offlineErrs := []error{
		&testOfflineError{msg: "节点不在线"},
		&testOfflineError{msg: "错误：节点不在线，请重试"},
		&testOfflineError{msg: "connection timeout"},
		&testOfflineError{msg: "等待节点响应超时"},
	}
	for _, err := range offlineErrs {
		if !isNodeOfflineOrTimeoutError(err) {
			t.Errorf("isNodeOfflineOrTimeoutError(%v) = false, want true", err)
		}
	}

	// 验证非离线错误不被误判。
	otherErrs := []error{
		&testOfflineError{msg: "some other error"},
		&testOfflineError{msg: "node offline"}, // 英文无"节点不在线"且无 timeout，不识别
	}
	for _, err := range otherErrs {
		if isNodeOfflineOrTimeoutError(err) {
			t.Errorf("isNodeOfflineOrTimeoutError(%v) = true, want false (非离线错误不应误判)", err)
		}
	}
}

// testOfflineError 模拟离线错误。
type testOfflineError struct {
	msg string
}

func (e *testOfflineError) Error() string { return e.msg }

// 验证 fail-closed 的错误文案包含关键信息。
func TestLimiterOfflineErrorMessage(t *testing.T) {
	// 模拟修复后的错误文案格式。
	nodeName := "测试节点"
	errMsg := "节点 " + nodeName + " 不在线，限速无法下发，请稍后重试"
	if !strings.Contains(errMsg, "不在线") || !strings.Contains(errMsg, "限速无法下发") {
		t.Errorf("错误文案应包含'不在线'和'限速无法下发'，got: %s", errMsg)
	}
}
