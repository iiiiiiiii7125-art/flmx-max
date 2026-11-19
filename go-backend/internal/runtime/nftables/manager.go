package nftables

import (
	"context"
	"errors"
)

type Manager struct {
	runner Runner
	// instance is this panel install's instance key (FLMX_INSTANCE;
	// "" is the default instance). It selects the nft table for the
	// plan-less operations (CollectCounters, Clear); Reconcile takes
	// the table from the plan, which the handler fills with the same
	// key, so the two always agree in production.
	instance string
}

func NewManager(runner Runner) *Manager {
	return NewManagerWithInstance(runner, "")
}

// NewManagerWithInstance builds a Manager for a non-default panel
// instance; the handler passes panelInstanceID() here.
func NewManagerWithInstance(runner Runner, instance string) *Manager {
	if runner == nil {
		runner = NewSSHRunner()
	}
	return &Manager{runner: runner, instance: instance}
}

// tableName returns this panel install's nft table.
func (m *Manager) tableName() string {
	if m == nil {
		return TableNameForInstance("")
	}
	return TableNameForInstance(m.instance)
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
	if err := m.runner.ApplyScript(ctx, cfg, plan.TableName(), result.Script); err != nil {
		return ApplyResult{}, err
	}
	return result, nil
}

func (m *Manager) Clear(ctx context.Context, cfg SSHConfig) error {
	if err := m.ensureInitialized(); err != nil {
		return err
	}
	script := RenderTable(NodePlan{InstanceID: m.instance})
	if err := m.runner.ApplyScript(ctx, cfg, m.tableName(), script); err != nil {
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
	table := m.tableName()
	raw, err := m.runner.ListTableJSON(ctx, cfg, table)
	if err != nil {
		return nil, err
	}
	return ParseCounterSamples(raw, table)
}

func (m *Manager) ensureInitialized() error {
	if m == nil || m.runner == nil {
		return errors.New("nftables manager not initialized")
	}
	return nil
}
