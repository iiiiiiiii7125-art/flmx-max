// R18-F02/F04: 管理 API create 盲 append 按名去重回归。
//
// 背景：createService / createServices（F-02）与 x/api 其余 15 类 create（F-04）
// 在 registry IsRegistered 守卫之后、OnUpdate 内对配置做无去重 append。
// 服务已在配置中、但不在注册表里时（例如暂停后 loader 跳过注册、或注册表与
// 配置漂移），一次重复 create 就会把第二份同名项写进配置并落盘、毒化
// gost.json。修复后：append 前按名查配置成员，已存在则替换（首份）、
// 多余同名副本一并清除，只留一份（与 R17-A socket 侧同款）。
//
// 本文件只用管理 API 的 HTTP 入口驱动（gin test engine 直挂 handler，
// 不走 -api/GOST_API/配置文件 api: 段启用门——入口仍不可达，见报告 §7）。
package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	corelogger "github.com/go-gost/core/logger"
	"github.com/go-gost/x/config"
	_ "github.com/go-gost/x/handler/auto"
	_ "github.com/go-gost/x/listener/tcp"
	xlogger "github.com/go-gost/x/logger"
	"github.com/go-gost/x/registry"
)

func r18TestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	corelogger.SetDefault(xlogger.Nop())
	r := gin.New()
	// 与 api.go Register 内 POST 路由同路径，直挂 handler（不走鉴权中间件）。
	r.POST("/config/services", createService)
	r.POST("/config/services/batch", createServices)
	r.POST("/config/chains", createChain)
	r.POST("/config/hops", createHop)
	r.POST("/config/authers", createAuther)
	r.POST("/config/admissions", createAdmission)
	r.POST("/config/bypasses", createBypass)
	r.POST("/config/resolvers", createResolver)
	r.POST("/config/hosts", createHosts)
	r.POST("/config/ingresses", createIngress)
	r.POST("/config/routers", createRouter)
	r.POST("/config/observers", createObserver)
	r.POST("/config/recorders", createRecorder)
	r.POST("/config/sds", createSD)
	r.POST("/config/limiters", createLimiter)
	r.POST("/config/climiters", createConnLimiter)
	r.POST("/config/rlimiters", createRateLimiter)
	return r
}

// r18SeedConfig 用给定配置替换全局配置，并把落盘指向 t.TempDir() 内文件
// （启用 persist，使内存与落盘两面都可断言）；t.Cleanup 恢复原配置与原路径。
func r18SeedConfig(t *testing.T, cfg *config.Config) string {
	t.Helper()
	originalConfig := config.Global()
	originalPersistPath := config.PersistPath()
	persistFile := filepath.Join(t.TempDir(), "gost.json")
	config.Set(cfg)
	config.SetPersistPath(persistFile)
	config.EnablePersist()
	t.Cleanup(func() {
		config.Set(originalConfig)
		config.SetPersistPath(originalPersistPath)
	})
	return persistFile
}

func r18Post(t *testing.T, r *gin.Engine, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func r18CountNamed[T any](items []*T, name string, nameOf func(*T) string) int {
	n := 0
	for _, it := range items {
		if it != nil && nameOf(it) == name {
			n++
		}
	}
	return n
}

func r18AssertSingleOnDisk(t *testing.T, persistFile, kind, name string, countOnDisk func(*config.Config) int) {
	t.Helper()
	data, err := os.ReadFile(persistFile)
	if err != nil {
		t.Fatalf("read persisted config: %v", err)
	}
	var onDisk config.Config
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatalf("unmarshal persisted config: %v", err)
	}
	if n := countOnDisk(&onDisk); n != 1 {
		t.Fatalf("persisted config has %d entries for %s %q, want 1: %s", n, kind, name, string(data))
	}
}

// F-02 形 1：paused 重建——服务已在配置中、但不在注册表里时，
// createService 不得再 append 第二份。
func TestR18F02CreateServiceDedupesPausedRebuild(t *testing.T) {
	name := "r18f02_paused"
	r := r18TestRouter()
	stale := config.ServiceConfig{Name: name, Addr: "127.0.0.1:0"}
	persistFile := r18SeedConfig(t, &config.Config{Services: []*config.ServiceConfig{&stale}})
	t.Cleanup(func() { registry.ServiceRegistry().Unregister(name) })
	registry.ServiceRegistry().Unregister(name)

	// 注意：handler 用 ctx.ShouldBindJSON(&req.Data) 把请求体直接绑进
	// 配置结构（无 "data" 包裹）；batch 则直接绑进 []ServiceConfig。
	w := r18Post(t, r, "/config/services", `{"name":"`+name+`","addr":"127.0.0.1:0"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("createService status = %d, body = %s", w.Code, w.Body.String())
	}

	if n := r18CountNamed(config.Global().Services, name, func(s *config.ServiceConfig) string { return s.Name }); n != 1 {
		t.Fatalf("expected exactly 1 in-memory entry for service %q, got %d", name, n)
	}
	r18AssertSingleOnDisk(t, persistFile, "service", name, func(c *config.Config) int {
		return r18CountNamed(c.Services, name, func(s *config.ServiceConfig) string { return s.Name })
	})
}

// F-02 形 2：批量——batch 内某项与配置已有成员同名（paused 漂移），
// 其余项正常新建；落盘后每名各一份。
func TestR18F02CreateServicesBatchDedupesExistingConfigEntry(t *testing.T) {
	dupName := "r18f02_batch_dup"
	freshName := "r18f02_batch_new"
	r := r18TestRouter()
	stale := config.ServiceConfig{Name: dupName, Addr: "127.0.0.1:0"}
	persistFile := r18SeedConfig(t, &config.Config{Services: []*config.ServiceConfig{&stale}})
	t.Cleanup(func() {
		registry.ServiceRegistry().Unregister(dupName)
		registry.ServiceRegistry().Unregister(freshName)
	})
	registry.ServiceRegistry().Unregister(dupName)
	registry.ServiceRegistry().Unregister(freshName)

	body := `[{"name":"` + dupName + `","addr":"127.0.0.1:0"},{"name":"` + freshName + `","addr":"127.0.0.1:0"}]`
	w := r18Post(t, r, "/config/services/batch", body)
	if w.Code != http.StatusOK {
		t.Fatalf("createServices status = %d, body = %s", w.Code, w.Body.String())
	}

	cfg := config.Global()
	if n := r18CountNamed(cfg.Services, dupName, func(s *config.ServiceConfig) string { return s.Name }); n != 1 {
		t.Fatalf("expected exactly 1 in-memory entry for service %q, got %d", dupName, n)
	}
	if n := r18CountNamed(cfg.Services, freshName, func(s *config.ServiceConfig) string { return s.Name }); n != 1 {
		t.Fatalf("expected exactly 1 in-memory entry for service %q, got %d", freshName, n)
	}
	r18AssertSingleOnDisk(t, persistFile, "service", dupName, func(c *config.Config) int {
		return r18CountNamed(c.Services, dupName, func(s *config.ServiceConfig) string { return s.Name })
	})
	r18AssertSingleOnDisk(t, persistFile, "service", freshName, func(c *config.Config) int {
		return r18CountNamed(c.Services, freshName, func(s *config.ServiceConfig) string { return s.Name })
	})
}

// F-02 形 3：毒化文件不再增份——配置里已有同名两份时再 create 一次，
// 内存与落盘都收敛回一份（不再变成三份）。
func TestR18F02CreateServiceDedupesAlreadyPoisonedConfig(t *testing.T) {
	name := "r18f02_poisoned"
	r := r18TestRouter()
	first := config.ServiceConfig{Name: name, Addr: "127.0.0.1:0"}
	second := config.ServiceConfig{Name: name, Addr: "127.0.0.1:0"}
	persistFile := r18SeedConfig(t, &config.Config{Services: []*config.ServiceConfig{&first, &second}})
	t.Cleanup(func() { registry.ServiceRegistry().Unregister(name) })
	registry.ServiceRegistry().Unregister(name)

	w := r18Post(t, r, "/config/services", `{"name":"`+name+`","addr":"127.0.0.1:0"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("createService status = %d, body = %s", w.Code, w.Body.String())
	}

	if n := r18CountNamed(config.Global().Services, name, func(s *config.ServiceConfig) string { return s.Name }); n != 1 {
		t.Fatalf("expected exactly 1 in-memory entry for service %q, got %d", name, n)
	}
	r18AssertSingleOnDisk(t, persistFile, "service", name, func(c *config.Config) int {
		return r18CountNamed(c.Services, name, func(s *config.ServiceConfig) string { return s.Name })
	})
}

// upsertNamed 本身的单元断言：同名反复 upsert（createServices 第 4 阶段
// 循环内逐项调用的语义）只留一份；首份被新值替换、多余副本清除；
// nil 项保留、nil 入项不改动。
func TestR18F02UpsertNamed(t *testing.T) {
	nameOf := func(s *config.ServiceConfig) string { return s.Name }

	// 批内重复：同一名字连续 upsert 两次，只留一份，且为第二次的值。
	items := []*config.ServiceConfig{{Name: "a", Addr: "127.0.0.1:1"}}
	items = upsertNamed(items, &config.ServiceConfig{Name: "a", Addr: "127.0.0.1:2"}, nameOf)
	items = upsertNamed(items, &config.ServiceConfig{Name: "a", Addr: "127.0.0.1:3"}, nameOf)
	if len(items) != 1 || items[0].Addr != "127.0.0.1:3" {
		t.Fatalf("repeated upsert: got %#v, want single entry with addr 127.0.0.1:3", items)
	}

	// 已毒化列表（同名两份）+ 一次 upsert：收敛回一份（新值），位置为首份位置。
	items = []*config.ServiceConfig{{Name: "a", Addr: "127.0.0.1:1"}, {Name: "b", Addr: "127.0.0.1:4"}, {Name: "a", Addr: "127.0.0.1:2"}}
	items = upsertNamed(items, &config.ServiceConfig{Name: "a", Addr: "127.0.0.1:9"}, nameOf)
	if len(items) != 2 || items[0].Name != "a" || items[0].Addr != "127.0.0.1:9" || items[1].Name != "b" {
		t.Fatalf("poisoned upsert: got %#v, want [a:9 b:4]", items)
	}

	// 新名追加；nil 项保留；名带空白按 TrimSpace 后比较。
	items = []*config.ServiceConfig{{Name: "a"}, nil}
	items = upsertNamed(items, &config.ServiceConfig{Name: "  a  "}, nameOf)
	if len(items) != 2 || items[1] != nil {
		t.Fatalf("whitespace/nil: got %#v, want [a nil]", items)
	}
	items = upsertNamed(items, &config.ServiceConfig{Name: "c"}, nameOf)
	if len(items) != 3 || items[2].Name != "c" {
		t.Fatalf("append new: got %#v, want [a nil c]", items)
	}
	if got := upsertNamed(items, nil, nameOf); len(got) != 3 {
		t.Fatalf("nil item: got %d entries, want 3", len(got))
	}
}

// F-04：其余 15 类 create 各一形——类成员已在配置中、但不在注册表里时，
// 重复 create 后配置中仅一份。
type r18F04Case struct {
	kind       string
	path       string
	seed       func(c *config.Config, name string)
	count      func(c *config.Config, name string) int
	unregister func(name string)
}

var r18F04Cases = []r18F04Case{
	{
		kind: "admission", path: "/config/admissions",
		seed: func(c *config.Config, n string) { c.Admissions = []*config.AdmissionConfig{{Name: n}} },
		count: func(c *config.Config, n string) int {
			return r18CountNamed(c.Admissions, n, func(a *config.AdmissionConfig) string { return a.Name })
		},
		unregister: func(n string) { registry.AdmissionRegistry().Unregister(n) },
	},
	{
		kind: "auther", path: "/config/authers",
		seed: func(c *config.Config, n string) { c.Authers = []*config.AutherConfig{{Name: n}} },
		count: func(c *config.Config, n string) int {
			return r18CountNamed(c.Authers, n, func(a *config.AutherConfig) string { return a.Name })
		},
		unregister: func(n string) { registry.AutherRegistry().Unregister(n) },
	},
	{
		kind: "bypass", path: "/config/bypasses",
		seed: func(c *config.Config, n string) { c.Bypasses = []*config.BypassConfig{{Name: n}} },
		count: func(c *config.Config, n string) int {
			return r18CountNamed(c.Bypasses, n, func(b *config.BypassConfig) string { return b.Name })
		},
		unregister: func(n string) { registry.BypassRegistry().Unregister(n) },
	},
	{
		kind: "chain", path: "/config/chains",
		seed: func(c *config.Config, n string) { c.Chains = []*config.ChainConfig{{Name: n}} },
		count: func(c *config.Config, n string) int {
			return r18CountNamed(c.Chains, n, func(ch *config.ChainConfig) string { return ch.Name })
		},
		unregister: func(n string) { registry.ChainRegistry().Unregister(n) },
	},
	{
		kind: "conn_limiter", path: "/config/climiters",
		seed: func(c *config.Config, n string) { c.CLimiters = []*config.LimiterConfig{{Name: n}} },
		count: func(c *config.Config, n string) int {
			return r18CountNamed(c.CLimiters, n, func(l *config.LimiterConfig) string { return l.Name })
		},
		unregister: func(n string) { registry.ConnLimiterRegistry().Unregister(n) },
	},
	{
		kind: "hop", path: "/config/hops",
		seed: func(c *config.Config, n string) { c.Hops = []*config.HopConfig{{Name: n}} },
		count: func(c *config.Config, n string) int {
			return r18CountNamed(c.Hops, n, func(h *config.HopConfig) string { return h.Name })
		},
		unregister: func(n string) { registry.HopRegistry().Unregister(n) },
	},
	{
		kind: "hosts", path: "/config/hosts",
		seed: func(c *config.Config, n string) { c.Hosts = []*config.HostsConfig{{Name: n}} },
		count: func(c *config.Config, n string) int {
			return r18CountNamed(c.Hosts, n, func(h *config.HostsConfig) string { return h.Name })
		},
		unregister: func(n string) { registry.HostsRegistry().Unregister(n) },
	},
	{
		kind: "ingress", path: "/config/ingresses",
		seed: func(c *config.Config, n string) { c.Ingresses = []*config.IngressConfig{{Name: n}} },
		count: func(c *config.Config, n string) int {
			return r18CountNamed(c.Ingresses, n, func(in *config.IngressConfig) string { return in.Name })
		},
		unregister: func(n string) { registry.IngressRegistry().Unregister(n) },
	},
	{
		kind: "limiter", path: "/config/limiters",
		seed: func(c *config.Config, n string) { c.Limiters = []*config.LimiterConfig{{Name: n}} },
		count: func(c *config.Config, n string) int {
			return r18CountNamed(c.Limiters, n, func(l *config.LimiterConfig) string { return l.Name })
		},
		unregister: func(n string) { registry.TrafficLimiterRegistry().Unregister(n) },
	},
	{
		kind: "observer", path: "/config/observers",
		seed: func(c *config.Config, n string) { c.Observers = []*config.ObserverConfig{{Name: n}} },
		count: func(c *config.Config, n string) int {
			return r18CountNamed(c.Observers, n, func(o *config.ObserverConfig) string { return o.Name })
		},
		unregister: func(n string) { registry.ObserverRegistry().Unregister(n) },
	},
	{
		kind: "rate_limiter", path: "/config/rlimiters",
		seed: func(c *config.Config, n string) { c.RLimiters = []*config.LimiterConfig{{Name: n}} },
		count: func(c *config.Config, n string) int {
			return r18CountNamed(c.RLimiters, n, func(l *config.LimiterConfig) string { return l.Name })
		},
		unregister: func(n string) { registry.RateLimiterRegistry().Unregister(n) },
	},
	{
		kind: "recorder", path: "/config/recorders",
		seed: func(c *config.Config, n string) { c.Recorders = []*config.RecorderConfig{{Name: n}} },
		count: func(c *config.Config, n string) int {
			return r18CountNamed(c.Recorders, n, func(r *config.RecorderConfig) string { return r.Name })
		},
		unregister: func(n string) { registry.RecorderRegistry().Unregister(n) },
	},
	{
		kind: "resolver", path: "/config/resolvers",
		seed: func(c *config.Config, n string) { c.Resolvers = []*config.ResolverConfig{{Name: n}} },
		count: func(c *config.Config, n string) int {
			return r18CountNamed(c.Resolvers, n, func(r *config.ResolverConfig) string { return r.Name })
		},
		unregister: func(n string) { registry.ResolverRegistry().Unregister(n) },
	},
	{
		kind: "router", path: "/config/routers",
		seed: func(c *config.Config, n string) { c.Routers = []*config.RouterConfig{{Name: n}} },
		count: func(c *config.Config, n string) int {
			return r18CountNamed(c.Routers, n, func(r *config.RouterConfig) string { return r.Name })
		},
		unregister: func(n string) { registry.RouterRegistry().Unregister(n) },
	},
	{
		kind: "sd", path: "/config/sds",
		seed: func(c *config.Config, n string) { c.SDs = []*config.SDConfig{{Name: n}} },
		count: func(c *config.Config, n string) int {
			return r18CountNamed(c.SDs, n, func(s *config.SDConfig) string { return s.Name })
		},
		unregister: func(n string) { registry.SDRegistry().Unregister(n) },
	},
}

func TestR18F04CreateDedupesExistingConfigEntry(t *testing.T) {
	r := r18TestRouter()
	for _, tc := range r18F04Cases {
		t.Run(tc.kind, func(t *testing.T) {
			name := "r18f04_" + tc.kind
			cfg := &config.Config{}
			tc.seed(cfg, name)
			r18SeedConfig(t, cfg)
			t.Cleanup(func() { tc.unregister(name) })
			tc.unregister(name)

			w := r18Post(t, r, tc.path, `{"name":"`+name+`"}`)
			if w.Code != http.StatusOK {
				t.Fatalf("create %s status = %d, body = %s", tc.kind, w.Code, w.Body.String())
			}
			if n := tc.count(config.Global(), name); n != 1 {
				t.Fatalf("expected exactly 1 in-memory entry for %s %q, got %d", tc.kind, name, n)
			}
		})
	}
}
