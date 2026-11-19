package nftables

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildApplyCommandStopsAfterValidationFailure(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	applyMarker := filepath.Join(dir, "applied")
	nftPath := filepath.Join(dir, "nft")
	fake := `#!/bin/sh
echo "$*" >> "` + logPath + `"
if [ "$1" = "list" ]; then
  exit 0
fi
if [ "$1" = "-c" ]; then
  exit 1
fi
touch "` + applyMarker + `"
`
	if err := os.WriteFile(nftPath, []byte(fake), 0o755); err != nil {
		t.Fatalf("write fake nft: %v", err)
	}

	command := buildApplyCommand(nftPath, "flmx", "table inet flmx { }", false)
	result := exec.Command("sh", "-c", command)
	if err := result.Run(); err == nil {
		t.Fatal("expected validation failure")
	}
	if _, err := os.Stat(applyMarker); !os.IsNotExist(err) {
		t.Fatalf("apply ran after validation failure, stat err=%v", err)
	}
	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake nft calls: %v", err)
	}
	if strings.Count(string(calls), "-f ") != 1 {
		t.Fatalf("expected validation only, got calls:\n%s", calls)
	}
}

func TestBuildCapabilityCheckCommandValidatesRenderedRulesWithoutApplying(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	nftPath := filepath.Join(dir, "nft")
	fake := `#!/bin/sh
echo "$*" >> "` + logPath + `"
if [ "$1" = "--version" ] || [ "$1" = "-c" ]; then
  exit 0
fi
exit 1
`
	if err := os.WriteFile(nftPath, []byte(fake), 0o755); err != nil {
		t.Fatalf("write fake nft: %v", err)
	}

	command := strings.Replace(buildCapabilityCheckCommand(nftPath, "flmx_capability_test"), "command -v nft", "command -v "+nftPath, 1)
	result := exec.Command("sh", "-c", command)
	if output, err := result.CombinedOutput(); err != nil {
		t.Fatalf("capability command failed: %v: %s", err, output)
	}
	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake nft calls: %v", err)
	}
	if strings.Count(string(calls), "-c -f ") != 1 || strings.Contains(string(calls), "\n-f ") {
		t.Fatalf("expected one check-only invocation, got calls:\n%s", calls)
	}
	if !strings.Contains(command, "table inet flmx_capability_test") || !strings.Contains(command, "meta l4proto tcp ct original proto-dst") {
		t.Fatalf("capability check does not contain representative rendered rules:\n%s", command)
	}
}

func TestBuildApplyCommandUsesAtomicReplacementBatch(t *testing.T) {
	dir := t.TempDir()
	batchPath := filepath.Join(dir, "batch.nft")
	nftPath := filepath.Join(dir, "nft")
	fake := `#!/bin/sh
if [ "$1" = "list" ]; then
  exit 0
fi
if [ "$1" = "-f" ]; then
  cp "$2" "` + batchPath + `"
fi
exit 0
`
	if err := os.WriteFile(nftPath, []byte(fake), 0o755); err != nil {
		t.Fatalf("write fake nft: %v", err)
	}

	script := "table inet flmx {\n  chain forward { }\n}"
	result := exec.Command("sh", "-c", buildApplyCommand(nftPath, "flmx", script, false))
	if output, err := result.CombinedOutput(); err != nil {
		t.Fatalf("apply command failed: %v: %s", err, output)
	}
	batch, err := os.ReadFile(batchPath)
	if err != nil {
		t.Fatalf("read applied batch: %v", err)
	}
	// The isolated table already exists: only it is deleted, the
	// plain tables are left alone (the one-shot migration gate).
	want := "delete table inet flmx\n" + script + "\n"
	if string(batch) != want {
		t.Fatalf("atomic batch = %q, want %q", batch, want)
	}
}

// B-01: the migration now needs ownership proof (migratePlain) on
// top of the P1-1 first-apply gate. With proof, the isolated table
// missing and the pre-isolation plain tables present, the batch
// deletes the plain tables as well, so no residual table keeps
// being served alongside the new one.
func TestBuildApplyCommandMigratesPlainTablesOnFirstApply(t *testing.T) {
	dir := t.TempDir()
	batchPath := filepath.Join(dir, "batch.nft")
	nftPath := filepath.Join(dir, "nft")
	fake := `#!/bin/sh
if [ "$1" = "list" ]; then
  case "$4" in
    flmx|flvx) exit 0 ;;
    *) exit 1 ;;
  esac
fi
if [ "$1" = "-f" ]; then
  cp "$2" "` + batchPath + `"
fi
exit 0
`
	if err := os.WriteFile(nftPath, []byte(fake), 0o755); err != nil {
		t.Fatalf("write fake nft: %v", err)
	}

	script := "table inet flmx_e3b0c442 {\n  chain forward { }\n}"
	result := exec.Command("sh", "-c", buildApplyCommand(nftPath, "flmx_e3b0c442", script, true))
	if output, err := result.CombinedOutput(); err != nil {
		t.Fatalf("apply command failed: %v: %s", err, output)
	}
	batch, err := os.ReadFile(batchPath)
	if err != nil {
		t.Fatalf("read applied batch: %v", err)
	}
	want := "delete table inet flmx\ndelete table inet flvx\n" + script + "\n"
	if string(batch) != want {
		t.Fatalf("migration batch = %q, want %q", batch, want)
	}
}

// B-01: without ownership proof the plain tables are left alone
// even on the first apply after the upgrade — a plain table on the
// node may be another panel's live table (FLMX + flux-panel, or two
// FLMX instances, co-managing one nft node).
func TestBuildApplyCommandLeavesForeignPlainTablesWithoutProof(t *testing.T) {
	dir := t.TempDir()
	batchPath := filepath.Join(dir, "batch.nft")
	nftPath := filepath.Join(dir, "nft")
	fake := `#!/bin/sh
if [ "$1" = "list" ]; then
  case "$4" in
    flmx|flvx) exit 0 ;;
    *) exit 1 ;;
  esac
fi
if [ "$1" = "-f" ]; then
  cp "$2" "` + batchPath + `"
fi
exit 0
`
	if err := os.WriteFile(nftPath, []byte(fake), 0o755); err != nil {
		t.Fatalf("write fake nft: %v", err)
	}

	script := "table inet flmx_e3b0c442 {\n  chain forward { }\n}"
	result := exec.Command("sh", "-c", buildApplyCommand(nftPath, "flmx_e3b0c442", script, false))
	if output, err := result.CombinedOutput(); err != nil {
		t.Fatalf("apply command failed: %v: %s", err, output)
	}
	batch, err := os.ReadFile(batchPath)
	if err != nil {
		t.Fatalf("read applied batch: %v", err)
	}
	// Only the new table is loaded; no plain table is deleted.
	if string(batch) != script+"\n" {
		t.Fatalf("unproven batch = %q, want %q", batch, script+"\n")
	}
}

// B-01: the migration runs at most once. Once the isolated table
// exists, a later apply deletes only it — even with proof and even
// when plain tables are (re)present, they are another panel's live
// tables by then, not this install's residue.
func TestBuildApplyCommandMigratesOnlyOnce(t *testing.T) {
	dir := t.TempDir()
	batchPath := filepath.Join(dir, "batch.nft")
	nftPath := filepath.Join(dir, "nft")
	fake := `#!/bin/sh
if [ "$1" = "list" ]; then
  exit 0
fi
if [ "$1" = "-f" ]; then
  cp "$2" "` + batchPath + `"
fi
exit 0
`
	if err := os.WriteFile(nftPath, []byte(fake), 0o755); err != nil {
		t.Fatalf("write fake nft: %v", err)
	}

	script := "table inet flmx_e3b0c442 {\n  chain forward { }\n}"
	result := exec.Command("sh", "-c", buildApplyCommand(nftPath, "flmx_e3b0c442", script, true))
	if output, err := result.CombinedOutput(); err != nil {
		t.Fatalf("apply command failed: %v: %s", err, output)
	}
	batch, err := os.ReadFile(batchPath)
	if err != nil {
		t.Fatalf("read applied batch: %v", err)
	}
	want := "delete table inet flmx_e3b0c442\n" + script + "\n"
	if string(batch) != want {
		t.Fatalf("second-apply batch = %q, want %q", batch, want)
	}
}

func TestAuthMethodsDefaultToPrivateKey(t *testing.T) {
	privateKey := mustGeneratePrivateKey(t)
	methods, err := authMethods(SSHConfig{PrivateKey: privateKey})
	if err != nil {
		t.Fatalf("authMethods: %v", err)
	}
	if len(methods) != 1 {
		t.Fatalf("expected 1 auth method, got %d", len(methods))
	}
}

func TestAuthMethodsDefaultPrivateKeyRequiresKey(t *testing.T) {
	_, err := authMethods(SSHConfig{})
	if err == nil || !strings.Contains(err.Error(), "SSH 私钥不能为空") {
		t.Fatalf("expected private key required error, got %v", err)
	}
}

func mustGeneratePrivateKey(t *testing.T) string {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	block := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}
	return string(pem.EncodeToMemory(block))
}
