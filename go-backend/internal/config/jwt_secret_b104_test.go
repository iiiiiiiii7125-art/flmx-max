package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B1-04：JWT_SECRET 弱密钥在启动单点（EnsureJWTSecret）打具名
// WARN JWT_SECRET_WEAK——只告警不拒启；达标静默。本组钉住：
// 短密钥告警在位、长但低熵告警在位、达标密钥不告警，且告警
// 文本绝不含密钥本体。

func TestJWTSecretWeakShortEnvSecretWarns(t *testing.T) {
	dir := t.TempDir()
	buf := captureStdLog(t)
	cfg := &Config{
		DBPath:       filepath.Join(dir, "panel.db"),
		JWTSecret:    "short-secret",
		JWTEphemeral: false,
	}
	if _, err := EnsureJWTSecret(cfg); err != nil {
		t.Fatalf("EnsureJWTSecret: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "WARN JWT_SECRET_WEAK") {
		t.Fatalf("short secret must warn, got log %q", out)
	}
	if strings.Contains(out, "short-secret") {
		t.Fatalf("warning must never contain the secret itself, got %q", out)
	}
	if cfg.JWTSecret != "short-secret" {
		t.Fatal("warn-only: the configured secret must be kept, startup not blocked")
	}
}

func TestJWTSecretWeakLowEntropyLongSecretWarns(t *testing.T) {
	dir := t.TempDir()
	buf := captureStdLog(t)
	cfg := &Config{
		DBPath:       filepath.Join(dir, "panel.db"),
		JWTSecret:    strings.Repeat("a", 40),
		JWTEphemeral: false,
	}
	if _, err := EnsureJWTSecret(cfg); err != nil {
		t.Fatalf("EnsureJWTSecret: %v", err)
	}
	if !strings.Contains(buf.String(), "WARN JWT_SECRET_WEAK") {
		t.Fatalf("low-entropy long secret must warn, got log %q", buf.String())
	}
}

func TestJWTSecretWeakAdequateSecretSilent(t *testing.T) {
	dir := t.TempDir()
	buf := captureStdLog(t)
	cfg := &Config{
		DBPath:       filepath.Join(dir, "panel.db"),
		JWTSecret:    "9f2c41aa07b34d5e8f01c6a2b3d495e7f08a1b2c3d4e5f60718293a4b5c6d7e8",
		JWTEphemeral: false,
	}
	if _, err := EnsureJWTSecret(cfg); err != nil {
		t.Fatalf("EnsureJWTSecret: %v", err)
	}
	if strings.Contains(buf.String(), "JWT_SECRET_WEAK") {
		t.Fatalf("adequate secret must stay silent, got log %q", buf.String())
	}
}

func TestJWTSecretWeakFileSecretWarns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, jwtSecretFileName)
	if err := os.WriteFile(path, []byte("file-weak\n"), 0o600); err != nil {
		t.Fatalf("write jwt_secret fixture: %v", err)
	}
	buf := captureStdLog(t)
	cfg := jwtTestCfg(dir)
	if _, err := EnsureJWTSecret(cfg); err != nil {
		t.Fatalf("EnsureJWTSecret: %v", err)
	}
	if cfg.JWTSecret != "file-weak" {
		t.Fatalf("file secret must be loaded, got %q", cfg.JWTSecret)
	}
	if !strings.Contains(buf.String(), "WARN JWT_SECRET_WEAK") {
		t.Fatalf("weak file secret must warn, got log %q", buf.String())
	}
}
