// R19-F02: loader 17 类 nil 项回归。
//
// 背景：R19-X-02——loader.register() 对 17 类逐项取 `.Name` 注册，仅
// services 有显式 guard（"service config is nil"）；其余 16 类 nil 即
// panic。落盘 gost.json 含一个 JSON null（viper 保持为 nil 项，见
// R19-FIX-F02-REPORT §3），启动时 loader.Load 即 panic、agent 退出；
// 自愈又明确保留 nil，故崩溃循环只能手修文件。
//
// 修复后：16 类补与 services 同口径的显式 guard，nil 项返回干净错误
// （"<kind> config is nil"），不得 panic。落盘毒化文件本身由启动自愈
// （StripNilConfigItems）先清除，loader 守卫是直接调用 Load 的后备。
package loader_test

import (
	"strings"
	"testing"

	"github.com/go-gost/x/config"
	"github.com/go-gost/x/config/loader"
	"github.com/go-gost/x/registry"
)

func cleanupR19F02Registries() {
	for name := range registry.LoggerRegistry().GetAll() {
		registry.LoggerRegistry().Unregister(name)
	}
	for name := range registry.AutherRegistry().GetAll() {
		registry.AutherRegistry().Unregister(name)
	}
	for name := range registry.AdmissionRegistry().GetAll() {
		registry.AdmissionRegistry().Unregister(name)
	}
	for name := range registry.BypassRegistry().GetAll() {
		registry.BypassRegistry().Unregister(name)
	}
	for name := range registry.ResolverRegistry().GetAll() {
		registry.ResolverRegistry().Unregister(name)
	}
	for name := range registry.HostsRegistry().GetAll() {
		registry.HostsRegistry().Unregister(name)
	}
	for name := range registry.IngressRegistry().GetAll() {
		registry.IngressRegistry().Unregister(name)
	}
	for name := range registry.RouterRegistry().GetAll() {
		registry.RouterRegistry().Unregister(name)
	}
	for name := range registry.SDRegistry().GetAll() {
		registry.SDRegistry().Unregister(name)
	}
	for name := range registry.ObserverRegistry().GetAll() {
		registry.ObserverRegistry().Unregister(name)
	}
	for name := range registry.RecorderRegistry().GetAll() {
		registry.RecorderRegistry().Unregister(name)
	}
	for name := range registry.TrafficLimiterRegistry().GetAll() {
		registry.TrafficLimiterRegistry().Unregister(name)
	}
	for name := range registry.ConnLimiterRegistry().GetAll() {
		registry.ConnLimiterRegistry().Unregister(name)
	}
	for name := range registry.RateLimiterRegistry().GetAll() {
		registry.RateLimiterRegistry().Unregister(name)
	}
	for name := range registry.HopRegistry().GetAll() {
		registry.HopRegistry().Unregister(name)
	}
	for name := range registry.ChainRegistry().GetAll() {
		registry.ChainRegistry().Unregister(name)
	}
	for name := range registry.ServiceRegistry().GetAll() {
		if svc := registry.ServiceRegistry().Get(name); svc != nil {
			svc.Close()
		}
		registry.ServiceRegistry().Unregister(name)
	}
}

func r19LoaderRunAndCatch(f func()) (panicked bool, value any) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
			value = r
		}
	}()
	f()
	return false, nil
}

// 17 类各一形：单 nil 项进 loader.Load，不得 panic、须返回干净错误。
type r19LoaderNilCase struct {
	kind string
	cfg  func() *config.Config
}

var r19LoaderNilCases = []r19LoaderNilCase{
	{kind: "logger", cfg: func() *config.Config { return &config.Config{Loggers: []*config.LoggerConfig{nil}} }},
	{kind: "auther", cfg: func() *config.Config { return &config.Config{Authers: []*config.AutherConfig{nil}} }},
	{kind: "admission", cfg: func() *config.Config { return &config.Config{Admissions: []*config.AdmissionConfig{nil}} }},
	{kind: "bypass", cfg: func() *config.Config { return &config.Config{Bypasses: []*config.BypassConfig{nil}} }},
	{kind: "resolver", cfg: func() *config.Config { return &config.Config{Resolvers: []*config.ResolverConfig{nil}} }},
	{kind: "hosts", cfg: func() *config.Config { return &config.Config{Hosts: []*config.HostsConfig{nil}} }},
	{kind: "ingress", cfg: func() *config.Config { return &config.Config{Ingresses: []*config.IngressConfig{nil}} }},
	{kind: "router", cfg: func() *config.Config { return &config.Config{Routers: []*config.RouterConfig{nil}} }},
	{kind: "sd", cfg: func() *config.Config { return &config.Config{SDs: []*config.SDConfig{nil}} }},
	{kind: "observer", cfg: func() *config.Config { return &config.Config{Observers: []*config.ObserverConfig{nil}} }},
	{kind: "recorder", cfg: func() *config.Config { return &config.Config{Recorders: []*config.RecorderConfig{nil}} }},
	{kind: "limiter", cfg: func() *config.Config { return &config.Config{Limiters: []*config.LimiterConfig{nil}} }},
	{kind: "conn limiter", cfg: func() *config.Config { return &config.Config{CLimiters: []*config.LimiterConfig{nil}} }},
	{kind: "rate limiter", cfg: func() *config.Config { return &config.Config{RLimiters: []*config.LimiterConfig{nil}} }},
	{kind: "hop", cfg: func() *config.Config { return &config.Config{Hops: []*config.HopConfig{nil}} }},
	{kind: "chain", cfg: func() *config.Config { return &config.Config{Chains: []*config.ChainConfig{nil}} }},
	{kind: "service", cfg: func() *config.Config { return &config.Config{Services: []*config.ServiceConfig{nil}} }},
}

func TestR19F02LoaderNilItems(t *testing.T) {
	for _, tc := range r19LoaderNilCases {
		t.Run(tc.kind, func(t *testing.T) {
			unlock := config.LockMutation()
			defer unlock()
			defer cleanupR19F02Registries()

			var err error
			panicked, v := r19LoaderRunAndCatch(func() { err = loader.Load(tc.cfg()) })
			if panicked {
				t.Fatalf("loader.Load(nil %s) PANICKED: %v", tc.kind, v)
			}
			if err == nil {
				t.Fatalf("loader.Load(nil %s) returned nil error, want clean \"is nil\" error", tc.kind)
			}
			if !strings.Contains(err.Error(), "nil") {
				t.Fatalf("loader.Load(nil %s) error = %v, want it to mention nil", tc.kind, err)
			}
			t.Logf("loader.Load(nil %s) clean error: %v", tc.kind, err)
		})
	}
}

// nil 项在有效项之后也不得 panic（位置无关性）。
func TestR19F02LoaderNilAfterValid(t *testing.T) {
	unlock := config.LockMutation()
	defer unlock()
	defer cleanupR19F02Registries()

	cfg := &config.Config{
		Chains:   []*config.ChainConfig{{Name: "r19f02-ok"}, nil},
		Limiters: []*config.LimiterConfig{{Name: "r19f02-ok", Limits: []string{"$ 1"}}, nil},
	}
	var err error
	panicked, v := r19LoaderRunAndCatch(func() { err = loader.Load(cfg) })
	if panicked {
		t.Fatalf("loader.Load(nil after valid) PANICKED: %v", v)
	}
	if err == nil {
		t.Fatalf("loader.Load(nil after valid) returned nil error, want clean error")
	}
	t.Logf("loader.Load(nil after valid) clean error: %v", err)
}
