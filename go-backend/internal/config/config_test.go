package config

import (
	"os"
	"testing"
)

// F-AUDIT-009: an empty JWT_SECRET must never be used as an HMAC key
// (it makes JWTs and the subscription ?user= sig forgeable).
func TestEphemeralJWTSecret(t *testing.T) {
	os.Unsetenv("JWT_SECRET")
	cfg := FromEnv()
	if cfg.JWTSecret == "" {
		t.Fatal("expected an ephemeral JWT secret to be generated, got empty")
	}
	if !cfg.JWTEphemeral {
		t.Fatal("expected JWTEphemeral=true when JWT_SECRET is unset")
	}
	if len(cfg.JWTSecret) < 32 {
		t.Fatalf("ephemeral secret too short: %d chars", len(cfg.JWTSecret))
	}
	// Two processes must not share the same ephemeral secret.
	cfg2 := FromEnv()
	if cfg.JWTSecret == cfg2.JWTSecret {
		t.Fatal("ephemeral secrets must differ across processes")
	}
}

func TestConfiguredJWTSecret(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret-value")
	cfg := FromEnv()
	if cfg.JWTSecret != "test-secret-value" {
		t.Fatalf("expected configured secret, got %q", cfg.JWTSecret)
	}
	if cfg.JWTEphemeral {
		t.Fatal("expected JWTEphemeral=false when JWT_SECRET is set")
	}
}
