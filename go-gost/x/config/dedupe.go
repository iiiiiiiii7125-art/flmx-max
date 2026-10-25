package config

import (
	"fmt"
	"strings"
)

// HealPersistedServices dedupes cfg.Services in place (see
// DeduplicateServices) and, when duplicates were removed, writes the healed
// config back to the persist path so the next start does not read the
// poisoned file again. It returns the removed service names; err is non-nil
// only when duplicates were removed but the re-persist failed — the
// in-memory config is healed either way, so the caller should warn, not
// abort startup (the next start will self-heal again).
func HealPersistedServices(cfg *Config) ([]string, error) {
	removed := DeduplicateServices(cfg)
	if len(removed) == 0 {
		return nil, nil
	}
	if err := PersistNow(cfg); err != nil {
		return removed, err
	}
	return removed, nil
}

// DeduplicateServices removes later entries in cfg.Services that repeat an
// earlier service name (names compared after trimming whitespace), keeping
// the first occurrence. It returns the removed names in removal order.
//
// R17-A: a persisted gost.json poisoned with duplicate service names makes
// the agent fail at startup (loader.Load cannot register the same name
// twice — the second copy fails at listener bind or with
// registry: duplicate object — and program.Start returns the error).
// Startup self-heal calls this before loading, and createServices relies on
// the same first-wins rule when it upserts into the live config.
//
// A nil cfg or a config without duplicates returns nil and leaves cfg
// untouched. Nil service entries are kept as-is.
func DeduplicateServices(cfg *Config) []string {
	if cfg == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(cfg.Services))
	var removed []string
	kept := cfg.Services[:0]
	for _, svc := range cfg.Services {
		if svc == nil {
			kept = append(kept, svc)
			continue
		}
		name := strings.TrimSpace(svc.Name)
		if _, ok := seen[name]; ok {
			removed = append(removed, svc.Name)
			continue
		}
		seen[name] = struct{}{}
		kept = append(kept, svc)
	}
	if len(removed) == 0 {
		return nil
	}
	cfg.Services = kept
	return removed
}

// R18-F03: 启动自愈从仅 Services（R17-A）扩展到 chains / limiters /
// climiters。毒化 gost.json 里同名链/限流器各两份时，未经去重进入
// loader.Load 会在第二个同名项的 Register 处报 registry: duplicate
// object、program.Start 返回错误、agent 退出——与 R17-X-01 的服务毒化
// 同型（链/限流器无监听器 eager 绑定，故只有 duplicate object 这一支）。
// 三类落盘在同一 gost.json、同一 PersistNow 路径、同一 program.go 接线
// 点（Parse 之后、loader.Load 之前），故扩展无结构性障碍。
//
// 口径与 DeduplicateServices 一致：保留首次出现，去掉后续同名项（名按
// TrimSpace 后比较），nil 项原样保留。DeduplicateServices 保持原实现不
// 动（R17 已证稳），三类新函数共用下方的 dedupeNamed helper。
func HealPersistedChains(cfg *Config) ([]string, error) {
	removed := DeduplicateChains(cfg)
	if len(removed) == 0 {
		return nil, nil
	}
	if err := PersistNow(cfg); err != nil {
		return removed, err
	}
	return removed, nil
}

// DeduplicateChains removes later entries in cfg.Chains that repeat an
// earlier chain name, keeping the first occurrence. See DeduplicateServices
// for the rule; nil chain entries are kept as-is.
func DeduplicateChains(cfg *Config) []string {
	if cfg == nil {
		return nil
	}
	kept, removed := dedupeNamed(cfg.Chains, func(ch *ChainConfig) string { return ch.Name })
	if len(removed) == 0 {
		return nil
	}
	cfg.Chains = kept
	return removed
}

func HealPersistedLimiters(cfg *Config) ([]string, error) {
	removed := DeduplicateLimiters(cfg)
	if len(removed) == 0 {
		return nil, nil
	}
	if err := PersistNow(cfg); err != nil {
		return removed, err
	}
	return removed, nil
}

// DeduplicateLimiters removes later entries in cfg.Limiters that repeat an
// earlier limiter name, keeping the first occurrence. Nil entries are kept
// as-is.
func DeduplicateLimiters(cfg *Config) []string {
	if cfg == nil {
		return nil
	}
	kept, removed := dedupeNamed(cfg.Limiters, func(lim *LimiterConfig) string { return lim.Name })
	if len(removed) == 0 {
		return nil
	}
	cfg.Limiters = kept
	return removed
}

func HealPersistedCLimiters(cfg *Config) ([]string, error) {
	removed := DeduplicateCLimiters(cfg)
	if len(removed) == 0 {
		return nil, nil
	}
	if err := PersistNow(cfg); err != nil {
		return removed, err
	}
	return removed, nil
}

// DeduplicateCLimiters removes later entries in cfg.CLimiters that repeat
// an earlier conn limiter name, keeping the first occurrence. Nil entries
// are kept as-is.
func DeduplicateCLimiters(cfg *Config) []string {
	if cfg == nil {
		return nil
	}
	kept, removed := dedupeNamed(cfg.CLimiters, func(lim *LimiterConfig) string { return lim.Name })
	if len(removed) == 0 {
		return nil
	}
	cfg.CLimiters = kept
	return removed
}

// R18-INT（集成裁定）：RLimiters 同样进自愈。x/socket 内无 rate limiter
// create 路径（F-03 全仓 grep 零命中），故 create 侧无第四处盲 append；
// 但 loader.Load 会加载 cfg.RLimiters（loader.go:218-221，第二个同名项
// 在 RateLimiterRegistry.Register 处报 registry: duplicate object、
// program.Start 返回错误、agent 退出），毒化 rlimiters 段与服务/链/
// 限流器毒化同型。F-04 已在 x/api 侧给 rate_limiter create 按名去重，
// 此处补自愈一侧，缺一面自愈即不完整。
func HealPersistedRLimiters(cfg *Config) ([]string, error) {
	removed := DeduplicateRLimiters(cfg)
	if len(removed) == 0 {
		return nil, nil
	}
	if err := PersistNow(cfg); err != nil {
		return removed, err
	}
	return removed, nil
}

// DeduplicateRLimiters removes later entries in cfg.RLimiters that repeat
// an earlier rate limiter name, keeping the first occurrence. Nil entries
// are kept as-is.
func DeduplicateRLimiters(cfg *Config) []string {
	if cfg == nil {
		return nil
	}
	kept, removed := dedupeNamed(cfg.RLimiters, func(lim *LimiterConfig) string { return lim.Name })
	if len(removed) == 0 {
		return nil
	}
	cfg.RLimiters = kept
	return removed
}

// R19-F02: 剥离全部 17 类命名切片中的 nil 项（落盘 JSON null 读入后的
// 内存形态），专用于启动/重载自愈。nil 项在此被清除并重新落盘——nil 项
// 拒绝落盘、不得进自愈保留；Deduplicate* 保持“nil 项原样保留”的已证稳
// 口径不动（见 r18_fix_f03_test.go 的断言）。运行时 update/delete 入口
// 亦只跳过 nil、不清除（见 x/api、x/socket 的 R19-F02 守卫）。
// 返回按类记的清除描述（如 "2 nil service(s)"），无清除时返回 nil。
// cfg 为 nil 时返回 nil，不改动任何配置。
func StripNilConfigItems(cfg *Config) []string {
	if cfg == nil {
		return nil
	}
	var stripped []string
	note := func(kind string, n int) {
		if n > 0 {
			stripped = append(stripped, fmt.Sprintf("%d nil %s(s)", n, kind))
		}
	}
	if kept, n := stripNil(cfg.Services); n > 0 {
		cfg.Services = kept
		note("service", n)
	}
	if kept, n := stripNil(cfg.Chains); n > 0 {
		cfg.Chains = kept
		note("chain", n)
	}
	if kept, n := stripNil(cfg.Hops); n > 0 {
		cfg.Hops = kept
		note("hop", n)
	}
	if kept, n := stripNil(cfg.Authers); n > 0 {
		cfg.Authers = kept
		note("auther", n)
	}
	if kept, n := stripNil(cfg.Admissions); n > 0 {
		cfg.Admissions = kept
		note("admission", n)
	}
	if kept, n := stripNil(cfg.Bypasses); n > 0 {
		cfg.Bypasses = kept
		note("bypass", n)
	}
	if kept, n := stripNil(cfg.Resolvers); n > 0 {
		cfg.Resolvers = kept
		note("resolver", n)
	}
	if kept, n := stripNil(cfg.Hosts); n > 0 {
		cfg.Hosts = kept
		note("hosts", n)
	}
	if kept, n := stripNil(cfg.Ingresses); n > 0 {
		cfg.Ingresses = kept
		note("ingress", n)
	}
	if kept, n := stripNil(cfg.Routers); n > 0 {
		cfg.Routers = kept
		note("router", n)
	}
	if kept, n := stripNil(cfg.SDs); n > 0 {
		cfg.SDs = kept
		note("sd", n)
	}
	if kept, n := stripNil(cfg.Observers); n > 0 {
		cfg.Observers = kept
		note("observer", n)
	}
	if kept, n := stripNil(cfg.Recorders); n > 0 {
		cfg.Recorders = kept
		note("recorder", n)
	}
	if kept, n := stripNil(cfg.Limiters); n > 0 {
		cfg.Limiters = kept
		note("limiter", n)
	}
	if kept, n := stripNil(cfg.CLimiters); n > 0 {
		cfg.CLimiters = kept
		note("conn limiter", n)
	}
	if kept, n := stripNil(cfg.RLimiters); n > 0 {
		cfg.RLimiters = kept
		note("rate limiter", n)
	}
	if kept, n := stripNil(cfg.Loggers); n > 0 {
		cfg.Loggers = kept
		note("logger", n)
	}
	return stripped
}

// stripNil removes nil entries from items, returning the kept slice and the
// removed count. The kept slice reuses the backing array (same idiom as
// DeduplicateServices); callers reassign the result only when count > 0.
func stripNil[T any](items []*T) ([]*T, int) {
	kept := items[:0]
	removed := 0
	for _, item := range items {
		if item == nil {
			removed++
			continue
		}
		kept = append(kept, item)
	}
	return kept, removed
}

// dedupeNamed is the shared first-wins dedupe behind DeduplicateChains /
// DeduplicateLimiters / DeduplicateCLimiters / DeduplicateRLimiters: names compared after trimming
// whitespace, nil entries kept as-is. It returns the kept slice and the
// removed names (untrimmed, in removal order); when nothing was removed it
// returns (nil, nil) so callers leave the config untouched.
func dedupeNamed[T any](items []*T, nameOf func(*T) string) ([]*T, []string) {
	seen := make(map[string]struct{}, len(items))
	var removed []string
	kept := items[:0]
	for _, item := range items {
		if item == nil {
			kept = append(kept, item)
			continue
		}
		name := strings.TrimSpace(nameOf(item))
		if _, ok := seen[name]; ok {
			removed = append(removed, nameOf(item))
			continue
		}
		seen[name] = struct{}{}
		kept = append(kept, item)
	}
	if len(removed) == 0 {
		return nil, nil
	}
	return kept, removed
}
