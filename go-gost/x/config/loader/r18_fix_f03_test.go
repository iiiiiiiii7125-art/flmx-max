package loader_test

import (
	"testing"

	"github.com/go-gost/x/config"
	"github.com/go-gost/x/config/loader"
	_ "github.com/go-gost/x/connector/relay"
	_ "github.com/go-gost/x/dialer/tcp"
	"github.com/go-gost/x/registry"
)

// R18-F03: 启动失败点定位——毒化 gost.json（同名链/限流器各两份）未经去重
// 进入 loader.Load 时，第二个同名项在 Register 处报 registry: duplicate
// object，program.Start 直接返回错误、agent 退出（与 R17-X-01 的服务毒化
// 同型；链/限流器无监听器 eager 绑定，故只有 duplicate object 这一支）。
// 去重后的同一配置必须能加载。
func poisonedChainsLimitersConfig() *config.Config {
	validChain := func(name string) *config.ChainConfig {
		return &config.ChainConfig{
			Name: name,
			Hops: []*config.HopConfig{{
				Name: "hop-" + name,
				Nodes: []*config.NodeConfig{{
					Name:      "node-" + name,
					Addr:      "127.0.0.1:443",
					Connector: &config.ConnectorConfig{Type: "relay"},
					Dialer:    &config.DialerConfig{Type: "tcp"},
				}},
			}},
		}
	}
	return &config.Config{
		Chains: []*config.ChainConfig{
			validChain("r18f03-chain"),
			validChain("r18f03-chain"),
		},
		Limiters: []*config.LimiterConfig{
			{Name: "r18f03-limiter", Limits: []string{"$ 1"}},
			{Name: "r18f03-limiter", Limits: []string{"$ 1"}},
		},
		CLimiters: []*config.LimiterConfig{
			{Name: "r18f03-climiter", Limits: []string{"$ 1"}},
			{Name: "r18f03-climiter", Limits: []string{"$ 1"}},
		},
	}
}

func cleanupR18F03Registries() {
	for name := range registry.ChainRegistry().GetAll() {
		registry.ChainRegistry().Unregister(name)
	}
	for name := range registry.TrafficLimiterRegistry().GetAll() {
		registry.TrafficLimiterRegistry().Unregister(name)
	}
	for name := range registry.ConnLimiterRegistry().GetAll() {
		registry.ConnLimiterRegistry().Unregister(name)
	}
}

func TestLoadPoisonedChainsLimitersFailsWithDuplicate(t *testing.T) {
	unlock := config.LockMutation()
	defer unlock()
	defer cleanupR18F03Registries()

	cfg := poisonedChainsLimitersConfig()
	err := loader.Load(cfg)
	if err == nil {
		t.Fatal("expected loader.Load to fail on duplicate chain/limiter names")
	}
	if err != registry.ErrDup {
		t.Fatalf("expected registry.ErrDup, got: %v", err)
	}
	t.Logf("loader.Load(poisoned chains/limiters) error (startup failure point): %v", err)
}

func TestLoadHealedChainsLimitersSucceeds(t *testing.T) {
	unlock := config.LockMutation()
	defer unlock()
	defer cleanupR18F03Registries()

	cfg := poisonedChainsLimitersConfig()
	if removed := config.DeduplicateChains(cfg); len(removed) != 1 || removed[0] != "r18f03-chain" {
		t.Fatalf("DeduplicateChains removed = %v, want [r18f03-chain]", removed)
	}
	if removed := config.DeduplicateLimiters(cfg); len(removed) != 1 || removed[0] != "r18f03-limiter" {
		t.Fatalf("DeduplicateLimiters removed = %v, want [r18f03-limiter]", removed)
	}
	if removed := config.DeduplicateCLimiters(cfg); len(removed) != 1 || removed[0] != "r18f03-climiter" {
		t.Fatalf("DeduplicateCLimiters removed = %v, want [r18f03-climiter]", removed)
	}
	if err := loader.Load(cfg); err != nil {
		t.Fatalf("loader.Load(healed config): %v", err)
	}
	if registry.ChainRegistry().Get("r18f03-chain") == nil {
		t.Fatal("chain r18f03-chain not registered after healed load")
	}
	if registry.TrafficLimiterRegistry().Get("r18f03-limiter") == nil {
		t.Fatal("limiter r18f03-limiter not registered after healed load")
	}
	if registry.ConnLimiterRegistry().Get("r18f03-climiter") == nil {
		t.Fatal("conn limiter r18f03-climiter not registered after healed load")
	}
}
