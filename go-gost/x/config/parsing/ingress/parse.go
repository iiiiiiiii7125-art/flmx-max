package ingress

import (
	"crypto/tls"
	"strings"

	"github.com/go-gost/core/ingress"
	"github.com/go-gost/core/logger"
	"github.com/go-gost/x/config"
	xingress "github.com/go-gost/x/ingress"
	ingress_plugin "github.com/go-gost/x/ingress/plugin"
	"github.com/go-gost/x/internal/loader"
	"github.com/go-gost/x/internal/plugin"
	xlogger "github.com/go-gost/x/logger"
)

func ParseIngress(cfg *config.IngressConfig) ingress.Ingress {
	if cfg == nil {
		return nil
	}

	if cfg.Plugin != nil {
		var tlsCfg *tls.Config
		if cfg.Plugin.TLS != nil {
			tlsCfg = &tls.Config{
				ServerName:         cfg.Plugin.TLS.ServerName,
				InsecureSkipVerify: !cfg.Plugin.TLS.Secure,
			}
		}
		switch strings.ToLower(cfg.Plugin.Type) {
		case "http":
			return ingress_plugin.NewHTTPPlugin(
				cfg.Name, cfg.Plugin.Addr,
				plugin.TLSConfigOption(tlsCfg),
				plugin.TimeoutOption(cfg.Plugin.Timeout),
			)
		default:
			return ingress_plugin.NewGRPCPlugin(
				cfg.Name, cfg.Plugin.Addr,
				plugin.TokenOption(cfg.Plugin.Token),
				plugin.TLSConfigOption(tlsCfg),
			)
		}
	}

	var rules []*ingress.Rule
	for _, rule := range cfg.Rules {
		if rule.Hostname == "" || rule.Endpoint == "" {
			continue
		}

		rules = append(rules, &ingress.Rule{
			Hostname: rule.Hostname,
			Endpoint: rule.Endpoint,
		})
	}
	// logger.Default() is nil until the host process registers one
	// (gost's main does so at startup). A caller that never called
	// SetDefault must not panic here (R9-B-01): fall back to a
	// no-op logger.
	log := logger.Default()
	if log == nil {
		log = xlogger.Nop()
	}

	opts := []xingress.Option{
		xingress.RulesOption(rules),
		xingress.ReloadPeriodOption(cfg.Reload),
		xingress.LoggerOption(log.WithFields(map[string]any{
			"kind":    "ingress",
			"ingress": cfg.Name,
		})),
	}
	if cfg.File != nil && cfg.File.Path != "" {
		opts = append(opts, xingress.FileLoaderOption(loader.FileLoader(cfg.File.Path)))
	}
	if cfg.Redis != nil && cfg.Redis.Addr != "" {
		switch cfg.Redis.Type {
		case "set": // redis set
			opts = append(opts, xingress.RedisLoaderOption(loader.RedisSetLoader(
				cfg.Redis.Addr,
				loader.DBRedisLoaderOption(cfg.Redis.DB),
				loader.UsernameRedisLoaderOption(cfg.Redis.Username),
				loader.PasswordRedisLoaderOption(cfg.Redis.Password),
				loader.KeyRedisLoaderOption(cfg.Redis.Key),
			)))
		default: // redis hash
			opts = append(opts, xingress.RedisLoaderOption(loader.RedisHashLoader(
				cfg.Redis.Addr,
				loader.DBRedisLoaderOption(cfg.Redis.DB),
				loader.UsernameRedisLoaderOption(cfg.Redis.Username),
				loader.PasswordRedisLoaderOption(cfg.Redis.Password),
				loader.KeyRedisLoaderOption(cfg.Redis.Key),
			)))
		}
	}
	if cfg.HTTP != nil && cfg.HTTP.URL != "" {
		opts = append(opts, xingress.HTTPLoaderOption(loader.HTTPLoader(
			cfg.HTTP.URL,
			loader.TimeoutHTTPLoaderOption(cfg.HTTP.Timeout),
		)))
	}
	return xingress.NewIngress(opts...)
}
