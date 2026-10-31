package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// R3-A01-003: HealPersistedConfig is the single shared self-heal
// entry for every config-load path (program startup/SIGHUP and the
// local API reload). It must apply all three steps in the canonical
// order — strip nil items, dedupe all named slices, prune orphan
// conn limiters — in one pass, because the API reload previously
// ran the strip step only and a duplicate-poisoned gost.json could
// never heal through it.
func TestR3A01HealPersistedConfigAppliesFullSequence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gost.json")
	oldPath := PersistPath()
	SetPersistPath(path)
	defer SetPersistPath(oldPath)

	cfg := &Config{
		Services: []*ServiceConfig{
			nil, // nil item read back from a persisted JSON null
			{Name: "svc-r3-k2"},
		},
		Loggers: []*LoggerConfig{
			{Name: "dup-r3-k2"},
			{Name: "dup-r3-k2"},
		},
		CLimiters: []*LimiterConfig{
			{Name: "rule_conn_limit_orphan_r3"},
		},
	}

	report := HealPersistedConfig(cfg)

	if len(report.Stripped) == 0 {
		t.Fatal("step 1 (strip nil) reported nothing")
	}
	foundDup := false
	for _, heal := range report.Duplicates {
		if heal.Kind == "logger" && len(heal.Removed) == 1 && heal.Removed[0] == "dup-r3-k2" {
			foundDup = true
		}
		if heal.Err != nil {
			t.Fatalf("dedupe persist failed: %v", heal.Err)
		}
	}
	if !foundDup {
		t.Fatalf("step 2 (dedupe) did not remove the duplicate logger: %+v", report.Duplicates)
	}
	if len(report.Pruned) != 1 || report.Pruned[0] != "rule_conn_limit_orphan_r3" {
		t.Fatalf("step 3 (prune orphans) = %v, want [rule_conn_limit_orphan_r3]", report.Pruned)
	}
	if report.StripPersistErr != nil || report.PrunePersistErr != nil {
		t.Fatalf("persist errors: strip=%v prune=%v", report.StripPersistErr, report.PrunePersistErr)
	}

	if len(cfg.Services) != 1 || cfg.Services[0].Name != "svc-r3-k2" {
		t.Fatalf("services after heal = %+v", cfg.Services)
	}
	if len(cfg.Loggers) != 1 {
		t.Fatalf("loggers after heal = %d, want 1", len(cfg.Loggers))
	}
	if len(cfg.CLimiters) != 0 {
		t.Fatalf("climiters after heal = %d, want 0", len(cfg.CLimiters))
	}

	// Removed() must flatten in step order: stripped, duplicates,
	// pruned.
	removed := report.Removed()
	if len(removed) != 3 {
		t.Fatalf("Removed() = %v, want 3 entries", removed)
	}
	if !strings.Contains(removed[0], "nil") || removed[1] != "dup-r3-k2" || removed[2] != "rule_conn_limit_orphan_r3" {
		t.Fatalf("Removed() order = %v, want [nil-desc, dup-r3-k2, rule_conn_limit_orphan_r3]", removed)
	}

	// The healed config must be what landed on disk.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("healed config was not persisted: %v", err)
	}
	var onDisk Config
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("persisted config does not parse: %v", err)
	}
	if len(onDisk.Loggers) != 1 || len(onDisk.CLimiters) != 0 || len(onDisk.Services) != 1 {
		t.Fatalf("persisted config not healed: loggers=%d climiters=%d services=%d",
			len(onDisk.Loggers), len(onDisk.CLimiters), len(onDisk.Services))
	}
}

// A clean config must produce an empty report and no persist write.
func TestR3A01HealPersistedConfigCleanConfigUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gost.json")
	oldPath := PersistPath()
	SetPersistPath(path)
	defer SetPersistPath(oldPath)

	cfg := &Config{
		Services: []*ServiceConfig{{Name: "svc-clean-r3"}},
		Loggers:  []*LoggerConfig{{Name: "log-clean-r3"}},
	}
	report := HealPersistedConfig(cfg)
	if removed := report.Removed(); len(removed) != 0 {
		t.Fatalf("clean config healed something: %v", removed)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("clean config must not be re-persisted, stat err = %v", err)
	}
	if got := HealPersistedConfig(nil).Removed(); got != nil {
		t.Fatalf("nil config Removed() = %v, want nil", got)
	}
}
