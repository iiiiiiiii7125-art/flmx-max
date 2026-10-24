package socket

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	corelogger "github.com/go-gost/core/logger"
	"github.com/go-gost/x/config"
	_ "github.com/go-gost/x/handler/auto"
	_ "github.com/go-gost/x/listener/tcp"
	xlogger "github.com/go-gost/x/logger"
	"github.com/go-gost/x/registry"
)

// R17-A: createServices 第四阶段曾对 c.Services 做无去重 append。服务已在
// 落盘配置中、但不在注册表里时（例如暂停后 loader 跳过注册、或注册表与
// 配置漂移），面板一次重复 AddService 下发就会把第二份同名服务写进
// 配置并落盘，毒化 gost.json。修复后：同名则替换（与 updateServices
// 第三阶段同口径），配置里已有的多余同名副本一并清除。
func TestCreateServicesDedupesExistingConfigEntry(t *testing.T) {
	corelogger.SetDefault(xlogger.Nop())

	name := "r17a_dup_upsert_tdd"
	stale := config.ServiceConfig{Name: name, Addr: "127.0.0.1:0"}
	originalConfig := config.Global()
	originalPersistPath := config.PersistPath()
	dir := t.TempDir()
	persistFile := filepath.Join(dir, "gost.json")
	t.Cleanup(func() {
		config.Set(originalConfig)
		config.SetPersistPath(originalPersistPath)
		registry.ServiceRegistry().Unregister(name)
	})
	registry.ServiceRegistry().Unregister(name)

	// 陈旧配置项：已在落盘配置中，但不在注册表里。
	config.Set(&config.Config{Services: []*config.ServiceConfig{&stale}})
	config.SetPersistPath(persistFile)
	config.EnablePersist()

	fresh := config.ServiceConfig{Name: name, Addr: "127.0.0.1:0"}
	if err := createServices(createServicesRequest{Data: []config.ServiceConfig{fresh}}); err != nil {
		t.Fatalf("createServices: %v", err)
	}

	// 内存配置：同名只留一份。
	count := 0
	for _, svc := range config.Global().Services {
		if svc != nil && svc.Name == name {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 in-memory entry for %q, got %d: %#v", name, count, config.Global().Services)
	}

	// 落盘配置：无重复名。
	data, err := os.ReadFile(persistFile)
	if err != nil {
		t.Fatalf("read persisted config: %v", err)
	}
	var onDisk config.Config
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatalf("unmarshal persisted config: %v", err)
	}
	seen := map[string]int{}
	for _, svc := range onDisk.Services {
		if svc == nil {
			continue
		}
		seen[svc.Name]++
	}
	for svcName, n := range seen {
		if n != 1 {
			t.Fatalf("persisted config has %d entries for service %q, want 1", n, svcName)
		}
	}
	if seen[name] != 1 {
		t.Fatalf("persisted config missing service %q: %v", name, seen)
	}
}

// R17-A: 配置里已毒化（同名两份）时再下发一次 AddService，落盘后也只留一份。
func TestCreateServicesDedupesAlreadyPoisonedConfig(t *testing.T) {
	corelogger.SetDefault(xlogger.Nop())

	name := "r17a_dup_poisoned_tdd"
	first := config.ServiceConfig{Name: name, Addr: "127.0.0.1:0"}
	second := config.ServiceConfig{Name: name, Addr: "127.0.0.1:0"}
	originalConfig := config.Global()
	originalPersistPath := config.PersistPath()
	dir := t.TempDir()
	persistFile := filepath.Join(dir, "gost.json")
	t.Cleanup(func() {
		config.Set(originalConfig)
		config.SetPersistPath(originalPersistPath)
		registry.ServiceRegistry().Unregister(name)
	})
	registry.ServiceRegistry().Unregister(name)

	config.Set(&config.Config{Services: []*config.ServiceConfig{&first, &second}})
	config.SetPersistPath(persistFile)
	config.EnablePersist()

	fresh := config.ServiceConfig{Name: name, Addr: "127.0.0.1:0"}
	if err := createServices(createServicesRequest{Data: []config.ServiceConfig{fresh}}); err != nil {
		t.Fatalf("createServices: %v", err)
	}

	count := 0
	for _, svc := range config.Global().Services {
		if svc != nil && svc.Name == name {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 in-memory entry for %q, got %d", name, count)
	}
	data, err := os.ReadFile(persistFile)
	if err != nil {
		t.Fatalf("read persisted config: %v", err)
	}
	var onDisk config.Config
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatalf("unmarshal persisted config: %v", err)
	}
	diskCount := 0
	for _, svc := range onDisk.Services {
		if svc != nil && svc.Name == name {
			diskCount++
		}
	}
	if diskCount != 1 {
		t.Fatalf("expected exactly 1 persisted entry for %q, got %d", name, diskCount)
	}
}
