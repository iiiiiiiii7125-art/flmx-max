// R19-F02: update*/delete*/pause/resume/rollback nil 全族回归。
//
// 背景：R19-X-02——配置切片含 nil 项时，x/socket 的 update/delete/pause/
// resume/rollback 入口直接取 `.Name` 而 panic（沙箱实测 5/5：updateChain、
// updateLimiter、updateConnLimiter、updateServices、deleteChain）；panic
// 若发生在 config.OnUpdate 闭包内，非 defer 解锁还会让 globalMux 写锁
// 永不释放、后续 config.Set/Global 永久死锁（L2 实测 30s 超时转储）。
//
// 修复后：各入口跳过 nil 项并记日志（nil 项原样保留，清理只走启动自愈），
// 不得 panic；OnUpdate 改 defer 解锁，panic 后锁必释放。
//
// 本文件用 runAndCatch 捕获 panic（红态：t.Errorf；绿态：无 panic 且
// 返回 nil error）；config.Set/Global 一律走带 5s 超时的安全包装——
// 红态下锁若已 wedged，测试快速失败而不把整个 test 二进制挂死。
package socket

import (
	"testing"
	"time"

	"github.com/go-gost/core/logger"
	"github.com/go-gost/core/service"
	"github.com/go-gost/x/config"
	_ "github.com/go-gost/x/connector/relay"
	_ "github.com/go-gost/x/dialer/tcp"
	xlogger "github.com/go-gost/x/logger"
	"github.com/go-gost/x/registry"
)

// r19RunAndCatch runs f, recovering any panic; returns (panicked, value).
func r19RunAndCatch(f func()) (panicked bool, value any) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
			value = r
		}
	}()
	f()
	return false, nil
}

// r19SafeSet sets the global config, failing fast (instead of hanging) if
// the global lock is wedged by an earlier panic.
func r19SafeSet(t *testing.T, cfg *config.Config) bool {
	t.Helper()
	done := make(chan struct{})
	go func() {
		config.Set(cfg)
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(5 * time.Second):
		t.Errorf("config.Set timed out after 5s (globalMux wedged?)")
		return false
	}
}

// r19SafeGlobal reads the global config with the same wedge guard.
func r19SafeGlobal(t *testing.T) (*config.Config, bool) {
	t.Helper()
	ch := make(chan *config.Config, 1)
	go func() { ch <- config.Global() }()
	select {
	case cfg := <-ch:
		return cfg, true
	case <-time.After(5 * time.Second):
		t.Errorf("config.Global timed out after 5s (globalMux wedged?)")
		return nil, false
	}
}

// r19SeedSocketConfig replaces the global config (persist pointed at a
// temp file) and restores both on cleanup, wedge-safe.
func r19SeedSocketConfig(t *testing.T, cfg *config.Config) {
	t.Helper()
	logger.SetDefault(xlogger.Nop())
	originalConfig, ok := r19SafeGlobal(t)
	if !ok {
		t.Fatalf("cannot read original config (globalMux wedged before test)")
	}
	originalPersistPath := config.PersistPath()
	persistFile := t.TempDir() + "/gost.json"
	if !r19SafeSet(t, cfg) {
		t.Fatalf("cannot seed config (globalMux wedged before test)")
	}
	config.SetPersistPath(persistFile)
	config.EnablePersist()
	t.Cleanup(func() {
		r19SafeSet(t, originalConfig)
		config.SetPersistPath(originalPersistPath)
	})
}

func r19ValidChain(name string) config.ChainConfig {
	return config.ChainConfig{Name: name, Hops: []*config.HopConfig{{Name: "h", Nodes: []*config.NodeConfig{{Name: "n", Addr: "127.0.0.1:443", Connector: &config.ConnectorConfig{Type: "relay"}, Dialer: &config.DialerConfig{Type: "tcp"}}}}}}
}

// L2 形 1：updateChain 在 nil chain 项上 panic（chain.go:85）。
func TestR19F02UpdateChainNil(t *testing.T) {
	name := "r19f02-chain"
	r19SeedSocketConfig(t, &config.Config{Chains: []*config.ChainConfig{
		nil,
		{Name: name, Hops: []*config.HopConfig{{Name: "h", Nodes: []*config.NodeConfig{{Name: "n", Addr: "127.0.0.1:443"}}}}},
	}})
	t.Cleanup(func() { registry.ChainRegistry().Unregister(name) })
	registry.ChainRegistry().Unregister(name)

	valid := r19ValidChain(name)
	panicked, v := r19RunAndCatch(func() {
		if err := updateChain(updateChainRequest{Chain: name, Data: valid}); err != nil {
			t.Errorf("updateChain returned error: %v", err)
		}
	})
	if panicked {
		t.Fatalf("updateChain PANICKED on nil chain entry: %v", v)
	}
	cfg, ok := r19SafeGlobal(t)
	if !ok {
		return
	}
	if len(cfg.Chains) != 2 || cfg.Chains[0] != nil || cfg.Chains[1] == nil || cfg.Chains[1].Name != name {
		t.Fatalf("expected config [nil %q] after update, got %#v", name, cfg.Chains)
	}
}

// L2 形 2：updateLimiter 在 nil limiter 项上 panic（limiter.go:75）。
func TestR19F02UpdateLimiterNil(t *testing.T) {
	name := "r19f02-limiter"
	r19SeedSocketConfig(t, &config.Config{Limiters: []*config.LimiterConfig{
		nil,
		{Name: name, Limits: []string{"$ 1"}},
	}})
	t.Cleanup(func() { registry.TrafficLimiterRegistry().Unregister(name) })
	registry.TrafficLimiterRegistry().Unregister(name)

	panicked, v := r19RunAndCatch(func() {
		if err := updateLimiter(updateLimiterRequest{Limiter: name, Data: config.LimiterConfig{Name: name, Limits: []string{"$ 2"}}}); err != nil {
			t.Errorf("updateLimiter returned error: %v", err)
		}
	})
	if panicked {
		t.Fatalf("updateLimiter PANICKED on nil limiter entry: %v", v)
	}
	cfg, ok := r19SafeGlobal(t)
	if !ok {
		return
	}
	if len(cfg.Limiters) != 2 || cfg.Limiters[0] != nil || cfg.Limiters[1] == nil || cfg.Limiters[1].Limits[0] != "$ 2" {
		t.Fatalf("expected [nil limiter:$2] after update, got %#v", cfg.Limiters)
	}
}

// L2 形 3：updateConnLimiter 在 nil conn limiter 项上 panic（limiter.go:182）。
func TestR19F02UpdateConnLimiterNil(t *testing.T) {
	name := "r19f02-climiter"
	r19SeedSocketConfig(t, &config.Config{CLimiters: []*config.LimiterConfig{
		nil,
		{Name: name, Limits: []string{"$ 1"}},
	}})
	t.Cleanup(func() { registry.ConnLimiterRegistry().Unregister(name) })
	registry.ConnLimiterRegistry().Unregister(name)

	panicked, v := r19RunAndCatch(func() {
		if err := updateConnLimiter(updateLimiterRequest{Limiter: name, Data: config.LimiterConfig{Name: name, Limits: []string{"$ 2"}}}); err != nil {
			t.Errorf("updateConnLimiter returned error: %v", err)
		}
	})
	if panicked {
		t.Fatalf("updateConnLimiter PANICKED on nil conn limiter entry: %v", v)
	}
	cfg, ok := r19SafeGlobal(t)
	if !ok {
		return
	}
	if len(cfg.CLimiters) != 2 || cfg.CLimiters[0] != nil || cfg.CLimiters[1] == nil || cfg.CLimiters[1].Limits[0] != "$ 2" {
		t.Fatalf("expected [nil climiter:$2] after update, got %#v", cfg.CLimiters)
	}
}

// L2 形 4：updateServices 在 nil service 项上 panic（service.go:202，第 3 阶段）。
func TestR19F02UpdateServicesNil(t *testing.T) {
	name := "r19f02-svc"
	r19SeedSocketConfig(t, &config.Config{Services: []*config.ServiceConfig{
		nil,
		{Name: name, Addr: "127.0.0.1:0"},
	}})
	t.Cleanup(func() {
		if svc := registry.ServiceRegistry().Get(name); svc != nil {
			svc.Close()
		}
		registry.ServiceRegistry().Unregister(name)
	})
	registry.ServiceRegistry().Unregister(name)

	panicked, v := r19RunAndCatch(func() {
		if err := updateServices(updateServicesRequest{Data: []config.ServiceConfig{{Name: name, Addr: "127.0.0.1:0"}}}); err != nil {
			t.Errorf("updateServices returned error: %v", err)
		}
	})
	if panicked {
		t.Fatalf("updateServices PANICKED on nil service entry: %v", v)
	}
	cfg, ok := r19SafeGlobal(t)
	if !ok {
		return
	}
	if len(cfg.Services) != 2 || cfg.Services[0] != nil || cfg.Services[1] == nil || cfg.Services[1].Name != name {
		t.Fatalf("expected [nil service] after update, got %#v", cfg.Services)
	}
}

// L2 形 5：deleteChain 在 nil chain 项上 panic（chain.go:110）。
func TestR19F02DeleteChainNil(t *testing.T) {
	name := "r19f02-keep"
	r19SeedSocketConfig(t, &config.Config{Chains: []*config.ChainConfig{
		nil,
		{Name: name},
	}})
	t.Cleanup(func() { registry.ChainRegistry().Unregister(name) })

	panicked, v := r19RunAndCatch(func() {
		if err := deleteChain(deleteChainRequest{Chain: name}); err != nil {
			t.Errorf("deleteChain returned error: %v", err)
		}
	})
	if panicked {
		t.Fatalf("deleteChain PANICKED on nil chain entry: %v", v)
	}
	cfg, ok := r19SafeGlobal(t)
	if !ok {
		return
	}
	// 目标链已删、nil 项原样保留（清理只走启动自愈）。
	if len(cfg.Chains) != 1 || cfg.Chains[0] != nil {
		t.Fatalf("expected [nil] after delete, got %#v", cfg.Chains)
	}
}

// 同族：deleteLimiter（limiter.go:100）。
func TestR19F02DeleteLimiterNil(t *testing.T) {
	name := "r19f02-keep-limiter"
	r19SeedSocketConfig(t, &config.Config{Limiters: []*config.LimiterConfig{
		nil,
		{Name: name, Limits: []string{"$ 1"}},
	}})

	panicked, v := r19RunAndCatch(func() {
		if err := deleteLimiter(deleteLimiterRequest{Limiter: name}); err != nil {
			t.Errorf("deleteLimiter returned error: %v", err)
		}
	})
	if panicked {
		t.Fatalf("deleteLimiter PANICKED on nil limiter entry: %v", v)
	}
	cfg, ok := r19SafeGlobal(t)
	if !ok {
		return
	}
	if len(cfg.Limiters) != 1 || cfg.Limiters[0] != nil {
		t.Fatalf("expected [nil] after delete, got %#v", cfg.Limiters)
	}
}

// 同族：deleteConnLimiter（limiter.go:203）。
func TestR19F02DeleteConnLimiterNil(t *testing.T) {
	name := "r19f02-keep-climiter"
	r19SeedSocketConfig(t, &config.Config{CLimiters: []*config.LimiterConfig{
		nil,
		{Name: name, Limits: []string{"$ 1"}},
	}})

	panicked, v := r19RunAndCatch(func() {
		if err := deleteConnLimiter(deleteLimiterRequest{Limiter: name}); err != nil {
			t.Errorf("deleteConnLimiter returned error: %v", err)
		}
	})
	if panicked {
		t.Fatalf("deleteConnLimiter PANICKED on nil conn limiter entry: %v", v)
	}
	cfg, ok := r19SafeGlobal(t)
	if !ok {
		return
	}
	if len(cfg.CLimiters) != 1 || cfg.CLimiters[0] != nil {
		t.Fatalf("expected [nil] after delete, got %#v", cfg.CLimiters)
	}
}

// 同族：deleteServices（service.go:328，第 3 阶段）。
func TestR19F02DeleteServicesNil(t *testing.T) {
	name := "r19f02-del-svc"
	r19SeedSocketConfig(t, &config.Config{Services: []*config.ServiceConfig{
		nil,
		{Name: name, Addr: "127.0.0.1:0"},
	}})
	t.Cleanup(func() { registry.ServiceRegistry().Unregister(name) })

	panicked, v := r19RunAndCatch(func() {
		if err := deleteServices(deleteServicesRequest{Services: []string{name}}); err != nil {
			t.Errorf("deleteServices returned error: %v", err)
		}
	})
	if panicked {
		t.Fatalf("deleteServices PANICKED on nil service entry: %v", v)
	}
	cfg, ok := r19SafeGlobal(t)
	if !ok {
		return
	}
	if len(cfg.Services) != 1 || cfg.Services[0] != nil {
		t.Fatalf("expected [nil] after delete, got %#v", cfg.Services)
	}
}

// r19CreateSocketService builds + starts a throwaway service for the
// pause/resume family (Addr 127.0.0.1:0 binds an ephemeral port).
func r19CreateSocketService(t *testing.T, name string) {
	t.Helper()
	t.Cleanup(func() {
		if svc := registry.ServiceRegistry().Get(name); svc != nil {
			svc.Close()
		}
		registry.ServiceRegistry().Unregister(name)
	})
	registry.ServiceRegistry().Unregister(name)
	if err := createServices(createServicesRequest{Data: []config.ServiceConfig{{Name: name, Addr: "127.0.0.1:0"}}}); err != nil {
		t.Fatalf("createServices(%q): %v", name, err)
	}
}

// 同族：pauseServices——建 map 处（service.go:401）与第 3 阶段（:433）。
func TestR19F02PauseServicesNil(t *testing.T) {
	name := "r19f02-pause-svc"
	r19SeedSocketConfig(t, &config.Config{})
	r19CreateSocketService(t, name)
	if !r19SafeSet(t, &config.Config{Services: []*config.ServiceConfig{
		nil,
		{Name: name, Addr: "127.0.0.1:0"},
	}}) {
		t.Fatalf("cannot seed nil service config")
	}

	panicked, v := r19RunAndCatch(func() {
		if err := pauseServices(pauseServicesRequest{Services: []string{name}}); err != nil {
			t.Errorf("pauseServices returned error: %v", err)
		}
	})
	if panicked {
		t.Fatalf("pauseServices PANICKED on nil service entry: %v", v)
	}
	cfg, ok := r19SafeGlobal(t)
	if !ok {
		return
	}
	if len(cfg.Services) != 2 || cfg.Services[0] != nil || cfg.Services[1] == nil {
		t.Fatalf("expected [nil service] after pause, got %#v", cfg.Services)
	}
	if paused, _ := cfg.Services[1].Metadata["paused"].(bool); !paused {
		t.Fatalf("expected service %q marked paused, got metadata %#v", name, cfg.Services[1].Metadata)
	}
}

// 同族：resumeServices——查配置处（service.go:483）与第 3 阶段（:560）。
func TestR19F02ResumeServicesNil(t *testing.T) {
	name := "r19f02-resume-svc"
	r19SeedSocketConfig(t, &config.Config{})
	r19CreateSocketService(t, name)
	if err := pauseServices(pauseServicesRequest{Services: []string{name}}); err != nil {
		t.Fatalf("pauseServices(%q): %v", name, err)
	}
	// 暂停后在配置前插一个 nil 项，再恢复。
	cfg, ok := r19SafeGlobal(t)
	if !ok {
		return
	}
	paused := cfg.Services[0]
	if paused == nil {
		t.Fatalf("expected paused service in config, got %#v", cfg.Services)
	}
	if !r19SafeSet(t, &config.Config{Services: []*config.ServiceConfig{nil, paused}}) {
		t.Fatalf("cannot seed nil service config")
	}

	panicked, v := r19RunAndCatch(func() {
		if err := resumeServices(resumeServicesRequest{Services: []string{name}}); err != nil {
			t.Errorf("resumeServices returned error: %v", err)
		}
	})
	if panicked {
		t.Fatalf("resumeServices PANICKED on nil service entry: %v", v)
	}
	cfg, ok = r19SafeGlobal(t)
	if !ok {
		return
	}
	if len(cfg.Services) != 2 || cfg.Services[0] != nil || cfg.Services[1] == nil {
		t.Fatalf("expected [nil service] after resume, got %#v", cfg.Services)
	}
	if _, exists := cfg.Services[1].Metadata["paused"]; exists {
		t.Fatalf("expected paused flag removed after resume, got metadata %#v", cfg.Services[1].Metadata)
	}
}

// 同族：rollbackPausedServices（service.go:606，OnUpdate 闭包内）。
func TestR19F02RollbackPausedServicesNil(t *testing.T) {
	name := "r19f02-rb-paused"
	r19SeedSocketConfig(t, &config.Config{Services: []*config.ServiceConfig{
		nil,
		{Name: name, Addr: "127.0.0.1:0"},
	}})
	t.Cleanup(func() {
		if svc := registry.ServiceRegistry().Get(name); svc != nil {
			svc.Close()
		}
		registry.ServiceRegistry().Unregister(name)
	})
	registry.ServiceRegistry().Unregister(name)

	svcCfg := config.ServiceConfig{Name: name, Addr: "127.0.0.1:0"}
	panicked, v := r19RunAndCatch(func() {
		rollbackPausedServices([]struct {
			name          string
			service       service.Service
			serviceConfig *config.ServiceConfig
		}{{name: name, service: nil, serviceConfig: &svcCfg}})
	})
	if panicked {
		t.Fatalf("rollbackPausedServices PANICKED on nil service entry: %v", v)
	}
	if registry.ServiceRegistry().Get(name) == nil {
		t.Fatalf("expected service %q re-registered by rollback", name)
	}
}

// 同族：rollbackResumedServices（service.go:635，OnUpdate 闭包内）。
func TestR19F02RollbackResumedServicesNil(t *testing.T) {
	name := "r19f02-rb-resumed"
	r19SeedSocketConfig(t, &config.Config{})
	r19CreateSocketService(t, name)
	if !r19SafeSet(t, &config.Config{Services: []*config.ServiceConfig{
		nil,
		{Name: name, Addr: "127.0.0.1:0"},
	}}) {
		t.Fatalf("cannot seed nil service config")
	}

	svcCfg := config.ServiceConfig{Name: name, Addr: "127.0.0.1:0"}
	panicked, v := r19RunAndCatch(func() {
		rollbackResumedServices([]struct {
			name          string
			service       service.Service
			serviceConfig *config.ServiceConfig
		}{{name: name, service: nil, serviceConfig: &svcCfg}})
	})
	if panicked {
		t.Fatalf("rollbackResumedServices PANICKED on nil service entry: %v", v)
	}
	cfg, ok := r19SafeGlobal(t)
	if !ok {
		return
	}
	if len(cfg.Services) != 2 || cfg.Services[0] != nil || cfg.Services[1] == nil {
		t.Fatalf("expected [nil service] after rollback, got %#v", cfg.Services)
	}
	if paused, _ := cfg.Services[1].Metadata["paused"].(bool); !paused {
		t.Fatalf("expected service %q re-marked paused, got metadata %#v", name, cfg.Services[1].Metadata)
	}
}
