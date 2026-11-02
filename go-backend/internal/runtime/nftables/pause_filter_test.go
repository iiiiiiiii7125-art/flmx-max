package nftables

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// 暂停即断流的核心在 transit/outgoing 两条链：按 conntrack 原始目的
// 端口匹配，DNAT 规则已移除时已建立连接的后续包仍被 drop。
func TestRenderPauseTableDropsEstablishedFlows(t *testing.T) {
	table := RenderPauseTable()
	for _, want := range []string{
		"table inet flmx_quota {",
		"set blocked_ports { type inet_service; }",
		"chain incoming {",
		"type filter hook prerouting priority -110; policy accept;",
		"fib daddr type local meta l4proto { tcp, udp } th dport @blocked_ports drop",
		"chain transit {",
		"type filter hook forward priority -10; policy accept;",
		"chain outgoing {",
		"type filter hook output priority -10; policy accept;",
	} {
		if !strings.Contains(table, want) {
			t.Fatalf("pause table missing %q:\n%s", want, table)
		}
	}
	if got := strings.Count(table, "ct status dnat meta l4proto { tcp, udp } ct original proto-dst @blocked_ports drop"); got != 2 {
		t.Fatalf("expected established-flow drop in both transit and outgoing chains, got %d:\n%s", got, table)
	}
	// 暂停表不得与主 NAT 表同名或互相包含：主表重建不能带走封禁集，
	// 封禁切换也不能重置主表计数器。
	if strings.Contains(table, "type nat hook") {
		t.Fatalf("pause table must not contain NAT chains:\n%s", table)
	}
}

func TestBuildSetBlockedCommandBlockCreatesTableAndAddsPort(t *testing.T) {
	cmd := buildSetBlockedCommand("nft", 20000, true)
	for _, want := range []string{
		"if ! nft list table inet flmx_quota >/dev/null 2>&1; then",
		"nft -f - <<'EOF'",
		"table inet flmx_quota {",
		"ct original proto-dst @blocked_ports drop",
		"nft add element inet flmx_quota blocked_ports '{ 20000 }'",
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("block command missing %q:\n%s", want, cmd)
		}
	}
	if strings.Contains(cmd, "delete element") {
		t.Fatalf("block command must not delete elements:\n%s", cmd)
	}
	// 建表必须排在加元素之前。
	if strings.Index(cmd, "table inet flmx_quota {") > strings.Index(cmd, "nft add element") {
		t.Fatalf("table creation must precede element add:\n%s", cmd)
	}
}

func TestBuildSetBlockedCommandUnblockDeletesOnlyExistingPort(t *testing.T) {
	cmd := buildSetBlockedCommand("nft", 20001, false)
	for _, want := range []string{
		"if nft list table inet flmx_quota >/dev/null 2>&1; then",
		"if nft get element inet flmx_quota blocked_ports '{ 20001 }' >/dev/null 2>&1; then",
		"nft delete element inet flmx_quota blocked_ports '{ 20001 }'",
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("unblock command missing %q:\n%s", want, cmd)
		}
	}
	// 恢复路径不建表、不加元素：无表/无元素时安静成功，规则清零不残留。
	if strings.Contains(cmd, "add element") || strings.Contains(cmd, "table inet flmx_quota {") {
		t.Fatalf("unblock command must not create the table or add elements:\n%s", cmd)
	}
}

func TestBuildClearBlocksCommandDeletesWholeTable(t *testing.T) {
	cmd := buildClearBlocksCommand("nft")
	for _, want := range []string{
		"if nft list table inet flmx_quota >/dev/null 2>&1; then",
		"nft delete table inet flmx_quota",
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("clear-blocks command missing %q:\n%s", want, cmd)
		}
	}
}

func TestSSHRunnerSetBlockedRejectsInvalidPort(t *testing.T) {
	runner := NewSSHRunner()
	for _, port := range []int{0, -1, 65536} {
		if err := runner.SetBlocked(context.Background(), SSHConfig{}, port, true); err == nil {
			t.Fatalf("expected error for port %d", port)
		}
	}
}

type blockingRunner struct {
	fakeRunner
	blockedCalls     []fakeBlockedCall
	clearBlocksCalls int
	clearBlocksErr   error
}

type fakeBlockedCall struct {
	port    int
	blocked bool
}

func (f *blockingRunner) SetBlocked(_ context.Context, _ SSHConfig, port int, blocked bool) error {
	f.blockedCalls = append(f.blockedCalls, fakeBlockedCall{port: port, blocked: blocked})
	return nil
}

func (f *blockingRunner) ClearBlocks(_ context.Context, _ SSHConfig) error {
	f.clearBlocksCalls++
	return f.clearBlocksErr
}

func TestManagerSetBlockedDelegatesToRunner(t *testing.T) {
	runner := &blockingRunner{}
	manager := NewManager(runner)
	if err := manager.SetBlocked(context.Background(), SSHConfig{}, 20000, true); err != nil {
		t.Fatalf("SetBlocked: %v", err)
	}
	if len(runner.blockedCalls) != 1 || runner.blockedCalls[0] != (fakeBlockedCall{port: 20000, blocked: true}) {
		t.Fatalf("unexpected blocked calls: %+v", runner.blockedCalls)
	}
}

func TestManagerSetBlockedUnsupportedRunner(t *testing.T) {
	manager := NewManager(&fakeRunner{})
	if err := manager.SetBlocked(context.Background(), SSHConfig{}, 20000, true); err == nil {
		t.Fatalf("expected error when runner lacks blocking support")
	}
}

func TestManagerClearAlsoClearsBlocks(t *testing.T) {
	runner := &blockingRunner{}
	manager := NewManager(runner)
	if err := manager.Clear(context.Background(), SSHConfig{}); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if len(runner.scripts) != 1 {
		t.Fatalf("expected empty main table applied once, got %d scripts", len(runner.scripts))
	}
	if runner.clearBlocksCalls != 1 {
		t.Fatalf("expected ClearBlocks once, got %d", runner.clearBlocksCalls)
	}
}

func TestManagerClearPropagatesClearBlocksError(t *testing.T) {
	runner := &blockingRunner{clearBlocksErr: errors.New("delete table failed")}
	manager := NewManager(runner)
	if err := manager.Clear(context.Background(), SSHConfig{}); err == nil {
		t.Fatalf("expected ClearBlocks error to propagate")
	}
}
