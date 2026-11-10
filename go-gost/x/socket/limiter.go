package socket

import (
	"errors"
	"strings"

	"github.com/go-gost/core/logger"
	"github.com/go-gost/x/config"
	parser "github.com/go-gost/x/config/parsing/limiter"
	"github.com/go-gost/x/registry"
)

func createLimiter(req createLimiterRequest) error {
	name := strings.TrimSpace(req.Data.Name)
	if name == "" {
		return errors.New("limiter name is required")
	}
	req.Data.Name = name

	if registry.TrafficLimiterRegistry().IsRegistered(name) {
		return errors.New("limiter " + name + " already exists")
	}

	v := parser.ParseTrafficLimiter(&req.Data)

	if err := registry.TrafficLimiterRegistry().Register(name, v); err != nil {
		return errors.New("limiter " + name + " already exists")
	}

	// F-A01: snapshot + rollback discipline, see createChain.
	originalConfig := config.Global()
	err := config.OnUpdate(func(c *config.Config) error {
		// R18-F03: 同名则替换（与 updateLimiter 同口径），不用无去重 append——
		// 限流器已在配置中、但不在注册表里时，重复 AddLimiter 下发会把第二份
		// 同名限流器写进配置并落盘、毒化 gost.json。配置里已有的多余同名副本
		// 一并清除。名字按 TrimSpace 后比较（与 DeduplicateLimiters 同口径）；
		// nil 项做 guard 保留原项不 panic（updateLimiter 直接取 .Name）。
		data := req.Data
		replaced := false
		kept := make([]*config.LimiterConfig, 0, len(c.Limiters)+1)
		for _, existing := range c.Limiters {
			if existing != nil && strings.TrimSpace(existing.Name) == data.Name {
				if replaced {
					continue
				}
				kept = append(kept, &data)
				replaced = true
				continue
			}
			kept = append(kept, existing)
		}
		if !replaced {
			kept = append(kept, &data)
		}
		c.Limiters = kept
		return nil
	})
	if err != nil {
		registry.TrafficLimiterRegistry().Unregister(name)
		restoreConfigSnapshot(originalConfig)
		return err
	}
	return nil
}

func updateLimiter(req updateLimiterRequest) error {

	// F-A02: name resolution mirrors updateChain — the wrapper field
	// wins, the data's own name is the fallback; an empty result is
	// rejected instead of registering an empty-named limiter.
	name := strings.TrimSpace(req.Limiter)
	if name == "" {
		name = strings.TrimSpace(req.Data.Name)
	}
	if name == "" {
		return errors.New("limiter name is required")
	}

	// F-A01: snapshot before Unregister closes the old runtime
	// limiter; a persist failure restores config + re-parsed runtime.
	originalConfig := config.Global()
	hadRuntime := registry.TrafficLimiterRegistry().IsRegistered(name)
	if hadRuntime {
		registry.TrafficLimiterRegistry().Unregister(name)
	}

	req.Data.Name = name

	v := parser.ParseTrafficLimiter(&req.Data)

	if err := registry.TrafficLimiterRegistry().Register(name, v); err != nil {
		if hadRuntime {
			restoreTrafficLimiterRuntime(originalConfig, name)
		}
		return errors.New("limiter " + name + " already exists")
	}

	err := config.OnUpdate(func(c *config.Config) error {
		found := false
		for i := range c.Limiters {
			// R19-F02: nil 项跳过并记日志（原样保留，清理只走启动自愈），不得 panic。
			if c.Limiters[i] == nil {
				logger.Default().Warnf("update limiter: skipped nil limiter entry at index %d", i)
				continue
			}
			if c.Limiters[i].Name == name {
				c.Limiters[i] = &req.Data
				found = true
				break
			}
		}
		if !found {
			c.Limiters = append(c.Limiters, &req.Data)
		}
		return nil
	})
	if err != nil {
		restoreConfigSnapshot(originalConfig)
		registry.TrafficLimiterRegistry().Unregister(name)
		if hadRuntime || limiterInConfig(originalConfig, name, false) {
			restoreTrafficLimiterRuntime(originalConfig, name)
		}
		return err
	}
	return nil
}

// restoreTrafficLimiterRuntime re-parses the snapshot's limiter entry
// back into the traffic limiter registry (F-A01 rollback).
func restoreTrafficLimiterRuntime(snapshot *config.Config, name string) {
	if snapshot == nil {
		return
	}
	for _, lc := range snapshot.Limiters {
		if lc == nil || lc.Name != name {
			continue
		}
		entry := *lc
		if err := registry.TrafficLimiterRegistry().Register(name, parser.ParseTrafficLimiter(&entry)); err != nil {
			logger.Default().Errorf("rollback limiter %s: restore failed: %v", name, err)
		}
		return
	}
}

// restoreConnLimiterRuntime is restoreTrafficLimiterRuntime for the
// conn limiter registry.
func restoreConnLimiterRuntime(snapshot *config.Config, name string) {
	if snapshot == nil {
		return
	}
	for _, lc := range snapshot.CLimiters {
		if lc == nil || lc.Name != name {
			continue
		}
		entry := *lc
		if err := registry.ConnLimiterRegistry().Register(name, parser.ParseConnLimiter(&entry)); err != nil {
			logger.Default().Errorf("rollback conn limiter %s: restore failed: %v", name, err)
		}
		return
	}
}

func limiterInConfig(cfg *config.Config, name string, conn bool) bool {
	if cfg == nil {
		return false
	}
	entries := cfg.Limiters
	if conn {
		entries = cfg.CLimiters
	}
	for _, lc := range entries {
		if lc != nil && lc.Name == name {
			return true
		}
	}
	return false
}

func deleteLimiter(req deleteLimiterRequest) error {

	name := strings.TrimSpace(req.Limiter)

	// F-A01: snapshot + rollback discipline, see deleteChain.
	originalConfig := config.Global()
	hadRuntime := registry.TrafficLimiterRegistry().IsRegistered(name)
	if hadRuntime {
		registry.TrafficLimiterRegistry().Unregister(name)
	}

	err := config.OnUpdate(func(c *config.Config) error {
		limiteres := c.Limiters
		c.Limiters = nil
		for _, s := range limiteres {
			// R19-F02: nil 项原样保留并记日志，不得 panic（清理只走启动自愈）。
			if s == nil {
				logger.Default().Warnf("delete limiter: kept nil limiter entry as-is")
				c.Limiters = append(c.Limiters, s)
				continue
			}
			if s.Name == name {
				continue
			}
			c.Limiters = append(c.Limiters, s)
		}
		return nil
	})
	if err != nil {
		restoreConfigSnapshot(originalConfig)
		if hadRuntime {
			restoreTrafficLimiterRuntime(originalConfig, name)
		}
		return err
	}
	return nil
}

type createLimiterRequest struct {
	Data config.LimiterConfig `json:"data"`
}

type updateLimiterRequest struct {
	Limiter string               `json:"limiter"`
	Data    config.LimiterConfig `json:"data"`
}

type deleteLimiterRequest struct {
	Limiter string `json:"limiter"`
}

func createConnLimiter(req createLimiterRequest) error {
	name := strings.TrimSpace(req.Data.Name)
	if name == "" {
		return errors.New("limiter name is required")
	}
	req.Data.Name = name

	if registry.ConnLimiterRegistry().IsRegistered(name) {
		return errors.New("conn limiter " + name + " already exists")
	}

	v := parser.ParseConnLimiter(&req.Data)

	if err := registry.ConnLimiterRegistry().Register(name, v); err != nil {
		return errors.New("conn limiter " + name + " already exists")
	}

	// F-A01: snapshot + rollback discipline, see createChain.
	originalConfig := config.Global()
	err := config.OnUpdate(func(c *config.Config) error {
		// R18-F03: 同名则替换（与 updateConnLimiter 同口径），不用无去重 append——
		// 连接限流器已在配置中、但不在注册表里时，重复下发会把第二份同名项写进
		// 配置并落盘、毒化 gost.json。配置里已有的多余同名副本一并清除。名字
		// 按 TrimSpace 后比较（与 DeduplicateCLimiters 同口径）；nil 项做 guard
		// 保留原项不 panic（updateConnLimiter 直接取 .Name）。
		data := req.Data
		replaced := false
		kept := make([]*config.LimiterConfig, 0, len(c.CLimiters)+1)
		for _, existing := range c.CLimiters {
			if existing != nil && strings.TrimSpace(existing.Name) == data.Name {
				if replaced {
					continue
				}
				kept = append(kept, &data)
				replaced = true
				continue
			}
			kept = append(kept, existing)
		}
		if !replaced {
			kept = append(kept, &data)
		}
		c.CLimiters = kept
		return nil
	})
	if err != nil {
		registry.ConnLimiterRegistry().Unregister(name)
		restoreConfigSnapshot(originalConfig)
		return err
	}
	return nil
}

func updateConnLimiter(req updateLimiterRequest) error {
	// F-A02: name resolution mirrors updateLimiter.
	name := strings.TrimSpace(req.Limiter)
	if name == "" {
		name = strings.TrimSpace(req.Data.Name)
	}
	if name == "" {
		return errors.New("limiter name is required")
	}
	req.Data.Name = name

	// F-A01: snapshot + rollback discipline, see updateLimiter.
	originalConfig := config.Global()
	hadRuntime := registry.ConnLimiterRegistry().IsRegistered(name)
	if hadRuntime {
		registry.ConnLimiterRegistry().Unregister(name)
	}

	v := parser.ParseConnLimiter(&req.Data)

	if err := registry.ConnLimiterRegistry().Register(name, v); err != nil {
		if hadRuntime {
			restoreConnLimiterRuntime(originalConfig, name)
		}
		return errors.New("conn limiter " + name + " already exists")
	}

	err := config.OnUpdate(func(c *config.Config) error {
		for i := range c.CLimiters {
			// R19-F02: nil 项跳过并记日志（原样保留，清理只走启动自愈），不得 panic。
			if c.CLimiters[i] == nil {
				logger.Default().Warnf("update conn limiter: skipped nil conn limiter entry at index %d", i)
				continue
			}
			if c.CLimiters[i].Name == name {
				c.CLimiters[i] = &req.Data
				return nil
			}
		}
		c.CLimiters = append(c.CLimiters, &req.Data)
		return nil
	})
	if err != nil {
		restoreConfigSnapshot(originalConfig)
		registry.ConnLimiterRegistry().Unregister(name)
		if hadRuntime || limiterInConfig(originalConfig, name, true) {
			restoreConnLimiterRuntime(originalConfig, name)
		}
		return err
	}
	return nil
}

func deleteConnLimiter(req deleteLimiterRequest) error {
	name := strings.TrimSpace(req.Limiter)

	// F-A01: snapshot + rollback discipline, see deleteChain.
	originalConfig := config.Global()
	hadRuntime := registry.ConnLimiterRegistry().IsRegistered(name)
	if hadRuntime {
		registry.ConnLimiterRegistry().Unregister(name)
	}

	err := config.OnUpdate(func(c *config.Config) error {
		limiteres := c.CLimiters
		c.CLimiters = nil
		for _, s := range limiteres {
			// R19-F02: nil 项原样保留并记日志，不得 panic（清理只走启动自愈）。
			if s == nil {
				logger.Default().Warnf("delete conn limiter: kept nil conn limiter entry as-is")
				c.CLimiters = append(c.CLimiters, s)
				continue
			}
			if s.Name == name {
				continue
			}
			c.CLimiters = append(c.CLimiters, s)
		}
		return nil
	})
	if err != nil {
		restoreConfigSnapshot(originalConfig)
		if hadRuntime {
			restoreConnLimiterRuntime(originalConfig, name)
		}
		return err
	}
	return nil
}
