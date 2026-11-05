package nftables

import (
	"context"
	"errors"
)

type Manager struct {
	runner Runner
}

func NewManager(runner Runner) *Manager {
	if runner == nil {
		runner = NewSSHRunner()
	}
	return &Manager{runner: runner}
}

func (m *Manager) Test(ctx context.Context, cfg SSHConfig) error {
	if err := m.ensureInitialized(); err != nil {
		return err
	}
	return m.runner.Test(ctx, cfg)
}

// ProbeHostKeyFingerprint 取回对端呈现的 SSH 主机密钥指纹（TOFU 钉入
// 用，见包级同名函数：不发送任何凭据）。探针不执行远程命令，故不经
// runner，直接走包级实现。
func (m *Manager) ProbeHostKeyFingerprint(ctx context.Context, cfg SSHConfig) (string, error) {
	if err := m.ensureInitialized(); err != nil {
		return "", err
	}
	return ProbeHostKeyFingerprint(ctx, cfg)
}

func (m *Manager) Reconcile(ctx context.Context, cfg SSHConfig, plan NodePlan) (ApplyResult, error) {
	if err := m.ensureInitialized(); err != nil {
		return ApplyResult{}, err
	}
	// C3-01：渲染层单点校验——非法 TargetHost 整单拒绝，脚本不下发。
	if err := ValidatePlan(plan); err != nil {
		return ApplyResult{}, err
	}
	result := ApplyResult{
		NodeID: plan.NodeID,
		Script: RenderTable(plan),
		Hashes: PlanHashes(plan),
	}
	if err := m.runner.ApplyScript(ctx, cfg, result.Script); err != nil {
		return ApplyResult{}, err
	}
	return result, nil
}

func (m *Manager) Clear(ctx context.Context, cfg SSHConfig) error {
	if err := m.ensureInitialized(); err != nil {
		return err
	}
	script := RenderTable(NodePlan{})
	if err := m.runner.ApplyScript(ctx, cfg, script); err != nil {
		return err
	}
	// 节点清理同时删除暂停过滤表：封禁端口随表释放，否则端口被新
	// 转发复用后仍会被残留封禁静默 drop。
	if runner, ok := m.runner.(interface {
		ClearBlocks(context.Context, SSHConfig) error
	}); ok {
		return runner.ClearBlocks(ctx, cfg)
	}
	return nil
}

func (m *Manager) CollectCounters(ctx context.Context, cfg SSHConfig) ([]CounterSample, error) {
	if err := m.ensureInitialized(); err != nil {
		return nil, err
	}
	raw, err := m.runner.ListTableJSON(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return ParseCounterSamples(raw)
}

func (m *Manager) ensureInitialized() error {
	if m == nil || m.runner == nil {
		return errors.New("nftables manager not initialized")
	}
	return nil
}
