package config

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// R17-A 回归夹具：R17-L4 毒化文件原件的复制件。
// 原件 ~/workspace/flvx-max/audit-out-132/r17/R17-L4-evidence/gost.json-candidate-written.json
// md5 e381299b2aa931d2f7c097661ead6702；14 个服务，6_1_0_tcp / 6_1_0_udp 各重复一次（尾部第 11-14 位）。
const r17PoisonedFixtureMD5 = "e381299b2aa931d2f7c097661ead6702"

func loadR17PoisonedFixture(t *testing.T) *Config {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "r17-poisoned-gost.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if got := fmt.Sprintf("%x", md5.Sum(data)); got != r17PoisonedFixtureMD5 {
		t.Fatalf("fixture md5 mismatch: got %s, want %s (fixture must be an exact copy of the R17-L4 evidence file)", got, r17PoisonedFixtureMD5)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	return &cfg
}

func countServiceNames(cfg *Config) map[string]int {
	seen := map[string]int{}
	for _, svc := range cfg.Services {
		if svc == nil {
			continue
		}
		seen[svc.Name]++
	}
	return seen
}

func TestDeduplicateServicesRemovesPoisonedDuplicates(t *testing.T) {
	cfg := loadR17PoisonedFixture(t)
	if len(cfg.Services) != 14 {
		t.Fatalf("fixture should have 14 services, got %d", len(cfg.Services))
	}

	removed := DeduplicateServices(cfg)

	if len(removed) != 2 || removed[0] != "6_1_0_tcp" || removed[1] != "6_1_0_udp" {
		t.Fatalf("removed = %v, want [6_1_0_tcp 6_1_0_udp]", removed)
	}
	if len(cfg.Services) != 12 {
		t.Fatalf("expected 12 services after dedupe, got %d", len(cfg.Services))
	}
	for name, n := range countServiceNames(cfg) {
		if n != 1 {
			t.Fatalf("service %q appears %d times after dedupe, want 1", name, n)
		}
	}
	// 保留首次出现：原顺序不变，只是尾部重复项被去掉。
	if cfg.Services[10] == nil || cfg.Services[10].Name != "6_1_0_tcp" {
		t.Fatalf("first occurrence of 6_1_0_tcp must be kept at index 10, got %#v", cfg.Services[10])
	}
	if cfg.Services[11] == nil || cfg.Services[11].Name != "6_1_0_udp" {
		t.Fatalf("first occurrence of 6_1_0_udp must be kept at index 11, got %#v", cfg.Services[11])
	}
}

func TestDeduplicateServicesNoDuplicatesIsNoop(t *testing.T) {
	cfg := &Config{Services: []*ServiceConfig{
		{Name: "a"},
		{Name: "b"},
	}}
	if removed := DeduplicateServices(cfg); removed != nil {
		t.Fatalf("removed = %v, want nil", removed)
	}
	if len(cfg.Services) != 2 {
		t.Fatalf("expected 2 services, got %d", len(cfg.Services))
	}
	if removed := DeduplicateServices(nil); removed != nil {
		t.Fatalf("DeduplicateServices(nil) = %v, want nil", removed)
	}
}

func TestPersistNowWritesDedupedConfig(t *testing.T) {
	cfg := loadR17PoisonedFixture(t)
	if removed := DeduplicateServices(cfg); len(removed) != 2 {
		t.Fatalf("removed = %v, want 2 names", removed)
	}

	originalPath := persistPath
	originalEnabled := persistEnable
	t.Cleanup(func() {
		persistPath = originalPath
		persistEnable = originalEnabled
	})
	persistPath = filepath.Join(t.TempDir(), "gost.json")
	persistEnable = false // PersistNow 不依赖 EnablePersist

	if err := PersistNow(cfg); err != nil {
		t.Fatalf("PersistNow: %v", err)
	}
	data, err := os.ReadFile(persistPath)
	if err != nil {
		t.Fatalf("read persisted config: %v", err)
	}
	var onDisk Config
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatalf("unmarshal persisted config: %v", err)
	}
	if len(onDisk.Services) != 12 {
		t.Fatalf("expected 12 persisted services, got %d", len(onDisk.Services))
	}
	for name, n := range countServiceNames(&onDisk) {
		if n != 1 {
			t.Fatalf("persisted service %q appears %d times, want 1", name, n)
		}
	}
}

func TestPersistNowWithoutPathFails(t *testing.T) {
	originalPath := persistPath
	t.Cleanup(func() { persistPath = originalPath })
	persistPath = ""
	if err := PersistNow(&Config{}); err == nil {
		t.Fatal("PersistNow with empty persist path should fail")
	}
}

// R17-A: 启动自愈编排——program.Start/reloadConfig 在 loader.Load 之前调
// 用 HealPersistedServices：毒化配置去重（告警内容即 removed 名单），
// 去重后的配置立即重新落盘，下次启动不再读到毒化文件。
func TestHealPersistedServices(t *testing.T) {
	cfg := loadR17PoisonedFixture(t)

	originalPath := persistPath
	originalEnabled := persistEnable
	persistFile := filepath.Join(t.TempDir(), "gost.json")
	t.Cleanup(func() {
		persistPath = originalPath
		persistEnable = originalEnabled
	})
	persistPath = persistFile
	persistEnable = false // HealPersistedServices 不依赖 EnablePersist

	removed, err := HealPersistedServices(cfg)
	if err != nil {
		t.Fatalf("HealPersistedServices: %v", err)
	}
	if len(removed) != 2 || removed[0] != "6_1_0_tcp" || removed[1] != "6_1_0_udp" {
		t.Fatalf("removed = %v, want [6_1_0_tcp 6_1_0_udp]", removed)
	}
	if len(cfg.Services) != 12 {
		t.Fatalf("expected 12 services after heal, got %d", len(cfg.Services))
	}

	onDiskData, err := os.ReadFile(persistFile)
	if err != nil {
		t.Fatalf("healed config was not persisted: %v", err)
	}
	var onDisk Config
	if err := json.Unmarshal(onDiskData, &onDisk); err != nil {
		t.Fatalf("unmarshal persisted config: %v", err)
	}
	if len(onDisk.Services) != 12 {
		t.Fatalf("expected 12 persisted services, got %d", len(onDisk.Services))
	}
	for name, n := range countServiceNames(&onDisk) {
		if n != 1 {
			t.Fatalf("persisted service %q appears %d times, want 1", name, n)
		}
	}
}

func TestHealPersistedServicesCleanIsNoop(t *testing.T) {
	originalPath := persistPath
	dir := t.TempDir()
	t.Cleanup(func() { persistPath = originalPath })
	persistPath = filepath.Join(dir, "gost.json")

	cfg := &Config{Services: []*ServiceConfig{{Name: "a"}, {Name: "b"}}}
	removed, err := HealPersistedServices(cfg)
	if err != nil {
		t.Fatalf("HealPersistedServices: %v", err)
	}
	if removed != nil {
		t.Fatalf("removed = %v, want nil", removed)
	}
	if _, err := os.Stat(filepath.Join(dir, "gost.json")); err == nil {
		t.Fatal("clean config must not trigger a persist write")
	}
}
