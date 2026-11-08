package handler

import (
	"encoding/json"
	"errors"
	"testing"

	"go-backend/internal/store/model"

	"gorm.io/gorm"
)

func TestConfigCleanupPreservesServiceDependencies(t *testing.T) {
	a := newCleanupAgent(t)
	if err := a.h.repo.DB().Create(&model.Forward{ID: 70, UserID: 1, UserName: "local", Name: "local", TunnelID: 1, RemoteAddr: "127.0.0.1:80", Status: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.h.repo.DB().Create(&model.ForwardPort{ForwardID: 70, NodeID: 1, Port: 32000}).Error; err != nil {
		t.Fatal(err)
	}
	a.h.cleanNodeConfigs(1, `{
		"services": [{"name":"70_1_0_tcp", "handler":{"chain":"chains_88"}, "limiter":"13, rule_traffic_limit_70"}],
		"chains": [{"name":"fed_chain_17"}, {"name":"chains_88"}, {"name":"chains_999"}],
		"limiters": [{"name":"13"}, {"name":"rule_traffic_limit_70"}, {"name":"99"}]
	}`)
	a.probe(t)
	if commands := a.commandsOfType("DeleteService"); len(commands) != 0 {
		t.Fatalf("shared service was deleted: %+v", commands)
	}
	assertCleanupDependency(t, a, "DeleteChains", "chain", "chains_999")
	assertCleanupDependency(t, a, "DeleteLimiters", "limiter", "99")
}

func TestConfigCleanupRemovesOrphanedForwardLimiter(t *testing.T) {
	a := newCleanupAgent(t)
	a.h.cleanNodeConfigs(1, `{"limiters":[{"name":"rule_traffic_limit_70"}]}`)
	a.probe(t)
	assertCleanupDependency(t, a, "DeleteLimiters", "limiter", "rule_traffic_limit_70")
}

func TestConfigCleanupPreservesDependenciesOnLookupFailure(t *testing.T) {
	for _, table := range []string{"tunnel", "speed_limit", "forward"} {
		t.Run(table, func(t *testing.T) {
			a := newCleanupAgent(t)
			callback := "test:config-cleanup-query-failure"
			injected := false
			if err := a.h.repo.DB().Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Table == table {
					injected = true
					tx.AddError(errors.New("injected dependency lookup failure"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = a.h.repo.DB().Callback().Query().Remove(callback) })
			configs := map[string]string{
				"tunnel":      `{"chains":[{"name":"chains_88"}]}`,
				"speed_limit": `{"limiters":[{"name":"13"}]}`,
				"forward":     `{"limiters":[{"name":"rule_traffic_limit_70"}]}`,
			}
			a.h.cleanNodeConfigs(1, configs[table])
			a.probe(t)
			if !injected {
				t.Fatal("expected dependency lookup failure to be injected")
			}
			for _, commandType := range []string{"DeleteChains", "DeleteLimiters"} {
				if commands := a.commandsOfType(commandType); len(commands) != 0 {
					t.Fatalf("lookup failure must not authorize cleanup: %+v", commands)
				}
			}
		})
	}
}

func assertCleanupDependency(t *testing.T, a *cleanupAgent, commandType, key, want string) {
	t.Helper()
	commands := a.commandsOfType(commandType)
	if len(commands) != 1 {
		t.Fatalf("expected one %s for %s, got %+v", commandType, want, commands)
	}
	var data map[string]string
	if err := json.Unmarshal(commands[0].Data, &data); err != nil {
		t.Fatal(err)
	}
	if data[key] != want {
		t.Fatalf("unexpected %s target: got %q, want %q", commandType, data[key], want)
	}
}
