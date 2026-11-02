package repo

import "testing"

func TestConfigPolicy(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want ConfigAccessPolicy
	}{
		{name: "app_name is public", key: "app_name", want: ConfigAccessPublic},
		{name: "app_logo is public", key: "app_logo", want: ConfigAccessPublic},
		{name: "app_favicon is public", key: "app_favicon", want: ConfigAccessPublic},
		{name: "app_bg_image is public", key: "app_bg_image", want: ConfigAccessPublic},
		{name: "app_bg_image_light is public", key: "app_bg_image_light", want: ConfigAccessPublic},
		{name: "app_bg_image_dark is public", key: "app_bg_image_dark", want: ConfigAccessPublic},
		{name: "cloudflare_site_key is public", key: "cloudflare_site_key", want: ConfigAccessPublic},
		{name: "is_commercial is public", key: "is_commercial", want: ConfigAccessPublic},
		{name: "hide_footer_brand is public", key: "hide_footer_brand", want: ConfigAccessPublic},
		{name: "jwt_secret is sensitive", key: "jwt_secret", want: ConfigAccessSensitive},
		{name: "license_key is sensitive", key: "license_key", want: ConfigAccessSensitive},
		{name: "cloudflare_secret_key is sensitive", key: "cloudflare_secret_key", want: ConfigAccessSensitive},
		{name: "trimmed public key is public", key: "  APP_NAME  ", want: ConfigAccessPublic},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PolicyForConfig(tt.key); got != tt.want {
				t.Fatalf("PolicyForConfig(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestConfigPolicyHelpers(t *testing.T) {
	publicKeys := []string{"app_name", "app_logo", "app_favicon", "app_bg_image", "app_bg_image_light", "app_bg_image_dark", "cloudflare_site_key", "is_commercial", "hide_footer_brand"}
	for _, key := range publicKeys {
		if !IsPublicConfigKey(key) {
			t.Fatalf("expected %q to be public", key)
		}
	}

	sensitiveKeys := []string{"jwt_secret", "license_key", "license_expiry", "license_machine_id", "machine_fingerprint", "cloudflare_secret_key"}
	for _, key := range sensitiveKeys {
		if !IsSensitiveConfigKey(key) {
			t.Fatalf("expected %q to be sensitive", key)
		}
	}

	input := map[string]string{
		"app_name":              "FLVX",
		"license_key":           "secret-license",
		"cloudflare_secret_key": "secret-cloudflare",
		"jwt_secret":            "secret-jwt",
		"cloudflare_site_key":   "site-key",
	}
	filtered := FilterSensitiveConfigs(input)
	if len(filtered) != 2 {
		t.Fatalf("expected 2 public configs, got %d", len(filtered))
	}
	if filtered["app_name"] != "FLVX" || filtered["cloudflare_site_key"] != "site-key" {
		t.Fatalf("unexpected filtered configs: %+v", filtered)
	}
	if _, ok := filtered["jwt_secret"]; ok {
		t.Fatal("expected jwt_secret to be filtered out")
	}
	if _, ok := filtered["license_key"]; ok {
		t.Fatal("expected license_key to be filtered out")
	}
	if _, ok := filtered["cloudflare_secret_key"]; ok {
		t.Fatal("expected cloudflare_secret_key to be filtered out")
	}
}

func TestFilterBackupConfigsOmitsLicenseState(t *testing.T) {
	filtered := FilterBackupConfigs(map[string]string{
		"app_name":            "Brand",
		"license_key":         "secret-license",
		"license_expiry":      "never",
		"license_machine_id":  "machine-id",
		"is_commercial":       "true",
		"machine_fingerprint": "fingerprint",
	})
	if len(filtered) != 1 || filtered["app_name"] != "Brand" {
		t.Fatalf("unexpected backup configs: %+v", filtered)
	}
}

// RB4-01: the backup filter must follow PolicyForConfig's
// default-sensitive verdict, not only the explicit sensitive list.
// Keys that are sensitive only by the default rule — legacy keys left
// in stored configs after their setting was removed (github_proxy_*,
// removed wholesale in 1.7.7; proxy URLs commonly embed user:pass) and
// arbitrary unknown keys an admin can write — must not ride into
// backup exports in plaintext.
func TestFilterBackupConfigsFiltersDefaultSensitiveKeys(t *testing.T) {
	for _, key := range []string{"github_proxy_url", "github_proxy_enabled", "totally_unknown_credential_key"} {
		if PolicyForConfig(key) != ConfigAccessSensitive {
			t.Fatalf("precondition: %s must classify sensitive via the default rule", key)
		}
		if IsSensitiveConfigKey(key) || IsSystemManagedConfigKey(key) || isBackupRuntimeExcludedConfigKey(key) {
			t.Fatalf("precondition: %s must reach the filter only via the default-sensitive rule", key)
		}
	}
	filtered := FilterBackupConfigs(map[string]string{
		"github_proxy_url":               "http://user:pass@proxy.example:8080",
		"github_proxy_enabled":           "true",
		"totally_unknown_credential_key": "s3cret",
	})
	if len(filtered) != 0 {
		t.Fatalf("default-sensitive keys must be filtered out of backups: %+v", filtered)
	}
}

// RB4-01 companion: the pending_realm_settle:<nodeID> family is
// sensitive only by the default rule, but its values are realm
// settlement markers that must round-trip with backups (X-P3-34).
// They survive via the explicit prefix exemption in
// FilterBackupConfigs — losing them would drop settlements on
// restore. Legitimate classified configs ride along unchanged.
func TestFilterBackupConfigsKeepsRealmSettleMarkers(t *testing.T) {
	filtered := FilterBackupConfigs(map[string]string{
		"app_name":                "Brand",
		"notify_daily_hour":       "9",
		"pending_realm_settle:42": `{"debt":1}`,
		"pending_realm_settle:7":  `{"debt":2}`,
	})
	if len(filtered) != 4 {
		t.Fatalf("realm settle markers and legitimate configs must be kept: %+v", filtered)
	}
	if filtered["pending_realm_settle:42"] != `{"debt":1}` || filtered["pending_realm_settle:7"] != `{"debt":2}` {
		t.Fatalf("realm settle markers changed: %+v", filtered)
	}
}

func TestSystemManagedConfigKeys(t *testing.T) {
	for _, key := range []string{"license_key", "license_expiry", "license_machine_id", "is_commercial", "machine_fingerprint"} {
		if !IsSystemManagedConfigKey(key) {
			t.Fatalf("expected %s to be system managed", key)
		}
	}
	for _, key := range []string{"jwt_secret", "cloudflare_secret_key", "app_name"} {
		if IsSystemManagedConfigKey(key) {
			t.Fatalf("did not expect %s to be system managed", key)
		}
	}
}
