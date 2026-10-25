package socket

import (
	"errors"
	"strings"

	"github.com/go-gost/core/logger"
	"github.com/go-gost/x/config"
	parser "github.com/go-gost/x/config/parsing/chain"
	"github.com/go-gost/x/registry"
)

func createChain(req createChainRequest) error {

	name := strings.TrimSpace(req.Data.Name)
	if name == "" {
		return errors.New("chain name is required")
	}
	req.Data.Name = name

	if registry.ChainRegistry().IsRegistered(name) {
		return errors.New("chain " + name + " already exists")
	}

	v, err := parser.ParseChain(&req.Data, logger.Default())
	if err != nil {
		return errors.New("create chain " + name + " failed: " + err.Error())
	}

	if err := registry.ChainRegistry().Register(name, v); err != nil {
		return errors.New("chain " + name + " already exists")
	}

	return config.OnUpdate(func(c *config.Config) error {
		// R18-F03: 同名则替换（与 updateChain 同口径），不用无去重 append——
		// 链已在配置中、但不在注册表里时，重复 AddChain 下发会把第二份同名链
		// 写进配置并落盘、毒化 gost.json。配置里已有的多余同名副本一并清除。
		// 与 updateChain 的两处有意差异：名字按 TrimSpace 后比较（与
		// DeduplicateServices/DeduplicateChains 同口径）；nil 项做 guard
		// 保留原项不 panic（updateChain 直接取 .Name，nil 项会 panic）。
		data := req.Data
		replaced := false
		kept := make([]*config.ChainConfig, 0, len(c.Chains)+1)
		for _, existing := range c.Chains {
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
		c.Chains = kept
		return nil
	})
}

func updateChain(req updateChainRequest) error {
	name := strings.TrimSpace(req.Chain)
	if name == "" {
		name = strings.TrimSpace(req.Data.Name)
	}
	if name == "" {
		return errors.New("chain name is required")
	}

	req.Data.Name = name
	v, err := parser.ParseChain(&req.Data, logger.Default())
	if err != nil {
		return errors.New("create chain " + name + " failed: " + err.Error())
	}

	if err := registry.ReplaceChain(name, v); err != nil {
		return errors.New("chain " + name + " already exists")
	}

	return config.OnUpdate(func(c *config.Config) error {
		found := false
		for i := range c.Chains {
			// R19-F02: nil 项跳过并记日志（原样保留，清理只走启动自愈），不得 panic。
			if c.Chains[i] == nil {
				logger.Default().Warnf("update chain: skipped nil chain entry at index %d", i)
				continue
			}
			if c.Chains[i].Name == name {
				c.Chains[i] = &req.Data
				found = true
				break
			}
		}
		if !found {
			c.Chains = append(c.Chains, &req.Data)
		}
		return nil
	})
}

func deleteChain(req deleteChainRequest) error {

	name := strings.TrimSpace(req.Chain)

	if registry.ChainRegistry().IsRegistered(name) {
		registry.ChainRegistry().Unregister(name)
	}

	return config.OnUpdate(func(c *config.Config) error {
		chains := c.Chains
		c.Chains = nil
		for _, s := range chains {
			// R19-F02: nil 项原样保留并记日志，不得 panic（清理只走启动自愈）。
			if s == nil {
				logger.Default().Warnf("delete chain: kept nil chain entry as-is")
				c.Chains = append(c.Chains, s)
				continue
			}
			if s.Name == name {
				continue
			}
			c.Chains = append(c.Chains, s)
		}
		return nil
	})
}

type createChainRequest struct {
	Data config.ChainConfig `json:"data"`
}

type updateChainRequest struct {
	Chain string             `json:"chain"`
	Data  config.ChainConfig `json:"data"`
}

type deleteChainRequest struct {
	Chain string `json:"chain"`
}
