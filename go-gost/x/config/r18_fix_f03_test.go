package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// R18-F03: 启动自愈从仅 Services（R17-A）扩展到 chains / limiters /
// climiters。毒化 gost.json 里同名链/限流器各两份时，未经去重进入
// loader.Load 会在第二个同名项的 Register 处报 registry: duplicate
// object、program.Start 返回错误、agent 退出——与 R17-X-01 的服务毒化
// 同型（见 x/config/loader/r18_fix_f03_test.go）。去重口径与
// DeduplicateServices 一致：保留首次出现，去掉后续同名项（名按 TrimSpace
// 后比较），nil 项原样保留。

func TestDeduplicateChainsRemovesDuplicates(t *testing.T) {
	cfg := &Config{Chains: []*ChainConfig{
		{Name: "chain-a"},
		nil,
		{Name: "chain-b"},
		{Name: " chain-a "},
		{Name: "chain-a"},
	}}
	removed := DeduplicateChains(cfg)
	if len(removed) != 2 || removed[0] != " chain-a " || removed[1] != "chain-a" {
		t.Fatalf("removed = %v, want [\" chain-a \" \"chain-a\"]", removed)
	}
	if len(cfg.Chains) != 3 {
		t.Fatalf("expected 3 chains after dedupe, got %d", len(cfg.Chains))
	}
	if cfg.Chains[0] == nil || cfg.Chains[0].Name != "chain-a" {
		t.Fatalf("first occurrence of chain-a must be kept at index 0, got %#v", cfg.Chains[0])
	}
	if cfg.Chains[1] != nil {
		t.Fatalf("nil chain entry must be kept as-is, got %#v", cfg.Chains[1])
	}
	if cfg.Chains[2] == nil || cfg.Chains[2].Name != "chain-b" {
		t.Fatalf("chain-b must be kept at index 2, got %#v", cfg.Chains[2])
	}
}

func TestDeduplicateChainsNoDuplicatesIsNoop(t *testing.T) {
	cfg := &Config{Chains: []*ChainConfig{{Name: "a"}, nil, {Name: "b"}}}
	if removed := DeduplicateChains(cfg); removed != nil {
		t.Fatalf("removed = %v, want nil", removed)
	}
	if len(cfg.Chains) != 3 {
		t.Fatalf("expected 3 chains, got %d", len(cfg.Chains))
	}
	if removed := DeduplicateChains(nil); removed != nil {
		t.Fatalf("DeduplicateChains(nil) = %v, want nil", removed)
	}
}

func TestDeduplicateLimitersRemovesDuplicates(t *testing.T) {
	cfg := &Config{Limiters: []*LimiterConfig{
		{Name: "lim-a"},
		{Name: "lim-b"},
		{Name: "lim-a"},
		nil,
	}}
	removed := DeduplicateLimiters(cfg)
	if len(removed) != 1 || removed[0] != "lim-a" {
		t.Fatalf("removed = %v, want [lim-a]", removed)
	}
	if len(cfg.Limiters) != 3 {
		t.Fatalf("expected 3 limiters after dedupe, got %d", len(cfg.Limiters))
	}
	if cfg.Limiters[2] != nil {
		t.Fatalf("nil limiter entry must be kept as-is, got %#v", cfg.Limiters[2])
	}
	if removed := DeduplicateLimiters(nil); removed != nil {
		t.Fatalf("DeduplicateLimiters(nil) = %v, want nil", removed)
	}
}

func TestDeduplicateCLimitersRemovesDuplicates(t *testing.T) {
	cfg := &Config{CLimiters: []*LimiterConfig{
		{Name: "clim-a"},
		{Name: "clim-a"},
		{Name: "clim-b"},
	}}
	removed := DeduplicateCLimiters(cfg)
	if len(removed) != 1 || removed[0] != "clim-a" {
		t.Fatalf("removed = %v, want [clim-a]", removed)
	}
	if len(cfg.CLimiters) != 2 {
		t.Fatalf("expected 2 conn limiters after dedupe, got %d", len(cfg.CLimiters))
	}
	if removed := DeduplicateCLimiters(nil); removed != nil {
		t.Fatalf("DeduplicateCLimiters(nil) = %v, want nil", removed)
	}
}

// R18-F03: HealPersistedChains / Limiters / CLimiters 与
// HealPersistedServices 同口径——去重后立即重新落盘，干净配置不触发写。
func TestHealPersistedChainsLimiters(t *testing.T) {
	cfg := &Config{
		Chains: []*ChainConfig{
			{Name: "chain-a"},
			{Name: "chain-a"},
			{Name: "chain-b"},
		},
		Limiters: []*LimiterConfig{
			{Name: "lim-a"},
			{Name: "lim-a"},
		},
		CLimiters: []*LimiterConfig{
			{Name: "clim-a"},
			{Name: "clim-a"},
			{Name: "clim-b"},
		},
	}

	originalPath := persistPath
	originalEnabled := persistEnable
	persistFile := filepath.Join(t.TempDir(), "gost.json")
	t.Cleanup(func() {
		persistPath = originalPath
		persistEnable = originalEnabled
	})
	persistPath = persistFile
	persistEnable = false // Heal 不依赖 EnablePersist

	removedChains, err := HealPersistedChains(cfg)
	if err != nil {
		t.Fatalf("HealPersistedChains: %v", err)
	}
	if len(removedChains) != 1 || removedChains[0] != "chain-a" {
		t.Fatalf("removedChains = %v, want [chain-a]", removedChains)
	}
	removedLimiters, err := HealPersistedLimiters(cfg)
	if err != nil {
		t.Fatalf("HealPersistedLimiters: %v", err)
	}
	if len(removedLimiters) != 1 || removedLimiters[0] != "lim-a" {
		t.Fatalf("removedLimiters = %v, want [lim-a]", removedLimiters)
	}
	removedCLimiters, err := HealPersistedCLimiters(cfg)
	if err != nil {
		t.Fatalf("HealPersistedCLimiters: %v", err)
	}
	if len(removedCLimiters) != 1 || removedCLimiters[0] != "clim-a" {
		t.Fatalf("removedCLimiters = %v, want [clim-a]", removedCLimiters)
	}
	if len(cfg.Chains) != 2 || len(cfg.Limiters) != 1 || len(cfg.CLimiters) != 2 {
		t.Fatalf("expected 2 chains / 1 limiter / 2 conn limiters after heal, got %d / %d / %d",
			len(cfg.Chains), len(cfg.Limiters), len(cfg.CLimiters))
	}

	data, err := os.ReadFile(persistFile)
	if err != nil {
		t.Fatalf("healed config was not persisted: %v", err)
	}
	var onDisk Config
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatalf("unmarshal persisted config: %v", err)
	}
	chainNames := []string{}
	for _, ch := range onDisk.Chains {
		if ch != nil {
			chainNames = append(chainNames, ch.Name)
		}
	}
	seen := map[string]int{}
	for _, n := range chainNames {
		seen[n]++
	}
	for name, n := range seen {
		if n != 1 {
			t.Fatalf("persisted chain %q appears %d times, want 1", name, n)
		}
	}
	if len(onDisk.Chains) != 2 || len(onDisk.Limiters) != 1 || len(onDisk.CLimiters) != 2 {
		t.Fatalf("expected 2 chains / 1 limiter / 2 conn limiters on disk, got %d / %d / %d",
			len(onDisk.Chains), len(onDisk.Limiters), len(onDisk.CLimiters))
	}
}

func TestHealPersistedChainsLimitersCleanIsNoop(t *testing.T) {
	originalPath := persistPath
	dir := t.TempDir()
	t.Cleanup(func() { persistPath = originalPath })
	persistPath = filepath.Join(dir, "gost.json")

	cfg := &Config{
		Chains:    []*ChainConfig{{Name: "a"}},
		Limiters:  []*LimiterConfig{{Name: "b"}},
		CLimiters: []*LimiterConfig{{Name: "c"}},
	}
	if removed, err := HealPersistedChains(cfg); err != nil || removed != nil {
		t.Fatalf("HealPersistedChains(clean) = %v, %v, want nil, nil", removed, err)
	}
	if removed, err := HealPersistedLimiters(cfg); err != nil || removed != nil {
		t.Fatalf("HealPersistedLimiters(clean) = %v, %v, want nil, nil", removed, err)
	}
	if removed, err := HealPersistedCLimiters(cfg); err != nil || removed != nil {
		t.Fatalf("HealPersistedCLimiters(clean) = %v, %v, want nil, nil", removed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "gost.json")); err == nil {
		t.Fatal("clean config must not trigger a persist write")
	}
}
