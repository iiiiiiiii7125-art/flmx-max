package socket

import (
	"path/filepath"
	"testing"

	corelogger "github.com/go-gost/core/logger"
	"github.com/go-gost/x/config"
	_ "github.com/go-gost/x/handler/auto"
	_ "github.com/go-gost/x/listener/tcp"
	xlogger "github.com/go-gost/x/logger"
	"github.com/go-gost/x/registry"
)

// D1-06: createServices starts the runtime services before persisting the
// config. If persistence fails, the created services must be rolled back
// (unregistered/closed and removed from the in-memory config), otherwise
// the panel is told "failed" while the service keeps running and vanishes
// on the next agent restart.
func TestCreateServicesPersistFailureRollsBackRuntime(t *testing.T) {
	corelogger.SetDefault(xlogger.Nop())

	name := "create_persist_rollback_tdd"
	originalConfig := config.Global()
	originalPersistPath := config.PersistPath()
	t.Cleanup(func() {
		config.Set(originalConfig)
		config.SetPersistPath(originalPersistPath)
		registry.ServiceRegistry().Unregister(name)
	})
	registry.ServiceRegistry().Unregister(name)
	config.Set(&config.Config{})
	config.SetPersistPath(filepath.Join(t.TempDir(), "missing", "gost.json"))
	config.EnablePersist()

	err := createServices(createServicesRequest{Data: []config.ServiceConfig{
		{Name: name, Addr: "127.0.0.1:0"},
	}})
	if err == nil {
		t.Fatal("expected persist failure to be returned")
	}
	if registry.ServiceRegistry().Get(name) != nil {
		t.Fatal("created service must be rolled back when persistence fails")
	}
	cfg := config.Global()
	for _, svc := range cfg.Services {
		if svc != nil && svc.Name == name {
			t.Fatal("failed service must not remain in the in-memory config")
		}
	}
}
