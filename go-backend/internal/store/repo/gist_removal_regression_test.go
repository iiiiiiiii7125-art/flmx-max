package repo

import (
	"path/filepath"
	"testing"
)

// DEV-RM-GIST regression pins (2.1.0): the gist_* config keys are
// unregistered — no longer system-managed (generic config/update has
// no dedicated gist write path) and no longer classified sensitive
// beyond the fail-closed default for unlisted keys.
func TestGistConfigKeysUnregistered(t *testing.T) {
	gistKeys := []string{
		"gist_token", "gist_password", "gist_id", "gist_interval_hours",
		"gist_last_at", "gist_last_upload", "gist_last_error", "gist_last_hash",
	}
	for _, key := range gistKeys {
		if IsSystemManagedConfigKey(key) {
			t.Fatalf("%s must no longer be system-managed", key)
		}
		if _, listed := sensitiveConfigKeys[key]; listed {
			t.Fatalf("%s must no longer be listed in sensitiveConfigKeys", key)
		}
		if _, listed := systemManagedConfigKeys[key]; listed {
			t.Fatalf("%s must no longer be listed in systemManagedConfigKeys", key)
		}
	}
	if _, listed := backupRuntimeExcludedConfigKeys["notify_gist_failure_pending"]; listed {
		t.Fatal("notify_gist_failure_pending must no longer be a backup runtime-excluded key")
	}
	// The quota pending ledger is a different feature and stays.
	if _, listed := backupRuntimeExcludedConfigKeys["notify_quota_recovery_pending"]; !listed {
		t.Fatal("notify_quota_recovery_pending must stay backup runtime-excluded")
	}
}

// DEV-RM-GIST: the schema v8 migration deletes stored gist config rows
// (including the GitHub credential row) and leaves every other row.
func TestMigrateRemoveGistConfigKeys(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "gist-migration.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	db := r.DB()

	// Seed via UpsertConfigs (idempotent): Open() already seeds some
	// rows itself (app_name), so raw INSERTs could collide.
	seed := map[string]string{
		"gist_token":                      "ghp_test_token",
		"gist_password":                   "pw",
		"gist_id":                         "123456",
		"gist_interval_hours":             "24",
		"gist_last_at":                    "1",
		"gist_last_upload":                "ok",
		"gist_last_error":                 "",
		"gist_last_hash":                  "abc",
		"notify_gist_failure_pending":     "{}",
		"notify_gist_failure_pending_gen": "3",
		"notify_quota_recovery_pending":   "{}",
	}
	if err := r.UpsertConfigs(seed, 1); err != nil {
		t.Fatalf("seed configs: %v", err)
	}

	if err := migrateRemoveGistConfigKeys(db); err != nil {
		t.Fatalf("migrateRemoveGistConfigKeys: %v", err)
	}

	for name := range seed {
		var count int64
		if err := db.Raw(`SELECT COUNT(*) FROM vite_config WHERE name = ?`, name).Row().Scan(&count); err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		switch name {
		case "notify_quota_recovery_pending":
			if count != 1 {
				t.Fatalf("%s must survive the migration, count = %d", name, count)
			}
		default:
			if count != 0 {
				t.Fatalf("%s must be deleted by the migration, count = %d", name, count)
			}
		}
	}
	// app_name is seeded by Open() itself and must survive untouched.
	var appNameCount int64
	if err := db.Raw(`SELECT COUNT(*) FROM vite_config WHERE name = 'app_name'`).Row().Scan(&appNameCount); err != nil {
		t.Fatalf("count app_name: %v", err)
	}
	if appNameCount != 1 {
		t.Fatalf("app_name must survive the migration, count = %d", appNameCount)
	}

	// Idempotent: a second run is a no-op success.
	if err := migrateRemoveGistConfigKeys(db); err != nil {
		t.Fatalf("second migrateRemoveGistConfigKeys run: %v", err)
	}
}
