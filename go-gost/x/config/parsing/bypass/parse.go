package bypass

import (
	"crypto/tls"
	"strings"

	"github.com/go-gost/core/bypass"
	"github.com/go-gost/core/logger"
	xbypass "github.com/go-gost/x/bypass"
	bypass_plugin "github.com/go-gost/x/bypass/plugin"
	"github.com/go-gost/x/config"
	"github.com/go-gost/x/internal/loader"
	"github.com/go-gost/x/internal/plugin"
	"github.com/go-gost/x/registry"
)

func ParseBypass(cfg *config.BypassConfig) bypass.Bypass {
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
			return bypass_plugin.NewHTTPPlugin(
				cfg.Name, cfg.Plugin.Addr,
				plugin.TLSConfigOption(tlsCfg),
				plugin.TimeoutOption(cfg.Plugin.Timeout),
			)
		default:
			return bypass_plugin.NewGRPCPlugin(
				cfg.Name, cfg.Plugin.Addr,
				plugin.TokenOption(cfg.Plugin.Token),
				plugin.TLSConfigOption(tlsCfg),
			)
		}
	}

	opts := []xbypass.Option{
		xbypass.MatchersOption(cfg.Matchers),
		xbypass.WhitelistOption(cfg.Reverse || cfg.Whitelist),
		xbypass.ReloadPeriodOption(cfg.Reload),
		xbypass.LoggerOption(logger.Default().WithFields(map[string]any{
			"kind":   "bypass",
			"bypass": cfg.Name,
		})),
	}
	if cfg.File != nil && cfg.File.Path != "" {
		opts = append(opts, xbypass.FileLoaderOption(loader.FileLoader(cfg.File.Path)))
	}
	if cfg.Redis != nil && cfg.Redis.Addr != "" {
		opts = append(opts, xbypass.RedisLoaderOption(loader.RedisSetLoader(
			cfg.Redis.Addr,
			loader.DBRedisLoaderOption(cfg.Redis.DB),
			loader.UsernameRedisLoaderOption(cfg.Redis.Username),
			loader.PasswordRedisLoaderOption(cfg.Redis.Password),
			loader.KeyRedisLoaderOption(cfg.Redis.Key),
		)))
	}
	if cfg.HTTP != nil && cfg.HTTP.URL != "" {
		opts = append(opts, xbypass.HTTPLoaderOption(loader.HTTPLoader(
			cfg.HTTP.URL,
			loader.TimeoutHTTPLoaderOption(cfg.HTTP.Timeout),
		)))
	}

	return xbypass.NewBypass(opts...)
}

func List(name string, names ...string) []bypass.Bypass {
	var bypasses []bypass.Bypass
	for _, n := range append([]string{name}, names...) {
		if n == "" {
			continue
		}
		// FIX2-C #8: the built-in flmx-no-private bypass is claimed
		// here, at parse time, instead of going through the registry:
		// the registry wrapper for an unregistered name answers
		// Contains=false (fail-open), so a service referencing this
		// name must never depend on a config-defined bypass existing.
		if n == xbypass.NoPrivateBypassName {
			bypasses = append(bypasses, xbypass.NewNoPrivateBypass())
			continue
		}
		if bp := registry.BypassRegistry().Get(n); bp != nil {
			bypasses = append(bypasses, bp)
		}
	}
	return bypasses
}

// RegisterBuiltinBypasses registers the built-in bypasses into the
// registry for lookup paths that bypass parsing (e.g. handler
// metadata referencing a bypass by name). A config-defined bypass
// with the same name wins: builtins only fill names the operator's
// config did not define. Called by the config loader after it has
// (re)registered the config's own bypasses.
func RegisterBuiltinBypasses() {
	if _, ok := registry.BypassRegistry().GetAll()[xbypass.NoPrivateBypassName]; !ok {
		_ = registry.BypassRegistry().Register(xbypass.NoPrivateBypassName, xbypass.NewNoPrivateBypass())
	}
}
