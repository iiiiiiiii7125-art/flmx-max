package socket

import (
	"strings"
	"testing"

	"github.com/go-gost/x/config"
	"github.com/go-gost/x/registry"
)

// R2-A01-003 回归（socket 面）：已暂停服务在 agent 重启后必须能 resume。
//
// 背景：loader 加载时跳过 Metadata.paused=true 的服务、不注册；旧
// resumeServices 第一阶段以注册表存在为前置，重启后恢复必报
// "service not found"，与 updateServices 的 upsert 口径矛盾。本测试
// 直接种子重启后状态（配置在册 + paused、注册表无此服务）验证按配置
// 重建；同时锁定两个不许放宽的口径：全未知服务仍报 not found、配置
// 在册但未暂停且未注册仍报 not found。

func seedRestartPausedSocketService(t *testing.T, name string) {
	t.Helper()
	r19SeedSocketConfig(t, &config.Config{})
	registry.ServiceRegistry().Unregister(name)
	t.Cleanup(func() {
		if svc := registry.ServiceRegistry().Get(name); svc != nil {
			svc.Close()
		}
		registry.ServiceRegistry().Unregister(name)
	})
	if !r19SafeSet(t, &config.Config{Services: []*config.ServiceConfig{
		{Name: name, Addr: "127.0.0.1:0", Metadata: map[string]any{"paused": true}},
	}}) {
		t.Fatalf("cannot seed post-restart config")
	}
}

func TestR2A01003ResumePausedServiceAfterRestart(t *testing.T) {
	name := "r2a01003-resume-svc"
	seedRestartPausedSocketService(t, name)

	if err := resumeServices(resumeServicesRequest{Services: []string{name}}); err != nil {
		t.Fatalf("resume paused-but-unregistered service: %v", err)
	}
	if svc := registry.ServiceRegistry().Get(name); svc == nil {
		t.Fatal("service not registered after resume")
	}
	cfg, ok := r19SafeGlobal(t)
	if !ok {
		return
	}
	if len(cfg.Services) != 1 || cfg.Services[0] == nil {
		t.Fatalf("unexpected config after resume: %#v", cfg.Services)
	}
	if _, exists := cfg.Services[0].Metadata["paused"]; exists {
		t.Fatalf("paused flag not removed after resume: %#v", cfg.Services[0].Metadata)
	}
}

func TestR2A01003ResumeUnknownStillNotFound(t *testing.T) {
	r19SeedSocketConfig(t, &config.Config{})

	err := resumeServices(resumeServicesRequest{Services: []string{"r2a01003-ghost"}})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("resume unknown service err = %v, want not found", err)
	}
}

func TestR2A01003ResumeUnpausedUnregisteredStillNotFound(t *testing.T) {
	name := "r2a01003-unpaused-svc"
	r19SeedSocketConfig(t, &config.Config{})
	registry.ServiceRegistry().Unregister(name)
	if !r19SafeSet(t, &config.Config{Services: []*config.ServiceConfig{
		{Name: name, Addr: "127.0.0.1:0"},
	}}) {
		t.Fatalf("cannot seed config")
	}

	err := resumeServices(resumeServicesRequest{Services: []string{name}})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("resume unpaused-unregistered service err = %v, want not found (口径不许放宽)", err)
	}
	if svc := registry.ServiceRegistry().Get(name); svc != nil {
		t.Fatal("unpaused-unregistered service must not be silently started by resume")
	}
}
