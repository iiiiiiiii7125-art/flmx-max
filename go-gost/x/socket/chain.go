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

	// F-A01: snapshot before the config mutation. config.Global()
	// returns a deep clone, so it pins the pre-command state; if the
	// persist inside OnUpdate fails, the runtime registration above
	// must be undone and the in-memory config restored, or the three
	// states (runtime / memory config / disk) split until restart.
	originalConfig := config.Global()
	err = config.OnUpdate(func(c *config.Config) error {
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
	if err != nil {
		registry.ChainRegistry().Unregister(name)
		restoreConfigSnapshot(originalConfig)
		return err
	}
	return nil
}

// restoreConfigSnapshot puts back the pre-command in-memory config
// after a failed OnUpdate (F-A01). config.Set re-publishes derived
// state (idle window) the same way the failed mutation would have.
func restoreConfigSnapshot(snapshot *config.Config) {
	if snapshot != nil {
		config.Set(snapshot)
	}
}

// restoreChainRuntime re-parses the chain entry for name from the
// snapshot config and puts it back into the registry (F-A01 delete /
// update rollback). The previously registered chain object cannot be
// reused: Unregister closes it and ReplaceChain retires it.
func restoreChainRuntime(snapshot *config.Config, name string) {
	if snapshot == nil {
		return
	}
	for _, cc := range snapshot.Chains {
		if cc == nil || cc.Name != name {
			continue
		}
		entry := *cc
		v, err := parser.ParseChain(&entry, logger.Default())
		if err != nil {
			logger.Default().Errorf("rollback chain %s: re-parse failed: %v", name, err)
			return
		}
		if err := registry.ReplaceChain(name, v); err != nil {
			logger.Default().Errorf("rollback chain %s: restore failed: %v", name, err)
		}
		return
	}
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

	// F-A01: same snapshot discipline as createChain — ReplaceChain
	// retires the old runtime chain immediately, so a persist failure
	// inside OnUpdate must restore both the config and a re-parsed
	// runtime chain (or drop the registration if the chain did not
	// exist in config before this upsert).
	originalConfig := config.Global()
	hadRuntime := registry.ChainRegistry().IsRegistered(name)
	if err := registry.ReplaceChain(name, v); err != nil {
		return errors.New("chain " + name + " already exists")
	}

	err = config.OnUpdate(func(c *config.Config) error {
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
	if err != nil {
		restoreConfigSnapshot(originalConfig)
		if hadRuntime || chainInConfig(originalConfig, name) {
			restoreChainRuntime(originalConfig, name)
		} else {
			registry.ChainRegistry().Unregister(name)
		}
		return err
	}
	return nil
}

func chainInConfig(cfg *config.Config, name string) bool {
	if cfg == nil {
		return false
	}
	for _, cc := range cfg.Chains {
		if cc != nil && cc.Name == name {
			return true
		}
	}
	return false
}

func deleteChain(req deleteChainRequest) error {

	name := strings.TrimSpace(req.Chain)

	// F-A01: snapshot first — Unregister below closes the runtime
	// chain, so on persist failure the chain must be re-parsed from
	// the snapshot and the config restored (deleting a chain whose
	// removal never reached disk must not stop it running).
	originalConfig := config.Global()
	hadRuntime := registry.ChainRegistry().IsRegistered(name)
	if hadRuntime {
		registry.ChainRegistry().Unregister(name)
	}

	err := config.OnUpdate(func(c *config.Config) error {
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
	if err != nil {
		restoreConfigSnapshot(originalConfig)
		if hadRuntime {
			restoreChainRuntime(originalConfig, name)
		}
		return err
	}
	return nil
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
