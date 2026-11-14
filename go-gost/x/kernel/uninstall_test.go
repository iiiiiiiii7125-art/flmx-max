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
