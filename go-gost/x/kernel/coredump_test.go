package kernel

import (
	"os"
	"path/filepath"
	"testing"
)

// elfHeader builds a minimal ELF header with the given EI_DATA byte
// order marker and e_type.
func elfHeader(bigEndian bool, eType uint16) []byte {
	hdr := make([]byte, 64)
	hdr[0] = 0x7f
	hdr[1] = 'E'
	hdr[2] = 'L'
	hdr[3] = 'F'
	hdr[4] = 2 // 64-bit
	if bigEndian {
		hdr[5] = 2
		hdr[16] = byte(eType >> 8)
		hdr[17] = byte(eType)
	} else {
		hdr[5] = 1
		hdr[16] = byte(eType)
		hdr[17] = byte(eType >> 8)
	}
	return hdr
}

func writeCoreFile(t *testing.T, dir, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRemoveCoreDumps(t *testing.T) {
	dir := t.TempDir()
	const etCore, etExec = 4, 2

	coreLE := writeCoreFile(t, dir, "core", elfHeader(false, etCore))
	corePID := writeCoreFile(t, dir, "core.1234", elfHeader(false, etCore))
	coreBE := writeCoreFile(t, dir, "core.77", elfHeader(true, etCore))
	plainNamedCore := writeCoreFile(t, dir, "core.88", []byte("just a text file named like a core"))
	execNamedCore := writeCoreFile(t, dir, "core.99", elfHeader(false, etExec))
	otherName := writeCoreFile(t, dir, "core.txt", elfHeader(false, etCore))
	prefixName := writeCoreFile(t, dir, "mycore", elfHeader(false, etCore))
	if err := os.Mkdir(filepath.Join(dir, "core.55"), 0o755); err != nil {
		t.Fatal(err)
	}

	removed, freed := RemoveCoreDumps(dir)
	if removed != 3 {
		t.Fatalf("removed = %d, want 3 (core, core.1234, core.77)", removed)
	}
	if want := int64(3 * 64); freed != want {
		t.Fatalf("freed = %d, want %d", freed, want)
	}
	for _, gone := range []string{coreLE, corePID, coreBE} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Fatalf("verified core dump %s must be deleted", gone)
		}
	}
	for _, kept := range []string{plainNamedCore, execNamedCore, otherName, prefixName} {
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("non-core file %s must be kept: %v", kept, err)
		}
	}
	if fi, err := os.Stat(filepath.Join(dir, "core.55")); err != nil || !fi.IsDir() {
		t.Fatal("a directory named like a core must be kept")
	}

	// A second sweep over the same directory is a no-op.
	if removed, freed := RemoveCoreDumps(dir); removed != 0 || freed != 0 {
		t.Fatalf("second sweep = %d/%d, want 0/0", removed, freed)
	}
	// A missing directory is a no-op, not an error.
	if removed, freed := RemoveCoreDumps(filepath.Join(dir, "nope")); removed != 0 || freed != 0 {
		t.Fatalf("missing dir sweep = %d/%d, want 0/0", removed, freed)
	}
}

func TestIsCoreDumpFile(t *testing.T) {
	dir := t.TempDir()
	if isCoreDumpFile(filepath.Join(dir, "missing")) {
		t.Fatal("missing file must not be a core dump")
	}
	short := writeCoreFile(t, dir, "core", []byte{0x7f, 'E'})
	if isCoreDumpFile(short) {
		t.Fatal("truncated header must not be a core dump")
	}
	badOrder := elfHeader(false, 4)
	badOrder[5] = 9 // unknown EI_DATA
	p := writeCoreFile(t, dir, "core.1", badOrder)
	if isCoreDumpFile(p) {
		t.Fatal("unknown byte order must not be a core dump")
	}
}
