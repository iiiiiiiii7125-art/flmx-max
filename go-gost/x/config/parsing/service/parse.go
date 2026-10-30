package service

import (
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/go-gost/core/auth"
	"github.com/go-gost/core/chain"
	"github.com/go-gost/core/handler"
	"github.com/go-gost/core/hop"
	"github.com/go-gost/core/listener"
	"github.com/go-gost/core/logger"
	"github.com/go-gost/core/observer"
	"github.com/go-gost/core/observer/stats"
	"github.com/go-gost/core/recorder"
	"github.com/go-gost/core/selector"
	"github.com/go-gost/core/service"
	xadmission "github.com/go-gost/x/admission"
	xauth "github.com/go-gost/x/auth"
	xbypass "github.com/go-gost/x/bypass"
	xchain "github.com/go-gost/x/chain"
	"github.com/go-gost/x/config"
	"github.com/go-gost/x/config/parsing"
	admission_parser "github.com/go-gost/x/config/parsing/admission"
	auth_parser "github.com/go-gost/x/config/parsing/auth"
	bypass_parser "github.com/go-gost/x/config/parsing/bypass"
	hop_parser "github.com/go-gost/x/config/parsing/hop"
	logger_parser "github.com/go-gost/x/config/parsing/logger"
	selector_parser "github.com/go-gost/x/config/parsing/selector"
	tls_util "github.com/go-gost/x/internal/util/tls"
	cache_limiter "github.com/go-gost/x/limiter/traffic/cache"
	"github.com/go-gost/x/metadata"
	mdutil "github.com/go-gost/x/metadata/util"
	xstats "github.com/go-gost/x/observer/stats"
	"github.com/go-gost/x/registry"
	xservice "github.com/go-gost/x/service"
	"github.com/vishvananda/netns"
)

func ParseService(cfg *config.ServiceConfig) (service.Service, error) {
	if cfg.Listener == nil {
		cfg.Listener = &config.ListenerConfig{}
	}
	if strings.TrimSpace(cfg.Listener.Type) == "" {
		cfg.Listener.Type = "tcp"
	}

	if cfg.Handler == nil {
		cfg.Handler = &config.HandlerConfig{}
	}
	if strings.TrimSpace(cfg.Handler.Type) == "" {
		cfg.Handler.Type = "auto"
	}

	log := logger.Default()
	if loggers := logger_parser.List(cfg.Logger, cfg.Loggers...); len(loggers) > 0 {
		log = logger.LoggerGroup(loggers...)
	}

	serviceLogger := log.WithFields(map[string]any{
		"kind":     "service",
		"service":  cfg.Name,
		"listener": cfg.Listener.Type,
		"handler":  cfg.Handler.Type,
	})

	tlsCfg := cfg.Listener.TLS
	if tlsCfg == nil {
		tlsCfg = &config.TLSConfig{}
	}
	tlsConfig, err := tls_util.LoadServerConfig(tlsCfg)
	if err != nil {
		serviceLogger.Error(err)
		return nil, err
	}
	if tlsConfig == nil {
		tlsConfig = parsing.DefaultTLSConfig().Clone()
		tls_util.SetTLSOptions(tlsConfig, tlsCfg.Options)
	}

	authers := auth_parser.List(cfg.Listener.Auther, cfg.Listener.Authers...)
	if len(authers) == 0 {
		if auther := auth_parser.ParseAutherFromAuth(cfg.Listener.Auth); auther != nil {
			authers = append(authers, auther)
		}
	}
	var auther auth.Authenticator
	if len(authers) > 0 {
		auther = xauth.AuthenticatorGroup(authers...)
	}

	admissions := admission_parser.List(cfg.Admission, cfg.Admissions...)

	var sockOpts *chain.SockOpts
	if cfg.SockOpts != nil {
		sockOpts = &chain.SockOpts{
			Mark: cfg.SockOpts.Mark,
		}
	}

	var ppv int
	ifce := cfg.Interface
	var preUp, preDown, postUp, postDown []string
	var ignoreChain bool
	var pStats stats.Stats
	var observerPeriod time.Duration
	var netnsIn, netnsOut string
	var dialTimeout time.Duration

	var limiterRefreshInterval time.Duration
	var limiterCleanupInterval time.Duration
	var limiterScope string

	enableStats := true
	observerPeriod = 5 * time.Second

	if cfg.Metadata != nil {
		md := metadata.NewMetadata(cfg.Metadata)
		ppv = mdutil.GetInt(md, parsing.MDKeyProxyProtocol)
		if v := mdutil.GetString(md, parsing.MDKeyInterface); v != "" {
			ifce = v
		}
		if v := mdutil.GetInt(md, parsing.MDKeySoMark); v > 0 {
			sockOpts = &chain.SockOpts{
				Mark: v,
			}
		}
		preUp = mdutil.GetStrings(md, parsing.MDKeyPreUp)
		preDown = mdutil.GetStrings(md, parsing.MDKeyPreDown)
		postUp = mdutil.GetStrings(md, parsing.MDKeyPostUp)
		postDown = mdutil.GetStrings(md, parsing.MDKeyPostDown)
		// S-01/S-03: 面板纳管的 agent 禁止 service shell hook。
		// preUp/postUp/preDown/postDown 经 /bin/sh -c 以 root 执行，
		// 经面板命令或联邦 peer 下发即为远程 root RCE。面板自身从不设置
		// 这些键，此处 fail-closed 直接拒绝构造。
		if len(preUp) > 0 || len(preDown) > 0 || len(postUp) > 0 || len(postDown) > 0 {
			return nil, fmt.Errorf("service %q: shell hooks (preUp/postUp/preDown/postDown) are disabled on managed agents", cfg.Name)
		}
		ignoreChain = mdutil.GetBool(md, parsing.MDKeyIgnoreChain)

		if md != nil && md.IsExists(parsing.MDKeyEnableStats) {
			enableStats = mdutil.GetBool(md, parsing.MDKeyEnableStats)
		}

		if period := mdutil.GetDuration(md, parsing.MDKeyObserverPeriod, "observePeriod"); period > 0 {
			observerPeriod = period
		}

		netnsIn = mdutil.GetString(md, parsing.MDKeyNetns)
		netnsOut = mdutil.GetString(md, parsing.MDKeyNetnsOut)

		dialTimeout = mdutil.GetDuration(md, parsing.MDKeyDialTimeout)

		limiterRefreshInterval = mdutil.GetDuration(md, parsing.MDKeyLimiterRefreshInterval)
		limiterCleanupInterval = mdutil.GetDuration(md, parsing.MDKeyLimiterCleanupInterval)
		limiterScope = mdutil.GetString(md, parsing.MDKeyLimiterScope)
	}

	if enableStats {
		resetTraffic := true
		if cfg.Metadata != nil {
			md := metadata.NewMetadata(cfg.Metadata)
			if md != nil && md.IsExists(parsing.MDKeyObserverResetTraffic) {
				resetTraffic = mdutil.GetBool(md, parsing.MDKeyObserverResetTraffic)
			}
		}
		pStats = xstats.NewStats(resetTraffic)
	}

	listenerLogger := serviceLogger.WithFields(map[string]any{
		"kind": "listener",
	})

	routerOpts := []chain.RouterOption{
		chain.TimeoutRouterOption(dialTimeout),
		chain.InterfaceRouterOption(ifce),
		chain.NetnsRouterOption(netnsOut),
		chain.SockOptsRouterOption(sockOpts),
		chain.ResolverRouterOption(registry.ResolverRegistry().Get(cfg.Resolver)),
		chain.HostMapperRouterOption(registry.HostsRegistry().Get(cfg.Hosts)),
		chain.LoggerRouterOption(listenerLogger),
	}
	if !ignoreChain {
		routerOpts = append(routerOpts,
			chain.ChainRouterOption(chainGroup(cfg.Listener.Chain, cfg.Listener.ChainGroup)),
		)
	}

	// A02-001: a service guarded by flmx-no-private gets the dial-point
	// target guard on both of its routers. The handler's pre-dial
	// Bypass.Contains and the router's dial used to resolve the target
	// independently (check via the system resolver, dial via the
	// service resolver / hosts mapper / net dialer), so a rebinding
	// DNS could pass the check with a public answer and land on a
	// private address at dial time. The guard re-validates the final
	// resolved target inside Router.Dial and pins hostnames to the
	// validated IP literal.
	noPrivateGuard := serviceHasNoPrivateBypass(cfg)

	listenerRouter := xchain.NewRouter(routerOpts...)
	if noPrivateGuard {
		listenerRouter.SetDialTargetGuard(xbypass.PinNoPrivateDialTarget)
	}

	var trafficLimiter listener.Option
	if cfg.Limiter != "" {
		lim := resolveTrafficLimiter(cfg.Limiter)
		trafficLimiter = listener.TrafficLimiterOption(
			cache_limiter.NewCachedTrafficLimiter(
				lim,
				cache_limiter.RefreshIntervalOption(limiterRefreshInterval),
				cache_limiter.CleanupIntervalOption(limiterCleanupInterval),
				cache_limiter.ScopeOption(limiterScope),
			),
		)
	}

	listenOpts := []listener.Option{
		listener.AddrOption(cfg.Addr),
		listener.RouterOption(listenerRouter),
		listener.AutherOption(auther),
		listener.AuthOption(auth_parser.Info(cfg.Listener.Auth)),
		listener.TLSConfigOption(tlsConfig),
		listener.AdmissionOption(xadmission.AdmissionGroup(admissions...)),
		listener.ConnLimiterOption(resolveConnLimiter(cfg.CLimiter)),
		listener.ServiceOption(cfg.Name),
		listener.ProxyProtocolOption(ppv),
		listener.StatsOption(pStats),
		listener.NetnsOption(netnsIn),
		listener.LoggerOption(listenerLogger),
	}
	if trafficLimiter != nil {
		listenOpts = append(listenOpts, trafficLimiter)
	}

	if netnsIn != "" {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		originNs, err := netns.Get()
		if err != nil {
			return nil, fmt.Errorf("netns.Get(): %v", err)
		}
		defer netns.Set(originNs)

		var ns netns.NsHandle

		if strings.HasPrefix(netnsIn, "/") {
			ns, err = netns.GetFromPath(netnsIn)
		} else {
			ns, err = netns.GetFromName(netnsIn)
		}
		if err != nil {
			return nil, fmt.Errorf("netns.Get(%s): %v", netnsIn, err)
		}
		defer ns.Close()

		if err := netns.Set(ns); err != nil {
			return nil, fmt.Errorf("netns.Set(%s): %v", netnsIn, err)
		}
	}

	var ln listener.Listener
	if rf := registry.ListenerRegistry().Get(cfg.Listener.Type); rf != nil {
		ln = rf(listenOpts...)
	} else {
		return nil, fmt.Errorf("unknown listener: %s", cfg.Listener.Type)
	}

	if cfg.Listener.Metadata == nil {
		cfg.Listener.Metadata = make(map[string]any)
	}
	listenerLogger.Debugf("metadata: %v", cfg.Listener.Metadata)
	if err := ln.Init(metadata.NewMetadata(cfg.Listener.Metadata)); err != nil {
		listenerLogger.Error("init: ", err)
		return nil, err
	}

	// Listener initialization binds the port. If handler/TLS/forwarder parsing
	// fails, release it so a reload rollback can restore the previous listener.
	configured := false
	defer func() {
		if !configured {
			ln.Close()
		}
	}()

	handlerLogger := serviceLogger.WithFields(map[string]any{
		"kind": "handler",
	})

	tlsCfg = cfg.Handler.TLS
	if tlsCfg == nil {
		tlsCfg = &config.TLSConfig{}
	}
	tlsConfig, err = tls_util.LoadServerConfig(tlsCfg)
	if err != nil {
		handlerLogger.Error(err)
		return nil, err
	}
	if tlsConfig == nil {
		tlsConfig = parsing.DefaultTLSConfig().Clone()
		tls_util.SetTLSOptions(tlsConfig, tlsCfg.Options)
	}

	authers = auth_parser.List(cfg.Handler.Auther, cfg.Handler.Authers...)
	if len(authers) == 0 {
		if auther := auth_parser.ParseAutherFromAuth(cfg.Handler.Auth); auther != nil {
			authers = append(authers, auther)
		}
	}

	auther = nil
	if len(authers) > 0 {
		auther = xauth.AuthenticatorGroup(authers...)
	}

	var recorders []recorder.RecorderObject
	for _, r := range cfg.Recorders {
		md := metadata.NewMetadata(r.Metadata)
		recorders = append(recorders, recorder.RecorderObject{
			Recorder: registry.RecorderRegistry().Get(r.Name),
			Record:   r.Record,
			Options: &recorder.Options{
				Direction:       mdutil.GetBool(md, parsing.MDKeyRecorderDirection),
				TimestampFormat: mdutil.GetString(md, parsing.MDKeyRecorderTimestampFormat),
				Hexdump:         mdutil.GetBool(md, parsing.MDKeyRecorderHexdump),
				HTTPBody:        mdutil.GetBool(md, parsing.MDKeyRecorderHTTPBody),
				MaxBodySize:     mdutil.GetInt(md, parsing.MDKeyRecorderHTTPMaxBodySize),
			},
		})
	}

	routerOpts = []chain.RouterOption{
		chain.RetriesRouterOption(cfg.Handler.Retries),
		chain.TimeoutRouterOption(dialTimeout),
		chain.InterfaceRouterOption(ifce),
		chain.NetnsRouterOption(netnsOut),
		chain.SockOptsRouterOption(sockOpts),
		chain.ResolverRouterOption(registry.ResolverRegistry().Get(cfg.Resolver)),
		chain.HostMapperRouterOption(registry.HostsRegistry().Get(cfg.Hosts)),
		chain.RecordersRouterOption(recorders...),
		chain.LoggerRouterOption(handlerLogger),
	}
	if !ignoreChain {
		routerOpts = append(routerOpts,
			chain.ChainRouterOption(chainGroup(cfg.Handler.Chain, cfg.Handler.ChainGroup)),
		)
	}

	handlerRouter := xchain.NewRouter(routerOpts...)
	if noPrivateGuard {
		handlerRouter.SetDialTargetGuard(xbypass.PinNoPrivateDialTarget)
	}

	var h handler.Handler
	if rf := registry.HandlerRegistry().Get(cfg.Handler.Type); rf != nil {
		h = rf(
			handler.RouterOption(handlerRouter),
			handler.AutherOption(auther),
			handler.AuthOption(auth_parser.Info(cfg.Handler.Auth)),
			handler.BypassOption(xbypass.BypassGroup(bypass_parser.List(cfg.Bypass, cfg.Bypasses...)...)),
			handler.TLSConfigOption(tlsConfig),
			handler.RateLimiterOption(registry.RateLimiterRegistry().Get(cfg.RLimiter)),
			handler.TrafficLimiterOption(registry.TrafficLimiterRegistry().Get(cfg.Handler.Limiter)),
			handler.ObserverOption(registry.ObserverRegistry().Get(cfg.Handler.Observer)),
			handler.RecordersOption(recorders...),
			handler.LoggerOption(handlerLogger),
			handler.ServiceOption(cfg.Name),
			handler.NetnsOption(netnsIn),
		)
	} else {
		return nil, fmt.Errorf("unknown handler: %s", cfg.Handler.Type)
	}

	if forwarder, ok := h.(handler.Forwarder); ok {
		hop, err := parseForwarder(cfg.Forwarder, log)
		if err != nil {
			return nil, err
		}
		forwarder.Forward(hop)
	}

	if cfg.Handler.Metadata == nil {
		cfg.Handler.Metadata = make(map[string]any)
	}
	handlerLogger.Debugf("metadata: %v", cfg.Handler.Metadata)
	if err := h.Init(metadata.NewMetadata(cfg.Handler.Metadata)); err != nil {
		handlerLogger.Error("init: ", err)
		return nil, err
	}
	var observer observer.Observer
	// 如果服务名以_tls结尾，则不启用观察器
	if strings.HasSuffix(cfg.Name, "_tls") {
		observer = nil
		fmt.Println("服务名以_tls结尾，跳过观察器启用")
	} else if cfg.Observer != "" {
		observer = registry.ObserverRegistry().Get(cfg.Observer)
	} else if pStats != nil {
		observer = registry.ObserverRegistry().Get("console")
	}

	s := xservice.NewService(cfg.Name, ln, h,
		xservice.AdmissionOption(xadmission.AdmissionGroup(admissions...)),
		xservice.PreUpOption(preUp),
		xservice.PreDownOption(preDown),
		xservice.PostUpOption(postUp),
		xservice.PostDownOption(postDown),
		xservice.RecordersOption(recorders...),
		xservice.StatsOption(pStats),
		xservice.ObserverOption(observer),
		xservice.ObserverPeriodOption(observerPeriod),
		xservice.LoggerOption(serviceLogger),
	)

	serviceLogger.Infof("listening on %s/%s", s.Addr().String(), s.Addr().Network())
	configured = true
	return s, nil
}

// serviceHasNoPrivateBypass reports whether the service references
// the built-in flmx-no-private bypass. Name-based detection is exact
// here: bypass_parser.List resolves this name to the built-in
// implementation at parse time (never to a registry wrapper), and a
// handler's Bypass can only come from cfg.Bypass/cfg.Bypasses — no
// handler metadata overrides it.
func serviceHasNoPrivateBypass(cfg *config.ServiceConfig) bool {
	if cfg == nil {
		return false
	}
	if cfg.Bypass == xbypass.NoPrivateBypassName {
		return true
	}
	for _, name := range cfg.Bypasses {
		if name == xbypass.NoPrivateBypassName {
			return true
		}
	}
	return false
}

func parseForwarder(cfg *config.ForwarderConfig, log logger.Logger) (hop.Hop, error) {
	if cfg == nil {
		return nil, nil
	}

	hopName := cfg.Hop
	if hopName == "" {
		hopName = cfg.Name
	}
	if hopName != "" {
		return registry.HopRegistry().Get(hopName), nil
	}

	hc := config.HopConfig{
		Name:     cfg.Name,
		Selector: cfg.Selector,
	}
	for _, node := range cfg.Nodes {
		if node == nil {
			continue
		}

		filter := node.Filter
		if filter == nil {
			if node.Protocol != "" || node.Host != "" || node.Path != "" {
				filter = &config.NodeFilterConfig{
					Protocol: node.Protocol,
					Host:     node.Host,
					Path:     node.Path,
				}
			}
		}

		httpCfg := node.HTTP
		if node.Auth != nil {
			if httpCfg == nil {
				httpCfg = &config.HTTPNodeConfig{}
			}
			if httpCfg.Auth == nil {
				httpCfg.Auth = node.Auth
			}
		}
		hc.Nodes = append(hc.Nodes, &config.NodeConfig{
			Name:     node.Name,
			Addr:     node.Addr,
			Network:  node.Network,
			Bypass:   node.Bypass,
			Bypasses: node.Bypasses,
			Filter:   filter,
			Matcher:  node.Matcher,
			HTTP:     httpCfg,
			TLS:      node.TLS,
			Metadata: node.Metadata,
		})
	}
	return hop_parser.ParseHop(&hc, log)
}

func chainGroup(name string, group *config.ChainGroupConfig) chain.Chainer {
	var chains []chain.Chainer
	var sel selector.Selector[chain.Chainer]

	if c := registry.ChainRegistry().Get(name); c != nil {
		chains = append(chains, c)
	}
	if group != nil {
		for _, s := range group.Chains {
			if c := registry.ChainRegistry().Get(s); c != nil {
				chains = append(chains, c)
			}
		}
		sel = selector_parser.ParseChainSelector(group.Selector)
	}
	if len(chains) == 0 {
		return nil
	}

	if sel == nil {
		sel = selector_parser.DefaultChainSelector()
	}

	return xchain.NewChainGroup(chains...).
		WithSelector(sel)
}
