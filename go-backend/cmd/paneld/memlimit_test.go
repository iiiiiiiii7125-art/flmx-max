package main

import (
	"os"
	"path/filepath"
	runtimedebug "runtime/debug"
	"strconv"
	"testing"
)

// TestMemoryTierFor pins the panel copy of the shared tier table
// to the agent's numbers (go-gost/memlimit.go TestMemoryTierFor:
// soft rungs 179/317/358/512/2048/2048 for the same totals). The
// two faces share one sizing policy; the tables must never drift
// apart.
func TestMemoryTierFor(t *testing.T) {
	cases := []struct {
		totalMiB int64
		wantSoft int64
	}{
		{256, 179},  // small tier
		{453, 317},  // ECS1686's 464448 kB machine shape
		{512, 358},  // top of the small tier
		{1024, 512}, // proportional tier
		{8192, 2048},
		{16384, 2048}, // absolute soft-cap ceiling
	}
	for _, c := range cases {
		if got := memoryTierFor(c.totalMiB); got != c.wantSoft {
			t.Errorf("memoryTierFor(%d) = %d, want %d", c.totalMiB, got, c.wantSoft)
		}
	}
}

// TestSoftLimitBytes pins the decision rules: an explicit
// GOMEMLIMIT in the environment always wins, an unknown machine
// size installs nothing, and a known size maps to the tier's soft
// cap in bytes.
func TestSoftLimitBytes(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	if _, ok := softLimitBytes(env(map[string]string{"GOMEMLIMIT": "1GiB"}), 8192<<20); ok {
		t.Error("explicit GOMEMLIMIT must suppress the in-process cap")
	}
	if _, ok := softLimitBytes(env(nil), 0); ok {
		t.Error("unknown total memory must install no cap")
	}
	limit, ok := softLimitBytes(env(nil), int64(464448)*1024)
	if !ok {
		t.Fatal("known total memory must install a cap")
	}
	if want := int64(317) << 20; limit != want {
		t.Errorf("soft limit for 464448 kB = %d bytes, want %d (317MiB tier)", limit, want)
	}
}

// writeMeminfoFixture points meminfoPath at a temp file with the
// given MemTotal (kB) for the duration of the test.
func writeMeminfoFixture(t *testing.T, totalKB int64) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "meminfo")
	content := "MemTotal:       " + strconv.FormatInt(totalKB, 10) + " kB\nMemFree:         1024 kB\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	old := meminfoPath
	meminfoPath = path
	t.Cleanup(func() { meminfoPath = old })
}

func TestReadMemTotalBytes(t *testing.T) {
	writeMeminfoFixture(t, 464448)
	got, err := readMemTotalBytes()
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(464448) * 1024; got != want {
		t.Errorf("readMemTotalBytes = %d, want %d", got, want)
	}
}

// TestApplyMemoryLimit is the construct-level end-to-end check:
// with a small-machine meminfo fixture, applyMemoryLimit must
// actually move the runtime's memory limit to the tier value; with
// an explicit GOMEMLIMIT in the environment it must leave the
// runtime untouched.
func TestApplyMemoryLimit(t *testing.T) {
	prev := runtimedebug.SetMemoryLimit(-1) // negative input reads without changing
	t.Cleanup(func() { runtimedebug.SetMemoryLimit(prev) })

	writeMeminfoFixture(t, 464448)
	t.Setenv("GOMEMLIMIT", "")
	applyMemoryLimit()
	if got, want := runtimedebug.SetMemoryLimit(-1), int64(317)<<20; got != want {
		t.Errorf("after applyMemoryLimit, runtime limit = %d, want %d", got, want)
	}

	runtimedebug.SetMemoryLimit(prev)
	t.Setenv("GOMEMLIMIT", "900MiB")
	applyMemoryLimit()
	if got := runtimedebug.SetMemoryLimit(-1); got != prev {
		t.Errorf("explicit GOMEMLIMIT: runtime limit changed to %d, want untouched %d", got, prev)
	}
}
