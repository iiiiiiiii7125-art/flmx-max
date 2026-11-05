package socket

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/go-gost/x/kernel"
)

// handleKernelCommand dispatches proxy-kernel commands. They are deliberately
// not mutation commands: an install may download for minutes and must not
// block forwarding changes behind the shared mutation queue.
func handleKernelCommand(cmdType string, data interface{}) (interface{}, error) {
	return handleKernelCommandContext(context.Background(), cmdType, data)
}

// handleKernelCommandContext is handleKernelCommand under a per-command
// budget (D1-06). The kernel Manager API takes no context, so the
// socket layer can only refuse to *start* work whose budget is already
// spent — an Apply that begins inside its budget runs under the
// manager's own download/install budgets as before.
func handleKernelCommandContext(ctx context.Context, cmdType string, data interface{}) (interface{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("命令执行预算已耗尽，拒绝执行: %w", err)
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("序列化数据失败: %v", err)
	}
	m := kernel.Default()
	switch cmdType {
	case "KernelApply":
		var req kernel.ApplyRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, fmt.Errorf("解析内核配置失败: %v", err)
		}
		return m.Apply(req)
	case "KernelControl":
		var req struct {
			Type   string `json:"type"`
			Action string `json:"action"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, fmt.Errorf("解析内核操作失败: %v", err)
		}
		return m.Control(req.Type, req.Action)
	case "KernelStatus":
		var req struct {
			Logs string `json:"logs"`
		}
		_ = json.Unmarshal(raw, &req)
		return m.Status(req.Logs), nil
	}
	return nil, fmt.Errorf("未知命令类型: %s", cmdType)
}
