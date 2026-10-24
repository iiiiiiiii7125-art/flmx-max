package loader_test

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-gost/x/config"
	"github.com/go-gost/x/config/loader"
	_ "github.com/go-gost/x/connector/http"
	_ "github.com/go-gost/x/dialer/tcp"
	_ "github.com/go-gost/x/handler/forward/local"
	_ "github.com/go-gost/x/listener/tcp"
	_ "github.com/go-gost/x/listener/udp"
	"github.com/go-gost/x/registry"
)

// R17-A: 启动失败点定位——毒化 gost.json（同名服务两份）未经去重进入
// loader.Load 时，第二次 Register 报 registry: duplicate object，
// program.Start 直接返回错误、agent 退出。去重后的同一配置必须能加载。
const r17PoisonedFixtureMD5 = "e381299b2aa931d2f7c097661ead6702"

func loadR17PoisonedConfig(t *testing.T) *config.Config {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "testdata", "r17-poisoned-gost.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if got := fmt.Sprintf("%x", md5.Sum(data)); got != r17PoisonedFixtureMD5 {
		t.Fatalf("fixture md5 mismatch: got %s, want %s", got, r17PoisonedFixtureMD5)
	}
	var cfg config.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	return &cfg
}

func TestLoadPoisonedConfigFailsWithDuplicate(t *testing.T) {
	unlock := config.LockMutation()
	defer unlock()
	defer func() {
		for name := range registry.ServiceRegistry().GetAll() {
			registry.ServiceRegistry().Unregister(name)
		}
	}()

	cfg := loadR17PoisonedConfig(t)
	if len(cfg.Services) != 14 {
		t.Fatalf("fixture should have 14 services, got %d", len(cfg.Services))
	}
	err := loader.Load(cfg)
	if err == nil {
		t.Fatal("expected loader.Load to fail on duplicate service names")
	}
	// 失败点：毒化配置在第 13 个服务（第二个 6_1_0_tcp）处失败——
	// ParseService 时监听器已 eager 绑定同一地址，报 address already
	// in use；若地址不同则走到 Register 的 registry: duplicate object。
	// 任一分支都意味着 program.Start 返回错误、agent 退出。
	if !isPoisonedLoadError(err) {
		t.Fatalf("unexpected loader error for poisoned config: %v", err)
	}
	t.Logf("loader.Load(poisoned) error (startup failure point): %v", err)
}

func TestLoadHealedConfigSucceeds(t *testing.T) {
	unlock := config.LockMutation()
	defer unlock()
	defer func() {
		for name := range registry.ServiceRegistry().GetAll() {
			registry.ServiceRegistry().Unregister(name)
		}
	}()

	cfg := loadR17PoisonedConfig(t)
	if removed := config.DeduplicateServices(cfg); len(removed) != 2 {
		t.Fatalf("removed = %v, want 2 names", removed)
	}
	if err := loader.Load(cfg); err != nil {
		t.Fatalf("loader.Load(healed config): %v", err)
	}
	registered := registry.ServiceRegistry().GetAll()
	if len(registered) != 12 {
		t.Fatalf("expected 12 registered services, got %d", len(registered))
	}
	for _, name := range []string{"6_1_0_tcp", "6_1_0_udp", "1_2_1_tcp"} {
		if registry.ServiceRegistry().Get(name) == nil {
			t.Fatalf("service %q not registered after healed load", name)
		}
	}
}

func isPoisonedLoadError(err error) bool {
	if err == nil {
		return false
	}
	if err == registry.ErrDup {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "duplicate object") ||
		strings.Contains(msg, "address already in use")
}
