package api

import (
	"net/http"
	"testing"

	"github.com/go-gost/x/config"
	"github.com/go-gost/x/registry"
)

// R2-A01-003 回归（本地管理 API 面）：已暂停服务在 agent 重启后必须能
// resume。socket 面的同名缺陷在 API 面有两个入口——单个 resumeService
// 与批量 resumeServices——旧实现都以注册表存在为前置，loader 跳过暂停
// 服务不注册，重启后一律 400 "service not found"。本测试直接种子重启
// 后状态（配置在册 + paused、注册表无此服务），双入口均须 200 并按
// 配置重建；全未知服务仍须 400。

func seedRestartPausedApiService(t *testing.T, name string) {
	t.Helper()
	r19SeedApiConfig(t, &config.Config{Services: []*config.ServiceConfig{
		{Name: name, Addr: "127.0.0.1:0", Metadata: map[string]any{"paused": true}},
	}})
	registry.ServiceRegistry().Unregister(name)
	t.Cleanup(func() {
		if svc := registry.ServiceRegistry().Get(name); svc != nil {
			svc.Close()
		}
		registry.ServiceRegistry().Unregister(name)
	})
}

func assertResumedAndUnpaused(t *testing.T, name string) {
	t.Helper()
	if svc := registry.ServiceRegistry().Get(name); svc == nil {
		t.Fatal("service not registered after resume")
	}
	cfg, ok := r19ApiSafeGlobal(t)
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

func TestR2A01003ApiResumeServiceAfterRestart(t *testing.T) {
	name := "r2a01003_api_resume"
	r := r19ApiRouter()
	seedRestartPausedApiService(t, name)

	w := r19ApiDo(t, r, http.MethodPost, "/config/services/"+name+"/resume", ``)
	if w.Code != http.StatusOK {
		t.Fatalf("resumeService after restart: status = %d (want 200), body = %s", w.Code, w.Body.String())
	}
	assertResumedAndUnpaused(t, name)
}

func TestR2A01003ApiResumeServicesBatchAfterRestart(t *testing.T) {
	name := "r2a01003_api_resume_batch"
	r := r19ApiRouter()
	seedRestartPausedApiService(t, name)

	w := r19ApiDo(t, r, http.MethodPost, "/config/services/batch/resume", `{"services":["`+name+`"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("resumeServices batch after restart: status = %d (want 200), body = %s", w.Code, w.Body.String())
	}
	assertResumedAndUnpaused(t, name)
}

func TestR2A01003ApiResumeUnknownStillNotFound(t *testing.T) {
	r := r19ApiRouter()
	r19SeedApiConfig(t, &config.Config{})

	w := r19ApiDo(t, r, http.MethodPost, "/config/services/r2a01003_ghost/resume", ``)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("resume unknown service: status = %d (want 400), body = %s", w.Code, w.Body.String())
	}
	w = r19ApiDo(t, r, http.MethodPost, "/config/services/batch/resume", `{"services":["r2a01003_ghost"]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("batch resume unknown service: status = %d (want 400), body = %s", w.Code, w.Body.String())
	}
}
