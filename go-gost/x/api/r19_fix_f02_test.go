// R19-F02: 管理 API update*/delete*/pause/resume/status nil 全族回归。
//
// 背景：R19-X-02——x/api 全部 17 类 update/delete 与服务 pause/resume/
// status/rollback 入口在配置切片迭代中直接取 `.Name`，nil 项即 panic；
// panic 发生在 config.OnUpdate 闭包内时，非 defer 解锁使 globalMux 写锁
// 永不释放（L2 实测 config.Set 死锁）。修复后：各入口跳过 nil 项并记
// 日志（nil 项原样保留，清理只走启动自愈），不得 panic。
//
// 本文件用带 gin.Recovery() 的测试路由驱动 HTTP 入口（panic 变 500，
// 测试二进制不崩）；config.Set/Global 一律走带超时安全包装，红态下
// 锁若已 wedged 则快速失败而不挂死。15 类表复用 r18F04Cases。
package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-gost/core/logger"
	"github.com/go-gost/core/service"
	"github.com/go-gost/x/config"
	"github.com/go-gost/x/config/parsing/parser"
	_ "github.com/go-gost/x/handler/auto"
	_ "github.com/go-gost/x/listener/tcp"
	xlogger "github.com/go-gost/x/logger"
	"github.com/go-gost/x/registry"
)

func r19ApiRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	logger.SetDefault(xlogger.Nop())
	r := gin.New()
	r.Use(gin.Recovery())
	r.POST("/config/services", createService)
	r.PUT("/config/services/:service", updateService)
	r.DELETE("/config/services/:service", deleteService)
	r.POST("/config/services/:service/pause", pauseService)
	r.POST("/config/services/:service/resume", resumeService)
	r.GET("/config/services/:service/status", getServiceStatus)
	r.POST("/config/services/batch", createServices)
	r.PUT("/config/services/batch", updateServices)
	r.DELETE("/config/services/batch", deleteServices)
	r.POST("/config/services/batch/pause", pauseServices)
	r.POST("/config/services/batch/resume", resumeServices)
	r.POST("/config/reload", reloadConfig)
	r.POST("/config/chains", createChain)
	r.PUT("/config/chains/:chain", updateChain)
	r.DELETE("/config/chains/:chain", deleteChain)
	r.POST("/config/hops", createHop)
	r.PUT("/config/hops/:hop", updateHop)
	r.DELETE("/config/hops/:hop", deleteHop)
	r.POST("/config/authers", createAuther)
	r.PUT("/config/authers/:auther", updateAuther)
	r.DELETE("/config/authers/:auther", deleteAuther)
	r.POST("/config/admissions", createAdmission)
	r.PUT("/config/admissions/:admission", updateAdmission)
	r.DELETE("/config/admissions/:admission", deleteAdmission)
	r.POST("/config/bypasses", createBypass)
	r.PUT("/config/bypasses/:bypass", updateBypass)
	r.DELETE("/config/bypasses/:bypass", deleteBypass)
	r.POST("/config/resolvers", createResolver)
	r.PUT("/config/resolvers/:resolver", updateResolver)
	r.DELETE("/config/resolvers/:resolver", deleteResolver)
	r.POST("/config/hosts", createHosts)
	r.PUT("/config/hosts/:hosts", updateHosts)
	r.DELETE("/config/hosts/:hosts", deleteHosts)
	r.POST("/config/ingresses", createIngress)
	r.PUT("/config/ingresses/:ingress", updateIngress)
	r.DELETE("/config/ingresses/:ingress", deleteIngress)
	r.POST("/config/routers", createRouter)
	r.PUT("/config/routers/:router", updateRouter)
	r.DELETE("/config/routers/:router", deleteRouter)
	r.POST("/config/observers", createObserver)
	r.PUT("/config/observers/:observer", updateObserver)
	r.DELETE("/config/observers/:observer", deleteObserver)
	r.POST("/config/recorders", createRecorder)
	r.PUT("/config/recorders/:recorder", updateRecorder)
	r.DELETE("/config/recorders/:recorder", deleteRecorder)
	r.POST("/config/sds", createSD)
	r.PUT("/config/sds/:sd", updateSD)
	r.DELETE("/config/sds/:sd", deleteSD)
	r.POST("/config/limiters", createLimiter)
	r.PUT("/config/limiters/:limiter", updateLimiter)
	r.DELETE("/config/limiters/:limiter", deleteLimiter)
	r.POST("/config/climiters", createConnLimiter)
	r.PUT("/config/climiters/:limiter", updateConnLimiter)
	r.DELETE("/config/climiters/:limiter", deleteConnLimiter)
	r.POST("/config/rlimiters", createRateLimiter)
	r.PUT("/config/rlimiters/:limiter", updateRateLimiter)
	r.DELETE("/config/rlimiters/:limiter", deleteRateLimiter)
	return r
}

// r19ApiSafeSet / r19ApiSafeGlobal：带 wedged 保护的 config.Set/Global。
func r19ApiSafeSet(t *testing.T, cfg *config.Config) bool {
	t.Helper()
	done := make(chan struct{})
	go func() {
		config.Set(cfg)
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(5 * time.Second):
		t.Errorf("config.Set timed out after 5s (globalMux wedged?)")
		return false
	}
}

func r19ApiSafeGlobal(t *testing.T) (*config.Config, bool) {
	t.Helper()
	ch := make(chan *config.Config, 1)
	go func() { ch <- config.Global() }()
	select {
	case cfg := <-ch:
		return cfg, true
	case <-time.After(5 * time.Second):
		t.Errorf("config.Global timed out after 5s (globalMux wedged?)")
		return nil, false
	}
}

// r19SeedApiConfig 替换全局配置（落盘指向临时文件），清理时 wedge-safe 恢复。
func r19SeedApiConfig(t *testing.T, cfg *config.Config) string {
	t.Helper()
	logger.SetDefault(xlogger.Nop())
	originalConfig, ok := r19ApiSafeGlobal(t)
	if !ok {
		t.Fatalf("cannot read original config (globalMux wedged before test)")
	}
	originalPersistPath := config.PersistPath()
	persistFile := filepath.Join(t.TempDir(), "gost.json")
	if !r19ApiSafeSet(t, cfg) {
		t.Fatalf("cannot seed config (globalMux wedged before test)")
	}
	config.SetPersistPath(persistFile)
	config.EnablePersist()
	t.Cleanup(func() {
		r19ApiSafeSet(t, originalConfig)
		config.SetPersistPath(originalPersistPath)
	})
	return persistFile
}

func r19ApiDo(t *testing.T, r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// r19PrependNil 在配置中唯一非空的命名切片前插一个 nil 项（模拟落盘
// JSON null 读入后的内存形态）。
func r19PrependNil(cfg *config.Config) {
	switch {
	case len(cfg.Services) > 0:
		cfg.Services = append([]*config.ServiceConfig{nil}, cfg.Services...)
	case len(cfg.Chains) > 0:
		cfg.Chains = append([]*config.ChainConfig{nil}, cfg.Chains...)
	case len(cfg.Hops) > 0:
		cfg.Hops = append([]*config.HopConfig{nil}, cfg.Hops...)
	case len(cfg.Authers) > 0:
		cfg.Authers = append([]*config.AutherConfig{nil}, cfg.Authers...)
	case len(cfg.Admissions) > 0:
		cfg.Admissions = append([]*config.AdmissionConfig{nil}, cfg.Admissions...)
	case len(cfg.Bypasses) > 0:
		cfg.Bypasses = append([]*config.BypassConfig{nil}, cfg.Bypasses...)
	case len(cfg.Resolvers) > 0:
		cfg.Resolvers = append([]*config.ResolverConfig{nil}, cfg.Resolvers...)
	case len(cfg.Hosts) > 0:
		cfg.Hosts = append([]*config.HostsConfig{nil}, cfg.Hosts...)
	case len(cfg.Ingresses) > 0:
		cfg.Ingresses = append([]*config.IngressConfig{nil}, cfg.Ingresses...)
	case len(cfg.Routers) > 0:
		cfg.Routers = append([]*config.RouterConfig{nil}, cfg.Routers...)
	case len(cfg.Observers) > 0:
		cfg.Observers = append([]*config.ObserverConfig{nil}, cfg.Observers...)
	case len(cfg.Recorders) > 0:
		cfg.Recorders = append([]*config.RecorderConfig{nil}, cfg.Recorders...)
	case len(cfg.SDs) > 0:
		cfg.SDs = append([]*config.SDConfig{nil}, cfg.SDs...)
	case len(cfg.Limiters) > 0:
		cfg.Limiters = append([]*config.LimiterConfig{nil}, cfg.Limiters...)
	case len(cfg.CLimiters) > 0:
		cfg.CLimiters = append([]*config.LimiterConfig{nil}, cfg.CLimiters...)
	case len(cfg.RLimiters) > 0:
		cfg.RLimiters = append([]*config.LimiterConfig{nil}, cfg.RLimiters...)
	case len(cfg.Loggers) > 0:
		cfg.Loggers = append([]*config.LoggerConfig{nil}, cfg.Loggers...)
	}
}

// 15 类 update：配置 [nil, {name}] 时 PUT 更新不得 panic（红态：gin
// Recovery 返 500；绿态：200，且 nil 项保留、目标项仍在）。
func TestR19F02ApiUpdateNil(t *testing.T) {
	r := r19ApiRouter()
	for _, tc := range r18F04Cases {
		t.Run(tc.kind, func(t *testing.T) {
			name := "r19f02_upd_" + tc.kind
			cfg := &config.Config{}
			tc.seed(cfg, name)
			r19SeedApiConfig(t, cfg)
			t.Cleanup(func() { tc.unregister(name) })
			tc.unregister(name)

			if w := r19ApiDo(t, r, http.MethodPost, tc.path, `{"name":"`+name+`"}`); w.Code != http.StatusOK {
				t.Fatalf("create %s status = %d, body = %s", tc.kind, w.Code, w.Body.String())
			}
			seeded, ok := r19ApiSafeGlobal(t)
			if !ok {
				return
			}
			r19PrependNil(seeded)
			if !r19ApiSafeSet(t, seeded) {
				t.Fatalf("cannot seed nil %s config", tc.kind)
			}

			w := r19ApiDo(t, r, http.MethodPut, tc.path+"/"+name, `{"name":"`+name+`"}`)
			if w.Code != http.StatusOK {
				t.Fatalf("update %s with nil entry: status = %d (want 200), body = %s", tc.kind, w.Code, w.Body.String())
			}
			after, ok := r19ApiSafeGlobal(t)
			if !ok {
				return
			}
			if n := tc.count(after, name); n != 1 {
				t.Fatalf("expected exactly 1 in-memory entry for %s %q, got %d", tc.kind, name, n)
			}
		})
	}
}

// 15 类 delete：配置 [nil, {name}] 时 DELETE 不得 panic；目标删除、nil 保留。
func TestR19F02ApiDeleteNil(t *testing.T) {
	r := r19ApiRouter()
	for _, tc := range r18F04Cases {
		t.Run(tc.kind, func(t *testing.T) {
			name := "r19f02_del_" + tc.kind
			cfg := &config.Config{}
			tc.seed(cfg, name)
			r19SeedApiConfig(t, cfg)
			t.Cleanup(func() { tc.unregister(name) })
			tc.unregister(name)

			if w := r19ApiDo(t, r, http.MethodPost, tc.path, `{"name":"`+name+`"}`); w.Code != http.StatusOK {
				t.Fatalf("create %s status = %d, body = %s", tc.kind, w.Code, w.Body.String())
			}
			seeded, ok := r19ApiSafeGlobal(t)
			if !ok {
				return
			}
			r19PrependNil(seeded)
			if !r19ApiSafeSet(t, seeded) {
				t.Fatalf("cannot seed nil %s config", tc.kind)
			}

			w := r19ApiDo(t, r, http.MethodDelete, tc.path+"/"+name, ``)
			if w.Code != http.StatusOK {
				t.Fatalf("delete %s with nil entry: status = %d (want 200), body = %s", tc.kind, w.Code, w.Body.String())
			}
			after, ok := r19ApiSafeGlobal(t)
			if !ok {
				return
			}
			if n := tc.count(after, name); n != 0 {
				t.Fatalf("expected 0 in-memory entries for %s %q after delete, got %d", tc.kind, name, n)
			}
		})
	}
}

// r19ApiCreateService creates a throwaway service via the HTTP entry.
func r19ApiCreateService(t *testing.T, r *gin.Engine, name string) {
	t.Helper()
	t.Cleanup(func() {
		if svc := registry.ServiceRegistry().Get(name); svc != nil {
			svc.Close()
		}
		registry.ServiceRegistry().Unregister(name)
	})
	registry.ServiceRegistry().Unregister(name)
	if w := r19ApiDo(t, r, http.MethodPost, "/config/services", `{"name":"`+name+`","addr":"127.0.0.1:0"}`); w.Code != http.StatusOK {
		t.Fatalf("create service %q status = %d, body = %s", name, w.Code, w.Body.String())
	}
}

// r19ApiInjectNilService prepends a nil service entry to the live config.
func r19ApiInjectNilService(t *testing.T) {
	t.Helper()
	cfg, ok := r19ApiSafeGlobal(t)
	if !ok {
		t.Fatalf("cannot read config to inject nil service")
	}
	cfg.Services = append([]*config.ServiceConfig{nil}, cfg.Services...)
	if !r19ApiSafeSet(t, cfg) {
		t.Fatalf("cannot seed nil service config")
	}
}

func r19ApiAssertNilFirstService(t *testing.T, name string) {
	t.Helper()
	cfg, ok := r19ApiSafeGlobal(t)
	if !ok {
		return
	}
	if len(cfg.Services) != 2 || cfg.Services[0] != nil || cfg.Services[1] == nil || cfg.Services[1].Name != name {
		t.Fatalf("expected [nil service %q] in config, got %#v", name, cfg.Services)
	}
}

// updateService（config_service.go:259）。
func TestR19F02ApiUpdateServiceNil(t *testing.T) {
	name := "r19f02_upd_svc"
	r := r19ApiRouter()
	r19SeedApiConfig(t, &config.Config{})
	r19ApiCreateService(t, r, name)
	r19ApiInjectNilService(t)

	w := r19ApiDo(t, r, http.MethodPut, "/config/services/"+name, `{"name":"`+name+`","addr":"127.0.0.1:0"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("updateService with nil entry: status = %d (want 200), body = %s", w.Code, w.Body.String())
	}
	r19ApiAssertNilFirstService(t, name)
}

// updateServices batch（config_service.go:343）。
func TestR19F02ApiUpdateServicesNil(t *testing.T) {
	name := "r19f02_upd_svcs"
	r := r19ApiRouter()
	r19SeedApiConfig(t, &config.Config{})
	r19ApiCreateService(t, r, name)
	r19ApiInjectNilService(t)

	w := r19ApiDo(t, r, http.MethodPut, "/config/services/batch", `[{"name":"`+name+`","addr":"127.0.0.1:0"}]`)
	if w.Code != http.StatusOK {
		t.Fatalf("updateServices with nil entry: status = %d (want 200), body = %s", w.Code, w.Body.String())
	}
	r19ApiAssertNilFirstService(t, name)
}

// deleteService（config_service.go:411）。
func TestR19F02ApiDeleteServiceNil(t *testing.T) {
	name := "r19f02_del_svc"
	r := r19ApiRouter()
	r19SeedApiConfig(t, &config.Config{})
	r19ApiCreateService(t, r, name)
	r19ApiInjectNilService(t)

	w := r19ApiDo(t, r, http.MethodDelete, "/config/services/"+name, ``)
	if w.Code != http.StatusOK {
		t.Fatalf("deleteService with nil entry: status = %d (want 200), body = %s", w.Code, w.Body.String())
	}
	cfg, ok := r19ApiSafeGlobal(t)
	if !ok {
		return
	}
	if len(cfg.Services) != 1 || cfg.Services[0] != nil {
		t.Fatalf("expected [nil] after deleteService, got %#v", cfg.Services)
	}
}

// deleteServices batch（config_service.go:482）。
func TestR19F02ApiDeleteServicesNil(t *testing.T) {
	name := "r19f02_del_svcs"
	r := r19ApiRouter()
	r19SeedApiConfig(t, &config.Config{})
	r19ApiCreateService(t, r, name)
	r19ApiInjectNilService(t)

	w := r19ApiDo(t, r, http.MethodDelete, "/config/services/batch", `{"services":["`+name+`"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("deleteServices with nil entry: status = %d (want 200), body = %s", w.Code, w.Body.String())
	}
	cfg, ok := r19ApiSafeGlobal(t)
	if !ok {
		return
	}
	if len(cfg.Services) != 1 || cfg.Services[0] != nil {
		t.Fatalf("expected [nil] after deleteServices, got %#v", cfg.Services)
	}
}

// pauseService（config_service.go:557/:571）。
func TestR19F02ApiPauseServiceNil(t *testing.T) {
	name := "r19f02_pause_svc"
	r := r19ApiRouter()
	r19SeedApiConfig(t, &config.Config{})
	r19ApiCreateService(t, r, name)
	r19ApiInjectNilService(t)

	w := r19ApiDo(t, r, http.MethodPost, "/config/services/"+name+"/pause", ``)
	if w.Code != http.StatusOK {
		t.Fatalf("pauseService with nil entry: status = %d (want 200), body = %s", w.Code, w.Body.String())
	}
	cfg, ok := r19ApiSafeGlobal(t)
	if !ok {
		return
	}
	if paused, _ := cfg.Services[1].Metadata["paused"].(bool); !paused {
		t.Fatalf("expected service %q marked paused, got %#v", name, cfg.Services[1])
	}
}

// resumeService（config_service.go:614/:666）：先暂停、再插 nil、再恢复。
func TestR19F02ApiResumeServiceNil(t *testing.T) {
	name := "r19f02_resume_svc"
	r := r19ApiRouter()
	r19SeedApiConfig(t, &config.Config{})
	r19ApiCreateService(t, r, name)
	if w := r19ApiDo(t, r, http.MethodPost, "/config/services/"+name+"/pause", ``); w.Code != http.StatusOK {
		t.Fatalf("pauseService setup status = %d, body = %s", w.Code, w.Body.String())
	}
	r19ApiInjectNilService(t)

	w := r19ApiDo(t, r, http.MethodPost, "/config/services/"+name+"/resume", ``)
	if w.Code != http.StatusOK {
		t.Fatalf("resumeService with nil entry: status = %d (want 200), body = %s", w.Code, w.Body.String())
	}
	cfg, ok := r19ApiSafeGlobal(t)
	if !ok {
		return
	}
	if _, exists := cfg.Services[1].Metadata["paused"]; exists {
		t.Fatalf("expected paused flag removed after resume, got %#v", cfg.Services[1].Metadata)
	}
}

// getServiceStatus（config_service.go:731）。
func TestR19F02ApiGetServiceStatusNil(t *testing.T) {
	name := "r19f02_status_svc"
	r := r19ApiRouter()
	r19SeedApiConfig(t, &config.Config{})
	r19ApiCreateService(t, r, name)
	r19ApiInjectNilService(t)

	w := r19ApiDo(t, r, http.MethodGet, "/config/services/"+name+"/status", ``)
	if w.Code != http.StatusOK {
		t.Fatalf("getServiceStatus with nil entry: status = %d (want 200), body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data ServiceStatusInfo `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal status response: %v", err)
	}
	if resp.Data.Name != name || resp.Data.Status != "running" {
		t.Fatalf("unexpected status response: %#v", resp.Data)
	}
}

// pauseServices batch（config_service.go:886/:916/:946）。
func TestR19F02ApiPauseServicesNil(t *testing.T) {
	name := "r19f02_pause_svcs"
	r := r19ApiRouter()
	r19SeedApiConfig(t, &config.Config{})
	r19ApiCreateService(t, r, name)
	r19ApiInjectNilService(t)

	w := r19ApiDo(t, r, http.MethodPost, "/config/services/batch/pause", `{"services":["`+name+`"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("pauseServices with nil entry: status = %d (want 200), body = %s", w.Code, w.Body.String())
	}
	cfg, ok := r19ApiSafeGlobal(t)
	if !ok {
		return
	}
	if paused, _ := cfg.Services[1].Metadata["paused"].(bool); !paused {
		t.Fatalf("expected service %q marked paused, got %#v", name, cfg.Services[1])
	}
}

// resumeServices batch（config_service.go:1015/:1095）。
func TestR19F02ApiResumeServicesNil(t *testing.T) {
	name := "r19f02_resume_svcs"
	r := r19ApiRouter()
	r19SeedApiConfig(t, &config.Config{})
	r19ApiCreateService(t, r, name)
	if w := r19ApiDo(t, r, http.MethodPost, "/config/services/batch/pause", `{"services":["`+name+`"]}`); w.Code != http.StatusOK {
		t.Fatalf("pauseServices setup status = %d, body = %s", w.Code, w.Body.String())
	}
	r19ApiInjectNilService(t)

	w := r19ApiDo(t, r, http.MethodPost, "/config/services/batch/resume", `{"services":["`+name+`"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("resumeServices with nil entry: status = %d (want 200), body = %s", w.Code, w.Body.String())
	}
	cfg, ok := r19ApiSafeGlobal(t)
	if !ok {
		return
	}
	if _, exists := cfg.Services[1].Metadata["paused"]; exists {
		t.Fatalf("expected paused flag removed after resume, got %#v", cfg.Services[1].Metadata)
	}
}

// rollbackPausedServices（config_service.go:776，OnUpdate 闭包内）。
func TestR19F02ApiRollbackPausedServicesNil(t *testing.T) {
	name := "r19f02_rb_paused"
	r19SeedApiConfig(t, &config.Config{Services: []*config.ServiceConfig{
		nil,
		{Name: name, Addr: "127.0.0.1:0"},
	}})
	t.Cleanup(func() {
		if svc := registry.ServiceRegistry().Get(name); svc != nil {
			svc.Close()
		}
		registry.ServiceRegistry().Unregister(name)
	})
	registry.ServiceRegistry().Unregister(name)

	svcCfg := config.ServiceConfig{Name: name, Addr: "127.0.0.1:0"}
	panicked, v := r19ApiRunAndCatch(func() {
		rollbackPausedServices([]struct {
			name          string
			service       service.Service
			serviceConfig *config.ServiceConfig
		}{{name: name, service: nil, serviceConfig: &svcCfg}})
	})
	if panicked {
		t.Fatalf("rollbackPausedServices PANICKED on nil service entry: %v", v)
	}
	if registry.ServiceRegistry().Get(name) == nil {
		t.Fatalf("expected service %q re-registered by rollback", name)
	}
}

// rollbackResumedServices（config_service.go:806，OnUpdate 闭包内）。
func TestR19F02ApiRollbackResumedServicesNil(t *testing.T) {
	name := "r19f02_rb_resumed"
	r := r19ApiRouter()
	r19SeedApiConfig(t, &config.Config{})
	r19ApiCreateService(t, r, name)
	r19ApiInjectNilService(t)

	svcCfg := config.ServiceConfig{Name: name, Addr: "127.0.0.1:0"}
	panicked, v := r19ApiRunAndCatch(func() {
		rollbackResumedServices([]struct {
			name          string
			service       service.Service
			serviceConfig *config.ServiceConfig
		}{{name: name, service: nil, serviceConfig: &svcCfg}})
	})
	if panicked {
		t.Fatalf("rollbackResumedServices PANICKED on nil service entry: %v", v)
	}
	cfg, ok := r19ApiSafeGlobal(t)
	if !ok {
		return
	}
	if paused, _ := cfg.Services[1].Metadata["paused"].(bool); !paused {
		t.Fatalf("expected service %q re-marked paused, got %#v", name, cfg.Services[1])
	}
}

func r19ApiRunAndCatch(f func()) (panicked bool, value any) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
			value = r
		}
	}()
	f()
	return false, nil
}

// POST /config/reload：落盘含 JSON null 的文件经 API 重载不得 panic，
// 且重载后 nil 项已被清除、文件不再含 null。
func TestR19F02ApiReloadStripsNil(t *testing.T) {
	// 日志器类型纪律：core logger 的 Default 经 atomic.Value 存放，
	// 同一进程内只能存一种具体类型。R18 测试与本文件路由均存 Nop，
	// 故此处亦存 Nop；毒化文件配 "log": {"output": "none"} 使
	// loader.Load 内 ParseLogger 同样返回 Nop（否则 Load 存
	// NewLogger 会因类型不一致 panic）。生产侧 main() 先存
	// NewLogger、Load 亦存 NewLogger，类型一致、不受影响。
	logger.SetDefault(xlogger.Nop())
	dir := t.TempDir()
	poisoned := filepath.Join(dir, "gost.json")
	content := `{
  "log": {"output": "none"},
  "services": [null, {"name": "r19f02-reload-svc", "addr": "127.0.0.1:0"}],
  "chains": [null, {"name": "r19f02-reload-chain"}],
  "limiters": [null],
  "authers": [null],
  "hops": [null]
}`
	if err := os.WriteFile(poisoned, []byte(content), 0600); err != nil {
		t.Fatalf("write poisoned config: %v", err)
	}
	parser.Init(parser.Args{CfgFile: poisoned})
	t.Cleanup(func() {
		for _, name := range []string{"r19f02-reload-svc"} {
			if svc := registry.ServiceRegistry().Get(name); svc != nil {
				svc.Close()
			}
			registry.ServiceRegistry().Unregister(name)
		}
		registry.ChainRegistry().Unregister("r19f02-reload-chain")
	})

	req := httptest.NewRequest(http.MethodPost, "/config/reload", nil)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Request = req
	panicked, v := r19ApiRunAndCatch(func() { reloadConfig(ctx) })
	if panicked {
		t.Fatalf("reloadConfig PANICKED on poisoned file: %v", v)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("reload status = %d (want 200), body = %s", w.Code, w.Body.String())
	}
	data, err := os.ReadFile(poisoned)
	if err != nil {
		t.Fatalf("read healed config: %v", err)
	}
	var onDisk config.Config
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatalf("unmarshal healed config: %v", err)
	}
	for _, svc := range onDisk.Services {
		if svc == nil {
			t.Fatalf("healed file still contains nil service: %s", string(data))
		}
	}
	for _, ch := range onDisk.Chains {
		if ch == nil {
			t.Fatalf("healed file still contains nil chain: %s", string(data))
		}
	}
	for _, l := range onDisk.Limiters {
		if l == nil {
			t.Fatalf("healed file still contains nil limiter: %s", string(data))
		}
	}
	for _, a := range onDisk.Authers {
		if a == nil {
			t.Fatalf("healed file still contains nil auther: %s", string(data))
		}
	}
	for _, h := range onDisk.Hops {
		if h == nil {
			t.Fatalf("healed file still contains nil hop: %s", string(data))
		}
	}
}
