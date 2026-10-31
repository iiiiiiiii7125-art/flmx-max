package loader

import (
	"strings"
	"testing"

	"github.com/go-gost/x/config"
)

// R3-A01-003 regression (transferred from the A01 seat probe
// TestR3A01ReloadHealGap): both config-load entrances — program
// startup/SIGHUP and the local API's reload handler — now run the
// single shared heal entry config.HealPersistedConfig. This test
// pins the two ends of the gap that entry closed:
//
//   - the OLD API sequence (StripNilConfigItems + Reload only)
//     still fails on a duplicate-name-poisoned config — that is
//     the defect A01-003 documented;
//   - the shared-entry sequence (HealPersistedConfig + Reload)
//     loads the same poisoned config cleanly, which is what both
//     entrances execute today.
func TestR3A01SharedHealEntryClosesReloadGap(t *testing.T) {
	oldPath := config.PersistPath()
	config.SetPersistPath(t.TempDir() + "/gost.json")
	defer config.SetPersistPath(oldPath)

	dupLoggerCfg := func() *config.Config {
		return &config.Config{
			Loggers: []*config.LoggerConfig{
				{Name: "dup-r3-k2"},
				{Name: "dup-r3-k2"},
			},
		}
	}

	// Old API sequence: strip nil only.
	apiCfg := dupLoggerCfg()
	_ = config.StripNilConfigItems(apiCfg)
	err := Reload(apiCfg, func(*config.Config) error { return nil })
	if err == nil {
		t.Fatal("strip-only sequence unexpectedly healed duplicate loggers")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate-object error from strip-only sequence, got: %v", err)
	}

	// Shared-entry sequence used by both entrances now.
	healedCfg := dupLoggerCfg()
	report := config.HealPersistedConfig(healedCfg)
	if len(report.Duplicates) == 0 {
		t.Fatal("shared heal entry removed no duplicates")
	}
	if err := Reload(healedCfg, func(*config.Config) error { return nil }); err != nil {
		t.Fatalf("shared-heal sequence must load cleanly, got: %v", err)
	}
}
