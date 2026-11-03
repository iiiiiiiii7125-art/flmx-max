package config

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// R7-19：jwt_secret 持久化改原子写（临时文件＋rename）并打来源
// 日志（读到既有密钥／新生成密钥两形态）。日志只记文件路径，
// 断言其绝不含密钥本体或片段。

func jwtTestCfg(dir string) *Config {
	return &Config{
		DBPath:       filepath.Join(dir, "panel.db"),
		JWTSecret:    "ephemeral-placeholder-must-not-be-persisted",
		JWTEphemeral: true,
	}
}

// captureStdLog 把标准 logger 输出重定向到 buffer（config 包的
// 来源日志走标准 logger）；测试结束自动恢复。
func captureStdLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })
	return &buf
}

// 新生成格：generated=true、密钥为 64 位 hex 且已换掉临时值；
// 落盘文件 0600、内容与内存一致、无 .tmp 残留；来源日志点名
// 「新生成」且不含密钥本体与片段。
func TestEnsureJWTSecretGeneratesAtomicallyAndLogsSource(t *testing.T) {
	dir := t.TempDir()
	buf := captureStdLog(t)
	cfg := jwtTestCfg(dir)

	generated, err := EnsureJWTSecret(cfg)
	if err != nil {
		t.Fatalf("EnsureJWTSecret: %v", err)
	}
	if !generated {
		t.Fatal("generated = false, want true on first creation")
	}
	if cfg.JWTEphemeral {
		t.Fatal("JWTEphemeral must be false after persistence")
	}
	if cfg.JWTSecret == "ephemeral-placeholder-must-not-be-persisted" || len(cfg.JWTSecret) != 64 {
		t.Fatalf("persisted secret malformed: len=%d", len(cfg.JWTSecret))
	}
	path := filepath.Join(dir, jwtSecretFileName)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat jwt_secret: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("jwt_secret perm = %o, want 0600", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read jwt_secret: %v", err)
	}
	if string(data) != cfg.JWTSecret+"\n" {
		t.Fatal("jwt_secret file content must equal the in-memory secret")
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("tmp file must not linger after atomic publish, stat err = %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "jwt_secret 来源: 新生成密钥") {
		t.Fatalf("source log must name the generated branch, got %q", out)
	}
	if strings.Contains(out, cfg.JWTSecret) || strings.Contains(out, cfg.JWTSecret[8:24]) {
		t.Fatal("source log must not contain the secret or any fragment of it")
	}
}

// 读既有格：generated=false、内存与文件均为既有值、文件字节不
// 被改写；重启形态（新进程口径的第二次调用）仍读回同一密钥、
// 不回归；来源日志点名「读到既有密钥」且不含密钥本体与片段。
func TestEnsureJWTSecretReadsExistingAndLogsSource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, jwtSecretFileName)
	existing := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := os.WriteFile(path, []byte(existing+"\n"), 0o600); err != nil {
		t.Fatalf("seed jwt_secret: %v", err)
	}
	buf := captureStdLog(t)

	cfg := jwtTestCfg(dir)
	generated, err := EnsureJWTSecret(cfg)
	if err != nil {
		t.Fatalf("EnsureJWTSecret: %v", err)
	}
	if generated {
		t.Fatal("generated = true, want false when an existing file is present")
	}
	if cfg.JWTSecret != existing {
		t.Fatal("existing secret must be loaded verbatim")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back jwt_secret: %v", err)
	}
	if string(data) != existing+"\n" {
		t.Fatal("existing jwt_secret file must not be rewritten on the read path")
	}
	out := buf.String()
	if !strings.Contains(out, "jwt_secret 来源: 读到既有密钥") {
		t.Fatalf("source log must name the existing branch, got %q", out)
	}
	if strings.Contains(out, existing) || strings.Contains(out, existing[8:24]) {
		t.Fatal("source log must not contain the secret or any fragment of it")
	}

	// 重启不回归：全新 Config 再调一次，仍读回同一密钥。
	cfg2 := jwtTestCfg(dir)
	generated2, err := EnsureJWTSecret(cfg2)
	if err != nil {
		t.Fatalf("EnsureJWTSecret (restart): %v", err)
	}
	if generated2 || cfg2.JWTSecret != existing {
		t.Fatal("restart must read back the same persisted secret")
	}
}

// 崩溃撕裂形态：原子写中途崩溃至多遗留 .tmp 半成品——既有
// jwt_secret 本体不被半写覆盖，下次启动读到的仍是完整旧值、
// .tmp 内容永不被当作真值。
func TestEnsureJWTSecretLeftoverTmpDoesNotClobberExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, jwtSecretFileName)
	existing := "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	if err := os.WriteFile(path, []byte(existing+"\n"), 0o600); err != nil {
		t.Fatalf("seed jwt_secret: %v", err)
	}
	// 模拟崩溃现场：.tmp 里是写了一半的新密钥。
	if err := os.WriteFile(path+".tmp", []byte("halftorn-new-secret"), 0o600); err != nil {
		t.Fatalf("seed torn tmp: %v", err)
	}
	cfg := jwtTestCfg(dir)
	generated, err := EnsureJWTSecret(cfg)
	if err != nil {
		t.Fatalf("EnsureJWTSecret: %v", err)
	}
	if generated {
		t.Fatal("generated = true, want false: the intact existing file wins over a torn tmp")
	}
	if cfg.JWTSecret != existing {
		t.Fatal("torn tmp content must never be accepted as the secret")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back jwt_secret: %v", err)
	}
	if string(data) != existing+"\n" {
		t.Fatal("existing jwt_secret must remain byte-identical after a torn-write crash")
	}
}

// 环境变量已配置（JWTEphemeral=false）：直接返回、不碰文件系统。
func TestEnsureJWTSecretConfiguredSkipsFile(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		DBPath:       filepath.Join(dir, "panel.db"),
		JWTSecret:    "env-provided-secret",
		JWTEphemeral: false,
	}
	generated, err := EnsureJWTSecret(cfg)
	if err != nil {
		t.Fatalf("EnsureJWTSecret: %v", err)
	}
	if generated {
		t.Fatal("generated = true, want false for env-configured secret")
	}
	if cfg.JWTSecret != "env-provided-secret" {
		t.Fatal("env-configured secret must be kept")
	}
	if _, err := os.Stat(filepath.Join(dir, jwtSecretFileName)); !os.IsNotExist(err) {
		t.Fatalf("no jwt_secret file must be created for env-configured secret, stat err = %v", err)
	}
}
