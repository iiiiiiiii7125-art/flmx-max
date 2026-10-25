package socket

import (
	"errors"
	"github.com/go-gost/x/config"
	parser "github.com/go-gost/x/config/parsing/limiter"
	"github.com/go-gost/x/registry"
	"strings"
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

	return config.OnUpdate(func(c *config.Config) error {
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
}

func updateLimiter(req updateLimiterRequest) error {

	name := strings.TrimSpace(req.Limiter)

	if registry.TrafficLimiterRegistry().IsRegistered(name) {
		registry.TrafficLimiterRegistry().Unregister(name)
	}

	req.Data.Name = name

	v := parser.ParseTrafficLimiter(&req.Data)

	if err := registry.TrafficLimiterRegistry().Register(name, v); err != nil {
		return errors.New("limiter " + name + " already exists")
	}

	return config.OnUpdate(func(c *config.Config) error {
		found := false
		for i := range c.Limiters {
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
}

func deleteLimiter(req deleteLimiterRequest) error {

	name := strings.TrimSpace(req.Limiter)

	if registry.TrafficLimiterRegistry().IsRegistered(name) {
		registry.TrafficLimiterRegistry().Unregister(name)
	}

	return config.OnUpdate(func(c *config.Config) error {
		limiteres := c.Limiters
		c.Limiters = nil
		for _, s := range limiteres {
			if s.Name == name {
				continue
			}
			c.Limiters = append(c.Limiters, s)
		}
		return nil
	})
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

	return config.OnUpdate(func(c *config.Config) error {
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
}

func updateConnLimiter(req updateLimiterRequest) error {
	name := strings.TrimSpace(req.Limiter)
	req.Data.Name = name
	if registry.ConnLimiterRegistry().IsRegistered(name) {
		registry.ConnLimiterRegistry().Unregister(name)
	}

	v := parser.ParseConnLimiter(&req.Data)

	if err := registry.ConnLimiterRegistry().Register(name, v); err != nil {
		return errors.New("conn limiter " + name + " already exists")
	}

	return config.OnUpdate(func(c *config.Config) error {
		for i := range c.CLimiters {
			if c.CLimiters[i].Name == name {
				c.CLimiters[i] = &req.Data
				return nil
			}
		}
		c.CLimiters = append(c.CLimiters, &req.Data)
		return nil
	})
}

func deleteConnLimiter(req deleteLimiterRequest) error {
	name := strings.TrimSpace(req.Limiter)

	if registry.ConnLimiterRegistry().IsRegistered(name) {
		registry.ConnLimiterRegistry().Unregister(name)
	}

	return config.OnUpdate(func(c *config.Config) error {
		limiteres := c.CLimiters
		c.CLimiters = nil
		for _, s := range limiteres {
			if s.Name == name {
				continue
			}
			c.CLimiters = append(c.CLimiters, s)
		}
		return nil
	})
}
