package kernel

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Uninstall 必须删掉 agent 建的全部 nftables 表（主表、旧名表、
// 端口跳跃探针表）：这些表驻留在内核里，进程退出不会带走它们。
func TestManagerUninstallRemovesAgentNftTables(t *testing.T) {
	var script string
	orig := nftRunner
	nftRunner = func(ctx context.Context, stdin string, args ...string) ([]byte, error) {
		script = stdin
		return nil, nil
	}
	t.Cleanup(func() { nftRunner = orig })

	m := NewManager(t.TempDir())
	if err := m.Uninstall(); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	for _, table := range []string{nftTable, legacyNFTTable, nftHopProbeTable} {
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
