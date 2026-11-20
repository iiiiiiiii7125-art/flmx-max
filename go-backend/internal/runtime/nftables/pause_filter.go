package nftables

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// 暂停过滤独立成表，不并入主 NAT 表：
//   - 封禁切换不得重置主表里的流量计数器；
//   - DNAT 规则被 reconcile 移除后，conntrack 里缓存的 NAT 绑定仍会让
//     已建立连接继续转发。本表的 transit/outgoing 链按 conntrack 原始
//     目的端口匹配，已建立连接的后续包同样被 drop，暂停即时断流；
//   - incoming 链覆盖直达本机地址的新连接（与 DNAT 是否存在无关）。
//
// R18-C3-01：暂停表按安装隔离（与主表同口径：flmx_quota_<8 hex
// 后缀，与主表同一身份派生）。旧 plain 表 flmx_quota 是全部面
// 板共用的一个表：他面板的每小时清扫（读本面板 DB 判定无主）
// 会把本面板的暂停端口当无主解封，Clear 也会整表清空他人封禁。
// legacyPauseTableName 只作为升级迁移的来源（建隔离表时一次性
// 认领旧元素）与恢复路径的残留清理，绝不再是操作表。
const (
	// legacyPauseTableName 是隔离前的 plain 暂停表名；新表不再用
	// 它，只在升级迁移时认领旧元素、恢复时清理残留。
	legacyPauseTableName = "flmx_quota"
	pauseSetName         = "blocked_ports"
)

// PauseTableNameForInstall derives this panel install's pause table
// name from its instance key plus its per-install identity, sharing
// the main table's 8-hex suffix (same tableNameSuffix): one install,
// one identity, two tables. With no identity it falls back to the
// legacy instance-only derivation.
func PauseTableNameForInstall(instance, installID string) string {
	return "flmx_quota_" + tableNameSuffix(instance, installID)
}

// PauseTableNameForInstance is the instance-only form of
// PauseTableNameForInstall (no install identity on file).
func PauseTableNameForInstance(instance string) string {
	return PauseTableNameForInstall(instance, "")
}

// pauseTableTemplate 是暂停过滤表的定义模板（%s 为表名）。
const pauseTableTemplate = `table inet %s {
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

// renderPauseTable renders the pause table definition for table.
func renderPauseTable(table string) string {
	return fmt.Sprintf(pauseTableTemplate, table)
}

// RenderPauseTable 返回默认实例的暂停过滤表完整定义（供命令构建
// 与测试断言）。默认实例同样隔离：flmx_quota_e3b0c442，不是
// plain flmx_quota。
func RenderPauseTable() string {
	return renderPauseTable(PauseTableNameForInstance(""))
}

// RenderPauseTableForInstall 返回指定安装的暂停过滤表完整定义。
func RenderPauseTableForInstall(instance, installID string) string {
	return renderPauseTable(PauseTableNameForInstall(instance, installID))
}

// pauseTableName returns this runner's pause table: the install's
// isolated table, never the plain shared name.
func (r *SSHRunner) pauseTableName() string {
	if r == nil {
		return PauseTableNameForInstance("")
	}
	return PauseTableNameForInstall(r.instance, r.installID)
}

// SetBlocked 切换暂停过滤表中的端口封禁状态。cleanupLegacy 只
// 在解除时有意义：为 true 时一并清理升级前 plain 表里同端口
// 的残留元素（恢复收敛）；为 false 时只动本安装的隔离表
// （清扫解除无主端口——plain 表元素归属无法证明，绝不能碰）。
func (r *SSHRunner) SetBlocked(ctx context.Context, cfg SSHConfig, port int, blocked bool, cleanupLegacy bool) error {
	if port < 1 || port > 65535 {
		return errors.New("invalid nftables port")
	}
	return r.run(ctx, cfg, buildSetBlockedCommand(nftBinary(cfg), r.pauseTableName(), port, blocked, cleanupLegacy))
}

// buildLegacyPauseElementCleanupCommand 生成恢复路径的残留清理片段
// （R18-C3-01）：升级前暂停过的端口在 plain 表里仍有残留元素；
// 只清隔离表会让恢复后的流量继续被残留规则 drop。清理只针对
// 本面板正在恢复的端口——另一套面板同号端口的暂停若恰在 plain
// 表里，会被一并摘掉，但对方下次 sync 按 DB 期望集重封（与
// 本面板的自愈口径相同），窗口有界；而不清理则本面板的恢复
// 永久失效。元素不存在时安静成功。
//
// R18-R2-C3-03：本片段只由 cleanupLegacy=true 的解除调用发出
// （sync 的恢复收敛：被恢复的是本面板自己的转发端口）。清扫
// （无主端口解除）绝不带本片段：无主端口没有归属转发，plain
// 表里同号元素更可能是另一套面板的封禁，清掉只会害人、于己
// 无益——端口复用时恢复收敛会再清残留。plain 表集合元素本身
// 不带归属，恢复收敛腿的残留窗口（对方同号暂停被短暂解除、
// 对方下次 sync 自愈）无法再分清，见 buildSetBlockedCommand。
func buildLegacyPauseElementCleanupCommand(nft string, port int) string {
	element := fmt.Sprintf(" element inet %s %s '{ %d }'", legacyPauseTableName, pauseSetName, port)
	return "if " + nft + " list table inet " + legacyPauseTableName + " >/dev/null 2>&1; then\n" +
		nft + " delete" + element + " 2>/dev/null || true\n" +
		"fi\n"
}

// buildSetBlockedCommand 生成暂停/恢复的单端口命令。cleanupLegacy
// 只在解除（blocked=false）时有意义，见
// buildLegacyPauseElementCleanupCommand。
//
// R18-R2-C3-03：建表时不再盲认领 plain 表元素。旧迁移把 plain
// 表 flmx_quota 的全部元素抄进本安装表——其中可能有另一套面板
// 的暂停端口，本面板会替它执法，直到每小时清扫按本面板 DB
// 判定无主再摘掉（窗口 ≤1h）。认领本就无必要：每次 sync 都
// 按本面板 DB 期望集逐个重封（见 syncNftablesNodeWithOverridesCtx），
// 应封的端口都会经由本函数加进来；plain 表本身永不删除，升
// 级前已封的端口在首次 sync 前仍由它继续执法，不留空档；
// 恢复时残留元素由 cleanupLegacy 腿逐个清理。
func buildSetBlockedCommand(nft, table string, port int, blocked bool, cleanupLegacy bool) string {
	element := fmt.Sprintf(" element inet %s %s '{ %d }'", table, pauseSetName, port)
	if blocked {
		// 表不存在先建（整表加载是幂等的：表已存在时跳过，不动既有
		// 封禁集），再把端口加入封禁集。建表时不再认领 plain 表
		// 元素，见文件头 R18-R2-C3-03 说明。
		return "set -eu\n" +
			"if ! " + nft + " list table inet " + table + " >/dev/null 2>&1; then\n" +
			nft + " -f - <<'EOF'\n" + renderPauseTable(table) + "\nEOF\n" +
			"fi\n" +
			nft + " add" + element
	}
	// 解除封禁不建表：表不存在即无封禁可解；元素不存在同样视为已解除，
	// 保证恢复路径可重复执行不报错。升级前 plain 表里的同端口残留
	// 是否一并清理由 cleanupLegacy 决定（见
	// buildLegacyPauseElementCleanupCommand）：恢复收敛带 true，
	// 否则恢复后流量仍被残留规则 drop；清扫带 false。
	cmd := "set -eu\n" +
		"if " + nft + " list table inet " + table + " >/dev/null 2>&1; then\n" +
		"if " + nft + " get" + element + " >/dev/null 2>&1; then\n" +
		nft + " delete" + element + "\n" +
		"fi\n" +
		"fi\n"
	if cleanupLegacy {
		cmd += buildLegacyPauseElementCleanupCommand(nft, port)
	}
	return cmd
}

// ClearBlocks 删除本安装的整张暂停过滤表（节点清理时调用），封禁
// 端口随表一并释放，可被新转发复用。只删本安装的隔离表，绝不删
// plain 表——另一套面板可能仍在用它（R18-C3-01）。
func (r *SSHRunner) ClearBlocks(ctx context.Context, cfg SSHConfig) error {
	return r.run(ctx, cfg, buildClearBlocksCommand(nftBinary(cfg), r.pauseTableName()))
}

func buildClearBlocksCommand(nft, table string) string {
	return "set -eu\n" +
		"if " + nft + " list table inet " + table + " >/dev/null 2>&1; then\n" +
		nft + " delete table inet " + table + "\n" +
		"fi"
}

func (m *Manager) SetBlocked(ctx context.Context, cfg SSHConfig, port int, blocked bool, cleanupLegacy bool) error {
	if err := m.ensureInitialized(); err != nil {
		return err
	}
	runner, ok := m.runner.(interface {
		SetBlocked(context.Context, SSHConfig, int, bool, bool) error
	})
	if !ok {
		return errors.New("nftables runner does not support traffic blocking")
	}
	return runner.SetBlocked(ctx, cfg, port, blocked, cleanupLegacy)
}

// ListBlockedPorts 读取节点当前封禁集（R2-038 对账清扫的读面）。
// 暂停表不存在即无封禁，返回空集而非错误。
func (r *SSHRunner) ListBlockedPorts(ctx context.Context, cfg SSHConfig) ([]int, error) {
	raw, err := r.runOutput(ctx, cfg, buildListBlockedPortsCommand(nftBinary(cfg), r.pauseTableName()))
	if err != nil {
		return nil, err
	}
	return ParseBlockedPortsJSON(raw)
}

func buildListBlockedPortsCommand(nft, table string) string {
	// 表不存在时安静无输出（等价空集），与解除封禁同口径。
	return "set -eu\n" +
		"if " + nft + " list table inet " + table + " >/dev/null 2>&1; then\n" +
		nft + " -j list set inet " + table + " " + pauseSetName + "\n" +
		"fi"
}

// ParseBlockedPortsJSON 解析 `nft -j list set` 输出中 blocked_ports
// 的元素集（升序）。空输出（表不存在）返回空集。元素只接受纯端口
// 数字：本集只由 SetBlocked 逐个写入单端口，出现区间等异形元素
// 说明集合被外部改动，返回错误让调用方整节点跳过本轮清扫，不做
// 猜测性处理。
func ParseBlockedPortsJSON(raw []byte) ([]int, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var doc struct {
		Nftables []map[string]json.RawMessage `json:"nftables"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse blocked ports output: %w", err)
	}
	for _, entry := range doc.Nftables {
		rawSet, ok := entry["set"]
		if !ok {
			continue
		}
		var set struct {
			Name string            `json:"name"`
			Elem []json.RawMessage `json:"elem"`
		}
		if err := json.Unmarshal(rawSet, &set); err != nil {
			return nil, fmt.Errorf("parse blocked ports set: %w", err)
		}
		if set.Name != pauseSetName {
			continue
		}
		ports := make([]int, 0, len(set.Elem))
		for _, rawElem := range set.Elem {
			var port int
			if err := json.Unmarshal(rawElem, &port); err != nil {
				return nil, fmt.Errorf("blocked ports set holds a non-port element: %s", string(rawElem))
			}
			if port < 1 || port > 65535 {
				return nil, fmt.Errorf("blocked ports set holds an out-of-range port: %d", port)
			}
			ports = append(ports, port)
		}
		sort.Ints(ports)
		return ports, nil
	}
	return nil, errors.New("blocked ports set not found in nft output")
}

func (m *Manager) ListBlockedPorts(ctx context.Context, cfg SSHConfig) ([]int, error) {
	if err := m.ensureInitialized(); err != nil {
		return nil, err
	}
	runner, ok := m.runner.(interface {
		ListBlockedPorts(context.Context, SSHConfig) ([]int, error)
	})
	if !ok {
		return nil, errors.New("nftables runner does not support listing blocked ports")
	}
	return runner.ListBlockedPorts(ctx, cfg)
}
