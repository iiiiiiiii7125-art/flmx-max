package loader

import (
	"fmt"

	"github.com/go-gost/core/logger"
	"github.com/go-gost/x/config"
	"github.com/go-gost/x/config/parsing"
	admission_parser "github.com/go-gost/x/config/parsing/admission"
	auth_parser "github.com/go-gost/x/config/parsing/auth"
	bypass_parser "github.com/go-gost/x/config/parsing/bypass"
	chain_parser "github.com/go-gost/x/config/parsing/chain"
	hop_parser "github.com/go-gost/x/config/parsing/hop"
	hosts_parser "github.com/go-gost/x/config/parsing/hosts"
	ingress_parser "github.com/go-gost/x/config/parsing/ingress"
	limiter_parser "github.com/go-gost/x/config/parsing/limiter"
	logger_parser "github.com/go-gost/x/config/parsing/logger"
	observer_parser "github.com/go-gost/x/config/parsing/observer"
	recorder_parser "github.com/go-gost/x/config/parsing/recorder"
	resolver_parser "github.com/go-gost/x/config/parsing/resolver"
	router_parser "github.com/go-gost/x/config/parsing/router"
	sd_parser "github.com/go-gost/x/config/parsing/sd"
	service_parser "github.com/go-gost/x/config/parsing/service"
	"github.com/go-gost/x/internal/idle"
	"github.com/go-gost/x/internal/reap"
	"github.com/go-gost/x/registry"
)

var (
	defaultLoader *loader = &loader{}
)

func Load(cfg *config.Config) error {
	return defaultLoader.Load(cfg)
}

// unloadService 注销服务前先把其 stats 里尚未被周期采样换出的尾
// 量交进全局流量管理器：换代/回滚卸载旧服务时旧 stats 对象随服务
// 一并丢弃，不先 FlushTraffic 则每被换代服务最后一截（不足一个
// 采样周期）的流量静默丢失（审计 A-T-03；停机路径的同款承接见
// program.stopRuntime）。接口断言与 stopRuntime 同款，不引依赖；
// 默认服务实现另在 Close 自身收口（x/service），此处为卸载点的
// 显式承接，覆盖一切实现 FlushTraffic 的服务类型，重复换出恒零。
func unloadService(name string) {
	if svc := registry.ServiceRegistry().Get(name); svc != nil {
		if flusher, ok := svc.(interface{ FlushTraffic() }); ok {
			flusher.FlushTraffic()
		}
	}
	registry.ServiceRegistry().Unregister(name)
}

// Reload replaces the runtime and commits the config only after it starts.
// The caller must hold config.LockMutation, including while parsing cfg, so the
// rollback snapshot includes every previously acknowledged runtime command.
// Failed loads can partially replace registries and bind listeners; always
// rebuild the last successful snapshot before returning an error.
func Reload(cfg *config.Config, run func(*config.Config) error) error {
	previous := config.Global()
	if err := apply(cfg, run); err != nil {
		// A failed partial load may have left both old and new listeners behind.
		for name := range registry.ServiceRegistry().GetAll() {
			unloadService(name)
		}
		if rollbackErr := apply(previous, run); rollbackErr != nil {
			return fmt.Errorf("reload failed: %w; restore previous config failed: %v", err, rollbackErr)
		}
		return fmt.Errorf("reload failed (previous config restored): %w", err)
	}
	config.Set(cfg)
	return nil
}

func apply(cfg *config.Config, run func(*config.Config) error) error {
	if err := Load(cfg); err != nil {
		return err
	}
	return run(cfg)
}

type loader struct{}

// applyIdleWindow publishes the panel's per-node no-progress
// teardown window (service metadata "idleTimeout") to the
// forwarding plane (internal/idle). The panel writes the same
// node-level value into every service it generates for a node; the
// scan takes the maximum, so a partially annotated config can only
// ever widen the window, never narrow it below the node's setting.
// No service carrying the key leaves the teardown disabled (the
// default). Load is the single funnel for initial load, reload and
// reload-rollback, so the published window always matches the
// config actually in force.
func applyIdleWindow(cfg *config.Config) {
	if cfg == nil {
		idle.Set(0)
		reap.Set(0, 0, 0)
		return
	}
	metadatas := make([]map[string]any, 0, len(cfg.Services))
	for _, svc := range cfg.Services {
		if svc == nil {
			continue
		}
		metadatas = append(metadatas, svc.Metadata)
	}
	idle.Publish(metadatas)
	// The reaper settings (internal/reap) publish through the
	// same funnel and scan; see config.publishIdleWindow.
	reap.Publish(metadatas)
}

func (l *loader) Load(cfg *config.Config) error {
	applyIdleWindow(cfg)

	logCfg := cfg.Log
	if logCfg == nil {
		logCfg = &config.LogConfig{}
	}
	logger.SetDefault(logger_parser.ParseLogger(&config.LoggerConfig{Log: logCfg}))

	tlsCfg, err := parsing.BuildDefaultTLSConfig(cfg.TLS)
	if err != nil {
		return err
	}
	parsing.SetDefaultTLSConfig(tlsCfg)

	if err := register(cfg); err != nil {
		return err
	}

	return nil
}

func register(cfg *config.Config) error {
	if cfg == nil {
		return nil
	}

	for name := range registry.LoggerRegistry().GetAll() {
		registry.LoggerRegistry().Unregister(name)
	}
	for _, loggerCfg := range cfg.Loggers {
		// R19-F02: nil 项显式报错（与 services 的 "service config is nil"
		// 同口径），不得 panic；落盘毒化文件由启动自愈先剥离 nil。
		if loggerCfg == nil {
			return fmt.Errorf("logger config is nil")
		}
		if err := registry.LoggerRegistry().Register(loggerCfg.Name, logger_parser.ParseLogger(loggerCfg)); err != nil {
			return err
		}
	}

	for name := range registry.AutherRegistry().GetAll() {
		registry.AutherRegistry().Unregister(name)
	}
	for _, autherCfg := range cfg.Authers {
		// R19-F02: nil 项显式报错，不得 panic。
		if autherCfg == nil {
			return fmt.Errorf("auther config is nil")
		}
		if err := registry.AutherRegistry().Register(autherCfg.Name, auth_parser.ParseAuther(autherCfg)); err != nil {
			return err
		}
	}

	for name := range registry.AdmissionRegistry().GetAll() {
		registry.AdmissionRegistry().Unregister(name)
	}
	for _, admissionCfg := range cfg.Admissions {
		// R19-F02: nil 项显式报错，不得 panic。
		if admissionCfg == nil {
			return fmt.Errorf("admission config is nil")
		}
		if err := registry.AdmissionRegistry().Register(admissionCfg.Name, admission_parser.ParseAdmission(admissionCfg)); err != nil {
			return err
		}
	}

	for name := range registry.BypassRegistry().GetAll() {
		registry.BypassRegistry().Unregister(name)
	}
	for _, bypassCfg := range cfg.Bypasses {
		// R19-F02: nil 项显式报错，不得 panic。
		if bypassCfg == nil {
			return fmt.Errorf("bypass config is nil")
		}
		if err := registry.BypassRegistry().Register(bypassCfg.Name, bypass_parser.ParseBypass(bypassCfg)); err != nil {
			return err
		}
	}
	// FIX2-C #8: built-in bypasses (flmx-no-private) must survive the
	// unregister-all above on every (re)load; a config-defined bypass
	// of the same name takes precedence (see RegisterBuiltinBypasses).
	bypass_parser.RegisterBuiltinBypasses()

	for name := range registry.ResolverRegistry().GetAll() {
		registry.ResolverRegistry().Unregister(name)
	}
	for _, resolverCfg := range cfg.Resolvers {
		// R19-F02: nil 项显式报错，不得 panic。
		if resolverCfg == nil {
			return fmt.Errorf("resolver config is nil")
		}
		r, err := resolver_parser.ParseResolver(resolverCfg)
		if err != nil {
			return err
		}
		if err := registry.ResolverRegistry().Register(resolverCfg.Name, r); err != nil {
			return err
		}
	}

	for name := range registry.HostsRegistry().GetAll() {
		registry.HostsRegistry().Unregister(name)
	}
	for _, hostsCfg := range cfg.Hosts {
		// R19-F02: nil 项显式报错，不得 panic。
		if hostsCfg == nil {
			return fmt.Errorf("hosts config is nil")
		}
		if err := registry.HostsRegistry().Register(hostsCfg.Name, hosts_parser.ParseHostMapper(hostsCfg)); err != nil {
			return err
		}
	}

	for name := range registry.IngressRegistry().GetAll() {
		registry.IngressRegistry().Unregister(name)
	}
	for _, ingressCfg := range cfg.Ingresses {
		// R19-F02: nil 项显式报错，不得 panic。
		if ingressCfg == nil {
			return fmt.Errorf("ingress config is nil")
		}
		if err := registry.IngressRegistry().Register(ingressCfg.Name, ingress_parser.ParseIngress(ingressCfg)); err != nil {
			return err
		}
	}

	for name := range registry.RouterRegistry().GetAll() {
		registry.RouterRegistry().Unregister(name)
	}
	for _, routerCfg := range cfg.Routers {
		// R19-F02: nil 项显式报错，不得 panic。
		if routerCfg == nil {
			return fmt.Errorf("router config is nil")
		}
		if err := registry.RouterRegistry().Register(routerCfg.Name, router_parser.ParseRouter(routerCfg)); err != nil {
			return err
		}
	}

	for name := range registry.SDRegistry().GetAll() {
		registry.SDRegistry().Unregister(name)
	}
	for _, sdCfg := range cfg.SDs {
		// R19-F02: nil 项显式报错，不得 panic。
		if sdCfg == nil {
			return fmt.Errorf("sd config is nil")
		}
		if err := registry.SDRegistry().Register(sdCfg.Name, sd_parser.ParseSD(sdCfg)); err != nil {
			return err
		}
	}

	for name := range registry.ObserverRegistry().GetAll() {
		registry.ObserverRegistry().Unregister(name)
	}
	for _, observerCfg := range cfg.Observers {
		// R19-F02: nil 项显式报错，不得 panic。
		if observerCfg == nil {
			return fmt.Errorf("observer config is nil")
		}
		if err := registry.ObserverRegistry().Register(observerCfg.Name, observer_parser.ParseObserver(observerCfg)); err != nil {
			return err
		}
	}

	for name := range registry.RecorderRegistry().GetAll() {
		registry.RecorderRegistry().Unregister(name)
	}
	for _, recorderCfg := range cfg.Recorders {
		// R19-F02: nil 项显式报错，不得 panic。
		if recorderCfg == nil {
			return fmt.Errorf("recorder config is nil")
		}
		if err := registry.RecorderRegistry().Register(recorderCfg.Name, recorder_parser.ParseRecorder(recorderCfg)); err != nil {
			return err
		}
	}

	for name := range registry.TrafficLimiterRegistry().GetAll() {
		registry.TrafficLimiterRegistry().Unregister(name)
	}
	for _, limiterCfg := range cfg.Limiters {
		// R19-F02: nil 项显式报错，不得 panic。
		if limiterCfg == nil {
			return fmt.Errorf("limiter config is nil")
		}
		if err := registry.TrafficLimiterRegistry().Register(limiterCfg.Name, limiter_parser.ParseTrafficLimiter(limiterCfg)); err != nil {
			return err
		}
	}

	for name := range registry.ConnLimiterRegistry().GetAll() {
		registry.ConnLimiterRegistry().Unregister(name)
	}
	for _, limiterCfg := range cfg.CLimiters {
		// R19-F02: nil 项显式报错，不得 panic。
		if limiterCfg == nil {
			return fmt.Errorf("conn limiter config is nil")
		}
		if err := registry.ConnLimiterRegistry().Register(limiterCfg.Name, limiter_parser.ParseConnLimiter(limiterCfg)); err != nil {
			return err
		}
	}

	for name := range registry.RateLimiterRegistry().GetAll() {
		registry.RateLimiterRegistry().Unregister(name)
	}
	for _, limiterCfg := range cfg.RLimiters {
		// R19-F02: nil 项显式报错，不得 panic。
		if limiterCfg == nil {
			return fmt.Errorf("rate limiter config is nil")
		}
		if err := registry.RateLimiterRegistry().Register(limiterCfg.Name, limiter_parser.ParseRateLimiter(limiterCfg)); err != nil {
			return err
		}
	}

	for name := range registry.HopRegistry().GetAll() {
		registry.HopRegistry().Unregister(name)
	}
	for _, hopCfg := range cfg.Hops {
		// R19-F02: nil 项显式报错，不得 panic。
		if hopCfg == nil {
			return fmt.Errorf("hop config is nil")
		}
		hop, err := hop_parser.ParseHop(hopCfg, logger.Default())
		if err != nil {
			return err
		}
		if err := registry.HopRegistry().Register(hopCfg.Name, hop); err != nil {
			return err
		}
	}

	for name := range registry.ChainRegistry().GetAll() {
		registry.ChainRegistry().Unregister(name)
	}
	for _, chainCfg := range cfg.Chains {
		// R19-F02: nil 项显式报错，不得 panic。
		if chainCfg == nil {
			return fmt.Errorf("chain config is nil")
		}
		c, err := chain_parser.ParseChain(chainCfg, logger.Default())
		if err != nil {
			return err
		}
		if err := registry.ChainRegistry().Register(chainCfg.Name, c); err != nil {
			return err
		}
	}

	for name := range registry.ServiceRegistry().GetAll() {
		unloadService(name)
	}
	for _, svcCfg := range cfg.Services {
		if svcCfg == nil {
			return fmt.Errorf("service config is nil")
		}
		if paused, _ := svcCfg.Metadata["paused"].(bool); paused {
			continue
		}
		svc, err := service_parser.ParseService(svcCfg)
		if err != nil {
			// R17-C: name the service — the raw error is usually a bare
			// bind failure ("address already in use") with no indication
			// of which service (or duplicated service) caused it.
			return fmt.Errorf("parse service %q: %w", svcCfg.Name, err)
		}
		if svc != nil {
			// R17-C: name the service in the duplicate error — a bare
			// "registry: duplicate object" gives forensics nothing to
			// match against the poisoned gost.json (R17-X-01).
			if err := registry.ServiceRegistry().Register(svcCfg.Name, svc); err != nil {
				svc.Close()
				return fmt.Errorf("register service %q: %w", svcCfg.Name, err)
			}
		}
	}

	return nil
}
