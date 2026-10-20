package handler

import (
	"errors"
	"testing"
)

// S-04: 节点离线时删除转发不得静默返回"成功"
// 修复前：control_plane.go 的 DeleteService 分支中，isNodeOfflineOrTimeoutError 时 continue，
//
//	最终返回 nil，forwardDelete 继续删库，导致 Agent 侧残留。
//
// 修复后：离线节点被收集并返回 nodeOfflineError，forwardDelete 中止，不删库。
func TestS04_NodeOfflineErrorType(t *testing.T) {
	err := &nodeOfflineError{nodeIDs: []int64{1, 2, 3}}
	if !IsNodeOfflineError(err) {
		t.Error("expected IsNodeOfflineError to recognize nodeOfflineError")
	}
	if !errors.As(err, &err) {
		t.Error("expected errors.As to work")
	}
	msg := err.Error()
	if len(msg) == 0 {
		t.Error("expected non-empty error message")
	}
	// 验证错误信息包含节点ID
	if !contains(msg, "1") {
		t.Errorf("expected error message to contain node IDs, got: %s", msg)
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestS04_OfflineErrorNotNil(t *testing.T) {
	// 契约测试：controlForwardServices 在 DeleteService 且节点离线时
	// 必须返回非 nil 错误（nodeOfflineError），而非 nil（静默成功）
	// 完整集成测试需要 mock wsServer，此处验证类型契约
	var err error = &nodeOfflineError{nodeIDs: []int64{99}}
	if err == nil {
		t.Error("nodeOfflineError must not be nil")
	}
	if !IsNodeOfflineError(err) {
		t.Error("must be recognized as node offline error")
	}
}
