package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-gost/core/auth"
	"github.com/go-gost/core/logger"
	"github.com/go-gost/core/service"
	api_service "github.com/go-gost/x/api/service"
	xauth "github.com/go-gost/x/auth"
	"github.com/go-gost/x/config"
	"github.com/go-gost/x/config/loader"
	auth_parser "github.com/go-gost/x/config/parsing/auth"
	"github.com/go-gost/x/config/parsing/parser"
	"github.com/go-gost/x/kernel"
	xmetrics "github.com/go-gost/x/metrics"
	metrics "github.com/go-gost/x/metrics/service"
	"github.com/go-gost/x/registry"
	xservice "github.com/go-gost/x/service"
	"github.com/go-gost/x/socket"
	"github.com/judwhite/go-svc"
)

type reporter interface {
	Stop()
}

type program struct {
	startReporter     func() reporter
	reporter          reporter
	srvApi            service.Service
	srvMetrics        service.Service
	srvProfiling      *http.Server
	profilingListener net.Listener

	cancel  context.CancelFunc
	stopped bool
}

func (p *program) Init(env svc.Environment) error {
	parser.Init(parser.Args{
		CfgFile:     cfgFile,
		Services:    services,
		Nodes:       nodes,
		Debug:       debug,
		Trace:       trace,
		ApiAddr:     apiAddr,
		MetricsAddr: metricsAddr,
	})

	return nil
}

func (p *program) Start() (err error) {
	unlock := config.LockMutation()
	defer unlock()
	p.stopped = false
	defer func() {
		if err != nil {
			p.stopRuntime()
		}
	}()
	cfg, err := parser.Parse()
	if err != nil {
		return err
	}

	// R17-A: 存量毒化 gost.json 自愈（见 healPersistedConfig；R18-F03 扩展
	// 到 chains / limiters / climiters）。
	healPersistedConfig(cfg)

	if outputFormat != "" {
		if err := cfg.Write(os.Stdout, outputFormat); err != nil {
			return err
		}
		os.Exit(0)
	}

	if err := loader.Load(cfg); err != nil {
		return err
	}

	if err := p.run(cfg); err != nil {
		return err
	}

	config.Set(cfg)
	socket.EnableConfigPersist()

	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGHUP)
	go p.reload(ctx, c)

	// F-033: Restore 必须在 reporter 启动前同步完成。旧实现异步 Restore
	// 与面板上线重下发（plans/070 B5）无序竞争：面板的 KernelApply 先
	// 落地后，Restore 再按 state.json 的旧配置逐个 Apply，谁后执行谁
	// 生效，面板新配置会被旧配置覆盖（60s 周期重下发才自愈）。
	kernel.Default().Restore()

	// A connected panel may immediately send commands. Only expose the agent
	// after initial config loading, runtime startup and persistence are ready.
	if p.startReporter != nil {
		p.reporter = p.startReporter()
	}

	go func() {
		select {
		case <-time.After(10 * time.Second):
			xservice.StartConfigReporter(ctx)
		case <-ctx.Done():
			return
		}
	}()

	return nil
}

func (p *program) run(cfg *config.Config) (err error) {
	defer func() {
		if err != nil {
			// Auxiliary listeners may occupy ports required by the rollback config.
			// Release all resources opened by this attempt before rebuilding it.
			p.stopRuntime()
		}
	}()
	for _, svc := range registry.ServiceRegistry().GetAll() {
		svc := svc
		go func() {
			svc.Serve()
		}()
	}

	if p.srvApi != nil {
		p.srvApi.Close()
		p.srvApi = nil
	}
	if cfg.API != nil {
		s, err := buildApiService(cfg.API)
		if err != nil {
			return err
		}

		p.srvApi = s

		go func() {
			defer s.Close()

			log := logger.Default().WithFields(map[string]any{"kind": "service", "service": "@api"})

			log.Info("listening on ", s.Addr())
			if err := s.Serve(); !errors.Is(err, http.ErrServerClosed) {
				log.Error(err)
			}
		}()
	}

	xmetrics.Enable(false)
	if p.srvMetrics != nil {
		p.srvMetrics.Close()
		p.srvMetrics = nil
	}
	if cfg.Metrics != nil && cfg.Metrics.Addr != "" {
		s, err := buildMetricsService(cfg.Metrics)
		if err != nil {
			return err
		}

		p.srvMetrics = s

		xmetrics.Enable(true)

		go func() {
			defer s.Close()

			log := logger.Default().WithFields(map[string]any{"kind": "service", "service": "@metrics"})

			log.Info("listening on ", s.Addr())
			if err := s.Serve(); !errors.Is(err, http.ErrServerClosed) {
				log.Error(err)
			}
		}()
	}

	if p.srvProfiling != nil {
		p.srvProfiling.Close()
		if p.profilingListener != nil {
			p.profilingListener.Close()
			p.profilingListener = nil
		}
		p.srvProfiling = nil
	}
	if cfg.Profiling != nil {
		addr := cfg.Profiling.Addr
		if addr == "" {
			addr = ":6060"
		}
		s := &http.Server{
			Addr: addr,
		}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return err
		}
		p.srvProfiling = s
		p.profilingListener = ln

		go func() {
			defer s.Close()

			log := logger.Default().WithFields(map[string]any{"kind": "service", "service": "@profiling"})

			log.Info("listening on ", addr)
			if err := s.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
				log.Error(err)
			}
		}()
	}

	return nil
}

func (p *program) Stop() error {
	if p.cancel != nil {
		p.cancel()
	}

	if p.reporter != nil {
		p.reporter.Stop()
	}
	kernel.Default().Shutdown()
	unlock := config.LockMutation()
	defer unlock()
	p.stopped = true
	p.stopRuntime()
	return nil
}

func (p *program) stopRuntime() {
	for name := range registry.ServiceRegistry().GetAll() {
		registry.ServiceRegistry().Unregister(name)
		logger.Default().Debugf("service %s shutdown", name)
	}

	if p.srvApi != nil {
		p.srvApi.Close()
		p.srvApi = nil
		logger.Default().Debug("service @api shutdown")
	}
	if p.srvMetrics != nil {
		p.srvMetrics.Close()
		p.srvMetrics = nil
		logger.Default().Debug("service @metrics shutdown")
	}
	if p.srvProfiling != nil {
		p.srvProfiling.Close()
		if p.profilingListener != nil {
			p.profilingListener.Close()
			p.profilingListener = nil
		}
		p.srvProfiling = nil
		logger.Default().Debug("service @profiling shutdown")
	}
}

func (p *program) reload(ctx context.Context, c chan os.Signal) {
	defer signal.Stop(c)

	for {
		select {
		case <-c:
			if err := p.reloadConfig(); err != nil {
				logger.Default().Error(err)
			} else {
				logger.Default().Info("config reloaded")
			}

		case <-ctx.Done():
			return
		}
	}
}

func (p *program) reloadConfig() error {
	unlock := config.LockMutation()
	defer unlock()
	if p.stopped {
		return errors.New("agent is shutting down")
	}
	cfg, err := parser.Parse()
	if err != nil {
		return err
	}
	// R17-A: SIGHUP 重载走同一加载路径，毒化文件同样先自愈（R18-F03：含
	// chains / limiters / climiters）。
	healPersistedConfig(cfg)
	if err := loader.Reload(cfg, p.run); err != nil {
		return err
	}
	activeServices := make(map[string]struct{}, len(cfg.Services))
	for _, svc := range cfg.Services {
		if svc != nil {
			activeServices[svc.Name] = struct{}{}
		}
	}
	xservice.GetGlobalTrafficManager().RetainServices(activeServices)

	return nil
}

// healPersistedConfig 去重启动/重载时加载的落盘配置（R17-A 仅服务；
// R18-F03 扩展到链与限流器；R18-INT 补 rate limiter），并剥离 nil 项
// （R19-F02：落盘 JSON null 读入后的内存形态，先剥离再加载、并重新落盘，
// 否则 loader.Load 在首个 nil 类 panic、agent 退出且文件不被清理，
// 重启后再次崩溃）。返回被去掉的重复名与 nil 项描述（五类去重 + nil
// 剥离合并）。存量 gost.json
// 若含重复服务名，loader.Load 会在第二个同名服务处失败（监听器 eager 绑定报
// address already in use，或 Register 报 registry: duplicate object）；若含
// 重复链/限流器名，同样在第二个同名项的 Register 处报 registry: duplicate
// object——任一分支都使 program.Start 直接返回错误、agent 退出，故先去重
// （保留首次出现）再加载；去重后把清理过的配置重新落盘，否则下次启动仍会读
// 到毒化文件。去重了哪些名以 Warn 日志按类写明。落盘失败只告警、不阻塞启动
// （下次启动会再次自愈）。五类各调一次 Heal*、各落盘一次（毒化多类时最多
// 5 次原子写；最终落盘文件含全部五类的愈合结果）。
func healPersistedConfig(cfg *config.Config) []string {
	var removed []string
	// R19-F02: 先剥离 nil 项（落盘 JSON null 读入后的内存形态）并重新
	// 落盘——nil 项拒绝落盘、不得进自愈保留；否则随后 loader.Load 在
	// 首个 nil 类的 `.Name` 处 panic、agent 退出，且文件未被清理，
	// 重启后再次崩溃（崩溃循环）。落盘失败只告警、不阻塞启动（下次
	// 启动会再次自愈）。
	if stripped := config.StripNilConfigItems(cfg); len(stripped) > 0 {
		logger.Default().Warnf("startup config self-heal: removed %s from persisted config", strings.Join(stripped, ", "))
		if err := config.PersistNow(cfg); err != nil {
			logger.Default().Warnf("startup config self-heal: failed to persist nil-stripped config: %v", err)
		}
		removed = append(removed, stripped...)
	}
	heal := func(kind string, fn func(*config.Config) ([]string, error)) {
		names, err := fn(cfg)
		if len(names) == 0 {
			return
		}
		logger.Default().Warnf("startup config self-heal: removed duplicate %s(s) %v from persisted config", kind, names)
		if err != nil {
			logger.Default().Warnf("startup config self-heal: failed to persist deduplicated config: %v", err)
		}
		removed = append(removed, names...)
	}
	heal("service", config.HealPersistedServices)
	heal("chain", config.HealPersistedChains)
	heal("limiter", config.HealPersistedLimiters)
	heal("conn limiter", config.HealPersistedCLimiters)
	heal("rate limiter", config.HealPersistedRLimiters)
	if len(removed) == 0 {
		return nil
	}
	return removed
}

func buildApiService(cfg *config.APIConfig) (service.Service, error) {
	var authers []auth.Authenticator
	if auther := auth_parser.ParseAutherFromAuth(cfg.Auth); auther != nil {
		authers = append(authers, auther)
	}
	if cfg.Auther != "" {
		authers = append(authers, registry.AutherRegistry().Get(cfg.Auther))
	}

	var auther auth.Authenticator
	if len(authers) > 0 {
		auther = xauth.AuthenticatorGroup(authers...)
	}

	network := "tcp"
	addr := cfg.Addr
	if strings.HasPrefix(addr, "unix://") {
		network = "unix"
		addr = strings.TrimPrefix(addr, "unix://")
	}
	return api_service.NewService(
		network, addr,
		api_service.PathPrefixOption(cfg.PathPrefix),
		api_service.AccessLogOption(cfg.AccessLog),
		api_service.AutherOption(auther),
	)
}

func buildMetricsService(cfg *config.MetricsConfig) (service.Service, error) {
	auther := auth_parser.ParseAutherFromAuth(cfg.Auth)
	if cfg.Auther != "" {
		auther = registry.AutherRegistry().Get(cfg.Auther)
	}

	network := "tcp"
	addr := cfg.Addr
	if strings.HasPrefix(addr, "unix://") {
		network = "unix"
		addr = strings.TrimPrefix(addr, "unix://")
	}
	return metrics.NewService(
		network, addr,
		metrics.PathOption(cfg.Path),
		metrics.AutherOption(auther),
	)
}
