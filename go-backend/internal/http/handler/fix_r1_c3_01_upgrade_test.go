package handler

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// R18-R2-C3-01: the in-panel self-upgrade path backfills the
// per-install identity (FLMX_INSTALL_ID) for deployments that
// predate it. installIDPin is the pure decision (missing key
// only, never rewrite); pinLegacyResourceNames is the wiring
// (same atomic .env write as the resource-name pins, before any
// backup/compose swap, so a rollback keeps the issued identity).

func TestInstallIDPinGeneratesValidUUIDWhenMissing(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 8; i++ {
		pin, ok := installIDPin(map[string]string{})
		if !ok {
			t.Fatal("missing key must yield a pin")
		}
		if pin.key != envKeyInstallID {
			t.Fatalf("pin key = %q, want %q", pin.key, envKeyInstallID)
		}
		if !validInstallID(pin.value) {
			t.Fatalf("generated identity %q must pass validInstallID", pin.value)
		}
		if seen[pin.value] {
			t.Fatalf("generated identity must be unique, saw %q twice", pin.value)
		}
		seen[pin.value] = true
	}
}

func TestInstallIDPinNeverRewritesExistingKey(t *testing.T) {
	for _, env := range []map[string]string{
		{envKeyInstallID: "11111111-1111-1111-1111-111111111111"},
		{envKeyInstallID: " 11111111-1111-1111-1111-111111111111 "},
		// Even an invalid existing value is left alone: the
		// panel falls back to the legacy derivation for it, and
		// rewriting would switch tables. The operator fixes it
		// by deleting the line and re-running the upgrade.
		{envKeyInstallID: "has space"},
	} {
		if _, ok := installIDPin(env); ok {
			t.Fatalf("existing key %q must never be rewritten", env[envKeyInstallID])
		}
	}
}

func TestInstallIDPinFillsEmptyValue(t *testing.T) {
	pin, ok := installIDPin(map[string]string{envKeyInstallID: ""})
	if !ok || !validInstallID(pin.value) {
		t.Fatalf("empty value must be treated as missing, got pin=%+v ok=%v", pin, ok)
	}
	// applyEnvPins fills an empty line in place and never touches
	// a non-empty one: the pin mechanism itself enforces
	// "only fill what's missing".
	data := []byte("DB_TYPE=sqlite\nFLMX_INSTALL_ID=\nFLMX_VERSION=1.9.7\n")
	out := string(applyEnvPins(data, []envKeyValue{pin}))
	if !strings.Contains(out, "FLMX_INSTALL_ID="+pin.value) {
		t.Fatalf("empty install-id line must be filled in place, got:\n%s", out)
	}
	data = []byte("DB_TYPE=sqlite\nFLMX_INSTALL_ID=11111111-1111-1111-1111-111111111111\n")
	out = string(applyEnvPins(data, []envKeyValue{pin}))
	if !strings.Contains(out, "FLMX_INSTALL_ID=11111111-1111-1111-1111-111111111111") {
		t.Fatalf("existing install-id must be preserved verbatim, got:\n%s", out)
	}
}

func TestPinLegacyResourceNamesBackfillsInstallID(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	original := "DB_TYPE=sqlite\nFLMX_VERSION=1.9.7\nJWT_SECRET=test\n"
	if err := os.WriteFile(envPath, []byte(original), 0o600); err != nil {
		t.Fatalf("WriteFile(.env) error = %v", err)
	}
	executor := &systemUpgradeExecutor{deployDir: dir, backendContainer: "no-such-backend-for-r1-c3-01"}
	if _, _, err := executor.pinLegacyResourceNames(context.Background()); err != nil {
		t.Fatalf("pinLegacyResourceNames error = %v", err)
	}
	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("ReadFile(.env) error = %v", err)
	}
	env := parseEnvFileValues(data)
	id := strings.TrimSpace(env[envKeyInstallID])
	if id == "" {
		t.Fatalf("upgrade must backfill FLMX_INSTALL_ID into .env, got:\n%s", data)
	}
	if !validInstallID(id) {
		t.Fatalf("backfilled identity %q must pass validInstallID", id)
	}
	if !strings.Contains(string(data), "DB_TYPE=sqlite\n") || !strings.Contains(string(data), "FLMX_VERSION=1.9.7\n") {
		t.Fatalf("backfill must not disturb other keys, got:\n%s", data)
	}
	// Second run: the issued identity is stable, never rewritten.
	if _, _, err := executor.pinLegacyResourceNames(context.Background()); err != nil {
		t.Fatalf("second pinLegacyResourceNames error = %v", err)
	}
	data2, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("ReadFile(.env) error = %v", err)
	}
	if got := strings.TrimSpace(parseEnvFileValues(data2)[envKeyInstallID]); got != id {
		t.Fatalf("second upgrade rewrote the identity: was %q, now %q", id, got)
	}
}

func TestPinLegacyResourceNamesKeepsExistingInstallID(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	original := "DB_TYPE=sqlite\nFLMX_VERSION=1.9.7\nFLMX_INSTALL_ID=11111111-1111-1111-1111-111111111111\n"
	if err := os.WriteFile(envPath, []byte(original), 0o600); err != nil {
		t.Fatalf("WriteFile(.env) error = %v", err)
	}
	executor := &systemUpgradeExecutor{deployDir: dir, backendContainer: "no-such-backend-for-r1-c3-01"}
	if _, _, err := executor.pinLegacyResourceNames(context.Background()); err != nil {
		t.Fatalf("pinLegacyResourceNames error = %v", err)
	}
	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("ReadFile(.env) error = %v", err)
	}
	if got := strings.TrimSpace(parseEnvFileValues(data)[envKeyInstallID]); got != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("upgrade must never rewrite an existing identity, got %q", got)
	}
}
