package repo

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"go-backend/internal/store/model"
)

// R171-B5 H-171-01: restore must reuse the save-side latency_targets
// validation (format layer of handler.normalizeLatencyTargetsConfig):
// bad values are skipped with a warning naming latency_targets, legal
// values restore verbatim. Written red-first: fails on the baseline
// where importConfigs stores any value unchecked.

func r171b5LatencyTargetsLegal() string {
	return `[{"name":"电信","host":"8.8.8.8","port":443,"enabled":true}]`
}

func r171b5ImportLatencyTargets(t *testing.T, r *Repository, raw string) *model.ImportResult {
	t.Helper()
	res, err := r.Import(&model.BackupData{Version: "1.0", Configs: map[string]string{"latency_targets": raw}}, []string{"configs"})
	if err != nil {
		t.Fatalf("Import configs failed: %v", err)
	}
	return res
}

func r171b5StoredLatencyTargets(t *testing.T, r *Repository) (string, bool) {
	t.Helper()
	cfg, err := r.GetConfigByName("latency_targets")
	if err != nil {
		t.Fatalf("GetConfigByName: %v", err)
	}
	if cfg == nil {
		return "", false
	}
	return cfg.Value, true
}

func TestR171B5LatencyTargetsRestoreSkipsBadValues(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	// H probe cell 1 (R171-H-restore-probe.log): "not-json" must not land.
	res := r171b5ImportLatencyTargets(t, r, "not-json")
	if res.ConfigsImported != 0 {
		t.Fatalf("badjson: ConfigsImported = %d, want 0", res.ConfigsImported)
	}
	if got, ok := r171b5StoredLatencyTargets(t, r); ok {
		t.Fatalf("badjson: stored latency_targets %q, want no row", got)
	}
	if !strings.Contains(strings.Join(res.ImportWarnings, "\n"), "latency_targets") {
		t.Fatalf("badjson: warnings %v do not name latency_targets", res.ImportWarnings)
	}

	// H probe cell 2: structurally valid JSON but illegal host/port.
	res = r171b5ImportLatencyTargets(t, r, `[{"name":"x","host":"bad host!!","port":0,"enabled":true}]`)
	if res.ConfigsImported != 0 {
		t.Fatalf("badhost: ConfigsImported = %d, want 0", res.ConfigsImported)
	}
	if got, ok := r171b5StoredLatencyTargets(t, r); ok {
		t.Fatalf("badhost: stored latency_targets %q, want no row", got)
	}
	if !strings.Contains(strings.Join(res.ImportWarnings, "\n"), "latency_targets") {
		t.Fatalf("badhost: warnings %v do not name latency_targets", res.ImportWarnings)
	}

	// Legal value restores verbatim (R-09 legal-cell口径).
	legal := r171b5LatencyTargetsLegal()
	res = r171b5ImportLatencyTargets(t, r, legal)
	if res.ConfigsImported != 1 {
		t.Fatalf("legal: ConfigsImported = %d, want 1 (warnings %v)", res.ConfigsImported, res.ImportWarnings)
	}
	if got, ok := r171b5StoredLatencyTargets(t, r); !ok || got != legal {
		t.Fatalf("legal: stored %q (ok=%v), want verbatim %q", got, ok, legal)
	}

	// Empty value restores as empty (clearable, same as save side).
	if err := r.DB().Exec(`DELETE FROM vite_config WHERE name = 'latency_targets'`).Error; err != nil {
		t.Fatalf("clear latency_targets: %v", err)
	}
	res = r171b5ImportLatencyTargets(t, r, "")
	if res.ConfigsImported != 1 {
		t.Fatalf("empty: ConfigsImported = %d, want 1", res.ConfigsImported)
	}
	if got, ok := r171b5StoredLatencyTargets(t, r); !ok || got != "" {
		t.Fatalf("empty: stored %q (ok=%v), want empty row", got, ok)
	}
}

// TestR171B5ValidateLatencyTargetsConfigValue locks the restore-side
// validator to the save-side rule set (see the handler-side corpus test
// TestR171B5LatencyTargetsSaveSideCorpus for the other half): same
// verdict on every case, so the two copies cannot drift silently.
func TestR171B5ValidateLatencyTargetsConfigValue(t *testing.T) {
	many := func(n int, pinned int) string {
		parts := make([]string, 0, n)
		for i := 0; i < n; i++ {
			p := false
			if i < pinned {
				p = true
			}
			parts = append(parts, fmt.Sprintf(`{"name":"t%d","host":"8.8.8.8","port":443,"enabled":true,"pinned":%v}`, i, p))
		}
		return "[" + strings.Join(parts, ",") + "]"
	}
	cases := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{"badjson", "not-json", true},
		{"badhost", `[{"name":"x","host":"bad host!!","port":0,"enabled":true}]`, true},
		{"object-not-array", `{"name":"x"}`, true},
		{"empty-name", `[{"name":"","host":"8.8.8.8","port":443,"enabled":true}]`, true},
		{"long-name", `[{"name":"012345678901234567890","host":"8.8.8.8","port":443,"enabled":true}]`, true},
		{"dup-names", `[{"name":"x","host":"8.8.8.8","port":443,"enabled":true},{"name":"x","host":"8.8.4.4","port":443,"enabled":true}]`, true},
		{"too-many", many(31, 0), true},
		{"too-many-pinned", many(4, 4), true},
		{"bad-port-zero", `[{"name":"x","host":"8.8.8.8","port":0,"enabled":true}]`, true},
		{"bad-port-over", `[{"name":"x","host":"8.8.8.8","port":70000,"enabled":true}]`, true},
		{"empty-label-host", `[{"name":"x","host":"a..b","port":443,"enabled":true}]`, true},
		{"loopback", `[{"name":"x","host":"127.0.0.1","port":443,"enabled":true}]`, true},
		{"localhost", `[{"name":"x","host":"localhost","port":443,"enabled":true}]`, true},
		{"scheme-like-host", `[{"name":"x","host":"https://example.com","port":443,"enabled":true}]`, true},
		{"legal", r171b5LatencyTargetsLegal(), false},
		{"legal-max", many(30, 3), false},
		{"empty", "", false},
		{"null", "null", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateLatencyTargetsConfigValue(tc.raw)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateLatencyTargetsConfigValue(%q) err = %v, wantErr = %v", tc.raw, err, tc.wantErr)
			}
		})
	}
}
