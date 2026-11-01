package repo

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"go-backend/internal/store/model"
)

// R4B09-001 (KR3): restore must mirror the save side for
// subscription_front_urls — the same normalize/validate single point
// (NormalizeSubscriptionFrontURLs, consumed by both
// handler.normalizeAndValidateConfigValue and importConfigs).
// Non-canonical values must never land verbatim via import: illegal
// values are skipped per-key with a warning (same disposition as the
// latency_targets / brand-asset / monitoring restore checks), legal
// values land normalized, canonical values round-trip byte-identical.
// Written red-first: on the baseline importConfigs stores this key
// unchecked, so every illegal case below lands verbatim.

const r4kr3FrontKey = "subscription_front_urls"

func r4kr3ImportFront(t *testing.T, r *Repository, configs map[string]string) *model.ImportResult {
	t.Helper()
	res, err := r.Import(&model.BackupData{Version: "1.0", Configs: configs}, []string{"configs"})
	if err != nil {
		t.Fatalf("Import configs failed: %v", err)
	}
	return res
}

func r4kr3StoredFront(t *testing.T, r *Repository) (string, bool) {
	t.Helper()
	cfg, err := r.GetConfigByName(r4kr3FrontKey)
	if err != nil {
		t.Fatalf("GetConfigByName: %v", err)
	}
	if cfg == nil {
		return "", false
	}
	return cfg.Value, true
}

func r4kr3ClearFront(t *testing.T, r *Repository) {
	t.Helper()
	if err := r.DB().Exec(`DELETE FROM vite_config WHERE name = 'subscription_front_urls'`).Error; err != nil {
		t.Fatalf("clear subscription_front_urls: %v", err)
	}
}

func TestR4KR3SubscriptionFrontRestoreSkipsIllegalValues(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	overCap := make([]string, 0, 21)
	for i := 0; i < 21; i++ {
		overCap = append(overCap, fmt.Sprintf(`"https://f%d.example.com"`, i))
	}
	cases := []struct {
		name string
		raw  string
	}{
		{"not-json-array", `https://a.example.com`},
		{"json-object", `{"url":"https://a.example.com"}`},
		{"non-string-element", `[42]`},
		{"empty-element", `[""]`},
		{"missing-scheme", `["a.example.com"]`},
		{"bad-scheme", `["ftp://a.example.com"]`},
		{"with-path", `["https://a.example.com/sub"]`},
		{"with-query", `["https://a.example.com/?x=1"]`},
		{"with-fragment", `["https://a.example.com/#f"]`},
		{"with-userinfo", `["https://user:pass@a.example.com"]`},
		{"empty-label-host", `["https://a..example.com"]`},
		{"trailing-dot-host", `["https://a.example.com."]`},
		{"missing-host", `["https://"]`},
		{"port-zero", `["https://a.example.com:0"]`},
		{"port-too-large", `["https://a.example.com:99999"]`},
		{"one-bad-among-good", `["https://a.example.com","a.example.com"]`},
		{"over-cap", "[" + strings.Join(overCap, ",") + "]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r4kr3ClearFront(t, r)
			res := r4kr3ImportFront(t, r, map[string]string{r4kr3FrontKey: tc.raw})
			if res.ConfigsImported != 0 {
				t.Fatalf("ConfigsImported = %d, want 0 (raw %q must be skipped)", res.ConfigsImported, tc.raw)
			}
			if got, ok := r4kr3StoredFront(t, r); ok {
				t.Fatalf("stored subscription_front_urls %q, want no row (raw %q must not land)", got, tc.raw)
			}
			if res.SkippedRows["configs"] < 1 {
				t.Fatalf("SkippedRows[configs] = %d, want >= 1", res.SkippedRows["configs"])
			}
			if !strings.Contains(strings.Join(res.ImportWarnings, "\n"), r4kr3FrontKey) {
				t.Fatalf("warnings %v do not name %s", res.ImportWarnings, r4kr3FrontKey)
			}
		})
	}
}

func TestR4KR3SubscriptionFrontRestoreNormalizesAndRoundTrips(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	// Legal but unnormalized: must land normalized, never verbatim.
	r4kr3ClearFront(t, r)
	res := r4kr3ImportFront(t, r, map[string]string{r4kr3FrontKey: `["HTTPS://SUB.0v0.DOG/"]`})
	if res.ConfigsImported != 1 {
		t.Fatalf("unnormalized: ConfigsImported = %d, want 1 (warnings %v)", res.ConfigsImported, res.ImportWarnings)
	}
	if got, ok := r4kr3StoredFront(t, r); !ok || got != `["https://sub.0v0.dog"]` {
		t.Fatalf("unnormalized: stored %q (ok=%v), want %q", got, ok, `["https://sub.0v0.dog"]`)
	}

	// Dedupe after normalization (trailing slash / case variants collapse).
	r4kr3ClearFront(t, r)
	res = r4kr3ImportFront(t, r, map[string]string{r4kr3FrontKey: `["https://a.example.com/","https://A.example.com"]`})
	if res.ConfigsImported != 1 {
		t.Fatalf("dedupe: ConfigsImported = %d, want 1 (warnings %v)", res.ConfigsImported, res.ImportWarnings)
	}
	if got, ok := r4kr3StoredFront(t, r); !ok || got != `["https://a.example.com"]` {
		t.Fatalf("dedupe: stored %q (ok=%v), want %q", got, ok, `["https://a.example.com"]`)
	}

	// Canonical value (what the product itself exports) round-trips
	// byte-identical.
	r4kr3ClearFront(t, r)
	canonical := `["https://sub.0v0.dog","http://192.0.2.10:8080","https://[2001:db8::1]:8443"]`
	res = r4kr3ImportFront(t, r, map[string]string{r4kr3FrontKey: canonical})
	if res.ConfigsImported != 1 {
		t.Fatalf("canonical: ConfigsImported = %d, want 1 (warnings %v)", res.ConfigsImported, res.ImportWarnings)
	}
	if got, ok := r4kr3StoredFront(t, r); !ok || got != canonical {
		t.Fatalf("canonical: stored %q (ok=%v), want verbatim %q", got, ok, canonical)
	}

	// Empty value restores as empty (clearable, same as save side).
	r4kr3ClearFront(t, r)
	res = r4kr3ImportFront(t, r, map[string]string{r4kr3FrontKey: ""})
	if res.ConfigsImported != 1 {
		t.Fatalf("empty: ConfigsImported = %d, want 1", res.ConfigsImported)
	}
	if got, ok := r4kr3StoredFront(t, r); !ok || got != "" {
		t.Fatalf("empty: stored %q (ok=%v), want empty row", got, ok)
	}
}

func TestR4KR3SubscriptionFrontRestoreSkipDoesNotBlockOtherKeys(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	// Disposition parity with the other restore-side checks: a bad
	// value skips only its own key; the rest of the configs segment
	// still imports (no whole-import rejection).
	res := r4kr3ImportFront(t, r, map[string]string{
		r4kr3FrontKey:  `["https://a.example.com/with-path"]`,
		"panel_domain": "panel.example.com",
	})
	if res.ConfigsImported != 1 {
		t.Fatalf("ConfigsImported = %d, want 1 (only panel_domain; warnings %v)", res.ConfigsImported, res.ImportWarnings)
	}
	if got, ok := r4kr3StoredFront(t, r); ok {
		t.Fatalf("stored subscription_front_urls %q, want no row", got)
	}
	cfg, err := r.GetConfigByName("panel_domain")
	if err != nil {
		t.Fatalf("GetConfigByName panel_domain: %v", err)
	}
	if cfg == nil || cfg.Value != "panel.example.com" {
		t.Fatalf("panel_domain not restored alongside skipped front key: %+v", cfg)
	}
}
