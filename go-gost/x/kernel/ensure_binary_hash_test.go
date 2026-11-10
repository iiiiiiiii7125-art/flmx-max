package kernel

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// C4: ensureBinary re-verifies an existing binary against a pinned
// SHA-256 when one is locally available; without a pinned
// expectation the existence-skip is kept.

func TestEnsureBinaryPinnedHashMatchSkipsInstall(t *testing.T) {
	dir := t.TempDir()
	content := []byte("fake-kernel-binary")
	if err := os.WriteFile(filepath.Join(dir, "fakebin"), content, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	d := &Download{BinName: "fakebin", Archive: "raw", SHA256: map[string]string{runtime.GOARCH: hex.EncodeToString(sum[:])}}
	m := NewManager(t.TempDir())
	inst := &instance{logs: &logRing{}}
	path, err := m.ensureBinary(inst, "fake 1.0", d, dir)
	if err != nil {
		t.Fatalf("ensureBinary match = %v, want nil", err)
	}
	if path != filepath.Join(dir, "fakebin") {
		t.Fatalf("path = %s", path)
	}
}

func TestEnsureBinaryPinnedHashMismatchReinstalls(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fakebin"), []byte("tampered"), 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("expected-content"))
	// URL is empty so the reinstall attempt fails fast with
	// "download address incomplete" — proving the mismatched file
	// was treated as missing rather than returned as-is.
	d := &Download{BinName: "fakebin", Archive: "raw", SHA256: map[string]string{runtime.GOARCH: hex.EncodeToString(sum[:])}}
	m := NewManager(t.TempDir())
	inst := &instance{logs: &logRing{}}
	path, err := m.ensureBinary(inst, "fake 1.0", d, dir)
	if err == nil || path != "" {
		t.Fatalf("ensureBinary mismatch = (%q, %v), want reinstall attempt failure", path, err)
	}
	if !strings.Contains(err.Error(), "安装") {
		t.Fatalf("err = %v, want install failure", err)
	}
}

func TestEnsureBinaryNoPinnedHashKeepsExistenceSkip(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fakebin"), []byte("anything"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Checksum-file-only spec: no locally available expectation.
	d := &Download{BinName: "fakebin", Archive: "raw", ChecksumURL: "https://example.invalid/sums.txt"}
	m := NewManager(t.TempDir())
	inst := &instance{logs: &logRing{}}
	path, err := m.ensureBinary(inst, "fake 1.0", d, dir)
	if err != nil || path != filepath.Join(dir, "fakebin") {
		t.Fatalf("ensureBinary no-pin = (%q, %v), want existence skip", path, err)
	}
	if _, ok := pinnedSHA256(d); ok {
		t.Fatal("pinnedSHA256 must be unavailable for checksum-only spec")
	}
}
