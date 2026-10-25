// R19-F02: config.OnUpdate 非 defer 解锁（放大器）回归。
//
// 背景：R19-X-02——OnUpdate 用 `Lock(); err := f(global); Unlock()`，
// 闭包内 panic 时写锁永不释放；panic 被 routeCommandSafely recover 后，
// 后续 config.Set/Global/OnUpdate 全部永久阻塞（L2 实测 config.Set
// 死锁、30s 超时转储）。修复后：闭包在内层函数中执行、defer 解锁，
// panic 后锁必释放；persist() 仍在解锁后执行（persist 经 Global()
// 取读锁，与写锁同持会自死锁，故解锁顺序不得变）。
package config

import (
	"testing"
	"time"
)

// R19-F02: StripNilConfigItems——全部 17 类的 nil 项剥离。
//
// 口径：只剥离 nil，保留所有有效项与顺序；无 nil 时返回 nil、不改动
// 配置；nil cfg 返回 nil。Deduplicate* 的“nil 项原样保留”口径不受
// 影响（由 r18_fix_f03_test.go 断言）。
func TestR19F02StripNilConfigItems(t *testing.T) {
	cfg := &Config{
		Services:   []*ServiceConfig{nil, {Name: "svc-a"}, nil, {Name: "svc-b"}},
		Chains:     []*ChainConfig{{Name: "chain-a"}, nil},
		Hops:       []*HopConfig{nil},
		Authers:    []*AutherConfig{{Name: "auther-a"}, nil},
		Admissions: []*AdmissionConfig{nil},
		Bypasses:   []*BypassConfig{nil, {Name: "bypass-a"}},
		Resolvers:  []*ResolverConfig{nil},
		Hosts:      []*HostsConfig{nil},
		Ingresses:  []*IngressConfig{nil},
		Routers:    []*RouterConfig{nil},
		SDs:        []*SDConfig{nil},
		Observers:  []*ObserverConfig{nil},
		Recorders:  []*RecorderConfig{nil},
		Limiters:   []*LimiterConfig{nil, {Name: "lim-a"}},
		CLimiters:  []*LimiterConfig{nil},
		RLimiters:  []*LimiterConfig{nil},
		Loggers:    []*LoggerConfig{nil, {Name: "log-a"}},
	}
	stripped := StripNilConfigItems(cfg)
	if len(stripped) != 17 {
		t.Fatalf("expected 17 kind descriptions, got %d: %v", len(stripped), stripped)
	}
	if n := countNilsForTest(cfg); n != 0 {
		t.Fatalf("expected 0 nil items after strip, got %d", n)
	}
	// 有效项保留、顺序不变。
	if len(cfg.Services) != 2 || cfg.Services[0].Name != "svc-a" || cfg.Services[1].Name != "svc-b" {
		t.Fatalf("services not kept in order: %#v", cfg.Services)
	}
	if len(cfg.Chains) != 1 || cfg.Chains[0].Name != "chain-a" {
		t.Fatalf("chains not kept: %#v", cfg.Chains)
	}
	if len(cfg.Bypasses) != 1 || cfg.Bypasses[0].Name != "bypass-a" {
		t.Fatalf("bypasses not kept: %#v", cfg.Bypasses)
	}
	if len(cfg.Limiters) != 1 || cfg.Limiters[0].Name != "lim-a" {
		t.Fatalf("limiters not kept: %#v", cfg.Limiters)
	}
	if len(cfg.Loggers) != 1 || cfg.Loggers[0].Name != "log-a" {
		t.Fatalf("loggers not kept: %#v", cfg.Loggers)
	}
	// 全空类被清空为 0 长（不再含 nil）。
	if len(cfg.Hops) != 0 || len(cfg.CLimiters) != 0 || len(cfg.RLimiters) != 0 {
		t.Fatalf("nil-only slices not emptied: hops=%d climiters=%d rlimiters=%d", len(cfg.Hops), len(cfg.CLimiters), len(cfg.RLimiters))
	}
}

func TestR19F02StripNilConfigItemsNoop(t *testing.T) {
	// 无 nil：返回 nil，且切片指针内容不动。
	cfg := &Config{Services: []*ServiceConfig{{Name: "a"}}, Chains: []*ChainConfig{{Name: "b"}}}
	if stripped := StripNilConfigItems(cfg); stripped != nil {
		t.Fatalf("expected nil stripped for clean config, got %v", stripped)
	}
	if len(cfg.Services) != 1 || len(cfg.Chains) != 1 {
		t.Fatalf("clean config modified: %#v", cfg)
	}
	// nil cfg 与空 cfg 均安全。
	if stripped := StripNilConfigItems(nil); stripped != nil {
		t.Fatalf("expected nil stripped for nil config, got %v", stripped)
	}
	if stripped := StripNilConfigItems(&Config{}); stripped != nil {
		t.Fatalf("expected nil stripped for empty config, got %v", stripped)
	}
}

func countNilsForTest(cfg *Config) int {
	n := 0
	count := func(items any) {
		switch v := items.(type) {
		case []*ServiceConfig:
			for _, it := range v {
				if it == nil {
					n++
				}
			}
		case []*ChainConfig:
			for _, it := range v {
				if it == nil {
					n++
				}
			}
		case []*HopConfig:
			for _, it := range v {
				if it == nil {
					n++
				}
			}
		case []*AutherConfig:
			for _, it := range v {
				if it == nil {
					n++
				}
			}
		case []*AdmissionConfig:
			for _, it := range v {
				if it == nil {
					n++
				}
			}
		case []*BypassConfig:
			for _, it := range v {
				if it == nil {
					n++
				}
			}
		case []*ResolverConfig:
			for _, it := range v {
				if it == nil {
					n++
				}
			}
		case []*HostsConfig:
			for _, it := range v {
				if it == nil {
					n++
				}
			}
		case []*IngressConfig:
			for _, it := range v {
				if it == nil {
					n++
				}
			}
		case []*RouterConfig:
			for _, it := range v {
				if it == nil {
					n++
				}
			}
		case []*SDConfig:
			for _, it := range v {
				if it == nil {
					n++
				}
			}
		case []*ObserverConfig:
			for _, it := range v {
				if it == nil {
					n++
				}
			}
		case []*RecorderConfig:
			for _, it := range v {
				if it == nil {
					n++
				}
			}
		case []*LimiterConfig:
			for _, it := range v {
				if it == nil {
					n++
				}
			}
		case []*LoggerConfig:
			for _, it := range v {
				if it == nil {
					n++
				}
			}
		}
	}
	count(cfg.Services)
	count(cfg.Chains)
	count(cfg.Hops)
	count(cfg.Authers)
	count(cfg.Admissions)
	count(cfg.Bypasses)
	count(cfg.Resolvers)
	count(cfg.Hosts)
	count(cfg.Ingresses)
	count(cfg.Routers)
	count(cfg.SDs)
	count(cfg.Observers)
	count(cfg.Recorders)
	count(cfg.Limiters)
	count(cfg.CLimiters)
	count(cfg.RLimiters)
	count(cfg.Loggers)
	return n
}

func TestR19F02OnUpdatePanicReleasesLock(t *testing.T) {
	original := Global()

	// 闭包内 panic，调用方 recover（模拟 routeCommandSafely）。
	func() {
		defer func() { _ = recover() }()
		_ = OnUpdate(func(c *Config) error {
			panic("r19-f02 probe panic")
		})
	}()

	// panic 之后 config.Set 必须能完成（红态：永久死锁）。
	setDone := make(chan struct{})
	go func() {
		Set(original)
		close(setDone)
	}()
	select {
	case <-setDone:
	case <-time.After(5 * time.Second):
		t.Fatal("config.Set deadlocked after OnUpdate panic (globalMux wedged)")
	}

	// Global() 读锁也必须能拿到。
	getDone := make(chan struct{})
	go func() {
		_ = Global()
		close(getDone)
	}()
	select {
	case <-getDone:
	case <-time.After(5 * time.Second):
		t.Fatal("config.Global deadlocked after OnUpdate panic (globalMux wedged)")
	}

	// 后续 OnUpdate 必须能正常执行。
	if err := OnUpdate(func(c *Config) error { return nil }); err != nil {
		t.Fatalf("subsequent OnUpdate failed: %v", err)
	}
}
