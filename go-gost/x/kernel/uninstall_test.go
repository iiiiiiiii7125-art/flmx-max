package kernel

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Uninstall 必须删掉 agent 建的全部 nftables 表（自己的主表、
// 端口跳跃探针表）：这些表驻留在内核里，进程退出不会带走它们。
// F2-NFT-01: 删表只删自己的——非 canonical 安装的卸载脚本不得
// 提及共享表名（它可能是另一 agent 的 live 表）；canonical 安装
// 的卸载仍删旧名表。
func TestManagerUninstallRemovesAgentNftTables(t *testing.T) {
	var script string
	orig := nftRunner
	nftRunner = func(ctx context.Context, stdin string, args ...string) ([]byte, error) {
		script = stdin
		return nil, nil
	}
	t.Cleanup(func() { nftRunner = orig })

	// 非 canonical 安装（scratch/自定义目录）：只删自己的表＋探针表。
	m := NewManager(t.TempDir())
	if err := m.Uninstall(); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	for _, table := range []string{m.nft.tableName(), nftHopProbeTable} {
		if !strings.Contains(script, "delete table inet "+table) {
			t.Fatalf("uninstall script must delete table %s, got:\n%s", table, script)
		}
	}
	for _, table := range []string{nftTable, legacyNFTTable, legacyAgentNFTTable} {
		if strings.Contains(script, "delete table inet "+table+"\n") {
			t.Fatalf("isolated uninstall script must not delete shared table %s, got:\n%s", table, script)
		}
	}

	// Canonical 安装：主表＋旧名表＋legacy 代表＋探针表全删
	// （F-R4-02：legacy 代的表由 canonical 卸载一并清）。
	script = ""
	mc := NewManager(defaultBaseDir)
	if err := mc.Uninstall(); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	for _, table := range []string{nftTable, legacyNFTTable, legacyAgentNFTTable, nftHopProbeTable} {
		if !strings.Contains(script, "delete table inet "+table) {
			t.Fatalf("uninstall script must delete table %s, got:\n%s", table, script)
		}
	}
}

// nft 执行失败要如实上抛，卸载方据此告警（而不是误以为表已清）。
func TestManagerUninstallPropagatesNftError(t *testing.T) {
	orig := nftRunner
	nftRunner = func(ctx context.Context, stdin string, args ...string) ([]byte, error) {
		return nil, errors.New("nft unavailable")
	}
	t.Cleanup(func() { nftRunner = orig })

	m := NewManager(t.TempDir())
	if err := m.Uninstall(); err == nil {
		t.Fatal("expected the nft error to propagate")
	}
}

// R2C-02：优雅退出回收自家 nft 表——只删自己的表，legacy 表与探针
// 表不动（legacy agent 活着时那是它的 live 表；探针表按需重建）。
func TestManagerShutdownRemovesOwnNftTable(t *testing.T) {
	var scripts []string
	orig := nftRunner
	nftRunner = func(ctx context.Context, stdin string, args ...string) ([]byte, error) {
		scripts = append(scripts, stdin)
		return nil, nil
	}
	t.Cleanup(func() { nftRunner = orig })

	// 非 canonical 安装：只删自己的后缀表。
	m := NewManager(t.TempDir())
	m.Shutdown()
	if len(scripts) != 1 {
		t.Fatalf("shutdown must run exactly one nft script, got %d", len(scripts))
	}
	script := scripts[0]
	if !strings.Contains(script, "delete table inet "+m.nft.tableName()) {
		t.Fatalf("shutdown script must delete own table %q, got:\n%s", m.nft.tableName(), script)
	}
	for _, table := range []string{nftTable, legacyNFTTable, legacyAgentNFTTable, nftHopProbeTable} {
		if strings.Contains(script, "delete table inet "+table+"\n") {
			t.Fatalf("shutdown script must not delete %q, got:\n%s", table, script)
		}
	}

	// Canonical 安装：删 plain 主表，legacy 表仍不动。
	scripts = nil
	mc := NewManager(defaultBaseDir)
	mc.Shutdown()
	if len(scripts) != 1 {
		t.Fatalf("canonical shutdown must run exactly one nft script, got %d", len(scripts))
	}
	script = scripts[0]
	if !strings.Contains(script, "delete table inet "+nftTable+"\n") {
		t.Fatalf("canonical shutdown script must delete %q, got:\n%s", nftTable, script)
	}
	for _, table := range []string{legacyNFTTable, legacyAgentNFTTable, nftHopProbeTable} {
		if strings.Contains(script, "delete table inet "+table+"\n") {
			t.Fatalf("canonical shutdown script must not delete %q, got:\n%s", table, script)
		}
	}
}

// R2C-02：nft 回收失败不得阻塞退出——Shutdown 无返回值，失败只记日志。
func TestManagerShutdownToleratesNftError(t *testing.T) {
	orig := nftRunner
	nftRunner = func(ctx context.Context, stdin string, args ...string) ([]byte, error) {
		return nil, errors.New("nft unavailable")
	}
	t.Cleanup(func() { nftRunner = orig })

	m := NewManager(t.TempDir())
	m.Shutdown() // must not panic or hang
}
