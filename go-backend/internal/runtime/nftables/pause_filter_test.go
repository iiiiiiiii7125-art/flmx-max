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
	listBlockedPorts []int
	listBlockedErr   error
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

func (f *blockingRunner) ListBlockedPorts(_ context.Context, _ SSHConfig) ([]int, error) {
	return f.listBlockedPorts, f.listBlockedErr
}

func TestBuildListBlockedPortsCommand(t *testing.T) {
	cmd := buildListBlockedPortsCommand("nft")
	for _, want := range []string{
		"if nft list table inet flmx_quota >/dev/null 2>&1; then",
		"nft -j list set inet flmx_quota blocked_ports",
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("list blocked ports command missing %q:\n%s", want, cmd)
		}
	}
	// 读面不得改动集合：不建表、不增删元素。
	for _, bad := range []string{"add element", "delete element", "table inet flmx_quota {"} {
		if strings.Contains(cmd, bad) {
			t.Fatalf("list command must not mutate the set (%q):\n%s", bad, cmd)
		}
	}
}

func TestParseBlockedPortsJSON(t *testing.T) {
	withElems := []byte(`{"nftables": [{"metainfo": {"version": "1.0.6", "json_schema_version": 1}}, {"set": {"family": "inet", "table": "flmx_quota", "name": "blocked_ports", "handle": 2, "type": "inet_service", "elem": [45002, 45001]}}]}`)
	ports, err := ParseBlockedPortsJSON(withElems)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(ports) != 2 || ports[0] != 45001 || ports[1] != 45002 {
		t.Fatalf("expected sorted [45001 45002], got %v", ports)
	}

	emptySet := []byte(`{"nftables": [{"metainfo": {"version": "1.0.6"}}, {"set": {"family": "inet", "table": "flmx_quota", "name": "blocked_ports", "handle": 2, "type": "inet_service"}}]}`)
	ports, err = ParseBlockedPortsJSON(emptySet)
	if err != nil || len(ports) != 0 {
		t.Fatalf("empty set must parse to no ports, got %v err=%v", ports, err)
	}

	// 表不存在时命令无输出：空集而非错误。
	ports, err = ParseBlockedPortsJSON(nil)
	if err != nil || len(ports) != 0 {
		t.Fatalf("empty output must parse to no ports, got %v err=%v", ports, err)
	}

	// 异形元素（区间）拒绝：集合被外部改动时整轮跳过，不猜测。
	interval := []byte(`{"nftables": [{"set": {"family": "inet", "table": "flmx_quota", "name": "blocked_ports", "type": "inet_service", "elem": [{"range": [45001, 45010]}]}}]}`)
	if _, err := ParseBlockedPortsJSON(interval); err == nil {
		t.Fatal("interval element must be rejected")
	}
	if _, err := ParseBlockedPortsJSON([]byte("not json")); err == nil {
		t.Fatal("malformed output must be rejected")
	}
	if _, err := ParseBlockedPortsJSON([]byte(`{"nftables": []}`)); err == nil {
		t.Fatal("missing set entry must be rejected")
	}
}

func TestManagerListBlockedPortsDelegatesToRunner(t *testing.T) {
	runner := &blockingRunner{listBlockedPorts: []int{45001}}
	manager := NewManager(runner)
	ports, err := manager.ListBlockedPorts(context.Background(), SSHConfig{})
	if err != nil {
		t.Fatalf("ListBlockedPorts: %v", err)
	}
	if len(ports) != 1 || ports[0] != 45001 {
		t.Fatalf("unexpected ports: %v", ports)
	}
}

func TestManagerListBlockedPortsUnsupportedRunner(t *testing.T) {
	manager := NewManager(&fakeRunner{})
	if _, err := manager.ListBlockedPorts(context.Background(), SSHConfig{}); err == nil {
		t.Fatal("expected error when runner lacks blocked-port listing")
	}
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
