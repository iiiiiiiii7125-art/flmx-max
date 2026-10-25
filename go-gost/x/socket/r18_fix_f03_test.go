package socket

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	corelogger "github.com/go-gost/core/logger"
	"github.com/go-gost/x/config"
	_ "github.com/go-gost/x/connector/relay"
	_ "github.com/go-gost/x/dialer/tcp"
	xlogger "github.com/go-gost/x/logger"
	"github.com/go-gost/x/registry"
)

// R18-F03: createChain 曾对 c.Chains 做无去重 append（chain.go:35）。链已在
// 落盘配置中、但不在注册表里时（与 R17-X-01 的服务漂移同型），面板一次重复
// AddChain 下发就会把第二份同名链写进配置并落盘，毒化 gost.json；启动时
// loader.Load 又会在第二个同名链的 Register 处报 registry: duplicate
// object、agent 退出。修复后：同名则替换（与 updateChain 同口径），配置里
// 已有的多余同名副本一并清除，nil 项原样保留不 panic。
func TestCreateChainDedupesExistingConfigEntry(t *testing.T) {
	corelogger.SetDefault(xlogger.Nop())

	name := "r18f03_dup_chain_tdd"
	stale := config.ChainConfig{Name: name}
	originalConfig := config.Global()
	originalPersistPath := config.PersistPath()
	dir := t.TempDir()
	persistFile := filepath.Join(dir, "gost.json")
	t.Cleanup(func() {
		config.Set(originalConfig)
		config.SetPersistPath(originalPersistPath)
		registry.ChainRegistry().Unregister(name)
	})
	registry.ChainRegistry().Unregister(name)

	// 陈旧配置项：已在落盘配置中，但不在注册表里。
	config.Set(&config.Config{Chains: []*config.ChainConfig{&stale}})
	config.SetPersistPath(persistFile)
	config.EnablePersist()

	fresh := config.ChainConfig{
		Name: name,
		Hops: []*config.HopConfig{{
			Name: "hop-fresh",
			Nodes: []*config.NodeConfig{{
				Name:      "node-fresh",
				Addr:      "127.0.0.1:443",
				Connector: &config.ConnectorConfig{Type: "relay"},
				Dialer:    &config.DialerConfig{Type: "tcp"},
			}},
		}},
	}
	if err := createChain(createChainRequest{Data: fresh}); err != nil {
		t.Fatalf("createChain: %v", err)
	}

	// 内存配置：同名只留一份，且为新下发的内容（替换，非跳过）。
	cfg := config.Global()
	count := 0
	for _, ch := range cfg.Chains {
		if ch != nil && ch.Name == name {
			count++
			if len(ch.Hops) != 1 || ch.Hops[0] == nil || ch.Hops[0].Name != "hop-fresh" {
				t.Fatalf("expected in-memory chain %q to be the fresh content, got %#v", name, ch)
			}
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 in-memory entry for %q, got %d: %#v", name, count, cfg.Chains)
	}

	// 落盘配置：无重复名。
	data, err := os.ReadFile(persistFile)
	if err != nil {
		t.Fatalf("read persisted config: %v", err)
	}
	var onDisk config.Config
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatalf("unmarshal persisted config: %v", err)
	}
	diskCount := 0
	for _, ch := range onDisk.Chains {
		if ch != nil && ch.Name == name {
			diskCount++
		}
	}
	if diskCount != 1 {
		t.Fatalf("expected exactly 1 persisted entry for %q, got %d", name, diskCount)
	}
}

// R18-F03: createLimiter 曾对 c.Limiters 做无去重 append（limiter.go:29）。
// 修复后同名则替换（与 updateLimiter 同口径），多余副本清除，nil 项保留。
func TestCreateLimiterDedupesExistingConfigEntry(t *testing.T) {
	corelogger.SetDefault(xlogger.Nop())

	name := "r18f03_dup_limiter_tdd"
	stale := config.LimiterConfig{Name: name, Limits: []string{"$ 1"}}
	originalConfig := config.Global()
	originalPersistPath := config.PersistPath()
	dir := t.TempDir()
	persistFile := filepath.Join(dir, "gost.json")
	t.Cleanup(func() {
		config.Set(originalConfig)
		config.SetPersistPath(originalPersistPath)
		registry.TrafficLimiterRegistry().Unregister(name)
	})
	registry.TrafficLimiterRegistry().Unregister(name)

	config.Set(&config.Config{Limiters: []*config.LimiterConfig{&stale}})
	config.SetPersistPath(persistFile)
	config.EnablePersist()

	fresh := config.LimiterConfig{Name: name, Limits: []string{"$ 2"}}
	if err := createLimiter(createLimiterRequest{Data: fresh}); err != nil {
		t.Fatalf("createLimiter: %v", err)
	}

	cfg := config.Global()
	count := 0
	for _, lim := range cfg.Limiters {
		if lim != nil && lim.Name == name {
			count++
			if len(lim.Limits) != 1 || lim.Limits[0] != "$ 2" {
				t.Fatalf("expected in-memory limiter %q to be the fresh content, got %#v", name, lim)
			}
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 in-memory entry for %q, got %d: %#v", name, count, cfg.Limiters)
	}

	data, err := os.ReadFile(persistFile)
	if err != nil {
		t.Fatalf("read persisted config: %v", err)
	}
	var onDisk config.Config
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatalf("unmarshal persisted config: %v", err)
	}
	diskCount := 0
	for _, lim := range onDisk.Limiters {
		if lim != nil && lim.Name == name {
			diskCount++
		}
	}
	if diskCount != 1 {
		t.Fatalf("expected exactly 1 persisted entry for %q, got %d", name, diskCount)
	}
}

// R18-F03: createConnLimiter 曾对 c.CLimiters 做无去重 append（limiter.go:118）。
// 修复后同名则替换（与 updateConnLimiter 同口径），多余副本清除，nil 项保留。
func TestCreateConnLimiterDedupesExistingConfigEntry(t *testing.T) {
	corelogger.SetDefault(xlogger.Nop())

	name := "r18f03_dup_climiter_tdd"
	stale := config.LimiterConfig{Name: name, Limits: []string{"$ 1"}}
	originalConfig := config.Global()
	originalPersistPath := config.PersistPath()
	dir := t.TempDir()
	persistFile := filepath.Join(dir, "gost.json")
	t.Cleanup(func() {
		config.Set(originalConfig)
		config.SetPersistPath(originalPersistPath)
		registry.ConnLimiterRegistry().Unregister(name)
	})
	registry.ConnLimiterRegistry().Unregister(name)

	config.Set(&config.Config{CLimiters: []*config.LimiterConfig{&stale}})
	config.SetPersistPath(persistFile)
	config.EnablePersist()

	fresh := config.LimiterConfig{Name: name, Limits: []string{"$ 2"}}
	if err := createConnLimiter(createLimiterRequest{Data: fresh}); err != nil {
		t.Fatalf("createConnLimiter: %v", err)
	}

	cfg := config.Global()
	count := 0
	for _, lim := range cfg.CLimiters {
		if lim != nil && lim.Name == name {
			count++
			if len(lim.Limits) != 1 || lim.Limits[0] != "$ 2" {
				t.Fatalf("expected in-memory conn limiter %q to be the fresh content, got %#v", name, lim)
			}
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 in-memory entry for %q, got %d: %#v", name, count, cfg.CLimiters)
	}

	data, err := os.ReadFile(persistFile)
	if err != nil {
		t.Fatalf("read persisted config: %v", err)
	}
	var onDisk config.Config
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatalf("unmarshal persisted config: %v", err)
	}
	diskCount := 0
	for _, lim := range onDisk.CLimiters {
		if lim != nil && lim.Name == name {
			diskCount++
		}
	}
	if diskCount != 1 {
		t.Fatalf("expected exactly 1 persisted entry for %q, got %d", name, diskCount)
	}
}
