package nftables

import (
	"context"
	"errors"
	"fmt"
)

// 暂停过滤独立成表，不并入主 NAT 表：
//   - 封禁切换不得重置主表里的流量计数器；
//   - DNAT 规则被 reconcile 移除后，conntrack 里缓存的 NAT 绑定仍会让
//     已建立连接继续转发。本表的 transit/outgoing 链按 conntrack 原始
//     目的端口匹配，已建立连接的后续包同样被 drop，暂停即时断流；
//   - incoming 链覆盖直达本机地址的新连接（与 DNAT 是否存在无关）。
const (
	pauseTableName = "flmx_quota"
	pauseSetName   = "blocked_ports"
)

const pauseTable = `table inet flmx_quota {
    set blocked_ports { type inet_service; }
    chain incoming {
        type filter hook prerouting priority -110; policy accept;
        fib daddr type local meta l4proto { tcp, udp } th dport @blocked_ports drop
    }
    chain transit {
        type filter hook forward priority -10; policy accept;
        ct status dnat meta l4proto { tcp, udp } ct original proto-dst @blocked_ports drop
    }
    chain outgoing {
        type filter hook output priority -10; policy accept;
        ct status dnat meta l4proto { tcp, udp } ct original proto-dst @blocked_ports drop
    }
}`

// RenderPauseTable 返回暂停过滤表的完整定义（供命令构建与测试断言）。
func RenderPauseTable() string {
	return pauseTable
}

func (r *SSHRunner) SetBlocked(ctx context.Context, cfg SSHConfig, port int, blocked bool) error {
	if port < 1 || port > 65535 {
		return errors.New("invalid nftables port")
	}
	return r.run(ctx, cfg, buildSetBlockedCommand(nftBinary(cfg), port, blocked))
}

func buildSetBlockedCommand(nft string, port int, blocked bool) string {
	element := fmt.Sprintf(" element inet %s %s '{ %d }'", pauseTableName, pauseSetName, port)
	if blocked {
		// 表不存在先建（整表加载是幂等的：表已存在时跳过，不动既有
		// 封禁集），再把端口加入封禁集。
		return "set -eu\n" +
			"if ! " + nft + " list table inet " + pauseTableName + " >/dev/null 2>&1; then\n" +
			nft + " -f - <<'EOF'\n" + pauseTable + "\nEOF\n" +
			"fi\n" +
			nft + " add" + element
	}
	// 解除封禁不建表：表不存在即无封禁可解；元素不存在同样视为已解除，
	// 保证恢复路径可重复执行不报错。
	return "set -eu\n" +
		"if " + nft + " list table inet " + pauseTableName + " >/dev/null 2>&1; then\n" +
		"if " + nft + " get" + element + " >/dev/null 2>&1; then\n" +
		nft + " delete" + element + "\n" +
		"fi\n" +
		"fi"
}

// ClearBlocks 删除整张暂停过滤表（节点清理时调用），封禁端口随表
// 一并释放，可被新转发复用。
func (r *SSHRunner) ClearBlocks(ctx context.Context, cfg SSHConfig) error {
	return r.run(ctx, cfg, buildClearBlocksCommand(nftBinary(cfg)))
}

func buildClearBlocksCommand(nft string) string {
	return "set -eu\n" +
		"if " + nft + " list table inet " + pauseTableName + " >/dev/null 2>&1; then\n" +
		nft + " delete table inet " + pauseTableName + "\n" +
		"fi"
}

func (m *Manager) SetBlocked(ctx context.Context, cfg SSHConfig, port int, blocked bool) error {
	if err := m.ensureInitialized(); err != nil {
		return err
	}
	runner, ok := m.runner.(interface {
		SetBlocked(context.Context, SSHConfig, int, bool) error
	})
	if !ok {
		return errors.New("nftables runner does not support traffic blocking")
	}
	return runner.SetBlocked(ctx, cfg, port, blocked)
}
