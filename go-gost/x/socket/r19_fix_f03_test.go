package socket

import (
	"testing"

	corelogger "github.com/go-gost/core/logger"
	"github.com/go-gost/x/config"
	xlogger "github.com/go-gost/x/logger"
	"github.com/go-gost/x/registry"
)

// R19-X-03（agent 侧一半）：DeleteCLimiters 必须把 climiter 从全局配置
// （从而经 OnUpdate 落盘的 gost.json）中清除。面板侧一旦补发该命令，
// 链路即闭合：命令 → handleDeleteCLimiter → deleteConnLimiter →
// config.OnUpdate（成功即 persist，config.go:134-144）。
//
// 注意：本测试只断言内存态清除；落盘由 OnUpdate/persist 保证（create 侧
// 已有 TestCreateLimiterReportsPersistFailure 覆盖同一 persist 路径），
// 且 persist 开关为进程级全局，不宜在本包测试内翻转。

func TestR19F03DeleteCLimiterRemovesFromGlobalConfig(t *testing.T) {
	corelogger.SetDefault(xlogger.Nop())

	name := "rule_conn_limit_42_r19f03"
	originalConfig := config.Global()
	defer config.Set(originalConfig)
	registry.ConnLimiterRegistry().Unregister(name)
	defer registry.ConnLimiterRegistry().Unregister(name)
	config.Set(&config.Config{})

	if err := createConnLimiter(createLimiterRequest{Data: config.LimiterConfig{Name: name, Limits: []string{"$ 7"}}}); err != nil {
		t.Fatalf("create conn limiter: %v", err)
	}
	if !registry.ConnLimiterRegistry().IsRegistered(name) {
		t.Fatalf("expected conn limiter registered before delete")
	}

	// 走 WS 分发入口（与面板发来的 DeleteCLimiters 命令同一路径）。
	reporter := &WebSocketReporter{}
	if err := reporter.handleDeleteCLimiter(map[string]interface{}{"limiter": name}); err != nil {
		t.Fatalf("handleDeleteCLimiter: %v", err)
	}

	if registry.ConnLimiterRegistry().IsRegistered(name) {
		t.Fatalf("expected conn limiter unregistered after delete")
	}
	for _, lim := range config.Global().CLimiters {
		if lim != nil && lim.Name == name {
			t.Fatalf("expected conn limiter %q removed from global config, got %#v", name, config.Global().CLimiters)
		}
	}
}

func TestR19F03DeleteCLimiterAbsentIsNoop(t *testing.T) {
	corelogger.SetDefault(xlogger.Nop())

	name := "rule_conn_limit_absent_r19f03"
	originalConfig := config.Global()
	defer config.Set(originalConfig)
	registry.ConnLimiterRegistry().Unregister(name)
	defer registry.ConnLimiterRegistry().Unregister(name)
	config.Set(&config.Config{
		CLimiters: []*config.LimiterConfig{{Name: "rule_conn_limit_kept_r19f03", Limits: []string{"$ 1"}}},
	})

	reporter := &WebSocketReporter{}
	if err := reporter.handleDeleteCLimiter(map[string]interface{}{"limiter": name}); err != nil {
		t.Fatalf("deleting an absent conn limiter must not fail: %v", err)
	}
	cfg := config.Global()
	if len(cfg.CLimiters) != 1 || cfg.CLimiters[0] == nil || cfg.CLimiters[0].Name != "rule_conn_limit_kept_r19f03" {
		t.Fatalf("absent delete must leave other limiters untouched, got %#v", cfg.CLimiters)
	}
}
